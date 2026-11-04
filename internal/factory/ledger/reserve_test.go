// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/runs"
)

// #78: four factory runs reserving at once on an enforced day with room for two: exactly two
// land, whatever the interleaving; in shadow all four land and the over ones say so.
func TestConcurrentReservationsAdmitOnlyWhatFits(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		l := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: Prefix + "20260927", Namespace: "agent-system"}}
		if err := SetSpentColumn(l, map[string]int64{runs.PrincipalFactory: 22_000_000}); err != nil {
			t.Fatal(err)
		}
		c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(l).Build()
		if err := StampMeter(t.Context(), c, "agent-system", now, time.Hour); err != nil {
			t.Fatal(err)
		}
		var (
			wg                 sync.WaitGroup
			mu                 sync.Mutex
			landed, over, busy int
		)
		for _, id := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd"} {
			wg.Go(func() {
				for { // a caller retries ErrBusy: the reconciler's requeue, the API's 503
					o, err := Reserve(t.Context(), c, "agent-system", now, id,
						Reservation{Principal: runs.PrincipalFactory, MaxMicroUSD: 1_500_000}, 25_000_000, 0, enforce)
					mu.Lock()
					switch {
					case errors.Is(err, ErrBusy):
						busy++
						mu.Unlock()
						continue
					case err == nil:
						landed++
					case !errors.Is(err, ErrOverBudget):
						t.Error(err)
					}
					if o != "" {
						over++
					}
					mu.Unlock()
					return
				}
			})
		}
		wg.Wait()
		used, err := DayUsed(t.Context(), c, "agent-system", runs.PrincipalFactory, now)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case enforce && (landed != 2 || over != 2 || used != 25_000_000):
			t.Fatalf("enforced: %d landed, %d over, used %d (%d busy)", landed, over, used, busy)
		case !enforce && (landed != 4 || over != 2 || used != 28_000_000):
			t.Fatalf("shadow: %d landed, %d over, used %d (%d busy)", landed, over, used, busy)
		}
	}
}
