// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

const (
	actStop   = "stop"
	actRetry  = "retry"
	actAnswer = "answer"

	actConfirmTTL = 10 * time.Minute

	actStopPrompt         = "<@%s>: stop task #%d? Its run ends now and the pull request stays open."
	actStopPromptNoIssue  = "<@%s>: stop this task? Its run ends now and the pull request stays open."
	actRetryPrompt        = "<@%s>: retry task #%d? It runs again from where it stopped."
	actRetryPromptNoIssue = "<@%s>: retry this task? It runs again from where it stopped."

	actConfirmStopLabel  = "Stop the task"
	actConfirmRetryLabel = "Retry the task"
	actCancelLabel       = "Cancel"
	actLineStopLabel     = "Stop"
	actLineRetryLabel    = "Retry"

	actOnlyPresserLine   = "Only <@%s> confirms this."
	actExpiredLine       = "This confirmation expired."
	actCancelledLine     = "Cancelled by <@%s>."
	actGoneLine          = "This confirmation is no longer active."
	actUnreadableLine    = "Slack could not be read just now; press the button again."
	actStopDisabledLine  = "Stopping a task from Slack is not turned on in this channel. Apply the `factory/stop` label on the issue."
	actRetryDisabledLine = "Retrying a task from Slack is not turned on in this channel. Comment `/factory retry` on the issue."

	actStoppedLine            = "Stopped."
	actRetriedLine            = "Retried."
	actStopNotMaintainerLine  = "Only a maintainer of this repository can stop a task."
	actRetryNotMaintainerLine = "Only a maintainer of this repository can retry a task."
	actNotRunningLine         = "This task is not running."
	actNotEscalatedLine       = "This task is not escalated."
	actNotLinkedLine          = "<@%s> is not linked to a GitHub account here. Mention me with `link`, then try again."
	actUnverifiedLine         = "The factory could not check your GitHub account just now; try again in a minute."
	actUnknownTaskLine        = "This task could not be found."
	actUnavailableLine        = "The factory could not be reached just now; try again in a minute."
)

func actValue(action, room string) string {
	return pressVersion + "|act|" + action + "|" + room
}

