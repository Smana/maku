// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Evaluation is the team evaluation's switch: the control cohort's share of unlabelled
// tasks, whether the scoper's recommendation is only recorded (observe) or used (apply), and the
// workflow the treatment cohort runs where its class allows it (WF8); unset, both cohorts run the floor.
// +kubebuilder:object:generate=true
type Evaluation struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ControlPercent int `json:"controlPercent,omitempty"`
	// +kubebuilder:validation:Enum=observe;apply
	Mode string `json:"mode,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	Treatment string `json:"treatment,omitempty"`
	// TreatmentHarness selects the harness for the treatment cohort without changing workflow,
	// template, loop, checks, model, effort, or tier. Empty uses defaults.harness (openhands).
	// +kubebuilder:validation:Enum=openhands;opencode
	// +optional
	TreatmentHarness string `json:"treatmentHarness,omitempty"`
	// TreatmentModel puts the treatment cohort's TreatmentRoles on a gateway alias (Gemini plan A8),
	// changing nothing else: workflow, template, harness, effort and caps stay the control's. A
	// provider alias applies to public tasks only. The measurement's one variable: it is refused
	// beside treatmentHarness or a treatment workflow.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	TreatmentModel string `json:"treatmentModel,omitempty"`
	// TreatmentRoles are the roles TreatmentModel moves; unset, the implementer.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=32
	// +optional
	TreatmentRoles []string `json:"treatmentRoles,omitempty"`
}

// ModelRoles are the roles TreatmentModel moves: TreatmentRoles, else the implementer.
func (e Evaluation) ModelRoles() []string {
	if len(e.TreatmentRoles) > 0 {
		return e.TreatmentRoles
	}
	return []string{"implementer"}
}

// The evaluation modes; unset is observe.
const (
	EvaluationObserve = "observe"
	EvaluationApply   = "apply"
)

// MaxDescription bounds a profile's description, shown in the scoper's brief.
const MaxDescription = 120

// Applies reports whether a recommended profile is used rather than only recorded.
func (c *Config) Applies() bool { return c.Evaluation.Mode == EvaluationApply }

// Workflow is the workflow called name, false for an unknown name or none, which runs nothing.
func (c *Config) Workflow(name string) (HarnessProfile, bool) {
	if !c.IsWorkflow(name) {
		return HarnessProfile{}, false
	}
	return c.HarnessProfiles[name], true
}

// RowFor is the loops row a task running workflow p, called name, reads: the workflow's loop, else
// the row named after it; rowOf reads default's for a name with no row (WF10, stage B).
func (c *Config) RowFor(name string, p HarnessProfile) string {
	if p.Loop != "" {
		return string(p.Loop)
	}
	return name
}

// rowOf is name's row laid over default's, key by key (WF10), else default's; off is no row at all.
func (c *Config) rowOf(name string) Loop {
	if name == string(LoopOff) {
		return Loop{}
	}
	def := c.Loops["default"]
	row, ok := c.Loops[name]
	if !ok || name == "default" {
		return def
	}
	return row.over(def)
}

// over is l with each key it leaves unset taken from def: models role by role and tier by tier,
// split whole. Neither row is changed.
func (l Loop) over(def Loop) Loop {
	out := l
	if out.Ask == nil {
		out.Ask = def.Ask
	}
	if out.Review == nil {
		out.Review = def.Review
	}
	if out.StallSteps == nil {
		out.StallSteps = def.StallSteps
	}
	if out.Continuations == nil {
		out.Continuations = def.Continuations
	}
	// A row that sets a loop key of its own never inherits default's "no judge": it read the
	// design's judge before default carried the stall rule, and keeps it.
	if out.Judge == "" && (def.Judge != NoJudge || (l.StallSteps == nil && l.Continuations == nil)) {
		out.Judge = def.Judge
	}
	if out.Scope == "" && out.Plan == nil {
		out.Scope = def.Scope
		out.Plan = def.Plan
	}
	if !out.searchSet && !out.Search { // a row built in Go with search true set it too
		out.Search = def.Search
	}
	if !out.testsSet && !out.Tests { // a row built in Go with tests true set it too
		out.Tests = def.Tests
	}
	if out.Split == nil {
		out.Split = def.Split
	}
	if len(def.Models) > 0 {
		out.Models = map[string]ModelAliases{}
		for _, m := range []map[string]ModelAliases{def.Models, l.Models} {
			for role, byTier := range m {
				merged := maps.Clone(out.Models[role])
				if merged == nil {
					merged = ModelAliases{}
				}
				maps.Copy(merged, byTier)
				out.Models[role] = merged
			}
		}
	}
	return out
}

// UnmarshalJSON decodes a row strictly, recording whether it writes search (searchSet) or tests (testsSet).
func (l *Loop) UnmarshalJSON(b []byte) error {
	type plain Loop
	var row plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&row); err != nil {
		return err
	}
	var probe struct {
		Search *bool `json:"search"`
		Tests  *bool `json:"tests"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	*l = Loop(row)
	l.searchSet = probe.Search != nil
	l.testsSet = probe.Tests != nil
	return nil
}

