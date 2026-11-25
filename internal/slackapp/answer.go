// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

const (
	actConfirmAnswerLabel    = "Confirm"
	actAnswerDisabledLine    = "Answering a task's question from Slack is not turned on in this channel. Edit the issue's `## Decisions` section on GitHub."
	actAnsweredRestartedLine = "Answered. The task starts again with your answer."
	actAnsweredLine          = "Answered. A maintainer must restart the task."
	actAnswerNotAllowedLine  = "Only the issue's author or a maintainer of this repository can answer a question."
	actNoQuestionLine        = "This task has no open question."

	maxSlackAnswerBytes = 2 << 10
)

// waitingLine renders a room.waiting_for_human thread line under the needs_you class (G6):
// mentions the one actor who can act (when recorded), states what is asked and the recommended
// action, and names what happens if nothing is done.
func waitingLine(e webhook.Event, sum summary.Summary, th Thread, roomsURL string) string {
	reason := webhook.Clip("reason", e.Reason)
	if reason == "" || reason == "other" {
		reason = sum.Status.ReasonCode
	}
	if q := questionForWait(e, sum); q != "" {
		return mentionPrefix(th.actorFor("needs_author")) + "The scoper asks: " + q + ". Reply to answer. " + needsYouDefault(e, sum)
	}
	url := strings.TrimSuffix(roomsURL, "/") + "/r/" + e.Room
	head := "Needs you (" + webhook.Clip("reason", e.Reason) + ")"
	if gp := gatePart(sum); gp != "" {
		head += " · " + gp
	}
	return mentionPrefix(th.actorFor(reason)) + head + ": answer it on the room page, " + url + ". Nothing posted here reaches the run. " + needsYouDefault(e, sum)
}

// questionForWait returns the broker's clipped question text when the wait is a scoper question, or
// "" when none is available. It reads only the summary's question field, never the webhook's raw
// fields or agent notes.
func questionForWait(e webhook.Event, sum summary.Summary) string {
	if webhook.Clip("reason", e.Reason) != "needs_author" && sum.Status.ReasonCode != "needs_author" && !hasOpenQuestion(sum) {
		return ""
	}
	return answerText(sum.Status.Question)
}

func hasOpenQuestion(sum summary.Summary) bool {
	if strings.TrimSpace(sum.Status.Question) != "" {
		return true
	}
	if sum.Status.Phase == "Escalated" && (sum.Status.ReasonCode == "needs_author" || strings.Contains(sum.Status.Reason, "author")) {
		return true
	}
	return slices.ContainsFunc(sum.NeedsYou, func(n summary.Need) bool {
		return n.Kind == summary.NeedQuestion
	})
}

func oneLineAnswer(raw string) string {
	s := slackDecode(raw)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxSlackAnswerBytes {
		s = strings.ToValidUTF8(s[:maxSlackAnswerBytes], "")
	}
	return s
}

// answerConfirm posts a ten-minute confirmation showing the reply author's exact words with Confirm
// and Cancel buttons. Only the reply's author may press Confirm or Cancel (SI5).
func (a *App) answerConfirm(ctx context.Context, th Thread, room, user, rawReply string) string {
	if !roomRE.MatchString(room) || !userRE.MatchString(user) {
		return "ignored"
	}
	words := oneLineAnswer(rawReply)
	if words == "" {
		return "ignored"
	}
	text := fmt.Sprintf("I understood: answer %q", answerText(words))
	val := actValue(actAnswer, room)
	blocks, err := json.Marshal([]slackBlock{
		sectionBlock(text),
		{
			Type: "actions",
			Elements: []slackButton{
				{
					Type:     "button",
					Text:     &slackBlockText{Type: "plain_text", Text: actConfirmAnswerLabel},
					ActionID: actionActConfirm,
					Value:    val,
					Style:    "primary",
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
		a.log().Error("answer confirm blocks marshal failed", "err", err)
		return "error"
	}
	expires := a.now().UTC().Add(actConfirmTTL).Format(time.RFC3339Nano)
	payload, err := metaPayload(ActPayload{
		Action:  actAnswer,
		Room:    room,
		Presser: user,
		Text:    words,
		Expires: expires,
	})
	if err != nil {
		a.log().Error("answer confirm metadata marshal failed", "err", err)
		return "error"
	}
	if _, err := a.Slack.PostCard(ctx, th.Channel, th.TS, text, blocks, &Metadata{
		EventType:    actEventType,
		EventPayload: payload,
	}); err != nil {
		a.log().Warn("slack post answer confirm failed", "channel", th.Channel, "room", room, "err", err)
		return "error"
	}
	return "proposed"
}
