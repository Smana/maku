// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serve(t *testing.T, h http.Handler) (int, Component) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/preflight", nil))
	var c Component
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, c
}

// IN3: GET /preflight runs the component's checks on demand and caches the answer 30 s, so a
// scrape storm costs one round of checks.
func TestHandlerCaches(t *testing.T) {
	var calls atomic.Int32
	clock := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	src := func(context.Context) []Result {
		calls.Add(1)
		return []Result{{Check: "database", Outcome: Pass}}
	}
	h := Handler("broker", "v1", src, func() time.Time { return clock })
	code, c := serve(t, h)
	if code != http.StatusOK || c.Component != "broker" || c.Version != "v1" || len(c.Checks) != 1 || c.Checks[0].Check != "database" {
		t.Fatalf("%d %+v", code, c)
	}
	serve(t, h)
	if calls.Load() != 1 {
		t.Fatalf("a second call inside 30 s ran the checks again: %d", calls.Load())
	}
	clock = clock.Add(31 * time.Second)
	serve(t, h)
	if calls.Load() != 2 {
		t.Fatalf("past 30 s the checks did not run again: %d", calls.Load())
	}
}

// No value leaves the endpoint: a result whose message carries a credential becomes an undecided
// result naming the check only, and an invalid code becomes undecided.
func TestHandlerWithholdsCredentials(t *testing.T) {
	src := func(context.Context) []Result {
		return []Result{{Check: "database", Outcome: Fail, Code: "db_unreachable", Message: "dial postgresql://rooms_broker:hunter2@db/rooms"}}
	}
	_, c := serve(t, Handler("broker", "v1", src, time.Now))
	if len(c.Checks) != 1 || c.Checks[0].Code != Undecided || strings.Contains(c.Checks[0].Message, "hunter2") ||
		strings.Contains(c.Checks[0].Message, "postgresql://") {
		t.Fatalf("%+v", c.Checks)
	}
}

// All checks are bounded together: a check that hangs past the bound returns the others, with the
// hung one undecided, before the ops server's write deadline.
func TestBounded(t *testing.T) {
	r := Bounded(t.Context(), 50*time.Millisecond, "knowledge", func(ctx context.Context) Result {
		<-ctx.Done()
		return Result{Check: "knowledge", Outcome: Pass}
	})
	if r.Outcome != Fail || r.Code != Undecided || !strings.Contains(r.Message, "did not finish") {
		t.Fatalf("%+v", r)
	}
	r = Bounded(t.Context(), time.Second, "knowledge", func(context.Context) Result {
		return Result{Check: "knowledge", Outcome: Pass}
	})
	if r.Outcome != Pass || r.Seconds <= 0 {
		t.Fatalf("%+v", r)
	}
}
