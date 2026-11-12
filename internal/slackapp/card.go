// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/webhook"
)

// The lines a press is answered with (§6, the design's one refusal table plus the two success ones).
// They are a closed vocabulary: one fixed sentence per code, whatever the room, the repository, the
// branch or the person pressing. No line names a repository, a branch, a GitHub login, an approver,
// or how many approvers a room has — the anti-oracle rule of the App's older refusals, applied to
// this path. `repository_permission` says "this room's repository", which is the room the card is
// about and nothing more; `not_linked` names the clicker alone, by the id Slack signed.
//
// A code is the broker's; the App never invents one and never shows one. Slack's own escapes do not
// apply here: every value is an id or a fixed string.
const (
	lineApproved    = "Decided: the push goes out."
	lineDenied      = "Decided: nothing is pushed."
	lineNotLinked   = "Not decided: <@%s> is not linked to a GitHub account here. Mention me with `link`, then press the button again."
	lineUnverified  = "Not decided: the factory could not check your GitHub account just now. Press the button again in a minute."
	lineNotPermit   = "Not decided: only this room's approvers decide here. Ask an owner to add you as an approver."
	lineRepoPerm    = "Not decided: your GitHub account cannot write to this room's repository."
	lineFourEyes    = "Not decided: you prompted this run, and four-eyes asks another approver to decide it."
	lineDecided     = "Already decided; nothing changed."
	lineExpired     = "Not decided: the deadline had passed."
	lineBinding     = "Not decided: this card no longer matches the request. Open the room page: %s"
	lineNotSupport  = "Not decided from Slack: this approval is on the room page only: %s"
	lineNotFound    = "Not decided: this request is no longer in the room's log."
	lineSealed      = "Not decided: this room is sealed."
	lineRateLimit   = "Too many presses just now; wait a minute."
	lineUnavailable = "Not decided: the factory could not be reached just now. Press the button again."
)

// The two decisions, as the broker's closed codes name them: a recorded decision is the only outcome
// that takes the card's buttons off, and the only one with a success line.
const (
	codeApproved = "approved"
	codeDenied   = "denied"
)

// lineNotEnabled is the one line a press in a channel without the approvals flag gets (§6 step 3):
// no card was drawn there, so the decision stays on the room page. It is not a broker code — no
// press there ever reaches the broker.
const lineNotEnabled = "Not decided from Slack: approvals are not turned on in this channel. Decide it on the room page: %s"

// refusalLines is §6's table: the broker's closed code as the one sentence the clicker reads. A code
// outside it is unavailable's line (refusalLine), so a broker that grows a code says something true
// and fixed rather than its own words.
var refusalLines = map[string]string{
	"not_linked":            lineNotLinked,
	"link_unverified":       lineUnverified,
	"not_permitted":         lineNotPermit,
	"repository_permission": lineRepoPerm,
	"four_eyes":             lineFourEyes,
	"already_decided":       lineDecided,
	"approval_expired":      lineExpired,
	"binding_mismatch":      lineBinding,
	"not_supported":         lineNotSupport,
	"not_found":             lineNotFound,
	"sealed":                lineSealed,
	"rate_limited":          lineRateLimit,
	"unavailable":           lineUnavailable,
}

// refusalLine is the line one closed code says, or unavailable's for anything else. Two codes carry
// a value: not_linked formats the id Slack signed the click with, and the two that send a person back
// to the page format pageURL — the rooms page's root, never a room's own page, because a click names
// a channel and a message ts and Threads maps room to thread only (the card's context block already
// carries the room's exact URL).
func refusalLine(code, user, pageURL string) string {
	line, ok := refusalLines[code]
	if !ok {
		return lineUnavailable
	}
	switch code {
	case "not_linked":
		return fmt.Sprintf(line, user)
	case "binding_mismatch", "not_supported":
		return fmt.Sprintf(line, pageURL)
	}
	return line
}

// answerLine is what the clicker sees: the success line when a decision was recorded, the code's fixed
// refusal line otherwise. One function, so the ephemeral and the card cannot say different things.
func answerLine(code, user, pageURL string) string {
	switch code {
	case codeApproved:
		return lineApproved
	case codeDenied:
		return lineDenied
	}
	return refusalLine(code, user, pageURL)
}

