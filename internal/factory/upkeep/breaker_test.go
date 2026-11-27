// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

func testBreakerStore(t *testing.T, now *time.Time) (*StateStore, *countingStateClient) {
	t.Helper()
	cc := &countingStateClient{Client: fake.NewClientBuilder().WithScheme(stateScheme()).Build()}
	st := &StateStore{
		Client:    cc,
		Reader:    cc,
		Namespace: "maku",
		Now:       func() time.Time { return *now },
	}
	return st, cc
}

func defaultBreakerSpec() config.ResolvedBreaker {
	return config.ResolvedBreaker{
		Window:                 10,
		MinSamples:             5,
		MinMergeRate:           0.25,
		MaxConsecutiveClosed:   3,
		MaxConsecutiveFailures: 3,
	}
}

func TestBreakerTripsOnConsecutiveClosedIncludingStaleAndProposal(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("a", 64)

	// NoOp before/between closes is strictly neutral.
	res, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "noop-1", Outcome: OutcomeNoOp, PromptSHA: sha1, At: now})
	if err != nil || res.NewlyTripped || res.State.BreakerState == BreakerPaused {
		t.Fatalf("NoOp should be neutral: res=%+v err=%v", res, err)
	}

	outcomes := []struct {
		id, out string
	}{
		{"t1", OutcomeClosedUnmerged},
		{"t2", OutcomeStale}, // stale-closed counts as rejection (Q14)
		{"p3", OutcomeProposalRejected},
	}
	for i, tc := range outcomes {
		now = now.Add(time.Minute)
		res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: tc.id, Outcome: tc.out, PromptSHA: sha1, At: now})
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && res.NewlyTripped {
			t.Fatalf("step %d tripped prematurely: %+v", i, res)
		}
	}
	if !res.NewlyTripped || res.Reason != BreakerReasonConsecutiveClosed ||
		res.State.BreakerState != BreakerPaused || res.State.TrippedPromptSHA != sha1 {
		t.Fatalf("expected consecutive_closed trip, got %+v", res)
	}
}

