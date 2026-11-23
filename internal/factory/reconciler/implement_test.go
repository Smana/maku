// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
)

// queuedFor is a task of the factory's queue another cap holds, its runs already recorded: what a
// first run reads when it decides whether to yield to a revision (CD22).
func queuedFor(name, reason string, runCount int) *v1alpha1.Task {
	started := metav1.NewTime(now.Add(-time.Hour))
	tk := issueTask(name, 8, "x")
	tk.Spec.Template, tk.Spec.PredictedClass = "solo", "review"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, RunMinutes: 45}
	tk.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseQueued, Reason: reason, RoomRef: name}
	for i := range runCount {
		tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: runID(name, i), Role: "implementer",
			Trigger: "initial", Phase: "Succeeded", Started: &started})
	}
	return tk
}

// CD22: a first run yields one slot-time to a revision; the revision itself does not wait.
func TestAFirstRunYieldsToARevision(t *testing.T) {
	ctx := t.Context()
	g := newRig(t, queuedFor("bbbbbbbb", "waiting_run_slot", 2), roomOf("bbbbbbbb"), issueTask("aaaaaaaa", 7, "x"))
	g.r.Cfg.Current().Caps.PreferRevisions = true
	tk := g.reconcile(t, "aaaaaaaa", 3) // Received → Triaged → Queued
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Reason != "waiting_revision_first" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if !slices.Contains(g.metrics.recorded, "revision_deferred") {
		t.Fatalf("the deferral is measured: %q", g.metrics.recorded)
	}
	// The revision is not waiting for the slot to continue itself: it starts on its own turn.
	if waiting, err := g.r.revisionWaiting(ctx, queuedFor("bbbbbbbb", "waiting_run_slot", 2)); err != nil || waiting {
		t.Fatalf("the revision yielded to itself: %t %v", waiting, err)
	}
	rev := g.reconcile(t, "bbbbbbbb", 1)
	if rev.Status.Phase != v1alpha1.PhaseImplementing || rev.Status.Reason == "waiting_revision_first" {
		t.Fatalf("the revision waited: %s %s", rev.Status.Phase, rev.Status.Reason)
	}
}

// The switch off changes nothing.
func TestWithoutTheSwitchNothingChanges(t *testing.T) {
	g := newRig(t, queuedFor("bbbbbbbb", "waiting_run_slot", 2), issueTask("aaaaaaaa", 7, "x"))
	tk := g.reconcile(t, "aaaaaaaa", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || tk.Status.Reason != "" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// Caps still bind: a neighbour stopped by another cap is not a revision waiting for a slot, and a
// slot that is already taken is taken whatever prefers revisions (Review Focus 5, CD22).
func TestANeighbourOnACapIsNoReasonToYield(t *testing.T) {
	for name, c := range map[string]struct {
		neighbour string
		live      int
	}{
		"the review WIP cap":       {neighbour: "waiting_review_wip"},
		"the active-task cap":      {neighbour: "waiting_active_tasks"},
		"every run slot is taken":  {neighbour: "waiting_run_slot", live: 4},
		"the neighbour is another": {neighbour: "waiting_room_busy"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, queuedFor("bbbbbbbb", c.neighbour, 2), issueTask("aaaaaaaa", 7, "x"))
			g.r.Cfg.Current().Caps.PreferRevisions = true
			for i := range c.live {
				id := "zzzzzzz" + string(rune('a'+i))
				g.runs.runs[id] = runs.Run{ID: id, Principal: runs.PrincipalFactory, Phase: "Running"}
			}
			tk := g.reconcile(t, "aaaaaaaa", 3)
			if c.live > 0 {
				if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Reason != "waiting_run_slot" {
					t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
				}
				return
			}
			if tk.Status.Phase != v1alpha1.PhaseImplementing || tk.Status.Reason != "" {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
		})
	}
}

type prBranchCountingForge struct {
	TaskForge
	calls int
}

func (f *prBranchCountingForge) PullRequestForBranch(ctx context.Context, branch string) (int, error) {
	f.calls++
	return f.TaskForge.PullRequestForBranch(ctx, branch)
}

// REL-M9: when boundPending deletes an unschedulable Pending run and ends the task, implementing
// returns immediately without falling through to detectPR or stuck.
func TestBoundPendingReturnsImmediatelyWithoutSpuriousCalls(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "x")
	tk.Spec.Template, tk.Spec.PredictedClass = "solo", "docs-links"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, RunMinutes: 45}
	started := metav1.NewTime(now.Add(-2 * time.Hour))
	tk.Status = v1alpha1.TaskStatus{
		Phase:   v1alpha1.PhaseImplementing,
		RoomRef: "3buqdlot",
		Runs:    []v1alpha1.RunRecord{{ID: runID("3buqdlot", 0), Role: "implementer", Trigger: "initial", Phase: "Pending", Started: &started}},
	}
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.r.Cfg.Current().Caps.MaxPendingMinutes = 30
	g.runs.runs[runID("3buqdlot", 0)] = runs.Run{ID: runID("3buqdlot", 0), Phase: "Pending", Created: now.Add(-2 * time.Hour)}
	cf := &prBranchCountingForge{TaskForge: g.f}
	g.r.Forge = cf

	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "run_unschedulable" {
		t.Fatalf("got %s/%s, want Escalated/run_unschedulable", got.Status.Phase, got.Status.Reason)
	}
	if cf.calls != 0 {
		t.Fatalf("detectPR called %d times after boundPending ended the task", cf.calls)
	}
}

// REL-M10: branchPushed iterates BranchActivity newest-first so a deleted branch is not
// misclassified as pushed_no_pr.
func TestBranchPushedReadsNewestFirst(t *testing.T) {
	g := newRig(t)
	g.f.SetActivity("agent/deleted",
		forge.Push{Type: "branch_deletion", Actor: "Smana"},
		forge.Push{Type: "push", Actor: "ogenki-agents[bot]"},
		forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]"},
	)
	g.f.SetActivity("agent/live",
		forge.Push{Type: "push", Actor: "ogenki-agents[bot]"},
		forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]"},
	)
	pushed, err := g.r.branchPushed(t.Context(), "agent/deleted")
	if err != nil || pushed {
		t.Fatalf("deleted branch: pushed=%v err=%v, want false, nil", pushed, err)
	}
	pushed, err = g.r.branchPushed(t.Context(), "agent/live")
	if err != nil || !pushed {
		t.Fatalf("live branch: pushed=%v err=%v, want true, nil", pushed, err)
	}
}
