// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxWorkflowStages bounds HarnessProfile.Stages (WR11).
const MaxWorkflowStages = 8

// MaxStageReviewRounds bounds a reviewer stage's maxRounds.
const MaxStageReviewRounds = 5

// JudgeEvidence is the list-form spelling for the default completion judge (WR11).
const JudgeEvidence = "evidence"

// GateWhenPlanFirst and GateWhenAlways are the valid gate.when values on a scoper stage.
const (
	GateWhenPlanFirst = "plan-first"
	GateWhenAlways    = "always"
)

// TestWhenRepositoryTestFirst is the stage condition gating the tester on Repository.spec.workflow.testFirst.
const TestWhenRepositoryTestFirst = "repository.testFirst"

// stageRank orders the built-in stages of a linear workflow: scoper < tester < implementer < reviewer.
var stageRank = map[string]int{
	"scoper":      0,
	"tester":      1,
	"implementer": 2,
	"reviewer":    3,
}

// compile validates and compiles a list-form workflow definition (WR11) into its resolved Stages
// and Template. A workflow sets stages or the old keys (template, tier, loop), not both; the list
// is linear with exactly one implementer, scoper and tester before it, reviewer after it, and at
// most one loop option and one gate per stage.
func (c *Config) compile(name string, p HarnessProfile) (Stages, Template, error) {
	var errs []error
	bad := func(f string, a ...any) {
		errs = append(errs, fmt.Errorf("workflows.%s: "+f, append([]any{name}, a...)...))
	}

	if len(p.Stages) == 0 {
		return Stages{}, Template{}, fmt.Errorf("workflows.%s: stages is empty", name)
	}
	var oldKeys []string
	if p.Template != "" {
		oldKeys = append(oldKeys, "template")
	}
	if p.Tier != "" {
		oldKeys = append(oldKeys, "tier")
	}
	if p.Loop != "" {
		oldKeys = append(oldKeys, "loop")
	}
	if p.Checks {
		oldKeys = append(oldKeys, "checks")
	}
	if len(oldKeys) > 0 {
		bad("set stages or the legacy keys (%s), not both", strings.Join(oldKeys, ", "))
	}
	if p.Budget != "" && !slices.Contains(tiers(), p.Budget) {
		bad("budget %q is not one of %v", p.Budget, tiers())
	}
	if len(p.Stages) > MaxWorkflowStages {
		bad("stages holds %d entries, at most %d", len(p.Stages), MaxWorkflowStages)
	}

	var st Stages
	st.Ask = true
	tmpl := Template{Roles: []string{"implementer"}}

	seen := map[string]bool{}
	lastRank := -1
	implementers := 0

	for i, s := range p.Stages {
		rank, known := stageRank[s.Role]
		if !known {
			bad("stages[%d]: unknown role %q (built-in stage roles are scoper, tester, implementer, reviewer)", i, s.Role)
			continue
		}
		if seen[s.Role] {
			bad("stages[%d]: duplicate role %q; a stage list is linear", i, s.Role)
			continue
		}
		if rank < lastRank {
			bad("stages[%d]: %s is out of order; a stage list is linear (scoper -> tester -> implementer -> reviewer)", i, s.Role)
		}
		seen[s.Role] = true
		if rank > lastRank {
			lastRank = rank
		}

		switch s.Role {
		case "scoper":
			st.Plan = true
			if s.When != "" || s.Checks || s.Search || s.Stall || s.Judge != "" || s.MaxRounds != 0 {
				bad("stages[%d] (scoper): accepts split and gate only", i)
			}
			if s.Split != nil {
				if s.Split.MaxParts < 2 || s.Split.MaxParts > 5 {
					bad("stages[%d] (scoper): split.maxParts must be 2..5, got %d", i, s.Split.MaxParts)
				} else {
					sp := &SplitPolicy{
						MaxChildren: s.Split.MaxParts,
						ChildTier:   DefaultChildTier,
						AnyTier:     true,
					}
					if c != nil {
						sp.FamilyUSD = 4 * c.Tiers[DefaultChildTier].TaskUSD
					}
					st.Split = sp
				}
			}
			if s.Gate != nil {
				switch s.Gate.When {
				case GateWhenPlanFirst, GateWhenAlways:
					st.Gate = s.Gate.When
				default:
					bad("stages[%d] (scoper): gate.when %q is %s or %s", i, s.Gate.When, GateWhenPlanFirst, GateWhenAlways)
				}
			}

		case "tester":
			st.Tests = true
			if s.When != "" && s.When != TestWhenRepositoryTestFirst {
				bad("stages[%d] (tester): when %q is %s or empty", i, s.When, TestWhenRepositoryTestFirst)
			}
			if s.Checks || s.Search || s.Stall || s.Judge != "" || s.MaxRounds != 0 || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (tester): accepts when only", i)
			}

		case "implementer":
			implementers++
			if s.When != "" || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (implementer): does not accept when, split or gate", i)
			}
			st.Checks = s.Checks
			st.Search = s.Search
			st.Stall = s.Stall
			if s.MaxRounds < 0 || s.MaxRounds > MaxContinuations {
				bad("stages[%d] (implementer): maxRounds must be 0..%d, got %d", i, MaxContinuations, s.MaxRounds)
			}
			switch s.Judge {
			case "":
			case JudgeEvidence:
				st.Judge = DefaultJudge
			default:
				if !slices.Contains(judges(), s.Judge) {
					bad("stages[%d] (implementer): judge %q is %s or one of %v", i, s.Judge, JudgeEvidence, judges())
				} else {
					st.Judge = s.Judge
				}
			}

		case "reviewer":
			st.Review = true
			if s.When != "" || s.Checks || s.Search || s.Stall || s.Judge != "" || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (reviewer): accepts maxRounds only", i)
			}
			if s.MaxRounds < 0 || s.MaxRounds > MaxStageReviewRounds {
				bad("stages[%d] (reviewer): maxRounds must be 0..%d, got %d", i, MaxStageReviewRounds, s.MaxRounds)
			}
			tmpl.Roles = []string{"implementer", "reviewer"}
			tmpl.MaxReviewRounds = s.MaxRounds
		}
	}

	if implementers != 1 {
		bad("stages must hold exactly one implementer, got %d", implementers)
	}
	if len(errs) > 0 {
		return Stages{}, Template{}, errors.Join(errs...)
	}
	return st, tmpl, nil
}

