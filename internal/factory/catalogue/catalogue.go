// SPDX-License-Identifier: Apache-2.0

// Package catalogue is what a repository offers at intake, built from the accepted config snapshot:
// its classes, their profiles, the roles each runs and the merge outcome. It never reads GitHub.
package catalogue

import (
	"maps"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
)

// The modes a class is in, as the gate reads it. A class the gate does not arm at all (the gate is
// off for the whole factory) is Unarmed; Off is a class a maintainer merges while the gate is on.
const (
	ModeLive    = "live"
	ModeShadow  = "shadow"
	ModeOff     = "off"
	ModeUnarmed = "unarmed"
)

// The merge outcomes Outcome returns.
const (
	OutcomeAuto       = "auto"
	OutcomeShadow     = "shadow"
	OutcomeApproval   = "approval"
	OutcomeMaintainer = "maintainer"
)

// NoTemplate is Profile.NotOffered for a profile that sets no template (P15).
const NoTemplate = "no_template"

// reviewClass is the class every repository offers: the work no other class claims.
const reviewClass = "review"

// Catalogue is one repository's offer, as one caller sees it.
type Catalogue struct {
	Repository     string             `json:"repository"`
	Generations    Generations        `json:"generations"`
	Evaluation     Evaluation         `json:"evaluation"`
	ApproveToMerge bool               `json:"approveToMerge"`
	Maintainer     bool               `json:"maintainer"`
	Classes        []Class            `json:"classes"`
	Profiles       map[string]Profile `json:"profiles"`
}

// Generations are the object generations the catalogue was built from. The merge policy's
// generation is its own type, MergePolicyGeneration, so a change of its shape (one generation per
// class, were merge policies to become reusable classes) touches that type and Build, and no reader.
type Generations struct {
	Factory     int64                 `json:"factory"`
	Repository  int64                 `json:"repository"`
	MergePolicy MergePolicyGeneration `json:"mergePolicy"`
	// MergePolicies are a bound repository's per-class policy generations (B19); MergePolicy is
	// then 0. Absent for a repository served from the per-repository shape.
	MergePolicies map[string]int64 `json:"mergePolicies,omitempty"`
}

// MergePolicyGeneration is the generation of the repository's MergePolicy object; zero means the
// repository file served it.
type MergePolicyGeneration int64

// Evaluation is the factory's team evaluation: whether the scoper's recommendation is used, and the
// control cohort's share.
type Evaluation struct {
	Mode           string `json:"mode"`
	ControlPercent int    `json:"controlPercent"`
}

// Class is one class a repository offers at intake.
type Class struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Mode        string  `json:"mode"` // live | shadow | off | unarmed
	CeilingUSD  float64 `json:"ceilingUSD"`
	// Floor is the profile the class runs under, "" when it declares none; Allowed the variants a
	// pin or the scoper may choose.
	Floor      string      `json:"floor"`
	Allowed    []string    `json:"allowed"`
	Escalation *Escalation `json:"escalation"`
}

// Escalation is where a failed run's signal group steps to; "" goes to a maintainer.
type Escalation struct {
	OutOfBudget  string `json:"outOfBudget"`
	NotConverged string `json:"notConverged"`
}

// generations are the repository's, with each bound class's own policy generation (B19).
func generations(cfg *config.Config, repository string, factory, repo, policy int64) Generations {
	g := Generations{Factory: factory, Repository: repo, MergePolicy: MergePolicyGeneration(policy)}
	for class, cg := range cfg.Generations.Repos[strings.ToLower(repository)].Classes {
		if g.MergePolicies == nil {
			g.MergePolicies = map[string]int64{}
		}
		g.MergePolicies[class] = cg.Policy
	}
	return g
}

// Profile is one harness profile, with the roles its template runs.
type Profile struct {
	Template    string   `json:"template"`
	Roles       []string `json:"roles"`
	Tier        string   `json:"tier"`
	Checks      bool     `json:"checks"`
	TestFirst   bool     `json:"testFirst"`
	Loop        string   `json:"loop"`
	Description string   `json:"description"`
	// Offered is whether intake may suggest or pin the profile: only one that sets a template, so
	// the roles shown are exact (P15). NotOffered says why not.
	Offered    bool   `json:"offered"`
	NotOffered string `json:"notOffered,omitempty"`
}

