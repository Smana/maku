// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPreflightGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	set, err := NewPreflightGauge(reg)
	if err != nil {
		t.Fatal(err)
	}
	head := "# HELP maku_run_controller_preflight_passed 1 while the run-controller's preflight check passes, 0 while it refuses new runs.\n" +
		"# TYPE maku_run_controller_preflight_passed gauge\n"
	for _, tc := range []struct {
		ok   bool
		want string
	}{{false, "0"}, {true, "1"}} {
		set("runtimeClass", tc.ok)
		exp := head + `maku_run_controller_preflight_passed{check="runtimeClass"} ` + tc.want + "\n"
		if err := testutil.GatherAndCompare(reg, strings.NewReader(exp), PreflightPassed); err != nil {
			t.Fatal(err)
		}
	}
}
