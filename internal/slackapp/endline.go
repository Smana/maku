// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/narrate"
	reasonwords "github.com/Smana/maku/internal/factory/reasons"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

// notificationClass maps a Slack webhook event onto one of the three notification classes (G6):
//   - needs_you: waits for human action (room.waiting_for_human, escalated room.ended)
//   - done: terminal end line (non-escalated room.ended, room.merged, room.reverted)
//   - changed: state change in the thread with no mention (everything else)
func notificationClass(e webhook.Event) narrate.Class {
	switch e.Type {
	case "room.waiting_for_human":
		return narrate.ClassNeedsYou
	case "room.ended":
		if isEscalatedEnd(e.State) {
			return narrate.ClassNeedsYou
		}
		return narrate.ClassDone
	case "room.merged", "room.reverted":
		return narrate.ClassDone
	default:
		return narrate.ClassChanged
	}
}

// mentionPrefix returns "<@user> " when actor is a valid Slack user id, or "" otherwise.
func mentionPrefix(actor string) string {
	if !userRE.MatchString(actor) {
		return ""
	}
	return "<@" + actor + "> "
}

// needsYouDefault names what happens if nothing is done for a needs_you notification (G6).
func needsYouDefault(e webhook.Event, sum summary.Summary) string {
	reason := webhook.Clip("reason", e.Reason)
	if reason == "" || reason == "other" {
		reason = sum.Status.ReasonCode
	}
	switch {
	case e.Type == "room.ended" && isEscalatedEnd(e.State):
		return "If nothing is done, it stops in 24 hours."
	case reason == "needs_author" || reason == "split_proposed" || hasOpenQuestion(sum):
		return "If nothing is done, it stops in 24 hours."
	case reason == "approval" || e.ApprovalID != "":
		return "If nothing is done, it stays parked until the deadline."
	default:
		return "If nothing is done, it stays parked."
	}
}

// doneEndLine formats a terminal end line under the done class (G6): mentions the one actor once
// when th.DoneMentioned is false, and omits the mention on later updates.
func doneEndLine(e webhook.Event, sum summary.Summary, th Thread) string {
	line := endLine(e, sum)
	if th.DoneMentioned {
		return line
	}
	return mentionPrefix(th.actorFor("")) + line
}

// escalatedEndLine formats an escalated room.ended notification under the needs_you class (G6):
// mentions the one actor (when recorded), states what stopped, names one recommended action when
// no button is attached, and says what happens if nothing is done.
func escalatedEndLine(e webhook.Event, sum summary.Summary, th Thread, ch Channel, roomsURL string) string {
	line := endLine(e, sum)
	if !ch.actsOn(actRetry) {
		url := strings.TrimSuffix(roomsURL, "/") + "/r/" + e.Room
		line += ". Comment `/factory retry` on the issue or open " + url + "."
	} else {
		line += "."
	}
	line += " " + needsYouDefault(e, sum)
	return mentionPrefix(th.actorFor("")) + line
}

