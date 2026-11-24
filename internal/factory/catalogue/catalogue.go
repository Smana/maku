// SPDX-License-Identifier: Apache-2.0

// Package catalogue is what a repository offers at intake, built from the accepted config snapshot:
// its classes and how each merges, and the workflows any task may run, with the roles each runs and
// the repository's default. It never reads GitHub.
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

// Catalogue is one repository's offer, as one caller sees it. Since the workflow left the class
// (WF4), every class offers the same workflows: Profiles, DefaultWorkflow first.
type Catalogue struct {
	Repository     string      `json:"repository"`
	Generations    Generations `json:"generations"`
	Evaluation     Evaluation  `json:"evaluation"`
	ApproveToMerge bool        `json:"approveToMerge"`
	Maintainer     bool        `json:"maintainer"`
	Classes        []Class     `json:"classes"`
	// DefaultWorkflow is the workflow a task runs when no label names one: the repository's
	// workflow.default (WF3).
	DefaultWorkflow string `json:"defaultWorkflow"`
	// Profiles are the factory's workflows by name, none excepted (WF1).
	Profiles map[string]Profile `json:"profiles"`
}

// Generations are the object generations the catalogue was built from: the Factory's, the
// Repository's, and each bound class's own MergePolicy's (B19). Zero means the file served it.
type Generations struct {
	Factory       int64            `json:"factory"`
	Repository    int64            `json:"repository"`
	MergePolicies map[string]int64 `json:"mergePolicies,omitempty"`
}

// Evaluation is the factory's team evaluation: whether the scoper's recommendation is used, the
// control cohort's share, and the workflow the treatment cohort runs ("" for the floor, WF8).
type Evaluation struct {
	Mode           string `json:"mode"`
	ControlPercent int    `json:"controlPercent"`
	Treatment      string `json:"treatment,omitempty"`
}

// Class is one class a repository offers at intake: how its tasks merge, and what one may spend at
// the default workflow's tier. It names no workflow (WF7).
type Class struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Mode        string  `json:"mode"` // live | shadow | off | unarmed
	CeilingUSD  float64 `json:"ceilingUSD"`
}

// Escalation is where a workflow's failed run steps to, by signal group; "" goes to a maintainer.
type Escalation struct {
	OutOfBudget  string `json:"outOfBudget"`
	NotConverged string `json:"notConverged"`
}

// generations are the repository's, with each bound class's own policy generation (B19).
func generations(cfg *config.Config, repository string, factory, repo int64) Generations {
	g := Generations{Factory: factory, Repository: repo}
	for class, cg := range cfg.Generations.Repos[strings.ToLower(repository)].Classes {
		if g.MergePolicies == nil {
			g.MergePolicies = map[string]int64{}
		}
		g.MergePolicies[class] = cg.Policy
	}
	return g
}

// Repositories returns the served repositories — public ones and private ones whose acknowledgement
// is set — sorted (WF12).
func Repositories(cfg *config.Config) []string {
	if cfg == nil {
		return []string{}
	}
	out := make([]string, 0, len(cfg.Repos()))
	for _, r := range cfg.Repos() {
		if cfg.PrivateRefused(r.Name) {
			continue
		}
		out = append(out, r.Name)
	}
	slices.Sort(out)
	return out
}

// Profile is one workflow, with the roles its template runs.
type Profile struct {
	Template    string   `json:"template"`
	Roles       []string `json:"roles"`
	Tier        string   `json:"tier"`
	Checks      bool     `json:"checks"`
	TestFirst   bool     `json:"testFirst"`
	Loop        string   `json:"loop"`
	Description string   `json:"description"`
	// Escalation is where a failed run steps to; nil goes to a maintainer.
	Escalation *Escalation `json:"escalation,omitempty"`
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
	factory, repo := cfg.GenerationsFor(rc.Name)
	mode := cfg.Evaluation.Mode
	if mode == "" {
		mode = config.EvaluationObserve
	}
	c := Catalogue{
		Repository:      rc.Name,
		Generations:     generations(cfg, rc.Name, factory, repo),
		Evaluation:      Evaluation{Mode: mode, ControlPercent: cfg.Evaluation.ControlPercent, Treatment: cfg.Evaluation.Treatment},
		ApproveToMerge:  gateOn && rc.Merge.ApproveToMerge,
		Maintainer:      maintainer,
		Classes:         []Class{},
		DefaultWorkflow: rc.DefaultWorkflow(),
		Profiles:        map[string]Profile{},
	}
	for name, p := range cfg.HarnessProfiles {
		if cfg.IsWorkflow(name) {
			c.Profiles[name] = profileOf(cfg, rc, name, p)
		}
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
		c.Classes = append(c.Classes, Class{Name: name, Description: cl.Description, Mode: ModeOf(cl, gateOn), CeilingUSD: ceilingOf(cfg, rc)})
	}
	return c, true
}

// offered is whether a class may be chosen at intake: not the factory's own revert, not another
// author's adopted pull requests, not a detector's findings.
func offered(name string, cl config.Class) bool {
	return name != "revert" && !cl.Adopted() && cl.Oracle == nil
}

// ceilingOf is what a task may spend: the task cap of the tier the repository's default workflow
// runs at, else of the standard tier.
func ceilingOf(cfg *config.Config, rc *config.Repository) float64 {
	tier := "standard"
	if p, ok := cfg.Workflow(rc.DefaultWorkflow()); ok && p.Tier != "" {
		tier = p.Tier
	}
	return cfg.Tiers[tier].TaskUSD
}

func profileOf(cfg *config.Config, rc *config.Repository, name string, p config.HarnessProfile) Profile {
	out := Profile{Template: p.Template, Tier: p.Tier, Checks: p.Checks, TestFirst: cfg.TestFirstIn(rc, name),
		Loop: string(p.Loop), Description: p.Description}
	if e := p.Escalation; e != nil {
		out.Escalation = &Escalation{OutOfBudget: e.OutOfBudget, NotConverged: e.NotConverged}
	}
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

// OfferedProfiles are the workflows intake may suggest or pin for a task of class: every offered
// workflow, the default first then by name (Workflows), since the workflow left the class (WF4);
// none for a class the catalogue does not offer.
func (c Catalogue) OfferedProfiles(class string) []string {
	if _, ok := c.Class(class); !ok {
		return nil
	}
	return c.Workflows()
}

// Workflows are the workflows a maintainer may pin, each only when offered (P15): the default
// first, then the others by name.
func (c Catalogue) Workflows() []string {
	var out []string
	if p, ok := c.Profiles[c.DefaultWorkflow]; ok && p.Offered {
		out = append(out, c.DefaultWorkflow)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Profiles)) {
		if name != c.DefaultWorkflow && c.Profiles[name].Offered {
			out = append(out, name)
		}
	}
	return out
}
