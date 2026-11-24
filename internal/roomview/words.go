// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	reasonwords "github.com/Smana/maku/internal/factory/reasons"
	"github.com/Smana/maku/internal/summary"
)

// word is the phrase a vocabulary has for v, else v cleaned: a value this broker does not know is
// still untrusted text.
func word(m map[string]string, v string) string {
	if w, ok := m[v]; ok {
		return w
	}
	return clean(v)
}

// stateWord is a coarse state in words. A room that needs the caller reads "needs you" whatever
// its state says, and only then: the broker decides, never the agent.
func stateWord(state string, needsYou bool) string {
	if needsYou {
		return "needs you"
	}
	return word(map[string]string{
		summary.StateWaiting: "waiting for a human", summary.StateRunning: "running", summary.StateQueued: "queued",
		summary.StateDone: "done", summary.StateFailed: "failed", summary.StateStopped: "stopped",
	}, state)
}

var phases = map[string]string{
	"Received": "received", "Triaged": "triaged", "Queued": "queued for a run", "Split": "split into parts",
	"Implementing": "an agent is coding", "Reviewing": "an agent is reviewing", "AwaitingCI": "waiting for CI",
	"AutoMerging": "auto-merging", "Merged": "merged, verifying", "Verifying": "verifying the merge",
	"AwaitingHuman": "waiting for a human", "Escalated": "handed to a maintainer",
	"Done": "done", "NoOp": "nothing to change", "Rejected": "rejected", "Reverted": "merge reverted",
	"Stopped": "stopped", "Closed": "closed",
	// A room's own phases, for a room no task owns.
	"Open": "open", "Active": "an agent is working", "Idle": "idle", "Sealed": "sealed, its log is full",
}

// phaseWords is a Task phase in words.
func phaseWords(phase string) string { return word(phases, phase) }

// reasonWords is a wait reason as a short phrase, at most 40 runes: summary's sentence for it is
// longer than a table cell wants.
func reasonWords(reason string) string {
	if words, ok := reasonwords.Longs(reason)[reason]; ok && words != reason {
		return reasonwords.For(reason).Short
	}
	return clean(reason)
}

// triggerWords is why a run exists. A human trigger reads "human review", as it is a person's
// review request that starts it.
func triggerWords(trigger string) string {
	return word(map[string]string{
		"initial": "first run", "review": "review", "human": "human review", "ci": "CI fix", "retry": "retry", "resume": "resume",
	}, trigger)
}

// outcomeWords is how a run ended; a run that has not ended is running.
func outcomeWords(outcome string) string {
	if outcome == "" {
		return "running"
	}
	return word(map[string]string{
		"succeeded": "succeeded", "failed": "failed", "budget_exhausted": "out of budget",
		"revoked": "revoked", "lost": "lost", "ended": "ended",
	}, outcome)
}

// verdictWords is a reviewer's verdict in words.
// judgeWords is the completion judge's verdict as words.
func judgeWords(verdict string) string {
	return word(map[string]string{"met": "met", "not_yet": "not yet", "impossible": "impossible"}, verdict)
}

func verdictWords(verdict string) string {
	return word(map[string]string{
		"approve": "approved", "approved": "approved", "changes": "changes requested", "none": "no verdict",
	}, verdict)
}
