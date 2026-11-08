// SPDX-License-Identifier: Apache-2.0

package config

import (
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

// Stage H0 ships contracts only: any mode but off, centrally or per repository, fails startup,
// as does a value the design does not define or a label an agent must never choose.
func TestHardeningRefusals(t *testing.T) {
	for name, c := range map[string]struct{ block, want string }{
		"audit, before H1":           {"hardening: {mode: audit}\n", "stage H0"},
		"enforce, before H1":         {"hardening: {mode: enforce}\n", "stage H0"},
		"a repository in audit":      {"hardening: {repositories: {Smana/cloud-native-ref: {mode: audit}}}\n", "stage H0"},
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

// A repository may only be stricter than the central mode. Tested on the rule itself: H0 refuses
// every mode but off, so no file can reach it yet.
func TestHardeningRepositoryStricterOnly(t *testing.T) {
	h := Hardening{Mode: hardening.ModeEnforce, Repositories: map[string]HardeningRepository{"a/b": {Mode: hardening.ModeAudit}, "c/d": {Mode: hardening.ModeEnforce}}}
	var errs []string
	h.validateStricter(func(f string, a ...any) { errs = append(errs, f) })
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