// notEnabledLine is lineNotEnabled with the one URL a press in a closed channel is sent to.
func notEnabledLine(roomsURL string) string {
	return fmt.Sprintf(lineNotEnabled, strings.TrimSuffix(roomsURL, "/"))
}

// The card §5 draws for a forge.push wait: fixed prose, and the event's ids and closed values. The
// two sentences are the room page's own approval help, so a person reads the same thing on both
// surfaces. Nothing here is room text, a reason, a GitHub login or another person's name (D5, D12).
const (
	cardTitle    = "Approval needed: push to the forge"
	cardApprove  = "Approve"
	cardDeny     = "Deny"
	cardOpenRoom = "Open the room"
	cardProse    = "If you approve: the push goes out and the run continues.\n" +
		"If you deny: nothing is pushed; the agent is told no and none of it runs."
	// cardDeadline is Slack's own date token: the client renders it in the reader's timezone, so the
	// App holds neither a clock nor a zone in the message.
	cardDeadline = "<!date^%d^{date_short_pretty} at {time}|by %d>"
	// cardMrkdwn is Block Kit's text type for everything but a header or a button label.
	cardMrkdwn = "mrkdwn"
)

// cardBlock is one Block Kit block in the three shapes §5 uses: a header and a section carry Text, a
// fields section carries Fields, and an actions or context block carries Elements.
type cardBlock struct {
	Type     string     `json:"type"`
	Text     *cardText  `json:"text,omitempty"`
	Fields   []cardText `json:"fields,omitempty"`
	Elements []any      `json:"elements,omitempty"`
}

// cardText is Block Kit's text object: mrkdwn everywhere but a header's and a button's labels.
type cardText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// cardButton is a card's button: the two deciding ones carry an action id, the card's value and a
// style; the room's carries only a URL, so it presses nothing.
type cardButton struct {
	Type     string   `json:"type"`
	Text     cardText `json:"text"`
	ActionID string   `json:"action_id,omitempty"`
	Value    string   `json:"value,omitempty"`
	Style    string   `json:"style,omitempty"`
	URL      string   `json:"url,omitempty"`
}

// cardValue is a deciding button's value (§5): v1|forge.push|<approvalId>|<branch>, under two thousand
// characters by construction — a ULID is 26 and a branch at most 107. The version and the class are
// literals, so a value the App did not write is refused by parseCardValue rather than read as one of
// its own.
func cardValue(approvalID, branch string) string {
	return pressVersion + "|" + pressClass + "|" + approvalID + "|" + branch
}

