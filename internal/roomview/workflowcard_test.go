// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strings"
	"testing"
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
