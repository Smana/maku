// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutinesClientForwardsAndValidates(t *testing.T) {
	type recordedReq struct {
		method string
		path   string
		query  string
		auth   string
		ct     string
		idem   string
		body   string
	}
	var seen []recordedReq
	var nextStatus int
	var nextBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, recordedReq{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			auth:   r.Header.Get("Authorization"),
			ct:     r.Header.Get("Content-Type"),
			idem:   r.Header.Get("Idempotency-Key"),
			body:   string(raw),
		})
		if nextStatus != 0 {
			w.WriteHeader(nextStatus)
		}
		_, _ = w.Write([]byte(nextBody))
	}))
	defer srv.Close()

	f := Factory{URL: srv.URL, HC: srv.Client()}

	// Empty token fails before hitting HTTP.
	if _, err := f.ListRoutines(t.Context(), "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty token: got %v, want ErrForbidden", err)
	}
	// Invalid repo / routine fails before hitting HTTP.
	if _, err := f.ListRoutines(t.Context(), "tok", "../evil"); err == nil {
		t.Fatal("expected error for invalid repo filter")
	}
	if _, err := f.GetRoutine(t.Context(), "tok", "..", "maku", "dead-links"); err == nil {
		t.Fatal("expected error for invalid owner")
	}
	if _, _, err := f.RunRoutine(t.Context(), "tok", "Smana", "maku", "BAD_NAME", "k1", nil); err == nil {
		t.Fatal("expected error for invalid routine name")
	}
	if len(seen) != 0 {
		t.Fatalf("invalid inputs hit HTTP: %+v", seen)
	}

	// ListRoutines without filter.
	nextStatus = http.StatusOK
	nextBody = `{"routines":[]}`
	got, err := f.ListRoutines(t.Context(), "tok-1", "")
	if err != nil || string(got) != `{"routines":[]}` {
		t.Fatalf("ListRoutines = %s, err=%v", got, err)
	}
	if seen[0].method != http.MethodGet || seen[0].path != "/v1/routines" || seen[0].query != "" || seen[0].auth != "Bearer tok-1" {
		t.Fatalf("ListRoutines req = %+v", seen[0])
	}

	// ListRoutines with repo filter + >64 KiB response (e.g. 128 KiB).
	bigPad := strings.Repeat("a", 128<<10)
	nextBody = `{"routines":[{"routine":"` + bigPad + `"}]}`
	got, err = f.ListRoutines(t.Context(), "tok-1", "Smana/maku")
	if err != nil || len(got) < 128<<10 {
		t.Fatalf("large ListRoutines len=%d err=%v", len(got), err)
	}
	if seen[1].query != "repository=Smana%2Fmaku" {
		t.Fatalf("ListRoutines query = %q", seen[1].query)
	}

	// GetRoutine.
	nextBody = `{"routine":"dead-links"}`
	got, err = f.GetRoutine(t.Context(), "tok-2", "Smana", "maku", "dead-links")
	if err != nil || string(got) != `{"routine":"dead-links"}` {
		t.Fatalf("GetRoutine = %s err=%v", got, err)
	}
	if seen[2].path != "/v1/routines/Smana/maku/dead-links" {
		t.Fatalf("GetRoutine path = %q", seen[2].path)
	}

	// RunRoutine preserves 201 Created and forwards Idempotency-Key + body.
	nextStatus = http.StatusCreated
	nextBody = `{"task":"t1"}`
	status, got, err := f.RunRoutine(t.Context(), "tok-3", "Smana", "maku", "dead-links", "idem-1", json.RawMessage(`{"mode":"pr"}`))
	if err != nil || status != http.StatusCreated || string(got) != `{"task":"t1"}` {
		t.Fatalf("RunRoutine status=%d body=%s err=%v", status, got, err)
	}
	if seen[3].method != http.MethodPost || seen[3].path != "/v1/routines/Smana/maku/dead-links/runs" ||
		seen[3].idem != "idem-1" || seen[3].ct != "application/json" || seen[3].body != `{"mode":"pr"}` {
		t.Fatalf("RunRoutine req = %+v", seen[3])
	}

	// Error code with digit (mode_requires_phase2) is preserved, not clipped to "refused".
	nextStatus = http.StatusBadRequest
	nextBody = `{"error":"mode_requires_phase2"}`
	var ref ErrRefused
	if _, _, err := f.RunRoutine(t.Context(), "tok-3", "Smana", "maku", "dead-links", "idem-2", nil); !errors.As(err, &ref) || ref.Status != http.StatusBadRequest || ref.Reason != "mode_requires_phase2" {
		t.Fatalf("mode_requires_phase2 err = %v", err)
	}

	// Free-text error is clipped to "refused".
	nextStatus = http.StatusConflict
	nextBody = `{"error":"https://evil.example/leak"}`
	if _, _, err := f.RunRoutine(t.Context(), "tok-3", "Smana", "maku", "dead-links", "idem-3", nil); !errors.As(err, &ref) || ref.Reason != "refused" {
		t.Fatalf("free text err = %v", err)
	}

	// Invalid JSON on 200 is rejected.
	nextStatus = http.StatusOK
	nextBody = `{not-json`
	if _, err := f.GetRoutine(t.Context(), "tok-2", "Smana", "maku", "dead-links"); err == nil {
		t.Fatal("expected invalid json error")
	}

	// Pause & Resume (including 204 No Content handling).
	nextStatus = http.StatusOK
	nextBody = `{"paused":true}`
	if got, err := f.PauseRoutine(t.Context(), "tok-4", "Smana", "maku", "dead-links", json.RawMessage(`{"reason":"hold"}`)); err != nil || string(got) != `{"paused":true}` {
		t.Fatalf("PauseRoutine = %s err=%v", got, err)
	}
	nextStatus = http.StatusNoContent
	nextBody = ""
	if got, err := f.ResumeRoutine(t.Context(), "tok-4", "Smana", "maku", "dead-links", nil); err != nil || got != nil {
		t.Fatalf("ResumeRoutine 204 = %s err=%v", got, err)
	}
}
