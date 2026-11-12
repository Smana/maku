// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// types are R19's eleven events, the monitor's verdict and an approval's close, thirteen, in the
// design's order.
var types = []string{"room.task_received", "room.task_triaged", "room.run_started", "room.run_ended",
	"room.waiting_for_human", "room.pr_opened", "room.verdict_posted", "room.merged", "room.reverted", "room.monitor_verdict",
	"room.budget_reached", "room.ended", "room.approval_closed"}

// Types lists the events a subscriber may ask for.
func Types() []string { return slices.Clone(types) }

// limits are the budget limits a run or a task can reach: the meter's, the person's and the
// family's. A revoked run's reason is annotation text, so a "budget-" prefix alone proves nothing.
var limits = []string{"budget-run", "budget-task", "budget-principal", "budget-fleet", "budget-person", "budget-family"}

// Limits lists the values an event's limit may hold.
func Limits() []string { return slices.Clone(limits) }

// States lists the values an event's state may hold: the six coarse states.
func States() []string {
	return []string{summary.StateQueued, summary.StateRunning, summary.StateWaiting, summary.StateDone,
		summary.StateFailed, summary.StateStopped}
}

// classes are the five classes the broker forwards to approvers (bridgeapi's list), repeated here as
// this package's contract: a class outside them is dropped from an event, never clipped to "other".
var classes = []string{"forge.push", "forge.pr", "forge.other", "mcp.write", "shell.high"}

// Classes lists the values an event's class may hold.
func Classes() []string { return slices.Clone(classes) }

// decisions are the four ways an approval closes (store.Approval*).
var decisions = []string{"approved", "denied", "expired", "superseded"}

// Decisions lists the values an event's decision may hold.
func Decisions() []string { return slices.Clone(decisions) }

// Reasons lists the values an event's reason may hold: the wait reasons the summary has prose
// for, the run ends that escalate a task, the broker's approval, and the three escalations the
// factory raises on a room it cannot use. They repeat the factory's strings: this package
// imports nothing of the factory's reconciler, so a new factory reason needs a line here.
func Reasons() []string {
	out := summary.Reasons()
	out = append(out, "approval", "manual", "revoked", "deleted", "principal_unknown",
		"budget-run", "budget-principal", "budget-fleet", "budget-person",
		"foreign_room", "room_log_unreadable", "rooms_crd_outdated")
	slices.Sort(out)
	return slices.Compact(out)
}

// ulidRE is an approval id's shape: the bridgeapi mints it, as a ULID, and nothing else produces
// one, so an id that does not match is dropped rather than carried into a card.
var ulidRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// digestRE is a push bundle's sha256, the one binding a digest-bound approval adds (HR28).
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxProtected bounds the protected paths a wait carries; ProtectedMore counts the rest.
const MaxProtected = 10

// protectedPaths reads a digest-bound approval's action for its protected paths: at most
// MaxProtected, each envelope.ShowablePath (bounded, no control or format character), and how many
// valid ones it left out. Anything else in the action never leaves the broker.
func protectedPaths(action json.RawMessage) ([]string, int) {
	var a struct {
		Protected []string `json:"protected"`
	}
	if json.Unmarshal(action, &a) != nil {
		return nil, 0
	}
	var out []string
	more := 0
	for _, p := range a.Protected {
		if !envelope.ShowablePath(p) {
			continue
		}
		if len(out) == MaxProtected {
			more++
			continue
		}
		out = append(out, p)
	}
	return out, more
}

// Clip is v when kind's vocabulary ("reason", "limit", "state", "verdict" or "decision") holds it, ""
// for "", else "other": no value outside a closed list leaves the broker or reaches a Slack thread.
func Clip(kind, v string) string {
	var list []string
	switch kind {
	case "reason":
		list = Reasons()
	case "limit":
		list = limits
	case "state":
		list = States()
	case "verdict":
		list = envelope.MonitorVerdicts()
	case "decision":
		list = decisions
	}
	if v == "" || slices.Contains(list, v) {
		return v
	}
	return "other"
}

// Tracker turns one room's log into one subscriber's events (R19). It is per subscriber and
// room (M1): what it has sent is that subscriber's, so two subscribers at different cursors never
// share it. The emitter warms it by replaying the room up to the subscriber's cursor.
type Tracker struct {
	seq         int64  // the last seq read: an entry at or below it is a replay and means nothing
	phase       string // the last task phase seen: an event fires on a change only (M2)
	pr          int    // the last pull request number seen, for merged, reverted and verdict_posted
	prSent      bool
	mergedSent  bool // room.merged goes out once: a human merge reaches it through reason, not phase
	ended       bool
	monitorSent bool   // room.monitor_verdict goes out once: a verdict is final once set (MC7)
	origin      string // the last origin the task facts named: every event after it repeats it (RA10)
}

// Events is what ev means to a subscriber of room, whose repository is repo: none, one or a few
// events, each carrying ids, states and codes only. A message is never decoded (M3).
func (t *Tracker) Events(room, repo string, ev envelope.Event) []Event {
	out := t.events(room, repo, ev)
	for i := range out {
		out[i].Origin = t.origin
	}
	return out
}