// parseCardValue reads a card button's value back: v1|forge.push|<approvalId>|<branch>. The branch is
// the AgentRun's own pattern (envelope.ValidBranch), never a copy of it.
func parseCardValue(v string) (approvalID, branch string, ok bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 4 || parts[0] != pressVersion || parts[1] != pressClass ||
		!approvalIDRE.MatchString(parts[2]) || !envelope.ValidBranch(parts[3]) {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// approvalCard is §5's card for one wait: the text Slack shows where blocks do not render, and the
// blocks themselves, in the design's order. now decides only what is drawn, never what is decided:
// past the deadline the card is drawn inert — the prose and the room link, no button — because the
// store would refuse the press (review focus 5, D10). A nil text and no blocks is the one case the
// caller answers with today's line: the App could not encode what it drew.
func approvalCard(e webhook.Event, roomsURL string, now time.Time) (string, json.RawMessage) {
	room := strings.TrimSuffix(roomsURL, "/") + "/r/" + e.Room
	fields := []cardText{
		{Type: cardMrkdwn, Text: "*Class*\n" + pressClass},
		{Type: cardMrkdwn, Text: "*Branch*\n" + slackText(e.Branch)},
		{Type: cardMrkdwn, Text: "*Approval*\n" + e.ApprovalID},
	}
	if e.Digest != "" { // HR28: a protected-path push is approved by its bundle's digest
		fields = append(fields, cardText{Type: cardMrkdwn, Text: "*Digest*\n" + slackText(shortDigest(e.Digest))},
			cardText{Type: cardMrkdwn, Text: "*Protected paths*\n" + protectedLine(e.Protected, e.ProtectedMore)})
	}
	// The deadline comes from the event's own RFC 3339 field: no clock and no timezone of the App's
	// reach the message. An event without one (the broker always sets it) is drawn without the row
	// rather than with an invented value, and is live, since nothing says it is past.
	deadline, dated := cardDeadlineOf(e)
	expired := dated && now.After(time.Unix(deadline, 0))
	if dated {
		fields = append(fields, cardText{Type: cardMrkdwn, Text: fmt.Sprintf("*Deadline*\n"+cardDeadline, deadline, deadline)})
	}
	buttons := []any{}
	if !expired {
		buttons = append(buttons,
			cardButton{Type: "button", Text: cardText{Type: "plain_text", Text: cardApprove},
				ActionID: actionApprove, Value: cardValue(e.ApprovalID, e.Branch), Style: "primary"},
			cardButton{Type: "button", Text: cardText{Type: "plain_text", Text: cardDeny},
				ActionID: actionDeny, Value: cardValue(e.ApprovalID, e.Branch), Style: "danger"})
	}
	buttons = append(buttons, cardButton{Type: "button", Text: cardText{Type: "plain_text", Text: cardOpenRoom}, URL: room})
	blocks := []cardBlock{
		{Type: "header", Text: &cardText{Type: "plain_text", Text: cardTitle}},
		{Type: "section", Fields: fields},
		{Type: "section", Text: &cardText{Type: cardMrkdwn, Text: cardProse}},
		{Type: "actions", Elements: buttons},
		{Type: "context", Elements: []any{cardText{Type: cardMrkdwn, Text: room}}},
	}
	raw, err := cardJSON(blocks)
	if err != nil {
		return "", nil // nothing the App builds can fail to encode; the caller keeps today's line
	}
	return cardTitle, raw
}

// shortDigest is a digest as the card shows it: its first 12 hex, as git abbreviates a commit.
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// maxCardPath bounds one protected path as the card shows it, in runes, before it is escaped.
const maxCardPath = 120

// maxCardField is Slack's limit on a section field's text, in characters.
const maxCardField = 2000

// protectedBudget is what the paths may take of a field, once escaped: the field's limit less its
// label and room for ", and N more".
const protectedBudget = maxCardField - len("*Protected paths*\n") - len(", and 99999 more")

// protectedLine is the card's protected paths: each bounded, then escaped, then kept while the
// escaped line fits protectedBudget; the ones it leaves out join the wait's own "and N more". A
// path is the one text the card shows that the factory did not write.
func protectedLine(paths []string, more int) string {
	var line string
	for i, p := range paths {
		if r := []rune(p); len(r) > maxCardPath {
			p = string(r[:maxCardPath]) + "…"
		}
		item := "`" + strings.ReplaceAll(slackText(p), "`", "'") + "`"
		if line != "" {
			item = ", " + item
		}
		if len([]rune(line))+len([]rune(item)) > protectedBudget {
			more += len(paths) - i
			break
		}
		line += item
	}
	if more > 0 {
		line += fmt.Sprintf(", and %d more", more)
	}
	if line == "" {
		line = "none named"
	}
	return line
}

// The four lines a closed card carries (§5's teardown, and the design's lifecycle). They are fixed
// and name nobody: §4's room.approval_closed payload is {approvalId, decision} — no actor — so a
// card that said "approved by @x" would name the wrong person whenever the room page decided it.
const (
	lineCardApproved   = "Approved."
	lineCardDenied     = "Denied."
	lineCardExpired    = "The deadline passed."
	lineCardSuperseded = "No longer open."
)

// closeCardLine is the one line a closed card carries for one of §4's four decisions. A value
// outside the closed set — the tracker drops those, so this is a barrier — reads as the line that
// says the card is closed without claiming how.
func closeCardLine(decision string) string {
	switch decision {
	case codeApproved:
		return lineCardApproved
	case codeDenied:
		return lineCardDenied
	case "expired":
		return lineCardExpired
	}
	return lineCardSuperseded
}

// inertCard is §5's card after it closes: the header, the fields it still knows, and one fixed
// outcome line, with no actions block at all — a block list left as it is keeps its buttons. It
// draws from the event (or the card's own tag) and never from a clock: what closed the approval is
// the store's, and the card only reports it (D10). The Deadline row is gone with the buttons: the
// close's payload carries no deadline, and the row is what a press decides against.
//
// text is the line and not the title: Slack shows text wherever blocks do not render (a
// notification, an old client), and the outcome is what that place should say.
func inertCard(e webhook.Event, roomsURL, line string) (text string, blocks json.RawMessage) {
	fields := []cardText{{Type: cardMrkdwn, Text: "*Class*\n" + pressClass}}
	if envelope.ValidBranch(e.Branch) {
		fields = append(fields, cardText{Type: cardMrkdwn, Text: "*Branch*\n" + slackText(e.Branch)})
	}
	if approvalIDRE.MatchString(e.ApprovalID) {
		fields = append(fields, cardText{Type: cardMrkdwn, Text: "*Approval*\n" + e.ApprovalID})
	}
	out := []cardBlock{
		{Type: "header", Text: &cardText{Type: "plain_text", Text: cardTitle}},
		{Type: "section", Fields: fields},
		{Type: "section", Text: &cardText{Type: cardMrkdwn, Text: line}},
	}
	// The room link lives in the context block — the one button an inert card would have carried —
	// and only the close knows the room: a press names a channel and a message ts and nothing that
	// resolves a room (Threads maps room to thread only). `r/` with no room is not a URL, so it is
	// left out rather than drawn broken; the close that follows the press draws it.
	if roomRE.MatchString(e.Room) {
		out = append(out, cardBlock{Type: "context", Elements: []any{
			cardText{Type: cardMrkdwn, Text: strings.TrimSuffix(roomsURL, "/") + "/r/" + e.Room}}})
	}
	raw, err := cardJSON(out)
	if err != nil {
		return line, nil // nothing the App builds can fail to encode; the caller empties the blocks
	}
	return line, raw
}

// closeCard makes a closed approval's card inert (§4's room.approval_closed, §5's teardown): the
// room's thread is read once, the card that names the approval is found by its tag, and it is
// rewritten in place — twice for a decision taken from Slack, once by the click and once by this,
// and idempotent either way.
//
// It is best effort and never fatal: the card is a view and the store is the fence (D10), so a close
// the App cannot draw must not make the broker retry it into a loop (three given-up deliveries
// disable the subscriber). One request has one outcome — card_closed, ignored, or error — and this
// returns it for the caller to count.
//
// §4's payload carries no class, no branch and no deadline, so the fields the wait drew are taken
// back from the card's own tag, which the App wrote there from the wait's event: an inert card must
// not lose what the live one said. A missing card is ignored rather than posted: the thread already
// told the wait's story.
func (a *App) closeCard(ctx context.Context, th Thread, e webhook.Event) string {
	if !approvalIDRE.MatchString(e.ApprovalID) {
		return "ignored"
	}
	msgs, err := a.Slack.Thread(ctx, th.Channel, th.TS)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", th.Channel, "err", err)
		return "error"
	}
	card, ok := a.cardIn(msgs, e.ApprovalID)
	if !ok {
		return "ignored"
	}
	if p, ok := card.Metadata.approvalPayload(); ok && e.Branch == "" {
		e.Branch = p.Branch
	}
	text, blocks := inertCard(e, a.RoomsURL, closeCardLine(e.Decision))
	if blocks == nil {
		blocks = json.RawMessage("[]") // the buttons come off whatever else failed
	}
	if err := a.Slack.Update(ctx, th.Channel, card.TS, text, blocks); err != nil {
		// The line the press path's own teardown logs, and what SlackCardUpdatesFailing reads.
		a.log().Warn("card update failed", "approval", e.ApprovalID, "channel", th.Channel, "err", err)
		return "error"
	}
	return "card_closed"
}

// cardJSON encodes a card's blocks as Slack reads them. HTML escaping is off because the only `<` in
// them is Slack's own date token, which the design writes verbatim: an escaped \u003c decodes to the
// same text but reads as noise in the wire bytes the L6 proof and the tests quote.
func cardJSON(v any) (json.RawMessage, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("slackapp: the card's blocks: %w", err)
	}
	return json.RawMessage(strings.TrimSuffix(b.String(), "\n")), nil
}