func TestBreakerTripsOnLowMergeRateAndNoOpNeutral(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("b", 64)

	// closed, closed, merged, closed, no_op, stale -> consecClosed=2, 1/5 = 0.20 < 0.25!
	seq := []string{
		OutcomeClosedUnmerged,
		OutcomeClosedUnmerged,
		OutcomeMerged,
		OutcomeClosedUnmerged,
		OutcomeNoOp,
		OutcomeStale,
	}
	var res RecordResult
	var err error
	for i, out := range seq {
		now = now.Add(time.Minute)
		res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{
			ID:        fmt.Sprintf("r-%d", i),
			Outcome:   out,
			PromptSHA: sha1,
			At:        now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !res.NewlyTripped || res.Reason != BreakerReasonLowMergeRate || res.State.BreakerState != BreakerPaused {
		t.Fatalf("expected low_merge_rate trip, got %+v", res)
	}
}

func TestBreakerTripsOnConsecutiveFailures(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("c", 64)

	var res RecordResult
	var err error
	for i := range 3 {
		now = now.Add(time.Minute)
		res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{
			ID:        fmt.Sprintf("esc-%d", i),
			Outcome:   OutcomeEscalated,
			PromptSHA: sha1,
			At:        now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !res.NewlyTripped || res.Reason != BreakerReasonConsecutiveFailures || res.State.BreakerState != BreakerPaused {
		t.Fatalf("expected consecutive_failures trip, got %+v", res)
	}
}

func TestBreakerPromptSHAResetAndHalfOpenLifecycle(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("1", 64)
	sha2 := strings.Repeat("2", 64)
	sha3 := strings.Repeat("3", 64)

	// Trip on sha1.
	for i := range 3 {
		now = now.Add(time.Minute)
		if _, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: fmt.Sprintf("c-%d", i), Outcome: OutcomeClosedUnmerged, PromptSHA: sha1, At: now}); err != nil {
			t.Fatal(err)
		}
	}

	// Unrelated commit to main (same EffectiveSHA = sha1): stays paused.
	gotSt, moved, err := b.CheckPromptReset(t.Context(), "acme/app", "dead-code", sha1)
	if err != nil || moved || gotSt.BreakerState != BreakerPaused {
		t.Fatalf("same SHA should stay paused: st=%+v moved=%t err=%v", gotSt, moved, err)
	}

	// Prompt or skill SHA changed -> half_open.
	gotSt, moved, err = b.CheckPromptReset(t.Context(), "acme/app", "dead-code", sha2)
	if err != nil || !moved || gotSt.BreakerState != BreakerHalfOpen {
		t.Fatalf("new SHA should move to half_open: st=%+v moved=%t err=%v", gotSt, moved, err)
	}

	// Trial NoOp stays half_open.
	now = now.Add(time.Minute)
	res, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "trial-noop", Outcome: OutcomeNoOp, PromptSHA: sha2, At: now})
	if err != nil || res.NewlyTripped || res.State.BreakerState != BreakerHalfOpen {
		t.Fatalf("trial NoOp must keep half_open: %+v err=%v", res, err)
	}

	// Trial close/stale/fail -> back to paused with new TrippedPromptSHA (sha2).
	now = now.Add(time.Minute)
	res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "trial-stale", Outcome: OutcomeStale, PromptSHA: sha2, At: now})
	if err != nil || !res.NewlyTripped || res.State.BreakerState != BreakerPaused || res.State.TrippedPromptSHA != sha2 {
		t.Fatalf("trial stale must re-trip with sha2: %+v err=%v", res, err)
	}

	// Manual pause must NOT auto-reset even when SHA changes to sha3.
	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "manual", "github:alice"); err != nil {
		t.Fatal(err)
	}
	gotSt, moved, err = b.CheckPromptReset(t.Context(), "acme/app", "dead-code", sha3)
	if err != nil || moved || gotSt.BreakerState != BreakerPaused {
		t.Fatalf("manual pause must block prompt auto-reset: st=%+v moved=%t err=%v", gotSt, moved, err)
	}

	// Resume clears both manual pause and breaker.
	now = now.Add(time.Minute)
	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", false, "", ""); err != nil {
		t.Fatal(err)
	}
	gotSt, err = st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || gotSt.Paused || gotSt.BreakerState != BreakerOK || gotSt.BreakerResetAt == nil || len(gotSt.BreakerHistory) != 0 {
		t.Fatalf("SetPaused(false) did not reset breaker cleanly: %+v err=%v", gotSt, err)
	}

	// Trip again, move to half_open on sha3, and trial merged -> ok.
	for i := range 3 {
		now = now.Add(time.Minute)
		if _, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: fmt.Sprintf("c2-%d", i), Outcome: OutcomeClosedUnmerged, PromptSHA: sha2, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, moved, err := b.CheckPromptReset(t.Context(), "acme/app", "dead-code", sha3); err != nil || !moved {
		t.Fatalf("expected half_open transition, moved=%t err=%v", moved, err)
	}
	now = now.Add(time.Minute)
	res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "trial-merge", Outcome: OutcomeMerged, PromptSHA: sha3, At: now})
	if err != nil || res.NewlyTripped || res.State.BreakerState != BreakerOK || res.State.BreakerReason != "" || res.State.TrippedPromptSHA != "" {
		t.Fatalf("trial merge must reset breaker to ok: %+v err=%v", res, err)
	}
}

