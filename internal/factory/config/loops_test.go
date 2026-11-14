// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// Loop policy part 2: a class's row, else default's, switches its loop on; an unset key takes
// the design's default; no row, or a row with none of the three keys, is today's run (D4).
func TestLoopFor(t *testing.T) {
	cfg, err := Parse([]byte(good + `loops:
  default: {}
  docs: {stallSteps: 40, continuations: 0, judge: none}
  tests: {judge: tier-standard}
`))
	if err != nil {
		t.Fatal(err)
	}
	for class, want := range map[string]*LoopPolicy{
		"docs":       {StallSteps: 40, Continuations: 0, JudgeModel: ""},
		"tests":      {StallSteps: 80, Continuations: 2, JudgeModel: "tier-standard"},
		"dashboards": nil, // default's row sets no key: off
		"review":     nil,
	} {
		got := cfg.LoopFor(class)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("LoopFor(%s) = %+v, want %+v", class, got, want)
		}
	}
	on, err := Parse([]byte(good + "loops: {default: {stallSteps: 90}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := on.LoopFor("review"); got == nil || *got != (LoopPolicy{90, 2, "tier-light"}) {
		t.Errorf("default's row covers review and every class without a row: %+v", got)
	}
	none, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if got := none.LoopFor("docs"); got != nil {
		t.Errorf("no loops: %+v, want off", got)
	}
}

func TestLoopsAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ loops, want string }{
		"an undeclared class":   {"{scripts: {stallSteps: 40}}", `loops.scripts: "scripts" is not default, review or a class`},
		"stallSteps 0":          {"{docs: {stallSteps: 0}}", "loops.docs.stallSteps must be 1..500"},
		"stallSteps 501":        {"{docs: {stallSteps: 501}}", "loops.docs.stallSteps must be 1..500"},
		"continuations 6":       {"{docs: {continuations: 6}}", "loops.docs.continuations must be 0..5"},
		"a model, not an alias": {"{docs: {judge: claude-haiku-5-5}}", `loops.docs.judge "claude-haiku-5-5" is none or one of`},
		"an unknown key":        {"{docs: {owner: me}}", `unknown field "owner"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + "loops: " + c.loops + "\n"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// D5: the portable AgentRun carries spec.loop and the run-controller passes it as AGENT_LOOP, so
// the loop policy parses on the portable runtime.
func TestLoopParsesOnThePortableRuntime(t *testing.T) {
	c, err := Parse([]byte(good + "loops: {docs: {stallSteps: 40, judge: tier-light}}\nruns: {group: maku.ogenki.io}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p := c.LoopFor("docs"); p == nil || p.StallSteps != 40 {
		t.Fatalf("docs' loop %+v", p)
	}
}

// PR4: loops.<class>.models picks a provider alias the gateway resolves; a portable runtime without
// runs.gatewayIdentity runs every role on modelSecret's model, so a models row is refused there,
// never silently ignored. With the key (GW10) it parses (TestGatewayIdentity).
func TestLoopModelsAreRefusedOnThePortableRuntime(t *testing.T) {
	const row = "loops: {docs: {models: {implementer: {frontier: kimi-frontier}}}}\n"
	_, err := Parse([]byte(byModel + row + "runs: {group: maku.ogenki.io}\n"))
	const want = "loops.docs.models needs runs.gatewayIdentity (a model gateway, rollout step 4): this runtime runs every role on modelSecret's model"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("portable: %v, want %q", err, want)
	}
	if _, err := Parse([]byte(byModel + row + "runs: {group: cloud.ogenki.io}\n")); err == nil || !strings.Contains(err.Error(), "removed in v4 step 8 (D5)") {
		t.Fatalf("cloud-native-ref's AgentRun: %v", err)
	}
	if _, err := Parse([]byte(byModel + row + "runs: {group: maku.ogenki.io, gatewayIdentity: true}\n")); err != nil {
		t.Fatalf("portable with a gateway identity: %v", err)
	}
}

// WF10: a row inherits default's row key by key; the keys it sets are its own.
func TestLoopRowInheritsDefaultKeyByKey(t *testing.T) {
	c := mustParse(t, good+gatewayIdentity+`loops:
  default: {scope: scoper, search: true, judge: tier-standard, models: {reviewer: {frontier: kimi-frontier}, implementer: {light: mistral-light}}}
  docs: {stallSteps: 60, models: {implementer: {standard: mistral-standard}}}
`)
	if c.ScopeFor("docs") != ScopeScoper || !c.SearchFor("docs") {
		t.Fatal("docs did not inherit default's scope and search")
	}
	if p := c.LoopFor("docs"); p == nil || p.StallSteps != 60 || p.JudgeModel != "tier-standard" {
		t.Fatalf("docs's loop: %+v, want its own stallSteps and default's judge", p)
	}
	for _, m := range []struct{ role, tier, want string }{
		{"reviewer", "frontier", "kimi-frontier"},       // default's role
		{"implementer", "light", "mistral-light"},       // default's tier of a role the row also sets
		{"implementer", "standard", "mistral-standard"}, // the row's own
	} {
		if got := c.ModelFor("docs", "public", m.role, m.tier); got != m.want {
			t.Fatalf("ModelFor(docs, %s, %s) = %q, want %q", m.role, m.tier, got, m.want)
		}
	}
	if c.Loops["docs"].Search || c.Loops["docs"].Scope != "" {
		t.Fatal("inheritance rewrote the row as written")
	}
}

// An explicit value on the row, off or false included, beats default's.
func TestLoopRowOverridesDefault(t *testing.T) {
	c := mustParse(t, good+`loops:
  default: {scope: scoper, search: true, judge: tier-standard}
  docs: {scope: "off", search: false, judge: none}
  tests: {scope: steering}
`)
	if c.ScopeFor("docs") != ScopeOff || c.SearchFor("docs") {
		t.Fatal("an explicit off or false was overridden")
	}
	if p := c.LoopFor("docs"); p == nil || p.JudgeModel != "" {
		t.Fatalf("judge: none was overridden: %+v", p)
	}
	if c.ScopeFor("tests") != ScopeSteering || !c.SearchFor("tests") {
		t.Fatal("tests: its own scope, default's search")
	}
}

func TestLoopInheritanceWarnsWhenItSwitchesALoopOn(t *testing.T) {
	c := mustParse(t, good+gatewayIdentity+`loops: {default: {judge: tier-light}, revert: {models: {reviewer: {frontier: kimi-frontier}}}, docs: {stallSteps: 40}}
`)
	want := "loops.revert now inherits default's judge: its loop switches on"
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, want) }) {
		t.Fatalf("no warning; got %v", c.Warnings)
	}
	// docs inherits the judge too, but its own stallSteps switched its loop on already.
	if !slices.ContainsFunc(c.Warnings, func(w string) bool {
		return strings.Contains(w, "loops.docs now inherits default's judge") && !strings.Contains(w, "switches on")
	}) {
		t.Fatalf("docs: %v", c.Warnings)
	}
	if c.LoopFor("revert") == nil {
		t.Fatal("revert's loop is off")
	}
}
