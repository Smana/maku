// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/envelope"
	reasonwords "github.com/Smana/maku/internal/factory/reasons"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

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
		return "the task's budget was spent"
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

// endLine renders the thread's one end line (UX7): outcome, spend against budget, duration from the
// first run's start to the last run's end, and the merge gate's decision. Every event field is clipped
// to the closed vocabulary first.
func endLine(e webhook.Event, sum summary.Summary) string {
	parts := []string{endOutcome(e, sum)}
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
