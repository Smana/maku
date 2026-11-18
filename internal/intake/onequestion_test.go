// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

// threeGapPlan is a small docs fix as glm-5.3 planned it on aws-0 (#625): one real question, then the
// two template gaps the floor adds, the acceptance check and the observable behaviour.
func threeGapPlan(t *testing.T) intake.Plan {
	t.Helper()
	p := floorPlan("## What is wrong\nThe cheat sheet lists a PR label the factory never applies.\n" +
		"[NEEDS CLARIFICATION: g1 Which label does the factory apply instead?]\n" +
		"## What it should be\nBetter.\n## Acceptance check\nFine.")
	p = intake.WithGaps(p, []intake.Gap{{ID: "g1", Section: "what-is-wrong", Text: "Which label does the factory apply instead?",
		Recommended: intake.Answer{Answer: "maku:run", Why: "the factory's own label"}}})
	p = intake.ApplyFloor(p, "the cheat sheet in docs/cheatsheet.md lists the wrong label")
	if len(p.Gaps) != 3 {
		t.Fatalf("fixture has %d gaps, want 3: %+v", len(p.Gaps), p.Gaps)
	}
	return p
}

func TestOneQuestionAsksOnlyTheRealGap(t *testing.T) {
	got, err := intake.OneQuestion(threeGapPlan(t), "the cheat sheet in docs/cheatsheet.md lists the wrong label", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].ID != "g1" || got.Question == nil || got.Question.Text != got.Gaps[0].Text {
		t.Fatalf("gaps = %+v, question = %+v", got.Gaps, got.Question)
	}
	body := got.Issues[0].Body
	markers, err := sanitize.ClarificationMarkers(body)
	if err != nil || len(markers) != 1 || markers[0].ID != "g1" {
		t.Fatalf("markers = %+v, %v\n%s", markers, err, body)
	}
	recs, err := sanitize.DecisionRecords(body)
	if err != nil || len(recs) != 2 || recs[0].Kind != "A" || recs[1].Kind != "A" {
		t.Fatalf("records = %+v, %v\n%s", recs, err, body)
	}
	if got.Hash != intake.Hash(got) {
		t.Fatal("hash not recomputed")
	}
}

func TestOneQuestionTemplateOnlyIsReady(t *testing.T) {
	p := intake.ApplyFloor(floorPlan(body("Better.", "Fine.")), named)
	if len(p.Gaps) != 2 {
		t.Fatalf("fixture gaps = %+v", p.Gaps)
	}
	got, err := intake.OneQuestion(p, named, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 0 || got.Question != nil {
		t.Fatalf("gaps = %+v", got.Gaps)
	}
	if _, err := sanitize.CheckFinalDraft(got.Issues[0].Title, got.Issues[0].Body); err != nil {
		t.Fatalf("not final: %v\n%s", err, got.Issues[0].Body)
	}
}

func TestOneQuestionUsesTheStatedDoneWhen(t *testing.T) {
	for _, req := range []string{
		"the troubleshooting row names factory-handoff; done when that row names maku-handoff",
		"the troubleshooting row names factory-handoff.\nAcceptance: that row names maku-handoff",
		"fix the troubleshooting row. It is done when that row names maku-handoff.",
	} {
		p := intake.ApplyFloor(floorPlan(body("Better.", "Fine.")), req)
		got, err := intake.OneQuestion(p, req, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, accept, _ := strings.Cut(got.Issues[0].Body, "## Acceptance check")
		if !strings.Contains(accept, "that row names maku-handoff") || strings.Contains(accept, "repeats the steps") {
			t.Errorf("%q: acceptance = %s", req, accept)
		}
	}
}

func TestOneQuestionAfterAnAnswerAsksNothing(t *testing.T) {
	got, err := intake.OneQuestion(threeGapPlan(t), "x", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("an answered handoff re-opened %+v", got.Gaps)
	}
	if !strings.Contains(got.Issues[0].Body, "maku:run") {
		t.Fatalf("recommendation not assumed:\n%s", got.Issues[0].Body)
	}
}

func TestOneQuestionAsksTheRepositoryWhenNothingGroundsIt(t *testing.T) {
	choices := []string{"smana/ai-native-ref", "smana/runlore-kb"}
	got, err := intake.OneQuestion(threeGapPlan(t), "x", false, choices)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 1 || !intake.IsRepositoryGap(got.Gaps[0]) || got.Gaps[0].Recommended.Answer != "smana/x" {
		t.Fatalf("gaps = %+v", got.Gaps)
	}
	for _, c := range choices {
		if !strings.Contains(got.Gaps[0].Text, c) {
			t.Fatalf("question %q does not name %s", got.Gaps[0].Text, c)
		}
	}
	if _, err := sanitize.ClarificationMarkers(got.Issues[0].Body); err != nil {
		t.Fatal(err)
	}
	// One choice is no question.
	one, err := intake.OneQuestion(threeGapPlan(t), "x", false, choices[:1])
	if err != nil || intake.IsRepositoryGap(one.Gaps[0]) {
		t.Fatalf("%+v %v", one.Gaps, err)
	}
}

func TestPathsIn(t *testing.T) {
	got := intake.PathsIn("dailyUSD 5, from tooling/aws-0/agent-factory/helm-values-sponsored.yaml; see https://example.test/a/b.md and smana/runlore and docs/x.md.")
	want := []string{"tooling/aws-0/agent-factory/helm-values-sponsored.yaml", "docs/x.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("PathsIn = %q, want %q", got, want)
	}
}

// #625: a request that reports a wrong value is corrected, not removed. The rule is the prompt's.
func TestSystemPromptPrefersCorrectingAWrongValue(t *testing.T) {
	p := intake.SystemPrompt()
	for _, want := range []string{"correct it to", "never recommend removing it", `"Answered:"`} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}
