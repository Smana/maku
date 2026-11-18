// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

const (
	askedQuestion    = "Which page holds the dead link?"
	askedRecommended = "README.md, the install section"
)

// askedPlan is the fixture as the planner answers `outcome: asked`: one ranked gap, its question
// lifted onto the plan, and the body's marker the final check refuses.
func askedPlan(t *testing.T, plan intake.Plan) intake.Plan {
	t.Helper()
	asked := clonePlan(t, plan)
	asked.Gaps = []intake.Gap{{ID: "g1", Text: askedQuestion, Recommended: intake.Answer{Answer: askedRecommended}}}
	asked.Issues[0].Body += "\n\n[NEEDS CLARIFICATION: g1 " + askedQuestion + "]"
	return intake.WithGaps(asked, asked.Gaps)
}

// Smana/maku#623: a mention whose plan asks one question posts that question in the thread, with
// its recommendation and the reply that takes it, and never a "Not proposed" refusal or a raw
// reason code. No Confirm is shown while a marker remains.
func TestAskedPlanPostsTheQuestion(t *testing.T) {
	rig, plan := withProposals(t)
	rig.broker.setPlan(askedPlan(t, plan), false, nil)
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)

	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want the one question: %+v", len(posts), posts)
	}
	q := posts[0]
	for _, want := range []string{askedQuestion, askedRecommended, "Reply", "accept the rest"} {
		if !strings.Contains(q.text, want) {
			t.Fatalf("the question message does not carry %q: %q", want, q.text)
		}
	}
	for _, never := range []string{"Not proposed", "unresolved_marker"} {
		if strings.Contains(q.text, never) {
			t.Fatalf("the question message says %q: %q", never, q.text)
		}
	}
	if strings.Contains(string(q.blocks), actionProposalConfirm) {
		t.Fatalf("a plan with a marker shows Confirm: %s", q.blocks)
	}
	if _, ok := q.meta.proposalPayload(); ok {
		t.Fatal("the question was posted as a proposal a ✅ could file")
	}
	if got := rig.metrics.count("events", "asked"); got != 1 {
		t.Fatalf("the question was counted %d times as asked", got)
	}
}

// The reply to the question continues the handoff by its id: an answer is sent as the answer, and
// "accept the rest" as acceptRest. Once the plan carries no marker, the proposal with Confirm is
// posted below the question.
func TestAskedPlanReplyReplans(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		want        HandoffRequest
	}{
		{"an answer is sent as the answer", "README.md",
			HandoffRequest{ID: defaultHandoffID, Answer: "README.md", OnBehalfOf: "slack:T01ABC/U02DEF", CeilingUSD: 2}},
		{"accept the rest takes the recommendations", "Accept the rest",
			HandoffRequest{ID: defaultHandoffID, AcceptRest: true, OnBehalfOf: "slack:T01ABC/U02DEF", CeilingUSD: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, plan := withProposals(t)
			rig.broker.setPlan(askedPlan(t, plan), false, nil)
			rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
			rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)

			rig.broker.setNextPlan(plan, false) // the answer resolves the gap: no marker remains
			rig.replySaid("U02DEF", "1729.9001", tc.reply)

			calls := rig.broker.handoffCalls()
			if len(calls) != 2 || !reflect.DeepEqual(calls[1], tc.want) {
				t.Fatalf("the reply's handoff: %+v, want %+v", calls, tc.want)
			}
			posts := rig.posts()
			if len(posts) != 2 {
				t.Fatalf("%d posts, want the question and then the proposal: %+v", len(posts), posts)
			}
			p := posts[1]
			prop, ok := p.meta.proposalPayload()
			if !ok || prop.Hash != intake.Hash(plan) || prop.Author != "U02DEF" || prop.ID != defaultHandoffID {
				t.Fatalf("the proposal's payload: %+v (%v)", prop, ok)
			}
			if !strings.Contains(string(p.blocks), actionProposalConfirm) {
				t.Fatalf("the proposal carries no Confirm: %s", p.blocks)
			}
			if strings.Contains(p.text, "Not proposed") {
				t.Fatalf("the reply was refused: %q", p.text)
			}
		})
	}

	t.Run("a bystander's reply does not answer the question", func(t *testing.T) {
		rig, plan := withProposals(t)
		rig.broker.setPlan(askedPlan(t, plan), false, nil)
		rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)
		rig.replySaid("U09XYZ", "1729.9001", "README.md")
		if n := len(rig.broker.handoffCalls()); n != 1 {
			t.Fatalf("%d handoff calls: a bystander answered the author's question", n)
		}
	})

	t.Run("an answer that leaves a marker asks the next question", func(t *testing.T) {
		rig, plan := withProposals(t)
		rig.broker.setPlan(askedPlan(t, plan), false, nil)
		rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)
		rig.replySaid("U02DEF", "1729.9001", "README.md")
		posts := rig.posts()
		if len(posts) != 2 || !strings.Contains(posts[1].text, askedQuestion) {
			t.Fatalf("the second question was not asked: %+v", posts)
		}
		if _, ok := posts[1].meta.proposalPayload(); ok {
			t.Fatal("a plan with a marker was posted as a proposal")
		}
	})
}

// A Slack line never shows a raw reason code: a marker the planner did not turn into a question is
// said in plain words, in the thread and on a filing.
func TestUnresolvedMarkerInPlainWords(t *testing.T) {
	refusal := "unresolved_marker: the plan still has a clarification to answer; resolve every gap and remove every marker before filing"
	if _, text, _ := notProposed(refusal, "U02DEF"); strings.Contains(text, "unresolved_marker") ||
		!strings.Contains(text, "the plan needs one answer first") {
		t.Fatalf("notProposed: %q", text)
	}
	if _, text, _ := notFiled("rejected: unresolved_marker", "U02DEF", "Smana/ai-native-ref", true); strings.Contains(text, "unresolved_marker") ||
		!strings.Contains(text, "the plan needs one answer first") {
		t.Fatalf("notFiled: %q", text)
	}
}

// A reply that re-plans a live proposal into a question asks it, and the card it supersedes loses its
// buttons: its plan is no longer the one the broker holds.
func TestReplanIntoAQuestionRetiresTheCard(t *testing.T) {
	rig, plan, ts := proposalAt(t, 1)
	rig.broker.setNextPlan(askedPlan(t, plan), false)
	rig.replySaid("U02DEF", "1729.9001", "which page, though?")

	posts := rig.posts()
	if len(posts) != 1 || !strings.Contains(posts[0].text, askedQuestion) {
		t.Fatalf("the question was not asked: %+v", posts)
	}
	if m, _, ok := rig.app.threadAsked(rig.slack.thread()); !ok || m.TS != posts[0].ts {
		t.Fatal("the question is not what the thread's next reply answers")
	}
	if live := liveProposals(t, rig.slack); len(live) != 0 {
		t.Fatalf("the superseded card %s is still live: %+v", ts, live)
	}
}
