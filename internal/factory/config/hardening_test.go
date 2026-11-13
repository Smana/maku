// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/hardening"
)

// The hardening configuration: an omitted block is off, and the defaults are the design's.
func TestHardeningDefaultsOff(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	h := c.Hardening
	if h.Mode != hardening.ModeOff || h.MinAuthorTrust != "approved" || h.Layers.Classifier != hardening.ModeAudit ||
		h.Drills.MaxAge.Duration != 168*time.Hour || len(h.LabelAllowlist) != 0 {
		t.Fatalf("defaults %+v", h)
	}
	if h.ModeFor("Smana/cloud-native-ref") != hardening.ModeOff {
		t.Fatal("an unlisted repository reads the central mode")
	}
	// YAML 1.1 reads an unquoted off as false: it is still off, for the mode and a layer.
	for _, block := range []string{"hardening: {mode: off}\n", "hardening: {mode: \"off\", layers: {classifier: off}}\n",
		"hardening:\n  mode: off\n  minAuthorTrust: merged\n  blockedUsers: [spammer]\n  labelAllowlist: [documentation, needs-human]\n  drills: {maxAge: 72h}\n"} {
		c, err := Parse([]byte(good + block))
		if err != nil || c.Hardening.Mode != hardening.ModeOff {
			t.Errorf("%q: %v", block, err)
		}
	}
	c, _ = Parse([]byte(good + "hardening: {layers: {classifier: off}, minAuthorTrust: merged}\n"))
	if c.Hardening.Layers.Classifier != hardening.ModeOff || c.Hardening.MinAuthorTrust != "merged" {
		t.Fatalf("%+v", c.Hardening)
	}
}

// Stage H1 accepts off and audit: enforce, centrally or per repository, fails startup, as does a
// value the design does not define or a label an agent must never choose.
func TestHardeningRefusals(t *testing.T) {
	for name, c := range map[string]struct{ block, want string }{
		"enforce on the XR":          {"hardening: {mode: enforce}\n", "R5 cannot hold on the XR"},
		"a repository below central": {"hardening: {mode: audit, repositories: {Smana/cloud-native-ref: {mode: off}}}\n", "only be stricter"},
		"a bogus mode":               {"hardening: {mode: on}\n", "hardening.mode"},
		"a bogus classifier":         {"hardening: {layers: {classifier: strict}}\n", "hardening.layers.classifier"},
		"a layer T1 to T4":           {"hardening: {layers: {schema: audit}}\n", "unknown field"},
		"a bogus trust":              {"hardening: {minAuthorTrust: trusted}\n", "hardening.minAuthorTrust"},
		"trust blocked":              {"hardening: {minAuthorTrust: blocked}\n", "hardening.minAuthorTrust"},
		"a factory label":            {"hardening: {labelAllowlist: [factory/live]}\n", "hardening.labelAllowlist"},
		"the trigger label":          {"hardening: {labelAllowlist: [factory/ready]}\n", "hardening.labelAllowlist"},
		"a class label":              {"hardening: {labelAllowlist: [docs]}\n", "hardening.labelAllowlist"},
		"an empty label":             {"hardening: {labelAllowlist: ['']}\n", "hardening.labelAllowlist"},
		"a zero drill age":           {"hardening: {drills: {maxAge: 0s}}\n", "hardening.drills.maxAge"},
		"an unknown repository":      {"hardening: {repositories: {Smana/other: {mode: off}}}\n", "hardening.repositories"},
		"an unknown key":             {"hardening: {mode: off, slack: true}\n", "unknown field"},
		"an unknown repository key":  {"hardening: {repositories: {Smana/cloud-native-ref: {minAuthorTrust: none}}}\n", "unknown field"},
		"an empty blocked user":      {"hardening: {blockedUsers: ['']}\n", "hardening.blockedUsers"},
		"a mode in capitals":         {"hardening: {mode: Audit}\n", "hardening.mode"},
		"a non-string mode":          {"hardening: {mode: 1}\n", "hardening.mode"},
		"a mode true":                {"hardening: {mode: true}\n", "hardening.mode"},
		"a duplicate key":            {"hardening:\n  mode: off\n  mode: off\n", "hardening.mode is written twice"},
		"a duplicate block":          {"hardening: {mode: off}\nhardening: {mode: off}\n", "hardening is written twice"},
		"a blocked maintainer stays": {"hardening: {blockedUsers: [smana]}\n", "hardening.blockedUsers"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + c.block))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error naming %q", err, c.want)
			}
		})
	}
}