// inheritedKeys are the keys default's row gives name's row, which the row leaves unset (WF10).
func (c *Config) inheritedKeys(name string) []string {
	def, row := c.Loops["default"], c.Loops[name]
	var keys []string
	for _, k := range []struct {
		key        string
		row, given bool
	}{
		{"ask", row.Ask != nil, def.Ask != nil},
		{"plan", row.Plan != nil || row.Scope != "", def.Plan != nil},
		{"review", row.Review != nil, def.Review != nil},
		{"stallSteps", row.StallSteps != nil, def.StallSteps != nil},
		{"continuations", row.Continuations != nil, def.Continuations != nil},
		{"judge", row.Judge != "", def.Judge != ""},
		{"scope", row.Scope != "" || row.Plan != nil, def.Scope != "" && def.Scope != ScopeOff},
		{"search", row.searchSet || row.Search, def.Search},
		{"tests", row.testsSet || row.Tests, def.Tests},
		{"split", row.Split != nil, def.Split != nil},
	} {
		if !k.row && k.given {
			keys = append(keys, k.key)
		}
	}
	for _, role := range slices.Sorted(maps.Keys(def.Models)) {
		for _, tier := range slices.Sorted(maps.Keys(def.Models[role])) {
			if _, own := row.Models[role][tier]; !own {
				keys = append(keys, "models."+role+"."+tier)
			}
		}
	}
	return keys
}

// inheritanceWarnings name, for each row but default's, the keys it now inherits from default's
// (WF10), and whether that switches its loop on: before WF10 the row was read whole.
func (c *Config) inheritanceWarnings() []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(c.Loops)) {
		keys := c.inheritedKeys(name)
		if name == "default" || len(keys) == 0 {
			continue
		}
		w := fmt.Sprintf("loops.%s now inherits default's %s", name, strings.Join(keys, ", "))
		if !c.Loops[name].On() && c.rowOf(name).On() {
			w += ": its loop switches on"
		}
		out = append(out, w+"; set the key on the row to keep its own value (WF10)")
	}
	return out
}

func (c *Config) validateEvaluation(bad func(string, ...any)) {
	switch c.Evaluation.Mode {
	case "", EvaluationObserve, EvaluationApply:
	default:
		bad("evaluation.mode is observe or apply, not %q", c.Evaluation.Mode)
	}
	if p := c.Evaluation.ControlPercent; p < 0 || p > 100 {
		bad("evaluation.controlPercent is 0 to 100")
	}
	if n := c.Evaluation.Treatment; n != "" {
		if !c.IsWorkflow(n) {
			bad("evaluation.treatment %q is not a workflow", n)
		}
	}
	if h := c.Evaluation.TreatmentHarness; h != "" && !ValidHarness(h) {
		bad("evaluation.treatmentHarness %q is not openhands or opencode", h)
	}
	e := c.Evaluation
	if m := e.TreatmentModel; m != "" {
		if !IsAlias(m) {
			bad("evaluation.treatmentModel %q is not a gateway alias", m)
		}
		if e.TreatmentHarness != "" || e.Treatment != "" {
			bad("evaluation.treatmentModel is the measurement's one variable: unset evaluation.treatmentHarness and evaluation.treatment")
		}
	} else if len(e.TreatmentRoles) > 0 {
		bad("evaluation.treatmentRoles needs evaluation.treatmentModel")
	}
	for _, r := range e.TreatmentRoles {
		if !slices.Contains(templateRoles(), r) {
			bad("evaluation.treatmentRoles: unknown role %q", r)
		}
	}
}