func TestBreakerManualTrialWhilePausedIdempotencyAndRevert(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st, cc := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("1", 64)
	sha2 := strings.Repeat("2", 64)

	// Pre-record merge -> idempotent replay emits 0 extra writes -> revert flips outcome and updates window.
	res, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "t1", Outcome: OutcomeMerged, PromptSHA: sha1, At: now})
	if err != nil || res.State.BreakerState != BreakerOK {
		t.Fatal(err)
	}
	beforeWrites := cc.creates.Load() + cc.updates.Load()
	_, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "t1", Outcome: OutcomeMerged, PromptSHA: sha1, At: now})
	if err != nil || cc.creates.Load()+cc.updates.Load() != beforeWrites {
		t.Fatalf("idempotent replay wrote ConfigMap: err=%v", err)
	}
	now = now.Add(time.Minute)
	res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "t1", Outcome: OutcomeReverted, PromptSHA: sha1, At: now})
	if err != nil || len(res.State.BreakerHistory) != 1 || res.State.BreakerHistory[0].Outcome != OutcomeReverted {
		t.Fatalf("revert did not update existing entry: %+v err=%v", res.State, err)
	}

	// Trip breaker to paused.
	for i := range 2 {
		now = now.Add(time.Minute)
		res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: fmt.Sprintf("c-%d", i), Outcome: OutcomeClosedUnmerged, PromptSHA: sha1, At: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	if res.State.BreakerState != BreakerPaused {
		t.Fatalf("expected paused after revert + 2 closes, got %+v", res.State)
	}

	// Manual trial while paused fails with sha2 -> stays paused (NewlyTripped=false) and updates TrippedPromptSHA to sha2.
	now = now.Add(time.Minute)
	res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "manual-fail", Outcome: OutcomeClosedUnmerged, PromptSHA: sha2, At: now})
	if err != nil || res.NewlyTripped || res.State.BreakerState != BreakerPaused || res.State.TrippedPromptSHA != sha2 {
		t.Fatalf("failed manual trial should keep paused and update sha2: %+v err=%v", res, err)
	}

	// Manual trial while paused succeeds -> transitions paused -> ok.
	now = now.Add(time.Minute)
	res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{ID: "manual-win", Outcome: OutcomeProposalAccepted, PromptSHA: sha2, At: now})
	if err != nil || res.NewlyTripped || res.State.BreakerState != BreakerOK {
		t.Fatalf("accepted manual trial should reset breaker to ok: %+v err=%v", res, err)
	}
}

func TestBreakerColdStartReconstructAndSummarizeTasks(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 5, 500_000_000, time.UTC)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("a", 64)

	// Cold start with 0 Task CRs: reconstruct from 3 closed/stale GitHub PRs carrying factory/routine:dead-code.
	pulls := []forge.LabelledPull{
		{Number: 12, State: "CLOSED", Labels: []string{"factory/routine:dead-code", "factory/stale"}},
		{Number: 11, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}},
		{Number: 10, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}},
		{Number: 9, State: "MERGED", MergedAt: now.Add(-10 * time.Hour), Labels: []string{"factory/routine:dead-code"}},
	}
	got, err := b.Reconstruct(t.Context(), "acme/app", "dead-code", spec, nil, pulls, nil, sha1)
	if err != nil {
		t.Fatal(err)
	}
	if got.BreakerState != BreakerPaused || got.BreakerReason != BreakerReasonConsecutiveClosed || got.TrippedPromptSHA != sha1 {
		t.Fatalf("cold-start reconstruct did not trip breaker: %+v", got)
	}

	// SummarizeTasks truncates LastRunAt to time.Second and summarizes Open/Rolling counts.
	t1Time := now.Add(-2 * time.Hour)
	t2Time := now.Add(-time.Hour)
	t3Time := now
	t1 := testHistoryTask("t1", "acme/app", "dead-code", v1alpha1.PhaseDone, "merged", 9, t1Time)
	t1.CreationTimestamp = metav1.NewTime(t1Time)
	t2 := testHistoryTask("t2", "acme/app", "dead-code", v1alpha1.PhaseNoOp, "no_action", 0, t2Time)
	t2.CreationTimestamp = metav1.NewTime(t2Time)
	t3 := testHistoryTask("t3", "acme/app", "dead-code", v1alpha1.PhaseImplementing, "running", 0, time.Time{})
	t3.CreationTimestamp = metav1.NewTime(t3Time)
	t3.Status.EndedAt = nil

	sum := SummarizeTasks([]*v1alpha1.Task{&t1, &t2, &t3}, 10)
	if sum.OpenCount != 1 || sum.RollingMerged != 1 || sum.RollingClosed != 0 || sum.RollingNoOp != 1 || sum.LastOutcome != "no_op" {
		t.Fatalf("unexpected task summary: %+v", sum)
	}
	if sum.LastRunAt == nil || !sum.LastRunAt.Time.Equal(t3Time.Truncate(time.Second)) {
		t.Fatalf("LastRunAt = %v, want truncated %v", sum.LastRunAt, t3Time.Truncate(time.Second))
	}
}