// A repository may only be stricter than the central mode. Tested on the rule itself: H1 refuses
// enforce, so a file reaches it only with a central audit (TestHardeningRefusals).
func TestHardeningRepositoryStricterOnly(t *testing.T) {
	h := Hardening{Mode: hardening.ModeEnforce, Repositories: map[string]HardeningRepository{"a/b": {Mode: hardening.ModeAudit}, "c/d": {Mode: hardening.ModeEnforce}}}
	var errs []string
	h.validateStricter(func(f string, _ ...any) { errs = append(errs, f) })
	if len(errs) != 1 {
		t.Fatalf("%v", errs)
	}
	if h.ModeFor("c/d") != hardening.ModeEnforce || h.ModeFor("x/y") != hardening.ModeEnforce {
		t.Fatal("ModeFor")
	}
	h = Hardening{Mode: hardening.ModeAudit, Repositories: map[string]HardeningRepository{"a/b": {Mode: hardening.ModeEnforce}}}
	if h.ModeFor("a/b") != hardening.ModeEnforce || h.ModeFor("x/y") != hardening.ModeAudit {
		t.Fatal("ModeFor, stricter")
	}
}

// Readiness reads its configuration facts here: the effective mode, the layers, the demo flag and
// a hash a drill report binds; forge_read is not wired before stage H4.
func TestHardeningSettings(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	s := c.HardeningSettings("Smana/cloud-native-ref", "v9.9.9")
	if s.Mode != hardening.ModeOff || s.Layers != hardening.LayersFor(hardening.ModeOff, hardening.ModeAudit) ||
		s.ReadFilter || s.Demo || s.DrillMaxAge != 168*time.Hour || s.Version != "v9.9.9" || len(s.ConfigHash) != 64 {
		t.Fatalf("%+v", s)
	}
	same, _ := Parse([]byte(good + "hardening: {mode: off, minAuthorTrust: approved}\n"))
	other, _ := Parse([]byte(good + "hardening: {minAuthorTrust: merged}\n"))
	if same.Hardening.Hash() != c.Hardening.Hash() || other.Hardening.Hash() == c.Hardening.Hash() {
		t.Fatal("the hash follows the effective configuration, not its spelling")
	}
	c.Demo = true
	if !c.HardeningSettings("x/y", "").Demo {
		t.Fatal("R7 reads the demo flag")
	}
}

// Plan PR8: R2 and R3 read the token source the runs get: the XR's policies, the minter's table
// under runTokens.perRun, and a shared token otherwise; the publisher's from the minter when set.
func TestHardeningSettingsTokenSource(t *testing.T) {
	runs := "runs: {group: maku.ogenki.io}\n"
	shared := "runTokens: {enabled: true, appIDFile: /a/app_id, privateKeyFile: /a/private_key}\n"
	for name, tc := range map[string]struct {
		file      string
		src       hardening.TokenSource
		pubMinter bool
	}{
		"the XR":               {good, hardening.TokenSourceXR, false},
		"per-run tokens":       {good + perRunTokens, hardening.TokenSourceMinter, false},
		"the minter publishes": {good + perRunTokens + "hardening: {publisher: {minter: true}}\n", hardening.TokenSourceMinter, true},
		"the shared writer":    {good + runs + shared, hardening.TokenSourceShared, false},
		"no run token":         {good + runs, hardening.TokenSourceShared, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if s := c.HardeningSettings("Smana/cloud-native-ref", ""); s.TokenSource != tc.src || s.PublisherMinter != tc.pubMinter {
				t.Fatalf("source %q, publisher minter %v; want %q, %v", s.TokenSource, s.PublisherMinter, tc.src, tc.pubMinter)
			}
		})
	}
}

