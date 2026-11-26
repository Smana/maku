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
	actStop        = "stop"
	actRetry       = "retry"
	actRetryDeeper = "retry_deeper"
	actAnswer      = "answer"

	actConfirmTTL = 10 * time.Minute

	actStopPrompt         = "<@%s>: stop task #%d? Its run ends now and the pull request stays open."
	actStopPromptNoIssue  = "<@%s>: stop this task? Its run ends now and the pull request stays open."
	actRetryPrompt        = "<@%s>: retry task #%d? It runs again from where it stopped."
	actRetryPromptNoIssue = "<@%s>: retry this task? It runs again from where it stopped."

	actConfirmStopLabel        = "Stop the task"
	actConfirmRetryLabel       = "Retry the task"
	actConfirmRetryDeeperLabel = "Run again deeper"
	actCancelLabel             = "Cancel"
	actLineStopLabel           = "Stop"
	actLineRetryLabel          = "Retry"
	actLineRetryDeeperLabel    = "Run again deeper"

	actOnlyPresserLine   = "Only <@%s> confirms this."
	actExpiredLine       = "This confirmation expired."
	actCancelledLine     = "Cancelled by <@%s>."
	actGoneLine          = "This confirmation is no longer active."
	actUnreadableLine    = "Slack could not be read just now; press the button again."
	actStopDisabledLine  = "Stopping a task from Slack is not turned on in this channel. Apply the `factory/stop` label on the issue."
	actRetryDisabledLine = "Retrying a task from Slack is not turned on in this channel. Comment `/factory retry` on the issue."

	actStoppedLine            = "Stopped."
	actRetriedLine            = "Retried."
	actSteppedUpLine          = "Stepped up and queued."
	actNoDeeperLine           = "This task has no deeper workflow to step up to."
	actTaskCapHoldsLine       = "The task's cap leaves less than a whole run, so a retry would only stop again: raise the cap on a new task."
	actDeeperOverCapLine      = "Stepping up costs more than the cost confirmed for this task. To run the deeper workflow, start the task again with it (New task, or `maku start --workflow <name>`), which confirms its higher cost."
	actStopNotMaintainerLine  = "Only a maintainer of this repository can stop a task."
	actRetryNotMaintainerLine = "Only a maintainer of this repository can retry a task."
	actNotRunningLine         = "This task is not running."
	actNotEscalatedLine       = "This task is not escalated."
	actNotLinkedLine          = "<@%s> is not linked to a GitHub account here. Mention me with `link`, then try again."
	actUnverifiedLine         = "The factory could not check your GitHub account just now; try again in a minute."
	actUnknownTaskLine        = "This task could not be found."
	actUnavailableLine        = "The factory could not be reached just now; try again in a minute."
)

// deeperConfirm formats the confirmation line for a deeper retry, naming the target workflow and
// total ceiling in dollars (WR4).
func deeperConfirm(user string, issue int, workflow string, ceilingUSD float64) string {
	target := "this task"
	if issue > 0 {
		target = fmt.Sprintf("task #%d", issue)
	}
	if ceilingUSD > 0 {
		return fmt.Sprintf("<@%s>: run %s again under %s? It may spend up to $%.2f in all.", user, target, workflow, ceilingUSD)
	}
	return fmt.Sprintf("<@%s>: run %s again under %s?", user, target, workflow)
}

func actValue(action, room string) string {
	return pressVersion + "|act|" + action + "|" + room
}

