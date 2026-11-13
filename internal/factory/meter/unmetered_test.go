// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/runs"
)

// Review #518, the backstop: under runs.gatewayIdentity a run that succeeded and still has no
// gateway series once endSettle has passed since it finished was never metered (a run-controller in
// model.mode secret, a scrape that misses the gateway). Its cost is unpriced, the fallback verdict,
// never $0; the model is named once. Within endSettle its series may still land (a scrape interval
// plus VictoriaMetrics' latency offset), so nothing is said yet; without the identity, or while the
// run is live, nothing changes.
func TestASucceededRunWithNoGatewaySeriesIsUnpriced(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		identity bool
		phase    string
		finished time.Time
		want     string
	}{
		"identity, succeeded past endSettle":   {true, "Succeeded", now.Add(-endSettle), FallbackUnpriced},
		"identity, succeeded within endSettle": {true, "Succeeded", now.Add(-endSettle + time.Second), ""},
		"identity, succeeded, no finishedAt":   {true, "Succeeded", time.Time{}, ""},
		"identity, still live":                 {true, "Running", time.Time{}, ""},
		"no identity, succeeded":               {false, "Succeeded", now.Add(-time.Hour), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := standardRun("aaaaaaaa")
			r.Phase, r.Finished = c.phase, c.finished
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
			var revoked []revocation
			var fell, named []string
			m := usdMeter(st, typed{}, &revoked, &fell)
			m.GatewayIdentity, m.Now = c.identity, func() time.Time { return now }
			m.Costs = &Costs{}
			m.OnUnpriced = func(_ context.Context, model string) { named = append(named, model) }
			for range 2 {
				if err := m.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := m.Costs.Get("aaaaaaaa")
			if got.Why != c.want || got.MicroUSD != 0 {
				t.Fatalf("cost %+v, want verdict %q", got, c.want)
			}
			if want := c.want != ""; want != slices.Equal(named, []string{"agent-default"}) {
				t.Fatalf("named %v: want the claim's model once exactly when unpriced", named)
			}
		})
	}
}

// A short run (a judge, a scoper) succeeds before its series reach the meter: the tick that reads it
// Succeeded with no series says nothing, and the next tick, which sees its series, prices it.
func TestAShortRunsLateSeriesArePriced(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r := standardRun("aaaaaaaa")
	r.Phase, r.Finished = "Succeeded", now.Add(-5*time.Second)
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
	src := typed{}
	var revoked []revocation
	var fell, named []string
	m := usdMeter(st, src, &revoked, &fell)
	m.GatewayIdentity, m.Now = true, func() time.Time { return now }
	m.Costs = &Costs{}
	m.OnUnpriced = func(_ context.Context, model string) { named = append(named, model) }
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	src["aaaaaaaa"] = typedUsage(Usage{Input: 100_000, CacheRead: 50_000, Output: 1_000})
	now = now.Add(30 * time.Second)
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Costs.Get("aaaaaaaa")
	if got.Why != "" || got.MicroUSD <= 0 || len(named) != 0 {
		t.Fatalf("cost %+v, named %v: want the run priced, and no model named unpriced", got, named)
	}
}