// slackDollars formats micro-USD as $X.XX, half-cent rounded like humanapi's task view.
func slackDollars(micro int64) string {
	if micro < 0 {
		micro = 0
	}
	cents := (micro + 5000) / 10000
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// slackPercent is used/limit as a rounded whole percentage.
func slackPercent(used, limit int64) int64 {
	if limit <= 0 {
		return 0
	}
	return (100*used + limit/2) / limit
}

// gateHoldReasons maps a closed TaskFacts wait/hold reason code onto the phrase Slack shows on a
// merge hold. Unknown codes are omitted so free text never reaches a thread line.
func gatePolicyName(p *summary.PolicyView) string {
	if p == nil {
		return ""
	}
	for _, name := range []string{p.Name, p.Class} {
		if name != "" && name != "review" && name != "gate" && envelope.ValidClassName(name) {
			return name
		}
	}
	return ""
}

func gateHoldReason(code, policy string) string {
	switch code {
	case "shadow_would_arm":
		if policy != "" {
			return "policy " + policy + " is in shadow here"
		}
		return "it would auto-merge, and the merge gate is in shadow"
	case "class_paused":
		if policy != "" {
			return "policy " + policy + " is paused"
		}
		return "the merge policy is paused"
	}
	if _, known := reasonwords.Longs(code)[code]; !known {
		return ""
	}
	return reasonwords.For(code).Short
}

// gatePart builds the one Slack gate sentence from the summary's closed policy name, merge mode,
// validated approver login and closed reason code (UX7, UX11), or "" before the gate has decided.
func gatePart(sum summary.Summary) string {
	p := sum.Status.Policy
	if p == nil || sum.Status.PR == nil {
		return ""
	}
	switch p.MergedHow {
	case "class":
		if name := gatePolicyName(p); name != "" {
			return "merged automatically by policy " + name
		}
		return "merged automatically"
	case "approved":
		who := "a maintainer's"
		if envelope.ValidLogin(sum.Status.PR.ApprovedBy) {
			who = "@" + sum.Status.PR.ApprovedBy + "'s"
		}
		return "merged on " + who + " approval"
	}
	if sum.Status.Phase != "AwaitingHuman" {
		return ""
	}
	if why := gateHoldReason(sum.Status.ReasonCode, gatePolicyName(p)); why != "" {
		return "waits for a human: " + why
	}
	return ""
}

// stepUpLine renders the one thread line posted when Maku steps a task up (WR4), with no mention
// and only closed/validated fields.
func stepUpLine(e webhook.Event) string {
	from, to := e.From, e.To
	if !envelope.ValidClassName(from) {
		from = ""
	}
	if !envelope.ValidClassName(to) {
		to = ""
	}
	var line string
	switch {
	case from != "" && to != "":
		line = fmt.Sprintf("Stepped up from %s to %s", from, to)
	case to != "":
		line = fmt.Sprintf("Stepped up to %s", to)
	default:
		line = "Stepped up to a deeper workflow"
	}
	if r := webhook.Clip("reason", e.Reason); r != "" && r != "other" {
		line += " (" + stepUpReasonWords(r) + ")"
	}
	if e.CeilingMicroUSD > 0 {
		line += " · new ceiling " + slackDollars(e.CeilingMicroUSD)
	}
	return line + "."
}

func stepUpReasonWords(reason string) string {
	switch reason {
	case "stalled":
		return "a run that committed nothing"
	case "budget-run":
		return "the run's budget was spent"
	case "budget-task":
		return "too little of the task's budget was left for a run"
	case "not_met":
		return "acceptance checks were not met"
	case "review_rounds_exhausted":
		return "review rounds were exhausted"
	case "ci_red":
		return "required checks stayed red"
	default:
		return reason
	}
}

// splitSummaryLine renders the thread's one summary line for a parent split task, e.g.
// "3 parts: 1 merged · 1 working · 1 waiting for #12", or "" when the room is not a split parent.
// summary.SplitSummary reads only closed state words and positive issue numbers, never free text.
func splitSummaryLine(sum summary.Summary) string {
	if sum.Flow == nil || sum.Flow.Split == nil {
		return ""
	}
	return summary.SplitSummary(sum.Flow.Split)
}

// endLine renders the thread's one end line (UX7): outcome, split parts summary when present,
// spend against budget, duration from the first run's start to the last run's end, and the merge
// gate's decision. Every event field is clipped to the closed vocabulary first.
func endLine(e webhook.Event, sum summary.Summary) string {
	parts := []string{endOutcome(e, sum)}
	if spLine := splitSummaryLine(sum); spLine != "" {
		parts = append(parts, spLine)
	}
	if sp := sum.Status.Spend; sp != nil && (sp.UsedMicroUSD > 0 || sp.LimitMicroUSD > 0) {
		s := slackDollars(sp.UsedMicroUSD)
		if sp.LimitMicroUSD > 0 {
			s += " of " + slackDollars(sp.LimitMicroUSD)
		}
		if !sum.Status.SpendFinal {
			s += " so far"
		}
		parts = append(parts, s)
	}
	if len(sum.Runs) > 0 {
		start, end := sum.Runs[0].StartedAt, sum.Runs[len(sum.Runs)-1].EndedAt
		if !start.IsZero() && !end.IsZero() && !end.Before(start) {
			parts = append(parts, summary.Span(end.Sub(start)))
		}
	}
	if gp := gatePart(sum); gp != "" {
		parts = append(parts, gp)
	}
	return strings.Join(parts, " · ")
}

func endOutcome(e webhook.Event, sum summary.Summary) string {
	pr := e.PR
	if pr <= 0 && sum.Status.PR != nil {
		pr = sum.Status.PR.Number
	}
	state := webhook.Clip("state", e.State)
	reason := webhook.Clip("reason", e.Reason)
	switch {
	case e.Type == "room.reverted" || sum.Status.Phase == "Reverted":
		if pr > 0 {
			return fmt.Sprintf("Reverted #%d", pr)
		}
		return "Reverted"
	case e.Type == "room.merged" || sum.Status.Phase == "Merged":
		if pr > 0 {
			return fmt.Sprintf("Merged #%d", pr)
		}
		return "Ended: done"
	case e.Type == "room.ended" && state != summary.StateDone && state != summary.StateStopped && state != summary.StateFailed:
		// An unrecognised event state is clipped to "other", never masked by an older summary phase.
		return "Ended: " + state
	case state == summary.StateDone || sum.Status.Phase == "Done":
		return "Ended: done"
	case state == summary.StateStopped || sum.Status.Phase == "Stopped":
		why := reason
		if why == "" || why == "other" {
			why = webhook.Clip("reason", sum.Status.ReasonCode)
		}
		switch why { // #644: a cancel, a discard and a deleted issue say which
		case "cancelled", "discarded":
			return "Ended: " + why
		case "issue_deleted":
			return "Ended: issue deleted"
		}
		return "Ended: stopped"
	case state == summary.StateFailed || sum.Status.Phase == "Escalated":
		why := reason
		if (why == "" || why == "other") && sum.Status.ReasonCode != "" {
			why = webhook.Clip("reason", sum.Status.ReasonCode)
		}
		switch {
		case strings.HasPrefix(why, "budget-") || why == "resume_budget" || why == "waiting_daily_budget":
			return "Ended: escalated (out of budget)"
		case why != "" && why != "other":
			return "Ended: escalated (" + strings.ReplaceAll(strings.ReplaceAll(why, "_", " "), "-", " ") + ")"
		default:
			return "Ended: escalated"
		}
	}
	return "Ended: " + state
}
