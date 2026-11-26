// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// The egress proxy's metrics (v4 D6). decision is allow, refuse or error; reason is one of the
// proxy's refusal reasons: both bounded.
const (
	EgressProxyConnections = "maku_egress_proxy_connections_total"
	EgressProxyBytes       = "maku_egress_proxy_bytes_total"
	EgressProxyRefusals    = "maku_egress_proxy_refusals_total"
)

// NewEgressProxyMetrics registers the three on reg and returns the setters egressproxy.Proxy takes.
// reasons are the refusal reasons, each exported at 0 from the start.
func NewEgressProxyMetrics(reg prometheus.Registerer, reasons []string) (count func(decision string), bytes func(n int64), refused func(reason string), err error) {
	conns := prometheus.NewCounterVec(prometheus.CounterOpts{Name: EgressProxyConnections,
		Help: "CONNECT requests the egress proxy decided, by decision (allow, refuse, error)."}, []string{"decision"})
	b := prometheus.NewCounter(prometheus.CounterOpts{Name: EgressProxyBytes,
		Help: "Bytes the egress proxy tunnelled, both directions, counted when a tunnel closes."})
	refusals := prometheus.NewCounterVec(prometheus.CounterOpts{Name: EgressProxyRefusals,
		Help: "Requests the egress proxy refused, by reason; install_closed is a run reaching its install set after it closed."}, []string{"reason"})
	for _, c := range []prometheus.Collector{conns, b, refusals} {
		if err := reg.Register(c); err != nil {
			return nil, nil, nil, fmt.Errorf("metrics: egress proxy: %w", err)
		}
	}
	for _, d := range []string{"allow", "refuse", "error"} {
		conns.WithLabelValues(d) // every decision is exported from the start, at 0
	}
	for _, r := range reasons {
		refusals.WithLabelValues(r)
	}
	return func(d string) { conns.WithLabelValues(d).Inc() }, func(n int64) { b.Add(float64(n)) },
		func(r string) { refusals.WithLabelValues(r).Inc() }, nil
}
