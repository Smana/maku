// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/runs"
)

// A task that ends while its run is still recorded as Running closes that record: its claim is
// gone (deleted by the stuck bound, by a stop, or out of band), so no later observe can move it,
// and every reader of status.runs (the room's facts, maku task) would keep showing the run live.
func TestAnEndingClosesTheRunsItsClaimsNoLongerHold(t *testing.T) {
	for name, c := range map[string]struct {
		end           func(t *testing.T, g *rig)
		phase, reason string
		taskPhase     string
	}{
		"stuck": {func(_ *testing.T, g *rig) {
			g.r.Now = func() time.Time { return now.Add(StuckAfter + time.Minute) }
		}, "Failed", "stuck", v1alpha1.PhaseEscalated},
		"lost": {func(t *testing.T, g *rig) {
			_ = g.runs.Delete(t.Context(), rid(0))
		}, "Failed", "run_lost", v1alpha1.PhaseEscalated},
		"stopped": {func(t *testing.T, g *rig) {
			halt(t, g, "annotation")
		}, "Revoked", "stopped_by_annotation", v1alpha1.PhaseStopped},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, issueTask("3buqdlot", 7, "x"))
			g.reconcile(t, "3buqdlot", 3)
			g.runs.set(rid(0), "Running")
			if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Runs[0].Phase != "Running" {
				t.Fatalf("seeded: %+v", tk.Status.Runs[0])
			}
			c.end(t, g)
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.taskPhase {
				t.Fatalf("task: %s %s", tk.Status.Phase, tk.Status.Reason)
			}
			rec := tk.Status.Runs[0]
			if rec.Phase != c.phase || rec.Reason != c.reason || rec.Finished == nil {
				t.Fatalf("record: phase=%q reason=%q finished=%v", rec.Phase, rec.Reason, rec.Finished)
			}
			if outcomeOf(rec) == "" {
				t.Fatal("the room's facts would still show the run live")
			}
		})
	}
}

// A claim that ends after the task moved on still lands in its own record, not only the current
// run's: an earlier record left Running takes its claim's terminal phase, and a record already
// closed is never reopened by a claim that still reads Running.
func TestAbsorbMovesEveryRecordForwardOnly(t *testing.T) {
	tk := &v1alpha1.Task{Status: v1alpha1.TaskStatus{Runs: []v1alpha1.RunRecord{
		{ID: "a", Phase: "Running"},
		{ID: "b", Phase: "Failed"},
		{ID: "c", Phase: "Running"},
	}}}
	absorb(tk, []runs.Run{{ID: "a", Phase: "BudgetExhausted"}, {ID: "b", Phase: "Running"}, {ID: "c", Phase: "Running"}})
	if got := []string{tk.Status.Runs[0].Phase, tk.Status.Runs[1].Phase, tk.Status.Runs[2].Phase}; got[0] != "BudgetExhausted" || got[1] != "Failed" || got[2] != "Running" {
		t.Fatalf("phases: %v", got)
	}
}
