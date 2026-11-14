// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"testing"
)

// The umbrella chart's demo (values-demo.yaml) renders a factory config; charts/agent-factory/tests/render.sh
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

// TestRenderedChartConfigParses is the same proof for a production render (v4 step 6, Task 21):
// render.sh writes the factory config the k3d production leg installs to CHART_FACTORY_CONFIG.
func TestRenderedChartConfigParses(t *testing.T) {
	path := os.Getenv("CHART_FACTORY_CONFIG")
	if path == "" {
		t.Skip("CHART_FACTORY_CONFIG is not set: render.sh sets it")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatalf("the umbrella chart's production config: %v", err)
	}
	if c.Demo {
		t.Fatal("the rendered production config is demo: true")
	}
}
