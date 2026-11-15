// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"fmt"
	"strings"

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

// gatePart maps summary.PolicyView.Decision onto the one Slack gate sentence (UX7, UX11), or "" when
// the gate has not decided yet.
func gatePart(decision string) string {
	d := strings.TrimSpace(decision)
	if d == "" {
		return ""
	}
	if d == "merged by the merger App: the class's conditions held" {
		return "merged automatically"
	}
	if strings.HasPrefix(d, "Merged automatically") || strings.HasPrefix(d, "merged automatically") {
		return "m" + answerText(d[1:])
	}
	if rest, ok := strings.CutPrefix(d, "merged by the merger App on "); ok {
		return "merged on " + answerText(rest)
	}
	if rest, ok := strings.CutPrefix(d, "merged on "); ok {
		return "merged on " + answerText(rest)
	}
	const waitPrefix = "waits for a human:"
	if len(d) >= len(waitPrefix) && strings.EqualFold(d[:len(waitPrefix)], waitPrefix) {
		return "waits for a human: " + answerText(strings.TrimSpace(d[len(waitPrefix):]))
	}
	return "waits for a human: " + answerText(d)
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
	if sum.Status.Policy != nil {
		if gp := gatePart(sum.Status.Policy.Decision); gp != "" {
			parts = append(parts, gp)
		}
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
