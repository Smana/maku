// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
)

func TestRunCostKeyCarriesHarness(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	now := t0
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{{
		ID: "aaaaaaaa", Role: "implementer", Tier: "standard", Model: "agent-default",
		Cohort: "treatment", Profile: "review-floor", Harness: "opencode", Phase: "Running", Created: t0,
	}}}
	src := typed{
		"aaaaaaaa": {Typed: true, Input: 1_000_000, Output: 0, Tokens: 1_000_000},
	}
	var gotKey CostKey
	var called bool
	m := &Meter{
		Runs:   st,
		Source: src,
		Cfg:    config.NewLive(&config.Config{Meter: config.Meter{Prices: map[string]config.Price{"agent-default": {Input: 1.0, Output: 2.0}}}, HarnessProfiles: map[string]config.HarnessProfile{"review-floor": {}}}),
		Now:    func() time.Time { return now },
		OnCost: func(_ context.Context, k CostKey, _ float64, _ map[string]float64) {
			gotKey, called = k, true
		},
	}
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st.runs[0].Phase = "Succeeded"
	st.runs[0].Finished = t0.Add(time.Minute)
	now = t0.Add(time.Minute)
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(4 * time.Minute)
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !called || gotKey.Harness != "opencode" || gotKey.Cohort != "treatment" {
		t.Fatalf("gotKey=%+v called=%v, want harness=opencode cohort=treatment", gotKey, called)
	}
}
