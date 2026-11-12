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
		Hardening: config.Hardening{Mode: hardening.ModeAudit, Repositories: repos,
			Publisher: config.Publisher{OctoSTS: config.PublisherOctoSTS{URL: "https://octo-sts.dev"}}}}
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
	if a := safeOutputApplier(off, applierDeps{Hardened: hardeningPorts(off, nil), Live: func() *config.Config { return off },
		Tokens: tokens, Log: log}); a != nil || len(tokens.asked) != 0 {
		t.Fatalf("an applier with nothing enforced: %+v, tokens %v", a, tokens.asked)
	}

	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.Issue, hardening.PR}}})
	a := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Tokens: tokens, Log: log})
	if a == nil {
		t.Fatal("no applier for an enforced repository")
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
	if a := safeOutputApplier(demo, applierDeps{Hardened: hardeningPorts(demo, nil), Live: func() *config.Config { return demo }, Log: log}); a != nil {
		t.Fatal("an applier in the demo")
	}

	// Readiness: R1 needs enforce, which this release refuses, so an enforced repository is not hardened.
	e := hardening.Evaluator{Settings: func(r string) hardening.Settings { return cfg.HardeningSettings(r, "v9.9.9") }}
	if r, err := e.Hardened(t.Context(), "Smana/ai-native-ref"); err != nil || r.Hardened || !slices.Contains(r.Missing, hardening.MissingEnforce) {
		t.Fatalf("readiness %+v %v", r, err)
	}
	// Coordination's prerequisites stay nil: execute still refuses (v4 MR16).
	src, err := os.ReadFile("factory.go")
	if err != nil || !strings.Contains(string(src), "Prerequisites: nil") {
		t.Fatalf("coordination's prerequisites are wired: %v", err)
	}
}

// The permission gauge's observer reads each sandbox role's and the publisher's policy on an
// enforced repository only, and keeps a role whose policy does not read out of the pass.
func TestSandboxPermissionObserver(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	files := fakePolicies{
		hardening.RolePolicyPath("implementer"): "permissions: {contents: write, pull_requests: read}\n",
		hardening.RolePolicyPath("reviewer"):    "permissions: 3\n",
		hardening.PublisherPolicyPath:           "permissions: {pull_requests: write, issues: write, contents: read}\n",
	}
	var got map[string]map[string]string
	calls := 0
	obs := sandboxPermissionObserver(func() *config.Config { return cfg }, func(string) policyReader { return files },
		func(_ context.Context, _ string, g map[string]map[string]string) { calls++; got = g }, slog.New(slog.DiscardHandler))
	obs(t.Context(), "Smana/cloud-native-ref", nil)
	if calls != 0 {
		t.Fatal("an unenforced repository was read")
	}
	obs(t.Context(), "Smana/ai-native-ref", nil)
	if calls != 1 || got["implementer"]["pull_requests"] != "read" || got["publisher"]["pull_requests"] != "write" {
		t.Fatalf("grants %v", got)
	}
	if _, ok := got["reviewer"]; ok {
		t.Fatal("a policy that does not parse is in the pass")
	}
	if g, ok := got["tester"]; !ok || len(g) != 0 {
		t.Fatalf("no policy is no grant: %v", got["tester"])
	}
}

type fakePolicies map[string]string

func (f fakePolicies) DefaultBranchFile(_ context.Context, path string) (string, string, bool, error) {
	text, ok := f[path]
	return text, "", ok, nil
}