func TestBreakerTrialMergeLaterRevertedUpdatesExistingEntryDespiteBreakerResetAt(t *testing.T) {
	for _, startState := range []string{BreakerHalfOpen, BreakerPaused} {
		t.Run("trial merge in "+startState+" later reverted", func(t *testing.T) {
			storeNow := time.Date(2026, 11, 26, 12, 0, 0, 10_000_000, time.UTC) // > EndedAt (real wall-clock advance)
			endedAt := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
			st, _ := testBreakerStore(t, &storeNow)
			b := &Breaker{Store: st, Now: func() time.Time { return storeNow }}
			spec := defaultBreakerSpec()
			sha1 := strings.Repeat("1", 64)
			sha2 := strings.Repeat("2", 64)

			if err := st.Mutate(t.Context(), "acme/app", "dead-code", func(s *RoutineState, _ time.Time) bool {
				s.BreakerState = startState
				s.BreakerReason = BreakerReasonConsecutiveClosed
				s.TrippedPromptSHA = sha1
				return true
			}); err != nil {
				t.Fatal(err)
			}

			// Trial merges with EndedAt < storeNow; Breaker.Record sets BreakerResetAt = storeNow (> EndedAt).
			res, err := b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{
				ID:        "trial-task",
				Outcome:   OutcomeMerged,
				PromptSHA: sha2,
				At:        endedAt,
			})
			if err != nil || res.State.BreakerState != BreakerOK || res.State.BreakerResetAt == nil {
				t.Fatalf("expected trial merge to reset breaker: res=%+v err=%v", res, err)
			}

			// Later revert preserves EndedAt (< *BreakerResetAt); must update existing entry to OutcomeReverted!
			storeNow = storeNow.Add(5 * time.Minute)
			res, err = b.Record(t.Context(), "acme/app", "dead-code", spec, BreakerOutcome{
				ID:        "trial-task",
				Outcome:   OutcomeReverted,
				PromptSHA: sha2,
				At:        endedAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.State.BreakerHistory) != 1 || res.State.BreakerHistory[0].Outcome != OutcomeReverted {
				t.Fatalf("revert of trial merge was dropped: history=%+v", res.State.BreakerHistory)
			}
		})
	}
}

func TestBreakerReconstructKeepsOlderClosedPRsBeforeNewerMergedTask(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	yesterday := now.Add(-24 * time.Hour)
	st, _ := testBreakerStore(t, &now)
	b := &Breaker{Store: st, Now: func() time.Time { return now }}
	spec := defaultBreakerSpec()
	sha1 := strings.Repeat("a", 64)

	// Newer merged Task CR (#13) + older GC'd CLOSED PRs (#12, #11, #10) from PullsWithLabel (newest-created first).
	tWin := testHistoryTask("t-win", "acme/app", "dead-code", v1alpha1.PhaseDone, "merged_verified", 13, yesterday)
	pulls := []forge.LabelledPull{
		{Number: 13, State: "MERGED", MergedAt: yesterday, Labels: []string{"factory/routine:dead-code"}},
		{Number: 12, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}},
		{Number: 11, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}},
		{Number: 10, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}},
	}

	got, err := b.Reconstruct(t.Context(), "acme/app", "dead-code", spec, []v1alpha1.Task{tWin}, pulls, nil, sha1)
	if err != nil {
		t.Fatal(err)
	}
	if got.BreakerState != BreakerOK {
		t.Fatalf("expected BreakerOK (older closed PRs must sort before newer merged #13), got %+v", got)
	}
	if len(got.BreakerHistory) != 4 || got.BreakerHistory[len(got.BreakerHistory)-1].ID != "t-win" {
		t.Fatalf("expected t-win last chronologically, got %+v", got.BreakerHistory)
	}
}
