// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/store"
)

// SEC-M12: per-principal rate limits on /api/rooms/{id}/events and unauthenticated /link/github/callback.
func TestEventsAndGitHubCallbackRateLimited(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Now = func() time.Time { return now }
	})
	// Burst of 20 allowed, 21st gets 429 on /api/rooms/{id}/events.
	for i := range 20 {
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/events", header("dev1")); code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, code)
		}
	}
	if code, _ := get(t, e, "/api/rooms/"+roomID+"/events", header("dev1")); code != http.StatusTooManyRequests {
		t.Fatalf("21st events request: got %d, want 429", code)
	}

	// Unauthenticated GET /link/github/callback rate-limits per remote host.
	s := &Server{Now: func() time.Time { return now }, GitHubLink: &GitHubLinker{}}
	for i := range 20 {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/link/github/callback?state=bad", nil)
		r.RemoteAddr = "198.51.100.7:12345"
		rec := httptest.NewRecorder()
		s.githubCallback(rec, r)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("callback %d unexpectedly rate-limited", i+1)
		}
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/link/github/callback?state=bad", nil)
	r.RemoteAddr = "198.51.100.7:54321"
	rec := httptest.NewRecorder()
	s.githubCallback(rec, r)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("21st callback from same host: got %d, want 429", rec.Code)
	}
}

// REL-M14: factoryCodes does not hold installCache.mu across I/O and does not negative-cache context.Canceled.
func TestFactoryCodesDoesNotHoldLockOrCacheCancel(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s := &Server{
		Now: func() time.Time { return now },
		FactoryInstall: func(ctx context.Context, _ string) ([]string, error) {
			n := calls.Add(1)
			if n == 1 {
				close(started)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []string{"ok"}, nil
				}
			}
			return []string{"unsandboxed"}, nil
		},
	}

	// First caller cancels its context while FactoryInstall is in flight.
	cctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.factoryCodes(cctx, "tok")
	}()
	<-started

	// Second concurrent caller must NOT block on installCache.mu while the first call is in flight.
	gotCh := make(chan []string, 1)
	go func() {
		gotCh <- s.factoryCodes(t.Context(), "tok")
	}()
	select {
	case got := <-gotCh:
		if len(got) != 1 || got[0] != "unsandboxed" {
			t.Fatalf("concurrent caller got %v, want [unsandboxed]", got)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("concurrent factoryCodes blocked while another call was in flight")
	}

	// Now cancel the first caller and verify it does not overwrite or poison the cache.
	cancel()
	wg.Wait()
	if got := s.factoryCodes(t.Context(), "tok"); len(got) != 1 || got[0] != "unsandboxed" {
		t.Fatalf("after canceled call, cache = %v, want [unsandboxed]", got)
	}

	// Separate server: a canceled first call must not negative-cache nil for installTTL.
	var n2 atomic.Int32
	s2 := &Server{
		Now: func() time.Time { return now },
		FactoryInstall: func(_ context.Context, _ string) ([]string, error) {
			if n2.Add(1) == 1 {
				return nil, context.Canceled
			}
			return []string{"fresh"}, nil
		},
	}
	if got := s2.factoryCodes(t.Context(), "tok"); got != nil {
		t.Fatalf("canceled call returned %v, want nil", got)
	}
	if got := s2.factoryCodes(t.Context(), "tok"); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("subsequent call after cancel got %v, want [fresh]", got)
	}
}

// REL-M14: takeBounded sweeps idle entries at maxLimiterEntries and fails closed when all are busy.
func TestTakeBoundedSweepsIdleAndCapsSize(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	var m map[string]*rate.Limiter
	for i := range maxLimiterEntries {
		if !takeBounded(&m, fmt.Sprintf("u%d", i), 10, 20, now) {
			t.Fatalf("entry %d refused under cap", i)
		}
	}
	// At cap with all entries just used at `now`, a new key is refused (fail closed).
	if takeBounded(&m, "overflow", 10, 20, now) {
		t.Fatal("expected fail-closed at cap when all limiters are busy")
	}
	// After buckets refill (1s later), idle entries are swept and the new key succeeds.
	if !takeBounded(&m, "overflow", 10, 20, now.Add(time.Second)) {
		t.Fatal("expected sweep of idle limiters to admit new key")
	}
	if len(m) > maxLimiterEntries {
		t.Fatalf("map grew past cap: %d > %d", len(m), maxLimiterEntries)
	}
}

