// SPDX-License-Identifier: Apache-2.0

package stages

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

func dryRunTestConfig() *config.Config {
	profiles, templates, loops := config.ShippedWorkflows()
	return &config.Config{
		HarnessProfiles: profiles,
		Templates:       templates,
		Loops:           loops,
		Roles: map[string]config.RoleDef{
			"security-reviewer": {Capability: config.CapabilityRead, Brief: "roles/security-reviewer.md"},
		},
		Repositories: []config.Repository{
			{Name: "acme/app"},
		},
	}
}

func endedTask(id string, at time.Time, class, wf string, st ...v1alpha1.StageRecord) v1alpha1.Task {
	ended := metav1.NewTime(at)
	var total int64
	for _, s := range st {
		total += s.MicroUSD
	}
	return v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: id},
		Spec:       v1alpha1.TaskSpec{Repository: "acme/app", PredictedClass: class, DataClass: "public"},
		Status: v1alpha1.TaskStatus{
			Phase:   v1alpha1.PhaseDone,
			EndedAt: &ended,
			Outcome: "accepted",
			Profile: &v1alpha1.ProfileStatus{Name: wf},
			Usage:   v1alpha1.Usage{CostMicroUSD: total},
			Stages:  st,
		},
	}
}

func TestADryRunAddsTheMedianOfANewStage(t *testing.T) {
	cfg := dryRunTestConfig()
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

	// Five historical tasks in class "docs" ran security-reviewer with costs
	// $0.05, $0.07, $0.09, $0.12, $0.20 -> median is $0.09.
	var tasks []v1alpha1.Task
	secCosts := []int64{50_000, 70_000, 90_000, 120_000, 200_000}
	for i, c := range secCosts {
		tasks = append(tasks, endedTask(
			fmt.Sprintf("hist000%d", i), base.Add(time.Duration(i)*time.Minute), "docs", "thorough",
			v1alpha1.StageRecord{Stage: "implement", Runs: 1, MicroUSD: 400_000},
			v1alpha1.StageRecord{Stage: "review", Runs: 1, MicroUSD: 140_000},
			v1alpha1.StageRecord{Stage: "security-reviewer", Runs: 1, MicroUSD: c},
		))
	}
	// Newest task ran standard ($0.40 implement + $0.14 review = $0.54).
	tasks = append(tasks, endedTask(
		"3buqdlot", base.Add(time.Hour), "docs", "standard",
		v1alpha1.StageRecord{Stage: "implement", Runs: 1, MicroUSD: 400_000},
		v1alpha1.StageRecord{Stage: "review", Runs: 1, MicroUSD: 140_000},
	))

	thorough := config.HarnessProfile{
		Budget: "frontier",
		Stages: []config.StageDef{
			{Role: "implementer", Stall: true},
			{Role: "reviewer", MaxRounds: 2},
			{Role: "security-reviewer"},
		},
	}

	res := DryRun(cfg, "thorough", thorough, "acme/app", tasks, 1)
	if res.Tasks != 1 || len(res.Rows) != 1 {
		t.Fatalf("tasks = %d rows = %d, want 1", res.Tasks, len(res.Rows))
	}
	row := res.Rows[0]
	if row.Task != "3buqdlot" || row.Class != "docs" || row.Unknown || !row.UsedMedian || row.EstimateUSD == nil {
		t.Fatalf("row = %+v", row)
	}
	if !slices.Equal(row.Ran, []string{"implementer", "reviewer"}) {
		t.Fatalf("Ran = %v, want [implementer reviewer]", row.Ran)
	}
	if !slices.Equal(row.WouldRun, []string{"implementer", "reviewer", "security-reviewer"}) {
		t.Fatalf("WouldRun = %v, want [implementer reviewer security-reviewer]", row.WouldRun)
	}
	if row.CostUSD != 0.54 {
		t.Fatalf("CostUSD = %.2f, want 0.54", row.CostUSD)
	}
	if math.Abs(*row.EstimateUSD-0.63) > 1e-9 {
		t.Fatalf("EstimateUSD = %.2f, want 0.63 ($0.54 + $0.09 median)", *row.EstimateUSD)
	}
	if res.ActualUSD != 0.54 || res.EstimatedUSD != 0.63 || res.MedianUsed != 1 || res.EstimatedOf != 1 {
		t.Fatalf("summary = %+v", res)
	}
}

