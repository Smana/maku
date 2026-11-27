// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

const (
	// BreakerOK allows scheduled and manual runs normally.
	BreakerOK = "ok"
	// BreakerHalfOpen allows a single trial run after an effective prompt+skill SHA change.
	BreakerHalfOpen = "half_open"
	// BreakerPaused halts scheduled ticks until prompt+skill SHA changes or a maintainer resumes.
	BreakerPaused = "paused"

	// BreakerReasonConsecutiveClosed trips when consecutive unmerged/stale/rejected PRs or proposals reach MaxConsecutiveClosed.
	BreakerReasonConsecutiveClosed = "consecutive_closed"
	// BreakerReasonLowMergeRate trips when rolling merge rate over at least MinSamples drops below MinMergeRate.
	BreakerReasonLowMergeRate = "low_merge_rate"
	// BreakerReasonConsecutiveFailures trips when consecutive escalated runs reach MaxConsecutiveFailures.
	BreakerReasonConsecutiveFailures = "consecutive_failures"

	// OutcomeMerged records a merged routine PR.
	OutcomeMerged = "merged"
	// OutcomeClosedUnmerged records a routine PR closed unmerged without factory/stale.
	OutcomeClosedUnmerged = "closed_unmerged"
	// OutcomeStale records a routine PR or proposal closed due to factory/stale (counts as rejection, Q14).
	OutcomeStale = "stale"
	// OutcomeReverted records a merged routine PR that was reverted (excluding drills).
	OutcomeReverted = "reverted"
	// OutcomeEscalated records a routine run ending in PhaseEscalated.
	OutcomeEscalated = "escalated"
	// OutcomeNoOp records a routine run ending in PhaseNoOp (strictly neutral).
	OutcomeNoOp = "no_op"
	// OutcomeProposalAccepted records a propose-mode factory/proposed issue accepted (published/readied/completed).
	OutcomeProposalAccepted = "proposal_accepted"
	// OutcomeProposalRejected records a propose-mode factory/proposed issue closed unaccepted.
	OutcomeProposalRejected = "proposal_rejected"

	// MaxBreakerHistory caps the number of rolling outcomes persisted per routine.
	MaxBreakerHistory = 100
)

// BreakerVerdict is the pure evaluation of a routine's rolling breaker entries.
type BreakerVerdict struct {
	Tripped           bool
	Reason            string
	MergeRate         float64
	HasMergeRate      bool
	ConsecutiveClosed int
	ConsecutiveFailed int
	RollingMerged     int
	RollingClosed     int
}

// BreakerOutcome is one routine run or proposal outcome fed into Breaker.Record.
type BreakerOutcome struct {
	ID        string
	Outcome   string
	PromptSHA string
	At        time.Time
}

// RecordResult reports the persisted breaker state after Breaker.Record.
type RecordResult struct {
	State        RoutineState
	NewlyTripped bool
	Reason       string
	MergeRate    float64
	HasMergeRate bool
}

// TaskSummary holds pure rolled-up Task metrics shared by Repository.status.routines[] and the API.
type TaskSummary struct {
	OpenCount     int
	LastRunAt     *metav1.Time
	LastOutcome   string
	RollingMerged int
	RollingClosed int
	RollingNoOp   int
}

// Breaker evaluates and persists the per-(repository, routine) rolling yield circuit breaker.
type Breaker struct {
	Store *StateStore
	Now   func() time.Time
	Log   *slog.Logger
}

