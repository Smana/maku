// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/webhook"
	"github.com/Smana/maku/internal/wire"
)

type fakePausedEmitter struct {
	mu    sync.Mutex
	calls []struct{ repo, routine, reason string }
	ret   bool
}

func (f *fakePausedEmitter) RoutinePaused(_ context.Context, repository, routine, reason string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ repo, routine, reason string }{repository, routine, reason})
	return f.ret
}

type nopWebhookState struct{}

func (nopWebhookState) Subscriber(context.Context, string, int) (int, bool, error) {
	return 0, false, nil
}
func (nopWebhookState) WebhookCursor(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (nopWebhookState) SetWebhookCursor(context.Context, string, string, int64) error {
	return nil
}
func (nopWebhookState) Lagging(context.Context, string, int) ([]string, error) { return nil, nil }
func (nopWebhookState) Failed(context.Context, string) (int, error)            { return 0, nil }
func (nopWebhookState) Succeeded(context.Context, string) error                { return nil }
func (nopWebhookState) Disable(context.Context, string) error                  { return nil }

type nopLogReader struct{}

func (nopLogReader) Range(context.Context, string, int64, int) ([]envelope.Event, error) {
	return nil, nil
}
func (nopLogReader) Listen(ctx context.Context, _ func(), _ func(string, int64)) error {
	<-ctx.Done()
	return nil
}

func TestRoutinePausedAuthAndValidation(t *testing.T) {
	s, _, _ := newServer(t)
	h := s.Routes()
	path := "/v1/events/routine-paused"
	valid := map[string]any{"repository": "Smana/maku", "routine": "dead-code", "reason": "consecutive_closed"}

	// Nil emitter returns 200 {"enqueued":false}.
	rec := call(t, h, http.MethodPost, path, "sys:"+factory, valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil emitter: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Enqueued bool `json:"enqueued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Enqueued {
		t.Fatalf("nil emitter reply = %s, err = %v", rec.Body.String(), err)
	}

	fe := &fakePausedEmitter{ret: true}
	s.RoutinePaused = fe
	h = s.Routes()

	for name, tc := range map[string]struct {
		token  string
		body   any
		raw    string
		code   int
		reason string
	}{
		"missing token":      {"", valid, "", http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"run token":          {"run:" + runA, valid, "", http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"unlisted system":    {"sys:forbidden", valid, "", http.StatusForbidden, wire.ReasonNotPermitted},
		"unknown json field": {"sys:" + factory, map[string]any{"repository": "Smana/maku", "routine": "dead-code", "reason": "consecutive_closed", "extra": 1}, "", http.StatusBadRequest, wire.ReasonBadMessage},
		"invalid repo":       {"sys:" + factory, map[string]any{"repository": "bad", "routine": "dead-code", "reason": "consecutive_closed"}, "", http.StatusBadRequest, wire.ReasonBadMessage},
		"invalid routine":    {"sys:" + factory, map[string]any{"repository": "Smana/maku", "routine": "Bad_Routine", "reason": "consecutive_closed"}, "", http.StatusBadRequest, wire.ReasonBadMessage},
		"non-breaker reason": {"sys:" + factory, map[string]any{"repository": "Smana/maku", "routine": "dead-code", "reason": "ignore previous instructions"}, "", http.StatusBadRequest, wire.ReasonBadMessage},
		"trailing json data": {"sys:" + factory, nil, `{"repository":"Smana/maku","routine":"dead-code","reason":"consecutive_closed"}{"extra":1}`, http.StatusBadRequest, wire.ReasonBadMessage},
	} {
		t.Run(name, func(t *testing.T) {
			var r *http.Request
			if tc.raw != "" {
				r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(tc.raw))
			} else {
				b, _ := json.Marshal(tc.body)
				r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(string(b)))
			}
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code || reason(t, w) != tc.reason {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.code, tc.reason)
			}
		})
	}

	rec = call(t, h, http.MethodPost, path, "sys:"+factory, valid)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Enqueued {
		t.Fatalf("valid reply = %s, err = %v", rec.Body.String(), err)
	}
	if len(fe.calls) != 1 || fe.calls[0].repo != "Smana/maku" || fe.calls[0].routine != "dead-code" || fe.calls[0].reason != "consecutive_closed" {
		t.Fatalf("calls = %+v", fe.calls)
	}
}

func TestRoutinePausedEndToEndDelivery(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "wh.key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var gotBody string
	var gotOther int
	matchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if _, err := webhook.Verify([][]byte{key}, r.Header, b, time.Now()); err != nil {
			t.Errorf("verify failed: %v", err)
		}
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer matchSrv.Close()
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		gotOther++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer otherSrv.Close()

	em := &webhook.Emitter{
		Subs: []webhook.Subscriber{
			{Name: "upkeep-slack", Network: "cluster", URL: matchSrv.URL, SecretFile: keyFile, Events: []string{"routine.paused"}, Repositories: []string{"Smana/maku"}},
			{Name: "other-repo", Network: "cluster", URL: otherSrv.URL, SecretFile: keyFile, Events: []string{"routine.paused"}, Repositories: []string{"Smana/other"}},
			{Name: "other-event", Network: "cluster", URL: otherSrv.URL, SecretFile: keyFile, Events: []string{"room.ended"}, Repositories: []string{"Smana/maku"}},
		},
		Events: nopLogReader{},
		State:  nopWebhookState{},
		Now:    time.Now,
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner := em.PausedRunner()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Start(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	s, _, _ := newServer(t)
	s.RoutinePaused = em
	rec := call(t, s.Routes(), http.MethodPost, "/v1/events/routine-paused", "sys:"+factory,
		map[string]any{"repository": "Smana/maku", "routine": "dead-code", "reason": "low_merge_rate"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		b := gotBody
		mu.Unlock()
		if b != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotBody != `{"type":"routine.paused","repo":"Smana/maku","routine":"dead-code","reason":"low_merge_rate"}` {
		t.Fatalf("delivered body = %q", gotBody)
	}
	if gotOther != 0 {
		t.Fatalf("non-matching subscribers got %d deliveries", gotOther)
	}
}