// Build is repository's catalogue for a caller who is, or is not, one of its maintainers; false
// when the config does not serve repository. Repository carries the config's spelling.
func Build(cfg *config.Config, repository string, maintainer bool) (Catalogue, bool) {
	rc := cfg.RepositoryFor(repository)
	if rc == nil {
		return Catalogue{}, false
	}
	gateOn := cfg.NeedsMerger()
	factory, repo, policy := cfg.GenerationsFor(rc.Name)
	mode := cfg.Evaluation.Mode
	if mode == "" {
		mode = config.EvaluationObserve
	}
	c := Catalogue{
		Repository:     rc.Name,
		Generations:    generations(cfg, rc.Name, factory, repo, policy),
		Evaluation:     Evaluation{Mode: mode, ControlPercent: cfg.Evaluation.ControlPercent},
		ApproveToMerge: gateOn && rc.Merge.ApproveToMerge,
		Maintainer:     maintainer,
		Classes:        []Class{},
		Profiles:       map[string]Profile{},
	}
	classes := maps.Clone(rc.Classes)
	if _, ok := classes[reviewClass]; !ok {
		if classes == nil {
			classes = map[string]config.Class{}
		}
		classes[reviewClass] = config.Class{Description: "Anything else: a maintainer merges"}
	}
	for _, name := range slices.Sorted(maps.Keys(classes)) {
		cl := classes[name]
		if !offered(name, cl) {
			continue
		}
		c.Classes = append(c.Classes, classOf(cfg, name, cl, gateOn))
		for _, p := range append([]string{cl.Harness}, cl.HarnessAllowed...) {
			if hp, ok := cfg.HarnessProfiles[p]; ok {
				c.Profiles[p] = profileOf(cfg, hp)
			}
		}
	}
	return c, true
}

// offered is whether a class may be chosen at intake: not the factory's own revert, not another
// author's adopted pull requests, not a detector's findings.
func offered(name string, cl config.Class) bool {
	return name != "revert" && !cl.Adopted() && cl.Oracle == nil
}

func classOf(cfg *config.Config, name string, cl config.Class, gateOn bool) Class {
	out := Class{Name: name, Description: cl.Description, Mode: ModeOf(cl, gateOn), Floor: cl.Harness,
		Allowed: slices.Clone(cl.HarnessAllowed), CeilingUSD: ceilingOf(cfg, cl)}
	if out.Allowed == nil {
		out.Allowed = []string{}
	}
	if e := cl.Escalation; e != nil {
		out.Escalation = &Escalation{OutOfBudget: e.OutOfBudget, NotConverged: e.NotConverged}
	}
	return out
}

// ceilingOf is what a task of the class may spend: the task cap of the tier its floor profile runs
// at, else of the factory's default tier.
func ceilingOf(cfg *config.Config, cl config.Class) float64 {
	tier := cfg.Defaults.Tier
	if p, ok := cfg.HarnessProfiles[cl.Harness]; ok && p.Tier != "" {
		tier = p.Tier
	}
	return cfg.Tiers[tier].TaskUSD
}

func profileOf(cfg *config.Config, p config.HarnessProfile) Profile {
	out := Profile{Template: p.Template, Tier: p.Tier, Checks: p.Checks, TestFirst: p.TestFirst,
		Loop: string(p.Loop), Description: p.Description}
	if t, ok := cfg.Templates[p.Template]; ok && p.Template != "" {
		out.Roles = slices.Clone(t.Roles)
		out.Offered = true
	} else {
		out.NotOffered = NoTemplate
	}
	return out
}

// ModeOf is the gate's outcome for a class: live arms, shadow decides and never arms, neither is a
// maintainer's merge (R32). With the gate off for the whole factory nothing is armed: unarmed.
func ModeOf(c config.Class, gateOn bool) string {
	switch {
	case !gateOn:
		return ModeUnarmed
	case c.Live:
		return ModeLive
	case c.Shadow:
		return ModeShadow
	}
	return ModeOff
}

// Outcome is how a task of a class in mode merges: auto (live), shadow (a maintainer merges and the
// factory records whether it would have), approval (a maintainer's approval merges it) or
// maintainer. It is the broker's to compute, never the model's (P3).
func Outcome(mode string, approveToMerge bool) string {
	switch {
	case mode == ModeLive:
		return OutcomeAuto
	case mode == ModeShadow:
		return OutcomeShadow
	case mode == ModeOff && approveToMerge:
		return OutcomeApproval
	}
	return OutcomeMaintainer
}

// Class is the offered class called name.
func (c Catalogue) Class(name string) (Class, bool) {
	i := slices.IndexFunc(c.Classes, func(cl Class) bool { return cl.Name == name })
	if i < 0 {
		return Class{}, false
	}
	return c.Classes[i], true
}

// OfferedProfiles are the profiles intake may suggest or pin for a class: its floor, then its
// allowed variants, each only when offered (P15).
func (c Catalogue) OfferedProfiles(class string) []string {
	cl, ok := c.Class(class)
	if !ok {
		return nil
	}
	var out []string
	for _, name := range append([]string{cl.Floor}, cl.Allowed...) {
		if p, ok := c.Profiles[name]; ok && p.Offered {
			out = append(out, name)
		}
	}
	return out
}
