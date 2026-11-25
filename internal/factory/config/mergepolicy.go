// SPDX-License-Identifier: Apache-2.0

package config

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TemplateNone is the harness of an adopted class: another author's pull requests, no run.
const TemplateNone = "none"

// LoopOff is a profile with no loop.
const LoopOff LoopRef = "off"

// LoopRef names a row of loops, or off.
// +kubebuilder:validation:MaxLength=253
type LoopRef string

// UnmarshalJSON reads a string, or false as off: YAML reads a bare off as false.
func (l *LoopRef) UnmarshalJSON(b []byte) error {
	if string(b) == "false" {
		*l = LoopOff
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("loop is off or a row of loops: %s", b)
	}
	*l = LoopRef(s)
	return nil
}

// Effort is a role's reasoning effort (CP9).
// +kubebuilder:validation:Enum=low;medium;high
// +kubebuilder:validation:MaxLength=6
type Effort string

// StageDef is one stage in a workflow's ordered list (WR11).
// +kubebuilder:object:generate=true
type StageDef struct {
	// +kubebuilder:validation:MaxLength=63
	Role string `json:"role"`
	// When is an optional condition on the stage (e.g. repository.testFirst).
	// +kubebuilder:validation:MaxLength=63
	When   string `json:"when,omitempty"`
	Checks bool   `json:"checks,omitempty"`
	Search bool   `json:"search,omitempty"`
	Stall  bool   `json:"stall,omitempty"`
	// Judge is evidence (the default completion judge) or a model alias.
	// +kubebuilder:validation:MaxLength=253
	Judge string `json:"judge,omitempty"`
	// MaxRounds is the stage's one bounded loop.
	// +kubebuilder:validation:Minimum=0
	MaxRounds int32     `json:"maxRounds,omitempty"`
	Split     *SplitDef `json:"split,omitempty"`
	Gate      *GateDef  `json:"gate,omitempty"`
	// Allowed marks this stage optional for repositories to opt into via Repository.spec.workflow.addStages (WR12).
	Allowed bool `json:"allowed,omitempty"`
}

// SplitDef configures task splitting on a scoper stage (WR11).
// +kubebuilder:object:generate=true
type SplitDef struct {
	// MaxParts is the most parts a split may hold: 2 to 5.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=5
	MaxParts int32 `json:"maxParts"`
}

// GateDef configures an approval gate on a stage (WR11).
// +kubebuilder:object:generate=true
type GateDef struct {
	// When is plan-first or always.
	// +kubebuilder:validation:Enum=plan-first;always
	// +kubebuilder:validation:MaxLength=16
	When string `json:"when"`
}

// HarnessProfile is one workflow (WF1, WF7, WR11): either the readable list form (budget and
// stages) or the legacy bundle (template, tier, checks and loop), plus description, step-up and
// per-role tuning. A Factory spells the map workflows.
// +kubebuilder:object:generate=true
type HarnessProfile struct {
	// +kubebuilder:validation:MaxLength=253
	Template string `json:"template,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	Tier string `json:"tier,omitempty"`
	// Budget is the tier under the name a person reads (WR11): light, standard or frontier.
	// +kubebuilder:validation:Enum=light;standard;frontier
	// +kubebuilder:validation:MaxLength=8
	Budget string `json:"budget,omitempty"`
	// Stages is the workflow's ordered stage list (WR11): mutually exclusive with template, tier and loop.
	// +kubebuilder:validation:MaxItems=8
	Stages []StageDef `json:"stages,omitempty"`
	Checks bool       `json:"checks,omitempty"`
	Loop   LoopRef    `json:"loop,omitempty"`
	// Description is the line the scoper's brief shows: when to choose the workflow.
	// +kubebuilder:validation:MaxLength=120
	Description string `json:"description,omitempty"`
	// Escalation is the workflow a failed run's signal group steps to (WF7, TE7); absent, or an
	// absent group, goes to a maintainer.
	Escalation *Escalation `json:"escalation,omitempty"`
	// StepUp is the workflow a task runs once more under when a run fails in a way a deeper
	// workflow can help (WR4); empty goes to a maintainer. It is escalation's one-target form: set
	// one of them.
	// +kubebuilder:validation:MaxLength=253
	StepUp string `json:"stepUp,omitempty"`
	// Effort maps a non-implementer role to low, medium or high (CP9). An absent role keeps the
	// harness's default, high.
	// +kubebuilder:validation:MaxProperties=4
	Effort map[string]Effort `json:"effort,omitempty"`
	// Models maps a non-implementer role to a model alias (CP10). Internal runs accept only the
	// tier-light, tier-standard and tier-frontier aliases; provider aliases are public only.
	// +kubebuilder:validation:MaxProperties=4
	Models map[string]ModelAlias `json:"models,omitempty"`
}

// EffectiveTier is the workflow's tier: Budget when set, else Tier.
func (p HarnessProfile) EffectiveTier() string {
	if p.Budget != "" {
		return p.Budget
	}
	return p.Tier
}

// EffectiveChecks reports whether the workflow runs repository checks.
func (p HarnessProfile) EffectiveChecks() bool {
	if len(p.Stages) > 0 {
		for _, s := range p.Stages {
			if s.Role == "implementer" {
				return s.Checks
			}
		}
		return false
	}
	return p.Checks
}

// Escalation maps a signal group to a workflow; an absent group goes to the human.
// +kubebuilder:object:generate=true
type Escalation struct {
	// +kubebuilder:validation:MaxLength=253
	OutOfBudget string `json:"outOfBudget,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	NotConverged string `json:"notConverged,omitempty"`
}

