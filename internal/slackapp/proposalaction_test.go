// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// The id that presses a button in the tests below and the two that own the seeded proposal: the
// plan's author is the mention's user, and a presser who is not the author is the maintainer a
// Confirm is for.
const (
	proposalAuthor  = "U02DEF"
	proposalPresser = "U07GHI"
)

// fixtureHash is a hash of the right shape that binds no plan: the value a button drawn for another
// message carries, refused before anything else is read.
var fixtureHash = "sha256:" + strings.Repeat("a", 64)

// proposalPressOf is the payload of a press on the proposal at ts: one action whose value is the one
// the message drew, and the id Slack signed the click with.
func proposalPressOf(t *testing.T, ts, user, actionID, value string) string {
	t.Helper()
	return pressPayload(t, func(m map[string]any) {
		m["user"] = map[string]any{"id": user}
		m["container"] = map[string]any{"type": "message", "message_ts": ts, "channel_id": "C03"}
		m["message"] = map[string]any{"ts": ts}
		m["actions"] = action(actionID, value)
	})
}

// pressSaid is the press's one answer: one chat.postEphemeral to the id Slack signed the click with,
// in the proposal's thread, and no line posted into the thread itself.
func pressSaid(t *testing.T, rig *appRig, user, thread, want string) {
	t.Helper()
	e := rig.slack.whispers()
	if len(e) != 1 || e[0].user != user || e[0].channel != "C03" || e[0].thread != thread || e[0].text != want {
		t.Fatalf("the presser saw %+v, want one %q in the thread", e, want)
	}
	if n := len(rig.posts()); n != 0 {
		t.Fatalf("%d thread posts: a press's verdict goes to the presser alone", n)
	}
}

// CD7 and the live proof's row 9: a maintainer's Confirm files the plan the metadata holds and turns
// the message into the Filed line; the hash in the button must equal the hash in the metadata.
func TestProposalConfirm(t *testing.T) {
	t.Run("a maintainer's Confirm files the plan the message showed", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.broker.room = "26zfnuxm"
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		calls := rig.broker.runCreates()
		if len(calls) != 1 {
			t.Fatalf("%d run_create calls, want one: %+v", len(calls), rig.broker.all())
		}
		c := calls[0]
		var got intake.Plan
		want := plan
		want.Hash = intake.Hash(plan) // a plan sent without one gains the hash it computes
		if err := json.Unmarshal(c.Plan, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("the call's plan is %+v (%v), want the message's", got, err)
		}
		if c.PlanHash != intake.Hash(plan) || c.OnBehalfOf != "slack:T01ABC/"+proposalPresser ||
			c.Key != "T01ABC/C03/"+proposalThreadTS || c.Repo != plan.Repository {
			t.Fatalf("the call: %+v", c)
		}

		// The message becomes the Filed line in place, with the tag that closes the thread and no
		// button left on it: one message from the mention to the room.
		filed := "Filed #42 for <@" + proposalPresser + ">; the factory starts it. Room: https://rooms.example/r/26zfnuxm"
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != ts || edits[0].channel != "C03" || edits[0].text != filed {
			t.Fatalf("the message was not turned into the Filed line: %+v", edits)
		}
		if edits[0].meta == nil || edits[0].meta.EventType != filedEventType {
			t.Fatalf("the rewritten message's tag: %+v", edits[0].meta)
		}
		if mark, ok := edits[0].meta.markerPayload(); !ok || mark.Room != "26zfnuxm" || mark.Repo != plan.Repository {
			t.Fatalf("the rewritten message's marker: %+v %v", mark, ok)
		}
		if string(edits[0].blocks) != "[]" {
			t.Fatalf("the filed message's blocks are %q, want an emptied list", edits[0].blocks)
		}
		if th, ok := rig.threads.get("26zfnuxm"); !ok || th.Channel != "C03" || th.TS != proposalThreadTS {
			t.Fatalf("the thread map: %+v %v", th, ok)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, filed)
		if n := rig.metrics.count("interactions", "filed"); n != 1 {
			t.Fatalf("the filing was counted %d times", n)
		}
	})

	// §6 step 4's dedup, on this family's own key: Slack's retry of one press answers 200 and does
	// nothing else, so one press is one filing.
	t.Run("a second press of the same button answers 200 and calls nothing", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		press := formBody(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1)))
		for i := range 2 {
			if st := rig.postInteraction(press); st != http.StatusOK {
				t.Fatalf("press %d: %d, want 200", i, st)
			}
		}
		if n := len(rig.broker.runCreates()); n != 1 {
			t.Fatalf("a retried press filed %d issues", n)
		}
		if e := rig.slack.whispers(); len(e) != 1 {
			t.Fatalf("the presser was answered %d times, want 1: %+v", len(e), e)
		}
		if n := rig.metrics.count("interactions", "duplicate"); n != 1 {
			t.Fatalf("interactions/duplicate counted %d times, want 1", n)
		}
	})

	// Review Focus 1 through the button: a value whose hash the metadata does not carry binds a plan
	// the message no longer says. Nothing is filed from either, and the message is drawn again from
	// its own metadata — no planner, no broker — so its buttons bind what it shows again.
	t.Run("a button whose hash the message no longer carries re-renders it and files nothing", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(fixtureHash, 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a stale button reached the broker: %+v", calls)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, planChangedLine)
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != ts {
			t.Fatalf("the message was not redrawn once: %+v", edits)
		}
		p, ok := edits[0].meta.proposalPayload()
		if !ok || p.Hash != intake.Hash(plan) || p.Rev != 1 {
			t.Fatalf("the redrawn message's payload: %+v (%v)", p, ok)
		}
		if !strings.Contains(string(edits[0].blocks), proposalValue(intake.Hash(plan), 1)) {
			t.Fatalf("the redrawn buttons do not bind the message's own hash: %s", edits[0].blocks)
		}
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
	})
}

