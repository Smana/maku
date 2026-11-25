// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

func escalatedOpenCodeTask(cohort string) *v1alpha1.Task {
	tk := issueTask("3buqdlot", 7, "fix it")
	tk.Spec.Template = "pair"
	tk.Spec.PredictedClass = "review"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}
	since := metav1.NewTime(now)
	tk.Status = v1alpha1.TaskStatus{
		Phase:      v1alpha1.PhaseEscalated,
		Reason:     "agent_stuck",
		PhaseSince: &since,
		RoomRef:    "3buqdlot",
		Usage:      v1alpha1.Usage{Tokens: 500_000, CostMicroUSD: 1_200_000},
		Profile: &v1alpha1.ProfileStatus{
			Name:          "review-floor",
			Source:        triage.SourceRepository,
			Cohort:        cohort,
			Harness:       config.HarnessOpenCode,
			HarnessSource: triage.HarnessSourceTreatment,
			Checks:        true,
			Resolved:      true,
		},
		Runs: []v1alpha1.RunRecord{{
			ID:           rid(0),
			Role:         "implementer",
			Trigger:      "initial",
			Phase:        "Failed",
			CostMicroUSD: 1_200_000,
			Provenance:   v1alpha1.RunProvenance{Harness: config.HarnessOpenCode},
		}},
	}
	return tk
}

func TestRescueCountsFailureOnceAndKeepsRescueOutsideTheTrial(t *testing.T) {
	cf := profileCfg(true)
	tk := escalatedOpenCodeTask(triage.CohortTreatment)
	g := profileRig(t, cf, tk, "")
	g.f.SetComments(7, forge.Comment{ID: 42, Author: "Smana", Body: "/factory retry harness=openhands", At: now.Add(time.Minute)})
	g.r.CanWrite = func(_ context.Context, _, _ string) (bool, error) { return true, nil }

	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseQueued {
		t.Fatalf("phase = %q, want Queued", got.Status.Phase)
	}
	p := got.Status.Profile
	if p.Cohort != triage.CohortRescue || p.Harness != config.HarnessOpenHands || p.HarnessSource != triage.HarnessSourceRescue || p.Counted {
		t.Fatalf("unexpected profile after rescue: %+v", p)
	}
	if p.Name != "review-floor" || got.Status.RoomRef != "3buqdlot" {
		t.Fatalf("rescue must preserve workflow and room: %+v room=%q", p, got.Status.RoomRef)
	}
	if !slices.Contains(got.Status.OutcomeCountedCohorts, triage.CohortTreatment) {
		t.Fatalf("OutcomeCountedCohorts = %v, want treatment recorded", got.Status.OutcomeCountedCohorts)
	}
	// Original failed treatment/opencode attempt counted once.
	if len(g.metrics.outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want 1", g.metrics.outcomes)
	}
	orig := g.metrics.outcomes[0]
	if orig.cohort != triage.CohortTreatment || orig.harness != config.HarnessOpenCode || orig.outcome != "failed" || orig.usd != 1.2 {
		t.Fatalf("original outcome = %+v, want treatment/opencode/failed/$1.2", orig)
	}

	// Finish the rescued task as Done/accepted with additional $0.80 spend ($2.00 total -> $0.80 for rescue).
	got.Status.Phase = v1alpha1.PhaseDone
	got.Status.Usage.CostMicroUSD = 2_000_000
	got.Status.UsageSettled = true
	got.Status.Outcome = "accepted"
	if err := g.c.Status().Update(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	g.r.outcome(t.Context(), cf, got)
	if len(g.metrics.outcomes) != 2 {
		t.Fatalf("outcomes after finish = %+v, want 2", g.metrics.outcomes)
	}
	resc := g.metrics.outcomes[1]
	if resc.cohort != triage.CohortRescue || resc.harness != config.HarnessOpenHands || resc.outcome != "accepted" || resc.usd != 0.8 {
		t.Fatalf("rescue outcome = %+v, want rescue/openhands/accepted/$0.8", resc)
	}
	for _, o := range g.metrics.outcomes {
		if o.cohort == triage.CohortTreatment && o.outcome == "accepted" {
			t.Fatalf("treatment arm must never be credited with rescue acceptance: %+v", g.metrics.outcomes)
		}
	}
}

func TestCrashBetweenOriginalCountAndRescueCannotDoubleCount(t *testing.T) {
	cf := profileCfg(true)
	tk := escalatedOpenCodeTask(triage.CohortTreatment)
	// Simulate a crash replay where treatment was already recorded in OutcomeCountedCohorts.
	tk.Status.OutcomeCountedCohorts = []string{triage.CohortTreatment}
	g := profileRig(t, cf, tk, "")
	g.f.SetComments(7, forge.Comment{ID: 43, Author: "Smana", Body: "/factory retry harness=openhands", At: now.Add(time.Minute)})
	g.r.CanWrite = func(_ context.Context, _, _ string) (bool, error) { return true, nil }

	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Profile.Cohort != triage.CohortRescue || got.Status.Profile.Harness != config.HarnessOpenHands {
		t.Fatalf("profile = %+v, want rescue/openhands", got.Status.Profile)
	}
	if len(g.metrics.outcomes) != 0 {
		t.Fatalf("already-counted treatment cohort must not be counted twice: %+v", g.metrics.outcomes)
	}
}

func TestPlainRetryKeepsItsCohortAndHarness(t *testing.T) {
	cf := profileCfg(true)
	tk := escalatedOpenCodeTask(triage.CohortTreatment)
	g := profileRig(t, cf, tk, "")
	g.f.SetComments(7, forge.Comment{ID: 44, Author: "Smana", Body: "/factory retry", At: now.Add(time.Minute)})
	g.r.CanWrite = func(_ context.Context, _, _ string) (bool, error) { return true, nil }

	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseQueued {
		t.Fatalf("phase = %q, want Queued", got.Status.Phase)
	}
	p := got.Status.Profile
	if p.Cohort != triage.CohortTreatment || p.Harness != config.HarnessOpenCode || p.HarnessSource != triage.HarnessSourceTreatment {
		t.Fatalf("plain /factory retry must keep cohort and harness unchanged: %+v", p)
	}
	if len(g.metrics.outcomes) != 0 {
		t.Fatalf("plain retry must not count an outcome: %+v", g.metrics.outcomes)
	}
}
