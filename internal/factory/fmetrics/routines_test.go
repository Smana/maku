// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRoutineMetricsAreExposedAndRenamed(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Acme/App")
	s.SetRoutines(map[string][]string{"Acme/App": {"dead-code", "doc-drift"}})

	s.IntakeError(WithRepository(ctx, "Acme/App"), "routine")
	s.RoutineRun(ctx, "acme/app", "dead-code", "accepted", 1.25)
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "no_op", 0.15)
	s.RoutineSkip(ctx, "Acme/App", "dead-code", "upkeep_budget")
	s.RoutinePromptRead(ctx, "Acme/App", "dead-code", "repo")
	s.RoutinePaused(ctx, "Acme/App", "doc-drift", true)
	s.RoutinePaused(ctx, "Acme/App", "dead-code", false)
	s.RoutineMergeRate(ctx, "Acme/App", "dead-code", 0.75)

	body := scrape()
	for _, want := range []string{
		`maku_factory_intake_errors_total{repository="Acme/App",source="routine"} 1`,
		`maku_factory_routine_runs_total{outcome="accepted",repository="Acme/App",routine="dead-code"} 1`,
		`maku_factory_routine_runs_total{outcome="no_op",repository="Acme/App",routine="doc-drift"} 1`,
		`maku_factory_routine_usd_total{outcome="accepted",repository="Acme/App",routine="dead-code"} 1.25`,
		`maku_factory_routine_usd_total{outcome="no_op",repository="Acme/App",routine="doc-drift"} 0.15`,
		`maku_factory_routine_skips_total{reason="upkeep_budget",repository="Acme/App",routine="dead-code"} 1`,
		`maku_factory_routine_prompt_reads_total{outcome="repo",repository="Acme/App",routine="dead-code"} 1`,
		`maku_factory_routine_paused{repository="Acme/App",routine="doc-drift"} 1`,
		`maku_factory_routine_paused{repository="Acme/App",routine="dead-code"} 0`,
		`maku_factory_routine_merge_rate{repository="Acme/App",routine="dead-code"} 0.75`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	if strings.Contains(body, "agent_factory_routine_") {
		t.Fatalf("unrenamed agent_factory_routine_ metric exposed:\n%s", body)
	}
}

