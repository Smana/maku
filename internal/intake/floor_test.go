// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

func floorPlan(body string, gaps ...intake.Gap) intake.Plan {
	p := intake.Plan{APIVersion: intake.APIVersion, Repository: "smana/x", Round: 1, RoundsLeft: 9,
		Issues: []intake.Issue{{Index: 1, Title: "factory: status is unclear", Body: body, Class: "docs-links"}}}
	p = intake.WithGaps(p, gaps)
	p.Hash = intake.Hash(p)
	return p
}

const (
	goodShould     = "The status line MUST show the phase of the run."
	goodAcceptance = "Run `maku status`: it shows the phase."
	named          = "the status line in internal/humanapi/status.go is unclear"
)

func body(should, acceptance string) string {
	return "## What is wrong\nStatus is unclear.\n## What it should be\n" + should + "\n## Acceptance check\n" + acceptance
}

// The floor's table: which request and body trigger which gaps, in which order, in which section.
func TestApplyFloor(t *testing.T) {
	tests := map[string]struct {
		request string
		body    string
		want    []string // sections of the added gaps, in rank order
	}{
		"complete plan adds nothing":        {named, body(goodShould, goodAcceptance), nil},
		"the live case: vague everything":   {"the factory should be better at telling people what's going on", body("Clearer status.", "It is better."), []string{"scope", "what-it-should-be", "acceptance-check"}},
		"no observable behaviour":           {named, body("Better status.", goodAcceptance), []string{"what-it-should-be"}},
		"placeholder behaviour":             {named, body("Not stated in the request: nothing here was read.", goodAcceptance), []string{"what-it-should-be"}},
		"verb of a visible result":          {named, body("The page lists every phase.", goodAcceptance), nil},
		"no acceptance command":             {named, body(goodShould, "It works."), []string{"acceptance-check"}},
		"acceptance observation only":       {named, body(goodShould, "The output MUST show the phase."), nil},
		"acceptance names a visible result": {named, body(goodShould, "The config reference page shows the corrected budget."), nil},
		"behaviour carries a decision":      {named, body("Correct it to dailyUSD 5, maxCostUSD 2", goodAcceptance) + "\n\n## Decisions\nD1 [g1]: dailyUSD 5, maxCostUSD 2", nil},
		"acceptance section empty":          {named, body(goodShould, ""), []string{"acceptance-check"}},
		"request names a file":              {"fix README.md", body(goodShould, goodAcceptance), nil},
		"request names a path":              {"fix internal/intake", body(goodShould, goodAcceptance), nil},
		"request names an identifier":       {"fix `Plan`", body(goodShould, goodAcceptance), nil},
		"request names a CamelCase type":    {"fix CachedPublicity", body(goodShould, goodAcceptance), nil},
		"request names nothing":             {"make it nicer", body(goodShould, goodAcceptance), []string{"scope"}},
		"the model already asked":           {named, body("[NEEDS CLARIFICATION: g1 what should it show?]", goodAcceptance), nil},
		"a marker is not an answer":         {named, body(goodShould, "[NEEDS CLARIFICATION: g1 how do we check?]"), nil},
		"a plan's own file is no citation":  {"make it nicer", body("FACTORY_STATUS.md MUST exist.", goodAcceptance), []string{"scope"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gaps []intake.Gap
			// A model marker already in the body needs its gap, as a model reply always has one.
			if strings.Contains(tc.body, "[NEEDS CLARIFICATION: g1 ") {
				section := "what-it-should-be"
				if strings.Contains(tc.body, "how do we check") {
					section = "acceptance-check"
				}
				gaps = []intake.Gap{{ID: "g1", Section: section, Text: "q", Recommended: intake.Answer{Answer: "a"}}}
			}
			before := floorPlan(tc.body, gaps...)
			got := intake.ApplyFloor(before, tc.request)
			added := got.Gaps[len(gaps):]
			var sections []string
			for _, g := range added {
				sections = append(sections, g.Section)
			}
			if strings.Join(sections, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("added %v, want %v", sections, tc.want)
			}
			if got.Hash != intake.Hash(got) {
				t.Fatalf("the hash is stale")
			}
			if len(tc.want) == 0 {
				if got.Hash != before.Hash {
					t.Fatalf("a plan the floor leaves alone changed")
				}
				return
			}
			assertCoherent(t, got)
			// The model's own gaps keep their rank; the floor's follow.
			for i, g := range gaps {
				if got.Gaps[i].ID != g.ID {
					t.Fatalf("model gap %d moved: %+v", i, got.Gaps)
				}
			}
			if got.Question == nil || got.Question.Text != got.Gaps[0].Text {
				t.Fatalf("question: %+v", got.Question)
			}
		})
	}
}

