// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/runs"
)

// runReserve is one standard-tier child run's whole reservation: 1.5 M tokens at the reference rate.
const runReserve = 2_550_000

// queuedChild is child idx of parent, admitted, with an open pull request 12+idx and runs as its
// history, queued for a revision: its next run is a retry of its own allocation.
func queuedChild(parent *v1alpha1.Task, idx int, history []v1alpha1.RunRecord) (*v1alpha1.Task, *roomv1.Room, forge.PR) {
	c := parent.Status.Coordination.Plan.Children[idx]
	child := coordination.ChildTask("agent-system", cfg(), parent, c, 21+idx, 1)
	child.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
	since := metav1.NewTime(now.Add(-30 * time.Minute))
	n := 12 + idx
	child.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseQueued, PhaseSince: &since, RoomRef: child.Name, NextTrigger: "human",
		PullRequest:    &v1alpha1.PullRequestRef{Number: n, URL: fmt.Sprintf("https://github.com/%s/pull/%d", c.Repository, n)},
		Classification: &v1alpha1.Classification{Tier: c.Tier, Classifier: "coordination", Fallback: "static"},
		Coordination:   &v1alpha1.CoordinationStatus{PlanVersion: parent.Status.Coordination.Plan.Version, ChildID: c.ID, AllocationMicroUSD: c.BudgetMicroUSD},
		Runs:           history}
	room := roomOf(child.Name)
	room.Spec.Repository = c.Repository
	return child, room, forge.PR{Number: n, State: "OPEN", HeadRef: "agent/" + child.Name}
}

// MR8 under the spec's $12.75 family: each child's lifetime cap is its own confirmed $5.10, the
// parent's allocations sum to the total, and a child's next run starts only while its own
// allocation holds the run's whole reservation over its settled spend, an unpriced run counted at
// the reference rate, never $0. It refuses even with the task cap in shadow and with its sibling's
// allocation untouched; the fleet's day stays a wait, never an allocation refusal.
func TestCoordinationChildRunsWithinItsOwnBudget(t *testing.T) {
	p := coordinationPlan(t, true)
	if got, _ := coordination.Allocated(p); got != 12_750_000 || p.Budget.TotalMicroUSD != 12_750_000 {
		t.Fatalf("the family allocates %d of %d", got, p.Budget.TotalMicroUSD)
	}
	started := metav1.NewTime(now.Add(-time.Hour))
	ended := func(cost, tokens int64, unpriced string) []v1alpha1.RunRecord {
		return []v1alpha1.RunRecord{{ID: "aaaaaaaa", Role: "implementer", Trigger: "initial", Phase: "Succeeded",
			CostMicroUSD: cost, Tokens: tokens, Unpriced: unpriced, Started: &started}}
	}
	for _, tc := range []struct {
		name    string
		history []v1alpha1.RunRecord
		spent   int64 // the fleet's day already settled
		phase   string
		reason  string
	}{
		{name: "a whole run left", history: ended(5_100_000-runReserve, 1, ""), phase: v1alpha1.PhaseImplementing},
		{name: "less than a run left", history: ended(5_100_000-runReserve+1, 1, ""), phase: v1alpha1.PhaseEscalated, reason: reasonAllocationExhausted},
		{name: "an unpriced run is never $0", history: ended(0, 1_500_001, "unpriced"), phase: v1alpha1.PhaseEscalated, reason: reasonAllocationExhausted},
		{name: "the fleet's day is a wait", history: ended(1_000_000, 1, ""), spent: 25_000_000, phase: v1alpha1.PhaseQueued, reason: "waiting_daily_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := filedParent(p, coordination.ReasonFiled)
			contract, roomA, prA := queuedChild(parent, 0, tc.history)
			consumer, roomB, _ := queuedChild(parent, 1, nil) // the sibling's $5.10 is untouched
			g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, roomA, consumer, roomB, spentLedger(map[string]int64{runs.PrincipalFactory: tc.spent}))
			g.r.Cfg.Current().Budgets = personBudgets(true, true)
			g.r.Cfg.Current().Budgets.EnforceTask = false // the allocation holds whatever the task cap's mode
			g.f.SetPR(prA)
			if contract.Spec.Budget.TaskMicroUSD != 5_100_000 || consumer.Spec.Budget.TaskMicroUSD != 5_100_000 {
				t.Fatalf("caps %d %d", contract.Spec.Budget.TaskMicroUSD, consumer.Spec.Budget.TaskMicroUSD)
			}
			got := g.reconcile(t, contract.Name, 1)
			if got.Status.Phase != tc.phase || got.Status.Reason != tc.reason {
				t.Fatalf("%s/%s, want %s/%s (spent %d)", got.Status.Phase, got.Status.Reason, tc.phase, tc.reason, coordination.AllocationSpent(got))
			}
			next := runID(contract.Name, 1)
			_, ran := g.runs.specs[next]
			if ran != (tc.phase == v1alpha1.PhaseImplementing) {
				t.Fatalf("the next run started %v", ran)
			}
			if ran {
				want := ledger.Reservation{Principal: runs.PrincipalFactory, Person: "github:smana", MaxMicroUSD: runReserve}
				if r := reservationOf(t, g, next); r != want {
					t.Fatalf("reservation %+v, want %+v", r, want)
				}
			}
		})
	}
}

// MR8, O7/O8: two independent children are both admitted and run at once, each on its own
// allocation, each run reserved once in the day ledger under the fleet and its own maintainer.
func TestIndependentChildrenRunConcurrently(t *testing.T) {
	p := coordinationPlan(t, true)
	g, _ := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonFiled))
	g.r.Cfg.Current().Budgets = personBudgets(true, true)
	cs := g.reconcile(t, p.ID, 2).Status.Coordination
	if cs.Children[0].Task == "" || cs.Children[1].Task == "" {
		t.Fatalf("children %+v", cs.Children)
	}
	persons := []string{"github:smana", "github:other-maintainer"}
	var objs []client.Object
	for i, st := range cs.Children {
		tk := g.reconcile(t, st.Task, 3)
		if tk.Status.Phase != v1alpha1.PhaseImplementing || tk.Spec.Budget.TaskMicroUSD != p.Children[i].BudgetMicroUSD {
			t.Fatalf("%s: %s/%s cap %d", st.ID, tk.Status.Phase, tk.Status.Reason, tk.Spec.Budget.TaskMicroUSD)
		}
		objs = append(objs, tk)
	}
	if len(g.runs.specs) != 2 {
		t.Fatalf("%d live runs, want both children's", len(g.runs.specs))
	}
	for i, o := range objs {
		tk := o.(*v1alpha1.Task)
		id := runID(tk.Name, 0)
		if r, ok := g.runs.runs[id]; !ok || runs.Terminal(r.Phase) {
			t.Fatalf("%s's run is not live: %+v", tk.Name, r)
		}
		want := ledger.Reservation{Principal: runs.PrincipalFactory, Person: persons[i], MaxMicroUSD: runReserve}
		if r := reservationOf(t, g, id); r != want {
			t.Fatalf("%s reserved %+v, want %+v", tk.Name, r, want)
		}
		if left := coordination.AllocationLeft(tk); left != p.Children[i].BudgetMicroUSD-2*runReserve {
			t.Fatalf("%s left %d: its live run holds its own allocation only", tk.Name, left)
		}
	}
}
