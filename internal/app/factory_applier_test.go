// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/hardening"
)

// fakeTokens records which repositories a publisher token was asked for.
type fakeTokens struct{ asked []string }

func (f *fakeTokens) For(repo string) (forge.TokenSource, error) {
	f.asked = append(f.asked, repo)
	return nil, errors.New("no token in a test")
}

func enforcingConfig(repos map[string]config.HardeningRepository) *config.Config {
	cfg := &config.Config{AgentsLogin: "ogenki-agents[bot]", FactoryLogin: "ogenki-agent-factory[bot]",
		Hardening: config.Hardening{Mode: hardening.ModeAudit, Repositories: repos, Publisher: config.Publisher{Minter: true}},
		RunTokens: config.RunTokens{Enabled: true, PerRun: true},
		GitHub:    config.GitHub{AgentsAppIDFile: "/a/app_id", AgentsKeyFile: "/a/private_key"}}
	cfg.Hardening.ApplyDefaults()
	return cfg
}

// Task 21: with no enforceOutputs anywhere the factory starts no applier and builds no publisher;
// with a repository's list it starts one that serves that repository with its own publisher, and
// coordination's prerequisites stay unproven: readiness still answers false.
func TestFactoryStartsApplier(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	off := enforcingConfig(nil)
	tokens := &fakeTokens{}
	if a, err := safeOutputApplier(off, applierDeps{Hardened: hardeningPorts(off, nil), Live: func() *config.Config { return off },
		Log: log}); a != nil || err != nil {
		t.Fatalf("an applier with nothing enforced: %+v, %v", a, err)
	}

	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.Issue, hardening.PR}}})
	if a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: log}); a == nil || err != nil {
		t.Fatalf("the minter's publisher: %v", err)
	}
	a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Tokens: tokens, Log: log})
	if a == nil || err != nil {
		t.Fatalf("no applier for an enforced repository: %v", err)
	}
	if got := a.Enforced("Smana/ai-native-ref"); !slices.Equal(got, []hardening.OutputType{hardening.Issue, hardening.PR}) {
		t.Fatalf("enforced %v", got)
	}
	if got := a.Enforced("Smana/cloud-native-ref"); len(got) != 0 {
		t.Fatalf("an unlisted repository is enforced: %v", got)
	}
	if a.AgentsLogin != cfg.AgentsLogin || a.FactoryLogin != cfg.FactoryLogin || a.Interval != 2*time.Second || a.AnswerWait != 5*time.Minute {
		t.Fatalf("applier %+v", a)
	}
	// The shadow pass's checker, its layers replaced by the applier itself (T1 to T4 enforce).
	if c := a.Checker("Smana/ai-native-ref"); c.Layers != hardening.LayersFor(hardening.ModeAudit, hardening.ModeOff) {
		t.Fatalf("checker %+v", c)
	}
	if _, err := a.Publisher(t.Context(), "Smana/ai-native-ref"); err == nil || !slices.Equal(tokens.asked, []string{"Smana/ai-native-ref"}) {
		t.Fatalf("publisher: %v, tokens asked for %v", err, tokens.asked)
	}
	// The demo is never hardened, whatever its config says.
	demo := enforcingConfig(cfg.Hardening.Repositories)
	demo.Demo = true
	if a, err := safeOutputApplier(demo, applierDeps{Hardened: hardeningPorts(demo, nil), Live: func() *config.Config { return demo }, Log: log}); a != nil || err != nil {
		t.Fatalf("an applier in the demo: %v", err)
	}

	// Readiness: R1 needs enforce, which this release refuses, so an enforced repository is not hardened.
	e := hardening.Evaluator{Settings: func(r string) hardening.Settings { return cfg.HardeningSettings(r, "v9.9.9") }}
	if r, err := e.Hardened(t.Context(), "Smana/ai-native-ref"); err != nil || r.Hardened || !slices.Contains(r.Missing, hardening.MissingEnforce) {
		t.Fatalf("readiness %+v %v", r, err)
	}
	// H5 (Task 55) wires coordination's prerequisites: execute asks class authority and readiness
	// (v4 MR16); TestPrerequisitesWired proves each fact refuses.
	src, err := os.ReadFile("factory.go")
	if err != nil || !strings.Contains(string(src), "Prerequisites: coordinationPrerequisites(live, readiness)") {
		t.Fatalf("coordination's prerequisites are not wired: %v", err)
	}
}

// Plan PR7: the publisher's token comes from the agents App's minter (hardening.publisher.minter),
// so the applier starts with no projected token to read, and refuses to start with no agents App.
func TestFactoryStartsApplierWithTheMinter(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: slog.New(slog.DiscardHandler)})
	if a == nil || err != nil {
		t.Fatalf("the minter's publisher did not start: %v", err)
	}
	cfg.RunTokens = config.RunTokens{}
	if _, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: slog.New(slog.DiscardHandler)}); err == nil || !strings.Contains(err.Error(), "runTokens.enabled") {
		t.Fatalf("a minter with no agents App started: %v", err)
	}
}

