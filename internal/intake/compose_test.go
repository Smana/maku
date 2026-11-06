// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

// validBody is a body holding the validator's three headings, in order: the draft shape a composed
// plan may carry.
const validBody = "## What is wrong\nThe install link 404s.\n## What it should be\nA link that resolves.\n## Acceptance check\nThe link resolves."

// composedAt is the fixed moment every compose test composes at: the plan carries it in Planner.At,
// which is display, so a pinned time keeps a diff readable.
var composedAt = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

// composeIn is the config a compose test composes against: the session surface, the pinned alias, the
// one-row class table, and no channel ceiling unless a test sets one.
func composeIn() intake.ComposeIn {
	return intake.ComposeIn{
		Surface: intake.SurfaceSession,
		Model:   "tier-frontier",
		Now:     composedAt,
		Rev:     1,
		Classes: classes(),
	}
}

// Review Focus 3: the reply cannot name its own roles or budget. A reply that does is dropped and
// logged, and the config's values are the plan's — and in its hash.
func TestRolesAndBudgetComeFromConfigOnly(t *testing.T) {
	table := classes()
	in := intake.ComposeIn{Surface: intake.SurfaceSession, Model: "tier-frontier",
		Now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), Classes: table}
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links",
			Roles: []string{"admin"}, BudgetUSD: 99}}}
	p, err := intake.Compose(d, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Issues[0].Roles, []string{"scoper", "implementer", "reviewer"}) || p.Budget.CeilingUSD != 2 {
		t.Fatalf("%+v %+v", p.Issues[0], p.Budget)
	}
	// The dropped values are not in the hash either: a $99 ceiling in the reply changes nothing.
	other := d
	other.Issues = []intake.DraftIssue{{Title: d.Issues[0].Title, Body: validBody, Class: "docs-links",
		Roles: []string{"reviewer", "admin"}, BudgetUSD: 99}}
	q, err := intake.Compose(other, in)
	if err != nil || intake.Hash(q) != intake.Hash(p) {
		t.Fatalf("a model value reached the hash: %v", err)
	}
	// And a class the config does not define is refused, not guessed.
	d.Issues = []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "admin"}}
	if _, err := intake.Compose(d, in); err == nil || !strings.Contains(err.Error(), intake.ReasonInvalidClass) {
		t.Fatalf("%v", err)
	}
}

// The channel's ceiling binds when it is below the class's, and the source says which one won.
func TestTheChannelCeilingBindsBelowTheClassCeiling(t *testing.T) {
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"}}}
	in := composeIn()
	in.CeilingUSD, in.CeilingSource = 1.5, "channel:C1.maxCostUSD"
	p, err := intake.Compose(d, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.CeilingUSD != 1.5 || p.Budget.Source != "channel:C1.maxCostUSD" {
		t.Fatalf("%+v", p.Budget)
	}
	// A channel ceiling above the class's does not raise it: the ceiling is the lower of the two.
	in.CeilingUSD, in.CeilingSource = 50, "channel:C1.maxCostUSD"
	if p, err := intake.Compose(d, in); err != nil || p.Budget.CeilingUSD != 2 || p.Budget.Source != "class:docs-links.taskUSD" {
		t.Fatalf("%+v %v", p.Budget, err)
	}
}

// The reply's evidence text is shown and its links are not: a URL the model sent is dropped, because
// a link a person clicks is one the broker built from a repository it verified (§security, CD5).
func TestModelSuppliedEvidenceLinksAreDropped(t *testing.T) {
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Evidence: []intake.Evidence{
			{Kind: "path", Text: "website/content/docs/use/guide/terminal-and-coding-agent.md", URL: "https://evil.example/steer"},
			{Kind: "thread", Text: "the CFP thread in #cnd", URL: "javascript:alert(1)"},
		},
		Alternatives: []intake.Alternative{{Repository: "smana/cloud-native-ref", Why: "serves the same docs"}},
		Question:     &intake.DraftQuestion{Text: "which repository?", Recommended: intake.Answer{Answer: "ai-native-ref", Why: "the link is in its docs"}},
		Issues:       []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"}},
	}
	p, err := intake.Compose(d, composeIn())
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range p.RepositoryEvidence {
		if e.URL != "" {
			t.Fatalf("evidence[%d] carries the model's link: %q", i, e.URL)
		}
	}
	if p.RepositoryEvidence[0].Text != d.Evidence[0].Text || p.RepositoryEvidence[0].Kind != "path" {
		t.Fatalf("%+v", p.RepositoryEvidence[0])
	}
	// Display fields the model did compose survive: they are the question's context.
	if !slices.Equal(p.Alternatives, d.Alternatives) {
		t.Fatalf("%+v", p.Alternatives)
	}
	if p.Question == nil || p.Question.Text != d.Question.Text || p.Question.Recommended.Answer != "ai-native-ref" {
		t.Fatalf("%+v", p.Question)
	}
}

