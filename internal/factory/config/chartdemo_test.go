// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"testing"
)

// The umbrella chart's profile: demo renders a factory config; charts/agent-factory/tests/render.sh
// writes it to the path in CHART_DEMO_FACTORY_CONFIG and runs this test, so the chart can never
// drift from the rules Parse enforces. Skipped when the variable is unset.
func TestRenderedDemoChartConfigParses(t *testing.T) {
	path := os.Getenv("CHART_DEMO_FACTORY_CONFIG")
	if path == "" {
		t.Skip("CHART_DEMO_FACTORY_CONFIG is not set: render.sh sets it")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatalf("the umbrella chart's demo config: %v", err)
	}
	if !c.Demo {
		t.Fatal("the rendered config is not demo: true")
	}
}