// MergeGuardrails bound every repository's class policy (MP3, MP6): a class may add protected
// paths and mandatory checks and choose lower caps, never the reverse. A cap of 0 takes its default
// (MR7, WithDefaults).
// +kubebuilder:object:generate=true
type MergeGuardrails struct {
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	ProtectedFiles []string `json:"protectedFiles,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	MandatoryChecks  []string `json:"mandatoryChecks,omitempty"`
	MaxPatchFiles    int      `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines int      `json:"maxModifiedLines,omitempty"`
	MaxPerDay        int      `json:"maxPerDay,omitempty"`
	// MaxGrantLifetime caps a saved approval grant's life from its creation or last renewal (merge
	// policy stage 5): the factory's lookup expires an older grant. 0 is DefaultGrantLifetime.
	MaxGrantLifetime Duration `json:"maxGrantLifetime,omitzero"`
}

// The guardrails' defaults (MR7): what caps a bound class when the Factory sets no guardrail.
const (
	DefaultGuardrailMaxPatchFiles    = 10
	DefaultGuardrailMaxModifiedLines = 300
	DefaultGuardrailMaxPerDay        = 5
)

// DefaultProtectedFiles are the paths no auto-merge touches whatever the Factory says: the
// workflows, policy-bot's file and the code owners.
func DefaultProtectedFiles() []string {
	return []string{`^\.github/`, `^\.policy\.yml$`, `^CODEOWNERS$`}
}

// WithDefaults is g with every zero cap at its default and DefaultProtectedFiles before its own
// protected paths, each once. It never aliases g's lists.
func (g MergeGuardrails) WithDefaults() MergeGuardrails {
	g.ProtectedFiles = union(DefaultProtectedFiles(), g.ProtectedFiles)
	g.MandatoryChecks = slices.Clone(g.MandatoryChecks)
	g.MaxPatchFiles = cmp.Or(g.MaxPatchFiles, DefaultGuardrailMaxPatchFiles)
	g.MaxModifiedLines = cmp.Or(g.MaxModifiedLines, DefaultGuardrailMaxModifiedLines)
	g.MaxPerDay = cmp.Or(g.MaxPerDay, DefaultGuardrailMaxPerDay)
	return g
}

// The bounds of mergeGuardrails.maxGrantLifetime: 90 days by default (the spec's), a year at most.
const (
	DefaultGrantLifetime = 90 * 24 * time.Hour
	MaxGrantLifetime     = 365 * 24 * time.Hour
)

// GrantLifetime is the cap in force: MaxGrantLifetime, or DefaultGrantLifetime when unset.
func (g MergeGuardrails) GrantLifetime() time.Duration {
	if g.MaxGrantLifetime.Duration == 0 {
		return DefaultGrantLifetime
	}
	return g.MaxGrantLifetime.Duration
}

