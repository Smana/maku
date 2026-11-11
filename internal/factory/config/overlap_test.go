// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestOverlapRolloutDefaultsAndModes(t *testing.T) {
	for name, tc := range map[string]struct {
		block             string
		want              OverlapMode
		scoperEnforcement bool
	}{
		"omitted block":               {"", OverlapOff, false},
		"omitted overlap":             {"intake: {}\n", OverlapOff, false},
		"omitted mode":                {"intake: {overlap: {scoperEnforcement: true}}\n", OverlapOff, true},
		"unquoted off":                {"intake: {overlap: {mode: off}}\n", OverlapOff, false},
		"quoted off":                  {"intake: {overlap: {mode: \"off\"}}\n", OverlapOff, false},
		"YAML false is off":           {"intake: {overlap: {mode: false}}\n", OverlapOff, false},
		"shadow":                      {"intake: {overlap: {mode: shadow}}\n", OverlapShadow, false},
		"enforce":                     {"intake: {overlap: {mode: enforce}}\n", OverlapEnforce, false},
		"scoper enforcement opted in": {"intake: {overlap: {mode: enforce, scoperEnforcement: true}}\n", OverlapEnforce, true},
		"scoper selector in shadow":   {"intake: {overlap: {mode: shadow, scoperEnforcement: true}}\n", OverlapShadow, true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(good + tc.block))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Intake.Overlap.Mode; got != tc.want {
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
			if got := cfg.Intake.Overlap.ScoperEnforcement; got != tc.scoperEnforcement {
				t.Errorf("scoperEnforcement = %t, want %t", got, tc.scoperEnforcement)
			}
		})
	}
}

func TestOverlapRolloutRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ block, want string }{
		"unknown mode":           {"intake: {overlap: {mode: sometimes}}\n", "intake.overlap.mode"},
		"capitalized mode":       {"intake: {overlap: {mode: Enforce}}\n", "intake.overlap.mode"},
		"true is not a mode":     {"intake: {overlap: {mode: true}}\n", "intake.overlap.mode"},
		"YAML yes is not a mode": {"intake: {overlap: {mode: yes}}\n", "intake.overlap.mode"},
		"unknown overlap key":    {"intake: {overlap: {mode: shadow, repository: payload}}\n", "unknown field"},
		"unknown intake key":     {"intake: {overlap: {mode: shadow}, unrestrictedSearch: true}\n", "unknown field"},
		"duplicate mode":         {"intake:\n  overlap:\n    mode: shadow\n    mode: enforce\n", "intake.overlap.mode is written twice"},
		"duplicate scoper flag":  {"intake:\n  overlap:\n    scoperEnforcement: false\n    scoperEnforcement: true\n", "intake.overlap.scoperEnforcement is written twice"},
		"duplicate overlap":      {"intake:\n  overlap: {mode: shadow}\n  overlap: {mode: enforce}\n", "intake.overlap is written twice"},
		"duplicate intake":       {"intake: {overlap: {mode: shadow}}\nintake: {overlap: {mode: enforce}}\n", "intake is written twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + tc.block))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestOverlapZeroValueIsValidWithoutDefaulting(t *testing.T) {
	var cfg Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("the otherwise empty config should fail its required fields")
	}
	if strings.Contains(err.Error(), "intake.overlap.mode") {
		t.Fatalf("the zero-value overlap mode must mean off: %v", err)
	}
}

func TestOverlapRolloutChangesConfigHash(t *testing.T) {
	base, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := Parse([]byte(good + "intake: {overlap: {mode: shadow}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	enforce, err := Parse([]byte(good + "intake: {overlap: {mode: enforce}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	scoper, err := Parse([]byte(good + "intake: {overlap: {mode: enforce, scoperEnforcement: true}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if base.Hash == shadow.Hash || shadow.Hash == enforce.Hash || enforce.Hash == scoper.Hash {
		t.Fatalf("rollout changes must change the config hash: base=%s shadow=%s enforce=%s scoper=%s",
			base.Hash, shadow.Hash, enforce.Hash, scoper.Hash)
	}
}
