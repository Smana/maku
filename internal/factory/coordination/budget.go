// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
)

// The fixed-allocation refusals (MR8).
var (
	// ErrAllocationBelowSpend is an edit reducing a child's allocation below what its Task has
	// settled plus what its live run still holds.
	ErrAllocationBelowSpend = errors.New("allocation_below_spend")
	// ErrIntegrationRunsExhausted is an integration attempt past integration.maxRuns.
	ErrIntegrationRunsExhausted = errors.New("integration_runs_exhausted")
)

// Allocated is p's static allocations, checked: planning + every child + maxRuns × runMicroUSD.
// ok is false on overflow; Validate refuses such a plan, and the sum within the total.
func Allocated(p v1alpha1.CoordinationPlan) (int64, bool) {
	sum, ok := mul(int64(p.Integration.MaxRuns), p.Integration.RunMicroUSD)
	ok = ok && add(&sum, p.Budget.PlanningMicroUSD)
	for _, c := range p.Children {
		ok = ok && add(&sum, c.BudgetMicroUSD)
	}
	return sum, ok
}

// IntegrationBudget is one integration attempt's caps: a single run of integration.runMicroUSD,
// which is also the attempt's whole task cap, so a retry is a new attempt counted against maxRuns.
func IntegrationBudget(p v1alpha1.CoordinationPlan) v1alpha1.Budget {
	return v1alpha1.Budget{RunMicroUSD: p.Integration.RunMicroUSD, TaskMicroUSD: p.Integration.RunMicroUSD}
}

// IntegrationAttemptAllowed refuses an integration attempt past the plan's maxRuns: a failed
// attempt still consumes one.
func IntegrationAttemptAllowed(cs *v1alpha1.CoordinationStatus) error {
	if cs == nil || cs.Plan == nil || len(cs.IntegrationAttempts) >= int(cs.Plan.Integration.MaxRuns) {
		return refuse(ErrIntegrationRunsExhausted, "integration.maxRuns", "every attempt is spent")
	}
	return nil
}

// runReservation is what one run of b may spend, as the day ledger reserves it: its USD run cap,
// else its token cap at the reference rate.
func runReservation(b v1alpha1.Budget) int64 {
	if b.RunMicroUSD > 0 {
		return b.RunMicroUSD
	}
	return config.RefMicroUSD(b.RunTokens)
}

// AllocationSpent is what t's runs hold of its lifetime allocation: each ended run's priced cost,
// an unpriced one's tokens at the reference rate (never $0), and a live run's whole reservation,
// which the meter may still spend.
func AllocationSpent(t *v1alpha1.Task) int64 {
	var spent int64
	for _, r := range t.Status.Runs {
		c := r.CostMicroUSD
		if r.Unpriced != "" || (r.Tokens > 0 && c == 0) {
			c = max(c, config.RefMicroUSD(r.Tokens))
		}
		if !runs.Terminal(r.Phase) {
			c = max(c, runReservation(t.Spec.Budget))
		}
		spent += c
	}
	return spent
}

// AllocationLeft is what t's own lifetime allocation (spec.budget.taskMicroUSD, a child's
// confirmed budgetMicroUSD) still holds after its spend and the next run's whole reservation:
// negative when the next run does not fit (MR8). It reads t alone, so no sibling's unspent
// allocation is ever borrowed.
func AllocationLeft(t *v1alpha1.Task) int64 {
	return t.Spec.Budget.TaskMicroUSD - AllocationSpent(t) - runReservation(t.Spec.Budget)
}

// checkReductions refuses next reducing a child's allocation below its current Task's spend.
func (s *Service) checkReductions(ctx context.Context, old *v1alpha1.CoordinationStatus, next v1alpha1.CoordinationPlan) error {
	for _, st := range old.Children {
		c, ok := find(next, st.ID)
		if !ok || st.Task == "" {
			continue
		}
		var t v1alpha1.Task
		switch err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: st.Task}, &t); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return err
		}
		if spent := AllocationSpent(&t); c.BudgetMicroUSD < spent {
			return refuse(ErrAllocationBelowSpend, "children."+c.ID+".budgetMicroUSD", "below the %d already spent or held", spent)
		}
	}
	return nil
}