// One line per refusal, no oracle about people or repositories, and every one of them an ephemeral
// to the presser: a stale value, a non-maintainer, a message whose metadata no longer holds, a
// channel that no longer serves the plan's repository. No refusal posts a second thread message.
func TestProposalConfirmRefusals(t *testing.T) {
	t.Run("the broker refuses the presser as not a maintainer", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.broker.refuse("slack:T01ABC/"+proposalPresser, "rejected: not_a_maintainer")
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		want := "Not filed: <@" + proposalPresser + ">'s GitHub account is not a maintainer of " +
			plan.Repository + "; a maintainer must confirm it."
		pressSaid(t, rig, proposalPresser, proposalThreadTS, want)
		if n := rig.broker.newIssues(); n != 0 {
			t.Fatalf("a refused press filed %d issues", n)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a refusal rewrote the message %d times", n)
		}
	})

	t.Run("a message whose metadata no longer holds a proposal", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT", Text: "Proposal v1 · " + fixtureHash})
		if st := rig.interact(proposalPressOf(t, "1729.0003", proposalPresser, actionProposalConfirm,
			proposalValue(fixtureHash, 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, proposalGoneLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a message with no proposal reached the broker: %+v", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a message with no proposal was rewritten %d times", n)
		}
	})

	// Review Focus 3 and CD24: the channel's repository list is a generated value and will change
	// under a live proposal. A Confirm is refused, never filed against a channel that stopped
	// serving the plan's repository.
	t.Run("a channel that stopped serving the plan's repository", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/cloud-native-ref"}, MaxCostUSD: 2})
		rig.app.Proposals = Proposals{Enabled: true}
		plan := planFixture(t, "slack-one-issue")
		rig.slack.seed(Message{TS: proposalThreadTS, User: proposalAuthor, Text: "<@U0BOT> " + mentionWords})
		p, err := newProposal(plan, intake.Hash(plan), proposalAuthor, 1)
		if err != nil {
			t.Fatalf("newProposal: %v", err)
		}
		rig.slack.seed(Message{TS: "1729.0002", ThreadTS: proposalThreadTS, User: "U0BOT",
			Text: "Proposal v1 · " + p.Hash, Metadata: metaOf(t, proposalEventType, p)})
		if st := rig.interact(proposalPressOf(t, "1729.0002", proposalPresser, actionProposalConfirm,
			proposalValue(p.Hash, 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		want := "Not filed: " + plan.Repository + " isn't served in this channel any more; ask an operator."
		pressSaid(t, rig, proposalPresser, proposalThreadTS, want)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a re-pointed channel reached the broker: %+v", calls)
		}
	})

	t.Run("a button this family cannot parse", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		value := "v1|proposal|" + strings.Repeat("a", 64) + "|1" // no hash shape: the family's own refusal
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm, value)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, proposalButtonLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("an unparseable button reached the broker: %+v", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("an unparseable button rewrote the message %d times", n)
		}
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
		_ = plan
	})

	// The switch flipped under a live message: the buttons no longer decide here, and the message's
	// ✅ fallback still files it, so the one line teaches that.
	t.Run("the switch flipped under a live proposal", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.app.Proposals = Proposals{}
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		// The door answers before it reads anything, as the card path's not_enabled answer does: the
		// presser is told, and no message or thread is read to say it.
		pressSaid(t, rig, proposalPresser, "", proposalOffLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a channel with proposals off reached the broker: %+v", calls)
		}
		if n := rig.metrics.count("interactions", "not_enabled"); n != 1 {
			t.Fatalf("interactions/not_enabled counted %d times, want 1", n)
		}
	})

	// The same hash check the Confirm's own case asserts, here as the one refusal whose message is
	// redrawn: nothing is filed and no planner is called.
	t.Run("the plan changed after the button was drawn", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(fixtureHash, 2))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, planChangedLine)
		if n := rig.broker.newIssues(); n != 0 {
			t.Fatalf("a stale button filed %d issues", n)
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 {
			t.Fatalf("%d edits, want the one re-render: %+v", len(edits), edits)
		}
		if p, ok := edits[0].meta.proposalPayload(); !ok || p.Hash != intake.Hash(plan) {
			t.Fatalf("the re-render's payload: %+v (%v)", p, ok)
		}
	})

	// Review Focus 1 again, from the thread's side: an edit in place keeps the ts, so a message the
	// thread has moved past is the one case the ts cannot catch. The press is refused, never filed,
	// and nothing is rewritten — the newer message is the live one and already shows the plan.
	t.Run("a Confirm on a message the thread replaced", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		newer, err := newProposal(plan, intake.Hash(plan), proposalAuthor, 2)
		if err != nil {
			t.Fatalf("newProposal: %v", err)
		}
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT",
			Text: "Proposal v2 · " + newer.Hash, Metadata: metaOf(t, proposalEventType, newer)})
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, proposalStaleLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a replaced message reached the broker: %+v", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a replaced message was rewritten %d times", n)
		}
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
		if n := rig.metrics.count("interactions", "ignored"); n != 0 {
			t.Fatalf("a press with an answer was counted ignored %d times", n)
		}
	})

	// A thread whose task was filed or cancelled closes to presses too: the message a person pressed
	// may still decode as a proposal (a reposted card's metadata survives the close), so the thread's
	// marker decides, and the presser is told rather than left with silence.
	t.Run("a Confirm on a closed thread", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT",
			Text:     "Filed #42 for <@" + proposalAuthor + ">; the factory starts it. Room: https://rooms.example/r/26zfnuxm",
			Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: plan.Repository, Room: "26zfnuxm"})})
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, proposalClosedPressLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a closed thread reached the broker: %+v", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a closed thread was rewritten %d times", n)
		}
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
	})

	// A message the App cannot read: the press is answered as a passing failure, so the press is its
	// own retry, and nothing is filed from a message nobody read.
	t.Run("a message Slack will not answer for", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.slack.msgErr = errors.New("slack is down")
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, "", proposalUnreadableLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("an unreadable message reached the broker: %+v", calls)
		}
		if n := rig.metrics.count("interactions", "error"); n != 1 {
			t.Fatalf("interactions/error counted %d times, want 1", n)
		}
	})
}

