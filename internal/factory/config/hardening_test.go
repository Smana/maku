// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
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
		"enforce, before H3":         {"hardening: {mode: enforce}\n", "stage H3"},
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

// Enforcement is stage H3's: this release refuses it, centrally and per repository, so R1 never holds.
func TestHardeningEnforceStillRefused(t *testing.T) {
	for name, block := range map[string]string{
		"central":         "hardening: {mode: enforce}\n",
		"a repository":    "hardening: {repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
		"over audit":      "hardening: {mode: audit, repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
		"both in enforce": "hardening: {mode: enforce, repositories: {Smana/cloud-native-ref: {mode: enforce}}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + block))
			if err == nil || !strings.Contains(err.Error(), "enforce arrives with stage H3") {
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
