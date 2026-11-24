// SPDX-License-Identifier: Apache-2.0

package config

import "slices"

// Stages is what a task of a workflow runs, every switch resolved (WR6).
type Stages struct {
	Ask, Plan, Tests, Search, Review bool
	Judge                            string       // "" is no judge
	Split                            *SplitPolicy // nil is no split
}

// Names lists the active stages in run order: ask, plan, tests, search, judge, review, split.
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
	return out
}

// TemplateForProfile resolves the template for workflow p (called name): its explicit template when
// set, else pair when its row sets review: true, solo when review: false, else "".
func (c *Config) TemplateForProfile(name string, p HarnessProfile) string {
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

// StagesFor resolves every stage switch of workflow on repository r (WR6). A row that omits ask,
// plan and review resolves as before: plan from scope: scoper, review from the workflow's template,
// and ask from the scoper's forced question.
func (c *Config) StagesFor(r *Repository, workflow string) Stages {
	p, ok := c.Workflow(workflow)
	if !ok {
		return Stages{}
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
	if lp := c.LoopFor(rowName); lp != nil {
		judge = lp.JudgeModel
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
	var split *SplitPolicy
	if plan && row.Split != nil {
		sp := *row.Split
		if sp.ChildTier == "" {
			sp.ChildTier = DefaultChildTier
		}
		if sp.FamilyUSD == 0 {
			sp.FamilyUSD = 4 * c.Tiers[sp.ChildTier].TaskUSD
		}
		split = &sp
	}
	return Stages{
		Ask:    ask,
		Plan:   plan,
		Tests:  tests,
		Search: row.Search,
		Judge:  judge,
		Review: review,
		Split:  split,
	}
}
