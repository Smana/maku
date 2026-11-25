// SPDX-License-Identifier: Apache-2.0

package roomview

import (
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
