// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

func TestThePresetsResolveToTheirStages(t *testing.T) {
	withPrices := strings.Replace(noTemplates, "  logsURL:",
		"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1)
	c := mustParse(t, withPrices+gatewayIdentity)

	repoOff := &Repository{Name: "acme/app"}
	repoTestFirst := &Repository{Name: "acme/app", Workflow: RepositoryWorkflow{TestFirst: true}}

	quick := c.StagesFor(repoTestFirst, WorkflowQuick, "")
	if !quick.Ask || quick.Plan || quick.Tests || quick.Search || quick.Judge != "" || quick.Review || quick.Split != nil {
		t.Fatalf("quick stages = %+v", quick)
	}
	if got := quick.Names(); !slices.Equal(got, []string{"ask"}) {
		t.Fatalf("quick names = %v", got)
	}

	std := c.StagesFor(repoTestFirst, WorkflowStandard, "")
	if !std.Ask || std.Plan || std.Tests || std.Search || std.Judge != "" || !std.Review || std.Split != nil {
		t.Fatalf("standard stages = %+v", std)
	}
	if got := std.Names(); !slices.Equal(got, []string{"ask", "review"}) {
		t.Fatalf("standard names = %v", got)
	}

	carefulOff := c.StagesFor(repoOff, WorkflowCareful, "")
	if !carefulOff.Ask || !carefulOff.Plan || carefulOff.Tests || !carefulOff.Search || carefulOff.Judge != DefaultJudge || !carefulOff.Review || carefulOff.Split == nil {
		t.Fatalf("careful (testFirst off) = %+v", carefulOff)
	}
	if got := carefulOff.Names(); !slices.Equal(got, []string{"ask", "plan", "search", "judge", "review", "split"}) {
		t.Fatalf("careful (testFirst off) names = %v", got)
	}

	carefulOn := c.StagesFor(repoTestFirst, WorkflowCareful, "")
	if !carefulOn.Tests {
		t.Fatalf("careful (testFirst on) should enable tests: %+v", carefulOn)
	}
	if got := carefulOn.Names(); !slices.Equal(got, []string{"ask", "plan", "tests", "search", "judge", "review", "split"}) {
		t.Fatalf("careful (testFirst on) names = %v", got)
	}

	if none := c.StagesFor(repoTestFirst, TemplateNone, ""); len(none.Names()) != 0 {
		t.Fatalf("none stages = %+v", none)
	}
}

func TestAPlatformRowWithoutTheNewKeysResolvesAsBefore(t *testing.T) {
	c := mustParse(t, strings.Replace(noTemplates, "caps:", `workflows:
  fast: {template: solo}
  normal: {template: pair}
  deep: {template: pair, loop: deep}
loops:
  default: {scope: off, stallSteps: 80, judge: none}
  deep: {scope: scoper, search: true, judge: tier-light}
caps:`, 1))

	if s := c.StagesFor(nil, "fast", ""); s.Ask || s.Plan || s.Review || s.Judge != "" {
		t.Fatalf("legacy solo = %+v", s)
	}
	if s := c.StagesFor(nil, "normal", ""); s.Ask || s.Plan || !s.Review || s.Judge != "" {
		t.Fatalf("legacy pair = %+v", s)
	}
	if s := c.StagesFor(nil, "deep", ""); !s.Ask || !s.Plan || !s.Search || !s.Review || s.Judge != DefaultJudge {
		t.Fatalf("legacy scoper row = %+v", s)
	}
}

func TestPlanAndScopeMayNotDisagree(t *testing.T) {
	for name, row := range map[string]string{
		"plan true with scope off":      "plan: true, scope: off",
		"plan true with scope steering": "plan: true, scope: steering",
		"plan false with scope scoper":  "plan: false, scope: scoper",
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(noTemplates, "caps:", "loops:\n  default: {"+row+"}\ncaps:", 1)
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "contradicts") {
				t.Fatalf("want contradiction error, got %v", err)
			}
		})
	}
	for name, row := range map[string]string{
		"plan true with scope scoper":    "plan: true, scope: scoper",
		"plan false with scope off":      "plan: false, scope: off",
		"plan false with scope steering": "plan: false, scope: steering",
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(noTemplates, "caps:", "loops:\n  default: {"+row+"}\ncaps:", 1)
			if _, err := Parse([]byte(raw)); err != nil {
				t.Fatalf("agreeing plan and scope should parse: %v", err)
			}
		})
	}
}

