// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstallUnsafeIsShownToEverySignedInHuman(t *testing.T) {
	for name, c := range map[string]struct {
		unsafe   []string
		user     string
		code     int
		wantBody string
	}{
		"listed":          {[]string{"unsafe_allow_runc", "network_policy_none"}, "alice", http.StatusOK, `{"unsafe":["unsafe_allow_runc","network_policy_none"]}`},
		"unset is empty":  {nil, "alice", http.StatusOK, `{"unsafe":[]}`},
		"unauthenticated": {[]string{"unsafe_allow_runc"}, "", http.StatusUnauthorized, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := &Server{Humans: headerAuth{}, Groups: groups, Unsafe: c.unsafe}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
			if c.user != "" {
				req.Header.Set("X-Test-User", c.user)
			}
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, c.code)
			}
			if c.wantBody != "" && strings.TrimSpace(rec.Body.String()) != c.wantBody {
				t.Fatalf("body %s, want %s", rec.Body, c.wantBody)
			}
		})
	}
}

// v4 D3, step 7 R1: the factory's banner code joins the chart's, read with the caller's own token.
// A failed read, a code outside the factory's set, or no token shows the chart's codes alone.
func TestInstallJoinsTheFactorysCodes(t *testing.T) {
	for name, c := range map[string]struct {
		factory  []string
		err      error
		wantBody string
	}{
		"private code":        {[]string{"private_code_unacknowledged"}, nil, `{"unsafe":["unsafe_allow_runc","private_code_unacknowledged"]}`},
		"none":                {nil, nil, `{"unsafe":["unsafe_allow_runc"]}`},
		"unreadable":          {nil, errors.New("runrequest: factory: 502"), `{"unsafe":["unsafe_allow_runc"]}`},
		"not the factory's":   {[]string{"network_policy_none", "bogus"}, nil, `{"unsafe":["unsafe_allow_runc"]}`},
		"listed by the chart": {[]string{"private_code_unacknowledged", "private_code_unacknowledged"}, nil, `{"unsafe":["unsafe_allow_runc","private_code_unacknowledged"]}`},
	} {
		t.Run(name, func(t *testing.T) {
			var seen string
			srv := &Server{Humans: tokenAuth{}, Groups: groups, Unsafe: []string{"unsafe_allow_runc"},
				FactoryInstall: func(_ context.Context, token string) ([]string, error) { seen = token; return c.factory, c.err }}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
			req.Header.Set("X-Test-User", "alice")
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != c.wantBody || seen != "tok-alice" {
				t.Fatalf("%d %s (token %q), want %s", rec.Code, rec.Body, seen, c.wantBody)
			}
		})
	}
	called := false
	srv := &Server{Humans: headerAuth{}, Groups: groups, FactoryInstall: func(context.Context, string) ([]string, error) { called = true; return nil, nil }}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
	req.Header.Set("X-Test-User", "alice")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if called || strings.TrimSpace(rec.Body.String()) != `{"unsafe":[]}` {
		t.Fatalf("no token reads nothing: called %v, %s", called, rec.Body)
	}
}