// PERF-M4: lastScope does not re-read [prevStart..LastSeq] when widening its window,
// and closedRoom bounds its forward scan on a huge non-factory sealed room.
func TestLastScopeNoRedundantRescanAndClosedRoomBounded(t *testing.T) {
	// Build a room with 1500 events where the only handoff with scope is at seq 10.
	const total = 1500
	evs := make([]envelope.Event, total)
	for i := range total {
		seq := int64(i + 1)
		evs[i] = envelope.Event{V: 1, Seq: seq, RoomID: roomID, Type: envelope.Message}
		if seq == 10 {
			evs[i].Type = envelope.Handoff
			evs[i].Payload = envelope.Must(envelope.HandoffPayload{
				Scope: &envelope.Scope{State: envelope.ScopeReady, Task: "fix it"},
			})
		}
	}
	readCounts := map[int64]int{}
	log := &countingRangeLog{
		memLog: &memLog{evs: evs, sealed: true},
		onRead: func(_ int64, got []envelope.Event) {
			for _, e := range got {
				readCounts[e.Seq]++
			}
		},
	}
	s := &Server{Log: log, FactoryPrincipal: "system:factory"}
	rm := &v1alpha1.Room{ObjectMeta: metav1.ObjectMeta{Name: roomID}}
	scope, err := s.lastScope(t.Context(), rm, store.RoomState{ID: roomID, LastSeq: total})
	if err != nil || scope == nil || scope.Task != "fix it" {
		t.Fatalf("lastScope = %+v, %v", scope, err)
	}
	for seq, n := range readCounts {
		if n > 1 {
			t.Fatalf("seq %d read %d times during lastScope widening (want 1)", seq, n)
		}
	}

	// closedRoom on a 1500-event sealed room with no factory task facts must stop after
	// closedMaxScan + one tail page rather than scanning all 1500 events.
	var rangeCalls int
	log.onRead = func(_ int64, _ []envelope.Event) { rangeCalls++ }
	gotRoom, err := s.closedRoom(t.Context(), roomID)
	if err != nil || gotRoom != nil {
		t.Fatalf("closedRoom = %+v, %v, want nil, nil", gotRoom, err)
	}
	maxCalls := closedMaxScan/closedPage + 1
	if rangeCalls > maxCalls {
		t.Fatalf("closedRoom made %d Range calls, want <= %d", rangeCalls, maxCalls)
	}
	// If the factory's task facts sit at the tail (seq 1499), closedRoom still finds them.
	evs[total-2] = envelope.Event{
		V: 1, Seq: total - 1, RoomID: roomID, Type: envelope.StateChanged,
		Actor:  envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"},
		Origin: envelope.OriginClient,
		Payload: envelope.TaskStatePayload(envelope.TaskFacts{
			Phase: "Done",
			Issue: &envelope.IssueFact{Number: 1, URL: "https://github.com/Smana/tail-repo/issues/1"},
		}),
	}
	gotRoom, err = s.closedRoom(t.Context(), roomID)
	if err != nil || gotRoom == nil || gotRoom.Spec.Repository != "Smana/tail-repo" {
		t.Fatalf("closedRoom tail lookup = %+v, %v", gotRoom, err)
	}
}

type countingRangeLog struct {
	*memLog
	onRead func(after int64, got []envelope.Event)
}

func (l *countingRangeLog) Range(ctx context.Context, id string, after int64, limit int) ([]envelope.Event, error) {
	got, err := l.memLog.Range(ctx, id, after, limit)
	if err == nil && l.onRead != nil {
		l.onRead(after, got)
	}
	return got, err
}
