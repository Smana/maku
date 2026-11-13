// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Smana/maku/internal/envelope"
)

// v4 D3, step 7 R1: GET /v1/install is the factory's part of the install banner, for any
// signed-in human: private_code_unacknowledged while a served repository is private code without
// an acknowledgement. It names no repository: the code is the whole answer.
func TestInstallRoute(t *testing.T) {
	unack := false
	s := &Server{Cfg: sponsoredCfg(), InstallUnsafe: func() []string {
		if unack {
			return []string{InstallPrivateCodeUnacknowledged}
		}
		return nil
	}, Auth: byToken{
		"dev":   {Kind: envelope.ActorHuman, ID: "human:2", Sub: "2", Groups: []string{"agents"}},
		"sys":   {Kind: envelope.ActorSystem, ID: "system:broker"},
		"slack": {Kind: envelope.ActorHuman, ID: "human:3", Sub: "3", Sponsored: true},
	}, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	get := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, InstallRoute[len("GET "):], nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := get("dev"); w.Code != http.StatusOK || w.Body.String() != "{\"unsafe\":[]}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	unack = true
	if w := get("dev"); w.Code != http.StatusOK || w.Body.String() != "{\"unsafe\":[\"private_code_unacknowledged\"]}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for token, code := range map[string]int{"sys": http.StatusForbidden, "slack": http.StatusForbidden, "nobody": http.StatusUnauthorized} {
		if w := get(token); w.Code != code {
			t.Errorf("%s: %d, want %d", token, w.Code, code)
		}
	}
	s.InstallUnsafe = nil
	if w := get("dev"); w.Code != http.StatusOK || w.Body.String() != "{\"unsafe\":[]}\n" {
		t.Fatalf("nil lists none: %d %s", w.Code, w.Body)
	}
}
