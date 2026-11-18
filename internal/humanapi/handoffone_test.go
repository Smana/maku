// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
)

// handoffReply is handoff/v1's fields these tests read.
type handoffReply struct {
	ID       string `json:"id"`
	Ready    bool   `json:"ready"`
	Question *struct {
		Text        string `json:"text"`
		Recommended string `json:"recommended"`
	} `json:"question"`
}

func handoffCall(t *testing.T, e env, args map[string]any) handoffReply {
	t.Helper()
	got := call(t, e, viaRoomctl("dev"), "handoff", args)
	if got.isError {
		t.Fatalf("handoff %v: %s", args, got.text)
	}
	var r handoffReply
	if err := json.Unmarshal(got.structured, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func storedPlan(t *testing.T, st *memHandoffStore, e env, id string) intake.Plan {
	t.Helper()
	row, err := st.GetHandoff(t.Context(), id, e.intake.clock())
	if err != nil {
		t.Fatal(err)
	}
	var p intake.Plan
	if err := json.Unmarshal(row.Plan, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// modelReplyFor is the gateway's answer carrying p's first issue and gaps as the model's draft.
func modelReplyFor(t *testing.T, p intake.Plan, gaps []intake.Gap) []byte {
	t.Helper()
	draft, err := json.Marshal(map[string]any{
		"repository": p.Repository,
		"issues": []any{map[string]any{
			"title": p.Issues[0].Title, "body": p.Issues[0].Body, "class": p.Issues[0].Class,
		}},
		"gaps": gaps,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(string(draft))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
}

// templateGapReply is a docs fix whose body states neither an observable result nor a check: the
// floor adds the two template gaps behind the model's one real question.
func templateGapReply(t *testing.T) []byte {
	t.Helper()
	p := planFixture(t, "single")
	p.Issues[0].Body = "## What is wrong\nThe cheat sheet lists a PR label the factory never applies.\n" +
		"[NEEDS CLARIFICATION: g1 Which label does the factory apply instead?]\n\n" +
		"## What it should be\nBetter.\n\n## Acceptance check\nFine."
	return modelReplyFor(t, p, []intake.Gap{{ID: "g1", Section: "what-is-wrong", Text: "Which label does the factory apply instead?",
		Recommended: intake.Answer{Answer: "Correct it to maku:run.", Why: "the factory applies maku:run"}}})
}

// #625: three open gaps, one question; the two template gaps are assumed as A records.
func TestHandoffAsksAtMostOneQuestion(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st))
	e.intake.replyWith(templateGapReply(t))

	r := handoffCall(t, e, map[string]any{"text": "the cheat sheet in docs/cheatsheet.md lists a PR label the factory never applies"})
	if r.Ready || r.Question == nil || r.Question.Text != "Which label does the factory apply instead?" {
		t.Fatalf("result = %+v", r)
	}
	p := storedPlan(t, st, e, r.ID)
	if len(p.Gaps) != 1 || strings.Count(p.Issues[0].Body, "[NEEDS CLARIFICATION") != 1 ||
		!strings.Contains(p.Issues[0].Body, "A1 [g2]: assumed") || !strings.Contains(p.Issues[0].Body, "A2 [g3]: assumed") {
		t.Fatalf("gaps = %+v\n%s", p.Gaps, p.Issues[0].Body)
	}
}

// #625: only template gaps open is a ready plan, and a stated done-when is the acceptance check.
func TestHandoffTemplateGapsOnlyIsReady(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st))
	p := planFixture(t, "single")
	p.Issues[0].Body = "## What is wrong\nThe FAIL row in docs/troubleshooting.md names factory-handoff.\n\n" +
		"## What it should be\nBetter.\n\n## Acceptance check\nFine."
	e.intake.replyWith(modelReplyFor(t, p, nil))

	r := handoffCall(t, e, map[string]any{"text": "the FAIL row in docs/troubleshooting.md names the old factory-handoff skill; done when that row names maku-handoff"})
	if !r.Ready || r.Question != nil {
		t.Fatalf("result = %+v", r)
	}
	body := storedPlan(t, st, e, r.ID).Issues[0].Body
	if !strings.Contains(body, "Done when that row names maku-handoff.") {
		t.Fatalf("the stated done-when is not the acceptance check:\n%s", body)
	}
}

// #625 comment: an answered question stays answered. The answer reaches every later re-plan of the
// handoff, and a re-plan that would ask again (here, the same two gaps) asks nothing.
func TestHandoffReplanKeepsTheAnswer(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st),
		intakeOption(t, func(_ *fakeIntake, in *Intake) { in.MaxRounds = 3 }))
	e.intake.replyWith(twoGapModelReply(t))
	r1 := handoffCall(t, e, map[string]any{"text": "the install link in README.md is dead"})
	if r1.Question == nil || r1.Question.Text != "Which install URL?" {
		t.Fatalf("begin = %+v", r1)
	}

	cand, err := answerFirstGap(storedPlan(t, st, e, r1.ID), "https://example.test/install")
	if err != nil {
		t.Fatal(err)
	}
	e.intake.replyWith(revisionModelReply(t, cand))
	r2 := handoffCall(t, e, map[string]any{"id": r1.ID, "answer": "https://example.test/install"})
	if !r2.Ready || r2.Question != nil {
		t.Fatalf("an answered handoff asked again: %+v", r2)
	}
	if !strings.Contains(e.intake.sent(), "Answered: Which install URL? — https://example.test/install") {
		t.Fatalf("the answer did not reach the model:\n%s", e.intake.sent())
	}

	// A reply to the ready plan re-plans; the model re-opens both gaps, and neither is asked.
	e.intake.replyWith(twoGapModelReply(t))
	r3 := handoffCall(t, e, map[string]any{"id": r1.ID, "answer": "also say which page it is"})
	if !r3.Ready || r3.Question != nil {
		t.Fatalf("a re-plan re-opened an answered gap: %+v", r3)
	}
	sent := e.intake.sent()
	if !strings.Contains(sent, "Answered: Which install URL? — https://example.test/install") || !strings.Contains(sent, "also say which page it is") {
		t.Fatalf("the re-plan forgot an answer:\n%s", sent)
	}
}

