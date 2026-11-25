// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// Phase H2: a task whose dependency issue has no merged pull request waits in Triaged with reason
// waiting_dependency, holding no run and no caps.activeTasks slot. Once the dependency merges, the
// dependent advances and runs.
func TestADependentTaskWaitsWithNoRun(t *testing.T) {
	dep := issueTask("dep11111", 12, "# Base change\n\n## Acceptance check\n- tests pass")
	dep.Labels = map[string]string{v1alpha1.LabelIssue: "12"}

	child := issueTask("chld2222", 13, "# Dependent change\n\n## Acceptance check\n- tests pass")
	child.Labels = map[string]string{v1alpha1.LabelIssue: "13"}
	child.Spec.DependsOn = []int32{12}

	other := issueTask("othr3333", 14, "# Independent change\n\n## Acceptance check\n- tests pass")
	other.Labels = map[string]string{v1alpha1.LabelIssue: "14"}

	g := newRig(t, dep, child, other)
	g.r.Cfg.Current().Caps.ActiveTasks = 1 // only one active task slot
	g.f.SetIssue(forge.Issue{Number: 12, State: "OPEN"})
	g.f.SetIssue(forge.Issue{Number: 13, State: "OPEN"})
	g.f.SetIssue(forge.Issue{Number: 14, State: "OPEN"})

	// Reconcile the dependent multiple times while #12 is open with no merged PR.
	got := g.reconcile(t, "chld2222", 3)
	if got.Status.Phase != v1alpha1.PhaseTriaged || got.Status.Reason != ReasonWaitingDependency {
		t.Fatalf("dependent phase=%q reason=%q, want Triaged / %s", got.Status.Phase, got.Status.Reason, ReasonWaitingDependency)
	}
	if len(got.Status.Runs) != 0 || got.Status.RoomRef != "" {
		t.Fatalf("dependent must hold no run and no room while waiting: runs=%d room=%q", len(got.Status.Runs), got.Status.RoomRef)
	}
	if len(g.runs.specs) != 0 {
		t.Fatalf("no run may be created for waiting dependent: %+v", g.runs.specs)
	}

	// Verify the waiting dependent does NOT consume the single caps.activeTasks slot:
	// an independent task starts immediately.
	o := g.reconcile(t, "othr3333", 3)
	if o.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("independent task phase=%q reason=%q, want Implementing (waiting_dependency must not hold activeTasks slot)", o.Status.Phase, o.Status.Reason)
	}
	// Mark independent task Done so the single slot is free again.
	setPhase(t, g, "othr3333", v1alpha1.PhaseDone, "merged")

	// Now merge dependency #12's pull request.
	mergedAt := metav1.NewTime(now)
	var depObj v1alpha1.Task
	if err := g.c.Get(t.Context(), taskKey("dep11111"), &depObj); err != nil {
		t.Fatal(err)
	}
	depObj.Status.Phase = v1alpha1.PhaseDone
	depObj.Status.Reason = "merged"
	depObj.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 101, URL: "https://github.com/Smana/cloud-native-ref/pull/101", MergedAt: &mergedAt}
	if err := g.c.Status().Update(t.Context(), &depObj); err != nil {
		t.Fatal(err)
	}
	g.f.SetIssue(forge.Issue{Number: 12, State: "CLOSED"})

	// Reconciling the dependent now advances it past Triaged to Queued and Implementing.
	got = g.reconcile(t, "chld2222", 2)
	if got.Status.Phase != v1alpha1.PhaseImplementing || len(got.Status.Runs) != 1 {
		t.Fatalf("dependent after dep merge: phase=%q reason=%q runs=%d, want Implementing with 1 run",
			got.Status.Phase, got.Status.Reason, len(got.Status.Runs))
	}
}

// Phase H2: a dependency issue closed without a merged pull request ends its dependents
// NoOp dependency_dropped with no run started.
func TestADroppedDependencyEndsItsDependents(t *testing.T) {
	dep := issueTask("dep11111", 12, "# Base change\n\n## Acceptance check\n- tests pass")
	dep.Labels = map[string]string{v1alpha1.LabelIssue: "12"}

	child := issueTask("chld2222", 13, "# Dependent change\n\n## Acceptance check\n- tests pass")
	child.Labels = map[string]string{v1alpha1.LabelIssue: "13"}
	child.Spec.DependsOn = []int32{12}

	g := newRig(t, dep, child)
	g.f.SetIssue(forge.Issue{Number: 12, State: "OPEN"})
	g.f.SetIssue(forge.Issue{Number: 13, State: "OPEN"})

	// First waits in Triaged while #12 is open.
	got := g.reconcile(t, "chld2222", 2)
	if got.Status.Phase != v1alpha1.PhaseTriaged || got.Status.Reason != ReasonWaitingDependency {
		t.Fatalf("before close: phase=%q reason=%q", got.Status.Phase, got.Status.Reason)
	}

	// #12 is closed without a merged pull request.
	g.f.SetIssue(forge.Issue{Number: 12, State: "CLOSED"})
	g.r.Now = func() time.Time { return now.Add(time.Minute) }

	got = g.reconcile(t, "chld2222", 1)
	if got.Status.Phase != v1alpha1.PhaseNoOp || got.Status.Reason != ReasonDependencyDropped {
		t.Fatalf("after unmerged close: phase=%q reason=%q, want NoOp / %s", got.Status.Phase, got.Status.Reason, ReasonDependencyDropped)
	}
	if len(got.Status.Runs) != 0 || len(g.runs.specs) != 0 {
		t.Fatalf("dropped dependent must start no run: runs=%d specs=%d", len(got.Status.Runs), len(g.runs.specs))
	}
	if g.r.noopRetryable(got) {
		t.Fatal("dependency_dropped NoOp task must not be retryable")
	}
}
