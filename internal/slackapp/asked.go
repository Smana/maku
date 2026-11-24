// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Smana/maku/internal/intake"
)

// askedEventType tags the message that asks the planner's one question (Smana/maku#623): a plan whose
// handoff came back `asked` still carries a `[NEEDS CLARIFICATION: gN …]` marker, so the broker's final
// check refuses it and there is no plan a Confirm could bind. The thread asks the question instead,
// as `handoff` does in a coding client, and the author's reply continues the handoff by its id.
const askedEventType = "factory_plan_question"

// acceptRestReply is the reply that takes the planner's recommendation for every open question, as
// `handoff`'s `acceptRest` does.
const acceptRestReply = "accept the rest"

// AskedPayload is the question message's metadata: the handoff id the broker stored the plan under,
// the mention's author, who alone answers it, and the revision this question round is. No plan and no
// hash: nothing is filed from this message.
type AskedPayload struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Rev    int    `json:"rev"`
}

// askedPayload decodes a question message's metadata; false for anything else.
func (m *Metadata) askedPayload() (AskedPayload, bool) {
	if m == nil || m.EventType != askedEventType {
		return AskedPayload{}, false
	}
	var p AskedPayload
	if json.Unmarshal(m.EventPayload, &p) != nil || !linkCodeRE.MatchString(p.ID) || !userRE.MatchString(p.Author) ||
		p.Rev < 1 || p.Rev > maxProposalRevisions {
		return AskedPayload{}, false
	}
	return p, true
}

// askedLine is the question as the thread says it, the way the thread quotes a scoper's: the question,
// the planner's recommendation, and the two replies that answer it.
func askedLine(q intake.Question) string {
	line := "The plan needs one answer first. The planner asks: " + slackText(strings.TrimSpace(q.Text))
	if r := strings.TrimSpace(q.Recommended.Answer); r != "" {
		line += "\nRecommended: " + slackText(r)
	}
	return line + "\nReply to answer, or reply `" + acceptRestReply + "` to take the recommendation."
}

// isMarkerRefusal is the final check's refusal of a plan that still carries a marker.
func isMarkerRefusal(err error) bool {
	var r Refused
	if !errors.As(err, &r) {
		return false
	}
	code, _, _ := strings.Cut(strings.TrimPrefix(string(r), "rejected: "), ":")
	return code == intake.ReasonUnresolvedMarker
}

// threadAsked is the thread's newest question message, when no proposal, Filed or Cancelled line of the
// App's came after it: that question is what a reply in the thread answers.
func (a *App) threadAsked(msgs []Message) (*Message, AskedPayload, bool) {
	var (
		latest *Message
		p      AskedPayload
	)
	for i := range msgs {
		m := &msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if ap, ok := m.Metadata.askedPayload(); ok {
			latest, p = m, ap
			continue
		}
		if _, ok := m.Metadata.proposalPayload(); ok {
			latest = nil
		} else if _, ok := m.Metadata.markerPayload(); ok {
			latest = nil
		}
	}
	return latest, p, latest != nil
}

// ask posts the planner's question in the thread, and makes the proposal it supersedes inert (stale,
// when there is one): that card's plan is no longer the one the broker holds, so its buttons must not
// stay live. The outcome is "asked", or "error" when Slack did not take the question.
func (a *App) ask(ctx context.Context, channel, thread string, stale *Message, p AskedPayload, q intake.Question) string {
	payload, err := metaPayload(p)
	if err != nil {
		a.log().Warn("question metadata failed", "channel", channel, "err", err)
		return "error"
	}
	if _, err := a.Slack.Post(ctx, channel, thread, askedLine(q), &Metadata{EventType: askedEventType, EventPayload: payload}); err != nil {
		a.log().Warn("slack post failed", "channel", channel, "err", err)
		return "error"
	}
	if stale != nil {
		if err := a.Slack.Update(ctx, channel, stale.TS, proposalReplacedLine, json.RawMessage("[]")); err != nil {
			a.log().Warn("proposal inert update failed", "channel", channel, "ts", stale.TS, "err", err)
		}
	}
	return "asked"
}

// answerAsked answers a reply to the planner's question: the author's words continue the handoff by its
// id (`accept the rest` as acceptRest), and the plan that comes back is asked again while a marker
// remains, or posted as the proposal with Confirm once none does. Anyone else's reply is "ignored".
func (a *App) answerAsked(ctx context.Context, ev slackEvent, ch Channel, thread string, q AskedPayload) string {
	if q.Author != ev.User {
		return "ignored"
	}
	a.proposalUpdate(ctx, countReply)
	if q.Rev >= maxProposalRevisions {
		return a.refusedLine(ctx, ev.Channel, thread, replyPlannedMost)
	}
	in := HandoffRequest{ID: q.ID, OnBehalfOf: "slack:" + a.TeamID + "/" + ev.User, CeilingUSD: ch.MaxCostUSD}
	reply := strings.TrimSpace(ev.Text)
	if strings.EqualFold(strings.TrimRight(reply, ".! "), acceptRestReply) {
		in.AcceptRest = true
	} else {
		in.Answer = clipReply(reply, maxProposalContextBytes)
	}
	st := &planStatus{}
	defer a.settle(ctx, ev.Channel, st, planDoneLine)
	res, err := a.plan(ctx, ev.Channel, thread, st, in)
	if code := a.planFailed(ctx, ev.Channel, thread, ev.User, ch.Repositories, err); code != "" {
		return code
	}
	next := AskedPayload{ID: res.ID, Author: q.Author, Rev: q.Rev + 1}
	if res.Asked != nil {
		return a.ask(ctx, ev.Channel, thread, nil, next, *res.Asked)
	}
	prop, err := newProposal(res.Plan, res.Hash, q.Author, next.Rev)
	if err != nil {
		return a.notShown(ctx, ev.Channel, thread, err)
	}
	prop.ID = res.ID
	prop.Degraded = res.Degraded
	prop.Pins = res.Pins
	prop.Workflow = res.Workflow
	prop.DefaultDescription, prop.DefaultCeilingUSD = res.DefaultDescription, res.DefaultCeilingUSD
	prop.Path, prop.StepUpUSD = res.Path, res.StepUpUSD
	return a.postProposalIn(ctx, ev.Channel, thread, st, prop, res.Question)
}
