// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// MR8 with the spec's exact $12.75: planning, both children and every integration attempt are
// allocated, never more; an integration attempt is one run at its own cap, at most maxRuns.
func TestCoordinationFamilyBudget(t *testing.T) {
	p := validPlan(t)
	if got, ok := Allocated(p); !ok || got != 12_750_000 || got != p.Budget.TotalMicroUSD {
		t.Fatalf("allocated %d %v, want the confirmed 12750000", got, ok)
	}
	if b := IntegrationBudget(p); b.RunMicroUSD != 510_000 || b.TaskMicroUSD != 510_000 {
		t.Fatalf("integration budget %+v", b)
	}
	cs := &v1alpha1.CoordinationStatus{Plan: &p}
	for i := range int(p.Integration.MaxRuns) {
		if err := IntegrationAttemptAllowed(cs); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		cs.IntegrationAttempts = append(cs.IntegrationAttempts, v1alpha1.IntegrationAttempt{Task: "aaaaaaaa", State: "failed"})
	}
	if err := IntegrationAttemptAllowed(cs); !errors.Is(err, ErrIntegrationRunsExhausted) || Reason(err) != "integration_runs_exhausted" {
		t.Fatalf("attempt past maxRuns: %v", err)
	}
}

// A child allocation is its own: settled spend plus the next run's whole reservation must fit it.
// A live run counts its whole reservation, and an unpriced run its tokens at the reference rate,
// never $0.
func TestCoordinationAllocationLeftBudget(t *testing.T) {
	b := v1alpha1.Budget{RunTokens: 1_500_000, TaskMicroUSD: 5_100_000} // a run reserves 2.55 USD
	for _, tc := range []struct {
		name string
		runs []v1alpha1.RunRecord
		left int64
	}{
		{name: "no run yet", left: 2_550_000},
		{name: "a settled run", runs: []v1alpha1.RunRecord{{ID: "aaaaaaaa", Phase: "Succeeded", CostMicroUSD: 1_000_000}}, left: 1_550_000},
		{name: "exactly a run left", runs: []v1alpha1.RunRecord{{ID: "aaaaaaaa", Phase: "Succeeded", CostMicroUSD: 2_550_000}}, left: 0},
		{name: "a live run holds its reservation", runs: []v1alpha1.RunRecord{{ID: "aaaaaaaa", Phase: "Running", CostMicroUSD: 10}}, left: 0},
		{name: "an unpriced run is never $0", runs: []v1alpha1.RunRecord{{ID: "aaaaaaaa", Phase: "Succeeded", Unpriced: "unpriced", Tokens: 1_500_001}}, left: -2},
		{name: "tokens with no cost are unpriced", runs: []v1alpha1.RunRecord{{ID: "aaaaaaaa", Phase: "Failed", Tokens: 2_000_000}}, left: -850_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Budget: b}, Status: v1alpha1.TaskStatus{Runs: tc.runs}}
			if got := AllocationLeft(task); got != tc.left {
				t.Fatalf("left %d, want %d", got, tc.left)
			}
		})
	}
}

// An edit may not reduce a child's allocation below what its Task has settled plus what its live
// run holds, and an increase needs the parent's and that child's maintainer's new confirmations.
func TestCoordinationBudgetEditsRefuse(t *testing.T) {
	ctx := context.Background()
	p := draftPlan(t)
	spent := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "c0nsumer", Namespace: ns},
		Spec: v1alpha1.TaskSpec{Repository: repoB, Budget: v1alpha1.Budget{RunTokens: 1_500_000, TaskMicroUSD: 5_100_000}},
		Status: v1alpha1.TaskStatus{Runs: []v1alpha1.RunRecord{
			{ID: "aaaaaaaa", Phase: "Succeeded", CostMicroUSD: 1_000_000}, {ID: "bbbbbbbb", Phase: "Running"}}}} // 1.00 + 2.55 held
	for _, tc := range []struct {
		name   string
		budget int64
		want   error
	}{
		{name: "below settled plus reserved", budget: 3_549_999, want: ErrAllocationBelowSpend},
		{name: "exactly settled plus reserved", budget: 3_550_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testService(t, config.CoordinationPlan, []client.Object{spent.DeepCopy()}...)
			mustDraft(t, s, p)
			task := parentOf(t, s, p.ID)
			task.Status.Coordination.Children = []v1alpha1.CoordinationChildStatus{{ID: "consumer", Repository: repoB, Task: "c0nsumer", Generation: 1}}
			if err := s.Client.Status().Update(ctx, &task); err != nil {
				t.Fatal(err)
			}
			q := *p.DeepCopy()
			q.Version = 2
			child(t, &q, "consumer").BudgetMicroUSD = tc.budget
			_, err := s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"})
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	s, _ := testService(t, config.CoordinationPlan)
	mustDraft(t, s, p)
	for _, c := range []struct {
		a  Actor
		in CoordinationConfirmation
	}{{smana, parentConfirm(p)}, {smana, childConfirm(p, "contract")}, {other, childConfirm(p, "consumer")}} {
		if _, err := s.Confirm(ctx, c.a, c.in); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "contract").BudgetMicroUSD = 5_200_000 // above the task ceiling: an increase
	q.Budget.TotalMicroUSD = 12_850_000
	if _, err := s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
		t.Fatal(err)
	}
	cs := parentOf(t, s, p.ID).Status.Coordination
	if Complete(cs) || parentConfirmation(cs) != nil {
		t.Fatal("an increase kept the parent's confirmation")
	}
	for _, a := range cs.Authorizations {
		if a.ChildID == "contract" {
			t.Fatal("an increase kept the increased child's authorization")
		}
	}
	if cs.Budget.AllocatedMicroUSD != 12_850_000 {
		t.Fatalf("allocated %d", cs.Budget.AllocatedMicroUSD)
	}
}
