// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Smana/maku/internal/webhook"
)

// Interactivity (v3.x Slack push approval §6, D11): Slack's button presses come to the same
// POST /slack/events door, through the same signature check, and are dispatched by content type.
// There is no second public path, no response_url dialled (the App's egress stays slack.com), and
// nothing here decides anything: a press is handed to the broker's decide route as the App's
// principal, and the store keeps the single-decision fence (D7). What this file does own is the
// answer: the clicker alone is told, in one fixed line (card.go), what became of the press, and a
// recorded decision leaves the card inert behind it.

// The two action ids a card carries (§5). No other press is read.
const (
	actionApprove = "factory_approval_approve"
	actionDeny    = "factory_approval_deny"
)

// The button's value (§5): pressVersion|pressClass|<approvalId>|<branch>. The version and the class
// are literals, so a value the App did not write is refused rather than read as one of its own.
const (
	pressVersion = "v1"
	pressClass   = "forge.push"
)

// maxPressPayload bounds one interaction's payload: a card's value is under two hundred bytes, so
// this is Slack's own size for the message it re-sends plus room to spare.
const maxPressPayload = 64 << 10

// pressAnswerBound is how long the door may spend telling the clicker something before it answers
// §6 step 3's already-local outcome. Slack requires the press answered within three seconds, and the
// Slack client's own timeout is ten: without this, one slow chat.postEphemeral would hold the 200
// past Slack's window, Slack would retry a press the App has already read, and the clicker could see
// the line twice. Two seconds leaves the rest of the window for the signature, the parse and the
// counter.
const pressAnswerBound = 2 * time.Second

