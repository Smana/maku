// SPDX-License-Identifier: Apache-2.0

package config

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
