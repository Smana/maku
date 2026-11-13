// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/runwatch"
)

// meteringValues is the metering leg's values file, whose meter.prices the leg's factory runs with.
const meteringValues = "../../../charts/agent-factory/tests/metering-values.yaml"

// legPrices reads meter.prices from the metering leg's values, as the factory gets them.
func legPrices(t *testing.T) map[string]config.Price {
	t.Helper()
	raw, err := os.ReadFile(meteringValues)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Factory struct {
			Config struct {
				Meter struct {
					Prices map[string]config.Price `json:"prices"`
				} `json:"meter"`
			} `json:"config"`
		} `json:"maku-factory"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Factory.Config.Meter.Prices) == 0 {
		t.Fatalf("%s sets no maku-factory.config.meter.prices", meteringValues)
	}
	return v.Factory.Config.Meter.Prices
}

// TestRecordedAgentgatewayReply prices a reply recorded from a live gateway: install.sh's METERING leg
// (plan v4 step 4, Task 17) runs the factory's meter.query on VictoriaMetrics after a run's model call
// and sets METER_REPLY to the reply and METER_RUN to the run's id. The run must read as a portable run
// with input and output tokens, and be priced through runCosts at the leg's own meter.prices with no
// unpriced model: the live gen_ai_request_model must match a price key, or the run would fall back.
func TestRecordedAgentgatewayReply(t *testing.T) {
	path, run := os.Getenv("METER_REPLY"), os.Getenv("METER_RUN")
	if path == "" || run == "" {
		t.Skip("METER_REPLY and METER_RUN are not set: install.sh METERING=1 sets them")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a test input the leg names
	if err != nil {
		t.Fatal(err)
	}
	portable := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"}
	series, err := parseSeries(raw, portable.RunSubject())
	if err != nil {
		t.Fatal(err)
	}
	u, ok := sumUsage(series)[run]
	if !ok || !u.Typed || u.Input == 0 || u.Output == 0 {
		t.Fatalf("run %s: usage %+v (read %t): want typed input and output tokens", run, u, ok)
	}
	by, unpriced := runCosts(legPrices(t), "agent-default", u)
	if unpriced != "" {
		t.Fatalf("run %s: model %q served it with no row in %s's meter.prices", run, unpriced, meteringValues)
	}
	var micro int64
	for model, v := range by {
		t.Logf("model %s: %d micro-USD", model, v)
		micro += v
	}
	if micro <= 0 {
		t.Fatalf("run %s priced at %d micro-USD: want more than zero", run, micro)
	}
	t.Logf("run %s: %d micro-USD at the leg's meter.prices", run, micro)
}

// The leg's own prices name a model they do not price, so the test above fails on an unpriced live
// model rather than passing on it; the echo provider's models are priced.
func TestLegPricesNameAnUnservedModel(t *testing.T) {
	prices := legPrices(t)
	if _, unpriced := runCosts(prices, "agent-default", byModel(map[string]ModelUsage{"glm-5.3": {Input: 10, Output: 5}})); unpriced != "glm-5.3" {
		t.Fatalf("unpriced %q: a model the leg does not price must be named", unpriced)
	}
	for _, m := range []string{"echo-default", "echo-light", "echo-frontier"} {
		if _, unpriced := runCosts(prices, "agent-default", byModel(map[string]ModelUsage{m: {Input: 10, Output: 5}})); unpriced != "" {
			t.Fatalf("unpriced %q: the leg prices %s", unpriced, m)
		}
	}
}
