// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

// workflowCfg serves Smana/a, whose default is standard, with a schedule naming careful and one
// naming none, and Smana/careful-repo, whose default is careful.
func workflowCfg() *config.Config {
	profiles, _, _ := config.ShippedWorkflows()
	return &config.Config{HarnessProfiles: profiles, Repositories: []config.Repository{
		{Name: "Smana/a", Maintainers: []string{"Smana"},
			Classes:   map[string]config.Class{"dead-links": {Oracle: &config.Oracle{Probe: "dead-links"}}},
			Schedules: []config.Schedule{{Name: "weekly", Workflow: "careful"}, {Name: "plain"}}},
		{Name: "Smana/careful-repo", Maintainers: []string{"Smana"}, Workflow: config.RepositoryWorkflow{Default: "careful"}},
	}}
}

func taskIn(repo, kind, ref string) *v1alpha1.Task {
	return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: repo, Source: v1alpha1.Source{Kind: kind, Ref: ref}}}
}

// WF3, WF4: a maintainer's label, then the task's source, then the repository's default.
func TestWorkflowFor(t *testing.T) {
	cfg := workflowCfg()
	revert := taskIn("Smana/a", "issue", "")
	revert.Spec.PredictedClass = "revert"
	oracle := taskIn("Smana/a", "schedule", "")
	oracle.Spec.Findings = []v1alpha1.Finding{{}}
	oracleClass := taskIn("Smana/a", "issue", "")
	oracleClass.Spec.PredictedClass = "dead-links"
	for _, c := range []struct {
		name       string
		task       *v1alpha1.Task
		pin        Pin
		want, from string
	}{
		{"a maintainer's label wins", taskIn("Smana/a", "issue", ""), Pin{Name: "careful"}, "careful", "label"},
		{"a maintainer may pick below the default", taskIn("Smana/a", "issue", ""), Pin{Name: "quick"}, "quick", "label"},
		{"a refused pin is ignored", taskIn("Smana/a", "issue", ""), Pin{Name: "quick", Refused: intake.NotMaintainer}, "standard", "repository"},
		{"a schedule names its own", taskIn("Smana/a", "schedule", "weekly"), Pin{}, "careful", "schedule"},
		{"a schedule without one runs quick", taskIn("Smana/a", "schedule", "plain"), Pin{}, "quick", "schedule"},
		{"a schedule the config lost runs quick", taskIn("Smana/a", "schedule", "gone"), Pin{}, "quick", "schedule"},
		{"a revert runs quick", revert, Pin{}, "quick", "revert"},
		{"an oracle finding runs quick", oracle, Pin{}, "quick", "oracle"},
		{"an oracle class's task runs quick", oracleClass, Pin{}, "quick", "oracle"},
		{"an adopted pull request runs none", taskIn("Smana/a", "adopt", ""), Pin{}, "none", "adopted"},
		{"the repository's default", taskIn("Smana/careful-repo", "issue", ""), Pin{}, "careful", "repository"},
		{"a repository not served runs standard", taskIn("Smana/elsewhere", "api", ""), Pin{}, "standard", "repository"},
	} {
		if got, from := WorkflowFor(cfg, c.task, c.pin); got != c.want || from != c.from {
			t.Errorf("%s: got %s from %s, want %s from %s", c.name, got, from, c.want, c.from)
		}
	}
}
