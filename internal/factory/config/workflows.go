// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// The shipped workflows' names (workflows WF2): a Factory that names none of them still offers all
// three, and none for an adopted pull request.
const (
	WorkflowQuick    = "quick"
	WorkflowStandard = "standard"
	WorkflowCareful  = "careful"
)

// ShippedWorkflows are the three workflows, quick, standard and careful, with none for adopted pull
// requests, as the templates, harness profiles and loops rows they are made of (WF2). Each call
// returns fresh maps.
func ShippedWorkflows() (profiles map[string]HarnessProfile, templates map[string]Template, loops map[string]Loop) {
	profiles = map[string]HarnessProfile{
		WorkflowQuick:    {Template: "solo", Description: "One implementer. For changes a check can verify."},
		WorkflowStandard: {Template: "pair", Description: "An implementer and a reviewer. The default."},
		WorkflowCareful: {Template: "pair", Tier: "frontier", Loop: WorkflowCareful,
			Description: "Scoped first, judged before review. For wide or unclear tasks."},
		TemplateNone: {Template: TemplateNone},
	}
	templates = map[string]Template{
		"solo":        {Roles: []string{"implementer"}},
		"pair":        {Roles: []string{"implementer", "reviewer"}, MaxReviewRounds: 2},
		"investigate": {Roles: []string{"triager"}},
	}
	loops = map[string]Loop{
		"default": {Scope: ScopeOff},
		WorkflowCareful: {Scope: ScopeScoper, Judge: DefaultJudge, Search: true,
			Split: &SplitPolicy{MaxChildren: 4, ChildTier: DefaultChildTier}},
	}
	return profiles, templates, loops
}

// FillShippedWorkflows adds each shipped entry whose key the maps lack, and never touches an entry
// they hold: a platform's own standard, or its own careful row, is kept whole (WF2). The careful
// row is added only while careful's profile names it, since a row nothing names is refused, and
// with its split only when split says the factory can run one.
func FillShippedWorkflows(profiles map[string]HarnessProfile, templates map[string]Template,
	loops map[string]Loop, split bool) (map[string]HarnessProfile, map[string]Template, map[string]Loop) {
	sp, st, sl := ShippedWorkflows()
	profiles = fill(profiles, sp)
	templates = fill(templates, st)
	if string(profiles[WorkflowCareful].Loop) != WorkflowCareful {
		delete(sl, WorkflowCareful)
	} else if !split {
		row := sl[WorkflowCareful]
		row.Split = nil
		sl[WorkflowCareful] = row
	}
	loops = fill(loops, sl)
	return profiles, templates, loops
}

