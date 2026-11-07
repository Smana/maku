// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/runwatch"
)

// tokens is a RunVerifier that knows a fixed token per subject, for the room tools audience only.
type tokens struct {
	subs map[string]string
	down bool
	aud  *[]string
}

func (v tokens) Verify(_ context.Context, raw, audience string) (*authn.Claims, error) {
	*v.aud = append(*v.aud, audience)
	if v.down {
		return nil, fmt.Errorf("%w: test", authn.ErrReviewUnavailable)
	}
	if audience != authn.AudienceRoomTools {
		return nil, authn.ErrWrongAudience
	}
	s, ok := v.subs[raw]
	if !ok {
		return nil, fmt.Errorf("%w: unknown", authn.ErrWrongIssuer)
	}
	return &authn.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: s}}, nil
}

// Task 6: in tokenreview mode :8090 verifies the caller's own bearer token, and its subject alone
// names the run: no key is read, and the gateway's headers, forged or not, are ignored.
func TestTokenReviewModeVerifiesTheRunsOwnToken(t *testing.T) {
	w := runwatch.New(runwatch.DefaultKind)
	w.Upsert(t.Context(), claim("7f3cq2xz", "reviewer", "3kq7x2ma", "Running", nil))
	w.Upsert(t.Context(), claim("done2345", "reviewer", "3kq7x2ma", "Succeeded", nil))
	var aud []string
	v := tokens{aud: &aud, subs: map[string]string{
		"live":    sub,
		"done":    "system:serviceaccount:agents:xplane-run-done2345",
		"someone": "system:serviceaccount:agents:someone",
	}}
	s, rejected := testServer(w, newClock())
	s.Key, s.Tokens = nil, v
	down, _ := testServer(w, newClock())
	down.Key, down.Tokens = nil, tokens{aud: &aud, down: true}

	for _, tc := range []struct {
		name          string
		s             *Server
		authorization string
		key, identity string
		status        int
		rejectAs      string
	}{
		{"the run's own token", s, "Bearer live", "", "", http.StatusOK, ""},
		{"no token", s, "", "", "", http.StatusUnauthorized, "mcp_key"},
		{"not a bearer", s, "Basic live", "", "", http.StatusUnauthorized, "mcp_key"},
		{"a token the cluster does not vouch for", s, "Bearer forged", "", "", http.StatusUnauthorized, "mcp_key"},
		{"the gateway's key and subject are not a credential here", s, "", "k", sub, http.StatusUnauthorized, "mcp_key"},
		{"a forged subject header beside a token is ignored", s, "Bearer someone", "", sub, http.StatusForbidden, "mcp_identity"},
		{"a verified subject that names no run", s, "Bearer someone", "", "", http.StatusForbidden, "mcp_identity"},
		{"a finished run", s, "Bearer done", "", "", http.StatusForbidden, "run_not_live"},
		{"the apiserver unreachable", down, "Bearer live", "", "", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*rejected = nil
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(body))
			if tc.authorization != "" {
				r.Header.Set("Authorization", tc.authorization)
			}
			if tc.key != "" {
				r.Header.Set(KeyHeader, tc.key)
			}
			if tc.identity != "" {
				r.Header.Set(IdentityHeader, tc.identity)
			}
			rec := httptest.NewRecorder()
			tc.s.ServeHTTP(rec, r)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.Bytes())
			}
			if tc.s == s && tc.rejectAs != "" && (len(*rejected) != 1 || (*rejected)[0] != tc.rejectAs) {
				t.Fatalf("rejected = %v, want [%s]", *rejected, tc.rejectAs)
			}
		})
	}
	for _, a := range aud {
		if a != authn.AudienceRoomTools {
			t.Fatalf("verified for audience %q, want %q only", a, authn.AudienceRoomTools)
		}
	}
}