// Stage H1: audit is accepted centrally and per repository.
func TestHardeningAuditAccepted(t *testing.T) {
	for name, tc := range map[string]struct {
		block            string
		central, forRepo hardening.Mode
	}{
		"central audit":           {"hardening: {mode: audit}\n", hardening.ModeAudit, hardening.ModeAudit},
		"a repository in audit":   {"hardening: {repositories: {Smana/cloud-native-ref: {mode: audit}}}\n", hardening.ModeOff, hardening.ModeAudit},
		"both in audit":           {"hardening: {mode: audit, repositories: {Smana/cloud-native-ref: {mode: audit}}}\n", hardening.ModeAudit, hardening.ModeAudit},
		"a repository left unset": {"hardening: {mode: audit, repositories: {Smana/cloud-native-ref: {}}}\n", hardening.ModeAudit, hardening.ModeAudit},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(good + tc.block))
			if err != nil {
				t.Fatal(err)
			}
			if c.Hardening.Mode != tc.central || c.Hardening.ModeFor("Smana/cloud-native-ref") != tc.forRepo {
				t.Fatalf("central %q, repository %q", c.Hardening.Mode, c.Hardening.ModeFor("Smana/cloud-native-ref"))
			}
		})
	}
}

// Mode enforce is stage H4's (HR34, HR36): on the XR it is refused, centrally and per repository,
// since R5 cannot hold there. Edited at H4: the refusal was "enforce arrives with stage H4".
func TestHardeningEnforceStillRefused(t *testing.T) {
	for name, block := range map[string]string{
		"central":         "hardening: {mode: enforce}\n",
		"a repository":    "hardening: {repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
		"over audit":      "hardening: {mode: audit, repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
		"both in enforce": "hardening: {mode: enforce, repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + block))
			if err == nil || !strings.Contains(err.Error(), "R5 cannot hold on the XR") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// T5's promotion to enforce is stage H5's.
func TestClassifierEnforceRefused(t *testing.T) {
	for _, block := range []string{"hardening: {layers: {classifier: enforce}}\n",
		"hardening: {mode: audit, layers: {classifier: enforce}}\n"} {
		_, err := Parse([]byte(good + block))
		if err == nil || !strings.Contains(err.Error(), "hardening.layers.classifier") {
			t.Fatalf("%q: got %v", block, err)
		}
	}
}

// agent-guard's webhook (HR9): an http(s) URL with no credentials, and a bounded timeout.
func TestAgentGuardConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		block string
		ok    bool
		want  time.Duration
	}{
		"in-cluster":         {"hardening: {agentGuard: {url: \"http://agent-guard.agent-system.svc:8080\", timeout: 2s}}\n", true, 2 * time.Second},
		"https":              {"hardening: {agentGuard: {url: \"https://agent-guard.example\"}}\n", true, 2 * time.Second},
		"the ten second cap": {"hardening: {agentGuard: {url: \"http://agent-guard:8080\", timeout: 10s}}\n", true, 10 * time.Second},
		"no url":             {"hardening: {agentGuard: {}}\n", true, 2 * time.Second},
		"ftp":                {"hardening: {agentGuard: {url: \"ftp://agent-guard:21\"}}\n", false, 0},
		"no host":            {"hardening: {agentGuard: {url: \"http://\"}}\n", false, 0},
		"userinfo":           {"hardening: {agentGuard: {url: \"http://u:p@agent-guard:8080\"}}\n", false, 0},
		"a zero timeout":     {"hardening: {agentGuard: {url: \"http://agent-guard:8080\", timeout: 0s}}\n", false, 0},
		"a negative timeout": {"hardening: {agentGuard: {url: \"http://agent-guard:8080\", timeout: -1s}}\n", false, 0},
		"over ten seconds":   {"hardening: {agentGuard: {url: \"http://agent-guard:8080\", timeout: 11s}}\n", false, 0},
		"an unknown key":     {"hardening: {agentGuard: {url: \"http://agent-guard:8080\", token: x}}\n", false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(good + tc.block))
			if (err == nil) != tc.ok {
				t.Fatalf("err %v, want ok %v", err, tc.ok)
			}
			if tc.ok && c.Hardening.AgentGuard.Timeout.Duration != tc.want {
				t.Fatalf("timeout %s, want %s", c.Hardening.AgentGuard.Timeout, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "hardening") {
				t.Fatalf("the error names the block: %v", err)
			}
		})
	}
}

