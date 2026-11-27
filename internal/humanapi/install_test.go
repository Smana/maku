// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/version"
)

type decodedInstall struct {
	Unsafe      []string `json:"unsafe"`
	Version     string   `json:"version"`
	Environment *struct {
		Broker struct {
			Version   string `json:"version"`
			Namespace string `json:"namespace"`
			AuthMode  string `json:"authMode"`
		} `json:"broker"`
		Factory *struct {
			Reachable       bool   `json:"reachable"`
			Version         string `json:"version"`
			Name            string `json:"name"`
			Namespace       string `json:"namespace"`
			DefaultHarness  string `json:"defaultHarness"`
			PolicyStage     string `json:"policyStage"`
			Hardening       string `json:"hardening"`
			RoutinesUIWrite *bool  `json:"routinesUIWrite"`
			Demo            bool   `json:"demo"`
		} `json:"factory"`
	} `json:"environment"`
}

func decodeInstall(t *testing.T, body string) decodedInstall {
	t.Helper()
	var out decodedInstall
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func TestInstallUnsafeIsShownToEverySignedInHuman(t *testing.T) {
	for name, c := range map[string]struct {
		unsafe    []string
		namespace string
		humanMode string
		user      string
		code      int
		wantCodes []string
		wantAuth  string
	}{
		"listed":          {[]string{"unsafe_allow_runc", "network_policy_none"}, "agent-system", "local", "alice", http.StatusOK, []string{"unsafe_allow_runc", "network_policy_none"}, "local"},
		"unset is empty":  {nil, "", "", "alice", http.StatusOK, []string{}, "oidc"},
		"unauthenticated": {[]string{"unsafe_allow_runc"}, "agent-system", "", "", http.StatusUnauthorized, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := &Server{Humans: headerAuth{}, Groups: groups, Unsafe: c.unsafe, Namespace: c.namespace, HumanMode: c.humanMode}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
			if c.user != "" {
				req.Header.Set("X-Test-User", c.user)
			}
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, c.code)
			}
			if c.code != http.StatusOK {
				return
			}
			if len(c.wantCodes) == 0 && !strings.Contains(rec.Body.String(), `"unsafe":[]`) {
				t.Fatalf("empty unsafe must encode as [], got %s", rec.Body)
			}
			got := decodeInstall(t, rec.Body.String())
			if !slices.Equal(got.Unsafe, c.wantCodes) {
				t.Fatalf("unsafe %v, want %v", got.Unsafe, c.wantCodes)
			}
			if got.Version != version.Version {
				t.Fatalf("version %q, want %q", got.Version, version.Version)
			}
			if got.Environment == nil {
				t.Fatalf("environment missing in %s", rec.Body)
			}
			if got.Environment.Broker.Version != version.Version || got.Environment.Broker.Namespace != c.namespace || got.Environment.Broker.AuthMode != c.wantAuth {
				t.Fatalf("broker env %+v", got.Environment.Broker)
			}
			if got.Environment.Factory != nil {
				t.Fatalf("unexpected factory env without FactoryInstall: %+v", got.Environment.Factory)
			}
		})
	}
}

