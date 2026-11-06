// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/policy"
)

func TestStepFor(t *testing.T) {
	const pr, issue, room = "https://github.com/o/r/pull/7", "https://github.com/o/r/issues/3", "https://rooms.example/r/room-1"
	held := StepIn{Phase: "AwaitingHuman", PR: 7, PRURL: pr, CanClose: true, RoomURL: room}
	with := func(f func(*StepIn)) StepIn { in := held; f(&in); return in }
	cases := []struct {
		name  string
		in    StepIn
		kind  string
		label string
		url   string
	}{
		{"approval wins over a held PR", with(func(i *StepIn) { i.Approvals = true }), StepApprove, "approve here", room},
		{"approval alone", StepIn{Phase: "Implementing", Approvals: true, RoomURL: room}, StepApprove, "approve here", room},
		{"held PR", held, StepPR, "review and merge or close PR #7", pr},
		{"held PR an approval returns", with(func(i *StepIn) { i.MergeByApproval = true }), StepPR, "review, then approve the current commit or close PR #7", pr},
		{"held PR holding a commit no run reported", with(func(i *StepIn) { i.Reason = "human_commit_in_pr" }), StepPR, "a repository admin merges PR #7: a person pushed a commit the factory cannot vouch for", pr},
		{"held PR, caller may not close", with(func(i *StepIn) { i.CanClose = false }), "", "", ""},
		{"awaiting human, no PR", with(func(i *StepIn) { i.PR = 0 }), "", "", ""},
		{"CI wait", with(func(i *StepIn) { i.Phase = "AwaitingCI" }), "", "", ""},
		{"queued", StepIn{Phase: "Queued", CanClose: true}, "", "", ""},
		{"sealed room", with(func(i *StepIn) { i.Sealed = true; i.Approvals = true }), "", "", ""},
		{"needs author", StepIn{Phase: "Escalated", Reason: "needs_author", Issue: 3, IssueURL: issue, CanClose: true}, StepAnswer, "answer the question on issue #3", issue},
		{"needs author, caller may not close", StepIn{Phase: "Escalated", Reason: "needs_author", Issue: 3, IssueURL: issue}, "", "", ""},
		{"escalated for another reason", StepIn{Phase: "Escalated", Reason: "stuck", Issue: 3, CanClose: true}, "", "", ""},
	}
	for _, c := range cases {
		s := StepFor(c.in)
		switch {
		case c.kind == "" && s != nil:
			t.Errorf("%s: got %+v, want none", c.name, s)
		case c.kind != "" && (s == nil || *s != Step{Kind: c.kind, Label: c.label, URL: c.url, Approve: c.in.MergeByApproval}):
			t.Errorf("%s: got %+v", c.name, s)
		}
	}
}

func TestViewStepFollowsTheCaller(t *testing.T) {
	pr := &envelope.PRFact{Number: 7, URL: "https://github.com/o/r/pull/7"}
	issue := &envelope.IssueFact{Number: 3, URL: "https://github.com/o/r/issues/3"}
	owner := policy.Subject{Kind: envelope.ActorHuman, Role: policy.Owner, WebUI: true}
	watcher := policy.Subject{Kind: envelope.ActorHuman, Role: policy.Watcher, WebUI: true}
	now := time.Now()
	for _, tc := range []struct {
		name string
		st   State
		sub  policy.Subject
		kind string
	}{
		{"merge for an owner", State{Facts: &envelope.TaskFacts{Phase: "AwaitingHuman", PR: pr}}, owner, StepPR},
		{"approve for an owner", State{Facts: &envelope.TaskFacts{Phase: "AwaitingHuman", MergeByApproval: true, PR: pr}}, owner, StepPR},
		{"no merge for a watcher", State{Facts: &envelope.TaskFacts{Phase: "AwaitingHuman", PR: pr}}, watcher, ""},
		{"answer for an owner", State{Facts: &envelope.TaskFacts{Phase: "Escalated", Reason: "needs_author", Issue: issue}}, owner, StepAnswer},
		{"no answer for a watcher", State{Facts: &envelope.TaskFacts{Phase: "Escalated", Reason: "needs_author", Issue: issue}}, watcher, ""},
		{"sealed room", State{Sealed: "Closed", Facts: &envelope.TaskFacts{Phase: "AwaitingHuman", PR: pr}}, owner, ""},
	} {
		got := View(tc.st, "r", "https://rooms.example/r/r", tc.sub,
			GitHubStanding{Login: "ann", MayWrite: policy.Allowed(tc.sub, policy.Close)}, 0, now).Step
		if (got == nil) != (tc.kind == "") || got != nil && got.Kind != tc.kind {
			t.Errorf("%s: step = %+v, want %q", tc.name, got, tc.kind)
		}
		// The step follows the merge need: approve exactly when the facts say an approval returns it.
		if got != nil && tc.st.Facts != nil && got.Approve != tc.st.Facts.MergeByApproval {
			t.Errorf("%s: step = %+v, approve want %t", tc.name, got, tc.st.Facts.MergeByApproval)
		}
	}
}

// PR1 Task 2's deferred case, pinned: a caller who owns the room and is also one of its pull request's
// requested reviewers is asked for the review, and the merge step is not theirs — needs/v1 lists one
// item per pull request and the review comes first (CD26), and the step is the listed need's, so the
// room and the row (rowStep, keyed on the need's action) agree it is none. The reviewer's action is the
// review, which the need's own label names. The same owner without the request gets the merge step,
// which is what makes this a statement about the review rather than about the standing.
func TestAnOwnerWhoIsAlsoARequestedReviewerIsAskedForTheReviewOnly(t *testing.T) {
	pr := &envelope.PRFact{Number: 7, URL: "https://github.com/o/r/pull/7"}
	owner := policy.Subject{Kind: envelope.ActorHuman, Role: policy.Owner, WebUI: true}
	st := State{Facts: &envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "policy_pending", PR: pr}}
	standing := GitHubStanding{Login: "ann", MayWrite: policy.Allowed(owner, policy.Close), Reviewers: []string{"ann"}}
	s := View(st, "r", "https://rooms.example/r/r", owner, standing, 0, time.Now())
	if len(s.NeedsYou) != 1 || s.NeedsYou[0].Kind != NeedReview || s.NeedsYou[0].Action.Kind != "review" ||
		s.NeedsYou[0].PR != 7 || s.NeedsYou[0].Approve {
		t.Fatalf("needs you: %+v", s.NeedsYou)
	}
	if s.Step != nil {
		t.Fatalf("a reviewer's step is the review, not a merge: %+v", s.Step)
	}
	standing.Reviewers = nil
	s = View(st, "r", "https://rooms.example/r/r", owner, standing, 0, time.Now())
	if len(s.NeedsYou) != 1 || s.NeedsYou[0].Kind != NeedMerge {
		t.Fatalf("the same owner, not asked to review: %+v", s.NeedsYou)
	}
	if s.Step == nil || s.Step.Kind != StepPR || s.Step.Label != "review and merge or close PR #7" {
		t.Fatalf("the same owner's step: %+v", s.Step)
	}
}