// assertCoherent holds the plan to the existing correlation rules: one marker per gap, one gap per
// marker, each marker in its named section, a one-line recommendation, and the draft still valid.
func assertCoherent(t *testing.T, p intake.Plan) {
	t.Helper()
	markers := map[string]string{}
	for _, is := range p.Issues {
		if _, err := sanitize.CheckDraft(is.Title, is.Body); err != nil {
			t.Fatalf("the body is no longer a draft: %v", err)
		}
		ms, err := sanitize.ClarificationMarkers(is.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			markers[m.ID] = sectionHeadingBefore(is.Body, m.Start)
		}
	}
	if len(markers) != len(p.Gaps) {
		t.Fatalf("%d markers, %d gaps", len(markers), len(p.Gaps))
	}
	for _, g := range p.Gaps {
		sec, ok := markers[g.ID]
		if !ok || sec != g.Section {
			t.Fatalf("gap %s (%s) vs marker section %q", g.ID, g.Section, sec)
		}
		if strings.ContainsAny(g.Recommended.Answer, "\r\n") || strings.TrimSpace(g.Recommended.Answer) == "" || g.Recommended.Why == "" {
			t.Fatalf("recommendation: %+v", g.Recommended)
		}
	}
}

func sectionHeadingBefore(body string, at int) string {
	section := "scope"
	for _, line := range strings.Split(body[:at], "\n") {
		switch strings.TrimSpace(line) {
		case "## What is wrong":
			section = "what-is-wrong"
		case "## What it should be":
			section = "what-it-should-be"
		case "## Acceptance check":
			section = "acceptance-check"
		}
	}
	return section
}

// Marker numbering continues after every g<n> a plan already uses, records included, and a marker
// lands above the Decisions section, never inside it.
func TestApplyFloorNumberingAndPlacement(t *testing.T) {
	b := body("Better.", goodAcceptance) + "\n\n## Decisions\n\nD1 [g4]: use a banner, because it is visible"
	got := intake.ApplyFloor(floorPlan(b), named)
	if len(got.Gaps) != 1 || got.Gaps[0].ID != "g5" {
		t.Fatalf("%+v", got.Gaps)
	}
	want := "## What it should be\nBetter.\n[NEEDS CLARIFICATION: g5 "
	if !strings.Contains(got.Issues[0].Body, want) {
		t.Fatalf("marker misplaced:\n%s", got.Issues[0].Body)
	}
	if _, err := sanitize.DecisionRecords(got.Issues[0].Body); err != nil {
		t.Fatal(err)
	}

	// The acceptance marker goes above Decisions too.
	b = body(goodShould, "It works.") + "\n\n## Decisions\n\nD1 [g1]: x, because y"
	got = intake.ApplyFloor(floorPlan(b), named)
	i, j := strings.Index(got.Issues[0].Body, "[NEEDS"), strings.Index(got.Issues[0].Body, "## Decisions")
	if i < 0 || i > j {
		t.Fatalf("marker not above Decisions:\n%s", got.Issues[0].Body)
	}

	// A scope marker precedes the first required heading.
	got = intake.ApplyFloor(floorPlan(body(goodShould, goodAcceptance)), "make it nicer")
	if !strings.HasPrefix(got.Issues[0].Body, "[NEEDS CLARIFICATION: g1 ") {
		t.Fatalf("scope marker not first:\n%s", got.Issues[0].Body)
	}
}

