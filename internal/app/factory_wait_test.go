// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/configsource"
)

func probe(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

// While no config can be served the factory waits: alive and started, never ready, and /readyz
// says why. Once a Sync serves, the start goes on and readiness is the caches' again.
func TestWaitServableRetriesUntilServed(t *testing.T) {
	gate := &startupGate{}
	h := gate.handler(factoryOps(gate.Synced, func() bool { return false }, http.NotFoundHandler()))
	notServable := fmt.Errorf("%w: config: repository is required", configsource.ErrNotServable)
	calls := 0
	sync := func(context.Context) error {
		calls++
		if calls <= 3 {
			// Each failed attempt is probed before the next one: alive, started, not ready.
			if calls > 1 {
				for path, want := range map[string]int{"/healthz": 200, "/startupz": 200, "/readyz": 503} {
					if code, _ := probe(t, h, path); code != want {
						t.Errorf("attempt %d: GET %s = %d, want %d", calls, path, code, want)
					}
				}
				if _, body := probe(t, h, "/readyz"); !strings.Contains(body, "waiting for a servable config") ||
					!strings.Contains(body, "repository is required") {
					t.Errorf("/readyz must name the wait and its reason: %q", body)
				}
			}
			return notServable
		}
		return nil
	}
	if err := waitServable(t.Context(), slog.New(slog.DiscardHandler), gate, sync, nil, time.Millisecond, 4*time.Millisecond); err != nil {
		t.Fatalf("a Sync that serves ends the wait: %v", err)
	}
	if calls != 4 {
		t.Fatalf("Sync ran %d times, want 4", calls)
	}
	if code, body := probe(t, h, "/readyz"); code != 503 || strings.Contains(body, "servable") {
		t.Fatalf("served, before the caches synced: /readyz = %d %q, want 503 watch not synced", code, body)
	}
	gate.serve(func(context.Context) bool { return true })
	if code, _ := probe(t, h, "/readyz"); code != 200 {
		t.Fatalf("caches synced: /readyz = %d, want 200", code)
	}
}

// Any other error, a Forbidden list or an unreachable API server, fails the start at once.
func TestWaitServableOtherErrorFailsAtOnce(t *testing.T) {
	boom := errors.New("list Factory: forbidden")
	calls := 0
	err := waitServable(t.Context(), slog.New(slog.DiscardHandler), &startupGate{},
		func(context.Context) error { calls++; return boom }, nil, time.Hour, time.Hour)
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err %v after %d calls, want %v after 1", err, calls, boom)
	}
}

// SIGTERM during the wait ends it, with errStoppedWaiting so the start exits cleanly.
func TestWaitServableStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- waitServable(ctx, slog.New(slog.DiscardHandler), &startupGate{}, func(context.Context) error {
			return configsource.ErrNotServable
		}, func() []string { return []string{"Repository a: Invalid"} }, time.Hour, time.Hour)
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errStoppedWaiting) {
			t.Fatalf("err %v, want errStoppedWaiting", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait ignored the cancel")
	}
}
