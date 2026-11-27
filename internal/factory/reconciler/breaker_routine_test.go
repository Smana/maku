// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/upkeep"
)

func TestReconcilerOutcomeFeedsYieldBreaker(t *testing.T) {
	storeClient := fake.NewClientBuilder().WithScheme(scheme()).Build()
	_ = corev1.AddToScheme(storeClient.Scheme())
	curTime := now
	st := &upkeep.StateStore{Client: storeClient, Reader: storeClient, Namespace: "agent-system", Now: func() time.Time { return curTime }}
	breaker := &upkeep.Breaker{Store: st, Now: func() time.Time { return curTime }}

	// 1. NoOp is neutral; 3 closed/stale PR transitions trip consecutive_closed and fire OnRoutinePaused.
	var pausedCalls []string
	tNoOp := routineTask("br2345ab", "dead-code", "system:scheduler")
	tClose1 := routineTask("br2345ac", "dead-code", "system:scheduler")
	tStale := routineTask("br2345ad", "dead-code", "system:scheduler")
	tClose2 := routineTask("br2345ae", "dead-code", "system:scheduler")
	for _, tk := range []*v1alpha1.Task{tNoOp, tClose1, tStale, tClose2} {
		tk.Spec.Routine.PromptSHA = strings.Repeat("a", 64)
	}

	g := newRig(t, tNoOp, tClose1, tStale, tClose2)
	g.r.RoutineBreaker = breaker
	g.r.Now = func() time.Time { return curTime }
	g.r.OnRoutinePaused = func(_ context.Context, repo, routine, reason string) {
		pausedCalls = append(pausedCalls, repo+"/"+routine+":"+reason)
	}
	rc := &g.r.Cfg.Current().Repositories[0]
	rc.ResolvedRoutines = map[string]config.ResolvedRoutine{
		"dead-code": {
			Name: "dead-code",
			Mode: config.RoutineModePR,
			Breaker: config.ResolvedBreaker{
				Window:                 10,
				MinSamples:             5,
				MinMergeRate:           0.25,
				MaxConsecutiveClosed:   3,
				MaxConsecutiveFailures: 3,
			},
		},
	}

	// NoOp task: starts run, succeeds with no PR -> PhaseNoOp (must stay neutral).
	_ = g.reconcile(t, "br2345ab", 3)
	g.runs.set(runID("br2345ab", 0), "Succeeded")
	g.log.end(runID("br2345ab", 0), "Succeeded", "agent_finished")
	got := g.reconcile(t, "br2345ab", 1)
	if got.Status.Phase != v1alpha1.PhaseNoOp {
		t.Fatalf("got %s, want PhaseNoOp", got.Status.Phase)
	}
	gotSt, err := st.Get(t.Context(), rc.Name, "dead-code")
	if err != nil || len(gotSt.BreakerHistory) != 0 || gotSt.BreakerState == upkeep.BreakerPaused {
		t.Fatalf("expected NoOp not to record or pause: %+v err=%v", gotSt, err)
	}

	// Helper to drive a routine task through opening a PR and closing it (normal or stale).
	driveClosedPR := func(taskName string, prNum int, stale bool) {
		curTime = curTime.Add(time.Minute)
		_ = g.reconcile(t, taskName, 3)
		rID := runID(taskName, 0)
		g.runs.set(rID, "Running")
		g.f.SetBranch("agent/"+taskName, prNum)
		var labels []string
		if stale {
			labels = []string{"factory/stale"}
		}
		g.f.SetPR(forge.PR{
			Number:  prNum,
			URL:     "https://github.com/Smana/cloud-native-ref/pull/12",
			State:   "OPEN",
			NodeID:  "PR_1",
			HeadSHA: "abc",
		})
		_ = g.reconcile(t, taskName, 2)
		g.runs.set(rID, "Succeeded")
		g.log.end(rID, "Succeeded", "agent_finished")
		_ = g.reconcile(t, taskName, 1) // AwaitingCI
		g.f.SetPR(forge.PR{
			Number:   prNum,
			URL:      "https://github.com/Smana/cloud-native-ref/pull/12",
			State:    "CLOSED",
			ClosedAt: curTime,
			NodeID:   "PR_1",
			HeadSHA:  "abc",
			Labels:   labels,
		})
		curTime = curTime.Add(time.Minute)
		ended := g.reconcile(t, taskName, 1)
		if ended.Status.Phase != v1alpha1.PhaseClosed {
			t.Fatalf("task %s phase=%s, want PhaseClosed", taskName, ended.Status.Phase)
		}
	}

	driveClosedPR("br2345ac", 101, false)
	driveClosedPR("br2345ad", 102, true)
	driveClosedPR("br2345ae", 103, false)

	gotSt, err = st.Get(t.Context(), rc.Name, "dead-code")
	if err != nil {
		t.Fatal(err)
	}
	if gotSt.BreakerState != upkeep.BreakerPaused || gotSt.BreakerReason != upkeep.BreakerReasonConsecutiveClosed {
		t.Fatalf("expected breaker paused/consecutive_closed, got %+v", gotSt)
	}
	if len(pausedCalls) != 1 || pausedCalls[0] != rc.Name+"/dead-code:consecutive_closed" {
		t.Fatalf("OnRoutinePaused calls = %+v", pausedCalls)
	}
	if !containsString(g.metrics.recorded, "routine_paused "+rc.Name+" dead-code true") {
		t.Fatalf("expected routine_paused true metric, got %v", g.metrics.recorded)
	}
	if !containsString(g.metrics.recorded, "routine_merge_rate "+rc.Name+" dead-code 0.0000") {
		t.Fatalf("expected routine_merge_rate 0.0000 metric, got %v", g.metrics.recorded)
	}

	// 2. Drill revert is ignored; 3 consecutive PhaseEscalated transitions trip consecutive_failures.
	storeClient2 := fake.NewClientBuilder().WithScheme(scheme()).Build()
	_ = corev1.AddToScheme(storeClient2.Scheme())
	st2 := &upkeep.StateStore{Client: storeClient2, Reader: storeClient2, Namespace: "agent-system", Now: func() time.Time { return curTime }}
	breaker2 := &upkeep.Breaker{Store: st2, Now: func() time.Time { return curTime }}

	esc1 := routineTask("br2345ba", "dead-code", "system:scheduler")
	esc2 := routineTask("br2345bb", "dead-code", "system:scheduler")
	esc3 := routineTask("br2345bc", "dead-code", "system:scheduler")
	var paused2 []string
	g2 := newRig(t, esc1, esc2, esc3)
	g2.r.RoutineBreaker = breaker2
	g2.r.Now = func() time.Time { return curTime }
	g2.r.OnRoutinePaused = func(_ context.Context, _, _, reason string) {
		paused2 = append(paused2, reason)
	}
	g2.r.Cfg.Current().Repositories[0].ResolvedRoutines = rc.ResolvedRoutines

	// Verify proposal_filed and Drill revert are skipped cleanly by recordRoutineBreaker directly.
	propDone := routineTask("br2345pa", "dead-code", "system:scheduler")
	propDone.Status.Phase = v1alpha1.PhaseDone
	propDone.Status.Reason = "proposal_filed"
	drillRev := routineTask("br2345da", "dead-code", "system:scheduler")
	drillRev.Status.Phase = v1alpha1.PhaseReverted
	drillRev.Annotations = map[string]string{v1alpha1.AnnotationDrill: "true"}
	ctxWithFx := context.WithValue(t.Context(), effectsKey{}, &effects{})
	g2.r.recordRoutineBreaker(ctxWithFx, g2.r.Cfg.Current(), propDone)
	g2.r.recordRoutineBreaker(ctxWithFx, g2.r.Cfg.Current(), drillRev)
	if len(ctxWithFx.Value(effectsKey{}).(*effects).after) != 0 {
		t.Fatalf("expected proposal_filed and Drill revert to schedule 0 breaker effects")
	}

	for _, id := range []string{"br2345ba", "br2345bb", "br2345bc"} {
		curTime = curTime.Add(time.Minute)
		_ = g2.reconcile(t, id, 3)
		rID := runID(id, 0)
		g2.runs.set(rID, "Failed")
		g2.log.end(rID, "Failed", "pod_lost")
		ended := g2.reconcile(t, id, 1)
		if ended.Status.Phase != v1alpha1.PhaseEscalated {
			t.Fatalf("task %s phase=%s, want PhaseEscalated", id, ended.Status.Phase)
		}
	}
	gotSt2, _ := st2.Get(t.Context(), rc.Name, "dead-code")
	if gotSt2.BreakerState != upkeep.BreakerPaused || gotSt2.BreakerReason != upkeep.BreakerReasonConsecutiveFailures {
		t.Fatalf("expected consecutive_failures trip, got %+v", gotSt2)
	}
	if len(paused2) != 1 || paused2[0] != upkeep.BreakerReasonConsecutiveFailures {
		t.Fatalf("paused2 = %v", paused2)
	}
}