func TestReviewOffOnAPairTemplateIsRefused(t *testing.T) {
	for name, block := range map[string]string{
		"pair with review false": "workflows:\n  checked: {template: pair}\nloops:\n  checked: {review: false}\n",
		"solo with review true":  "workflows:\n  checked: {template: solo}\nloops:\n  checked: {review: true}\n",
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(noTemplates, "caps:", block+"caps:", 1)
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "contradicting") {
				t.Fatalf("want template/review contradiction, got %v", err)
			}
		})
	}
}

func TestACustomWorkflowMayRunTheJudgeAlone(t *testing.T) {
	raw := strings.Replace(noTemplates, "caps:", `workflows:
  judged: {}
  judged-solo: {}
loops:
  judged: {judge: tier-light}
  judged-solo: {judge: tier-light, review: false}
caps:`, 1)
	c := mustParse(t, raw)

	s := c.StagesFor(nil, "judged", "")
	if !s.Ask || s.Plan || s.Search || s.Split != nil || s.Judge != DefaultJudge || !s.Review {
		t.Fatalf("judged stages = %+v", s)
	}
	if got := c.TemplateFor("judged"); got != "pair" {
		t.Fatalf("TemplateFor(judged) = %q, want pair", got)
	}

	solo := c.StagesFor(nil, "judged-solo", "")
	if !solo.Ask || solo.Plan || solo.Search || solo.Split != nil || solo.Judge != DefaultJudge || solo.Review {
		t.Fatalf("judged-solo stages = %+v", solo)
	}
	if got := c.TemplateFor("judged-solo"); got != "solo" {
		t.Fatalf("TemplateFor(judged-solo) = %q, want solo", got)
	}
}

func TestAClassRequirementAddsStagesToAnyWorkflow(t *testing.T) {
	c := mustParse(t, noTemplates)
	repo := &Repository{
		Name:  "acme/app",
		Merge: Merge{ContractCheck: "contract-tests"},
		Classes: map[string]Class{
			"infra": {Requires: []string{"plan", "tests", "judge", "review"}, AllowedFiles: []string{`^infrastructure/.*$`}},
		},
	}
	quick := c.StagesFor(repo, WorkflowQuick, "infra")
	if !quick.Ask || !quick.Plan || !quick.Tests || quick.Judge != DefaultJudge || !quick.Review {
		t.Fatalf("quick under infra = %+v, want ask+plan+tests+judge+review", quick)
	}
	// Without merge.contractCheck, tests is not added.
	repoNoContract := &Repository{
		Name: "acme/app",
		Classes: map[string]Class{
			"infra": {Requires: []string{"tests", "review"}, AllowedFiles: []string{`^infrastructure/.*$`}},
		},
	}
	quickNoContract := c.StagesFor(repoNoContract, WorkflowQuick, "infra")
	if quickNoContract.Tests || !quickNoContract.Review {
		t.Fatalf("quick without contractCheck = %+v, want tests=false review=true", quickNoContract)
	}
}

func TestARequirementNeverRemovesAStage(t *testing.T) {
	withPrices := strings.Replace(noTemplates, "  logsURL:",
		"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1)
	c := mustParse(t, withPrices+gatewayIdentity)
	repo := &Repository{
		Name:     "acme/app",
		Workflow: RepositoryWorkflow{TestFirst: true},
		Merge:    Merge{ContractCheck: "contract-tests"},
		Classes: map[string]Class{
			"narrow": {Requires: []string{"plan"}, AllowedFiles: []string{`^docs/.*$`}},
		},
	}
	got := c.StagesFor(repo, WorkflowCareful, "narrow")
	if !got.Ask || !got.Plan || !got.Tests || !got.Search || got.Judge != DefaultJudge || !got.Review || got.Split == nil {
		t.Fatalf("careful under narrow class lost a stage: %+v", got)
	}
}