// DefaultMinPromotionEvidence is MP7's twenty: zero bad in twenty bounds the bad rate at 13.9%.
const DefaultMinPromotionEvidence = 20

// PolicyManagement holds the rollout's stage (MR9) and its operators, read through EffectiveStage,
// Reads, Simulates and Manages.
type PolicyManagement struct {
	// Stage is read, simulate or manage; unset is manage.
	// +optional
	Stage string `json:"stage,omitempty"`
	// Operators are the GitHub logins who approve a promotion below a policy's required evidence
	// (MP7's exception). Read by the broker; none approves nothing.
	Operators []string `json:"operators,omitempty"`
}

// IsOperator is whether login is one of the operators; GitHub logins are case-insensitive.
func (pm PolicyManagement) IsOperator(login string) bool {
	return login != "" && slices.ContainsFunc(pm.Operators, func(o string) bool { return strings.EqualFold(o, login) })
}

// Validate reports policyManagement's rules; the factory and the broker, which reads the same key,
// both apply it.
func (pm PolicyManagement) Validate(bad func(string, ...any)) {
	if pm.Stage != "" && !slices.Contains(stages(), pm.Stage) {
		bad("policyManagement.stage %q is one of read, simulate, manage", pm.Stage)
	}
	for _, o := range pm.Operators {
		if !githubLoginRE.MatchString(o) {
			bad("policyManagement.operators: %q is not a GitHub login", o)
		}
	}
}

// The class authorities of a repository (MP15): central classes, or the repository's own file.
const (
	ClassAuthorityCentral    = "central"
	ClassAuthorityRepository = "repository"
)

var profileRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)

