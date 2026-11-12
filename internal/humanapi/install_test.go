// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
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
