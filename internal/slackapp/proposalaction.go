// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Confirm and Cancel (CD7's two controls), riding the approval design's one interactivity branch: the
// same signed body, the same door, the same dedup discipline (§6, CD29). What is this file's is the
// family's own decode: a card's value names an approval, a proposal's names the hash and the revision
// it was drawn for, and the dispatcher stops at the value's version prefix (the design's §6 step 3,
// the conversational design's interactivity row). A value this family cannot read is this family's
// refusal, said to the person who pressed — never the dispatcher's silence, and never a filing.
//
// Nothing a Confirm or a Cancel does is written into the thread: the proposal message is the thread's
// one conversation, and a press is one person's act on it. Every verdict is one chat.postEphemeral to
// the id Slack signed the click with, in the proposal's thread, as the card path's answers are.

// The lines one press is answered with. They are a closed vocabulary on the same rule as the card's
// (§6's refusal table): one fixed sentence per reason, naming no repository, branch, GitHub login or
// other person beyond the ids Slack signed and the message's own metadata.
const (
	// planChangedLine is the one sentence for a plan that moved after it was drawn: the button's hash
	// is no longer the metadata's, or run_create recomposed a different plan. Two reaches — this
	// family's own check before the broker and its refusal after — and one spelling.
	planChangedLine = "Not filed: the plan changed after this was drawn; mention me again to re-plan it."
	// proposalOffLine is a press in a channel whose proposals switch is off (the switch flipped under a
	// live message). The buttons no longer file anything here; the message's ✅ fallback does not
	// depend on the switch, so the one line teaches it.
	proposalOffLine = "Not filed: proposals are off in this channel; a maintainer reacts :white_check_mark: to the message to file it."
	// proposalGoneLine is a press on a message whose metadata is no longer a proposal: the plan lives
	// nowhere else (CD20) and no rendered text is ever parsed to recover one, so the fix is a new
	// mention.
	proposalGoneLine = "Not filed: this proposal can no longer be read; mention me again."
	// proposalButtonLine is a press whose value this family cannot parse: the button and the message no
	// longer belong together, and the fix is to read the message again.
	proposalButtonLine = "Not filed: this proposal's button no longer matches it; open the message again."
	// proposalRepoGoneLine is Review Focus 3 and CD24: the channel's repository list is a generated
	// value that changes under a live proposal, so a Confirm is refused rather than filed against a
	// channel that stopped serving the plan's repository.
	proposalRepoGoneLine = "Not filed: %s isn't served in this channel any more; ask an operator."
	// proposalStaleLine is a press on a message the thread has already replaced. An inert repost keeps
	// the replaced message's metadata — Slack keeps it through an update that carries none — so only
	// the thread says which card is live, and the live one's buttons are the ones to press (Review
	// Focus 1).
	proposalStaleLine = "Not filed: a newer revision of this proposal is in this thread; press that message's buttons."
	// proposalClosedPressLine is a press in a thread whose task is already filed or cancelled: the
	// thread's message already says so, and the press files nothing.
	proposalClosedPressLine = "Not filed: this thread's task is already filed or cancelled; mention me again to plan another."
	// proposalUnreadableLine is a press whose message or thread the App could not read just now: a
	// passing failure, so pressing again is the fix.
	proposalUnreadableLine = "Not filed: Slack could not be read just now; press the button again."
	// proposalOnlyAuthorLine is a Cancel from anyone but the proposal's author (CD7): the person who
	// asked withdraws their own plan, and a maintainer confirms it.
	proposalOnlyAuthorLine = "Only <@%s> cancels this proposal; a maintainer confirms it."
	// proposalCancelledLine is what the message becomes on Cancel, and what its presser reads: one
	// spelling for the thread and the person.
	proposalCancelledLine = "Proposal cancelled by <@%s>; mention me again to plan another."
	// proposalCancelFailedLine is a Cancel Slack refused to write: the message keeps its buttons, and
	// its author is told the one thing to do.
	proposalCancelFailedLine = "Not cancelled: Slack could not rewrite the message just now; press Cancel again."
)