// validateWorkflowStages validates a list-form workflow during Config.Validate, including runtime
// prerequisites (meter + prices + gatewayIdentity for split, prices for a public-only judge).
func (c *Config) validateWorkflowStages(bad func(string, ...any), name string, p HarnessProfile) {
	st, _, err := c.compile(name, p)
	if err != nil {
		for _, e := range strings.Split(err.Error(), "\n") {
			bad("%s", e)
		}
		return
	}
	if st.Judge != "" && PublicOnly(st.Judge) {
		c.needsPrice(bad, "workflows."+name+".stages.implementer.judge", st.Judge)
	}
	if st.Split != nil {
		switch {
		case c.Meter.URL == "" || c.Meter.Query == "":
			bad("workflows.%s.stages: split needs a meter: a family's cap is in USD", name)
		case len(c.Meter.Prices) == 0:
			bad("workflows.%s.stages: split needs meter.prices: a family's cap is in USD", name)
		case !c.Runs.GatewayIdentity:
			bad("workflows.%s.stages: split needs runs.gatewayIdentity (a model gateway): this runtime meters no USD, so familyUSD would never be spent", name)
		}
	}
}

// stageByRole returns the first StageDef in p.Stages with role, if any.
func stageByRole(p HarnessProfile, role string) (StageDef, bool) {
	for _, s := range p.Stages {
		if s.Role == role {
			return s, true
		}
	}
	return StageDef{}, false
}

// compiledLoop resolves a list-form workflow's implementer loop policy, nil when neither stall nor
// judge nor maxRounds is set.
func compiledLoop(p HarnessProfile) *LoopPolicy {
	impl, ok := stageByRole(p, "implementer")
	if !ok {
		return nil
	}
	if !impl.Stall && impl.Judge == "" && impl.MaxRounds == 0 {
		return nil
	}
	lp := &LoopPolicy{StallSteps: DefaultStallSteps, Continuations: DefaultContinuations}
	if impl.MaxRounds > 0 {
		lp.Continuations = impl.MaxRounds
	}
	switch impl.Judge {
	case "":
	case JudgeEvidence:
		lp.JudgeModel = DefaultJudge
	default:
		lp.JudgeModel = impl.Judge
	}
	return lp
}

// compiledScope resolves a list-form workflow's scope mode.
func compiledScope(p HarnessProfile) ScopeMode {
	if _, ok := stageByRole(p, "scoper"); ok {
		return ScopeScoper
	}
	return ScopeOff
}

// compiledSearch resolves whether a list-form workflow enables search on its implementer.
func compiledSearch(p HarnessProfile) bool {
	impl, ok := stageByRole(p, "implementer")
	return ok && impl.Search
}

// compiledSplit resolves a list-form workflow's SplitPolicy, nil when none.
func (c *Config) compiledSplit(p HarnessProfile) *SplitPolicy {
	sc, ok := stageByRole(p, "scoper")
	if !ok || sc.Split == nil {
		return nil
	}
	sp := SplitPolicy{
		MaxChildren: sc.Split.MaxParts,
		ChildTier:   DefaultChildTier,
		AnyTier:     true,
		FamilyUSD:   4 * c.Tiers[DefaultChildTier].TaskUSD,
	}
	return &sp
}

// MaxReviewRoundsFor returns the review round cap for workflow, reading the compiled reviewer
// stage when workflow uses the list form, else fallbackTemplate's MaxReviewRounds.
func (c *Config) MaxReviewRoundsFor(workflow, fallbackTemplate string) int32 {
	if p, ok := c.Workflow(workflow); ok && len(p.Stages) > 0 {
		if rev, has := stageByRole(p, "reviewer"); has {
			return rev.MaxRounds
		}
		return 0
	}
	return c.Templates[fallbackTemplate].MaxReviewRounds
}