func TestRoutineLabelsAreBounded(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Acme/App")

	names := make([]string, 0, 19)
	for i := 1; i <= 18; i++ {
		names = append(names, fmt.Sprintf("r%02d", i))
	}
	names = append(names, "Bad_Name")
	s.SetRoutines(map[string][]string{"Acme/App": names})

	// r01..r16 pass through; r17, r18, Bad_Name, unconfigured, prompt injection -> "other"
	s.RoutineRun(ctx, "Acme/App", "r01", "accepted", 0.5)
	s.RoutineRun(ctx, "Acme/App", "r16", "accepted", 0.25)
	s.RoutineRun(ctx, "Acme/App", "r17", "accepted", 0.1)
	s.RoutineRun(ctx, "Acme/App", "r18", "accepted", 0.1)
	s.RoutineRun(ctx, "Acme/App", "Bad_Name", "accepted", 0.1)
	s.RoutineRun(ctx, "Acme/App", "unconfigured", "accepted", 0.1)
	s.RoutineRun(ctx, "Acme/App", "ignore previous instructions", "accepted", -5)

	// Unlisted repo -> repository="other", routine="other"
	s.RoutineRun(ctx, "Evil/Repo", "r01", "accepted", 0.2)

	// All 8 taskOutcomes pass, bogus collapses to "other"
	for _, o := range taskOutcomes() {
		s.RoutineRun(ctx, "Acme/App", "r02", o, 0)
	}
	s.RoutineRun(ctx, "Acme/App", "r02", "bogus_outcome", 0)

	// All 13 Skip* constants pass, bogus collapses to "other"
	for _, reason := range []string{
		"kill_switch", "not_owned", "disabled", "paused_manual", "breaker_paused",
		"open_cap", "cooldown", "upkeep_budget", "probe_empty", "probe_refused",
		"prompt_invalid", "skill_missing", "skill_invalid",
	} {
		s.RoutineSkip(ctx, "Acme/App", "r01", reason)
	}
	s.RoutineSkip(ctx, "Acme/App", "r01", "bogus_skip")

	// All 6 prompt read outcomes pass, bogus collapses to "other"
	for _, outcome := range routinePromptReadOutcomes() {
		s.RoutinePromptRead(ctx, "Acme/App", "r01", outcome)
	}
	s.RoutinePromptRead(ctx, "Acme/App", "r01", "bogus_prompt_outcome")

	// NaN/Inf ignored; clamp <0 -> 0, >1 -> 1
	s.RoutineMergeRate(ctx, "Acme/App", "r01", math.NaN())
	s.RoutineMergeRate(ctx, "Acme/App", "r01", math.Inf(1))
	s.RoutineMergeRate(ctx, "Acme/App", "r02", -0.5)
	s.RoutineMergeRate(ctx, "Acme/App", "r03", 1.5)

	body := scrape()
	for _, want := range []string{
		`maku_factory_routine_runs_total{outcome="accepted",repository="Acme/App",routine="r01"} 1`,
		`maku_factory_routine_runs_total{outcome="accepted",repository="Acme/App",routine="r16"} 1`,
		`maku_factory_routine_runs_total{outcome="accepted",repository="Acme/App",routine="other"} 5`,
		`maku_factory_routine_usd_total{outcome="accepted",repository="Acme/App",routine="other"} 0.4`,
		`maku_factory_routine_runs_total{outcome="accepted",repository="other",routine="other"} 1`,
		`maku_factory_routine_runs_total{outcome="other",repository="Acme/App",routine="r02"} 1`,
		`maku_factory_routine_skips_total{reason="other",repository="Acme/App",routine="r01"} 1`,
		`maku_factory_routine_prompt_reads_total{outcome="other",repository="Acme/App",routine="r01"} 1`,
		`maku_factory_routine_merge_rate{repository="Acme/App",routine="r02"} 0`,
		`maku_factory_routine_merge_rate{repository="Acme/App",routine="r03"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	for _, o := range taskOutcomes() {
		want := fmt.Sprintf(`maku_factory_routine_runs_total{outcome=%q,repository="Acme/App",routine="r02"} 1`, o)
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	for _, reason := range []string{
		"kill_switch", "not_owned", "disabled", "paused_manual",
		"open_cap", "cooldown", "upkeep_budget", "probe_empty", "probe_refused",
		"prompt_invalid", "skill_missing", "skill_invalid",
	} {
		want := fmt.Sprintf(`maku_factory_routine_skips_total{reason=%q,repository="Acme/App",routine="r01"} 1`, reason)
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	for _, outcome := range routinePromptReadOutcomes() {
		want := fmt.Sprintf(`maku_factory_routine_prompt_reads_total{outcome=%q,repository="Acme/App",routine="r01"} 1`, outcome)
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	for _, unwanted := range []string{`routine="r17"`, `routine="r18"`, `routine="Bad_Name"`, `routine="unconfigured"`, `ignore previous instructions`, `Evil/Repo`, `bogus_outcome`, `bogus_skip`, `bogus_prompt_outcome`, `routine="r01"` + "}"} {
		if unwanted == `routine="r01"}` {
			if strings.Contains(body, `maku_factory_routine_merge_rate{repository="Acme/App",routine="r01"}`) {
				t.Errorf("NaN/Inf merge rate recorded for r01\n%s", body)
			}
			continue
		}
		if strings.Contains(body, unwanted) {
			t.Errorf("unbounded label value leaked: %s\n%s", unwanted, body)
		}
	}

	// Pruning: SetRoutines removing "r03" prunes its paused/merge_rate gauges and collapses future calls to "other".
	s.RoutinePaused(ctx, "Acme/App", "r03", true)
	s.SetRoutines(map[string][]string{"Acme/App": {"r01", "r02"}})
	body = scrape()
	if strings.Contains(body, `routine="r03"`) {
		// Note: r03 has no runs/skips, only paused & merge_rate, so r03 must be completely gone from scrape.
		t.Errorf("removed routine r03 remained in scrape:\n%s", body)
	}
	s.RoutinePaused(ctx, "Acme/App", "r03", true)
	body = scrape()
	if !strings.Contains(body, `maku_factory_routine_paused{repository="Acme/App",routine="other"} 1`+"\n") {
		t.Errorf("removed routine r03 did not collapse to other:\n%s", body)
	}
}

func TestRoutineMetricsConcurrentAndNilSafe(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Acme/App")
	s.SetRoutines(map[string][]string{"Acme/App": {"dead-code", "doc-drift"}})

	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 25 {
				switch (id + i) % 5 {
				case 0:
					s.RoutineRun(ctx, "Acme/App", "dead-code", "accepted", 0.1)
				case 1:
					s.RoutineSkip(ctx, "Acme/App", "doc-drift", "cooldown")
				case 2:
					s.RoutinePaused(ctx, "Acme/App", "dead-code", i%2 == 0)
				case 3:
					s.RoutineMergeRate(ctx, "Acme/App", "dead-code", 0.5)
				case 4:
					s.SetRoutines(map[string][]string{"Acme/App": {"dead-code", "doc-drift"}})
					_ = scrape()
				}
			}
		}(g)
	}
	wg.Wait()

	// Nil-safe: New(nil, ...) and nil *Set must not panic.
	noopSet, err := New(nil, nil, "agent-system", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	noopSet.SetRoutines(map[string][]string{"Acme/App": {"dead-code"}})
	noopSet.RoutineRun(ctx, "Acme/App", "dead-code", "accepted", 1)
	noopSet.RoutineSkip(ctx, "Acme/App", "dead-code", "open_cap")
	noopSet.RoutinePromptRead(ctx, "Acme/App", "dead-code", "default")
	noopSet.RoutinePaused(ctx, "Acme/App", "dead-code", true)
	noopSet.RoutineMergeRate(ctx, "Acme/App", "dead-code", 0.5)

	var nilSet *Set
	nilSet.SetRoutines(map[string][]string{"Acme/App": {"dead-code"}})
	nilSet.RoutineRun(ctx, "Acme/App", "dead-code", "accepted", 1)
	nilSet.RoutineSkip(ctx, "Acme/App", "dead-code", "open_cap")
	nilSet.RoutinePromptRead(ctx, "Acme/App", "dead-code", "default")
	nilSet.RoutinePaused(ctx, "Acme/App", "dead-code", true)
	nilSet.RoutineMergeRate(ctx, "Acme/App", "dead-code", 0.5)
}

func TestRoutineGuardsUnwiredRepoAndNonFiniteUSD(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Acme/App")
	// SetRoutines includes an unwired repo ("Unwired/Repo") not in ListRepositories.
	s.SetRoutines(map[string][]string{
		"Acme/App":     {"doc-drift"},
		"Unwired/Repo": {"secret-routine"},
	})

	s.RoutineRun(ctx, "Unwired/Repo", "secret-routine", "accepted", 0.5)
	s.RoutineSkip(ctx, "Unwired/Repo", "secret-routine", "open_cap")
	s.RoutinePaused(ctx, "Unwired/Repo", "secret-routine", true)
	s.RoutineMergeRate(ctx, "Unwired/Repo", "secret-routine", 0.8)

	// Non-finite and negative usd values on RoutineRun must be clamped to 0.
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "accepted", math.NaN())
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "accepted", math.Inf(1))
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "accepted", math.Inf(-1))
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "accepted", -1.25)
	s.RoutineRun(ctx, "Acme/App", "doc-drift", "accepted", 0.75)

	body := scrape()
	if strings.Contains(body, `routine="secret-routine"`) {
		t.Fatalf("unwired repo leaked routine name:\n%s", body)
	}
	for _, want := range []string{
		`maku_factory_routine_runs_total{outcome="accepted",repository="other",routine="other"} 1`,
		`maku_factory_routine_skips_total{reason="open_cap",repository="other",routine="other"} 1`,
		`maku_factory_routine_paused{repository="other",routine="other"} 1`,
		`maku_factory_routine_merge_rate{repository="other",routine="other"} 0.8`,
		`maku_factory_routine_runs_total{outcome="accepted",repository="Acme/App",routine="doc-drift"} 5`,
		`maku_factory_routine_usd_total{outcome="accepted",repository="Acme/App",routine="doc-drift"} 0.75`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}