func (t *Tracker) events(room, repo string, ev envelope.Event) []Event {
	if ev.Seq <= t.seq {
		return nil
	}
	t.seq = ev.Seq
	if ev.Type == envelope.ApprovalRequested {
		e := Event{Type: "room.waiting_for_human", Room: room, Repo: repo, Reason: "approval"}
		var p envelope.ApprovalRequestedPayload
		if json.Unmarshal(ev.Payload, &p) != nil {
			return []Event{e}
		}
		// Each value is dropped when it is outside its closed shape: the App then draws today's line.
		if ulidRE.MatchString(p.ApprovalID) {
			e.ApprovalID = p.ApprovalID
		}
		if slices.Contains(classes, p.Class) {
			e.Class = p.Class
		}
		if envelope.ValidBranch(p.Branch) {
			e.Branch = p.Branch
		}
		if digestRE.MatchString(p.Digest) && e.Class == "forge.push" {
			e.Digest = p.Digest
			e.Protected, e.ProtectedMore = protectedPaths(p.Action)
		}
		if !p.ExpiresAt.IsZero() {
			e.ExpiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return []Event{e}
	}
	if ev.Type == envelope.ApprovalDecided {
		var p envelope.ApprovalDecidedPayload
		if json.Unmarshal(ev.Payload, &p) != nil || !ulidRE.MatchString(p.ApprovalID) || !slices.Contains(decisions, p.Decision) {
			return nil // a close the App cannot act on is not an event
		}
		return []Event{{Type: "room.approval_closed", Room: room, ApprovalID: p.ApprovalID, Decision: p.Decision}}
	}
	if ev.Type != envelope.StateChanged {
		return nil
	}
	var k struct {
		Kind   string `json:"kind"`
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(ev.Payload, &k) != nil {
		return nil
	}
	switch k.Kind {
	case "task":
		var f envelope.TaskFacts
		if json.Unmarshal(ev.Payload, &f) != nil {
			return nil
		}
		return t.task(room, repo, f)
	case "run_phase":
		return runEvents(room, ev.RunID, k.Phase, k.Reason)
	case "verdict_posted":
		return []Event{{Type: "room.verdict_posted", Room: room, PR: t.pr}}
	}
	return nil
}

// task is a task_state entry's events: the first pull request, then the phase's own event on a
// change of phase, the budget its reason names, and the first coarse end.
func (t *Tracker) task(room, repo string, f envelope.TaskFacts) []Event {
	if f.Origin != "" {
		t.origin = f.Origin
	}
	var out []Event
	if f.PR != nil && f.PR.Number > 0 {
		t.pr = f.PR.Number
		if !t.prSent {
			t.prSent = true
			out = append(out, Event{Type: "room.pr_opened", Room: room, PR: t.pr})
		}
	}
	if !t.mergedSent && slices.Contains([]string{"merged", "merged_verified", "merged_unverified"}, f.Reason) {
		// A human merge is Done to Done with reason merged: no Merged phase, so the reason is the fact.
		t.mergedSent = true
		out = append(out, Event{Type: "room.merged", Room: room, PR: t.pr})
	}
	if f.Monitor != "" && t.origin != "" && !t.monitorSent {
		t.monitorSent = true // final once set (MC7): a later rewrite of the facts repeats it
		out = append(out, Event{Type: "room.monitor_verdict", Room: room, Repo: repo, PR: t.pr, Verdict: Clip("verdict", f.Monitor)})
	}
	if f.Phase == t.phase {
		return out // the factory rewrites a phase's facts (budget, run, PR): no new event
	}
	t.phase = f.Phase
	switch f.Phase {
	case "Received":
		out = append(out, Event{Type: "room.task_received", Room: room, Repo: repo})
	case "Triaged":
		out = append(out, Event{Type: "room.task_triaged", Room: room, Repo: repo})
	case "AwaitingHuman", "Escalated":
		out = append(out, Event{Type: "room.waiting_for_human", Room: room, Reason: Clip("reason", f.Reason)})
	case "Merged":
		if !t.mergedSent {
			t.mergedSent = true
			out = append(out, Event{Type: "room.merged", Room: room, PR: t.pr})
		}
	case "Reverted":
		out = append(out, Event{Type: "room.reverted", Room: room, PR: t.pr})
	}
	if strings.HasPrefix(f.Reason, "budget-") {
		out = append(out, Event{Type: "room.budget_reached", Room: room, Limit: Clip("limit", f.Reason)})
	}
	s := summary.Coarse(f.Phase, "", false)
	if !t.ended && (s == summary.StateDone || s == summary.StateFailed || s == summary.StateStopped) {
		t.ended = true
		out = append(out, Event{Type: "room.ended", Room: room, State: s})
	}
	return out
}

// runEvents is a run_phase entry's events: its start, or its end and the budget it reached.
// The terminal phases repeat runwatch.Terminal's: this package stays free of the k8s imports.
func runEvents(room, run, phase, reason string) []Event {
	switch phase {
	case "Running":
		return []Event{{Type: "room.run_started", Room: room, Run: run}}
	case "Succeeded", "Failed", "BudgetExhausted", "Revoked":
		out := []Event{{Type: "room.run_ended", Room: room, Run: run}}
		if strings.HasPrefix(reason, "budget-") {
			out = append(out, Event{Type: "room.budget_reached", Room: room, Limit: Clip("limit", reason)})
		}
		return out
	}
	return nil
}