func TestReconcilerHalfOpenTrialMergeThenRevertedWithAdvancingClock(t *testing.T) {
	storeClient := fake.NewClientBuilder().WithScheme(scheme()).Build()
	_ = corev1.AddToScheme(storeClient.Scheme())
	recNow := now
	// StateStore clock advances slightly past reconciler's EndedAt (simulating API update latency).
	st := &upkeep.StateStore{
		Client:    storeClient,
		Reader:    storeClient,
		Namespace: "agent-system",
		Now:       func() time.Time { return recNow.Add(10 * time.Millisecond) },
	}
	breaker := &upkeep.Breaker{Store: st, Now: func() time.Time { return recNow.Add(10 * time.Millisecond) }}

	sha1 := strings.Repeat("a", 64)
	sha2 := strings.Repeat("b", 64)
	if err := st.Mutate(t.Context(), "Smana/cloud-native-ref", "dead-code", func(s *upkeep.RoutineState, _ time.Time) bool {
		s.BreakerState = upkeep.BreakerHalfOpen
		s.BreakerReason = upkeep.BreakerReasonConsecutiveClosed
		s.TrippedPromptSHA = sha1
		return true
	}); err != nil {
		t.Fatal(err)
	}

	tk := routineTask("brhalf01", "dead-code", "system:scheduler")
	tk.Spec.Routine.PromptSHA = sha2
	tk.Status.Phase = v1alpha1.PhaseVerifying
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{
		Number:         77,
		NodeID:         "PR_77",
		AutoMerged:     true,
		MergeCommitSHA: "m77",
		MergedAt:       &metav1.Time{Time: recNow.Add(-31 * time.Minute)},
	}

	g := newRig(t, tk)
	g.r.RoutineBreaker = breaker
	g.r.Now = func() time.Time { return recNow }
	g.r.Cfg.Current().Control = config.Control{Repository: g.r.Cfg.Current().Repositories[0].Name, Issue: 1}
	g.r.Cfg.Current().Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"dead-code": {
			Name: "dead-code",
			Mode: config.RoutineModePR,
			Breaker: config.ResolvedBreaker{
				Window:                 10,
				MinSamples:             5,
				MinMergeRate:           0.25,
				MaxConsecutiveClosed:   3,
				MaxConsecutiveFailures: 3,
			},
		},
	}
	g.f.SetCommitChecks("m77", green("SUCCESS").Runs...)
	g.f.SetPR(forge.PR{Number: 77, NodeID: "PR_77", State: "MERGED", MergedBy: "ogenki-agent-merger[bot]", MergeCommitSHA: "m77", MergedAt: recNow.Add(-31 * time.Minute)})

	got := g.reconcile(t, "brhalf01", 1)
	if got.Status.Phase != v1alpha1.PhaseDone || got.Status.EndedAt == nil {
		t.Fatalf("expected PhaseDone with EndedAt, got phase=%s endedAt=%v", got.Status.Phase, got.Status.EndedAt)
	}
	stAfterMerge, err := st.Get(t.Context(), "Smana/cloud-native-ref", "dead-code")
	if err != nil {
		t.Fatal(err)
	}
	if stAfterMerge.BreakerState != upkeep.BreakerOK || stAfterMerge.BreakerResetAt == nil || !got.Status.EndedAt.Time.Before(*stAfterMerge.BreakerResetAt) {
		t.Fatalf("expected BreakerOK with EndedAt < BreakerResetAt: st=%+v endedAt=%v", stAfterMerge, got.Status.EndedAt.Time)
	}

	// Later maintainer revert: EndedAt is preserved (< BreakerResetAt), breaker must flip entry to OutcomeReverted!
	recNow = recNow.Add(10 * time.Minute)
	var latest v1alpha1.Task
	if err := g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: "brhalf01"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Annotations = map[string]string{v1alpha1.AnnotationRevert: "label"}
	if err := g.c.Update(t.Context(), &latest); err != nil {
		t.Fatal(err)
	}
	got = g.reconcile(t, "brhalf01", 1)
	if got.Status.Phase != v1alpha1.PhaseReverted {
		t.Fatalf("expected PhaseReverted, got %s", got.Status.Phase)
	}
	stAfterRevert, err := st.Get(t.Context(), "Smana/cloud-native-ref", "dead-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(stAfterRevert.BreakerHistory) != 1 || stAfterRevert.BreakerHistory[0].Outcome != upkeep.OutcomeReverted {
		t.Fatalf("expected BreakerHistory[0].Outcome == reverted, got %+v", stAfterRevert.BreakerHistory)
	}
}
