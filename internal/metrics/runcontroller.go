// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// PreflightPassed is the run-controller's preflight gauge (v4 D18): 1 while a check passes, 0 while
// it refuses runs. check is one of a fixed set (runtimeClass, then network), so the label stays bounded.
const PreflightPassed = "maku_run_controller_preflight_passed" //nolint:gosec // a metric name, not a credential

// NewPreflightGauge registers PreflightPassed on reg, the controller-runtime registry the
// run-controller serves on :9090, and returns the setter runctl.Preflight.Passed takes.
func NewPreflightGauge(reg prometheus.Registerer) (func(check string, ok bool), error) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: PreflightPassed,
		Help: "1 while the run-controller's preflight check passes, 0 while it refuses new runs."}, []string{"check"})
	if err := reg.Register(g); err != nil {
		return nil, fmt.Errorf("metrics: %s: %w", PreflightPassed, err)
	}
	return func(check string, ok bool) {
		v := 0.0
		if ok {
			v = 1
		}
		g.WithLabelValues(check).Set(v)
	}, nil
}
