// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPolicyClientReadsRecentAndTimeline(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/policy/repositories/Smana/a/decisions":
			_, _ = w.Write([]byte(`{"repository":"Smana/a","since":"2026-11-01","decisions":[]}`))
		case "/api/policy/repositories/Smana/a/decisions/42":
			_, _ = w.Write([]byte(`{"repository":"Smana/a","pullRequest":42,"decisions":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, HC: srv.Client(), Token: func(context.Context) (string, error) { return "tok", nil }}
	recent, err := c.PolicyRecent(t.Context(), "Smana/a", "2026-11-01")
	if err != nil || recent.Repository != "Smana/a" || recent.Since != "2026-11-01" {
		t.Fatalf("%+v %v", recent, err)
	}
	timeline, err := c.PolicyTimeline(t.Context(), "Smana/a", 42)
	if err != nil || timeline.PullRequest != 42 {
		t.Fatalf("%+v %v", timeline, err)
	}
	want := []string{"/api/policy/repositories/Smana/a/decisions?since=2026-11-01", "/api/policy/repositories/Smana/a/decisions/42"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths %q, want %q", paths, want)
		}
	}
}
