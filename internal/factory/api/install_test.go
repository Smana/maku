// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// v4 D3, step 7 R1: GET /v1/install is the factory's part of the install banner and bounded
// environment metadata, for any signed-in human: private_code_unacknowledged while a served
// repository is private code without an acknowledgement. It names no repository.
func TestInstallRoute(t *testing.T) {
	unack := false
	cfg := sponsoredCfg()
	cfg.Factory = "gcp-0"
	cfg.Defaults.Harness = config.HarnessOpenCode
	cfg.PolicyManagement.Stage = config.StageSimulate
	cfg.Hardening.Mode = hardening.ModeEnforce
	cfg.Routines.UIWrite = true
	s := &Server{Cfg: cfg, Namespace: "agent-system", InstallUnsafe: func() []string {
		if unack {
			return []string{InstallPrivateCodeUnacknowledged}
		}
		return nil
	}, Auth: byToken{
		"dev":    {Kind: envelope.ActorHuman, ID: "human:2", Sub: "2", Groups: []string{"agents"}},
		"sys":    {Kind: envelope.ActorSystem, ID: "system:broker"},
		"syssub": {Kind: envelope.ActorSystem, ID: "system:broker", Sub: "system:serviceaccount:agent-system:broker"},
		"slack":  {Kind: envelope.ActorHuman, ID: "human:3", Sub: "3", Sponsored: true},
	}, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	get := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, InstallRoute[len("GET "):], nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	decode := func(t *testing.T, body string) installResponse {
		t.Helper()
		var out installResponse
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		return out
	}

	w := get("dev")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unsafe":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "cloud-native-ref") {
		t.Fatalf("install must never name a repository: %s", w.Body)
	}
	got := decode(t, w.Body.String())
	if len(got.Unsafe) != 0 || got.Version != version.Version || got.Factory != "gcp-0" ||
		got.Namespace != "agent-system" || got.DefaultHarness != "opencode" ||
		got.PolicyStage != "simulate" || got.Hardening != "enforce" ||
		got.RoutinesUIWrite == nil || !*got.RoutinesUIWrite || got.Demo {
		t.Fatalf("unexpected install payload: %+v (%s)", got, w.Body)
	}

	unack = true
	w = get("dev")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := decode(t, w.Body.String()); !slices.Equal(got.Unsafe, []string{InstallPrivateCodeUnacknowledged}) {
		t.Fatalf("unsafe %v, want [%s]", got.Unsafe, InstallPrivateCodeUnacknowledged)
	}

	for token, code := range map[string]int{
		"sys":    http.StatusForbidden,
		"syssub": http.StatusForbidden,
		"slack":  http.StatusForbidden,
		"nobody": http.StatusUnauthorized,
	} {
		if w := get(token); w.Code != code {
			t.Errorf("%s: %d, want %d", token, w.Code, code)
		}
	}

	s.InstallUnsafe = nil
	s.Cfg = nil
	w = get("dev")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unsafe":[]`) {
		t.Fatalf("nil lists none: %d %s", w.Code, w.Body)
	}
	if got := decode(t, w.Body.String()); got.Version != version.Version || got.Factory != "" || got.RoutinesUIWrite != nil {
		t.Fatalf("nil cfg: %+v", got)
	}
}