// Cancel is the author's and nobody else's, and a cancelled proposal cannot be filed afterwards.
func TestProposalCancel(t *testing.T) {
	t.Run("the author's Cancel closes the proposal in place", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalAuthor, actionProposalCancel,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		want := "Proposal cancelled by <@" + proposalAuthor + ">; mention me again to plan another."
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != ts || edits[0].channel != "C03" || edits[0].text != want {
			t.Fatalf("the message was not cancelled in place: %+v", edits)
		}
		if string(edits[0].blocks) != "[]" {
			t.Fatalf("the cancelled message's blocks are %q, want an emptied list", edits[0].blocks)
		}
		if edits[0].meta == nil || edits[0].meta.EventType != filedEventType {
			t.Fatalf("the cancelled message's tag: %+v", edits[0].meta)
		}
		if mark, ok := edits[0].meta.markerPayload(); !ok || mark.Repo != plan.Repository || mark.Room != "" {
			t.Fatalf("the cancelled message's marker: %+v %v", mark, ok)
		}
		pressSaid(t, rig, proposalAuthor, proposalThreadTS, want)
		if n := len(rig.broker.all()); n != 0 {
			t.Fatalf("a cancel reached the broker: %+v", rig.broker.all())
		}
		if n := rig.metrics.count("interactions", "cancelled"); n != 1 {
			t.Fatalf("the cancellation was counted %d times", n)
		}
	})

	// PR 5 Task 5's rule: a closed thread files nothing and says nothing, whatever shape the check
	// mark arrives in — the message itself already says the proposal is cancelled.
	t.Run("a check mark on a cancelled proposal is ignored", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.interact(proposalPressOf(t, ts, proposalAuthor, actionProposalCancel, proposalValue(intake.Hash(plan), 1)))
		posts, edits := len(rig.posts()), len(rig.slack.proposalEdits())
		rig.react("C03", ts, proposalAuthor, "white_check_mark")
		if n := len(rig.broker.all()); n != 0 {
			t.Fatalf("a ✅ on a cancelled proposal reached the broker: %+v", rig.broker.all())
		}
		if len(rig.posts()) != posts || len(rig.slack.proposalEdits()) != edits {
			t.Fatalf("a ✅ on a cancelled proposal wrote something: %+v %+v", rig.posts(), rig.slack.proposalEdits())
		}
		if n := rig.metrics.count("events", "ignored"); n != 1 {
			t.Fatalf("events/ignored counted %d times, want 1", n)
		}
	})

	t.Run("a press by anyone else keeps the buttons", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalCancel,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		want := "Only <@" + proposalAuthor + "> cancels this proposal; a maintainer confirms it."
		pressSaid(t, rig, proposalPresser, proposalThreadTS, want)
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a bystander's cancel rewrote the message %d times", n)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if n := len(rig.broker.all()); n != 0 {
			t.Fatalf("a bystander's cancel reached the broker: %+v", rig.broker.all())
		}
	})

	// A thread the App cannot read is not a thread it closes: the author is told, the message keeps
	// its buttons, and the press is the retry.
	t.Run("a thread Slack will not answer for", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.slack.threadErr = errors.New("slack is down")
		if st := rig.interact(proposalPressOf(t, ts, proposalAuthor, actionProposalCancel,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalAuthor, proposalThreadTS, proposalUnreadableLine)
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("an unreadable thread was rewritten %d times", n)
		}
		if n := rig.metrics.count("interactions", "error"); n != 1 {
			t.Fatalf("interactions/error counted %d times, want 1", n)
		}
	})

	// A Cancel Slack refuses to write leaves the proposal live and its buttons working, and its author
	// is told the one thing to do — never that it was cancelled.
	t.Run("a cancel Slack refuses to write", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		rig.slack.failProposalUpdates = 1
		if st := rig.interact(proposalPressOf(t, ts, proposalAuthor, actionProposalCancel,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalAuthor, proposalThreadTS, proposalCancelFailedLine)
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a refused cancel was recorded as %d edits", n)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if n := rig.metrics.count("interactions", "error"); n != 1 {
			t.Fatalf("interactions/error counted %d times, want 1", n)
		}
		if n := rig.metrics.count("interactions", "cancelled"); n != 0 {
			t.Fatalf("a refused cancel was counted cancelled %d times", n)
		}
	})
}