func (b *Breaker) now() time.Time {
	if b != nil && b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeBreakerSpec(spec config.ResolvedBreaker) config.ResolvedBreaker {
	if spec.Window <= 0 {
		spec.Window = config.DefaultBreakerWindow
		spec.MinMergeRate = config.DefaultBreakerMinMergeRate
	}
	spec.MinSamples = cmp.Or(spec.MinSamples, config.DefaultBreakerMinSamples)
	spec.MaxConsecutiveClosed = cmp.Or(spec.MaxConsecutiveClosed, config.DefaultBreakerMaxConsecutiveClosed)
	spec.MaxConsecutiveFailures = cmp.Or(spec.MaxConsecutiveFailures, config.DefaultBreakerMaxConsecutiveFailures)
	return spec
}

func isMergedOutcome(o string) bool {
	return o == OutcomeMerged || o == OutcomeProposalAccepted
}

func isClosedOutcome(o string) bool {
	return o == OutcomeClosedUnmerged || o == OutcomeStale || o == OutcomeReverted || o == OutcomeProposalRejected
}

func isValidBreakerOutcome(o string) bool {
	return isMergedOutcome(o) || isClosedOutcome(o) || o == OutcomeEscalated || o == OutcomeNoOp
}

// Evaluate computes the rolling yield breaker verdict over chronological entries (oldest to newest).
// OutcomeNoOp is strictly neutral and ignored.
func Evaluate(entries []BreakerEntry, spec config.ResolvedBreaker) BreakerVerdict {
	spec = normalizeBreakerSpec(spec)
	var v BreakerVerdict

	closedStreakOpen := true
	failStreakOpen := true
	for i := len(entries) - 1; i >= 0; i-- {
		o := entries[i].Outcome
		if o == OutcomeNoOp || !isValidBreakerOutcome(o) {
			continue
		}
		if closedStreakOpen {
			if isClosedOutcome(o) {
				v.ConsecutiveClosed++
			} else {
				closedStreakOpen = false
			}
		}
		if failStreakOpen {
			if o == OutcomeEscalated {
				v.ConsecutiveFailed++
			} else {
				failStreakOpen = false
			}
		}
		if v.RollingMerged+v.RollingClosed < spec.Window {
			switch {
			case isMergedOutcome(o):
				v.RollingMerged++
			case isClosedOutcome(o):
				v.RollingClosed++
			}
		}
	}

	total := v.RollingMerged + v.RollingClosed
	if total > 0 {
		v.HasMergeRate = true
		v.MergeRate = float64(v.RollingMerged) / float64(total)
	}

	switch {
	case v.ConsecutiveClosed >= spec.MaxConsecutiveClosed:
		v.Tripped = true
		v.Reason = BreakerReasonConsecutiveClosed
	case v.ConsecutiveFailed >= spec.MaxConsecutiveFailures:
		v.Tripped = true
		v.Reason = BreakerReasonConsecutiveFailures
	case total >= spec.MinSamples && v.MergeRate < spec.MinMergeRate:
		v.Tripped = true
		v.Reason = BreakerReasonLowMergeRate
	}
	return v
}

// Record updates (repo, routine)'s persisted yield breaker state for out.
// OutcomeNoOp is strictly neutral (reads state with zero ConfigMap writes).
func (b *Breaker) Record(ctx context.Context, repo, routine string, spec config.ResolvedBreaker, out BreakerOutcome) (RecordResult, error) {
	if b == nil || b.Store == nil {
		return RecordResult{}, errors.New("upkeep breaker: state store is required")
	}
	if !isValidBreakerOutcome(out.Outcome) {
		return RecordResult{}, fmt.Errorf("upkeep breaker: unknown outcome %q", out.Outcome)
	}
	if out.Outcome == OutcomeNoOp {
		st, err := b.Store.Get(ctx, repo, routine)
		if err != nil {
			return RecordResult{}, err
		}
		v := Evaluate(st.BreakerHistory, spec)
		return RecordResult{State: st, MergeRate: v.MergeRate, HasMergeRate: v.HasMergeRate}, nil
	}

	var res RecordResult
	err := b.Store.Mutate(ctx, repo, routine, func(st *RoutineState, now time.Time) bool {
		at := out.At.UTC()
		if at.IsZero() {
			at = now
		}

		changed := false
		if out.ID != "" {
			for i := range st.BreakerHistory {
				if st.BreakerHistory[i].ID == out.ID {
					if st.BreakerHistory[i].Outcome == out.Outcome {
						v := Evaluate(st.BreakerHistory, spec)
						res = RecordResult{State: st.clone(), MergeRate: v.MergeRate, HasMergeRate: v.HasMergeRate}
						return false
					}
					st.BreakerHistory[i].Outcome = out.Outcome
					st.BreakerHistory[i].At = at
					changed = true
					break
				}
			}
		}
		if !changed && st.BreakerResetAt != nil && at.Before(*st.BreakerResetAt) {
			v := Evaluate(st.BreakerHistory, spec)
			res = RecordResult{State: st.clone(), MergeRate: v.MergeRate, HasMergeRate: v.HasMergeRate}
			return false
		}
		curState := cmp.Or(st.BreakerState, BreakerOK)
		if !changed && len(st.BreakerHistory) >= MaxBreakerHistory && at.Before(st.BreakerHistory[0].At) && (curState == BreakerOK || !isMergedOutcome(out.Outcome)) {
			v := Evaluate(st.BreakerHistory, spec)
			res = RecordResult{State: st.clone(), MergeRate: v.MergeRate, HasMergeRate: v.HasMergeRate}
			return false
		}
		if !changed {
			st.BreakerHistory = append(st.BreakerHistory, BreakerEntry{
				ID:      out.ID,
				Outcome: out.Outcome,
				At:      at,
			})
		}
		slices.SortStableFunc(st.BreakerHistory, func(a, c BreakerEntry) int {
			return a.At.Compare(c.At)
		})
		if len(st.BreakerHistory) > MaxBreakerHistory {
			st.BreakerHistory = slices.Clone(st.BreakerHistory[len(st.BreakerHistory)-MaxBreakerHistory:])
		}

		v := Evaluate(st.BreakerHistory, spec)
		newlyTripped := false
		tripReason := ""

		switch curState {
		case BreakerHalfOpen:
			if isMergedOutcome(out.Outcome) {
				resetAt := now
				st.BreakerState = BreakerOK
				st.BreakerReason = ""
				st.TrippedPromptSHA = ""
				st.TrippedAt = nil
				st.BreakerResetAt = &resetAt
				st.BreakerHistory = []BreakerEntry{{ID: out.ID, Outcome: out.Outcome, At: at}}
				v = Evaluate(st.BreakerHistory, spec)
			} else {
				tripReason = v.Reason
				if tripReason == "" {
					if out.Outcome == OutcomeEscalated {
						tripReason = BreakerReasonConsecutiveFailures
					} else {
						tripReason = BreakerReasonConsecutiveClosed
					}
				}
				trippedAt := now
				st.BreakerState = BreakerPaused
				st.BreakerReason = tripReason
				if out.PromptSHA != "" {
					st.TrippedPromptSHA = out.PromptSHA
				}
				st.TrippedAt = &trippedAt
				newlyTripped = true
			}
		case BreakerPaused:
			if isMergedOutcome(out.Outcome) {
				resetAt := now
				st.BreakerState = BreakerOK
				st.BreakerReason = ""
				st.TrippedPromptSHA = ""
				st.TrippedAt = nil
				st.BreakerResetAt = &resetAt
				st.BreakerHistory = []BreakerEntry{{ID: out.ID, Outcome: out.Outcome, At: at}}
				v = Evaluate(st.BreakerHistory, spec)
			} else if out.PromptSHA != "" {
				// Failed manual trial while paused updates TrippedPromptSHA so the next cron tick
				// does not auto-reset to half_open on the already-tested SHA.
				st.TrippedPromptSHA = out.PromptSHA
			}
		default: // BreakerOK
			st.BreakerState = BreakerOK
			if v.Tripped {
				trippedAt := now
				st.BreakerState = BreakerPaused
				st.BreakerReason = v.Reason
				if out.PromptSHA != "" {
					st.TrippedPromptSHA = out.PromptSHA
				}
				st.TrippedAt = &trippedAt
				newlyTripped = true
				tripReason = v.Reason
			}
		}

		res = RecordResult{
			State:        st.clone(),
			NewlyTripped: newlyTripped,
			Reason:       tripReason,
			MergeRate:    v.MergeRate,
			HasMergeRate: v.HasMergeRate,
		}
		return true
	})
	return res, err
}

// CheckPromptReset transitions a breaker-paused routine to half_open when its effective prompt+skill
// SHA differs from TrippedPromptSHA. Manually paused routines (st.Paused == true) never auto-reset.
func (b *Breaker) CheckPromptReset(ctx context.Context, repo, routine, currentEffectiveSHA string) (RoutineState, bool, error) {
	if b == nil || b.Store == nil {
		return RoutineState{}, false, errors.New("upkeep breaker: state store is required")
	}
	if currentEffectiveSHA == "" {
		st, err := b.Store.Get(ctx, repo, routine)
		return st, false, err
	}
	var out RoutineState
	var transitioned bool
	err := b.Store.Mutate(ctx, repo, routine, func(st *RoutineState, _ time.Time) bool {
		if st.Paused || st.BreakerState != BreakerPaused || st.TrippedPromptSHA == "" || currentEffectiveSHA == st.TrippedPromptSHA {
			out = st.clone()
			return false
		}
		st.BreakerState = BreakerHalfOpen
		transitioned = true
		out = st.clone()
		return true
	})
	return out, transitioned, err
}

// Reconstruct rebuilds rolling breaker state on cold start (when BreakerState == "" && BreakerResetAt == nil)
// from cluster Task CRs and GitHub PRs/issues carrying factory/routine:<routine>.
func (b *Breaker) Reconstruct(ctx context.Context, repo, routine string, spec config.ResolvedBreaker, tasks []v1alpha1.Task, pulls []forge.LabelledPull, issues []forge.Issue, currentPromptSHA string) (RoutineState, error) {
	if b == nil || b.Store == nil {
		return RoutineState{}, errors.New("upkeep breaker: state store is required")
	}
	cur, err := b.Store.Get(ctx, repo, routine)
	if err != nil {
		return RoutineState{}, err
	}
	if cur.BreakerState != "" || cur.BreakerResetAt != nil {
		return cur, nil
	}

	issueByNum := make(map[int]forge.Issue, len(issues))
	for _, iss := range issues {
		issueByNum[iss.Number] = iss
	}

	var entries []BreakerEntry
	seenPRs := make(map[int]bool)
	seenPRAt := make(map[int]time.Time)
	seenIssues := make(map[int]bool)

	for i := range tasks {
		t := &tasks[i]
		if !taskMatchesRoutine(t, repo, routine) {
			continue
		}
		at := taskEndTime(t).UTC()
		if t.Status.PullRequest != nil && t.Status.PullRequest.Number > 0 {
			prNum := t.Status.PullRequest.Number
			seenPRs[prNum] = true
			if !at.IsZero() {
				if prev, ok := seenPRAt[prNum]; !ok || at.Before(prev) {
					seenPRAt[prNum] = at
				}
			}
		}
		if t.Spec.Issue > 0 {
			seenIssues[t.Spec.Issue] = true
		}
		switch t.Status.Phase {
		case v1alpha1.PhaseDone:
			if t.Status.Reason == "proposal_filed" || t.Status.Reason == "proposal_ready" {
				if t.Spec.Issue > 0 {
					if iss, ok := issueByNum[t.Spec.Issue]; ok {
						if out, ok := classifyProposalIssueOutcome(t, iss); ok {
							entries = append(entries, BreakerEntry{ID: fmt.Sprintf("proposal:%d", iss.Number), Outcome: out, At: cmpTime(iss.ClosedAt, iss.LastEditedAt, at, iss.CreatedAt).UTC()})
						}
					}
				}
				continue
			}
			if t.Status.Outcome == "failed" {
				entries = append(entries, BreakerEntry{ID: t.Name, Outcome: OutcomeClosedUnmerged, At: at})
			} else {
				entries = append(entries, BreakerEntry{ID: t.Name, Outcome: OutcomeMerged, At: at})
			}
		case v1alpha1.PhaseClosed:
			out := OutcomeClosedUnmerged
			if t.Status.Reason == "stale" {
				out = OutcomeStale
			}
			entries = append(entries, BreakerEntry{ID: t.Name, Outcome: out, At: at})
		case v1alpha1.PhaseReverted:
			if t.Annotations[v1alpha1.AnnotationDrill] == "true" {
				continue
			}
			entries = append(entries, BreakerEntry{ID: t.Name, Outcome: OutcomeReverted, At: at})
		case v1alpha1.PhaseEscalated:
			entries = append(entries, BreakerEntry{ID: fmt.Sprintf("%s:esc:%d", t.Name, t.Status.Retries), Outcome: OutcomeEscalated, At: at})
		}
	}

	// PullsWithLabel arrives newest-created first; anchor preserves relative chronological order
	// for CLOSED pulls that lack MergedAt. Lower anchor on seenPRs too so older GC'd CLOSED PRs
	// never sort after newer Task-tracked PRs.
	anchor := b.now()
	for _, p := range pulls {
		if strings.EqualFold(p.State, "OPEN") {
			continue
		}
		if seenPRs[p.Number] {
			if !p.MergedAt.IsZero() && p.MergedAt.UTC().Before(anchor) {
				anchor = p.MergedAt.UTC()
			}
			if tAt := seenPRAt[p.Number]; !tAt.IsZero() && tAt.Before(anchor) {
				anchor = tAt
			}
			continue
		}
		switch strings.ToUpper(p.State) {
		case "MERGED":
			at := p.MergedAt.UTC()
			if at.IsZero() {
				at = anchor.Add(-time.Millisecond)
			}
			if at.Before(anchor) {
				anchor = at
			}
			entries = append(entries, BreakerEntry{ID: fmt.Sprintf("pr:%d", p.Number), Outcome: OutcomeMerged, At: at})
		case "CLOSED":
			at := anchor.Add(-time.Millisecond)
			anchor = at
			out := OutcomeClosedUnmerged
			if slices.Contains(p.Labels, "factory/stale") {
				out = OutcomeStale
			}
			entries = append(entries, BreakerEntry{ID: fmt.Sprintf("pr:%d", p.Number), Outcome: out, At: at})
		}
	}

	for _, iss := range issues {
		if seenIssues[iss.Number] || !slices.Contains(iss.Labels, "factory/proposed") {
			continue
		}
		if out, ok := classifyProposalIssueOutcome(nil, iss); ok {
			entries = append(entries, BreakerEntry{
				ID:      fmt.Sprintf("proposal:%d", iss.Number),
				Outcome: out,
				At:      cmpTime(iss.ClosedAt, iss.LastEditedAt, iss.CreatedAt, b.now()).UTC(),
			})
		}
	}

	if len(entries) == 0 {
		cur.BreakerState = BreakerOK
		return cur, nil
	}

	slices.SortStableFunc(entries, func(a, c BreakerEntry) int {
		if d := a.At.Compare(c.At); d != 0 {
			return d
		}
		return strings.Compare(a.ID, c.ID)
	})
	if len(entries) > MaxBreakerHistory {
		entries = slices.Clone(entries[len(entries)-MaxBreakerHistory:])
	}

	var out RoutineState
	err = b.Store.Mutate(ctx, repo, routine, func(st *RoutineState, now time.Time) bool {
		if st.BreakerState != "" || st.BreakerResetAt != nil {
			out = st.clone()
			return false
		}
		st.BreakerHistory = entries
		v := Evaluate(entries, spec)
		if v.Tripped {
			t := now
			st.BreakerState = BreakerPaused
			st.BreakerReason = v.Reason
			st.TrippedPromptSHA = currentPromptSHA
			st.TrippedAt = &t
		} else {
			st.BreakerState = BreakerOK
		}
		out = st.clone()
		return true
	})
	return out, err
}

func classifyProposalIssueOutcome(t *v1alpha1.Task, iss forge.Issue) (string, bool) {
	if (t != nil && t.Status.Published != nil) || slices.Contains(iss.Labels, "factory/publish") || strings.EqualFold(iss.StateReason, "completed") {
		return OutcomeProposalAccepted, true
	}
	if !strings.EqualFold(iss.State, "CLOSED") {
		return "", false
	}
	if slices.Contains(iss.Labels, "factory/stale") {
		return OutcomeStale, true
	}
	return OutcomeProposalRejected, true
}

// DefaultPromptEffectiveSHA returns EffectivePromptSHA(sha256(defaultPrompt), "") for fallback display/reconstruction.
func DefaultPromptEffectiveSHA(defaultPrompt string) string {
	if defaultPrompt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(defaultPrompt))
	return EffectivePromptSHA(hex.EncodeToString(sum[:]), "")
}

