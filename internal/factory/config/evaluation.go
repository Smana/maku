// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"cmp"
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
	if out.StallSteps == nil {
		out.StallSteps = def.StallSteps
	}
	if out.Continuations == nil {
		out.Continuations = def.Continuations
	}
	out.Judge = cmp.Or(out.Judge, def.Judge)
	out.Scope = cmp.Or(out.Scope, def.Scope)
	if !out.searchSet && !out.Search { // a row built in Go with search true set it too
		out.Search = def.Search
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

// UnmarshalJSON decodes a row strictly, recording whether it writes search (searchSet).
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
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	*l = Loop(row)
	l.searchSet = probe.Search != nil
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
		{"stallSteps", row.StallSteps != nil, def.StallSteps != nil},
		{"continuations", row.Continuations != nil, def.Continuations != nil},
		{"judge", row.Judge != "", def.Judge != ""},
		{"scope", row.Scope != "", def.Scope != "" && def.Scope != ScopeOff},
		{"search", row.searchSet || row.Search, def.Search},
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
}
