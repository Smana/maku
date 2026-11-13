// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Review #518: an ended run's charge against a USD cap is its priced cost, an unpriced run's tokens
// at the reference rate, and an unpriced run with no tokens at all (the meter's backstop: a run that
// succeeded with no gateway series) its whole reservation, never $0, so the caps stay bounded.
func TestRunCharge(t *testing.T) {
	usdBudget := v1alpha1.Budget{RunMicroUSD: 2_550_000, RunTokens: 1_500_000}
	tokenBudget := v1alpha1.Budget{RunTokens: 1_500_000}
	for name, c := range map[string]struct {
		b    v1alpha1.Budget
		rec  v1alpha1.RunRecord
		want int64
	}{
		"priced":                               {usdBudget, v1alpha1.RunRecord{CostMicroUSD: 700_000, Tokens: 400_000}, 700_000},
		"unpriced, with tokens":                {usdBudget, v1alpha1.RunRecord{Unpriced: "unpriced", Tokens: 100_000}, config.RefMicroUSD(100_000)},
		"tokens, no cost":                      {usdBudget, v1alpha1.RunRecord{Tokens: 100_000}, config.RefMicroUSD(100_000)},
		"unpriced, no tokens: the USD run cap": {usdBudget, v1alpha1.RunRecord{Unpriced: "unpriced"}, 2_550_000},
		"unpriced, no tokens: the token cap":   {tokenBudget, v1alpha1.RunRecord{Unpriced: "unpriced"}, config.RefMicroUSD(1_500_000)},
		"nothing spent, priced at nothing":     {usdBudget, v1alpha1.RunRecord{}, 0},
	} {
		if got := RunCharge(c.b, c.rec); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestAllocationSpentChargesAnUnmeteredRunItsReservation(t *testing.T) {
	task := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Budget: v1alpha1.Budget{RunMicroUSD: 2_550_000, TaskMicroUSD: 10_000_000}},
		Status: v1alpha1.TaskStatus{Runs: []v1alpha1.RunRecord{{Phase: "Succeeded", Unpriced: "unpriced"}}}}
	if got := AllocationSpent(task); got != 2_550_000 {
		t.Fatalf("spent %d, want the run's 2550000 reservation", got)
	}
}