// proposalAction is Confirm and Cancel at the door: the family's own switch, its own dedup key, and
// the work queued behind Slack's three-second answer — the door must answer 200 inside Slack's window
// (§6 step 2), and a filing is a broker call. It is the same shape as the card's own case, because
// both families ride one branch (CD29).
//
// The value is decoded inside this case and never by the dispatcher: parseProposalValue's four parts
// are this family's shape, so a card's value on one of these buttons is this family's refusal.
//
// The outcome is the door's word — "not_enabled", "duplicate", "busy" or "queued", whose work counts
// its own ("filed", "duplicate", "unrewritten", "cancelled", "refused", "error", "ignored").
func (a *App) proposalAction(ctx context.Context, in interaction) string {
	if !a.Proposals.Enabled {
		// Answered locally, with no broker call: the switch was flipped under a live message, and the
		// ✅ fallback the line names does not depend on it.
		a.sayProposalOff(ctx, in)
		return "not_enabled"
	}
	// Stable across Slack's retries of one press and distinct from the card's key, which is keyed by
	// the approval it binds: a proposal press binds a message and a moment, not an approval (F-10).
	key := proposalDedupKey(in)
	if !a.events.add(key) {
		return "duplicate"
	}
	if !a.enqueue(func() {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
		defer cancel()
		a.count(hctx, "interactions", a.proposalPress(hctx, in))
	}) {
		a.events.forget(key)
		return "busy"
	}
	return "queued"
}

// proposalDedupKey is one proposal press's identity (§6 step 4): the button, the person, the moment
// Slack stamped the press with and the message it was made on. It is stable across Slack's retries of
// one interaction and distinct from the card's key, which names the approval a press binds.
func proposalDedupKey(in interaction) string {
	return "i|" + in.ActionID + "/" + in.User + "/" + in.ActionTS + "/" + in.MessageTS
}

// sayProposalOff tells the presser alone that the buttons no longer file anything here, inside
// pressAnswerBound: the one Slack call the door makes on this path, and so the one that must not
// outlast Slack's three seconds. A failed or timed-out answer is logged and dropped, as the card
// path's is: it is never retried and never turns the press into a filing.
func (a *App) sayProposalOff(ctx context.Context, in interaction) {
	actx, cancel := context.WithTimeout(ctx, pressAnswerBound)
	defer cancel()
	if err := a.Slack.PostEphemeral(actx, in.Channel, in.User, "", proposalOffLine); err != nil {
		a.log().Warn("slack ephemeral post failed", "channel", in.Channel, "err", err)
	}
}

