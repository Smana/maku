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

// REL-M7: Branch exclusion refuses a second live reservation on the same repository and branch
// (matching repository case-insensitively), while allowing different branches or repositories.
func TestReserveBranchExclusion(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme()).Build()
	r1 := Reservation{Principal: runs.PrincipalFactory, Repository: "Org/Repo", Branch: "feat/fix", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run1", r1, 10_000_000, 0, false); err != nil {
		t.Fatal(err)
	}
	// Same repo (different case) and same branch -> ErrBranchBusy.
	r2 := Reservation{Principal: runs.PrincipalFactory, Repository: "org/repo", Branch: "feat/fix", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run2", r2, 10_000_000, 0, false); !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("got %v, want ErrBranchBusy", err)
	}
	// Idempotent retry of run1 passes.
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run1", r1, 10_000_000, 0, false); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	// Same repo, different branch -> allowed.
	r3 := Reservation{Principal: runs.PrincipalFactory, Repository: "org/repo", Branch: "feat/other", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run3", r3, 10_000_000, 0, false); err != nil {
		t.Fatalf("different branch refused: %v", err)
	}
	// Different repo, same branch -> allowed.
	r4 := Reservation{Principal: runs.PrincipalFactory, Repository: "org/other", Branch: "feat/fix", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run4", r4, 10_000_000, 0, false); err != nil {
		t.Fatalf("different repo refused: %v", err)
	}
	// Releasing run1 unblocks run2.
	if err := Release(t.Context(), c, "agent-system", now, "run1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(t.Context(), c, "agent-system", now, "run2", r2, 10_000_000, 0, false); err != nil {
		t.Fatalf("after release, got %v", err)
	}
}

// REL-M8 & REL-M7: ErrRoomBusy and ErrBranchBusy hold across the 00:00 UTC boundary by checking
// yesterday's ledger ConfigMap, and clear once yesterday's reservation is released.
func TestReserveExclusionsAcrossUTCMidnight(t *testing.T) {
	beforeMidnight := time.Date(2026, 9, 27, 23, 59, 0, 0, time.UTC)
	afterMidnight := time.Date(2026, 9, 28, 0, 1, 0, 0, time.UTC)

	c := fake.NewClientBuilder().WithScheme(scheme()).Build()
	r1 := Reservation{Principal: runs.PrincipalFactory, Room: "room-1", Repository: "org/repo", Branch: "feat/x", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", beforeMidnight, "run1", r1, 10_000_000, 0, false); err != nil {
		t.Fatal(err)
	}
	// Idempotent retry of run1 right after midnight passes without duplicating.
	if _, err := Reserve(t.Context(), c, "agent-system", afterMidnight, "run1", r1, 10_000_000, 0, false); err != nil {
		t.Fatalf("idempotent retry across midnight failed: %v", err)
	}
	// Room conflict across midnight.
	rRoom := Reservation{Principal: runs.PrincipalFactory, Room: "room-1", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", afterMidnight, "run2", rRoom, 10_000_000, 0, false); !errors.Is(err, ErrRoomBusy) {
		t.Fatalf("room across midnight: got %v, want ErrRoomBusy", err)
	}
	// Branch conflict across midnight.
	rBranch := Reservation{Principal: runs.PrincipalFactory, Repository: "Org/Repo", Branch: "feat/x", MaxMicroUSD: 1_000_000}
	if _, err := Reserve(t.Context(), c, "agent-system", afterMidnight, "run3", rBranch, 10_000_000, 0, false); !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("branch across midnight: got %v, want ErrBranchBusy", err)
	}
	// Releasing run1 from yesterday's ledger unblocks both room and branch on today's ledger.
	if err := Release(t.Context(), c, "agent-system", beforeMidnight, "run1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(t.Context(), c, "agent-system", afterMidnight, "run2", rRoom, 10_000_000, 0, false); err != nil {
		t.Fatalf("room after release: %v", err)
	}
	if _, err := Reserve(t.Context(), c, "agent-system", afterMidnight, "run3", rBranch, 10_000_000, 0, false); err != nil {
		t.Fatalf("branch after release: %v", err)
	}
}