func parseActValue(v string) (action, room string, ok bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 4 || parts[0] != pressVersion || parts[1] != "act" ||
		(parts[2] != actStop && parts[2] != actRetry && parts[2] != actAnswer) || !roomRE.MatchString(parts[3]) {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// actLineBlocks attaches the Stop button to a waiting-for-human thread line when stop is turned on
// in ch, and the Retry button to an escalated end line when retry is turned on in ch.
func actLineBlocks(e webhook.Event, ch Channel, line string) json.RawMessage {
	var action, actionID, label string
	switch {
	case e.Type == "room.waiting_for_human" && ch.actsOn(actStop):
		action, actionID, label = actStop, actionActStop, actLineStopLabel
	case e.Type == "room.ended" && isEscalatedEnd(e.State) && ch.actsOn(actRetry):
		action, actionID, label = actRetry, actionActRetry, actLineRetryLabel
	default:
		return nil
	}
	blocks := []slackBlock{
		sectionBlock(line),
		{
			Type: "actions",
			Elements: []slackButton{{
				Type:     "button",
				Text:     &slackBlockText{Type: "plain_text", Text: label},
				ActionID: actionID,
				Value:    actValue(action, e.Room),
			}},
		},
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return nil
	}
	return raw
}

func isEscalatedEnd(state string) bool {
	return webhook.Clip("state", state) == summary.StateFailed || state == "escalated"
}

// actAction deduplicates and enqueues a stop or retry button press or confirmation click.
func (a *App) actAction(ctx context.Context, in interaction) string {
	key := proposalDedupKey(in)
	if !a.events.add(key) {
		return "duplicate"
	}
	if !a.enqueue(func() {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
		defer cancel()
		a.count(hctx, "interactions", a.actPress(hctx, in))
	}) {
		a.events.forget(key)
		return "busy"
	}
	return "queued"
}

// actConfirm posts a ten-minute confirmation message in th named to user for stopping or retrying
// the task in room. One sponsored task read supplies the issue number when available, and no
// classifier call is made.
func (a *App) actConfirm(ctx context.Context, th Thread, action, room, user string) string {
	if (action != actStop && action != actRetry) || !roomRE.MatchString(room) || !userRE.MatchString(user) {
		return "ignored"
	}
	issue := 0
	if sum := a.taskSummary(ctx, room); sum.Status.Issue != nil {
		issue = sum.Status.Issue.Number
	}

	var text, confirmLabel, style string
	if action == actStop {
		confirmLabel, style = actConfirmStopLabel, "danger"
		if issue > 0 {
			text = fmt.Sprintf(actStopPrompt, user, issue)
		} else {
			text = fmt.Sprintf(actStopPromptNoIssue, user)
		}
	} else {
		confirmLabel, style = actConfirmRetryLabel, "primary"
		if issue > 0 {
			text = fmt.Sprintf(actRetryPrompt, user, issue)
		} else {
			text = fmt.Sprintf(actRetryPromptNoIssue, user)
		}
	}

	val := actValue(action, room)
	blocks, err := json.Marshal([]slackBlock{
		sectionBlock(text),
		{
			Type: "actions",
			Elements: []slackButton{
				{
					Type:     "button",
					Text:     &slackBlockText{Type: "plain_text", Text: confirmLabel},
					ActionID: actionActConfirm,
					Value:    val,
					Style:    style,
				},
				{
					Type:     "button",
					Text:     &slackBlockText{Type: "plain_text", Text: actCancelLabel},
					ActionID: actionActCancel,
					Value:    val,
				},
			},
		},
	})
	if err != nil {
		a.log().Error("act confirm blocks marshal failed", "err", err)
		return "error"
	}
	expires := a.now().UTC().Add(actConfirmTTL).Format(time.RFC3339Nano)
	payload, err := metaPayload(ActPayload{
		Action:  action,
		Room:    room,
		Presser: user,
		Expires: expires,
	})
	if err != nil {
		a.log().Error("act confirm metadata marshal failed", "err", err)
		return "error"
	}
	if _, err := a.Slack.PostCard(ctx, th.Channel, th.TS, text, blocks, &Metadata{
		EventType:    actEventType,
		EventPayload: payload,
	}); err != nil {
		a.log().Warn("slack post act confirm failed", "channel", th.Channel, "room", room, "err", err)
		return "error"
	}
	return "proposed"
}

// actPress handles a button press on a thread line's Stop/Retry button or on a stop/retry
// confirmation's Confirm/Cancel buttons.
func (a *App) actPress(ctx context.Context, in interaction) string {
	unlock := a.edits.lock(in.Channel, in.MessageTS)
	defer unlock()

	msg, err := a.Slack.Message(ctx, in.Channel, in.MessageTS)
	if err != nil {
		a.log().Warn("slack message fetch failed", "channel", in.Channel, "ts", in.MessageTS, "err", err)
		return a.pressAnswer(ctx, in, "", actUnreadableLine, "error")
	}
	thread := msg.ThreadTS
	if thread == "" {
		thread = msg.TS
	}
	if msg.User == "" || msg.User != a.BotUserID {
		return a.pressAnswer(ctx, in, thread, actGoneLine, "refused")
	}

	switch in.ActionID {
	case actionActStop, actionActRetry:
		action, room, ok := parseActValue(in.Value)
		if !ok || (in.ActionID == actionActStop && action != actStop) || (in.ActionID == actionActRetry && action != actRetry) {
			return a.pressAnswer(ctx, in, thread, actGoneLine, "refused")
		}
		if !a.Channels[in.Channel].actsOn(action) {
			return a.pressAnswer(ctx, in, thread, actDisabledLine(action), "not_enabled")
		}
		return a.actConfirm(ctx, Thread{Channel: in.Channel, TS: thread}, action, room, in.User)

	case actionActConfirm, actionActCancel:
		p, ok := msg.Metadata.actPayload()
		valAct, valRoom, vok := parseActValue(in.Value)
		if !ok || !vok || p.Action != valAct || p.Room != valRoom || !userRE.MatchString(p.Presser) {
			return a.pressAnswer(ctx, in, thread, actGoneLine, "refused")
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, p.Expires)
		if err != nil {
			return a.pressAnswer(ctx, in, thread, actGoneLine, "refused")
		}
		if in.User != p.Presser {
			return a.pressAnswer(ctx, in, thread, fmt.Sprintf(actOnlyPresserLine, p.Presser), "refused")
		}
		if a.now().After(expiresAt) {
			if err := a.closeActMessage(ctx, in.Channel, in.MessageTS, actExpiredLine); err != nil {
				a.log().Warn("act expiry update failed", "channel", in.Channel, "ts", in.MessageTS, "err", err)
			}
			return a.pressAnswer(ctx, in, thread, actExpiredLine, "refused")
		}
		if !a.Channels[in.Channel].actsOn(p.Action) {
			return a.pressAnswer(ctx, in, thread, actDisabledLine(p.Action), "not_enabled")
		}
		if in.ActionID == actionActCancel {
			line := fmt.Sprintf(actCancelledLine, in.User)
			if err := a.closeActMessage(ctx, in.Channel, in.MessageTS, line); err != nil {
				a.log().Warn("act cancel update failed", "channel", in.Channel, "ts", in.MessageTS, "err", err)
				return a.pressAnswer(ctx, in, thread, actUnreadableLine, "error")
			}
			return "cancelled"
		}

		req := Act{
			Team:      in.Team,
			User:      in.User,
			Channel:   in.Channel,
			MessageTS: in.MessageTS,
			Room:      p.Room,
		}
		var res ActResult
		switch p.Action {
		case actStop:
			res, err = a.Broker.Stop(ctx, req)
		case actRetry:
			res, err = a.Broker.Retry(ctx, req)
		case actAnswer:
			res, err = a.Broker.Answer(ctx, Answer{
				Team:      in.Team,
				User:      in.User,
				Channel:   in.Channel,
				MessageTS: in.MessageTS,
				Room:      p.Room,
				Text:      p.Text,
			})
		}
		if err != nil {
			a.log().Warn("slack act failed", "action", p.Action, "err", err)
			return a.pressAnswer(ctx, in, thread, actUnavailableLine, "error")
		}
		outcome, line := actResultLine(p.Action, res.Result, in.User)
		if err := a.closeActMessage(ctx, in.Channel, in.MessageTS, line); err != nil {
			a.log().Warn("act confirm update failed", "action", p.Action, "channel", in.Channel, "ts", in.MessageTS, "err", err)
			_ = a.pressAnswer(ctx, in, thread, line, outcome)
			return "error"
		}
		return outcome
	}
	return "ignored"
}

func (a *App) closeActMessage(ctx context.Context, channel, ts, line string) error {
	return a.Slack.UpdateProposal(ctx, channel, ts, line, json.RawMessage("[]"), &Metadata{
		EventType:    actClosedEventType,
		EventPayload: json.RawMessage("{}"),
	})
}

func actDisabledLine(action string) string {
	switch action {
	case actRetry:
		return actRetryDisabledLine
	case actAnswer:
		return actAnswerDisabledLine
	default:
		return actStopDisabledLine
	}
}

func actResultLine(action, code, user string) (outcome, line string) {
	switch code {
	case "stopped":
		return "decided", actStoppedLine
	case "retried":
		return "decided", actRetriedLine
	case "answered_restarted":
		return "decided", actAnsweredRestartedLine
	case "answered":
		return "decided", actAnsweredLine
	case "not_allowed":
		return "refused", actAnswerNotAllowedLine
	case "no_question":
		return "refused", actNoQuestionLine
	case "not_maintainer", "not_permitted", "repository_permission":
		switch action {
		case actRetry:
			return "refused", actRetryNotMaintainerLine
		case actAnswer:
			return "refused", actAnswerNotAllowedLine
		default:
			return "refused", actStopNotMaintainerLine
		}
	case "not_running":
		return "refused", actNotRunningLine
	case "not_escalated":
		return "refused", actNotEscalatedLine
	case "not_linked":
		return "refused", fmt.Sprintf(actNotLinkedLine, user)
	case "link_unverified":
		return "refused", actUnverifiedLine
	case "unknown_task":
		return "refused", actUnknownTaskLine
	case "rate_limited":
		return "refused", lineRateLimit
	default:
		return "refused", actUnavailableLine
	}
}