// A composed plan is the contract's plan: the broker's provenance, the revision it was asked at, the
// hash over its material set — and it holds Check, the same re-validation issue_file will run.
func TestAComposedPlanIsTheWholeContract(t *testing.T) {
	d := intake.Draft{Repository: "Smana/AI-Native-Ref",
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"}}}
	in := composeIn()
	in.Rev = 3
	p, err := intake.Compose(d, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != intake.APIVersion || p.Rev != 3 {
		t.Fatalf("%+v", p)
	}
	if p.Planner != (intake.Planner{Model: "tier-frontier", Surface: "session", At: composedAt}) {
		t.Fatalf("%+v", p.Planner)
	}
	// The repository is the candidate's spelling, so the hash does not depend on the model's casing.
	if p.Repository != "smana/ai-native-ref" {
		t.Fatalf("%q", p.Repository)
	}
	if p.Issues[0].Index != 1 || p.Hash != intake.Hash(p) {
		t.Fatalf("%+v", p)
	}
	if r := intake.Check(p, []string{"smana/ai-native-ref"}, classes()); r != nil {
		t.Fatalf("the composed plan does not hold: %+v", r)
	}
	// The hash is over the composed fields and nothing the reply named: the same draft composes to
	// the same hash twice.
	again, err := intake.Compose(d, in)
	if err != nil || intake.Hash(again) != p.Hash {
		t.Fatalf("%v", err)
	}
}

// An issue may wait only for issues before it: a dependsOn entry that names itself, a later issue or
// nothing at all is dropped, and one bad entry drops the list — a plan is ordered, never circular.
func TestDependsOnKeepsOnlyEarlierIndices(t *testing.T) {
	for _, c := range []struct {
		name string
		in   [][]int
		want [][]int
	}{
		{name: "earlier indices are kept", in: [][]int{{}, {1}, {1, 2}}, want: [][]int{nil, {1}, {1, 2}}},
		{name: "an index of its own is dropped with the list", in: [][]int{nil, {2}, {1}}, want: [][]int{nil, nil, {1}}},
		{name: "an entry that is not earlier drops the list", in: [][]int{{}, {}, {1, 3}}, want: [][]int{nil, nil, nil}},
		{name: "zero is not an index", in: [][]int{{0}, {1}, {}}, want: [][]int{nil, {1}, nil}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := intake.Draft{Repository: "Smana/ai-native-ref"}
			for _, deps := range c.in {
				d.Issues = append(d.Issues, intake.DraftIssue{
					Title: "docs: x is wrong", Body: validBody, Class: "docs-links", DependsOn: deps})
			}
			p, err := intake.Compose(d, composeIn())
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range c.want {
				if !slices.Equal(p.Issues[i].DependsOn, want) {
					t.Fatalf("issue %d: %v, want %v", i+1, p.Issues[i].DependsOn, want)
				}
			}
		})
	}
}

// The one line an operator reads: every value the reply named that config owns is dropped, and the
// line names the field — never the value the model chose.
func TestADroppedModelValueIsLoggedByName(t *testing.T) {
	var buf bytes.Buffer
	in := composeIn()
	in.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links",
			Roles: []string{"admin"}, BudgetUSD: 99}}}
	if _, err := intake.Compose(d, in); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	if !strings.Contains(line, "roles@1") || !strings.Contains(line, "budgetUSD@1") {
		t.Fatalf("the line does not name the dropped fields: %s", line)
	}
	if strings.Contains(line, "admin") || strings.Contains(line, "99") {
		t.Fatalf("the line carries the model's values: %s", line)
	}
	if n := strings.Count(strings.TrimSpace(line), "\n"); n != 0 {
		t.Fatalf("%d lines for one drop: %s", n+1, line)
	}
	// A reply that names nothing config owns logs nothing: the line is the exception, not the log.
	buf.Reset()
	d.Issues[0].Roles, d.Issues[0].BudgetUSD = nil, 0
	if _, err := intake.Compose(d, in); err != nil || buf.Len() != 0 {
		t.Fatalf("%v %s", err, buf.String())
	}
}

// Compose refuses what the contract refuses, with the closed list's reason: a surface nobody labels,
// a plan with no issue, a draft the validator refuses.
func TestComposeRefusesWhatTheContractRefuses(t *testing.T) {
	one := []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"}}
	for _, c := range []struct {
		name  string
		draft intake.Draft
		in    func(*intake.ComposeIn)
		want  string
	}{
		{name: "a surface nobody labels",
			draft: intake.Draft{Repository: "Smana/ai-native-ref", Issues: one},
			in:    func(in *intake.ComposeIn) { in.Surface = "" },
			want:  intake.ReasonPlannerInvalid},
		{name: "no issue at all",
			draft: intake.Draft{Repository: "Smana/ai-native-ref"},
			want:  intake.ReasonPlannerInvalid},
		{name: "a draft the validator refuses",
			draft: intake.Draft{Repository: "Smana/ai-native-ref",
				Issues: []intake.DraftIssue{{Title: "no area prefix", Body: validBody, Class: "docs-links"}}},
			want: intake.ReasonInvalidDraft},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := composeIn()
			if c.in != nil {
				c.in(&in)
			}
			p, err := intake.Compose(c.draft, in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}
