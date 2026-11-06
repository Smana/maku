// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

// hostilePlan is plan/v1 with every field a model writes carrying the markup that would matter if a
// view let it through: a heading, a link, an autolink, emphasis and a script tag.
func hostilePlan() intake.Plan {
	p := intake.Plan{
		APIVersion:         intake.APIVersion,
		Rev:                1,
		Planner:            intake.Planner{Model: "tier-frontier", Surface: "slack", At: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)},
		Repository:         "smana/ai-native-ref",
		RepositoryEvidence: []intake.Evidence{{Kind: "path", Text: "# README.md says so"}},
		Alternatives:       []intake.Alternative{{Repository: "smana/other", Why: "**also** mentions it"}},
		Issues: []intake.Issue{
			{Index: 1, Title: "# the install link [is dead](https://evil.example)", Body: "## What is wrong\nThe link 404s, see https://evil.example\n## What it should be\nA link.\n## Acceptance check\nIt resolves.", Class: "docs-links", Roles: []string{"scoper", "implementer", "reviewer"}},
			{Index: 2, Title: "docs: the guide names `v1`", Body: "## What is wrong\nThe guide is stale.\n## What it should be\nCurrent.\n## Acceptance check\nIt is current.", Class: "docs-links", Roles: []string{"implementer"}, DependsOn: []int{1}},
		},
		Budget:   intake.Budget{CeilingUSD: 1.5, Source: "channel:maxCostUSD"},
		Question: &intake.Question{Text: "**which page**?", Recommended: intake.Answer{Answer: "the guide", Why: "it holds <script>alert(1)</script> the link"}},
	}
	p.Hash = intake.Hash(p)
	return p
}

// CD18: the plan a person reads is the plan, with a model's title a code span, its body a fenced
// block and its question escaped — never a heading, a link or a list of its own.
func TestPlanGivesAModelsTextNoStructure(t *testing.T) {
	var b bytes.Buffer
	if err := Plan(&b, hostilePlan(), intake.TemplateLabel, Options{Format: Markdown, Now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	root, src := parse(md)
	// flat is the view's plain text plus the text of its code spans: a model's title, question and
	// evidence sit in code spans by design, and this reads what a person sees either way.
	flat := plain(root, src) + "\n" + strings.Join(codeSpans(md), "\n")
	for _, want := range []string{
		intake.TemplateLabel,  // the label a degraded plan is never without
		"$1.50",               // the ceiling, from the broker's budget
		"channel:maxCostUSD",  // and where the number came from
		"smana/ai-native-ref", // the repository it files into
		"docs-links",          // each issue's class
		"scoper, implementer, reviewer",
		"waits for #1",
		"which page", // the question, escaped
		"mentions it",
		"README.md says so",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the view does not carry %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, hostilePlan().Hash) {
		t.Errorf("the view does not carry the plan's hash:\n%s", md)
	}
	assertInert(t, md, rooms, []string{"# the install link", "evil.example", "which page", "script", "mentions it", "README.md says so"})
	// The body's own headings stay text in its fence: an issue's draft is what it says it is.
	if strings.Count(md, "## What is wrong") != 2 {
		t.Fatalf("a body's heading left its block:\n%s", md)
	}
}

// A terminal's view is the same facts without Markdown: no emphasis, no code spans, no fence.
func TestPlanTextForATerminal(t *testing.T) {
	var b bytes.Buffer
	if err := Plan(&b, hostilePlan(), "", Options{Format: Text, Now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// The view's own markup is what must be gone: a model's text is quoted as it wrote it, so the
	// emphasis it typed is not this package's.
	head, _, _ := strings.Cut(out, "\n\n")
	if strings.Contains(head, "**") || strings.Contains(head, "`") || strings.Contains(out, "```") {
		t.Fatalf("a terminal's view holds Markdown:\n%s", out)
	}
	for _, want := range []string{"Plan · smana/ai-native-ref", "$1.50", "docs-links", "which page"} {
		if !strings.Contains(out, want) {
			t.Errorf("the text view does not carry %q:\n%s", want, out)
		}
	}
}

// A body holding a fence of its own is fenced by a longer one: the block never closes early.
func TestPlanFencesOutrunTheBody(t *testing.T) {
	p := hostilePlan()
	p.Issues[0].Body = "## What is wrong\n```\nnot a fence\n```"
	var b bytes.Buffer
	if err := Plan(&b, p, "", Options{Format: Markdown}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "````\n## What is wrong") {
		t.Fatalf("the fence does not outrun the body:\n%s", b.String())
	}
}