func fill[V any](dst, shipped map[string]V) map[string]V {
	if dst == nil {
		dst = map[string]V{}
	}
	for k, v := range shipped {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
	return dst
}

// applyShippedWorkflows fills c with the shipped workflows (FillShippedWorkflows). The careful
// row's split needs what validateLoops asks of any split: a metered, priced runtime whose runs
// carry a gateway identity.
func (c *Config) applyShippedWorkflows() {
	split := c.Meter.URL != "" && c.Meter.Query != "" && len(c.Meter.Prices) > 0 && c.Runs.GatewayIdentity
	c.HarnessProfiles, c.Templates, c.Loops = FillShippedWorkflows(c.HarnessProfiles, c.Templates, c.Loops, split)
}

// IsWorkflow is whether name is a workflow a task may run: one of HarnessProfiles, never none,
// which runs nothing and is an adopted pull request's alone.
func (c *Config) IsWorkflow(name string) bool {
	_, ok := c.HarnessProfiles[name]
	return ok && name != TemplateNone
}

// RepositoryWorkflow is a repository's say in the workflow (WF3, WF7): the one its tasks run when
// no label and no source names one, and whether its careful tasks start with the tester.
// +kubebuilder:object:generate=true
type RepositoryWorkflow struct {
	// Default is the workflow a task runs when nothing else names one; unset is standard.
	// +kubebuilder:validation:MaxLength=253
	Default string `json:"default,omitempty"`
	// TestFirst starts every public careful task with a tester run that writes the failing tests
	// first (v3 feature 4, TF1); needs merge.contractCheck.
	TestFirst bool `json:"testFirst,omitempty"`
}

// DefaultWorkflow is the workflow a task of the repository runs when nothing else names one (WF3):
// workflow.default, else standard, which a repository the config does not serve (nil) runs too.
func (r *Repository) DefaultWorkflow() string {
	if r != nil && r.Workflow.Default != "" {
		return r.Workflow.Default
	}
	return WorkflowStandard
}

// TestFirstIn is whether a public task running workflow starts with the tester: the repository
// opts in, and the workflow is careful (WF7). A repository not served (nil) runs none.
func (r *Repository) TestFirstIn(workflow string) bool {
	return r != nil && r.Workflow.TestFirst && workflow == WorkflowCareful
}

// WorkflowOr is the workflow the schedule's tasks run: its own, else quick (WF3).
func (s Schedule) WorkflowOr() string {
	if s.Workflow != "" {
		return s.Workflow
	}
	return WorkflowQuick
}

// validateWorkflow is a repository's workflow block and its schedules' workflows (WF3, WF7).
func (c *Config) validateWorkflow(r *Repository, bad func(string, ...any)) {
	switch d := r.Workflow.Default; {
	case d == TemplateNone:
		bad("workflow.default: none is for adopted pull requests: name a workflow that runs")
	case d != "" && !c.IsWorkflow(d):
		bad("workflow.default %q is not a workflow", d)
	}
	if r.Workflow.TestFirst && r.Merge.ContractCheck == "" {
		bad("workflow.testFirst needs merge.contractCheck: the tester's red check proves the contract (TF7)")
	}
	for _, s := range r.Schedules {
		if s.Workflow != "" && !c.IsWorkflow(s.Workflow) {
			bad("schedule %s: workflow %q is not a workflow", s.Name, s.Workflow)
		}
	}
}

// upgradeNotes is where every refusal of a removed setting points (WF13).
const upgradeNotes = "docs/upgrading.md#workflows-name-what-runs"

// refuseRemoved names each setting of the workflows design that release N warned about and release
// N+1 refuses (WF9, WF13), with what to write instead, so the file fails on what it means rather
// than on an unknown field.
func refuseRemoved(top map[string]json.RawMessage) []error {
	var errs []error
	removed := func(path, instead string) {
		errs = append(errs, fmt.Errorf("%s: %s (%s)", path, instead, upgradeNotes))
	}
	if _, ok := top["harnessProfiles"]; ok {
		removed("harnessProfiles", "renamed workflows: move the map under workflows")
	}
	var block map[string]json.RawMessage
	if json.Unmarshal(top["templates"], &block) == nil {
		if _, ok := block["trio"]; ok {
			removed("templates.trio", "removed, trio had pair's roles since the tester left it: name pair")
		}
	}
	block = nil
	if json.Unmarshal(top["triage"], &block) == nil {
		if _, ok := block["classifierURL"]; ok {
			removed("triage.classifierURL", "removed, the task classifier was never deployed and the scoper replaced it: remove the key")
		}
	}
	block = nil
	if json.Unmarshal(top["defaults"], &block) == nil {
		for _, k := range []string{"template", "tier"} {
			if _, ok := block[k]; ok {
				removed("defaults."+k, "removed, a workflow names its "+k+": remove the key")
			}
		}
	}
	var workflows map[string]map[string]json.RawMessage
	if json.Unmarshal(top["workflows"], &workflows) == nil {
		for _, name := range slices.Sorted(maps.Keys(workflows)) {
			if _, ok := workflows[name]["testFirst"]; ok {
				removed("workflows."+name+".testFirst", "removed, the tester is the repository's opt-in: set the Repository's workflow.testFirst")
			}
		}
	}
	errs = append(errs, refuseRemovedMergePolicies(top)...)
	return errs
}

// DefaultTemplate is the template of r's default workflow (WF3), pair when it names none or r is
// not served: what a task runs when nothing else names its roles.
func (c *Config) DefaultTemplate(r *Repository) string {
	if p, ok := c.Workflow(r.DefaultWorkflow()); ok && p.Template != "" {
		return p.Template
	}
	return "pair"
}