// cardDeadlineOf is the event's deadline as a unix second, which is what Slack's date token takes.
func cardDeadlineOf(e webhook.Event) (unix int64, ok bool) {
	at, err := time.Parse(time.RFC3339, e.ExpiresAt)
	if err != nil {
		return 0, false
	}
	return at.Unix(), true
}

// cardWanted is §5's four conditions, the thread's included: the room was filed from Slack (th), the
// channel draws cards and serves the event's repository, and the event carries the three values a
// card is drawn from — an approval id, this class, and a branch inside the AgentRun's shape. Every
// other wait keeps today's line, so a forge.pr approval in the same thread reads as it always did.
func (a *App) cardWanted(e webhook.Event, th Thread) bool {
	ch, served := a.Channels[th.Channel]
	return served && ch.Approvals && servesRepo(ch.Repositories, e.Repo) &&
		e.Class == pressClass && approvalIDRE.MatchString(e.ApprovalID) && envelope.ValidBranch(e.Branch)
}

// postOrUpdateCard draws the wait as §5's card, once per approval: the room's thread is read for a
// card that names this approval, and that one is rewritten in place, so a webhook redelivery — or a
// restart that lost the in-memory delivery set — cannot double the card. A thread too long to read
// posts anyway and counts too_long, its one honest downgrade. The outcome is the counter's word:
// card_posted, card_updated or too_long; error when the thread or Slack could not be reached, which
// the broker retries.
func (a *App) postOrUpdateCard(ctx context.Context, th Thread, e webhook.Event) string {
	text, blocks := approvalCard(e, a.RoomsURL, a.now())
	if blocks == nil {
		a.log().Error("the card could not be encoded, the thread keeps today's line", "room", e.Room)
		return a.postWaitLine(ctx, th, e)
	}
	payload, perr := metaPayload(ApprovalPayload{ApprovalID: e.ApprovalID, Class: e.Class, Branch: e.Branch})
	if perr != nil {
		// The tag is what finds the card again, so a card posted without it would be doubled by the
		// next delivery. It is posted all the same, and this names the consequence.
		a.log().Error("card metadata: a redelivery would draw a second card", "room", e.Room, "err", perr)
		payload = json.RawMessage(`{}`)
	}
	meta := &Metadata{EventType: approvalEventType, EventPayload: payload}
	msgs, err := a.Slack.Thread(ctx, th.Channel, th.TS)
	tooLong := errors.Is(err, ErrThreadTooLong)
	if err != nil && !tooLong {
		a.log().Warn("slack thread fetch failed", "channel", th.Channel, "err", err)
		return "error"
	}
	if card, ok := a.cardIn(msgs, e.ApprovalID); ok {
		if err := a.Slack.Update(ctx, th.Channel, card.TS, text, blocks); err != nil {
			// The same line the decide path's own teardown logs, and what its alert reads.
			a.log().Warn("card update failed", "approval", e.ApprovalID, "channel", th.Channel, "err", err)
			return "error"
		}
		return "card_updated"
	}
	if _, err := a.Slack.PostCard(ctx, th.Channel, th.TS, text, blocks, meta); err != nil {
		a.log().Warn("slack post failed", "room", e.Room, "err", err)
		return "error"
	}
	if tooLong {
		return "too_long"
	}
	return "card_posted"
}