func parseActValue(v string) (action, room string, ok bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 4 || parts[0] != pressVersion || parts[1] != "act" ||
		(parts[2] != actStop && parts[2] != actRetry && parts[2] != actRetryDeeper && parts[2] != actAnswer) || !roomRE.MatchString(parts[3]) {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// actLineBlocks attaches the Stop button to a waiting-for-human thread line when stop is turned on
// in ch, and the Retry and Run again deeper buttons to an escalated end line when retry is turned
// on in ch.
func actLineBlocks(e webhook.Event, ch Channel, line string, sums ...summary.Summary) json.RawMessage {
	var buttons []slackButton
	switch {
	case e.Type == "room.waiting_for_human" && ch.actsOn(actStop):
		buttons = []slackButton{{
			Type:     "button",
			Text:     &slackBlockText{Type: "plain_text", Text: actLineStopLabel},
			ActionID: actionActStop,
			Value:    actValue(actStop, e.Room),
		}}
	case e.Type == "room.ended" && isEscalatedEnd(e.State) && ch.actsOn(actRetry):
		buttons = []slackButton{{
			Type:     "button",
			Text:     &slackBlockText{Type: "plain_text", Text: actLineRetryLabel},
			ActionID: actionActRetry,
			Value:    actValue(actRetry, e.Room),
		}}
		alreadyStepped := len(sums) > 0 && sums[0].Status.Profile != nil &&
			(sums[0].Status.Profile.Source == "escalation" ||
				(sums[0].Status.Profile.StepUp != nil && (sums[0].Status.Profile.StepUp.Mode == "auto" || sums[0].Status.Profile.StepUp.Mode == "manual")))
		if !alreadyStepped {
			buttons = append(buttons, slackButton{
				Type:     "button",
				Text:     &slackBlockText{Type: "plain_text", Text: actLineRetryDeeperLabel},
				ActionID: actionActRetryDeeper,
				Value:    actValue(actRetryDeeper, e.Room),
			})
		}
	default:
		return nil
	}
	blocks := []slackBlock{
		sectionBlock(line),
		{
			Type:     "actions",
			Elements: buttons,
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
	if (action != actStop && action != actRetry && action != actRetryDeeper) || !roomRE.MatchString(room) || !userRE.MatchString(user) {
		return "ignored"
	}
	sum := a.taskSummary(ctx, room)
	issue := 0
	if sum.Status.Issue != nil {
		issue = sum.Status.Issue.Number
	}

	var text, confirmLabel, style string
	switch action {
	case actStop:
		confirmLabel, style = actConfirmStopLabel, "danger"
		if issue > 0 {
			text = fmt.Sprintf(actStopPrompt, user, issue)
		} else {
			text = fmt.Sprintf(actStopPromptNoIssue, user)
		}
	case actRetryDeeper:
		confirmLabel, style = actConfirmRetryDeeperLabel, "primary"
		wf, ceilUSD := "a deeper workflow", 0.0
		if p := sum.Status.Profile; p != nil {
			if p.StepUp != nil && p.StepUp.To != "" {
				wf = p.StepUp.To
				ceilUSD = float64(p.StepUp.CeilingMicroUSD) / 1e6
			} else if p.StepUpCeilingMicroUSD > 0 {
				ceilUSD = float64(p.StepUpCeilingMicroUSD) / 1e6
			}
		}
		text = deeperConfirm(user, issue, wf, ceilUSD)
	default:
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
	case actionActStop, actionActRetry, actionActRetryDeeper:
		action, room, ok := parseActValue(in.Value)
		if !ok || (in.ActionID == actionActStop && action != actStop) ||
			(in.ActionID == actionActRetry && action != actRetry) ||
			(in.ActionID == actionActRetryDeeper && action != actRetryDeeper) {
			return a.pressAnswer(ctx, in, thread, actGoneLine, "refused")
		}
		gate := action
		if gate == actRetryDeeper {
			gate = actRetry
		}
		if !a.Channels[in.Channel].actsOn(gate) {
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
		gate := p.Action
		if gate == actRetryDeeper {
			gate = actRetry
		}
		if !a.Channels[in.Channel].actsOn(gate) {
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
		case actRetryDeeper:
			req.Deeper = true
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
	case actRetry, actRetryDeeper:
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
	case "stepped_up":
		return "decided", actSteppedUpLine
	case "no_deeper_workflow":
		return "refused", actNoDeeperLine
	case "deeper_over_cap":
		return "refused", actDeeperOverCapLine
	case "task_cap_holds":
		return "refused", actTaskCapHoldsLine
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
		case actRetry, actRetryDeeper:
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