func (c *Config) validateMergePolicy(bad func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(c.HarnessProfiles)) {
		p := c.HarnessProfiles[name]
		if !profileRE.MatchString(name) {
			bad("workflows: %q is not a workflow name", name)
		}
		if p.Template != "" && p.Template != TemplateNone && !slices.Contains(templates(), p.Template) {
			bad("workflows.%s: template %q is not one of %v or %s", name, p.Template, templates(), TemplateNone)
		}
		if p.Tier != "" && !slices.Contains(tiers(), p.Tier) {
			bad("workflows.%s: tier %q is not one of %v", name, p.Tier, tiers())
		}
		if p.Budget != "" && !slices.Contains(tiers(), p.Budget) {
			bad("workflows.%s: budget %q is not one of %v", name, p.Budget, tiers())
		}
		if p.Budget != "" && p.Tier != "" && len(p.Stages) == 0 {
			bad("workflows.%s: set budget or tier, not both", name)
		}
		if _, ok := c.Loops[string(p.Loop)]; p.Loop != "" && p.Loop != LoopOff && !ok {
			bad("workflows.%s: loop %q is off or a row of loops", name, p.Loop)
		}
		if len(p.Description) > MaxDescription {
			bad("workflows.%s: description is %d bytes at most", name, MaxDescription)
		}
		if p.Template == TemplateNone && (p.Checks || p.Tier != "" || p.Budget != "" || len(p.Stages) > 0 || p.Loop != "" || p.Escalation != nil || p.StepUp != "" || len(p.Effort) > 0 || len(p.Models) > 0) {
			bad("workflows.%s: template none runs nothing, so it takes no other key", name)
		}
		if len(p.Stages) > 0 {
			c.validateWorkflowStages(bad, name, p)
		}
		if p.Template != "" && p.Template != TemplateNone {
			if rowName := c.RowFor(name, p); rowName != string(LoopOff) {
				if own, ok := c.Loops[rowName]; ok && own.Review != nil {
					if tmpl, exists := c.Templates[p.Template]; exists && !c.Investigates(p.Template) {
						hasReviewer := slices.Contains(tmpl.Roles, "reviewer")
						if *own.Review && !hasReviewer {
							bad("workflows.%s: template %s has no reviewer, contradicting loops.%s.review: true", name, p.Template, rowName)
						}
						if !*own.Review && hasReviewer {
							bad("workflows.%s: template %s has a reviewer, contradicting loops.%s.review: false", name, p.Template, rowName)
						}
					}
				}
			}
		}
		c.validateWorkflowTuning(bad, name, p)
		if p.StepUp != "" && p.Escalation != nil {
			bad("workflows.%s: set stepUp or escalation, not both", name)
		}
		if p.StepUp != "" {
			switch {
			case p.StepUp == name:
				bad("workflows.%s.stepUp: a workflow never steps up to itself", name)
			case !c.IsWorkflow(p.StepUp):
				bad("workflows.%s.stepUp %q is not a workflow", name, p.StepUp)
			default:
				target := c.HarnessProfiles[p.StepUp]
				if target.StepUp == name {
					bad("workflows.%s.stepUp: %s and %s step up to each other", name, name, p.StepUp)
				} else if c.stepUpHasCycle(name) {
					bad("workflows.%s.stepUp: cycle in stepUp chain", name)
				}
				if c.workflowCeilingLower(p, target) {
					bad("workflows.%s.stepUp: %s has a lower ceiling than %s", name, p.StepUp, name)
				}
			}
		}
		if e := p.Escalation; e != nil {
			for _, g := range []struct{ key, target string }{{"outOfBudget", e.OutOfBudget}, {"notConverged", e.NotConverged}} {
				if !c.IsWorkflow(g.target) && g.target != "" {
					bad("workflows.%s: escalation %s %q is not a workflow (TE7)", name, g.key, g.target)
				}
			}
		}
	}
	c.PolicyManagement.Validate(bad)
	if l := c.MergeGuardrails.MaxGrantLifetime.Duration; l < 0 || l > MaxGrantLifetime {
		bad("mergeGuardrails.maxGrantLifetime must be 0 (the default, %s) to %s", DefaultGrantLifetime, MaxGrantLifetime)
	}
	g := c.MergeGuardrails
	for _, f := range []struct {
		key string
		n   int
	}{{"maxPatchFiles", g.MaxPatchFiles}, {"maxModifiedLines", g.MaxModifiedLines}, {"maxPerDay", g.MaxPerDay}} {
		if f.n < 0 {
			bad("mergeGuardrails.%s must be 0 (no guardrail) or more", f.key)
		}
	}
	for _, p := range g.ProtectedFiles {
		if _, err := regexp.Compile(p); err != nil || p == "" {
			bad("mergeGuardrails.protectedFiles %q is not a regular expression", p)
		}
	}
	for _, n := range g.MandatoryChecks {
		if n == "" {
			bad("mergeGuardrails.mandatoryChecks names an empty check")
		}
	}
}

// validateAuthority is a repository's classAuthority (MP15): one authoritative source, so a
// repository cut over to its own file declares no central class, and the cutover needs the switch.
func (c *Config) validateAuthority(r *Repository, bad func(string, ...any)) {
	switch r.ClassAuthority {
	case "", ClassAuthorityCentral:
		return
	case ClassAuthorityRepository:
	default:
		bad("classAuthority %q is %s or %s", r.ClassAuthority, ClassAuthorityCentral, ClassAuthorityRepository)
		return
	}
	if !c.PolicyManagement.Manages() {
		bad("classAuthority repository needs policyManagement.stage manage (or the repositoryAuthority switch)")
	}
	if len(r.Classes) > 0 {
		bad("classAuthority repository: classes are declared centrally too, two authoritative sources (MP15): remove them")
	}
}

// RepositoryAuthoritative is whether the repository's classes are its own file's (stage 4): cut
// over, with the switch on.
func (c *Config) RepositoryAuthoritative(r *Repository) bool {
	return r != nil && r.ClassAuthority == ClassAuthorityRepository && c.PolicyManagement.Manages()
}

// SetClassSource makes source the classes of every repository cut over to its own file (stage 4):
// Repos and RepositoryFor return them, so every reader reads one set. The binary sets it on every
// config before it is published (config.Live.Prepare runs it on each publish, and on the first
// config at registration); with the switch off it is never called.
func (c *Config) SetClassSource(source func(repository string) (map[string]Class, bool)) {
	c.classSource = source
}