func TestADryRunDropsARemovedStagesCost(t *testing.T) {
	cfg := dryRunTestConfig()
	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	tasks := []v1alpha1.Task{
		endedTask("3buqdlot", base, "docs", "standard",
			v1alpha1.StageRecord{Stage: "implement", Runs: 1, MicroUSD: 400_000},
			v1alpha1.StageRecord{Stage: "review", Runs: 1, MicroUSD: 140_000},
		),
	}
	quick := cfg.HarnessProfiles[config.WorkflowQuick]

	res := DryRun(cfg, config.WorkflowQuick, quick, "acme/app", tasks, 20)
	if res.Tasks != 1 || len(res.Rows) != 1 {
		t.Fatalf("res = %+v", res)
	}
	row := res.Rows[0]
	if row.Unknown || row.UsedMedian || row.EstimateUSD == nil {
		t.Fatalf("row = %+v, want known estimate with no median needed", row)
	}
	if !slices.Equal(row.WouldRun, []string{"implementer"}) {
		t.Fatalf("WouldRun = %v, want [implementer]", row.WouldRun)
	}
	if row.CostUSD != 0.54 || *row.EstimateUSD != 0.40 {
		t.Fatalf("CostUSD=%.2f EstimateUSD=%.2f, want 0.54 / 0.40", row.CostUSD, *row.EstimateUSD)
	}
	if res.ActualUSD != 0.54 || res.EstimatedUSD != 0.40 || res.MedianUsed != 0 {
		t.Fatalf("summary = %+v", res)
	}
}

func TestADryRunSaysWhenItHasNoFigure(t *testing.T) {
	cfg := dryRunTestConfig()
	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	// Only 4 historical runs of security-reviewer in class "docs" (< 5 minimum).
	var tasks []v1alpha1.Task
	for i := range 4 {
		tasks = append(tasks, endedTask(
			fmt.Sprintf("hist000%d", i), base.Add(time.Duration(i)*time.Minute), "docs", "thorough",
			v1alpha1.StageRecord{Stage: "implement", Runs: 1, MicroUSD: 400_000},
			v1alpha1.StageRecord{Stage: "review", Runs: 1, MicroUSD: 100_000},
			v1alpha1.StageRecord{Stage: "security-reviewer", Runs: 1, MicroUSD: 80_000},
		))
	}
	// Newest task did not run security-reviewer, so it needs a class median (unavailable -> ?).
	tasks = append(tasks, endedTask(
		"3buqdlot", base.Add(time.Hour), "docs", "standard",
		v1alpha1.StageRecord{Stage: "implement", Runs: 1, MicroUSD: 400_000},
		v1alpha1.StageRecord{Stage: "review", Runs: 1, MicroUSD: 140_000},
	))

	thorough := config.HarnessProfile{
		Budget: "frontier",
		Stages: []config.StageDef{
			{Role: "implementer", Stall: true},
			{Role: "reviewer", MaxRounds: 2},
			{Role: "security-reviewer"},
		},
	}

	res := DryRun(cfg, "thorough", thorough, "acme/app", tasks, 2)
	if res.Tasks != 2 || len(res.Rows) != 2 {
		t.Fatalf("res = %+v", res)
	}
	// First row is newest ("3buqdlot"): unknown, left out of EstimatedUSD.
	unknownRow := res.Rows[0]
	if unknownRow.Task != "3buqdlot" || !unknownRow.Unknown || unknownRow.EstimateUSD != nil {
		t.Fatalf("unknownRow = %+v, want Unknown=true EstimateUSD=nil", unknownRow)
	}
	// Second row ("hist0003") already ran all three stages: known ($0.58), counted in EstimatedUSD.
	knownRow := res.Rows[1]
	if knownRow.Unknown || knownRow.EstimateUSD == nil || *knownRow.EstimateUSD != 0.58 {
		t.Fatalf("knownRow = %+v, want known 0.58", knownRow)
	}
	if res.UnknownCount != 1 || res.EstimatedOf != 1 || res.EstimatedUSD != 0.58 || res.ActualUSD != 1.12 {
		t.Fatalf("summary = %+v, want UnknownCount=1 EstimatedOf=1 EstimatedUSD=0.58 ActualUSD=1.12", res)
	}
}
