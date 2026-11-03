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

const alice = "github:alice"

// D3: one run, one entry, both keys. The fleet and the person each count it, and the ledger took
// one write.
func TestOneEntryChargesThePersonAndTheFleet(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(ledgerWith(nil)).Build()
	rv := readLedger(t, c).ResourceVersion
	mine := Reservation{Principal: runs.PrincipalFactory, Person: alice, MaxMicroUSD: 1_500_000}
	if over, err := Reserve(t.Context(), c, "agent-system", now, "aaaaaaaa", mine, 25_000_000, 5_000_000, false); err != nil || over != "" {
		t.Fatal(over, err)
	}
	cm := readLedger(t, c)
	if cm.Data[Reserved+"aaaaaaaa"] != `{"principal":"system:factory","person":"github:alice","maxMicroUSD":1500000}` {
		t.Fatalf("one entry naming both: %v", cm.Data)
	}
	if rvNum(t, cm.ResourceVersion) != rvNum(t, rv)+1 {
		t.Fatalf("one write: %s → %s", rv, cm.ResourceVersion)
	}
	for _, p := range []string{runs.PrincipalFactory, alice} {
		if used, err := DayUsed(t.Context(), c, "agent-system", p, now); err != nil || used != 1_500_000 {
			t.Fatalf("%s: %d %v", p, used, err)
		}
	}
}

func rvNum(t *testing.T, rv string) int {
	t.Helper()
	n := 0
	for _, r := range rv {
		n = n*10 + int(r-'0')
	}
	return n
}

// D4/D5: the person's day refuses before the fleet's, so the refusal names whose cap it is; in
// shadow both are reported and the entry lands.
func TestThePersonDayRefusesFirst(t *testing.T) {
	for name, c := range map[string]struct {
		spent   map[string]int64
		enforce bool
		err     error
		over    string
	}{
		"both fit":                {spent: map[string]int64{alice: 3_000_000}, enforce: true},
		"person over":             {spent: map[string]int64{alice: 4_000_000}, enforce: true, err: ErrPersonOverBudget, over: alice},
		"fleet over":              {spent: map[string]int64{runs.PrincipalFactory: 24_000_000}, enforce: true, err: ErrOverBudget, over: runs.PrincipalFactory},
		"both over, person named": {spent: map[string]int64{alice: 5_000_000, runs.PrincipalFactory: 25_000_000}, enforce: true, err: ErrPersonOverBudget, over: alice},
		"person over, shadow":     {spent: map[string]int64{alice: 4_000_000}, over: alice},
		"another person's spend":  {spent: map[string]int64{"github:bob": 5_000_000}, enforce: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := ledgerWith(nil)
			if err := SetSpentColumn(l, c.spent); err != nil {
				t.Fatal(err)
			}
			k := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(l).Build()
			if err := StampMeter(t.Context(), k, "agent-system", now, time.Hour); err != nil {
				t.Fatal(err)
			}
			over, err := Reserve(t.Context(), k, "agent-system", now, "aaaaaaaa",
				Reservation{Principal: runs.PrincipalFactory, Person: alice, MaxMicroUSD: 1_500_000}, 25_000_000, 5_000_000, c.enforce)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) || over != c.over {
				t.Fatalf("over %q err %v", over, err)
			}
			_, held := readLedger(t, k).Data[Reserved+"aaaaaaaa"]
			if held != (c.err == nil) {
				t.Fatalf("a refused admission reserves nothing, an admitted one lands: held %v", held)
			}
		})
	}
}

// The gate: label-triggered and API admission share one reservation. A task run (the fleet's
// entry charged to alice) and alice's API runs (the same shape, in a room) race on a day with
// room for exactly two of alice's runs: two land whatever the interleaving, the person's day is
// never overshot, and in shadow all land and the over ones say whose day.
func TestLabelAndAPIAdmissionsShareThePersonDay(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		l := ledgerWith(nil)
		if err := SetSpentColumn(l, map[string]int64{alice: 2_000_000}); err != nil {
			t.Fatal(err)
		}
		c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(l).Build()
		if err := StampMeter(t.Context(), c, "agent-system", now, time.Hour); err != nil {
			t.Fatal(err)
		}
		entries := map[string]Reservation{
			"aaaaaaaa": {Principal: runs.PrincipalFactory, Person: alice, MaxMicroUSD: 1_500_000},                   // the label's task run
			"bbbbbbbb": {Principal: runs.PrincipalFactory, Person: alice, Room: "3kq7x2ma", MaxMicroUSD: 1_500_000}, // API, room A
			"cccccccc": {Principal: runs.PrincipalFactory, Person: alice, Room: "4mz8y3nb", MaxMicroUSD: 1_500_000}, // API, room B
			"dddddddd": {Principal: runs.PrincipalFactory, Person: alice, MaxMicroUSD: 1_500_000},                   // a retry of another task
		}
		var (
			wg           sync.WaitGroup
			mu           sync.Mutex
			landed, over int
		)
		for id, mine := range entries {
			wg.Go(func() {
				for {
					o, err := Reserve(t.Context(), c, "agent-system", now, id, mine, 25_000_000, 5_000_000, enforce)
					if errors.Is(err, ErrBusy) {
						continue
					}
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						landed++
					case !errors.Is(err, ErrPersonOverBudget):
						t.Error(err)
					}
					if o == alice {
						over++
					} else if o != "" {
						t.Errorf("over %q", o)
					}
					return
				}
			})
		}
		wg.Wait()
		used, err := DayUsed(t.Context(), c, "agent-system", alice, now)
		if err != nil {
			t.Fatal(err)
		}
		fleet, err := DayUsed(t.Context(), c, "agent-system", runs.PrincipalFactory, now)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case enforce && (landed != 2 || over != 2 || used != 5_000_000 || fleet != 3_000_000):
			t.Fatalf("enforced: %d landed, %d over, alice %d, fleet %d", landed, over, used, fleet)
		case !enforce && (landed != 4 || over != 2 || used != 8_000_000 || fleet != 6_000_000):
			t.Fatalf("shadow: %d landed, %d over, alice %d, fleet %d", landed, over, used, fleet)
		}
	}
}

// An entry that names its principal as its person counts it once.
func TestAPersonWhoIsThePrincipalCountsOnce(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Data: map[string]string{
		Reserved + "aaaaaaaa": `{"principal":"github:alice","person":"github:alice","maxMicroUSD":7}`}}
	r, err := ledgerReservations(cm)
	if err != nil {
		t.Fatal(err)
	}
	if got := dayUsed(nil, r, alice); got != 7 {
		t.Fatal(got)
	}
}
