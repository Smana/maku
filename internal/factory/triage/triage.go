// SPDX-License-Identifier: Apache-2.0

// Package triage decides a task's team and budget once (§2). Phase 1 uses the config's defaults;
// phase 4 adds the C7 classifier and the class matrix behind the same interface.
package triage

import (
	"context"
	"fmt"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Decision is what triage fixes on a task: its template, predicted class, data class, outcome and budget.
type Decision struct {
	Template, PredictedClass, DataClass, Outcome string
	Budget                                       v1alpha1.Budget
	Classification                               v1alpha1.Classification
	// Declared is a class a maintainer, a schedule or a maintainer's intake named: informational
	// (the policy fact computes its own), and never the cohort (P8). Pinned is an honoured profile
	// pin (Pin.Honoured), the one thing that makes a task labeled. Cohort is the evaluation's group.
	Declared, Pinned bool
	Cohort           string
	// Pin is the profile a person pinned and whether triage refused it (P6); zero without one.
	Pin Pin
	// HarnessPin is the harness a person pinned (harness:<name> label or intake.harness) and
	// whether triage refused it; zero without one.
	HarnessPin Pin
	// ModelPin is the gateway alias a maintainer's model:<alias> label pinned the implementer and
	// the reviewer to, and whether triage refused it (Gemini plan A7); zero without one.
	ModelPin Pin
	// Notes are sentences triage says once on the task, such as LegacyLabelNote.
	Notes []string
}

// The evaluation cohorts a task is frozen in at triage, plus rescue for an explicit OpenHands rescue.
const (
	CohortLabeled   = "labeled"
	CohortControl   = "control"
	CohortTreatment = "treatment"
	CohortRescue    = "rescue"
)

// CohortOf is a task's evaluation group: a pinned profile is labeled, else percent % are control.
// A declared class is no pin: a class label or a schedule's class joins the cohorts (P8, G8).
func CohortOf(taskID string, pinned bool, percent int) string {
	switch {
	case pinned:
		return CohortLabeled
	case Cohort(taskID, percent):
		return CohortControl
	}
	return CohortTreatment
}

// Triager decides once per task.
type Triager interface {
	Triage(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (Decision, error)
}

// BudgetFor is the tier's caps and model (§6.2, R11).
func BudgetFor(cfg *config.Config, tier string) v1alpha1.Budget {
	c := cfg.Tiers[tier]
	return v1alpha1.Budget{Tier: tier, Model: c.Model, RunTokens: c.RunTokens, TaskTokens: c.TaskTokens, RunMinutes: c.RunMinutes,
		RunMicroUSD: config.MicroUSD(c.RunUSD), TaskMicroUSD: config.MicroUSD(c.TaskUSD)}
}

// Static is phase 1's triage: the config's defaults for every task, at the standard tier, with
// Template, else the template of the repository's default workflow (pair when it names none).
type Static struct {
	Template string
}

// ResolveOutcome decides a task's outcome against Factory.spec.outcomes (phase F). An outcome not
// offered by cfg falls back to change with one explanatory note.
func ResolveOutcome(cfg *config.Config, t *v1alpha1.Task) (outcome, note string) {
	want := t.Spec.Outcome
	if want == "" && t.Spec.Intake != nil {
		want = t.Spec.Intake.Outcome
	}
	want = config.NormalizeOutcome(want)
	if !config.ValidOutcome(want) || !cfg.OffersOutcome(want) {
		return config.OutcomeChange, fmt.Sprintf("outcome %q is not offered by this factory; running as change.", want)
	}
	return want, ""
}

// Triage returns the defaults; a task's own data class wins over the default one.
func (s Static) Triage(_ context.Context, cfg *config.Config, t *v1alpha1.Task) (Decision, error) {
	d := cfg.Defaults
	dataClass := t.Spec.DataClass
	if dataClass == "" {
		dataClass = d.DataClass
	}
	template := s.Template
	if template == "" {
		template = cfg.DefaultTemplate(cfg.RepositoryFor(t.Spec.Repository))
	}
	outcome, outcomeNote := ResolveOutcome(cfg, t)
	if t.Spec.Source.Kind == "review" {
		outcome, outcomeNote = config.OutcomeReview, ""
	}
	switch outcome {
	case config.OutcomeReview:
		template = "review"
	case config.OutcomeReport:
		template = "investigate"
	}
	var notes []string
	if outcomeNote != "" {
		notes = []string{outcomeNote}
	}
	const tier = "standard"
	return Decision{Template: template, PredictedClass: d.PredictedClass, DataClass: dataClass, Outcome: outcome,
		Budget:         BudgetFor(cfg, tier),
		Classification: v1alpha1.Classification{Tier: tier, Classifier: "static", Fallback: "static"},
		Cohort:         CohortOf(t.Name, false, cfg.Evaluation.ControlPercent), Notes: notes}, nil
}

// sponsoredRuns is how many whole runs a ceiling holds: implementer, tester and reviewer. runFits
// admits a run only while the task's cap still holds a whole run cap, so a run cap equal to the
// ceiling would starve the second run.
const sponsoredRuns = 3

// Ceiling caps a budget under an api task's max_cost (F4, R5, C4): the task at the ceiling, each
// run at a third of it, in USD and in tokens at the reference rate, so an unpriced run keeps a
// ceiling too. It only ever lowers the class's caps, and never to 0, which reads as unset (I-2).
func Ceiling(b v1alpha1.Budget, maxMicroUSD int64) v1alpha1.Budget {
	if maxMicroUSD <= 0 {
		return b
	}
	taskTokens := int64(float64(maxMicroUSD) / config.RefUSDPerMToken)
	b.TaskMicroUSD = capAt(b.TaskMicroUSD, maxMicroUSD)
	b.RunMicroUSD = capAt(b.RunMicroUSD, max(1, maxMicroUSD/sponsoredRuns))
	b.TaskTokens = capAt(b.TaskTokens, max(1, taskTokens))
	b.RunTokens = capAt(b.RunTokens, max(1, taskTokens/sponsoredRuns))
	return b
}

// capAt is v under c: c when v is unset (0) or above it.
func capAt(v, c int64) int64 {
	if v == 0 || v > c {
		return c
	}
	return v
}