// postWaitLine is what the wait would have been without a card: the thread's own line. It is the one
// caller of approvalCard's impossible failure, so a card the App cannot encode still tells the thread.
func (a *App) postWaitLine(ctx context.Context, th Thread, e webhook.Event) string {
	line, ok := a.line(e)
	if !ok {
		return "ignored"
	}
	if _, err := a.Slack.Post(ctx, th.Channel, th.TS, line, nil); err != nil {
		a.log().Warn("slack post failed", "room", e.Room, "err", err)
		return "error"
	}
	return "posted"
}

// cardIn is the thread's card for one approval, the newest first: the App's own message — posted by
// its bot user — tagged factory_approval, whose payload names the approval. A message that fails any
// of that is not a card, which is the branch a foreign message already takes for a draft: nothing
// else in the thread can be rewritten in its place.
func (a *App) cardIn(msgs []Message, approvalID string) (Message, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if p, ok := m.Metadata.approvalPayload(); ok && p.ApprovalID == approvalID {
			return m, true
		}
	}
	return Message{}, false
}

// cardAt is the card a press names and the thread it lives in, from one read: the interaction payload
// carries a channel and a message ts and nothing that names the thread, so the App reads the pressed
// message once — here — and every press-path caller takes both from it. A read that fails leaves the
// thread empty rather than losing the answer.
func (a *App) cardAt(ctx context.Context, channel, ts string) (Message, string) {
	m, err := a.Slack.Message(ctx, channel, ts)
	if err != nil {
		a.log().Info("slack message read for the answer's thread failed", "channel", channel, "err", err)
		return Message{}, ""
	}
	if m.ThreadTS != "" {
		return m, m.ThreadTS
	}
	return m, m.TS
}
