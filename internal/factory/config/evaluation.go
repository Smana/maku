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

// ProfileFor is the profile name names for cl: its floor or one of its variants, never another.
func (c *Config) ProfileFor(cl Class, name string) (HarnessProfile, bool) {
	if name == "" || (name != cl.Harness && !slices.Contains(cl.HarnessAllowed, name)) {
		return HarnessProfile{}, false
	}
	p, ok := c.HarnessProfiles[name]
	return p, ok
}

// RowFor is the loops row a task of class runs under profile p: the profile's, else the class's.
func (c *Config) RowFor(class string, p HarnessProfile) string {
	if p.Loop != "" {
		return string(p.Loop)
	}
	return class
}

// VariantProblems is every reason cl's floor, variants and escalation are refused (TE2, TE6, TE7).
func (c *Config) VariantProblems(class string, cl Class) []string {
	return variantProblems(c.HarnessProfiles, cl, func(floor HarnessProfile, name string, v HarnessProfile, bad func(string, ...any)) {
		c.belowFloor(class, floor, name, v, bad)
	})
}

// VariantProblemsIn is VariantProblems against the profiles alone, for a class resolved outside a
// config: every profile named exists, and the escalation stays within harnessAllowed. Comparing a
// variant to its floor reads the templates and loops, so it is left to Parse's VariantProblems.
func VariantProblemsIn(profiles map[string]HarnessProfile, _ string, cl Class) []string {
	return variantProblems(profiles, cl, nil)
}

// variantProblems is VariantProblems, with the floor comparison handed in (nil skips it).
func variantProblems(profiles map[string]HarnessProfile, cl Class,
	belowFloor func(floor HarnessProfile, name string, v HarnessProfile, bad func(string, ...any))) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	if (cl.Live || cl.Shadow) && (len(cl.HarnessAllowed) > 0 || cl.Escalation != nil) {
		bad("harnessAllowed, escalation: a live or shadow class has one profile (TE2)")
	}
	floor, hasFloor := profiles[cl.Harness]
	if cl.Harness != "" && !hasFloor {
		bad("harness %q is not in harnessProfiles (TE2)", cl.Harness)
	}
	for _, name := range cl.HarnessAllowed {
		v, ok := profiles[name]
		if !ok {
			bad("harnessAllowed %q is not in harnessProfiles (TE2)", name)
			continue
		}
		if hasFloor && belowFloor != nil {
			belowFloor(floor, name, v, bad)
		}
	}
	if e := cl.Escalation; e != nil {
		for _, g := range []struct{ key, target string }{{"outOfBudget", e.OutOfBudget}, {"notConverged", e.NotConverged}} {
			if g.target != "" && !slices.Contains(cl.HarnessAllowed, g.target) {
				bad("escalation %s %q is not in harnessAllowed (TE7)", g.key, g.target)
			}
		}
	}
	return out
}

// belowFloor: the template and loop are compared as they run, a profile without a loop running
// the class's row (RowFor); the tier only when both set one, since a run never lowers its tier.
func (c *Config) belowFloor(class string, floor HarnessProfile, name string, v HarnessProfile, bad func(string, ...any)) {
	switch {
	case floor.Template == "" && v.Template != "":
		bad("harnessAllowed %q sets a template the floor leaves to triage (TE2)", name)
	case floor.Template != "" && v.Template != "":
		roles := c.Templates[v.Template].Roles
		if slices.ContainsFunc(c.Templates[floor.Template].Roles, func(r string) bool { return !slices.Contains(roles, r) }) {
			bad("harnessAllowed %q drops roles of the floor (TE2)", name)
		}
	}
	if floor.Tier != "" && v.Tier != "" && slices.Index(tiers(), v.Tier) < slices.Index(tiers(), floor.Tier) {
		bad("harnessAllowed %q is below the floor's tier (TE2)", name)
	}
	fr, vr := c.RowFor(class, floor), c.RowFor(class, v)
	if c.ScopeFor(fr) != ScopeOff && c.ScopeFor(vr) == ScopeOff {
		bad("harnessAllowed %q drops the floor's scope (TE6)", name)
	}
	if judged := func(row string) bool { p := c.LoopFor(row); return p != nil && p.JudgeModel != "" }; judged(fr) && !judged(vr) {
		bad("harnessAllowed %q drops the floor's judge (TE6)", name)
	}
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
		out.Models = make(map[string]ModelAliases, len(def.Models)+len(l.Models))
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
		if _, ok := c.HarnessProfiles[n]; !ok {
			bad("evaluation.treatment %q is not a workflow in harnessProfiles", n)
		}
	}
	for _, r := range c.Repos() {
		for _, name := range slices.Sorted(maps.Keys(r.Classes)) {
			cl := r.Classes[name]
			for _, p := range c.VariantProblems(name, cl) {
				bad("%s: classes.%s: %s", r.Name, name, p)
			}
			if r.Merge.ContractCheck != "" {
				continue
			}
			for _, pn := range slices.Compact(slices.Sorted(slices.Values(append([]string{cl.Harness}, cl.HarnessAllowed...)))) {
				if c.HarnessProfiles[pn].TestFirst {
					bad("%s: class %s: profile %q: testFirst needs merge.contractCheck", r.Name, name, pn)
				}
			}
		}
	}
}
