// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

// DefaultPlanGateHours is the default wait before an unanswered plan gate marks itself parked (phase F3).
const (
	DefaultPlanGateHours = 72
	minPlanGateHours     = 1
	maxPlanGateHours     = 720
)

// The shipped workflows' names (workflows WF2): a Factory that names none of them still offers all
// three, and none for an adopted pull request.
const (
	WorkflowQuick    = "quick"
	WorkflowStandard = "standard"
	WorkflowCareful  = "careful"
)

// ShippedWorkflows are the three workflows, quick, standard and careful, in the list form (WR11),
// with none for adopted pull requests, plus the built-in templates and default loop row for legacy
// workflows. Each call returns fresh maps.
func ShippedWorkflows() (profiles map[string]HarnessProfile, templates map[string]Template, loops map[string]Loop) {
	profiles = map[string]HarnessProfile{
		WorkflowQuick: {
			Description: "One implementer. For changes a check can verify.",
			Budget:      "standard",
			StepUp:      WorkflowStandard,
			Stages: []StageDef{
				{Role: "implementer", Stall: true},
			},
		},
		WorkflowStandard: {
			Description: "An implementer and a reviewer. The default.",
			Budget:      "standard",
			StepUp:      WorkflowCareful,
			Stages: []StageDef{
				{Role: "implementer", Stall: true},
				{Role: "reviewer", MaxRounds: 2},
			},
		},
		WorkflowCareful: {
			Description: "Scoped first, judged before review. For wide or unclear tasks.",
			Budget:      "frontier",
			Stages: []StageDef{
				{Role: "scoper", Split: &SplitDef{MaxParts: 4}, Gate: &GateDef{When: GateWhenPlanFirst}},
				{Role: "tester", When: TestWhenRepositoryTestFirst},
				{Role: "implementer", Search: true, Stall: true, Judge: JudgeEvidence},
				{Role: "reviewer", MaxRounds: 2},
			},
		},
		TemplateNone: {Template: TemplateNone},
	}
	templates = map[string]Template{
		"solo":        {Roles: []string{"implementer"}},
		"pair":        {Roles: []string{"implementer", "reviewer"}, MaxReviewRounds: 2},
		"investigate": {Roles: []string{"triager"}},
		"review":      {Roles: []string{"reviewer"}},
	}
	stall := int32(DefaultStallSteps)
	askOn, reviewOn := true, true
	loops = map[string]Loop{
		"default": {Ask: &askOn, Review: &reviewOn, StallSteps: &stall, Judge: NoJudge},
	}
	return profiles, templates, loops
}

// legacyShippedProfile returns the pre-WR11 workflow definition for name when a platform
// customizes a shipped workflow through its legacy loops table without redefining workflows.<name>.
func legacyShippedProfile(name string, hasOwnLoop bool) HarnessProfile {
	switch name {
	case WorkflowQuick:
		return HarnessProfile{Template: "solo", StepUp: WorkflowStandard, Description: "One implementer. For changes a check can verify."}
	case WorkflowStandard:
		return HarnessProfile{Template: "pair", StepUp: WorkflowCareful, Description: "An implementer and a reviewer. The default."}
	case WorkflowCareful:
		p := HarnessProfile{Template: "pair", Tier: "frontier", Description: "Scoped first, judged before review. For wide or unclear tasks."}
		if hasOwnLoop {
			p.Loop = WorkflowCareful
		}
		return p
	}
	return HarnessProfile{}
}

