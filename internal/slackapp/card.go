// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"fmt"
	"strings"
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