// One dispatcher, two families: a proposal action never reaches the approval path, an approval
// action never reaches this one, and an unknown action id is dropped as it is today.
func TestProposalInteractionDispatch(t *testing.T) {
	// CD29: the two families share one door and one set of ids, and neither family's flag gates the
	// other's — a proposal press in a channel that draws no cards is still the proposal's.
	t.Run("a proposal press in a channel without the approvals flag is the proposal's", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if n := len(rig.broker.runCreates()); n != 1 {
			t.Fatalf("%d filings: the card branch's flag gated the proposal's", n)
		}
		if got := rig.broker.decided(); len(got) != 0 {
			t.Fatalf("a proposal press reached the decide route: %+v", got)
		}
	})

	t.Run("an approval press in a channel with proposals on is the card's", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2, Approvals: true})
		rig.app.Proposals = Proposals{Enabled: true}
		seedCard(t, rig, cardTS, cardApprovalID)
		if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if got := rig.broker.decided(); len(got) != 1 {
			t.Fatalf("the broker was asked to record %+v, want one press", got)
		}
		if n := len(rig.broker.runCreates()); n != 0 {
			t.Fatalf("a card press filed %d issues", n)
		}
	})

	t.Run("an unknown action id is dropped", func(t *testing.T) {
		rig, _, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, "factory_unknown", proposalValue(fixtureHash, 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if n := rig.metrics.count("interactions", "ignored"); n != 1 {
			t.Fatalf("interactions/ignored counted %d times, want 1", n)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("an unknown action id reached the broker: %+v", calls)
		}
		if e := rig.slack.whispers(); len(e) != 0 {
			t.Fatalf("an unknown action id said something: %+v", e)
		}
	})

	// The split itself: a value is read inside the family its action id belongs to, so a proposal
	// press carrying a card's value is this family's refusal — one line, no filing, never ignored.
	t.Run("a proposal press carrying an approval's value is the proposal family's refusal", func(t *testing.T) {
		rig, _, ts := proposalAt(t, 1)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			cardValue(cardApprovalID, cardBranch))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, proposalButtonLine)
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
		if n := rig.metrics.count("interactions", "ignored"); n != 0 {
			t.Fatalf("the split dropped a proposal's value as ignored %d times", n)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a card's value on a proposal button reached the broker: %+v", calls)
		}
	})

	// And the other direction: the card's own case answers a value it cannot read exactly as it
	// always has — 200 ignored, no line, no broker call — never this family's refusal.
	t.Run("an approval press carrying a proposal's value is the card family's", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		if st := rig.interact(pressPayload(t, func(m map[string]any) {
			m["actions"] = action(actionApprove, proposalValue(fixtureHash, 1))
		})); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if n := rig.metrics.count("interactions", "ignored"); n != 1 {
			t.Fatalf("interactions/ignored counted %d times, want 1", n)
		}
		if e := rig.slack.whispers(); len(e) != 0 {
			t.Fatalf("the card's case answered a value it cannot read: %+v", e)
		}
		if got := rig.broker.decided(); len(got) != 0 {
			t.Fatalf("a proposal's value on a card reached the decide route: %+v", got)
		}
	})
}

