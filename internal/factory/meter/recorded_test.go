// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"os"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/runwatch"
)

// TestRecordedAgentgatewayReply prices a reply recorded from a live gateway: install.sh's METERING leg
// (plan v4 step 4, Task 17) runs the factory's meter.query on VictoriaMetrics after a run's model call
// and sets METER_REPLY to the reply and METER_RUN to the run's id. The run must read as a portable run
// with input and output tokens, and cost more than zero at the leg's prices (tests/metering-values.yaml).
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
	price := config.Price{Input: 1.00, CacheRead: 0.10, Output: 2.00}
	var micro int64
	for model, mu := range u.Models {
		m := cost(price, Usage{Input: mu.Input, CacheRead: mu.CacheRead, CacheWrite: mu.CacheWrite, Output: mu.Output, Typed: true})
		t.Logf("model %s: %+v, %d micro-USD", model, mu, m)
		micro += m
	}
	if micro <= 0 {
		t.Fatalf("run %s priced at %d micro-USD: want more than zero", run, micro)
	}
	t.Logf("run %s: %d micro-USD at the leg's echo prices", run, micro)
}