// T5 runs only where a classifier is configured, and the hash a drill binds says which: the URL's
// presence moves it, its host does not.
func TestHashCoversClassifierPresence(t *testing.T) {
	parse := func(block string) Hardening {
		t.Helper()
		c, err := Parse([]byte(good + block))
		if err != nil {
			t.Fatal(err)
		}
		return c.Hardening
	}
	none := parse("hardening: {mode: audit}\n")
	one := parse("hardening: {mode: audit, agentGuard: {url: \"http://agent-guard.agent-system.svc:8080\"}}\n")
	other := parse("hardening: {mode: audit, agentGuard: {url: \"http://guard.elsewhere.svc:9090\"}}\n")
	if none.EffectiveClassifier() != hardening.ModeOff || one.EffectiveClassifier() != hardening.ModeAudit {
		t.Fatalf("effective classifier %q and %q", none.EffectiveClassifier(), one.EffectiveClassifier())
	}
	if none.Hash() == one.Hash() {
		t.Fatal("the classifier's presence does not move the hash")
	}
	if one.Hash() != other.Hash() {
		t.Fatal("the classifier's host moves the hash")
	}
	c, _ := Parse([]byte(good + "hardening: {mode: audit}\n"))
	if s := c.HardeningSettings("Smana/cloud-native-ref", ""); s.Layers != hardening.LayersFor(hardening.ModeAudit, hardening.ModeOff) {
		t.Fatalf("with no URL T5 is off: %+v", s.Layers)
	}
	c, _ = Parse([]byte(good + "hardening: {mode: audit, agentGuard: {url: \"http://agent-guard:8080\"}}\n"))
	if s := c.HardeningSettings("Smana/cloud-native-ref", ""); s.Layers != hardening.LayersFor(hardening.ModeAudit, hardening.ModeAudit) {
		t.Fatalf("with a URL T5 runs at its layer: %+v", s.Layers)
	}
}

type passingPolicies struct{}

func (passingPolicies) DefaultBranchFile(_ context.Context, _, path string) ([]byte, bool, error) {
	if path == hardening.PublisherPolicyPath {
		return []byte("issuer: x\nsubject: y\npermissions:\n  pull_requests: write\n"), true, nil
	}
	return []byte("issuer: x\nsubject: y\npermissions:\n  contents: read\n"), true, nil
}

type passingDrills struct{ at time.Time }

func (d passingDrills) LastPass(context.Context, hardening.DrillKey) (hardening.DrillRecord, bool, error) {
	return hardening.DrillRecord{ID: "drill-1", At: d.at}, true, nil
}