// proposalPress is the press's work, after the 200 (under handleTimeout). The message it was made on
// is read once and decides everything: the plan, its author, the hash its buttons were drawn for
// (CD20 — the metadata is the whole proposal, so an App that has just restarted reads exactly what an
// App that watched the mention would). Nothing is taken from a rendered word, and no URL from the
// body is ever dialled.
func (a *App) proposalPress(ctx context.Context, in interaction) string {
	msg, err := a.Slack.Message(ctx, in.Channel, in.MessageTS)
	if err != nil {
		a.log().Warn("slack message fetch failed", "channel", in.Channel, "ts", in.MessageTS, "err", err)
		return a.pressAnswer(ctx, in, "", proposalUnreadableLine, "error")
	}
	thread := msg.ThreadTS
	if thread == "" {
		thread = msg.TS
	}
	// Only the App's own message carrying a proposal whose hash binds its plan, and whose author is an
	// id this workspace could hold, is one: the same decoder the thread's flows read, so a draft, a
	// Filed line or a foreign message is no more a proposal to a press than to a reply.
	p, ok := msg.Metadata.proposalPayload()
	if msg.User != a.BotUserID || !ok || !userRE.MatchString(p.Author) {
		return a.pressAnswer(ctx, in, thread, proposalGoneLine, "refused")
	}
	// The value is this family's, read here rather than by the dispatcher: the version, the family,
	// the hash's shape and the revision.
	hash, _, ok := parseProposalValue(in.Value)
	if !ok {
		return a.pressAnswer(ctx, in, thread, proposalButtonLine, "refused")
	}
	if hash != p.Hash {
		// The buttons bind a hash the message no longer says (Review Focus 1): nothing is filed from
		// either side of the disagreement, and the message is drawn again from its own metadata — no
		// planner, no broker, no thread line — so its buttons bind what it shows again.
		a.redrawProposal(ctx, in.Channel, in.MessageTS, p)
		return a.pressAnswer(ctx, in, thread, planChangedLine, "refused")
	}
	switch in.ActionID {
	case actionProposalConfirm:
		// The press is taken up as a Confirm from here: one action, whatever the filing or the broker
		// answers, and the outcome word says what came of it (CD7).
		a.proposalUpdate(ctx, countConfirm)
		return a.confirmProposal(ctx, in, thread, p)
	case actionProposalCancel:
		a.proposalUpdate(ctx, countCancel)
		return a.cancelProposal(ctx, in, thread, p)
	}
	return "ignored"
}

// pressAnswer is the press's one answer: one chat.postEphemeral to the id Slack signed the click
// with, in the proposal's thread, success included — as the card path's answer is. code is the
// outcome the caller counts; a failed answer is "error", because the person was told nothing and that
// is what the counter should say.
func (a *App) pressAnswer(ctx context.Context, in interaction, thread, line, code string) string {
	if err := a.Slack.PostEphemeral(ctx, in.Channel, in.User, thread, line); err != nil {
		a.log().Warn("slack ephemeral post failed", "channel", in.Channel, "err", err)
		return "error"
	}
	return code
}

// redrawProposal rewrites the message from the metadata it already holds: the sections the payload
// renders, the buttons binding the hash and the revision it carries, and the same metadata back.
// Nothing is planned, computed or filed, and its failure is logged and dropped — the presser's line
// already says nothing was filed, and a repost would give the thread two live proposals.
func (a *App) redrawProposal(ctx context.Context, channel, ts string, p ProposalPayload) {
	text, blocks, err := proposalSections(p, nil, nil, channel)
	if err != nil {
		a.log().Warn("proposal redraw failed", "channel", channel, "ts", ts, "err", err)
		return
	}
	meta, err := proposalMeta(p)
	if err != nil {
		a.log().Warn("proposal redraw metadata failed", "channel", channel, "ts", ts, "err", err)
		return
	}
	if err := a.Slack.UpdateProposal(ctx, channel, ts, text, blocks, meta); err != nil {
		a.log().Warn("proposal redraw update failed", "channel", channel, "ts", ts, "err", err)
	}
}

// confirmProposal files the plan the message holds, once the press's own checks have passed: CD8's
// button on the run_create path the ✅ fallback uses, with who may file left to the broker's linked
// maintainer check.
//
// The channel's repository list is read here, at the press, because CD24 makes it a generated value
// that changes under a live proposal: a Confirm whose plan is no longer served is refused, never
// filed (Review Focus 3). fileProposal reads the thread under the edit lock and refuses a message a
// revision replaced, so what is filed is the thread's live proposal and nothing else (Review Focus 1).
//
// The outcome is fileProposal's, and its line goes to the presser alone: the thread watched the plan,
// but only the person who pressed needs to be told what the press did.
func (a *App) confirmProposal(ctx context.Context, in interaction, thread string, p ProposalPayload) string {
	ch := a.Channels[in.Channel]
	if !slices.Contains(ch.Repositories, p.Plan.Repository) {
		return a.pressAnswer(ctx, in, thread,
			fmt.Sprintf(proposalRepoGoneLine, slackText(p.Plan.Repository)), "refused")
	}
	code, line := a.fileProposal(ctx, in.Channel, thread, in.MessageTS, in.User, p, true)
	if line == "" {
		return code
	}
	return a.pressAnswer(ctx, in, thread, line, code)
}

