// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// TestTheSpecsTwoDefinitionsCompile verifies the design spec's standard and careful YAML, verbatim,
// compile to the same Stages and budget as the phase E presets (plus the list form's checks, stall
// and gate switches).
func TestTheSpecsTwoDefinitionsCompile(t *testing.T) {
	withPrices := strings.Replace(noTemplates, "  logsURL:",
		"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1)
	specYAML := `workflows:
  standard:
    description: "An implementer and a reviewer. The default."
    budget: standard
    stepUp: careful
    stages:
      - {role: implementer, checks: true, stall: true}
      - {role: reviewer, maxRounds: 2}
  careful:
    description: "Planned first, judged before review. For wide or unclear tasks."
    budget: frontier
    stages:
      - {role: scoper, split: {maxParts: 4}, gate: {when: plan-first}}
      - {role: tester, when: repository.testFirst}
      - {role: implementer, checks: true, search: true, stall: true, judge: evidence}
      - {role: reviewer, maxRounds: 2}
`
	c := mustParse(t, withPrices+gatewayIdentity+specYAML)
	presets := mustParse(t, withPrices+gatewayIdentity)

	repoOff := &Repository{Name: "acme/app"}
	repoOn := &Repository{Name: "acme/app", Workflow: RepositoryWorkflow{TestFirst: true}}

	// Budget matches phase E presets.
	if got := c.HarnessProfiles[WorkflowStandard].EffectiveTier(); got != "standard" {
		t.Fatalf("standard budget = %q, want standard", got)
	}
	if got := c.HarnessProfiles[WorkflowCareful].EffectiveTier(); got != "frontier" {
		t.Fatalf("careful budget = %q, want frontier", got)
	}

	// Compiled stages match phase E's resolved stage names and switches on both repositories.
	for _, tc := range []struct {
		name string
		wf   string
		repo *Repository
	}{
		{"standard", WorkflowStandard, repoOn},
		{"careful/testFirst-off", WorkflowCareful, repoOff},
		{"careful/testFirst-on", WorkflowCareful, repoOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.StagesFor(tc.repo, tc.wf, "")
			want := presets.StagesFor(tc.repo, tc.wf, "")
			if !slices.Equal(got.Names(), want.Names()) {
				t.Fatalf("Names() = %v, want %v", got.Names(), want.Names())
			}
			if got.Ask != want.Ask || got.Plan != want.Plan || got.Tests != want.Tests ||
				got.Search != want.Search || got.Judge != want.Judge || got.Review != want.Review ||
				got.Stall != want.Stall {
				t.Fatalf("stages = %+v, want %+v", got, want)
			}
			if (got.Split == nil) != (want.Split == nil) || (got.Split != nil && *got.Split != *want.Split) {
				t.Fatalf("split = %+v, want %+v", got.Split, want.Split)
			}
			if !got.Checks {
				t.Fatalf("spec %s sets checks: true, got Checks=false", tc.wf)
			}
		})
	}

	// Direct compile check for template, gate, and maxRounds.
	stdSt, stdTmpl, err := c.compile(WorkflowStandard, c.HarnessProfiles[WorkflowStandard])
	if err != nil {
		t.Fatalf("compile standard: %v", err)
	}
	if !stdSt.Checks || !stdSt.Stall || stdSt.Gate != "" || !slices.Equal(stdTmpl.Roles, []string{"implementer", "reviewer"}) || stdTmpl.MaxReviewRounds != 2 {
		t.Fatalf("standard compiled = (%+v, %+v)", stdSt, stdTmpl)
	}
	careSt, careTmpl, err := c.compile(WorkflowCareful, c.HarnessProfiles[WorkflowCareful])
	if err != nil {
		t.Fatalf("compile careful: %v", err)
	}
	if !careSt.Checks || !careSt.Stall || careSt.Gate != GateWhenPlanFirst || careSt.Judge != DefaultJudge ||
		careSt.Split == nil || careSt.Split.MaxChildren != 4 || !slices.Equal(careTmpl.Roles, []string{"implementer", "reviewer"}) || careTmpl.MaxReviewRounds != 2 {
		t.Fatalf("careful compiled = (%+v, %+v)", careSt, careTmpl)
	}
}

