// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// CD18: the composer is a template and says so. It is never a candidate for the live proof, and
// it never invents a repository.
func TestTheTemplateComposerIsLabelledAndGuessesNothing(t *testing.T) {
	p, r := intake.ComposeTemplate("the install link is dead", "", []string{"Smana/ai-native-ref"},
		map[string]intake.Class{"docs-links": {CeilingUSD: 2, Source: "class:docs-links.taskUSD", Roles: []string{"implementer"}}})
	if r != nil || p.Planner.Model != "composer" || p.Repository != "Smana/ai-native-ref" {
		t.Fatalf("%+v %+v", p, r)
	}
	if !strings.HasPrefix(p.Issues[0].Title, "factory:") || p.Question != nil {
		t.Fatalf("%+v", p.Issues[0])
	}
	if _, r := intake.ComposeTemplate("x", "", []string{"a/one", "a/two"}, nil); r == nil || r.Reason != intake.ReasonRepositoryNotAllowed {
		t.Fatalf("%+v", r)
	}
}

// whatShouldBe and whatToCheck are the composer's own defaults: the mode has no model, so a section
// the request did not fill says so rather than inventing what the work should be.
const (
	whatShouldBe = "Not stated in the request: this is a template draft, so nothing here was read from the repository."
	whatToCheck  = "Not stated in the request: the reviewer confirms the change removes the problem above."
)