// cancelProposal withdraws the author's own proposal (CD7): the message becomes the cancelled line in
// place, its blocks are emptied so no button survives it, and its metadata becomes a Filed line's
// marker — which is what closes the thread to a later reply, mention or ✅, since threadProposal reads
// a marker as closed. A cancellation starts no room, so the marker carries the repository alone.
//
// Only the proposal's author cancels: a maintainer confirms someone else's plan, and the person who
// asked withdraws it. Anyone else's press is answered, and the buttons stay where they are.
//
// The thread is read under the edit lock the re-plans take, and before anything is written: a revision
// in flight finishes first, and the message cancelled is the one the thread shows — a press on a
// message a repost replaced cancels nothing, since the thread's live card would otherwise be left with
// working buttons on a closed thread (Review Focus 1).
func (a *App) cancelProposal(ctx context.Context, in interaction, thread string, p ProposalPayload) string {
	if p.Author != in.User {
		return a.pressAnswer(ctx, in, thread, fmt.Sprintf(proposalOnlyAuthorLine, p.Author), "refused")
	}
	// The lock is taken and released around the write alone: the answer to the presser is a Slack call
	// of its own, and holding the App's edits shut while it runs would make one press wait out another.
	a.editMu.Lock()
	code, line := a.cancelInPlace(ctx, in, thread, p)
	a.editMu.Unlock()
	return a.pressAnswer(ctx, in, thread, line, code)
}

// cancelInPlace is cancelProposal's work under a.editMu: the thread read, the check that the pressed
// message is the one the thread shows, and the write that closes it. It posts nothing, so the answer
// to the presser never holds the App's edits.
func (a *App) cancelInPlace(ctx context.Context, in interaction, thread string, p ProposalPayload) (code, line string) {
	msgs, err := a.Slack.Thread(ctx, in.Channel, thread)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", in.Channel, "err", err)
		return "error", proposalUnreadableLine
	}
	if code, line := a.threadPress(in, msgs); code != "" {
		return code, line
	}
	line = fmt.Sprintf(proposalCancelledLine, in.User)
	mark, err := metaPayload(MarkerPayload{Repo: p.Plan.Repository}) // no room: a cancellation starts none
	if err != nil {
		a.log().Error("cancel mark payload: the proposal stays editable", "err", err)
		mark = json.RawMessage(`{}`)
	}
	// An emptied block list, not none: chat.update leaves a message's blocks as they are when the key
	// is absent, so nil would keep Confirm and Cancel live on a cancelled proposal (the same write
	// fileProposal, repostProposal and App.inert make).
	if err := a.Slack.UpdateProposal(ctx, in.Channel, in.MessageTS, line, json.RawMessage("[]"),
		&Metadata{EventType: filedEventType, EventPayload: mark}); err != nil {
		a.log().Warn("proposal cancel failed", "channel", in.Channel, "ts", in.MessageTS, "err", err)
		return "error", proposalCancelFailedLine
	}
	return "cancelled", line
}

// threadPress is the thread's verdict on a press, read from the messages alone: nothing when the
// pressed message is the thread's newest valid proposal and the thread is open, else the outcome and
// the line the presser reads. An inert repost keeps the replaced message's metadata, so the metadata
// alone cannot tell a live proposal from a replaced one — the thread can, and this is the check
// fileProposal makes for the ✅.
func (a *App) threadPress(in interaction, msgs []Message) (code, line string) {
	latest, closed := a.threadProposal(msgs)
	switch {
	case closed:
		return "refused", proposalClosedPressLine
	case latest == nil || latest.TS != in.MessageTS:
		return "refused", proposalStaleLine
	}
	return "", ""
}