// v4 D3, step 7 R1: the factory's banner code and environment metadata join the chart's, read with
// the caller's own token. A failed read, a code outside the factory's set, or no token shows the
// chart's codes alone.
func TestInstallJoinsTheFactorysCodes(t *testing.T) {
	uiWrite := true
	for name, c := range map[string]struct {
		factory       runrequest.InstallInfo
		err           error
		wantCodes     []string
		wantReachable bool
	}{
		"private code": {
			factory: runrequest.InstallInfo{
				Unsafe:          []string{"private_code_unacknowledged"},
				Version:         "v0.58.0",
				Factory:         "gcp-0",
				Namespace:       "agent-system",
				DefaultHarness:  "opencode",
				PolicyStage:     "manage",
				Hardening:       "enforce",
				RoutinesUIWrite: &uiWrite,
			},
			wantCodes:     []string{"unsafe_allow_runc", "private_code_unacknowledged"},
			wantReachable: true,
		},
		"none":                {runrequest.InstallInfo{Version: "v0.58.0"}, nil, []string{"unsafe_allow_runc"}, true},
		"unreadable":          {runrequest.InstallInfo{}, errors.New("runrequest: factory: 502"), []string{"unsafe_allow_runc"}, false},
		"not the factory's":   {runrequest.InstallInfo{Unsafe: []string{"network_policy_none", "bogus"}}, nil, []string{"unsafe_allow_runc"}, true},
		"listed by the chart": {runrequest.InstallInfo{Unsafe: []string{"private_code_unacknowledged", "private_code_unacknowledged"}}, nil, []string{"unsafe_allow_runc", "private_code_unacknowledged"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			var seen string
			srv := &Server{Humans: tokenAuth{}, Groups: groups, Unsafe: []string{"unsafe_allow_runc"},
				FactoryInstall: func(_ context.Context, token string) (runrequest.InstallInfo, error) {
					seen = token
					return c.factory, c.err
				}}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
			req.Header.Set("X-Test-User", "alice")
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || seen != "tok-alice" {
				t.Fatalf("%d %s (token %q)", rec.Code, rec.Body, seen)
			}
			got := decodeInstall(t, rec.Body.String())
			if !slices.Equal(got.Unsafe, c.wantCodes) {
				t.Fatalf("unsafe %v, want %v", got.Unsafe, c.wantCodes)
			}
			if got.Environment == nil || got.Environment.Factory == nil {
				t.Fatalf("missing factory environment in %s", rec.Body)
			}
			if got.Environment.Factory.Reachable != c.wantReachable {
				t.Fatalf("reachable %v, want %v", got.Environment.Factory.Reachable, c.wantReachable)
			}
			if c.factory.Factory != "" {
				f := got.Environment.Factory
				if f.Version != "v0.58.0" || f.Name != "gcp-0" || f.Namespace != "agent-system" ||
					f.DefaultHarness != "opencode" || f.PolicyStage != "manage" || f.Hardening != "enforce" ||
					f.RoutinesUIWrite == nil || !*f.RoutinesUIWrite {
					t.Fatalf("factory env %+v", f)
				}
			}
		})
	}
	called := false
	srv := &Server{Humans: headerAuth{}, Groups: groups, FactoryInstall: func(context.Context, string) (runrequest.InstallInfo, error) {
		called = true
		return runrequest.InstallInfo{}, nil
	}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
	req.Header.Set("X-Test-User", "alice")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	got := decodeInstall(t, rec.Body.String())
	if called || len(got.Unsafe) != 0 || got.Environment == nil || got.Environment.Factory != nil {
		t.Fatalf("no token reads nothing: called %v, %s", called, rec.Body)
	}
}

// Review of #540: the factory's codes and metadata are read at most once per installTTL, whoever
// views the page, and a factory slower than installTimeout gives no banner rather than a slow page.
func TestInstallCachesAndFailsSoft(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	srv := &Server{Humans: tokenAuth{}, Groups: groups, Now: func() time.Time { return now },
		FactoryInstall: func(context.Context, string) (runrequest.InstallInfo, error) {
			calls++
			return runrequest.InstallInfo{
				Unsafe:  []string{"private_code_unacknowledged"},
				Version: "v0.58.0",
				Factory: "gcp-0",
			}, nil
		}}
	view := func() decodedInstall {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
		req.Header.Set("X-Test-User", "alice")
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		return decodeInstall(t, rec.Body.String())
	}
	first, second := view(), view()
	if !slices.Equal(first.Unsafe, []string{"private_code_unacknowledged"}) ||
		!slices.Equal(second.Unsafe, []string{"private_code_unacknowledged"}) ||
		second.Environment == nil || second.Environment.Factory == nil || second.Environment.Factory.Version != "v0.58.0" ||
		calls != 1 {
		t.Fatalf("%d factory reads inside the TTL, want 1 (%+v %+v)", calls, first, second)
	}
	now = now.Add(installTTL + time.Second)
	if third := view(); !slices.Equal(third.Unsafe, []string{"private_code_unacknowledged"}) || calls != 2 {
		t.Fatalf("%d factory reads past the TTL, want 2", calls)
	}

	slow := &Server{Humans: tokenAuth{}, Groups: groups, FactoryInstall: func(ctx context.Context, _ string) (runrequest.InstallInfo, error) {
		<-ctx.Done()
		return runrequest.InstallInfo{}, ctx.Err()
	}}
	start := time.Now()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/install", nil)
	req.Header.Set("X-Test-User", "alice")
	rec := httptest.NewRecorder()
	slow.Routes().ServeHTTP(rec, req)
	got := decodeInstall(t, rec.Body.String())
	if took := time.Since(start); took > installTimeout+time.Second || len(got.Unsafe) != 0 ||
		got.Environment == nil || got.Environment.Factory == nil || got.Environment.Factory.Reachable {
		t.Fatalf("a slow factory took %s and answered %s", took, rec.Body)
	}
}
