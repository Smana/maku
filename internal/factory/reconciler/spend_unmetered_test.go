// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Review #518: a family's cap charges an unpriced run with no tokens (the meter's backstop, a run
// that succeeded with no gateway series) its whole reservation, never $0; a priced run its cost and
// an unpriced one with tokens the reference rate, as before.
func TestSpendChargesAnUnmeteredRunItsReservation(t *testing.T) {
	task := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Budget: v1alpha1.Budget{RunMicroUSD: 2_550_000}},
		Status: v1alpha1.TaskStatus{Runs: []v1alpha1.RunRecord{
			{ID: "a", CostMicroUSD: 700_000, Tokens: 400_000},
			{ID: "b", Unpriced: "unpriced", Tokens: 100_000},
			{ID: "c", Unpriced: "unpriced"},
		}}}
	if got, want := spend(task), 700_000+config.RefMicroUSD(100_000)+2_550_000; got != want {
		t.Fatalf("spend %d, want %d", got, want)
	}
}
