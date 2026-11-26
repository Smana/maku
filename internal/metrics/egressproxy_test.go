// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEgressProxyRefusalsByReason(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, _, refused, err := NewEgressProxyMetrics(reg, []string{"not_allowed", "install_closed"})
	if err != nil {
		t.Fatal(err)
	}
	refused("install_closed")
	want := `
# HELP maku_egress_proxy_refusals_total Requests the egress proxy refused, by reason; install_closed is a run reaching its install set after it closed.
# TYPE maku_egress_proxy_refusals_total counter
maku_egress_proxy_refusals_total{reason="install_closed"} 1
maku_egress_proxy_refusals_total{reason="not_allowed"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), EgressProxyRefusals); err != nil {
		t.Fatal(err)
	}
}