func TestAStageListIsLinear(t *testing.T) {
	base := strings.Replace(noTemplates, "  logsURL:",
		"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1) + gatewayIdentity

	cases := map[string]struct {
		stages string
		want   string
	}{
		"missing implementer": {
			stages: `[{role: scoper}, {role: reviewer, maxRounds: 2}]`,
			want:   "exactly one implementer",
		},
		"two implementers": {
			stages: `[{role: implementer}, {role: implementer}]`,
			want:   "duplicate role",
		},
		"duplicate reviewer": {
			stages: `[{role: implementer}, {role: reviewer}, {role: reviewer}]`,
			want:   "duplicate role",
		},
		"reviewer before implementer": {
			stages: `[{role: reviewer}, {role: implementer}]`,
			want:   "out of order",
		},
		"scoper after implementer": {
			stages: `[{role: implementer}, {role: scoper}]`,
			want:   "out of order",
		},
		"tester after implementer": {
			stages: `[{role: implementer}, {role: tester}]`,
			want:   "out of order",
		},
		"tester before scoper": {
			stages: `[{role: tester}, {role: scoper}, {role: implementer}]`,
			want:   "out of order",
		},
		"unknown stage role": {
			stages: `[{role: wizard}, {role: implementer}]`,
			want:   `unknown role "wizard"`,
		},
		"gate on implementer": {
			stages: `[{role: implementer, gate: {when: always}}]`,
			want:   "does not accept when, split or gate",
		},
		"invalid gate.when": {
			stages: `[{role: scoper, gate: {when: never}}, {role: implementer}]`,
			want:   `gate.when "never"`,
		},
		"split maxParts out of range": {
			stages: `[{role: scoper, split: {maxParts: 1}}, {role: implementer}]`,
			want:   "split.maxParts must be 2..5",
		},
		"invalid tester when": {
			stages: `[{role: tester, when: always}, {role: implementer}]`,
			want:   `when "always"`,
		},
		"reviewer maxRounds out of range": {
			stages: `[{role: implementer}, {role: reviewer, maxRounds: 9}]`,
			want:   "maxRounds must be 0..5",
		},
		"over max 8 stages": {
			stages: `[{role: scoper}, {role: tester}, {role: implementer}, {role: reviewer}, {role: reviewer}, {role: reviewer}, {role: reviewer}, {role: reviewer}, {role: reviewer}]`,
			want:   "at most 8",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := base + "workflows:\n  custom:\n    budget: standard\n    stages: " + tc.stages + "\n"
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestStagesAndTemplateTogetherAreRefused(t *testing.T) {
	for name, wf := range map[string]string{
		"stages with template": `template: pair, stages: [{role: implementer}]`,
		"stages with tier":     `tier: frontier, stages: [{role: implementer}]`,
		"stages with loop":     `loop: default, stages: [{role: implementer}]`,
		"stages with checks":   `checks: true, stages: [{role: implementer}]`,
		"budget with tier":     `template: pair, tier: frontier, budget: frontier`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := noTemplates + "workflows:\n  mixed: {" + wf + "}\n"
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "not both") {
				t.Fatalf("got %v, want mutual-exclusion error", err)
			}
		})
	}
}

func TestAPlatformsOldWorkflowStillLoads(t *testing.T) {
	raw := strings.Replace(good, "caps:", `workflows:
  legacy: {template: pair, tier: frontier, checks: true, loop: legacy}
loops:
  default: {scope: "off", stallSteps: 80, judge: none}
  legacy: {scope: scoper, search: true, judge: tier-light}
caps:`, 1)
	c := mustParse(t, raw)

	if got := c.TemplateFor("legacy"); got != "pair" {
		t.Fatalf("TemplateFor(legacy) = %q, want pair", got)
	}
	st := c.StagesFor(nil, "legacy", "")
	if !st.Plan || !st.Search || !st.Review || !st.Checks || !st.Stall || st.Judge != DefaultJudge {
		t.Fatalf("legacy stages = %+v", st)
	}
	// Task I1 Q2: explicit templates block warns in release I while still loading.
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "templates is deprecated") }) {
		t.Fatalf("expected templates deprecation warning, got %v", c.Warnings)
	}
}

func TestStagesCarriesChecksStallAndGate(t *testing.T) {
	raw := noTemplates + `workflows:
  gated:
    budget: standard
    stages:
      - {role: scoper, gate: {when: always}}
      - {role: implementer, checks: true, stall: true}
      - {role: reviewer, maxRounds: 3}
`
	c := mustParse(t, raw)
	st := c.StagesFor(nil, "gated", "")
	if !st.Plan || !st.Checks || !st.Stall || st.Gate != GateWhenAlways || !st.Review {
		t.Fatalf("gated stages = %+v", st)
	}
	if got := c.MaxReviewRoundsFor("gated", "pair"); got != 3 {
		t.Fatalf("MaxReviewRoundsFor = %d, want 3", got)
	}
	if !c.HarnessProfiles["gated"].EffectiveChecks() {
		t.Fatal("EffectiveChecks() = false, want true")
	}
}