// Audit never makes a repository hardened: R1 needs enforce, which this release refuses.
func TestReadinessStaysFalseUnderAudit(t *testing.T) {
	c, err := Parse([]byte(good + "hardening: {mode: audit, agentGuard: {url: \"http://agent-guard:8080\"}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	e := hardening.Evaluator{Settings: func(r string) hardening.Settings { return c.HardeningSettings(r, "v9.9.9") },
		Policies: passingPolicies{}, Drills: passingDrills{at: at.Add(-time.Hour)}, Now: func() time.Time { return at }}
	r, err := e.Hardened(t.Context(), "Smana/cloud-native-ref")
	if err != nil {
		t.Fatal(err)
	}
	if r.Hardened || !slices.Contains(r.Missing, hardening.MissingEnforce) {
		t.Fatalf("%+v", r)
	}
}

// enforcePublisher is the octo-sts publisher an enforceOutputs list needs (HR11).
const enforcePublisher = "  publisher: {octoSTS: {url: \"https://octo-sts.dev\"}}\n"

// perRunTokens puts runs on the run-controller, each with its own token from the agents App (v4 D10).
const perRunTokens = "runs: {group: maku.ogenki.io}\nrunTokens: {enabled: true, perRun: true, appIDFile: /a/app_id, privateKeyFile: /a/private_key}\n"

// H2 enforces the non-code outputs per repository (HR10): a repository's list is a superset of the
// central one, push is enforceable from H3, and a list needs audit at least and a publisher.
func TestEnforceOutputs(t *testing.T) {
	for name, tc := range map[string]struct {
		block, want string
	}{
		"central pr, a repository all four": {"hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {enforceOutputs: [pr, comment, labels, issue]}}\n", ""},
		"a repository alone, in audit": {"hardening:\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [comment]}}\n", ""},
		"a repository's list missing pr": {"hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {enforceOutputs: [comment]}}\n", "superset"},
		"push": {"hardening:\n  mode: audit\n  enforceOutputs: [push]\n" + enforcePublisher, ""},
		"a repository's push": {"hardening:\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [pr, push]}}\n", ""},
		"an unknown type":   {"hardening:\n  mode: audit\n  enforceOutputs: [review]\n" + enforcePublisher, "hardening.enforceOutputs"},
		"central, mode off": {"hardening:\n  enforceOutputs: [pr]\n" + enforcePublisher, "audit"},
		"a repository, mode off": {"hardening:\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {enforceOutputs: [pr]}}\n", "audit"},
		"no publisher":           {"hardening:\n  mode: audit\n  enforceOutputs: [pr]\n", "hardening.publisher.octoSTS"},
		"a plain-http publisher": {"hardening:\n  mode: audit\n  enforceOutputs: [pr]\n  publisher: {octoSTS: {url: \"http://octo-sts.dev\"}}\n", "hardening.publisher.octoSTS.url"},
		"the minter's publisher": {perRunTokens + "hardening:\n  mode: audit\n  enforceOutputs: [pr]\n  publisher: {minter: true}\n", ""},
		"the minter and octo-sts": {perRunTokens + "hardening:\n  mode: audit\n  enforceOutputs: [pr]\n  publisher: {minter: true, octoSTS: {url: \"https://octo-sts.dev\"}}\n",
			"hardening.publisher.minter and hardening.publisher.octoSTS.url"},
		"the minter without run tokens": {"hardening:\n  mode: audit\n  publisher: {minter: true}\n", "hardening.publisher.minter needs runTokens.enabled"},
		"a zero applier interval":       {"hardening:\n  mode: audit\n  applier: {interval: 0s}\n", "hardening.applier.interval"},
		"a zero answer wait":            {"hardening:\n  mode: audit\n  applier: {answerWait: 0s}\n", "hardening.applier.answerWait"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + tc.block))
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// EnforcedFor is the repository's effective list, sorted: its own when it writes one, else the
// central one. Nothing is enforced by default, and the publisher and applier have their defaults.
func TestEnforcedFor(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Hardening.EnforcedFor("Smana/cloud-native-ref"); len(got) != 0 || c.Hardening.Enforcing() {
		t.Fatalf("enforced by default: %v", got)
	}
	if a := c.Hardening.Applier; a.Interval.Duration != 2*time.Second || a.AnswerWait.Duration != 5*time.Minute {
		t.Fatalf("applier defaults %+v", a)
	}
	c, err = Parse([]byte(good + "hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher +
		"  repositories: {Smana/cloud-native-ref: {enforceOutputs: [labels, pr, issue, comment, pr]}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := c.Hardening
	if got := h.EnforcedFor("Smana/cloud-native-ref"); !slices.Equal(got, []hardening.OutputType{hardening.Comment, hardening.Issue, hardening.Labels, hardening.PR}) {
		t.Fatalf("repository list %v", got)
	}
	if got := h.EnforcedFor("Smana/other"); !slices.Equal(got, []hardening.OutputType{hardening.PR}) {
		t.Fatalf("an unlisted repository reads the central list: %v", got)
	}
	if p := h.Publisher.OctoSTS; p.URL != "https://octo-sts.dev" || p.TokenFile != "/var/run/secrets/octo-sts/token" || p.Identity != "publisher" {
		t.Fatalf("publisher defaults %+v", p)
	}
	if !h.Enforcing() {
		t.Fatal("Enforcing with a list")
	}
}

// A drill binds the enforced lists: adding a type moves the hash, and a file without any list keeps
// the hash it had before enforceOutputs existed.
func TestHashCoversEnforce(t *testing.T) {
	parse := func(block string) Hardening {
		t.Helper()
		c, err := Parse([]byte(good + block))
		if err != nil {
			t.Fatal(err)
		}
		return c.Hardening
	}
	none := parse("hardening: {mode: audit}\n")
	pr := parse("hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher)
	two := parse("hardening:\n  mode: audit\n  enforceOutputs: [pr, comment]\n" + enforcePublisher)
	repo := parse("hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher +
		"  repositories: {Smana/cloud-native-ref: {enforceOutputs: [pr, labels]}}\n")
	if none.Hash() == pr.Hash() || pr.Hash() == two.Hash() || pr.Hash() == repo.Hash() {
		t.Fatal("an enforced type does not move the hash")
	}
	if withPublisher := parse("hardening:\n  mode: audit\n" + enforcePublisher); withPublisher.Hash() != none.Hash() {
		t.Fatal("the publisher alone moves the hash")
	}
	// The pre-H2 encoding, written out: an unchanged file keeps its drill.
	b, _ := json.Marshal(struct {
		Mode, MinAuthorTrust, Classifier, MaxAge string
		BlockedUsers, LabelAllowlist             []string
		Repositories                             map[string]hardening.Mode
	}{string(none.Mode), none.MinAuthorTrust, string(none.EffectiveClassifier()), none.Drills.MaxAge.String(),
		none.BlockedUsers, none.LabelAllowlist, none.repositoryModes()})
	if s := sha256.Sum256(b); hex.EncodeToString(s[:]) != none.Hash() {
		t.Fatal("a file with no enforceOutputs changed its hash")
	}
}

// HR13 (the owner's ruling, 2026-11-12): an enforced repository whose runs are not
// cloud-native-ref's XR would run on a token no role policy scopes. The factory refuses to start.
func TestUnscopedTokenRefused(t *testing.T) {
	block := "hardening:\n  mode: audit\n  enforceOutputs: [pr]\n" + enforcePublisher
	runs := "runs: {group: maku.ogenki.io}\n"
	for name, tc := range map[string]struct {
		file    string
		refused bool
	}{
		"the XR":                 {good + block, false},
		"the run controller":     {good + runs + block, true},
		"nothing enforced there": {good + runs + "hardening: {mode: audit}\n", false},
		"per-run tokens":         {good + runs + "runTokens: {enabled: true, perRun: true, appIDFile: /a/app_id, privateKeyFile: /a/private_key}\n" + block, false},
		"the shared writer":      {good + runs + "runTokens: {enabled: true, appIDFile: /a/app_id, privateKeyFile: /a/private_key}\n" + block, true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.file))
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, want refused %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), "HR13") {
				t.Fatalf("err %v does not name HR13", err)
			}
		})
	}
}

