// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// Review #518: a run the factory meters by its gateway identity (spec.meteredBy gateway) is refused
// by a run-controller in model.mode secret, where it would make no ar_agent series and end at $0,
// as spec.dataClass internal is; model.mode gateway starts it.
func TestMeteredByGatewayNeedsGatewayMode(t *testing.T) {
	metered := func(r *v1alpha1.AgentRun) { r.Spec.MeteredBy = "gateway" }
	got := testConfig(t).Invalid(goRun(metered))
	if !strings.Contains(got, "spec.meteredBy gateway needs model.mode gateway") {
		t.Errorf("secret mode: %q", got)
	}
	if got := testConfig(t).Invalid(goRun()); strings.Contains(got, "meteredBy") {
		t.Errorf("secret mode, an unstamped run: %q", got)
	}
	if got := gatewayTestConfig(t).Invalid(gatewayRun(metered)); got != "" {
		t.Errorf("gateway mode refuses a metered run: %q", got)
	}
}
