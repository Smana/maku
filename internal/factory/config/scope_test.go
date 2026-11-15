// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// Scoper design SC2, offline proof 1: the switch parses, defaults to off, a row inherits default's
// scope (WF10) and its own off is kept, and
// scope never switches the loop on (loop part 2 R2). The test writes the bare form {scope: off},
// which YAML 1.1 reads as false: it must still mean off.
func TestScopeFor(t *testing.T) {
	cfg, err := Parse([]byte(good + `loops:
  default: {scope: steering}
  standard: {scope: scoper}
  careful: {stallSteps: 40}
  quick: {scope: off}
`))
	if err != nil {
		t.Fatal(err)
	}
	for class, want := range map[string]ScopeMode{"standard": ScopeScoper, "careful": ScopeSteering, "quick": ScopeOff,
		"review": ScopeSteering, "docs-links": ScopeSteering} {
		if got := cfg.ScopeFor(class); got != want {
			t.Errorf("ScopeFor(%s) = %s, want %s", class, got, want)
		}
	}
	if cfg.LoopFor("standard") != nil || cfg.LoopFor("review") != nil {
		t.Error("a scope row switched the loop on")
	}
	none, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if got := none.ScopeFor("standard"); got != ScopeOff {
		t.Errorf("no loops: %s, want off (D4)", got)
	}
}

// WF10: a row with only scope inherits default's loop, and the loop switches on; default's scope
// off leaves a row's own scope alone.
func TestAScopeOnlyRowInheritsDefaultsLoop(t *testing.T) {
	cfg, err := Parse([]byte(good + `loops:
  default: {stallSteps: 40}
  standard: {scope: steering}
`))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.LoopFor("standard"); p == nil || p.StallSteps != 40 {
		t.Errorf("standard did not inherit default's loop: %+v", p)
	}
	if cfg.LoopFor("quick") == nil {
		t.Error("a workflow without a row gets default's loop")
	}
	if cfg.ScopeFor("standard") != ScopeSteering || cfg.ScopeFor("quick") != ScopeOff || !cfg.SteeringOn() {
		t.Error("scope follows the row")
	}
}

// YAML 1.1 reads bare false and no as the boolean false: both are off. yes is true, which is refused.
func TestYAML11BooleansInScope(t *testing.T) {
	for _, v := range []string{"false", "no", "off"} {
		cfg, err := Parse([]byte(good + "loops: {standard: {scope: " + v + "}}\n"))
		if err != nil {
			t.Fatalf("scope: %s: %v", v, err)
		}
		if got := cfg.ScopeFor("standard"); got != ScopeOff {
			t.Errorf("scope: %s = %q, want off", v, got)
		}
	}
	_, err := Parse([]byte(good + "loops: {standard: {scope: yes}}\n"))
	if err == nil || !strings.Contains(err.Error(), "scope is off, steering or scoper") {
		t.Fatalf("scope: yes: err = %v", err)
	}
}

func TestScopesAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ loops, want string }{
		"a fourth mode":       {"{standard: {scope: always}}", `loops.standard.scope "always" is off, steering or scoper`},
		"a bare on":           {"{standard: {scope: on}}", "scope is off, steering or scoper"},
		"an undeclared class": {"{scripts: {scope: steering}}", "loops.scripts: a row is named default or a workflow"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + "loops: " + c.loops + "\n"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// D5: the run-controller enforces the scoper's read-only token and its room_handoff path itself
// (the per-run token minted for its role; runctl's Invalid: roomToolsURL), where the token is chosen; the factory no longer
// refuses the scoper on the portable runtime, demo or not (its split is refused there: TestSplitIsRefusedOnThePortableRuntime).
func TestScoperParsesOnThePortableRuntime(t *testing.T) {
	for _, loops := range []string{
		"{standard: {scope: scoper}}",
		"{standard: {scope: steering}}",
	} {
		if _, err := Parse([]byte(priced + "loops: " + loops + "\nruns: {group: maku.ogenki.io}\n")); err != nil {
			t.Errorf("%s on the portable runtime: %v", loops, err)
		}
	}
	if _, err := Parse([]byte(demoGood)); err != nil {
		t.Errorf("the demo's scoper: %v", err)
	}
}

// A split is capped in USD (familyUSD), and portable runs without runs.gatewayIdentity reach no
// meter: the family's budget would never decrease. Refused there, as models is; parses on
// cloud-native-ref's AgentRun, and on the portable kind with the key (GW10).
func TestSplitIsRefusedOnThePortableRuntime(t *testing.T) {
	const want = "loops.standard.split needs runs.gatewayIdentity (a model gateway, rollout step 4): this runtime meters no USD, so familyUSD would never be spent"
	for _, row := range []string{"{standard: {scope: scoper, split: {maxChildren: 3}}}", "{standard: {scope: scoper, split: {maxChildren: 3, familyUSD: 12}}}"} {
		if _, err := Parse([]byte(priced + "loops: " + row + "\nruns: {group: maku.ogenki.io}\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s portable: %v, want %q", row, err, want)
		}
		if _, err := Parse([]byte(priced + "loops: " + row + "\nruns: {group: cloud.ogenki.io}\n")); err == nil || !strings.Contains(err.Error(), "removed in v4 step 8 (D5)") {
			t.Errorf("%s on cloud-native-ref's AgentRun: %v", row, err)
		}
		if _, err := Parse([]byte(priced + "loops: " + row + "\nruns: {group: maku.ogenki.io, gatewayIdentity: true}\n")); err != nil {
			t.Errorf("%s portable with a gateway identity: %v", row, err)
		}
	}
}
