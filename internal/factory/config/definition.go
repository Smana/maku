// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/envelope"
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
// is linear with exactly one implementer, scoper and tester before it, reviewer and declared custom
// read roles after it, and at most one loop option and one gate per stage.
func (c *Config) compile(name string, p HarnessProfile) (Stages, Template, error) {
	return c.compileFor(name, p, nil)
}

// compileFor validates p.Stages in full and resolves active Stages and Template with repository
// opt-in addStages applied to stages marked allowed (WR11, WR12).
func (c *Config) compileFor(name string, p HarnessProfile, addStages []string) (Stages, Template, error) {
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
		isCustomRead := false
		if !known && c != nil {
			if def, ok := c.Roles[s.Role]; ok {
				if def.Capability != CapabilityRead {
					bad("stages[%d]: role %q has capability %q; only read custom roles may be stages", i, s.Role, def.Capability)
					continue
				}
				rank = stageRank["reviewer"]
				known = true
				isCustomRead = true
			}
		}
		if !known {
			bad("stages[%d]: unknown role %q (built-in stage roles are scoper, tester, implementer, reviewer, or a declared read role)", i, s.Role)
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

		active := !s.Allowed || slices.Contains(addStages, s.Role)

		switch {
		case s.Role == "scoper":
			if s.When != "" || s.Checks || s.Search || s.Stall || s.Judge != "" || s.MaxRounds != 0 {
				bad("stages[%d] (scoper): accepts split, gate and allowed only", i)
			}
			var sp *SplitPolicy
			if s.Split != nil {
				if s.Split.MaxParts < 2 || s.Split.MaxParts > 5 {
					bad("stages[%d] (scoper): split.maxParts must be 2..5, got %d", i, s.Split.MaxParts)
				} else {
					sp = &SplitPolicy{
						MaxChildren: s.Split.MaxParts,
						ChildTier:   DefaultChildTier,
						AnyTier:     true,
					}
					if c != nil {
						sp.FamilyUSD = 4 * c.Tiers[DefaultChildTier].TaskUSD
					}
				}
			}
			var gate string
			if s.Gate != nil {
				switch s.Gate.When {
				case GateWhenPlanFirst, GateWhenAlways:
					gate = s.Gate.When
				default:
					bad("stages[%d] (scoper): gate.when %q is %s or %s", i, s.Gate.When, GateWhenPlanFirst, GateWhenAlways)
				}
			}
			if active {
				st.Plan = true
				if sp != nil {
					st.Split = sp
				}
				if gate != "" {
					st.Gate = gate
				}
			}

		case s.Role == "tester":
			if s.When != "" && s.When != TestWhenRepositoryTestFirst {
				bad("stages[%d] (tester): when %q is %s or empty", i, s.When, TestWhenRepositoryTestFirst)
			}
			if s.Checks || s.Search || s.Stall || s.Judge != "" || s.MaxRounds != 0 || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (tester): accepts when and allowed only", i)
			}
			if active {
				st.Tests = true
			}

		case s.Role == "implementer":
			implementers++
			if s.Allowed {
				bad("stages[%d] (implementer): implementer cannot be marked allowed", i)
			}
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

		case s.Role == "reviewer":
			if s.When != "" || s.Checks || s.Search || s.Stall || s.Judge != "" || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (reviewer): accepts maxRounds and allowed only", i)
			}
			if s.MaxRounds < 0 || s.MaxRounds > MaxStageReviewRounds {
				bad("stages[%d] (reviewer): maxRounds must be 0..%d, got %d", i, MaxStageReviewRounds, s.MaxRounds)
			}
			if active {
				st.Review = true
				tmpl.Roles = []string{"implementer", "reviewer"}
				tmpl.MaxReviewRounds = s.MaxRounds
			}

		case isCustomRead:
			if s.When != "" || s.Checks || s.Search || s.Stall || s.Judge != "" || s.Split != nil || s.Gate != nil {
				bad("stages[%d] (%s): accepts maxRounds and allowed only", i, s.Role)
			}
			if s.MaxRounds < 0 || s.MaxRounds > MaxStageReviewRounds {
				bad("stages[%d] (%s): maxRounds must be 0..%d, got %d", i, s.Role, MaxStageReviewRounds, s.MaxRounds)
			}
			if active {
				st.ExtraRoles = append(st.ExtraRoles, s.Role)
			}
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

// activeStageByRole returns the StageDef with role if it is active (not marked allowed, or named
// in addStages).
func activeStageByRole(p HarnessProfile, role string, addStages []string) (StageDef, bool) {
	for _, s := range p.Stages {
		if s.Role != role {
			continue
		}
		if s.Allowed && !slices.Contains(addStages, s.Role) {
			return StageDef{}, false
		}
		return s, true
	}
	return StageDef{}, false
}

// IsCustomReadRole reports whether name is a declared custom role with the read capability.
func (c *Config) IsCustomReadRole(name string) bool {
	if c == nil || name == "" {
		return false
	}
	def, ok := c.Roles[name]
	return ok && def.Capability == CapabilityRead
}

// CompiledStages returns the active stages of workflow on repository r in linear run order,
// resolving both list-form definitions and legacy workflows. Stages marked allowed are included
// only when r names them in workflow.addStages (WR12).
func (c *Config) CompiledStages(r *Repository, workflow string) []StageDef {
	if c == nil {
		return nil
	}
	p, ok := c.Workflow(workflow)
	if !ok {
		return nil
	}
	return c.CompiledStagesForProfile(r, workflow, p)
}

// CompiledStagesForProfile returns the active stages of profile p (named workflow) on repository r
// in linear run order. Used by both the reconciler and workflow dry-run (WR11, WR12).
func (c *Config) CompiledStagesForProfile(r *Repository, workflow string, p HarnessProfile) []StageDef {
	if c == nil || p.Template == TemplateNone {
		return nil
	}
	var addStages []string
	if r != nil {
		addStages = r.Workflow.AddStages
	}
	if len(p.Stages) > 0 {
		out := make([]StageDef, 0, len(p.Stages))
		for _, s := range p.Stages {
			if s.Allowed && !slices.Contains(addStages, s.Role) {
				continue
			}
			out = append(out, s)
		}
		return out
	}
	tmplName := c.TemplateForProfile(workflow, p)
	if c.Investigates(tmplName) {
		return []StageDef{{Role: "triager"}}
	}
	rowName := c.RowFor(workflow, p)
	row := c.rowOf(rowName)
	var out []StageDef
	if row.scopeMode() == ScopeScoper {
		sc := StageDef{Role: "scoper"}
		if row.Split != nil {
			sc.Split = &SplitDef{MaxParts: row.Split.MaxChildren}
		}
		out = append(out, sc)
	}
	if row.Tests {
		out = append(out, StageDef{Role: "tester", When: TestWhenRepositoryTestFirst})
	}
	impl := StageDef{Role: "implementer", Checks: p.Checks, Search: row.Search}
	if lp := c.LoopFor(rowName); lp != nil {
		impl.Stall = lp.StallSteps > 0
		impl.Judge = lp.JudgeModel
		impl.MaxRounds = lp.Continuations
	}
	out = append(out, impl)
	if tmpl, ok := c.Templates[tmplName]; ok && slices.Contains(tmpl.Roles, "reviewer") {
		out = append(out, StageDef{Role: "reviewer", MaxRounds: tmpl.MaxReviewRounds})
	}
	return out
}

// StageMaxRoundsFor returns the review round cap for role in workflow on repository r, falling back
// to fallbackTemplate's MaxReviewRounds for built-in reviewer when the workflow does not name it.
func (c *Config) StageMaxRoundsFor(r *Repository, workflow, role, fallbackTemplate string) int32 {
	for _, s := range c.CompiledStages(r, workflow) {
		if s.Role == role {
			return s.MaxRounds
		}
	}
	if role == "reviewer" && c != nil {
		return c.Templates[fallbackTemplate].MaxReviewRounds
	}
	return 0
}

// EffectiveBudgetTier returns the budget tier for workflow on repository r: the workflow's budget
// (defaulting to standard), narrowed to r.Workflow.Budget when r sets a lower tier (WR12).
func (c *Config) EffectiveBudgetTier(r *Repository, workflow string) string {
	wfTier := "standard"
	if c != nil {
		if p, ok := c.Workflow(workflow); ok {
			if t := p.EffectiveTier(); t != "" {
				wfTier = t
			}
		}
	}
	if r != nil && r.Workflow.Budget != "" {
		if slices.Index(tiers(), r.Workflow.Budget) >= 0 &&
			slices.Index(tiers(), r.Workflow.Budget) < slices.Index(tiers(), wfTier) {
			return r.Workflow.Budget
		}
	}
	return wfTier
}

// MaxCandidateDefinitionBytes bounds an unstored candidate workflow YAML sent to dry-run.
const MaxCandidateDefinitionBytes = 64 * 1024

// CompileCandidate parses, validates, and compiles an unstored candidate workflow YAML definition
// against c without mutating c (WR11, Task I5). rawYAML may be either a single workflow mapping
// (budget, stages, ...) or a document holding workflows (and optional roles) keyed by name.
func (c *Config) CompileCandidate(name string, rawYAML []byte) (HarnessProfile, Stages, error) {
	if !profileRE.MatchString(name) {
		return HarnessProfile{}, Stages{}, fmt.Errorf("workflow %q is not a valid workflow name", name)
	}
	if len(rawYAML) == 0 || len(bytes.TrimSpace(rawYAML)) == 0 {
		return HarnessProfile{}, Stages{}, errors.New("definition is empty")
	}
	if len(rawYAML) > MaxCandidateDefinitionBytes {
		return HarnessProfile{}, Stages{}, fmt.Errorf("definition exceeds %d bytes", MaxCandidateDefinitionBytes)
	}
	js, err := yaml.YAMLToJSON(rawYAML)
	if err != nil {
		return HarnessProfile{}, Stages{}, fmt.Errorf("invalid YAML: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(js, &top); err != nil {
		return HarnessProfile{}, Stages{}, fmt.Errorf("definition must be a YAML mapping: %w", err)
	}

	// Build a read-only shallow clone of c so compilation never mutates the live Config.
	tmp := Config{
		Defaults:        c.Defaults,
		Tiers:           c.Tiers,
		Templates:       c.Templates,
		Loops:           c.Loops,
		Meter:           c.Meter,
		Runs:            c.Runs,
		HarnessProfiles: maps.Clone(c.HarnessProfiles),
		Roles:           maps.Clone(c.Roles),
	}
	if tmp.Roles == nil {
		tmp.Roles = map[string]RoleDef{}
	}

	var p HarnessProfile
	if _, hasWorkflows := top["workflows"]; hasWorkflows {
		var doc struct {
			Workflows map[string]HarnessProfile `json:"workflows"`
			Roles     map[string]RoleDef        `json:"roles,omitempty"`
		}
		dec := json.NewDecoder(bytes.NewReader(js))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return HarnessProfile{}, Stages{}, fmt.Errorf("invalid definition: %w", err)
		}
		var roleErrs []error
		for _, rname := range slices.Sorted(maps.Keys(doc.Roles)) {
			rdef := doc.Roles[rname]
			if !profileRE.MatchString(rname) || !envelope.ValidPersona(rname) {
				roleErrs = append(roleErrs, fmt.Errorf("roles: %q is not a valid role name", rname))
				continue
			}
			if isBuiltInRole(rname) {
				roleErrs = append(roleErrs, fmt.Errorf("roles.%s: built-in role name is reserved", rname))
				continue
			}
			if rdef.Capability != CapabilityRead {
				roleErrs = append(roleErrs, fmt.Errorf("roles.%s: capability %q is invalid; custom roles may bind read only", rname, rdef.Capability))
				continue
			}
			if rdef.Brief != "" {
				if _, berr := cleanBriefPath(rdef.Brief); berr != nil {
					roleErrs = append(roleErrs, fmt.Errorf("roles.%s.brief: %w", rname, berr))
					continue
				}
			}
			tmp.Roles[rname] = rdef
		}
		if len(roleErrs) > 0 {
			return HarnessProfile{}, Stages{}, errors.Join(roleErrs...)
		}
		var ok bool
		p, ok = doc.Workflows[name]
		if !ok {
			return HarnessProfile{}, Stages{}, fmt.Errorf("workflow %q not found in definition", name)
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader(js))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return HarnessProfile{}, Stages{}, fmt.Errorf("invalid workflow definition: %w", err)
		}
	}

	if len(p.Stages) > 0 {
		st, _, err := tmp.compile(name, p)
		if err != nil {
			return HarnessProfile{}, Stages{}, err
		}
		return p, st, nil
	}
	if p.Template == "" {
		return HarnessProfile{}, Stages{}, fmt.Errorf("workflows.%s: stages is empty", name)
	}
	if p.Template != TemplateNone && !slices.Contains(templates(), p.Template) {
		return HarnessProfile{}, Stages{}, fmt.Errorf("workflows.%s: template %q is not one of %v", name, p.Template, templates())
	}
	if p.Budget != "" && !slices.Contains(tiers(), p.Budget) {
		return HarnessProfile{}, Stages{}, fmt.Errorf("workflows.%s: budget %q is not one of %v", name, p.Budget, tiers())
	}
	if p.Tier != "" && !slices.Contains(tiers(), p.Tier) {
		return HarnessProfile{}, Stages{}, fmt.Errorf("workflows.%s: tier %q is not one of %v", name, p.Tier, tiers())
	}
	if tmp.HarnessProfiles == nil {
		tmp.HarnessProfiles = map[string]HarnessProfile{}
	}
	tmp.HarnessProfiles[name] = p
	return p, tmp.StagesFor(nil, name, ""), nil
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
