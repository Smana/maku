// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/reconciler"
)

func TestGateRequired(t *testing.T) {
	const app = 424242
	for name, c := range map[string]struct {
		rules []forge.RequiredStatus
		want  bool
	}{
		"from the App":         {[]forge.RequiredStatus{{Context: "ci", IntegrationID: 15368}, {Context: reconciler.GateStatusContext, IntegrationID: app}}, true},
		"from another App":     {[]forge.RequiredStatus{{Context: reconciler.GateStatusContext, IntegrationID: 1}}, false},
		"from any App":         {[]forge.RequiredStatus{{Context: reconciler.GateStatusContext}}, false},
		"no rule":              {nil, false},
		"policy-bot alone":     {[]forge.RequiredStatus{{Context: "policy-bot: main", IntegrationID: app}}, false},
		"another context, App": {[]forge.RequiredStatus{{Context: "maku/merge-gate-x", IntegrationID: app}}, false},
	} {
		if got := GateRequired(c.rules, app); got != c.want {
			t.Errorf("%s: got %t, want %t", name, got, c.want)
		}
	}
	if GateRequired([]forge.RequiredStatus{{Context: reconciler.GateStatusContext}}, 0) {
		t.Error("an unknown App id requires nothing")
	}
}

type gateGuardRig struct {
	f      *forgetest.Fake
	cfg    *config.Config
	log    bytes.Buffer
	mu     sync.Mutex
	got    map[string][]bool
	unread map[string]int
	obs    fenceObserver
}

func newGateGuardRig(t *testing.T, gateStatus bool) *gateGuardRig {
	t.Helper()
	idFile := filepath.Join(t.TempDir(), "app_id")
	if err := os.WriteFile(idFile, []byte("424242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &gateGuardRig{f: forgetest.NewFake(), got: map[string][]bool{}, unread: map[string]int{}}
	g.cfg = &config.Config{Repository: "Smana/demo"}
	g.cfg.GitHub.MergerAppIDFile = idFile
	g.cfg.Merge.GateStatus = gateStatus
	g.obs = gateGuardObserver(func() *config.Config { return g.cfg }, func(string) rulesReader { return g.f },
		func(_ context.Context, repo string, required bool) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.got[repo] = append(g.got[repo], required)
		}, func(_ context.Context, repo string) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.unread[repo]++
		}, slog.New(slog.NewTextHandler(&g.log, nil)))
	return g
}

func (g *gateGuardRig) unreadCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.unread["Smana/demo"]
}

func (g *gateGuardRig) recorded() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.got["Smana/demo"]
}

func TestGateGuardMakesNoCallWithoutGateStatus(t *testing.T) {
	g := newGateGuardRig(t, false)
	g.obs(t.Context(), "Smana/demo", nil)
	if n := g.f.BranchRuleReads(); n != 0 || len(g.recorded()) != 0 {
		t.Fatalf("%d reads, %v recorded", n, g.recorded())
	}
}

func TestGateGuardWarnsOnceWhenTheRuleGoes(t *testing.T) {
	g := newGateGuardRig(t, true)
	g.f.SetBranchRules("main", []forge.RequiredStatus{{Context: reconciler.GateStatusContext, IntegrationID: 424242}}, nil)
	g.obs(t.Context(), "Smana/demo", nil)
	g.f.SetBranchRules("main", []forge.RequiredStatus{{Context: reconciler.GateStatusContext}}, nil)
	g.obs(t.Context(), "Smana/demo", nil)
	g.obs(t.Context(), "Smana/demo", nil)
	if got := g.recorded(); len(got) != 3 || !got[0] || got[1] || got[2] {
		t.Fatalf("recorded %v, want [true false false]", got)
	}
	const warn = "maku/merge-gate is not required from the App that posts it on main: anyone with write access can merge an agents pull request"
	if n := strings.Count(g.log.String(), warn); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, g.log.String())
	}
}

func TestGateGuardKeepsTheLastValueOnAnError(t *testing.T) {
	g := newGateGuardRig(t, true)
	g.f.SetBranchRules("main", []forge.RequiredStatus{{Context: reconciler.GateStatusContext, IntegrationID: 424242}}, nil)
	g.obs(t.Context(), "Smana/demo", nil)
	g.f.SetBranchRules("main", nil, errors.New("forge: branch rules: 502"))
	g.obs(t.Context(), "Smana/demo", nil)
	g.f.FailRepositoryInfo(errors.New("forge: repository: 502"))
	g.obs(t.Context(), "Smana/demo", nil)
	if got := g.recorded(); len(got) != 1 || !got[0] {
		t.Fatalf("recorded %v, want [true]: an error records nothing", got)
	}
	if strings.Contains(g.log.String(), "not required") {
		t.Fatalf("an error is not a missing rule:\n%s", g.log.String())
	}
	if n := g.unreadCount(); n != 2 {
		t.Fatalf("%d unread passes counted, want 2", n)
	}
}
