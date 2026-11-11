// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"bytes"
	"encoding/json"
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

// Ranked model gaps survive composition in their model order, the legacy question maps the first,
// and the intake round is display metadata carried independently of the plan's material hash.
func TestComposeRankedGaps(t *testing.T) {
	body := strings.Replace(validBody, "The install link 404s.",
		"The install link 404s.\n[NEEDS CLARIFICATION: g1 first…]", 1)
	body = strings.Replace(body, "The link resolves.",
		"[NEEDS CLARIFICATION: g2 second]\nThe link resolves.", 1)
	gaps := []intake.Gap{
		{ID: "g1", Section: "what-is-wrong", Text: "first…", Recommended: intake.Answer{Answer: "use one", Why: "simplest"}},
		{ID: "g2", Section: "acceptance-check", Text: "second", Recommended: intake.Answer{Answer: "run it"}},
	}
	d := intake.Draft{Repository: "Smana/ai-native-ref", Gaps: gaps,
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: body, Class: "docs-links"}}}
	in := composeIn()
	in.Round, in.RoundsLeft = 2, 8
	p, err := intake.Compose(d, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Gaps, gaps) {
		t.Fatalf("ranked gaps changed: %+v", p.Gaps)
	}
	wantQuestion := &intake.Question{Text: "first…", Recommended: intake.Answer{Answer: "use one", Why: "simplest"}}
	if p.Question == nil || *p.Question != *wantQuestion {
		t.Fatalf("question = %+v, want %+v", p.Question, wantQuestion)
	}
	if p.Round != 2 || p.RoundsLeft != 8 {
		t.Fatalf("round = %d, roundsLeft = %d", p.Round, p.RoundsLeft)
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
		Gaps: []intake.Gap{{ID: "g1", Section: "what-is-wrong", Text: "which repository?",
			Recommended: intake.Answer{Answer: "ai-native-ref", Why: "the link is in its docs"}}},
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Class: "docs-links",
			Body: strings.Replace(validBody, "The install link 404s.",
				"The install link 404s.\n[NEEDS CLARIFICATION: g1 which repository?]", 1)}},
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
	if p.RepositoryEvidence[0].Text != d.Evidence[0].Text || p.RepositoryEvidence[0].Kind != intake.ModelClaim {
		t.Fatalf("%+v", p.RepositoryEvidence[0])
	}
	// Display fields the model did compose survive: they are the question's context.
	if !slices.Equal(p.Alternatives, d.Alternatives) {
		t.Fatalf("%+v", p.Alternatives)
	}
	if p.Question == nil || p.Question.Text != d.Gaps[0].Text || p.Question.Recommended.Answer != "ai-native-ref" {
		t.Fatalf("%+v", p.Question)
	}
	// The alternative it composed is a candidate, so the plan holds Check as well.
	if r := intake.Check(p, []string{"smana/ai-native-ref", "smana/cloud-native-ref"}, classes()); r != nil {
		t.Fatalf("the composed plan does not hold: %+v", r)
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

// The ceiling is the sum of the plan's classes, because a plan of two issues spends two classes'
// taskUSD: two $2.00 issues are $4.00, one issue is still $2.00, and the channel's cap lowers the sum
// when the channel is the lower of the two (controller ruling, fix round 1).
func TestTheCeilingIsTheSumOfTheIssuesClasses(t *testing.T) {
	table := classes()
	table["docs"] = intake.Class{Roles: []string{"implementer"}, CeilingUSD: 1, Source: "class:docs.taskUSD"}
	two := intake.Draft{Repository: "Smana/ai-native-ref", Issues: []intake.DraftIssue{
		{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"},
		{Title: "docs: y is wrong", Body: validBody, Class: "docs-links"},
	}}
	in := composeIn()
	in.Classes = table
	p, err := intake.Compose(two, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.CeilingUSD != 4 || p.Budget.Source != "class:docs-links.taskUSD" {
		t.Fatalf("%+v", p.Budget)
	}
	// Different classes add up too, and the source names each one that contributed, in issue order.
	mixed := intake.Draft{Repository: "Smana/ai-native-ref", Issues: []intake.DraftIssue{
		two.Issues[0],
		{Title: "docs: y is wrong", Body: validBody, Class: "docs"},
	}}
	p, err = intake.Compose(mixed, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.CeilingUSD != 3 || p.Budget.Source != "class:docs-links.taskUSD+class:docs.taskUSD" {
		t.Fatalf("%+v", p.Budget)
	}
	// The channel's cap still binds below the sum.
	in.CeilingUSD, in.CeilingSource = 3, "channel:C1.maxCostUSD"
	if p, err := intake.Compose(two, in); err != nil || p.Budget.CeilingUSD != 3 || p.Budget.Source != "channel:C1.maxCostUSD" {
		t.Fatalf("%+v %v", p.Budget, err)
	}
	// One issue is one class's ceiling: the sum of one term is itself.
	single := intake.Draft{Repository: "Smana/ai-native-ref", Issues: two.Issues[:1]}
	if p, err := intake.Compose(single, composeIn()); err != nil || p.Budget.CeilingUSD != 2 || p.Budget.Source != "class:docs-links.taskUSD" {
		t.Fatalf("%+v %v", p.Budget, err)
	}
}

// Review Focus 1, over the alternatives: an outsider named as "also fits" is a repository the request
// never allowed, so the plan is refused whole and the outsider is never echoed (CD5, §security).
func TestAlternativesMustBeCandidates(t *testing.T) {
	candidates := []string{"smana/ai-native-ref", "smana/cloud-native-ref"}
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Alternatives: []intake.Alternative{{Repository: "Smana/Cloud-Native-Ref", Why: "serves the same docs"}},
		Issues:       []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: "docs-links"}}}
	p, err := intake.Compose(d, composeIn())
	if err != nil {
		t.Fatal(err)
	}
	// An alternative that is a candidate holds, in another case as GitHub reads names.
	if r := intake.Check(p, candidates, classes()); r != nil {
		t.Fatalf("an alternative that is a candidate was refused: %+v", r)
	}
	// The same plan carrying an outsider: refused whole.
	p.Alternatives = append(slices.Clone(p.Alternatives), intake.Alternative{Repository: "owner/secret-repo", Why: "also fits"})
	r := intake.Check(p, candidates, classes())
	if r == nil || r.Reason != intake.ReasonRepositoryNotInCandidates {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.Error(), "secret-repo") || strings.Contains(r.Sentence, "owner/") {
		t.Fatalf("the refusal names the outsider: %s", r.Error())
	}
	// Compose itself has no candidate set to check against, so it composes the outsider — and Check
	// is what refuses it. A composed plan never carries an alternative the request did not allow.
	d.Alternatives = []intake.Alternative{{Repository: "owner/secret-repo", Why: "also fits"}}
	outsider, err := intake.Compose(d, composeIn())
	if err != nil {
		t.Fatal(err)
	}
	if r := intake.Check(outsider, candidates, classes()); r == nil || r.Reason != intake.ReasonRepositoryNotInCandidates {
		t.Fatalf("%+v", r)
	}
}

// The one line an operator reads: every value the reply named that config owns is dropped, and the
// line names the field — never the value the model chose, and never the repository either (it is not
// checked against the candidates yet when Compose writes this line).
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
	rec := logRecord(t, buf.String())
	if got := rec["msg"]; got != "intake: the reply named values the config owns; dropped" {
		t.Fatalf("msg: %v", got)
	}
	dropped, ok := rec["dropped"].([]any)
	if !ok || !slices.Equal(anyStrings(dropped), []string{"roles@1", "budgetUSD@1"}) {
		t.Fatalf("the line does not name the dropped fields: %v", rec["dropped"])
	}
	// The repository is not an attribute: at this point in the call nothing has checked it against
	// the candidates, and the line is an operator's, not a person's.
	if _, ok := rec["repository"]; ok {
		t.Fatalf("the line carries the repository: %v", rec)
	}
	// Nothing the model chose reaches the line, in any attribute: time and source are the handler's
	// own and are stripped, so a clock reading (…:27.99Z) cannot make this pass or fail by accident.
	delete(rec, "time")
	delete(rec, "source")
	rest, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rest), "admin") || strings.Contains(string(rest), "99") {
		t.Fatalf("the line carries the model's values: %s", rest)
	}
	// A reply that names nothing config owns logs nothing: the line is the exception, not the log.
	buf.Reset()
	d.Issues[0].Roles, d.Issues[0].BudgetUSD = nil, 0
	if _, err := intake.Compose(d, in); err != nil || buf.Len() != 0 {
		t.Fatalf("%v %s", err, buf.String())
	}
}

// logRecord is the one JSON record a logger wrote, decoded: the test reads the line's fields rather
// than its bytes, so that a timestamp or a source line cannot decide the assertion.
func logRecord(t *testing.T, line string) map[string]any {
	t.Helper()
	if n := strings.Count(strings.TrimSpace(line), "\n"); n != 0 {
		t.Fatalf("%d lines for one record: %s", n+1, line)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("the line is not one JSON record: %v (%s)", err, line)
	}
	return rec
}

// anyStrings is a decoded JSON array of strings, for a comparison that names the expected values.
func anyStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		s, ok := v.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
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