// The composer's plan is a plan: one issue, the template's three sections in the template's order,
// the class and the money from config, no question, and it holds the re-validation every consumer
// runs.
func TestTheComposerFilesOneIssueInTheTemplatesOrder(t *testing.T) {
	candidates := []string{"smana/ai-native-ref"}
	table := map[string]intake.Class{
		"docs-links": {Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
	p, r := intake.ComposeTemplate("the install link is dead", "I read docs/intro.md", candidates, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	if len(p.Issues) != 1 {
		t.Fatalf("%d issues: %+v", len(p.Issues), p)
	}
	is := p.Issues[0]
	if is.Index != 1 || is.Class != "docs-links" || !slices.Equal(is.Roles, table["docs-links"].Roles) {
		t.Fatalf("%+v", is)
	}
	if is.DependsOn != nil {
		t.Fatalf("one issue waits for nothing: %+v", is.DependsOn)
	}
	if p.Budget != (intake.Budget{CeilingUSD: 2, Source: "class:docs-links.taskUSD"}) {
		t.Fatalf("%+v", p.Budget)
	}
	// The three headings, in the template's order, with the person's words kept whole.
	wrong := strings.Index(is.Body, "## What is wrong")
	should := strings.Index(is.Body, "## What it should be")
	check := strings.Index(is.Body, "## Acceptance check")
	if wrong < 0 || should < wrong || check < should {
		t.Fatalf("the body is not the template, in order: %q", is.Body)
	}
	if !strings.Contains(is.Body[wrong:should], "the install link is dead") ||
		!strings.Contains(is.Body[wrong:should], "I read docs/intro.md") {
		t.Fatalf("the request and its context are not what is wrong: %q", is.Body)
	}
	if !strings.Contains(is.Body[should:check], whatShouldBe) || !strings.Contains(is.Body[check:], whatToCheck) {
		t.Fatalf("a section the request left empty is not the composer's default: %q", is.Body)
	}
	if p.APIVersion != intake.APIVersion || p.Rev != 0 || p.Question != nil || len(p.Alternatives) != 0 {
		t.Fatalf("%+v", p)
	}
	if p.Hash != intake.Hash(p) {
		t.Fatalf("the plan's hash is not the hash of its material: %q", p.Hash)
	}
	if ref := intake.Check(p, candidates, table); ref != nil {
		t.Fatalf("the composer's plan does not hold Check: %+v", ref)
	}
}

// The class is the alphabetically first one the config defines: a deterministic composer picks, it
// does not infer, and two processes reading the same table name the same class.
func TestTheComposerTakesTheFirstClassAlphabetically(t *testing.T) {
	table := map[string]intake.Class{
		"housekeeping": {Roles: []string{"implementer"}, CeilingUSD: 1.5, Source: "class:housekeeping.taskUSD"},
		"docs-links":   {Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
		"docs":         {Roles: []string{"implementer", "reviewer"}, CeilingUSD: 1, Source: "class:docs.taskUSD"},
	}
	p, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"}, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	if p.Issues[0].Class != "docs" || !slices.Equal(p.Issues[0].Roles, table["docs"].Roles) {
		t.Fatalf("%+v", p.Issues[0])
	}
	if p.Budget != (intake.Budget{CeilingUSD: 1, Source: "class:docs.taskUSD"}) {
		t.Fatalf("%+v", p.Budget)
	}
}

// CD17: the two paths cannot disagree about money. The composer's plan and a plan Compose makes from
// the same draft carry the same material, so a degraded plan is not a cheaper plan.
func TestTheComposerAndComposeCannotDisagreeAboutMoney(t *testing.T) {
	table := map[string]intake.Class{
		"housekeeping": {Roles: []string{"implementer"}, CeilingUSD: 1.5, Source: "class:housekeeping.taskUSD"},
		"docs-links":   {Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
	p, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"}, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	d := intake.Draft{Repository: "smana/ai-native-ref", Issues: []intake.DraftIssue{{
		Title: p.Issues[0].Title, Body: p.Issues[0].Body, Class: p.Issues[0].Class}}}
	q, err := intake.Compose(d, intake.ComposeIn{Surface: intake.SurfaceSession, Model: "tier-frontier",
		Now: composedAt, Rev: p.Rev, Classes: table})
	if err != nil {
		t.Fatal(err)
	}
	if q.Budget != p.Budget || !slices.Equal(q.Issues[0].Roles, p.Issues[0].Roles) {
		t.Fatalf("composer %+v %+v, compose %+v %+v", p.Budget, p.Issues[0].Roles, q.Budget, q.Issues[0].Roles)
	}
	if intake.Hash(q) != p.Hash {
		t.Fatalf("the same draft hashes two ways: %q and %q", intake.Hash(q), p.Hash)
	}
}

// The same request composes to the same material: nothing in a composer plan is a clock or a map's
// iteration order. A different class is a different plan, because the class and the money are in it.
func TestTheComposerIsDeterministicInItsMaterial(t *testing.T) {
	table := map[string]intake.Class{
		"docs-links":   {Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
		"housekeeping": {Roles: []string{"implementer"}, CeilingUSD: 1.5, Source: "class:housekeeping.taskUSD"},
	}
	a, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"}, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	b, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"}, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	if intake.Hash(a) != intake.Hash(b) {
		t.Fatalf("the same request hashes two ways: %q and %q", intake.Hash(a), intake.Hash(b))
	}
	other, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"},
		map[string]intake.Class{"housekeeping": table["housekeeping"]})
	if r != nil {
		t.Fatalf("%+v", r)
	}
	if intake.Hash(other) == intake.Hash(a) {
		t.Fatalf("a plan charged to another class hashes the same: %q", intake.Hash(other))
	}
	// A candidate is copied as the caller spelled it: the composer did not choose it, and Check reads
	// a repository without regard to case, so the operator's own spelling survives.
	if a.Repository != "smana/ai-native-ref" {
		t.Fatalf("%q", a.Repository)
	}
}

// The composer never guesses a repository: exactly one candidate, or a refusal that names none of
// them (CD5, §security).
func TestTheComposerRefusesWithoutExactlyOneRepository(t *testing.T) {
	table := map[string]intake.Class{
		"docs-links": {Roles: []string{"implementer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
	for _, tc := range []struct {
		name       string
		candidates []string
	}{
		{"none", nil},
		{"two", []string{"a/one", "a/two"}},
		{"twenty-one", candidateList(21)},
		{"one, but empty", []string{"   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, r := intake.ComposeTemplate("the install link is dead", "", tc.candidates, table)
			if r == nil || r.Reason != intake.ReasonRepositoryNotAllowed {
				t.Fatalf("%+v %+v", p, r)
			}
			if p.APIVersion != "" {
				t.Fatalf("a refusal carries no plan: %+v", p)
			}
			for _, c := range tc.candidates {
				if c = strings.TrimSpace(c); c != "" && strings.Contains(r.Sentence, c) {
					t.Fatalf("the refusal names a repository back: %q", r.Sentence)
				}
			}
		})
	}
}

// What the composer cannot carry is refused with the model path's reason and the field's bound: the
// composer is a plan like any other, and its refusals are the contract's.
func TestTheComposerRefusesWhatTheRequestCannotCarry(t *testing.T) {
	table := map[string]intake.Class{
		"docs-links": {Roles: []string{"implementer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
	candidates := []string{"smana/ai-native-ref"}
	for _, tc := range []struct {
		name, text, context string
		classes             map[string]intake.Class
		want                string
	}{
		{"text over the bound", strings.Repeat("x", 16<<10+1), "", table, "text: 16385 bytes, the bound is 16 KiB"},
		{"context over the bound", "the link is dead", strings.Repeat("x", 4<<10+1), table, "context: 4097 bytes, the bound is 4 KiB"},
		{"empty request", "   \n\t", "", table, "text: empty, the request is a sentence about what is wrong"},
		{"no class configured", "the link is dead", "", nil, "classes: none in the config's table"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, r := intake.ComposeTemplate(tc.text, tc.context, candidates, tc.classes)
			if r == nil || r.Reason != intake.ReasonPlannerInvalid {
				t.Fatalf("%+v %+v", p, r)
			}
			if !strings.Contains(r.Sentence, tc.want) {
				t.Fatalf("%q does not carry %q", r.Sentence, tc.want)
			}
		})
	}
	// A body the contract cannot hold is refused with its bound, whole: the composer truncates
	// nothing, so a request that nearly fills the text bound and brings context is refused rather
	// than filed half-written.
	p, r := intake.ComposeTemplate(strings.Repeat("x", 16<<10), strings.Repeat("y", 4<<10), candidates, table)
	if r == nil || r.Reason != intake.ReasonPlanTooLarge || !strings.Contains(r.Sentence, "the bound is 16384") {
		t.Fatalf("%+v %+v", p, r)
	}
}

// CD18: every surface says what a template draft is, in one spelling, and the plan says it too — in
// `planner.model`, which is the field a proof and a dashboard read.
func TestTheTemplateDraftSaysSoOnEverySurface(t *testing.T) {
	if intake.TemplateLabel != "template draft: the plan service is unavailable" {
		t.Fatalf("%q", intake.TemplateLabel)
	}
	table := map[string]intake.Class{
		"docs-links": {Roles: []string{"implementer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
	p, r := intake.ComposeTemplate("the install link is dead", "", []string{"smana/ai-native-ref"}, table)
	if r != nil {
		t.Fatalf("%+v", r)
	}
	if p.Planner.Model != "composer" {
		t.Fatalf("the plan does not say no model wrote it: %+v", p.Planner)
	}
	// The label is the surface's line, never contract text: a hash that covered it would make the
	// degraded plan's hash depend on how a surface worded a warning.
	m, err := intake.Material(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(m), intake.TemplateLabel) {
		t.Fatalf("the label is in the plan's material: %s", m)
	}
}

// candidateList is n distinct repositories, for the bound the composer refuses at any count but one.
func candidateList(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "owner/repo-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return out
}
