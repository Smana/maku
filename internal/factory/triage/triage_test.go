// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"reflect"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

func cfg() *config.Config {
	return &config.Config{Defaults: config.Defaults{DataClass: "public", PredictedClass: "review"},
		HarnessProfiles: map[string]config.HarnessProfile{"standard": {Template: "solo"}},
		Tiers:           map[string]config.Tier{"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}}}
}

// Static: the standard tier and the default workflow's template (WF3), the defaults' classes.
func TestStaticUsesTheDefaults(t *testing.T) {
	d, err := Static{}.Triage(t.Context(), cfg(), &v1alpha1.Task{})
	if err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}
	if d.Template != "solo" || d.PredictedClass != "review" || d.DataClass != "public" || d.Budget != want ||
		!reflect.DeepEqual(d.Classification, v1alpha1.Classification{Tier: "standard", Classifier: "static", Fallback: "static"}) {
		t.Fatalf("%+v", d)
	}
}

// The task's own data class wins over the default.
func TestStaticKeepsTheTasksDataClass(t *testing.T) {
	d, _ := Static{}.Triage(t.Context(), cfg(), &v1alpha1.Task{Spec: v1alpha1.TaskSpec{DataClass: "internal"}})
	if d.DataClass != "internal" {
		t.Fatal(d.DataClass)
	}
}

// P8: only a pinned profile is the labeled cohort; otherwise percent % are control.
func TestPinnedIsLabeled(t *testing.T) {
	for name, c := range map[string]struct {
		pinned  bool
		percent int
		want    string
	}{
		"pinned":              {true, 0, CohortLabeled},
		"pinned, all control": {true, 100, CohortLabeled},
		"unpinned, control":   {false, 100, CohortControl},
		"unpinned":            {false, 0, CohortTreatment},
	} {
		if got := CohortOf("3buqdlot", c.pinned, c.percent); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

func TestAnOutcomeNotOfferedFallsBackToChange(t *testing.T) {
	c := cfg()
	// Default Factory.spec.outcomes is [change] only.
	d, err := Static{}.Triage(t.Context(), c, &v1alpha1.Task{
		Spec: v1alpha1.TaskSpec{Outcome: config.OutcomeReport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != config.OutcomeChange {
		t.Fatalf("Outcome = %q, want %q", d.Outcome, config.OutcomeChange)
	}
	if len(d.Notes) == 0 {
		t.Fatalf("expected fallback note when report is not offered")
	}

	// Once offered, triage preserves the outcome with no fallback note.
	c.Outcomes = []string{config.OutcomeChange, config.OutcomeReport, config.OutcomePlanFirst}
	d, err = Static{}.Triage(t.Context(), c, &v1alpha1.Task{
		Spec: v1alpha1.TaskSpec{Outcome: config.OutcomeReport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != config.OutcomeReport || len(d.Notes) != 0 {
		t.Fatalf("offered outcome: got %q notes=%v", d.Outcome, d.Notes)
	}
}
