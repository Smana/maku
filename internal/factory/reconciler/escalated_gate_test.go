// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// escalated puts the rig's task in Escalated for reason, an hour before now, its pull request's
// maku/merge-gate left at the ci_red the gate posted before the escalation.
func (g *rig) escalated(t *testing.T, reason string) {
	t.Helper()
	g.r.Cfg.Current().Repositories[0].Merge.GateStatus = new(true)
	g.patch(t, func(tk *v1alpha1.Task) {
		since := metav1.NewTime(now.Add(-time.Hour))
		tk.Status.Phase, tk.Status.Reason, tk.Status.PhaseSince = v1alpha1.PhaseEscalated, reason, &since
	})
	if err := g.f.SetStatus(context.Background(), "abc", "failure", GateStatusContext, "docs-links: ci_red"); err != nil {
		t.Fatal(err)
	}
}

// lastGateStatus is the last maku/merge-gate posted on head.
func lastGateStatus(g *rig, head string) forge.Status {
	got := g.f.Statuses(head)
	return got[len(got)-1]
}

// #628: a task escalated on its budget, whose current head a maintainer approves, returns to the
// gate as an AwaitingHuman task does, uncounted and once per review, and merges on the approval.
func TestAnApprovalReturnsAnEscalatedTaskToTheGate(t *testing.T) {
	for _, reason := range []string{"budget-task", "budget-run", "ci_red"} {
		t.Run(reason, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingHuman)
			g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
			g.escalated(t, reason)
			pr := gatePR("abc", rid(0))
			pr.Reviews = []forge.Review{{ID: 77, Author: "Smana", State: "APPROVED", Commit: "abc", At: now}}
			g.f.SetPR(pr)
			g.f.SetChecks(12, green("SUCCESS"))
			got := g.reconcile(t, "3buqdlot", 1)
			if got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "reentered_approved" ||
				got.Status.GateReentries != 0 || !containsID(got.Status.Handled, -77) {
				t.Fatalf("%s %s reentries %d handled %v", got.Status.Phase, got.Status.Reason, got.Status.GateReentries, got.Status.Handled)
			}
			if got = g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseAutoMerging || got.Status.PullRequest.MergedHow != "approved" {
				t.Fatalf("arms on the approval: %s %s %+v", got.Status.Phase, got.Status.Reason, got.Status.PullRequest)
			}
		})
	}
}

// An escalated task's approval returns nothing with approveToMerge off, on an older head, from a
// non-maintainer, or for a reason a human fixes on the pull request.
func TestAnApprovalOfAnEscalatedTaskNeedsTheHumanPath(t *testing.T) {
	for name, tc := range map[string]struct {
		reason, commit, author string
		off                    bool
	}{
		"switch off":     {reason: "budget-task", commit: "abc", author: "Smana", off: true},
		"older head":     {reason: "budget-task", commit: "old", author: "Smana"},
		"not maintainer": {reason: "budget-task", commit: "abc", author: "octocat"},
		"by hand":        {reason: "not_mergeable", commit: "abc", author: "Smana"},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingHuman)
			g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = !tc.off
			g.escalated(t, tc.reason)
			pr := gatePR("abc", rid(0))
			pr.Reviews = []forge.Review{{ID: 77, Author: tc.author, State: "APPROVED", Commit: tc.commit, At: now}}
			g.f.SetPR(pr)
			got := g.reconcile(t, "3buqdlot", 1)
			if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != tc.reason {
				t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
			}
		})
	}
}

// #628: a ci_red escalation returns to the gate once the head's required checks are green, and the
// gate's fresh decision replaces the stale ci_red.
func TestGreenChecksReturnACIRedEscalationToTheGate(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingHuman)
	g.escalated(t, "ci_red")
	g.f.SetPR(gatePR("abc", rid(0)))
	g.f.SetChecks(12, green("SUCCESS"))
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "reentered_ci_green" || got.Status.GateReentries != 1 {
		t.Fatalf("%s %s %d", got.Status.Phase, got.Status.Reason, got.Status.GateReentries)
	}
	g.reconcile(t, "3buqdlot", 1)
	if s := lastGateStatus(g, "abc"); s.Description == "docs-links: ci_red" {
		t.Fatalf("the stale ci_red stands: %+v", g.f.Statuses("abc"))
	}
}

// #628: any other escalation leaves the task with a human, but a stale ci_red on its green head is
// replaced by what is true: a human decides, for the escalation's reason. Red checks leave it.
func TestGreenChecksRefreshAStaleCIRedOnAnEscalatedTask(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingHuman)
	g.escalated(t, "budget-task")
	g.f.SetPR(gatePR("abc", rid(0)))
	red := green("SUCCESS")
	red.Runs[0].State = "FAILURE"
	g.f.SetChecks(12, red)
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseEscalated || len(g.f.Statuses("abc")) != 1 {
		t.Fatalf("red checks: %s %+v", got.Status.Phase, g.f.Statuses("abc"))
	}
	g.f.SetChecks(12, green("SUCCESS"))
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task" {
		t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
	}
	if s := lastGateStatus(g, "abc"); s.State != "pending" || s.Description != "docs-links: a human decides, budget-task" {
		t.Fatalf("%+v", g.f.Statuses("abc"))
	}
	g.reconcile(t, "3buqdlot", 1)
	if n := len(g.f.Statuses("abc")); n != 2 {
		t.Fatalf("the refresh is posted once: %+v", g.f.Statuses("abc"))
	}
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
