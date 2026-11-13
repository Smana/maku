// SPDX-License-Identifier: Apache-2.0

package config

import (
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
	if _, err := Parse([]byte(byModel + row + "runs: {group: cloud.ogenki.io}\n")); err != nil {
		t.Fatalf("cloud-native-ref's AgentRun: %v", err)
	}
	if _, err := Parse([]byte(byModel + row + "runs: {group: maku.ogenki.io, gatewayIdentity: true}\n")); err != nil {
		t.Fatalf("portable with a gateway identity: %v", err)
	}
}