// The floor is deterministic, and the input plan is never mutated.
func TestApplyFloorDeterministicAndPure(t *testing.T) {
	in := floorPlan(body("Better.", "Fine."))
	snapshot := in.Issues[0].Body
	a, b := intake.ApplyFloor(in, "nicer"), intake.ApplyFloor(in, "nicer")
	if a.Hash != b.Hash || len(a.Gaps) != 3 {
		t.Fatalf("%+v", a.Gaps)
	}
	if in.Issues[0].Body != snapshot || len(in.Gaps) != 0 {
		t.Fatalf("the input was mutated")
	}
}

// The floor's gaps are accepted at the round cap by the existing deterministic path: Revise with
// the cap replaces every marker with its recommendation and leaves a plan that files.
func TestApplyFloorGapsAreAcceptedAtTheCap(t *testing.T) {
	base := intake.ApplyFloor(floorPlan(body("Better.", "Fine.")), "nicer")
	base.Round, base.RoundsLeft = 9, 1
	base.Hash = intake.Hash(base)
	got, err := intake.Revise(base, base.Hash, base, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 0 || strings.Contains(got.Issues[0].Body, "[NEEDS CLARIFICATION") ||
		strings.Count(got.Issues[0].Body, "\nA") < 3 {
		t.Fatalf("%+v\n%s", got.Gaps, got.Issues[0].Body)
	}
}

// A body the marker would push over the draft bound is left as the model wrote it.
func TestApplyFloorKeepsTheBodyBound(t *testing.T) {
	huge := body("Better.", goodAcceptance) + "\n" + strings.Repeat("x", 16<<10-len(body("Better.", goodAcceptance))-1)
	got := intake.ApplyFloor(floorPlan(huge), named)
	if len(got.Gaps) != 0 || len(got.Issues[0].Body) > 16<<10 {
		t.Fatalf("%d gaps, %d bytes", len(got.Gaps), len(got.Issues[0].Body))
	}
}

// aws-0, 2026-11-19: a fully stated request planned with the correct values in both sections gets no
// template gap; the same sections without a stated value still do.
func TestFloorCountsAStatedValueAsConcrete(t *testing.T) {
	const request = "the factory configuration reference shows the Slack app budget as dailyUSD 10.20 and maxCostUSD 5.10, " +
		"but tooling/aws-0/agent-factory/helm-values-sponsored.yaml sets dailyUSD 5 and maxCostUSD 2"
	stated := floorPlan("## What is wrong\nThe reference gives dailyUSD 10.20.\n" +
		"## What it should be\nThe reference gives dailyUSD 5 and maxCostUSD 2.\n" +
		"## Acceptance check\nThe reference page gives dailyUSD 5 and maxCostUSD 2.")
	if got := intake.ApplyFloor(stated, request); len(got.Gaps) != 0 {
		t.Fatalf("gaps beside a stated fix: %+v", got.Gaps)
	}
	vague := floorPlan("## What is wrong\nThe reference gives the wrong budget.\n" +
		"## What it should be\nThe budget is right.\n## Acceptance check\nIt is right.")
	if got := intake.ApplyFloor(vague, "the factory configuration reference shows the Slack app budget as dailyUSD 10.20 and maxCostUSD 5.10 in tooling/aws-0/README.md"); len(got.Gaps) != 2 {
		t.Fatalf("gaps = %+v, want the two template gaps", got.Gaps)
	}
}

// A planner that writes the stated value in code font with its YAML colon ("`dailyUSD: 5`") still
// states the value the request gave as "dailyUSD 5": the floor adds no behaviour sentence and no
// A-line (aws-0, OpenCode + Haiku, 2026-11-20).
func TestStatesValueReadsInsideACodeSpan(t *testing.T) {
	request := "the config reference has the wrong Slack app budget; fix it to dailyUSD 5, maxCostUSD 2"
	section := "The Slack app budget should be corrected to `dailyUSD: 5` and `maxCostUSD: 2`."
	if !intake.StatesValue(section, request) {
		t.Fatalf("statesValue(%q, %q) = false, want true", section, request)
	}
	if intake.StatesValue("The budget should be corrected to `slackBudget: unset`.", request) {
		t.Fatal("a code span naming nothing the request names must not count")
	}
}