// twoServed serves smana/ai-native-ref and smana/runlore-kb, both public and writable by dev, and
// answers the path lookup from holds: a path exists only in the repository it names.
func twoServed(t *testing.T, holds map[string]string) option {
	t.Helper()
	repos := intakeOption(t, func(_ *fakeIntake, in *Intake) {
		in.Allow = map[string]bool{"Smana/ai-native-ref": true, "Smana/runlore-kb": true}
		in.Public = publicRepos{"smana/ai-native-ref": true, "smana/runlore-kb": true}
		if holds != nil {
			in.Paths = func(_ context.Context, repository, path string) (bool, error) {
				return strings.EqualFold(holds[path], repository), nil
			}
		}
	})
	return func(s *Server, h *fanout.Hub, v *hubView) {
		repos(s, h, v)
		w := world(nil)
		w.set(func() {
			w.writes = map[string][]string{"smana/ai-native-ref": {"dev"}, "smana/runlore-kb": {"dev"}}
		})
		w.gate(s)
	}
}

// #626: a path the request quotes picks the repository that holds it, before the model chooses.
func TestHandoffGroundsAQuotedPath(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st),
		twoServed(t, map[string]string{"tooling/aws-0/agent-factory/helm-values-sponsored.yaml": "smana/ai-native-ref"}))

	r := handoffCall(t, e, map[string]any{"text": "the budget numbers in tooling/aws-0/agent-factory/helm-values-sponsored.yaml are wrong in README.md"})
	if !r.Ready {
		t.Fatalf("a grounded repository was asked about: %+v", r)
	}
	sent := e.intake.sent()
	if !strings.Contains(sent, "- smana/ai-native-ref") || strings.Contains(sent, "runlore-kb") {
		t.Fatalf("the model was offered the wrong repositories:\n%s", sent)
	}
}

// #626: several candidates and nothing that grounds one: the one question is which repository, and
// an answer that quotes a path is grounded on the re-plan.
func TestHandoffAsksWhichRepository(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st),
		twoServed(t, map[string]string{"tooling/aws-0/agent-factory/helm-values-sponsored.yaml": "smana/ai-native-ref"}))

	r1 := handoffCall(t, e, map[string]any{"text": "the reference page README.md has the wrong budget numbers for the Slack app"})
	if r1.Ready || r1.Question == nil || !strings.HasPrefix(r1.Question.Text, "Which repository") ||
		!strings.Contains(r1.Question.Text, "smana/runlore-kb") || r1.Question.Recommended != "smana/ai-native-ref" {
		t.Fatalf("result = %+v", r1)
	}
	r2 := handoffCall(t, e, map[string]any{"id": r1.ID, "answer": "dailyUSD 5 and maxCostUSD 2, from tooling/aws-0/agent-factory/helm-values-sponsored.yaml"})
	if !r2.Ready || r2.Question != nil {
		t.Fatalf("after the answer: %+v", r2)
	}
	sent := e.intake.sent()
	if strings.Contains(sent, "- smana/runlore-kb") || !strings.Contains(sent, "Answered: Which repository") {
		t.Fatalf("the re-plan was not grounded on the answer:\n%s", sent)
	}
}