// The permission gauge's observer reads the role table on an enforced repository only.
func TestSandboxPermissionObserver(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	var got map[string]map[string]string
	calls := 0
	obs := sandboxPermissionObserver(func() *config.Config { return cfg }, tableGrants(func() *config.Config { return cfg }, minter.Roles),
		func(_ context.Context, _ string, g map[string]map[string]string) { calls++; got = g })
	obs(t.Context(), "Smana/cloud-native-ref", nil)
	if calls != 0 {
		t.Fatal("an unenforced repository was read")
	}
	obs(t.Context(), "Smana/ai-native-ref", nil)
	if calls != 1 || got["implementer"] == nil || got["publisher"]["pull_requests"] != "write" {
		t.Fatalf("grants %v", got)
	}
	none := sandboxPermissionObserver(func() *config.Config { return cfg }, nil,
		func(context.Context, string, map[string]map[string]string) { calls++ })
	none(t.Context(), "Smana/ai-native-ref", nil)
	if calls != 1 {
		t.Fatal("no role table recorded a pass")
	}
}

// Plan PR8 (HR42, option A): under per-run tokens, readiness's R2 and R3 and the permission gauge
// read the minter's role table for the repository's enforced outputs; under a shared token R2 is
// missing whatever the table says.
func TestTokenSourceReadiness(t *testing.T) {
	const repo = "Smana/ai-native-ref"
	all := []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels}
	for name, tc := range map[string]struct {
		perRun       bool
		table        minter.Table
		wantMissing  hardening.Missing
		wantNot      hardening.Missing
		implementer  map[string]string // the gauge's implementer row; nil: not recorded
		publisherPRs string
	}{
		"HR12":   {true, minter.TableHR12, hardening.MissingWriteToken, hardening.MissingPublisher, map[string]string{"pull_requests": "read", "contents": "write"}, "write"},
		"D10":    {true, minter.TableD10, hardening.MissingWriteToken, hardening.MissingPublisher, map[string]string{"pull_requests": "write", "contents": "write"}, "write"},
		"shared": {false, minter.TableHR12, hardening.MissingWriteToken, hardening.MissingPublisher, nil, "write"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enforcingConfig(map[string]config.HardeningRepository{repo: {Mode: hardening.ModeAudit, EnforceOutputs: all}})
			cfg.RunTokens.PerRun = tc.perRun
			live := func() *config.Config { return cfg }
			grants := tableGrants(live, tc.table)
			e := hardeningEvaluator(live, grants, nil, nil)
			r, err := e.Hardened(t.Context(), repo)
			if err != nil || !slices.Contains(r.Missing, tc.wantMissing) || slices.Contains(r.Missing, tc.wantNot) {
				t.Fatalf("readiness %+v %v: want %s missing, %s held", r, err, tc.wantMissing, tc.wantNot)
			}
			var got map[string]map[string]string
			obs := sandboxPermissionObserver(live, grants,
				func(_ context.Context, _ string, g map[string]map[string]string) { got = g })
			obs(t.Context(), repo, nil)
			if tc.implementer == nil {
				if _, ok := got["implementer"]; ok {
					t.Fatalf("a shared token's roles were recorded: %v", got)
				}
			} else if g := got["implementer"]; g["pull_requests"] != tc.implementer["pull_requests"] || g["contents"] != tc.implementer["contents"] {
				t.Fatalf("implementer %v, want %v", g, tc.implementer)
			}
			if got["publisher"]["pull_requests"] != tc.publisherPRs {
				t.Fatalf("publisher %v", got["publisher"])
			}
		})
	}
}

// Plan PR13 (HR13): under per-run tokens an enforced repository passes HR13's refusal, but a
// role table in which the implementer keeps pull_requests: write once pr is enforced
// (D10 as written) leaves that enforcement advisory: the factory warns, naming the repository.
func TestRoleTableWarning(t *testing.T) {
	const repo = "Smana/ai-native-ref"
	for name, tc := range map[string]struct {
		perRun bool
		table  minter.Table
		warn   bool
	}{
		"per-run, HR12":       {true, minter.TableHR12, false},
		"per-run, D10":        {true, minter.TableD10, true},
		"shared, D10":         {false, minter.TableD10, false},
		"the factory's table": {true, minter.Roles, false},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enforcingConfig(map[string]config.HardeningRepository{repo: {Mode: hardening.ModeAudit,
				EnforceOutputs: []hardening.OutputType{hardening.PR}}})
			cfg.Repositories = []config.Repository{{Name: repo}, {Name: "Smana/cloud-native-ref"}}
			cfg.RunTokens.PerRun = tc.perRun
			w := roleTableWarning(cfg, tc.table)
			if (w != "") != tc.warn {
				t.Fatalf("warning %q, want one: %v", w, tc.warn)
			}
			if tc.warn && (!strings.Contains(w, repo) || strings.Contains(w, "cloud-native-ref") || !strings.Contains(w, "pull_requests: write")) {
				t.Fatalf("warning %q", w)
			}
		})
	}
}
