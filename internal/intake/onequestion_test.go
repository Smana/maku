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

// Two equivalent template gaps in one Acceptance check (the planner's and the floor's) write their
// default once, with one A record.
func TestOneQuestionWritesAnEquivalentTemplateGapOnce(t *testing.T) {
	p := floorPlan(body("Better.", "Fine.\n[NEEDS CLARIFICATION: g1 How do we check it?]"))
	p = intake.WithGaps(p, []intake.Gap{{ID: "g1", Section: "acceptance-check", Text: "How do we check it?",
		Recommended: intake.Answer{Answer: "A reviewer repeats the steps in What is wrong and the problem no longer appears, and the repository's own test gate passes.", Why: "w"}}})
	p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Fine.\n", "Fine.\n[NEEDS CLARIFICATION: g2 Which command or observation proves the change is done?]\n", 1)
	p = intake.WithGaps(p, append(p.Gaps, intake.Gap{ID: "g2", Section: "acceptance-check", Text: "Which command or observation proves the change is done?"}))
	got, err := intake.OneQuestion(p, named, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := got.Issues[0].Body
	if n := strings.Count(b, "repeats the steps"); n != 2 { // the check, and its one A record
		t.Fatalf("default written %d times, want once plus its record:\n%s", n, b)
	}
	if strings.Count(b, "assumed") != 1 {
		t.Fatalf("records:\n%s", b)
	}
	if _, err := sanitize.CheckFinalDraft(got.Issues[0].Title, b); err != nil {
		t.Fatalf("not final: %v\n%s", err, b)
	}
}

// A template gap beside a concrete section is dropped with no record: a concrete Acceptance check,
// or a What it should be that carries the person's answer.
func TestOneQuestionDropsATemplateGapBesideAConcreteSection(t *testing.T) {
	const answer = "dailyUSD 5, maxCostUSD 2"
	p := floorPlan("## What is wrong\nThe budget is wrong.\n## What it should be\nCorrect it to the right value: " + answer + "\n" +
		"[NEEDS CLARIFICATION: g2 What observable behaviour MUST the change produce? Name what a user or reviewer sees.]\n" +
		"## Acceptance check\nThe config reference page shows the corrected budget.\n" +
		"[NEEDS CLARIFICATION: g3 Which command or observation proves the change is done?]\n\n## Decisions\nD1 [g1]: " + answer)
	p = intake.WithGaps(p, []intake.Gap{
		{ID: "g2", Section: "what-it-should-be", Text: "What observable behaviour MUST the change produce? Name what a user or reviewer sees."},
		{ID: "g3", Section: "acceptance-check", Text: "Which command or observation proves the change is done?"},
	})
	got, err := intake.OneQuestion(p, named, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := got.Issues[0].Body
	if strings.Contains(b, "assumed") || strings.Contains(b, "MUST no longer occur") || strings.Contains(b, "repeats the steps") || strings.Contains(b, "\n\n\n") {
		t.Fatalf("a template default beside a concrete section:\n%s", b)
	}
	if _, err := sanitize.CheckFinalDraft(got.Issues[0].Title, b); err != nil {
		t.Fatalf("not final: %v\n%s", err, b)
	}
}

// statedPlan is a plan whose one real gap is question, in What it should be (aws-0, 2026-11-19).
func statedPlan(question, recommended string) intake.Plan {
	p := floorPlan("## What is wrong\nThe reference disagrees with the factory.\n## What it should be\nThe reference states the right value: " +
		"[NEEDS CLARIFICATION: g1 " + question + "]\n## Acceptance check\nThe reference page shows the right value.")
	return intake.WithGaps(p, []intake.Gap{{ID: "g1", Section: "what-it-should-be", Text: question,
		Recommended: intake.Answer{Answer: recommended, Why: "the planner's guess"}}})
}

// A question the request already answers ("says X, but ... is Y") is never asked: the plan is Ready
// with the value the request states after the contrast, recorded as an assumption.
func TestOneQuestionNeverAsksWhatTheRequestStates(t *testing.T) {
	tests := map[string]struct {
		request, question, recommended, want string
	}{
		"the careful task's parts": {
			request:  "the harness reference says a careful task can split into 5 parts, but the factory caps it at 4",
			question: "Is the correct maximum number of parts for a careful task 5 or 4?", recommended: "5", want: "4",
		},
		"the Slack app budget": {
			request:  "the Slack app budget on the config reference says dailyUSD 10, but it should be dailyUSD 5, maxCostUSD 2",
			question: "Should the Slack app's daily budget be 10 or 5?", recommended: "10", want: "5",
		},
		"instead of names the wrong one": {
			request:  "the cheat sheet should name `maku:run` instead of `factory:run`",
			question: "Which label is right, `factory:run` or `maku:run`?", recommended: "factory:run", want: "maku:run",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := intake.OneQuestion(statedPlan(tc.question, tc.recommended), tc.request, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Gaps) != 0 || got.Question != nil {
				t.Fatalf("asked %+v for what the request states", got.Gaps)
			}
			b := got.Issues[0].Body
			should := strings.SplitN(strings.SplitN(b, "## What it should be", 2)[1], "## Acceptance check", 2)[0]
			if !strings.Contains(should, tc.want) {
				t.Fatalf("What it should be lacks %q:\n%s", tc.want, b)
			}
			if !strings.Contains(b, "assumed "+tc.want+", because the request states it") {
				t.Fatalf("no A record for the stated value:\n%s", b)
			}
			if _, err := sanitize.CheckFinalDraft(got.Issues[0].Title, b); err != nil {
				t.Fatalf("not final: %v\n%s", err, b)
			}
		})
	}
}

// A real gap, one the request leaves open, is still asked.
func TestOneQuestionStillAsksARealGap(t *testing.T) {
	for request, question := range map[string]string{
		"the Slack app budget on the config reference is wrong":                      "What are the right dailyUSD and maxCostUSD for the Slack app?",
		"the Slack app budget on the config reference says 10 or 5 and is confusing": "Should the daily budget be 10 or 5?",
		"the reference says 5 parts, but it is wrong":                                "Is the correct maximum 5 or 4?",
	} {
		got, err := intake.OneQuestion(statedPlan(question, "x"), request, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Gaps) != 1 || got.Question == nil || got.Question.Text != question {
			t.Errorf("%q: gaps = %+v, want the question asked", request, got.Gaps)
		}
	}
}

// An answer settles the source of truth: the follow-up it would open is not asked (#640).
func TestOneQuestionAfterTheBudgetAnswerAsksNoSource(t *testing.T) {
	req := "the Slack app budget on the config reference is wrong\nAnswered: What are the right values? — dailyUSD 5, maxCostUSD 2"
	got, err := intake.OneQuestion(statedPlan("What is the source of truth for the Slack app budget?", "the config reference"), req, true, nil)
	if err != nil || len(got.Gaps) != 0 {
		t.Fatalf("gaps = %+v, %v", got.Gaps, err)
	}
}

// The prompt says a request that states both values leaves no gap about them.
func TestSystemPromptNeverAsksWhatIsStated(t *testing.T) {
	p := intake.SystemPrompt()
	for _, want := range []string{"there is no gap about the value", "Never ask the person to confirm something they stated", "caps it at 4"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}
