// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

// O1: the proposal shows the class, the suggested profile with its roles and the merge outcome in
// the broker's words, then the planner's why, inert; a fallback plan shows no suggestion line.
func TestProposalSuggestionLines(t *testing.T) {
	v := proposalView()
	v.Issue.Class = "docs-links"
	v.Issue.Suggestion = &ProposalSuggestion{Profile: "links-solo", Roles: []string{"implementer"}, Outcome: "auto",
		Why: "*one* file <https://evil.example|click>"}
	sections, err := Proposal(v, Options{Format: Slack, Rooms: rooms})
	if err != nil {
		t.Fatal(err)
	}
	var issue string
	for _, s := range sections {
		if s.Key == sectionIssue {
			issue = s.Body
		}
	}
	want := "Suggested: class docs-links · profile links-solo (implementer) · merges on its own when the gate's conditions hold"
	if !strings.Contains(issue, want) {
		t.Fatalf("lacks %q:\n%s", want, issue)
	}
	if !strings.Contains(issue, "why (the planner's reading): ") || strings.Contains(issue, "<https://evil.example|click>") {
		t.Fatalf("the why is not inert:\n%s", issue)
	}
	v.Issue.Suggestion = nil
	sections, _ = Proposal(v, Options{Format: Slack, Rooms: rooms})
	for _, s := range sections {
		if strings.Contains(s.Body, "Suggested:") {
			t.Fatalf("a fallback plan shows a suggestion: %s", s.Body)
		}
	}
}

// The factory's choice reads as such, with no profile named.
func TestSuggestionLineFactorysChoice(t *testing.T) {
	got := SuggestionLine("review", "", nil, "approval")
	if got != "class review · profile: the factory's choice · a maintainer's approval merges it" {
		t.Fatal(got)
	}
}

// #460: the plan view labels the why as the planner's reading, unverified, and keeps it inert.
func TestPlanShowsTheSuggestion(t *testing.T) {
	p := hostilePlan()
	p.Issues[0].Suggestion = &intake.Suggestion{Profile: "links-solo", Roles: []string{"implementer"},
		Merge: intake.MergeShown{Outcome: "auto"}, Why: "# [click](https://evil.example)"}
	var b bytes.Buffer
	if err := Plan(&b, p, "", Options{Format: Markdown, Now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		// The broker's line, escaped as every config value in this view is.
		`suggested: class docs\-links · profile links\-solo \(implementer\) · merges on its own when the gate's conditions hold`,
		"why (the planner's reading, unverified): `# [click](https://evil.example)`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "suggested:") != 1 {
		t.Fatalf("one suggestion, one line:\n%s", out)
	}
}