// TaskOutcomeLabel returns the bounded status/API outcome string for a routine Task.
func TaskOutcomeLabel(t *v1alpha1.Task) string {
	switch t.Status.Phase {
	case v1alpha1.PhaseDone:
		switch {
		case t.Status.Outcome == "failed":
			return "failed"
		case t.Status.Outcome != "":
			return t.Status.Outcome
		case t.Status.PullRequest != nil && t.Status.PullRequest.HumanCommits:
			return "merged_human"
		default:
			return "accepted"
		}
	case v1alpha1.PhaseNoOp:
		return "no_op"
	case v1alpha1.PhaseClosed:
		return "closed"
	case v1alpha1.PhaseStopped:
		return "stopped"
	case v1alpha1.PhaseRejected:
		return "rejected"
	case v1alpha1.PhaseReverted:
		return "reverted"
	default:
		return t.Status.Outcome
	}
}

// SummarizeTasks computes OpenCount, LastRunAt (truncated to time.Second for statuswriter
// idempotency), LastOutcome, and rolling window counts over a routine's Tasks.
func SummarizeTasks(tasks []*v1alpha1.Task, window int) TaskSummary {
	window = cmp.Or(window, config.DefaultBreakerWindow)
	var sum TaskSummary
	if len(tasks) == 0 {
		return sum
	}

	byCreated := slices.Clone(tasks)
	slices.SortStableFunc(byCreated, func(a, b *v1alpha1.Task) int {
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	newest := byCreated[0]
	runTS := newest.CreationTimestamp.Time
	if runTS.IsZero() {
		runTS = taskEndTime(newest)
	}
	if !runTS.IsZero() {
		mt := metav1.NewTime(runTS.Truncate(time.Second))
		sum.LastRunAt = &mt
	}
	for _, t := range byCreated {
		if !v1alpha1.TerminalPhase(t.Status.Phase) {
			sum.OpenCount++
			continue
		}
		if sum.LastOutcome == "" {
			if out := TaskOutcomeLabel(t); out != "" {
				sum.LastOutcome = out
			}
		}
	}
	if sum.LastOutcome == "" {
		sum.LastOutcome = TaskOutcomeLabel(newest)
	}

	byEnded := slices.Clone(tasks)
	slices.SortStableFunc(byEnded, func(a, b *v1alpha1.Task) int {
		if c := taskEndTime(b).Compare(taskEndTime(a)); c != 0 {
			return c
		}
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})

	endedCount := 0
	for _, t := range byEnded {
		if !v1alpha1.TerminalPhase(t.Status.Phase) {
			continue
		}
		endedCount++
		if endedCount <= window && t.Status.Phase == v1alpha1.PhaseNoOp {
			sum.RollingNoOp++
		}
		if sum.RollingMerged+sum.RollingClosed >= window {
			continue
		}
		switch t.Status.Phase {
		case v1alpha1.PhaseDone:
			if t.Status.Outcome != "failed" {
				sum.RollingMerged++
			}
		case v1alpha1.PhaseClosed, v1alpha1.PhaseReverted:
			sum.RollingClosed++
		}
	}
	return sum
}
