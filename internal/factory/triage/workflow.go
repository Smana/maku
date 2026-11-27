// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"cmp"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// The sources of a task's workflow (WF3, WF4), as ProfileStatus.Source records them: a maintainer's
// label, the task's source, or the repository's default.
const (
	SourceLabel      = "label"
	SourceSchedule   = "schedule"
	SourceRevert     = "revert"
	SourceOracle     = "oracle"
	SourceAdopted    = "adopted"
	SourceRepository = "repository"
)

// WorkflowFor is the workflow t runs and why: the first of a maintainer's pin, the task's source,
// the repository's default (WF3, WF4). A revert and an oracle finding run quick, whoever started
// them; a schedule's task runs the schedule's workflow, quick when it names none; an adopted pull
// request runs none. A pin
// refused at triage is no pin: the task runs as if unpinned (P6).
func WorkflowFor(cfg *config.Config, t *v1alpha1.Task, pin Pin) (name, source string) {
	rc := cfg.RepositoryFor(t.Spec.Repository)
	switch {
	case t.Spec.Source.Kind == "adopt":
		return config.TemplateNone, SourceAdopted
	case pin.Honoured():
		return pin.Name, SourceLabel
	case t.Spec.PredictedClass == "revert":
		return config.WorkflowQuick, SourceRevert
	case len(t.Spec.Findings) > 0 || oracleClass(rc, t.Spec.PredictedClass):
		return config.WorkflowQuick, SourceOracle // its detector verifies it: one implementer (MC6)
	case t.Spec.Source.Kind == "schedule":
		if rc != nil {
			if i := slices.IndexFunc(rc.Schedules, func(s config.Schedule) bool { return s.Name == t.Spec.Source.Ref }); i >= 0 {
				return rc.Schedules[i].WorkflowOr(), SourceSchedule
			}
		}
		return config.WorkflowQuick, SourceSchedule
	case t.Spec.Source.Kind == v1alpha1.SourceKindRoutine:
		name := t.Spec.Source.Ref
		if t.Spec.Routine != nil && t.Spec.Routine.Name != "" {
			name = t.Spec.Routine.Name
		}
		if rc != nil {
			if rr, ok := rc.ResolvedRoutines[name]; ok {
				return cmp.Or(rr.Workflow, rc.DefaultWorkflow()), SourceSchedule
			}
		}
		return config.WorkflowQuick, SourceSchedule
	}
	return rc.DefaultWorkflow(), SourceRepository
}

// Harness sources recorded on ProfileStatus.HarnessSource.
const (
	HarnessSourceLabeled   = "labeled"
	HarnessSourceTreatment = "treatment"
	HarnessSourceDefault   = "default"
	HarnessSourceRescue    = "rescue"
)

// HarnessFor resolves a task's harness and why:
// 1. Integration-only (no-model) tasks are forced to OpenHands.
// 2. An honoured maintainer pin (harness:<name> label or intake.harness) -> labeled.
// 3. Treatment cohort with evaluation.treatmentHarness set -> treatment.
// 4. Configured defaults.harness -> default.
// 5. Fallback -> openhands / default.
func HarnessFor(cfg *config.Config, t *v1alpha1.Task, pin Pin, cohort string) (harness, source string) {
	if t != nil && t.Spec.Kind == "integration" {
		return config.HarnessOpenHands, HarnessSourceDefault
	}
	if pin.Honoured() {
		return pin.Name, HarnessSourceLabeled
	}
	if cohort == CohortTreatment && cfg != nil && cfg.Evaluation.TreatmentHarness != "" {
		return cfg.Evaluation.TreatmentHarness, HarnessSourceTreatment
	}
	if cfg != nil && cfg.Defaults.Harness != "" {
		return cfg.Defaults.Harness, HarnessSourceDefault
	}
	return config.HarnessOpenHands, HarnessSourceDefault
}

// oracleClass reports whether class is an oracle class of rc; none when rc is not served.
func oracleClass(rc *config.Repository, class string) bool {
	cl, _ := rc.Class(class)
	return cl.Oracle != nil
}
