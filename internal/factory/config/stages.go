// SPDX-License-Identifier: Apache-2.0

package config

import "slices"

// Stages is what a task of a workflow runs, every switch resolved (WR6, WR11).
type Stages struct {
	Ask, Plan, Tests, Search, Review bool
	Checks, Stall                    bool
	Judge                            string       // "" is no judge
	Split                            *SplitPolicy // nil is no split
	Gate                             string       // "" | "plan-first" | "always"
	// ExtraRoles lists active custom read roles after implementer, in compiled stage order.
	ExtraRoles []string
}

// Names lists the active stages in run order: ask, plan, tests, search, judge, review, split,
// followed by any active custom read roles.
func (s Stages) Names() []string {
	var out []string
	if s.Ask {
		out = append(out, "ask")
	}
	if s.Plan {
		out = append(out, "plan")
	}
	if s.Tests {
		out = append(out, "tests")
	}
	if s.Search {
		out = append(out, "search")
	}
	if s.Judge != "" {
		out = append(out, "judge")
	}
	if s.Review {
		out = append(out, "review")
	}
	if s.Split != nil {
		out = append(out, "split")
	}
	out = append(out, s.ExtraRoles...)
	return out
}

// TemplateForProfile resolves the template for workflow p (called name): when p uses the list form
// (stages), pair when a reviewer stage is present else solo; otherwise its explicit template when
// set, else pair when its row sets review: true, solo when review: false, else "".
func (c *Config) TemplateForProfile(name string, p HarnessProfile) string {
	if len(p.Stages) > 0 {
		if _, hasReviewer := activeStageByRole(p, "reviewer", nil); hasReviewer {
			return "pair"
		}
		return "solo"
	}
	if p.Template != "" {
		return p.Template
	}
	row := c.rowOf(c.RowFor(name, p))
	if row.Review != nil {
		if *row.Review {
			return "pair"
		}
		return "solo"
	}
	return ""
}

// TemplateFor resolves workflow's template, "" for none or an unknown workflow.
func (c *Config) TemplateFor(workflow string) string {
	p, ok := c.Workflow(workflow)
	if !ok {
		return ""
	}
	return c.TemplateForProfile(workflow, p)
}

// StagesFor resolves every stage switch of workflow on repository r and merge class (WR6, WR11).
// A workflow in the list form compiles directly from its stages; a legacy row that omits ask, plan
// and review resolves as before: plan from scope: scoper, review from the workflow's template, and
// ask from the scoper's forced question. Any stage in class's requires is added and never removed;
// tests is added only when the repository sets merge.contractCheck.
func (c *Config) StagesFor(r *Repository, workflow, class string) Stages {
	p, ok := c.Workflow(workflow)
	if !ok {
		return Stages{}
	}
	if len(p.Stages) > 0 {
		var addStages []string
		if r != nil {
			addStages = r.Workflow.AddStages
		}
		st, _, err := c.compileFor(workflow, p, addStages)
		if err != nil {
			return Stages{}
		}
		if ts, hasTester := activeStageByRole(p, "tester", addStages); hasTester && ts.When == TestWhenRepositoryTestFirst {
			st.Tests = r != nil && r.Workflow.TestFirst
		}
		c.applyClassRequires(r, class, &st)
		if st.Split != nil && st.Split.FamilyUSD == 0 {
			sp := *st.Split
			sp.FamilyUSD = 4 * c.Tiers[sp.ChildTier].TaskUSD
			st.Split = &sp
		}
		return st
	}
	rowName := c.RowFor(workflow, p)
	row := c.rowOf(rowName)
	tmpl := c.TemplateForProfile(workflow, p)
	inv := c.Investigates(tmpl)

	plan := !inv && row.scopeMode() == ScopeScoper
	ask := plan
	if row.Ask != nil {
		ask = *row.Ask
	}
	tests := !inv && row.Tests && r != nil && r.Workflow.TestFirst
	var judge string
	var stall bool
	if lp := c.LoopFor(rowName); lp != nil {
		judge = lp.JudgeModel
		stall = lp.StallSteps > 0
	}
	var review bool
	if !inv {
		switch {
		case p.Template != "":
			review = slices.Contains(c.Templates[p.Template].Roles, "reviewer")
		case row.Review != nil:
			review = *row.Review
		}
	}
	st := Stages{
		Ask:    ask,
		Plan:   plan,
		Tests:  tests,
		Search: row.Search,
		Checks: p.Checks,
		Stall:  stall,
		Judge:  judge,
		Review: review,
	}
	if !inv {
		c.applyClassRequires(r, class, &st)
	}
	if st.Plan && row.Split != nil {
		sp := *row.Split
		if sp.ChildTier == "" {
			sp.ChildTier = DefaultChildTier
		}
		if sp.FamilyUSD == 0 {
			sp.FamilyUSD = 4 * c.Tiers[sp.ChildTier].TaskUSD
		}
		st.Split = &sp
	}
	return st
}

// applyClassRequires adds any stage required by class on repository r to st; it never removes one.
func (c *Config) applyClassRequires(r *Repository, class string, st *Stages) {
	if r == nil || class == "" {
		return
	}
	cl, ok := r.Class(class)
	if !ok || len(cl.Requires) == 0 {
		return
	}
	for _, req := range cl.Requires {
		switch req {
		case "plan":
			st.Plan = true
		case "tests":
			if r.Merge.ContractCheck != "" {
				st.Tests = true
			}
		case "judge":
			if st.Judge == "" {
				st.Judge = DefaultJudge
			}
		case "review":
			st.Review = true
		}
	}
}

// RequiredStagesFor returns the stages class requires on repository r, in canonical order.
func (c *Config) RequiredStagesFor(r *Repository, class string) []string {
	if r == nil || class == "" {
		return nil
	}
	cl, ok := r.Class(class)
	if !ok || len(cl.Requires) == 0 {
		return nil
	}
	var out []string
	for _, st := range RequiredStages() {
		if slices.Contains(cl.Requires, st) {
			out = append(out, st)
		}
	}
	return out
}