// Stage H3 (HR34): push is enforceable under mode audit, alone or with the other four, centrally
// or per repository.
func TestEnforcePush(t *testing.T) {
	for name, block := range map[string]string{
		"all five, a repository": "hardening:\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [push, pr, comment, labels, issue]}}\n",
		"push alone, a repository": "hardening:\n" + enforcePublisher +
			"  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [push]}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(good + block))
			if err != nil {
				t.Fatal(err)
			}
			if !c.Hardening.EnforcesPush() || !slices.Contains(c.Hardening.EnforcedFor("Smana/cloud-native-ref"), hardening.Push) {
				t.Fatalf("push not enforced: %v", c.Hardening.EnforcedFor("Smana/cloud-native-ref"))
			}
		})
	}
	c, err := Parse([]byte(good + "hardening:\n" + enforcePublisher +
		"  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [pr]}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hardening.EnforcesPush() {
		t.Fatal("pr alone enforces push")
	}
}

func TestHashCoversPush(t *testing.T) {
	base := "hardening:\n" + enforcePublisher + "  repositories: {Smana/cloud-native-ref: {mode: audit, enforceOutputs: [pr%s]}}\n"
	a, err := Parse([]byte(good + fmt.Sprintf(base, "")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(good + fmt.Sprintf(base, ", push")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Fatal("adding push leaves the hash unchanged")
	}
}

// HR27: a protected-path push waits 24h by default, and approvalTTL is within (0, 168h]; the
// mirror's size limit is 1 GiB by default, and at least 64 MiB.
func TestApproverTTL(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Hardening.Applier; a.ApprovalTTL.Duration != 24*time.Hour || a.MirrorMaxBytes != 1<<30 {
		t.Fatalf("defaults %+v", a)
	}
	c, err = Parse([]byte(good + "hardening: {applier: {approvalTTL: 168h, mirrorMaxBytes: 134217728}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Hardening.Applier; a.ApprovalTTL.Duration != 168*time.Hour || a.MirrorMaxBytes != 128<<20 {
		t.Fatalf("set %+v", a)
	}
	for name, tc := range map[string]struct{ block, want string }{
		"zero":           {"hardening: {applier: {approvalTTL: 0s}}\n", "hardening.applier.approvalTTL"},
		"negative":       {"hardening: {applier: {approvalTTL: -1h}}\n", "hardening.applier.approvalTTL"},
		"over a week":    {"hardening: {applier: {approvalTTL: 169h}}\n", "hardening.applier.approvalTTL"},
		"a zero mirror":  {"hardening: {applier: {mirrorMaxBytes: 0}}\n", "hardening.applier.mirrorMaxBytes"},
		"a small mirror": {"hardening: {applier: {mirrorMaxBytes: 1024}}\n", "hardening.applier.mirrorMaxBytes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(good + tc.block)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// enforceAll is a repository at mode enforce with all five types, its publisher and the broker's
// system principal (HR34, HR35).
const enforceAll = "hardening:\n  mode: audit\n  repositories: {Smana/cloud-native-ref: {mode: enforce, enforceOutputs: [comment, issue, labels, pr, push]}}\n" + enforcePublisher

// HR34, HR36: mode enforce validates on the run-controller with all five types enforced and a
// system:broker; it fails on the XR, with fewer types, or with no broker principal.
func TestModeEnforce(t *testing.T) {
	broker := "  systemIssuer: https://k8s\n  systemJWKS: https://k8s/jwks\n  systemPrincipals: {sa: system:broker}\n"
	if _, err := Parse([]byte(withAPI(good+perRunTokens+enforceAll, broker))); err != nil {
		t.Fatalf("enforce on the run-controller: %v", err)
	}
	for name, tc := range map[string]struct{ cfg, want string }{
		"on the XR":       {withAPI(good+enforceAll, broker), "R5 cannot hold on the XR"},
		"four types":      {withAPI(good+perRunTokens+strings.Replace(enforceAll, "pr, push", "pr", 1), broker), "all five output types"},
		"no broker":       {good + perRunTokens + enforceAll, "system:broker"},
		"central enforce": {withAPI(good+perRunTokens+"hardening: {mode: enforce}\n", broker), "all five output types"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.cfg)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// withAPI adds lines under the config's api block.
func withAPI(cfg, lines string) string { return strings.Replace(cfg, "api:\n", "api:\n"+lines, 1) }