// replannedSlack is the seam a re-plan needs to be put inside one filing: it answers the read a press
// (or a ✅) makes with the message as that person saw it, and then lets an in-place revision land —
// same ts, next rev, another plan — before the filing path reads the thread under the App's edit lock.
type replannedSlack struct {
	*fakeSlack
	once    sync.Once
	inPlace Message
}

func (s *replannedSlack) Message(ctx context.Context, channel, ts string) (Message, error) {
	m, err := s.fakeSlack.Message(ctx, channel, ts)
	s.once.Do(func() { s.seed(s.inPlace) }) // the revision lands in place, at the same ts
	return m, err
}

// A revision is written in place, so a re-plan that lands between the caller's read and the locked
// thread read keeps the ts the press named: only the payload the thread holds says which plan is live
// (CD14, CD20). Neither path files the plan the message no longer shows — the filing path is the one
// place the comparison is made, so both are covered by the same check.
func TestProposalPlanChangedUnderTheLock(t *testing.T) {
	// replanned is the rig with the seam in place: the message at ts gains a revision carrying another
	// plan the first time anything reads it.
	replanned := func(t *testing.T, rig *appRig, plan intake.Plan, ts string) {
		t.Helper()
		next := clonePlan(t, plan)
		next.Issues[0].Title = "docs: the plan the thread's reply produced"
		p, err := newProposal(next, intake.Hash(next), proposalAuthor, 2)
		if err != nil {
			t.Fatalf("newProposal: %v", err)
		}
		rig.app.Slack = &replannedSlack{fakeSlack: rig.slack, inPlace: Message{
			TS: ts, ThreadTS: proposalThreadTS, User: "U0BOT",
			Text: "Proposal v2 · " + p.Hash, Metadata: metaOf(t, proposalEventType, p)}}
	}

	t.Run("a Confirm refuses a plan the thread re-planned under it", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		replanned(t, rig, plan, ts)
		if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
			proposalValue(intake.Hash(plan), 1))); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		pressSaid(t, rig, proposalPresser, proposalThreadTS, planChangedLine)
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("the plan moved under the press and %+v was filed", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a refused filing rewrote the message %d times", n)
		}
		if n := rig.metrics.count("interactions", "refused"); n != 1 {
			t.Fatalf("interactions/refused counted %d times, want 1", n)
		}
	})

	t.Run("a ✅ refuses a plan the thread re-planned under it", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		replanned(t, rig, plan, ts)
		rig.react("C03", ts, proposalAuthor, "white_check_mark")
		posts := rig.posts()
		if len(posts) != 1 || posts[0].text != planChangedLine || posts[0].thread != proposalThreadTS {
			t.Fatalf("the thread was told %+v, want one %q in its thread", posts, planChangedLine)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("the plan moved under the ✅ and %+v was filed", calls)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a refused ✅ rewrote the message %d times", n)
		}
		if n := rig.metrics.count("events", "refused"); n != 1 {
			t.Fatalf("events/refused counted %d times, want 1", n)
		}
	})
}