// approvalIDRE is an approval's id, a ULID: bridgeapi is its only producer. The decide route checks
// the same shape, and neither takes the other's word for it.
var approvalIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// interactionPayload is Slack's interactivity body as §6 names it: a closed struct, so a field Slack
// sends beyond these is never read.
type interactionPayload struct {
	Type                string `json:"type"`
	IsEnterpriseInstall bool   `json:"is_enterprise_install"`
	Team                struct {
		ID string `json:"id"`
	} `json:"team"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
		ActionTS string `json:"action_ts"`
	} `json:"actions"`
	Container struct {
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
}

// interaction is one press the App accepts: §6's shapes are already checked, and the card's own
// values — the approval, the branch it binds, the decision — come from the button's value, never
// from the message's text.
type interaction struct {
	Team, User, Channel, MessageTS string
	ActionID, ActionTS             string
	ApprovalID, Branch, Decision   string
}

// handleInteraction is §6's second body shape of the one door: the body is already verified, so
// this only decides what the press means and what Slack is told — never more than three seconds
// later, which is why the work is queued rather than done here.
func (a *App) handleInteraction(w http.ResponseWriter, r *http.Request, body []byte) {
	ctx := r.Context()
	switch outcome := a.interaction(ctx, body); outcome {
	case "queued":
		// The worker answers for itself once it has run (§6 steps 5-7): the counter's outcome is
		// the decision's, not the door's.
		w.WriteHeader(http.StatusOK)
	case "busy":
		// Full or draining. Slack retries a non-2xx and the interaction's dedup key is forgotten,
		// so the retry is decided rather than read as a duplicate (F-10).
		a.count(ctx, "interactions", outcome)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	default:
		a.count(ctx, "interactions", outcome)
		w.WriteHeader(http.StatusOK)
	}
}

// interaction reads one press from the signed body and hands it to a worker: §6 step 3's shapes,
// step 4's dedup, step 5's enqueue. Its return value is the door's own outcome — ignored,
// not_enabled, duplicate, busy, or queued, whose outcome the worker counts instead.
func (a *App) interaction(ctx context.Context, body []byte) string {
	in, ok := parseInteraction(body)
	// One workspace: a Slack Connect peer's press carries its own team_id, as its events do.
	if !ok || !teamRE.MatchString(in.Team) || in.Team != a.TeamID {
		return "ignored"
	}
	ch, served := a.Channels[in.Channel]
	if !served {
		return "ignored" // a channel the App does not serve: the same silence an unknown event gets
	}
	if !ch.Approvals {
		// Answered locally, with no broker call (§6 step 3): the flag is off, so no card with a
		// button was drawn here and the clicker alone is told where the decision lives.
		a.sayNotEnabled(ctx, in)
		return "not_enabled"
	}
	// §6 step 4: stable across Slack's retries of one press, so a retry answers 200 and does
	// nothing else. UX only, never correctness: the store records one decision per approval (D7).
	key := "i|" + in.ApprovalID + "/" + in.User + "/" + in.ActionID + "/" + in.ActionTS
	if !a.events.add(key) {
		return "duplicate"
	}
	if !a.enqueue(func() {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
		defer cancel()
		a.count(hctx, "interactions", a.decide(hctx, in))
	}) {
		a.events.forget(key)
		return "busy"
	}
	return "queued"
}

// sayNotEnabled tells the clicker alone where the decision lives, inside pressAnswerBound: the one
// Slack call the door makes, and so the one that must not outlast Slack's three seconds. A failed or
// timed-out answer is logged and dropped (§6 step 6): it is never retried, never enqueued as work,
// and never turns the press into a decision.
func (a *App) sayNotEnabled(ctx context.Context, in interaction) {
	actx, cancel := context.WithTimeout(ctx, pressAnswerBound)
	defer cancel()
	if err := a.Slack.PostEphemeral(actx, in.Channel, in.User, "", notEnabledLine(a.RoomsURL)); err != nil {
		a.log().Warn("slack ephemeral post failed", "channel", in.Channel, "err", err)
	}
}

// pageRoot is the rooms page's root, the only URL a refusal sends a person to: a click carries a
// channel and a message ts, and Threads maps room to thread only, so the App cannot name the room's
// own page here. The card's own context block carries it.
func (a *App) pageRoot() string {
	return strings.TrimSuffix(a.RoomsURL, "/")
}

// decide is the press's work, after the 200 (§6 steps 5-7): the one broker call, the answer the
// clicker alone sees, and — when the broker recorded a decision — the card made inert. Its return
// value is the counter's outcome, a bounded word and never a payload field:
//
//	approved, denied   -> decided
//	already_decided    -> duplicate
//	not_supported      -> not_supported
//	any other code     -> refused
//	no call, no answer -> error
//
// The answer is the one step that always runs: whatever the broker said, the person who pressed is
// told something true, and a press that decided nothing leaves the card's buttons where they were —
// the refusal lines themselves say to press again.
func (a *App) decide(ctx context.Context, in interaction) string {
	page := a.pageRoot()
	res, err := a.Broker.Decide(ctx, Decision{Team: in.Team, User: in.User, Channel: in.Channel,
		MessageTS: in.MessageTS, ApprovalID: in.ApprovalID, Branch: in.Branch, Decision: in.Decision})
	if err != nil {
		a.log().Warn("slack decide failed", "err", err)
		a.answer(ctx, in, refusalLine("unavailable", in.User, page)) // false is already this outcome
		return "error"                                               // D7: the store is the fence, so a press that never arrived decided nothing
	}
	outcome, line := outcomeOf(res.Result), answerLine(res.Result, in.User, page)
	told := a.answer(ctx, in, line)
	if outcome == "decided" {
		if err := a.inert(ctx, in, line); err != nil {
			a.log().Warn("card update failed", "approval", in.ApprovalID, "channel", in.Channel, "err", err)
			return "error" // the decision stands; the card still shows live buttons, which the alert names
		}
	}
	if !told {
		return "error"
	}
	return outcome
}

// outcomeOf is a closed code as the counter's bounded word (§6 step 2). Every refusal that is not a
// duplicate or a class the App never drew a card for is `refused`: the counter says whether the press
// landed, and the line says why it did not.
func outcomeOf(code string) string {
	switch code {
	case codeApproved, codeDenied:
		return "decided"
	case "already_decided":
		return "duplicate"
	case "not_supported":
		return "not_supported"
	}
	return "refused"
}

// answer tells the clicker alone, in the card's thread, what became of the press (§6 step 6): one
// chat.postEphemeral addressed to the id Slack signed the click with, always, success included. A
// failed answer is logged and dropped — never retried, never turned into a second press — and its
// false is the worker's `error`. The card and its thread come from the card path's one read
// (cardAt), so a press costs Slack one conversations.replies and no more.
func (a *App) answer(ctx context.Context, in interaction, line string) bool {
	_, thread := a.cardAt(ctx, in.Channel, in.MessageTS)
	if err := a.Slack.PostEphemeral(ctx, in.Channel, in.User, thread, line); err != nil {
		a.log().Warn("slack ephemeral post failed", "channel", in.Channel, "err", err)
		return false
	}
	return true
}

// inert takes the card's buttons off after a recorded decision (§6 step 7): the card becomes §5's
// inert form — the header, the fields and the clicker's own line — and its blocks are replaced
// wholesale, because a block list left as it is keeps the buttons. The fields come from the press,
// which names the approval and the branch it bound and no room: the card it rewrites already carries
// the room link, and the broker's room.approval_closed rewrites it again with the room (closeCard).
// The class is the card's own constant — no card is drawn for another one. Blocks the App could not
// encode fall back to emptied ones: no button may survive.
func (a *App) inert(ctx context.Context, in interaction, line string) error {
	text, blocks := inertCard(webhook.Event{ApprovalID: in.ApprovalID, Branch: in.Branch}, a.RoomsURL, line)
	if blocks == nil {
		blocks = json.RawMessage("[]")
	}
	return a.Slack.Update(ctx, in.Channel, in.MessageTS, text, blocks)
}

// parseInteraction reads Slack's interactivity body — the form already verified — into one press:
// one payload field under 64 KiB, block_actions from a person in a channel, one action whose value
// is a card's. false is §6 step 3's "anything else": 200 ignored, no broker call, no message.
func parseInteraction(body []byte) (interaction, bool) {
	q, err := url.ParseQuery(string(body))
	if err != nil {
		return interaction{}, false
	}
	payloads := q["payload"]
	if len(payloads) != 1 || payloads[0] == "" || len(payloads[0]) > maxPressPayload {
		return interaction{}, false
	}
	var p interactionPayload
	if json.Unmarshal([]byte(payloads[0]), &p) != nil || p.Type != "block_actions" || p.IsEnterpriseInstall ||
		len(p.Actions) != 1 {
		return interaction{}, false
	}
	act := p.Actions[0]
	if !userRE.MatchString(p.User.ID) || !channelRE.MatchString(p.Channel.ID) || !tsRE.MatchString(act.ActionTS) {
		return interaction{}, false
	}
	decision, ok := decisionOf(act.ActionID)
	if !ok {
		return interaction{}, false
	}
	approvalID, branch, ok := parseCardValue(act.Value)
	if !ok {
		return interaction{}, false
	}
	ts := messageTSOf(p)
	if ts == "" {
		return interaction{}, false
	}
	return interaction{Team: p.Team.ID, User: p.User.ID, Channel: p.Channel.ID, MessageTS: ts,
		ActionID: act.ActionID, ActionTS: act.ActionTS,
		ApprovalID: approvalID, Branch: branch, Decision: decision}, true
}

// decisionOf maps a card's two action ids to §1's decision values.
func decisionOf(actionID string) (string, bool) {
	switch actionID {
	case actionApprove:
		return "approved", true
	case actionDeny:
		return "denied", true
	}
	return "", false
}

// messageTSOf names the message the press was made on (§6 step 3, review focus 3). The container's
// message_ts is the message Slack rendered the press from, so it names the card whenever it is
// there; Slack's own payload does not promise it, so an absent container falls back to message.ts
// rather than refusing the click. Two disagreeing timestamps are not averaged or guessed between:
// the one that names the card wins. Neither present is not a press at all.
func messageTSOf(p interactionPayload) string {
	ts := p.Container.MessageTS
	if ts == "" {
		ts = p.Message.TS
	}
	if !tsRE.MatchString(ts) {
		return ""
	}
	return ts
}
