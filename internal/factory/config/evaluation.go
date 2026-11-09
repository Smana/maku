// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"maps"
	"slices"
)

// Evaluation is the team evaluation's switch (TE): the control cohort's share of unlabelled
// tasks, and whether the scoper's recommendation is only recorded (observe) or used (apply).
type Evaluation struct {
	ControlPercent int    `json:"controlPercent,omitempty"`
	Mode           string `json:"mode,omitempty"`
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
func (c *Config) VariantProblems(cl Class) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	if (cl.Live || cl.Shadow) && (len(cl.HarnessAllowed) > 0 || cl.Escalation != nil) {
		bad("harnessAllowed, escalation: a live or shadow class has one profile (TE2)")
	}
	floor, hasFloor := c.HarnessProfiles[cl.Harness]
	if cl.Harness != "" && !hasFloor {
		bad("harness %q is not in harnessProfiles (TE2)", cl.Harness)
	}
	for _, name := range cl.HarnessAllowed {
		v, ok := c.HarnessProfiles[name]
		if !ok {
			bad("harnessAllowed %q is not in harnessProfiles (TE2)", name)
			continue
		}
		if hasFloor {
			c.belowFloor(floor, name, v, bad)
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

// belowFloor compares only what both profiles set: an empty template or tier is today's selection.
// A loop is compared only when both name one, as the template and tier are.
func (c *Config) belowFloor(floor HarnessProfile, name string, v HarnessProfile, bad func(string, ...any)) {
	if floor.Template != "" && v.Template != "" {
		roles := c.Templates[v.Template].Roles
		if slices.ContainsFunc(c.Templates[floor.Template].Roles, func(r string) bool { return !slices.Contains(roles, r) }) {
			bad("harnessAllowed %q drops roles of the floor (TE2)", name)
		}
	}
	if floor.Tier != "" && v.Tier != "" && slices.Index(tiers(), v.Tier) < slices.Index(tiers(), floor.Tier) {
		bad("harnessAllowed %q is below the floor's tier (TE2)", name)
	}
	if floor.Loop == "" || v.Loop == "" {
		return
	}
	fr, vr := string(floor.Loop), string(v.Loop)
	if c.ScopeFor(fr) != ScopeOff && c.ScopeFor(vr) == ScopeOff {
		bad("harnessAllowed %q drops the floor's scope (TE6)", name)
	}
	if j := c.rowOf(fr).Judge; j != "" && j != NoJudge {
		if p := c.LoopFor(vr); p == nil || p.JudgeModel == "" {
			bad("harnessAllowed %q drops the floor's judge (TE6)", name)
		}
	}
}

// rowOf is name's row, else default's; off is no row at all.
func (c *Config) rowOf(name string) Loop {
	if name == string(LoopOff) {
		return Loop{}
	}
	row, ok := c.Loops[name]
	if !ok {
		row = c.Loops["default"]
	}
	return row
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
	for _, r := range c.Repos() {
		for _, name := range slices.Sorted(maps.Keys(r.Classes)) {
			for _, p := range c.VariantProblems(r.Classes[name]) {
				bad("%s: classes.%s: %s", r.Name, name, p)
			}
		}
	}
}
