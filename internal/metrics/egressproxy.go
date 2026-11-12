// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// The egress proxy's metrics (v4 D6). decision is allow, refuse or error: bounded.
const (
	EgressProxyConnections = "maku_egress_proxy_connections_total"
	EgressProxyBytes       = "maku_egress_proxy_bytes_total"
)

// NewEgressProxyMetrics registers both on reg and returns the setters egressproxy.Proxy takes.
func NewEgressProxyMetrics(reg prometheus.Registerer) (count func(decision string), bytes func(n int64), err error) {
	conns := prometheus.NewCounterVec(prometheus.CounterOpts{Name: EgressProxyConnections,
		Help: "CONNECT requests the egress proxy decided, by decision (allow, refuse, error)."}, []string{"decision"})
	b := prometheus.NewCounter(prometheus.CounterOpts{Name: EgressProxyBytes,
		Help: "Bytes the egress proxy tunnelled, both directions, counted when a tunnel closes."})
	for _, c := range []prometheus.Collector{conns, b} {
		if err := reg.Register(c); err != nil {
			return nil, nil, fmt.Errorf("metrics: egress proxy: %w", err)
		}
	}
	for _, d := range []string{"allow", "refuse", "error"} {
		conns.WithLabelValues(d) // every decision is exported from the start, at 0
	}
	return func(d string) { conns.WithLabelValues(d).Inc() }, func(n int64) { b.Add(float64(n)) }, nil
}
