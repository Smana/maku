// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
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
	if a, err := safeOutputApplier(off, applierDeps{Hardened: hardeningPorts(off, nil), Live: func() *config.Config { return off },
		Log: log}); a != nil || err != nil {
		t.Fatalf("an applier with nothing enforced: %+v, %v", a, err)
	}

	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.Issue, hardening.PR}}})
	// #491's review nit 6: no readable publisher token, no start, and the error says what to set.
	cfg.Hardening.Publisher.OctoSTS.TokenFile = filepath.Join(t.TempDir(), "absent")
	if _, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: log}); err == nil || !strings.Contains(err.Error(), "hardening.publisher.audience") {
		t.Fatalf("a missing publisher token started: %v", err)
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Hardening.Publisher.OctoSTS.TokenFile = token
	if a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: log}); a == nil || err != nil {
		t.Fatalf("a readable token: %v", err)
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

// Plan PR7: with hardening.publisher.minter the publisher's token comes from the agents App's
// minter, so the applier starts with no projected token to read.
func TestFactoryStartsApplierWithTheMinter(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	cfg.Hardening.Publisher = config.Publisher{Minter: true, OctoSTS: config.PublisherOctoSTS{TokenFile: filepath.Join(t.TempDir(), "absent")}}
	cfg.RunTokens = config.RunTokens{Enabled: true, PerRun: true}
	cfg.GitHub.AgentsAppIDFile, cfg.GitHub.AgentsKeyFile = "/a/app_id", "/a/private_key"
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
	obs := sandboxPermissionObserver(func() *config.Config { return cfg }, func(string) policyReader { return files }, nil,
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

// Plan PR8 (HR42, option A): under per-run tokens, readiness's R2 and R3 and the permission gauge
// read the minter's role table for the repository's enforced outputs, and no policy file; under a
// shared token R2 is missing whatever the policies say.
func TestTokenSourceReadiness(t *testing.T) {
	const repo = "Smana/ai-native-ref"
	all := []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels}
	readOnly := "permissions: {contents: read, pull_requests: read}\n"
	for name, tc := range map[string]struct {
		perRun       bool
		table        minter.Table
		wantMissing  hardening.Missing
		wantNot      hardening.Missing
		implementer  map[string]string // the gauge's implementer row; nil: not recorded
		publisherPRs string
		reads        int
	}{
		"HR12":   {true, minter.TableHR12, hardening.MissingWriteToken, hardening.MissingPublisher, map[string]string{"pull_requests": "read", "contents": "write"}, "write", 0},
		"D10":    {true, minter.TableD10, hardening.MissingWriteToken, hardening.MissingPublisher, map[string]string{"pull_requests": "write", "contents": "write"}, "write", 0},
		"shared": {false, minter.TableHR12, hardening.MissingWriteToken, hardening.MissingPublisher, nil, "write", 0},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enforcingConfig(map[string]config.HardeningRepository{repo: {Mode: hardening.ModeAudit, EnforceOutputs: all}})
			cfg.Runs.Kind = runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents"}
			cfg.RunTokens = config.RunTokens{Enabled: true, PerRun: tc.perRun}
			cfg.GitHub.AgentsAppIDFile, cfg.GitHub.AgentsKeyFile = "/a/app_id", "/a/private_key"
			cfg.Hardening.Publisher = config.Publisher{Minter: true}
			live := func() *config.Config { return cfg }
			files := &countingPolicies{fakePolicies: fakePolicies{hardening.PublisherPolicyPath: readOnly}}
			for _, r := range hardening.SandboxRoles() {
				files.fakePolicies[hardening.RolePolicyPath(r)] = readOnly
			}
			grants := tableGrants(live, tc.table)
			e := hardening.Evaluator{Settings: func(r string) hardening.Settings { return cfg.HardeningSettings(r, "v9.9.9") },
				Policies: readinessFiles{files}, Grants: grants}
			r, err := e.Hardened(t.Context(), repo)
			if err != nil || !slices.Contains(r.Missing, tc.wantMissing) || slices.Contains(r.Missing, tc.wantNot) {
				t.Fatalf("readiness %+v %v: want %s missing, %s held", r, err, tc.wantMissing, tc.wantNot)
			}
			var got map[string]map[string]string
			obs := sandboxPermissionObserver(live, func(string) policyReader { return files }, grants,
				func(_ context.Context, _ string, g map[string]map[string]string) { got = g }, slog.New(slog.DiscardHandler))
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
			if files.reads != tc.reads {
				t.Fatalf("%d policy files read, want %d", files.reads, tc.reads)
			}
		})
	}
}

// Plan PR13 (HR13): under per-run tokens an enforced repository leaves the XR without HR13's
// refusal, but a role table in which the implementer keeps pull_requests: write once pr is enforced
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
		"the XR, D10":         {false, minter.TableD10, false},
		"the factory's table": {true, minter.Roles, false},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enforcingConfig(map[string]config.HardeningRepository{repo: {Mode: hardening.ModeAudit,
				EnforceOutputs: []hardening.OutputType{hardening.PR}}})
			cfg.Repositories = []config.Repository{{Name: repo}, {Name: "Smana/cloud-native-ref"}}
			if tc.perRun {
				cfg.Runs.Kind = runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents"}
				cfg.RunTokens = config.RunTokens{Enabled: true, PerRun: true}
			}
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

type fakePolicies map[string]string

func (f fakePolicies) DefaultBranchFile(_ context.Context, path string) (string, string, bool, error) {
	text, ok := f[path]
	return text, "", ok, nil
}

// countingPolicies serves both policy ports, the observer's and readiness's, counting every read.
type countingPolicies struct {
	fakePolicies
	reads int
}

func (c *countingPolicies) DefaultBranchFile(ctx context.Context, path string) (string, string, bool, error) {
	c.reads++
	return c.fakePolicies.DefaultBranchFile(ctx, path)
}

// readinessFiles adapts countingPolicies to readiness's port, which also names the repository.
type readinessFiles struct{ c *countingPolicies }

func (r readinessFiles) DefaultBranchFile(ctx context.Context, _, path string) ([]byte, bool, error) {
	text, _, ok, err := r.c.DefaultBranchFile(ctx, path)
	return []byte(text), ok, err
}
