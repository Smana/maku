// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"math"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
)

func TestTheReasonIsBuiltFromFactsOnly(t *testing.T) {
	for _, tc := range []struct{ source, by, rec, trigger, want string }{
		{"repository", "", "", "", "this repository's default"},
		{"repository", "", "quick", "", "this repository's default; the planner suggested quick"},
		{"label", "alice", "", "", "pinned by @alice"},
		{"escalation", "", "", "stalled", "stepped up after a run that committed nothing"},
		{"something-new", "", "", "", "this repository's default"},
	} {
		if got := Reason(tc.source, tc.by, tc.rec, tc.trigger, nil); got != tc.want {
			t.Errorf("Reason(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestTheCardInPlainText(t *testing.T) {
	var b strings.Builder
	c := WorkflowCard{
		Title: "Fix the link checker", Repository: "owner/repo", Workflow: "standard",
		Reason: "this repository's default", Path: []string{"implementer", "reviewer", "CI", "merge gate"},
		CeilingUSD: 5.10,
	}
	if err := WorkflowCardText(&b, c, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	want := "Workflow   standard · this repository's default\n" +
		"Will run   implementer → reviewer → CI → merge gate\n" +
		"Cost       up to $5.10\n"
	if !strings.HasSuffix(b.String(), want) || strings.Contains(b.String(), "tier") {
		t.Fatalf("got:\n%s", b.String())
	}
}

func TestTheCardNamesTheStepUpCeiling(t *testing.T) {
	var b strings.Builder
	c := WorkflowCard{
		Title: "Fix the link checker", Repository: "owner/repo", Workflow: "standard",
		Reason: "this repository's default", Path: []string{"implementer", "reviewer", "CI", "merge gate"},
		CeilingUSD: 5.10, StepUpUSD: 13.60,
	}
	if err := WorkflowCardText(&b, c, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	want := "Cost       up to $5.10 · up to $13.60 if it steps up\n"
	if !strings.Contains(b.String(), want) {
		t.Fatalf("got:\n%s", b.String())
	}
}

func TestTheCardShowsSteppedUpFrom(t *testing.T) {
	card := CardOf(&envelope.TaskFacts{
		Profile: &envelope.ProfileFact{
			Name:            "careful",
			Source:          "escalation",
			Cohort:          "treatment",
			Reason:          "out_of_budget",
			CeilingMicroUSD: 13_600_000,
			StepUp: &envelope.StepUpFact{
				From:            "standard",
				To:              "careful",
				Trigger:         "stalled",
				Mode:            "auto",
				CeilingMicroUSD: 13_600_000,
			},
		},
	}, "Fix the link checker")
	var b strings.Builder
	if err := WorkflowCardText(&b, card, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Cost       up to $13.60 · stepped up from standard\n") {
		t.Fatalf("got:\n%s", b.String())
	}
}

func TestTheCardShowsTheTypicalCostOnlyWhenItExists(t *testing.T) {
	base := WorkflowCard{
		Workflow: "standard", Reason: "this repository's default",
		Path: []string{"implementer", "reviewer", "CI", "merge gate"}, CeilingUSD: 5.10,
	}
	var without strings.Builder
	if err := WorkflowCardText(&without, base, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(without.String(), "Cost       up to $5.10\n") || strings.Contains(without.String(), "typically") {
		t.Fatalf("without typical:\n%s", without.String())
	}

	withTypical := base
	withTypical.TypicalUSD = 0.41
	var with strings.Builder
	if err := WorkflowCardText(&with, withTypical, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with.String(), "Cost       up to $5.10 · typically $0.41\n") {
		t.Fatalf("with typical:\n%s", with.String())
	}
}

func TestTheCardNamesTheRequiringClass(t *testing.T) {
	card := CardOf(&envelope.TaskFacts{
		Plan: []string{"implementer", "reviewer"},
		Profile: &envelope.ProfileFact{
			Name:            "standard",
			Source:          "repository",
			Cohort:          "treatment",
			CeilingMicroUSD: 5_100_000,
			RequiredBy:      "infrastructure",
			RequiredStages:  []string{"plan"},
		},
	}, "Update ingress")
	if !strings.Contains(card.Reason, "; the class infrastructure requires a plan") {
		t.Fatalf("Reason = %q", card.Reason)
	}
	if len(card.Path) == 0 || card.Path[0] != "scoper" {
		t.Fatalf("Path = %v, want scoper prepended", card.Path)
	}
}

func TestTheCardNamesTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  string
		inferred bool
		reviewPR int
		wantRow  string
	}{
		{"default change", "", false, 0, "Outcome    a pull request\n"},
		{"explicit change", "change", false, 0, "Outcome    a pull request\n"},
		{"plan-first", "plan-first", false, 0, "Outcome    a plan to approve, then a pull request\n"},
		{"plan-first inferred", "plan-first", true, 0, "Outcome    a plan to approve, then a pull request (inferred)\n"},
		{"review with PR", "review", false, 42, "Outcome    a review of #42\n"},
		{"report", "report", true, 0, "Outcome    findings on the issue (inferred)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			c := WorkflowCard{
				Outcome:         tc.outcome,
				OutcomeInferred: tc.inferred,
				ReviewPR:        tc.reviewPR,
				Workflow:        "standard",
				Reason:          "this repository's default",
				Path:            []string{"implementer", "reviewer", "CI", "merge gate"},
				CeilingUSD:      5.10,
			}
			if err := WorkflowCardText(&b, c, Options{Format: Text}); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(b.String(), tc.wantRow) {
				t.Fatalf("card does not open with %q:\n%s", tc.wantRow, b.String())
			}
		})
	}
}

// A confirmed cost below the workflow's own ceiling caps the task: the Cost row says so, and a
// step-up above it says it is offered, not run (gcp-0, 2026-11-25; Maku #720).
func TestTheCardSaysAConfirmedCostCapsIt(t *testing.T) {
	cases := []struct {
		name string
		c    WorkflowCard
		want string
	}{
		{"capped", WorkflowCard{Workflow: "careful", CeilingUSD: 5.10, ConfirmedUSD: 5.10, WorkflowCeilingUSD: 13.60},
			"capped at your confirmed $5.10 (careful's ceiling is $13.60)"},
		{"capped, step-up", WorkflowCard{Workflow: "standard", CeilingUSD: 5.10, StepUpUSD: 5.10, ConfirmedUSD: 5.10, WorkflowCeilingUSD: 5.10},
			"up to $5.10 · a step-up stays within your $5.10"},
		{"confirmed above the ceiling", WorkflowCard{Workflow: "standard", CeilingUSD: 5.10, StepUpUSD: 13.60, ConfirmedUSD: 20, WorkflowCeilingUSD: 5.10},
			"up to $5.10 · up to $13.60 if it steps up, within your $20.00"},
		{"a step-up above the confirmed cost", WorkflowCard{Workflow: "standard", CeilingUSD: 5.10, StepUpUSD: 13.60, StepUpTo: "careful", ConfirmedUSD: 5.10, WorkflowCeilingUSD: 5.10},
			"up to $5.10 · a step-up to careful ($13.60) is offered, not run"},
		{"a step-up above the confirmed cost, target unnamed", WorkflowCard{Workflow: "standard", CeilingUSD: 5.10, StepUpUSD: 13.60, ConfirmedUSD: 5.10},
			"up to $5.10 · a step-up ($13.60) is offered, not run"},
		{"a step-up figure equal to the base", WorkflowCard{Workflow: "standard", CeilingUSD: 5.10, StepUpUSD: 5.10},
			"up to $5.10"},
	}
	for _, tc := range cases {
		if got := CostLine(tc.c); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	card := CardOf(&envelope.TaskFacts{Profile: &envelope.ProfileFact{Name: "careful", Source: "label",
		CeilingMicroUSD: 5_100_000, ConfirmedMicroUSD: 5_100_000, WorkflowCeilingMicroUSD: 13_600_000}}, "")
	if got := CostLine(card); got != "capped at your confirmed $5.10 (careful's ceiling is $13.60)" {
		t.Errorf("from facts: %q", got)
	}
}

// A plan's ceiling sums its issues' task ceilings, so its step-up figure sums their step-ups: the
// gcp-0 card read "up to $15.30 · up to $13.60 if it steps up" for three $5.10 issues.
func TestPlanStepUpUSDIsOnThePlansBasis(t *testing.T) {
	for _, c := range []struct {
		name                     string
		plan, task, stepUp, want float64
		issues                   int
	}{
		{"three issues", 15.30, 5.10, 13.60, 40.80, 3},
		{"one issue", 5.10, 5.10, 13.60, 13.60, 1},
		{"no task ceiling", 15.30, 0, 13.60, 13.60, 3},
		{"no step-up", 15.30, 5.10, 0, 0, 3},
	} {
		if got := PlanStepUpUSD(c.plan, c.task, c.stepUp, c.issues); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: PlanStepUpUSD = %v, want %v", c.name, got, c.want)
		}
		card := WorkflowCard{CeilingUSD: c.plan, StepUpUSD: PlanStepUpUSD(c.plan, c.task, c.stepUp, c.issues)}
		if line := CostLine(card); strings.Contains(line, "steps up") && card.StepUpUSD <= card.CeilingUSD {
			t.Errorf("%s: a step-up no higher than the ceiling is shown: %q", c.name, line)
		}
	}
}
