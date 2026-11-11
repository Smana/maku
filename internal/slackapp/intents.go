// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/intent"
	"github.com/Smana/maku/internal/summary"
)

// Tier 1 of a filed thread's reading (natural-language intents): a reply tier 0's phrase table does
// not know goes to the broker's intent_classify, which maps it to one of nine labels. In shadow
// (channels.<id>.intents: shadow) the label is counted and logged and the thread keeps today's
// unknown line: nothing here posts, confirms or executes anything, and no reply is ever an action
// (invariant 8). The App never guesses who is linked: the broker resolves the author and refuses
// anyone else, and a refusal, a cap or an outage is today's line, counted.

// The outcomes maku_slack_intents_total{outcome} takes at this stage: shadowed for a label read, refused
// for a classification the broker would not or could not make, not_enabled for a broker without the tool.
const (
	intentShadowed   = "shadowed"
	intentRefused    = "refused"
	intentNotEnabled = "not_enabled"
	intentTier       = "1"
)

// slackDecode undoes the three entities Slack puts in a message's text, so the classifier is shown
// what the person typed, not the transport's spelling of it.
func slackDecode(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// tierOne classifies one unknown reply in shadow, after today's line is posted. It is a no-op in a
// channel whose intents switch is off, and for a thread whose room the marker does not name. The phase
// is the kind of the one room_summary status line, read here and used for nothing else; an unreadable
// room is the phase `unknown`.
func (a *App) tierOne(ctx context.Context, ev slackEvent, ch Channel, room string) {
	if !ch.intentsServed() || !roomRE.MatchString(room) {
		return
	}
	phase := "unknown"
	if sum, err := a.Broker.RoomSummary(ctx, room); err == nil {
		if kind := summary.StatusOf(sum, a.now()).Kind; intent.ValidPhase(kind) {
			phase = kind
		}
	}
	reply := slackDecode(ev.Text)
	res, err := a.Broker.IntentClassify(ctx, IntentRequest{Team: a.TeamID, User: ev.User, Channel: ev.Channel,
		Reply: reply, Phase: phase, Collect: ch.CollectReplies})
	if err != nil {
		outcome, why := intentRefused, "error"
		var refused Refused
		switch {
		case errors.Is(err, ErrUnknownTool):
			outcome, why = intentNotEnabled, "tool_unknown"
		case errors.As(err, &refused):
			why = shown(string(refused))
		}
		a.intentCount(ctx, string(intent.Unknown), outcome)
		a.log().Info("slack intent not classified", "channel", ev.Channel, "reason", why)
		return
	}
	label := intent.Intent(res.Intent)
	if !label.Valid() {
		// A broker that names a tenth label is not one this App can count: it is unknown, and the label
		// is never put in a series.
		label = intent.Unknown
	}
	a.intentCount(ctx, string(label), intentShadowed)
	sum := sha256.Sum256([]byte(reply))
	a.log().Info("slack intent classified", "channel", ev.Channel, "thread", ev.ThreadTS, "intent", string(label),
		"confidence", res.Confidence, "phase", phase, "sha256", hex.EncodeToString(sum[:]), "length", utf8.RuneCountInString(reply))
}

// intentCount counts one tier 1 reading.
func (a *App) intentCount(ctx context.Context, label, outcome string) {
	if a.Metrics != nil {
		a.Metrics.SlackIntent(ctx, intentTier, label, outcome)
	}
}
