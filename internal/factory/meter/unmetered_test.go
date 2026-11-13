// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/factory/runs"
)

// Review #518, the backstop: under runs.gatewayIdentity a run that succeeded with no gateway series
// was never metered (a run-controller in model.mode secret, a scrape that misses the gateway). Its
// cost is unpriced, the fallback verdict, never $0; the model is named once. Without the identity,
// or while the run is live, nothing changes.
func TestASucceededRunWithNoGatewaySeriesIsUnpriced(t *testing.T) {
	for name, c := range map[string]struct {
		identity bool
		phase    string
		want     string
	}{
		"identity, succeeded":    {true, "Succeeded", FallbackUnpriced},
		"identity, still live":   {true, "Running", ""},
		"no identity, succeeded": {false, "Succeeded", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := standardRun("aaaaaaaa")
			r.Phase = c.phase
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
			var revoked []revocation
			var fell, named []string
			m := usdMeter(st, typed{}, &revoked, &fell)
			m.GatewayIdentity = c.identity
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
