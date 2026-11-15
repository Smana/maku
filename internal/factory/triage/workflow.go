// SPDX-License-Identifier: Apache-2.0

package triage

import (
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
	}
	return rc.DefaultWorkflow(), SourceRepository
}

// oracleClass reports whether class is an oracle class of rc; none when rc is not served.
func oracleClass(rc *config.Repository, class string) bool {
	cl, _ := rc.Class(class)
	return cl.Oracle != nil
}