// FillShippedWorkflows adds each shipped entry whose key the maps lack, and never touches an entry
// they hold: a platform's own standard, or its own careful row, is kept whole (WF2, WR11). A
// platform that customizes shipped workflows via loops.<name> or loops.default keeps the legacy
// binding so its rows still govern. The shipped careful workflow carries split only when split
// says the factory can run one.
func FillShippedWorkflows(profiles map[string]HarnessProfile, templates map[string]Template,
	loops map[string]Loop, split bool) (map[string]HarnessProfile, map[string]Template, map[string]Loop) {
	sp, st, sl := ShippedWorkflows()
	if !split {
		c := sp[WorkflowCareful]
		stages := slices.Clone(c.Stages)
		for i := range stages {
			if stages[i].Role == "scoper" {
				stages[i].Split = nil
			}
		}
		c.Stages = stages
		sp[WorkflowCareful] = c
	}
	_, userHadDefault := loops["default"]
	for _, name := range []string{WorkflowQuick, WorkflowStandard, WorkflowCareful} {
		_, hasProfile := profiles[name]
		_, hasLoop := loops[name]
		if !hasProfile && (hasLoop || userHadDefault) {
			sp[name] = legacyShippedProfile(name, hasLoop)
		}
	}
	profiles = fill(profiles, sp)
	templates = fill(templates, st)
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
// no label and no source names one, and whether tasks whose workflow's row sets tests start with
// the tester.
// +kubebuilder:object:generate=true
type RepositoryWorkflow struct {
	// Default is the workflow a task runs when nothing else names one; unset is standard.
	// +kubebuilder:validation:MaxLength=253
	Default string `json:"default,omitempty"`
	// TestFirst starts every public task whose workflow's row sets tests with a tester run that
	// writes the failing tests first (v3 feature 4, TF1); needs merge.contractCheck.
	TestFirst bool `json:"testFirst,omitempty"`
	// StepUp is whether a task of the repository steps up without asking: auto (the default), ask
	// (the task waits with a "run again deeper" offer) or off.
	// +kubebuilder:validation:Enum=auto;ask;off
	StepUp string `json:"stepUp,omitempty"`
	// PlanGate controls whether plan-first tasks wait for approval after scoping: ask (default)
	// or auto (approves at once and says so in the room).
	// +kubebuilder:validation:Enum=ask;auto
	PlanGate string `json:"planGate,omitempty"`
	// PlanGateHours is how long an unanswered plan waits before its status comment says it is
	// parked (default 72, 1 to 720). It never expires into a run or a stop on its own.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=720
	PlanGateHours int `json:"planGateHours,omitempty"`
}

// StepUpMode is the repository's step-up policy: auto (default), ask, or off.
func (r *Repository) StepUpMode() string {
	if r != nil && r.Workflow.StepUp != "" {
		return r.Workflow.StepUp
	}
	return "auto"
}

// PlanGateMode is the repository's plan-gate policy: ask (default) or auto.
func (r *Repository) PlanGateMode() string {
	if r != nil && r.Workflow.PlanGate == "auto" {
		return "auto"
	}
	return "ask"
}

// PlanGateHoursOr returns the configured plan-gate wait in hours, defaulting to DefaultPlanGateHours.
func (r *Repository) PlanGateHoursOr() int {
	if r != nil && r.Workflow.PlanGateHours > 0 {
		return r.Workflow.PlanGateHours
	}
	return DefaultPlanGateHours
}

// PlanGateWait returns the configured plan-gate wait duration.
func (r *Repository) PlanGateWait() time.Duration {
	return time.Duration(r.PlanGateHoursOr()) * time.Hour
}

// UnmarshalJSON decodes RepositoryWorkflow strictly, reading YAML 1.1's unquoted stepUp: off
// (boolean false) as "off".
func (w *RepositoryWorkflow) UnmarshalJSON(b []byte) error {
	var raw struct {
		Default       string          `json:"default,omitempty"`
		TestFirst     bool            `json:"testFirst,omitempty"`
		StepUp        json.RawMessage `json:"stepUp,omitempty"`
		PlanGate      string          `json:"planGate,omitempty"`
		PlanGateHours int             `json:"planGateHours,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*w = RepositoryWorkflow{
		Default:       raw.Default,
		TestFirst:     raw.TestFirst,
		PlanGate:      raw.PlanGate,
		PlanGateHours: raw.PlanGateHours,
	}
	switch s := string(raw.StepUp); s {
	case "", "null":
	case "false":
		w.StepUp = "off"
	default:
		var v string
		if err := json.Unmarshal(raw.StepUp, &v); err != nil {
			return fmt.Errorf("workflow.stepUp is auto, ask or off: %s", s)
		}
		w.StepUp = v
	}
	return nil
}

// StepUpFor is the workflow a task running workflow steps up to for reason, or "" when it does not
// step up. A workflow's stepUp applies to verified failure triggers (budget-run, stalled, not_met,
// review_rounds_exhausted, ci_red, budget-task); legacy escalation maps its two signal groups.
func (c *Config) StepUpFor(workflow, reason string) string {
	p, ok := c.Workflow(workflow)
	if !ok {
		return ""
	}
	if p.StepUp != "" {
		switch reason {
		case "budget-run", "stalled", "not_met", "review_rounds_exhausted", "ci_red", "budget-task":
			return p.StepUp
		}
		return ""
	}
	if p.Escalation == nil {
		return ""
	}
	switch reason {
	case "budget-run", "stalled":
		return p.Escalation.OutOfBudget
	case "not_met", "review_rounds_exhausted":
		return p.Escalation.NotConverged
	}
	return ""
}

func (c *Config) stepUpHasCycle(start string) bool {
	seen := map[string]bool{start: true}
	cur := c.HarnessProfiles[start].StepUp
	for cur != "" {
		if seen[cur] {
			return true
		}
		seen[cur] = true
		p, ok := c.HarnessProfiles[cur]
		if !ok {
			return false
		}
		cur = p.StepUp
	}
	return false
}

func (c *Config) workflowTierIndex(p HarnessProfile) int {
	t := p.EffectiveTier()
	if t == "" {
		t = "standard"
	}
	return slices.Index(tiers(), t)
}

func (c *Config) workflowCeilingLower(from, to HarnessProfile) bool {
	fromTier, toTier := from.EffectiveTier(), to.EffectiveTier()
	if fromTier == "" {
		fromTier = "standard"
	}
	if toTier == "" {
		toTier = "standard"
	}
	fromUSD, toUSD := c.Tiers[fromTier].TaskUSD, c.Tiers[toTier].TaskUSD
	if fromUSD > 0 && toUSD > 0 {
		return toUSD < fromUSD
	}
	return c.workflowTierIndex(to) < c.workflowTierIndex(from)
}

// DefaultWorkflow is the workflow a task of the repository runs when nothing else names one (WF3):
// workflow.default, else standard, which a repository the config does not serve (nil) runs too.
func (r *Repository) DefaultWorkflow() string {
	if r != nil && r.Workflow.Default != "" {
		return r.Workflow.Default
	}
	return WorkflowStandard
}

// TestFirstIn is whether a public task of r running workflow starts with the tester: the
// repository opts in (or the workflow declares an unconditional tester stage), and the workflow
// includes a tester stage or its row sets tests. A repository not served (nil) runs none.
func (c *Config) TestFirstIn(r *Repository, workflow string) bool {
	if r == nil {
		return false
	}
	p, ok := c.Workflow(workflow)
	if !ok {
		return false
	}
	if len(p.Stages) > 0 {
		ts, has := stageByRole(p, "tester")
		if !has {
			return false
		}
		if ts.When == TestWhenRepositoryTestFirst {
			return r.Workflow.TestFirst
		}
		return true
	}
	if !r.Workflow.TestFirst {
		return false
	}
	return c.rowOf(c.RowFor(workflow, p)).Tests
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
	if m := r.Workflow.StepUp; m != "" && m != "auto" && m != "ask" && m != "off" {
		bad("workflow.stepUp %q is not one of auto, ask, off", m)
	}
	if pg := r.Workflow.PlanGate; pg != "" && pg != "ask" && pg != "auto" {
		bad("workflow.planGate %q is not one of ask, auto", pg)
	}
	if h := r.Workflow.PlanGateHours; h != 0 && (h < minPlanGateHours || h > maxPlanGateHours) {
		bad("workflow.planGateHours %d must be %d..%d", h, minPlanGateHours, maxPlanGateHours)
	}
	for _, s := range r.Schedules {
		if s.Workflow != "" && !c.IsWorkflow(s.Workflow) {
			bad("schedule %s: workflow %q is not a workflow", s.Name, s.Workflow)
		}
	}
}

// validateWorkflowTuning checks the per-role effort and model overrides (CP9, CP10). A provider
// alias under an internal default is refused at load; under a public default, ModelFor still keeps
// it off an internal run.
func (c *Config) validateWorkflowTuning(bad func(string, ...any), name string, p HarnessProfile) {
	allRoles := c.roles()
	for _, role := range slices.Sorted(maps.Keys(p.Effort)) {
		key := "workflows." + name + ".effort." + role
		switch effort := string(p.Effort[role]); {
		case !slices.Contains(allRoles, role):
			bad("workflows.%s.effort: unknown role %q", name, role)
		case role == "implementer":
			bad("%s: the implementer's effort is not settable", key)
		case !slices.Contains([]string{"low", "medium", "high"}, effort):
			bad("%s %q is low, medium or high", key, effort)
		}
	}
	for _, role := range slices.Sorted(maps.Keys(p.Models)) {
		alias, key := string(p.Models[role]), "workflows."+name+".models."+role
		_, provider := providerModels()[alias]
		switch {
		case !slices.Contains(allRoles, role):
			bad("workflows.%s.models: unknown role %q", name, role)
			continue
		case role == "implementer":
			bad("%s: the implementer's model is not settable", key)
		case !slices.Contains(models(), alias) && !provider:
			bad("%s: %q is not a model alias", key, alias)
		case c.Defaults.DataClass == "internal" && !slices.Contains([]string{"tier-light", "tier-standard", "tier-frontier"}, alias):
			bad("%s %s: an internal task takes tier-light, tier-standard or tier-frontier", key, alias)
		case provider:
			c.needsPrice(bad, key, alias)
		}
	}
	if len(p.Models) > 0 && !c.Runs.GatewayIdentity {
		bad("workflows.%s.models needs runs.gatewayIdentity (a model gateway): the portable runtime ignores a run's model alias", name)
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
	if tmpl := c.TemplateFor(r.DefaultWorkflow()); tmpl != "" {
		return tmpl
	}
	return "pair"
}
