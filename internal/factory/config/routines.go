// SPDX-License-Identifier: Apache-2.0

package config

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

// PrincipalUpkeep is the ledger key for cron upkeep routine runs (Q15).
const PrincipalUpkeep = "system:upkeep"

// RoutinesConfig controls routine management behavior in the factory config file.
type RoutinesConfig struct {
	// UIWrite allows maintainers to write routine bindings from the Web UI into ConfigMap
	// agent-factory-routine-bindings (default false).
	UIWrite bool `json:"uiWrite,omitempty"`
}

// ServedRoutine is an accepted Routine definition, its object generation, and whether it carries
// the catalog label.
type ServedRoutine struct {
	Spec       RoutineSpec
	Generation int64
	Catalog    bool
}

// Routine delivery modes.
const (
	RoutineModePropose  = "propose"
	RoutineModeDraft    = "draft"
	RoutineModePR       = "pr"
	RoutineModeDisabled = "disabled"
)

// Probe kinds supported by RoutineSpec.Probe.Kind.
const (
	ProbeNone            = "none"
	ProbeActionsFindings = "actions-findings"
	ProbeDeadLinks       = "dead-links"
	ProbeRenovateRed     = "renovate-red"
)

// Routine refusal reasons beyond the shared binding reasons in binding.go.
const (
	ReasonOverRoutine  = "OverRoutine"
	ReasonMissingRef   = "MissingRef"
	ReasonDefinedTwice = "DefinedTwice"
)

const (
	// MaxRoutineOpenPRs caps how many open PRs a single routine binding may hold.
	MaxRoutineOpenPRs = 5
	// MaxRoutineFindings caps how many probe findings one routine task may batch.
	MaxRoutineFindings = MaxFindings
	// DefaultRoutineMaxCostUSD is the default per-task USD ceiling when neither Routine nor binding sets one (A1).
	DefaultRoutineMaxCostUSD = 2.00
	// DefaultProbeArtifact is the default GitHub Actions artifact name for actions-findings probes.
	DefaultProbeArtifact = "findings"
	// DefaultProbeFile is the default JSON file path inside the probe artifact.
	DefaultProbeFile = "findings.json"
	// DefaultUpkeepDailyMaxUSD caps the default upkeepDailyUSD at $10.00 (or 25% of factoryDailyUSD).
	DefaultUpkeepDailyMaxUSD = 10.00
)

// Yield circuit breaker defaults (UP7).
const (
	DefaultBreakerWindow                 = 10
	DefaultBreakerMinSamples             = 5
	DefaultBreakerMinMergeRate           = 0.25
	DefaultBreakerMaxConsecutiveClosed   = 3
	DefaultBreakerMaxConsecutiveFailures = 3
)

var (
	routineNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)
	skillNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`)
)

// ValidRoutineName reports whether name is a valid Routine name.
func ValidRoutineName(name string) bool { return routineNameRE.MatchString(name) }

// ValidSkillName reports whether name is a valid Agent Skill name.
func ValidSkillName(name string) bool { return skillNameRE.MatchString(name) }

// ProbeSpec configures a routine's pre-run CI or repository probe.
// +kubebuilder:object:generate=true
type ProbeSpec struct {
	// Kind is none, actions-findings, dead-links, or renovate-red.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=none;actions-findings;dead-links;renovate-red
	// +kubebuilder:validation:MaxLength=32
	Kind string `json:"kind"`
	// RequireConsecutive is how many newest qualifying workflow runs must report a finding before it triggers.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	// +optional
	RequireConsecutive int `json:"requireConsecutive,omitempty"`
	// MaxFindings is the maximum findings batched into one task (1..16; default 1).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	MaxFindings int `json:"maxFindings,omitempty"`
}

// BreakerSpec configures a routine's rolling yield circuit breaker.
// +kubebuilder:object:generate=true
type BreakerSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	Window int `json:"window,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	MinSamples int `json:"minSamples,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	MinMergeRate *float64 `json:"minMergeRate,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	// +optional
	MaxConsecutiveClosed int `json:"maxConsecutiveClosed,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	// +optional
	MaxConsecutiveFailures int `json:"maxConsecutiveFailures,omitempty"`
}

// RoutineSpec is a reusable Routine's definition, bound by Repositories.
// +kubebuilder:object:generate=true
type RoutineSpec struct {
	// +kubebuilder:validation:MaxLength=280
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Workflow string `json:"workflow,omitempty"`
	// +kubebuilder:validation:Enum=public;internal
	// +kubebuilder:validation:MaxLength=16
	// +optional
	DataClass string `json:"dataClass,omitempty"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=14336
	DefaultPrompt string `json:"defaultPrompt"`
	// +optional
	Probe ProbeSpec `json:"probe,omitzero"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:Enum=probeWorkflow;probeArtifact;probeFile;probeAuthor;oracleClass
	// +kubebuilder:validation:items:MaxLength=32
	// +optional
	Requires []string `json:"requires,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	// +optional
	MaxOpenPRs int `json:"maxOpenPRs,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxCostUSD float64 `json:"maxCostUSD,omitempty"`
	// +optional
	Cooldown Duration `json:"cooldown,omitzero"`
	// +optional
	Breaker BreakerSpec `json:"breaker,omitzero"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	// +optional
	ProtectedPaths []string `json:"protectedPaths,omitempty"`
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=pypi;npm;golang;crates
	// +kubebuilder:validation:items:MaxLength=16
	// +optional
	Egress []string `json:"egress,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	ProbeWorkflow string `json:"probeWorkflow,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ProbeArtifact string `json:"probeArtifact,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ProbeFile string `json:"probeFile,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ProbeAuthor string `json:"probeAuthor,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	OracleClass string `json:"oracleClass,omitempty"`
}

// RoutineBinding is one Repository's binding of a Routine (UP2). A binding fills parameters and
// tightens bounds; it never widens what the Routine says.
// +kubebuilder:object:generate=true
type RoutineBinding struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=propose;draft;pr;disabled
	// +kubebuilder:validation:MaxLength=16
	Mode string `json:"mode"`
	// +optional
	Paused bool `json:"paused,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Cron string `json:"cron,omitempty"`
	// +optional
	RepoPrompt *bool `json:"repoPrompt,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`
	// +optional
	Skill string `json:"skill,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Workflow string `json:"workflow,omitempty"`
	// +kubebuilder:validation:Enum=public;internal
	// +kubebuilder:validation:MaxLength=16
	// +optional
	DataClass string `json:"dataClass,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	ProbeWorkflow string `json:"probeWorkflow,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ProbeArtifact string `json:"probeArtifact,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ProbeFile string `json:"probeFile,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ProbeAuthor string `json:"probeAuthor,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	OracleClass string `json:"oracleClass,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	// +optional
	ProtectedPaths []string `json:"protectedPaths,omitempty"`
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=pypi;npm;golang;crates
	// +kubebuilder:validation:items:MaxLength=16
	// +optional
	Egress []string `json:"egress,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	// +optional
	MaxOpenPRs int `json:"maxOpenPRs,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxCostUSD float64 `json:"maxCostUSD,omitempty"`
	// +optional
	Cooldown *Duration `json:"cooldown,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	MaxFindings int `json:"maxFindings,omitempty"`
	// +optional
	Breaker *BreakerSpec `json:"breaker,omitempty"`
}

// ResolvedBreaker is a routine's breaker configuration with all defaults applied.
type ResolvedBreaker struct {
	Window                 int
	MinSamples             int
	MinMergeRate           float64
	MaxConsecutiveClosed   int
	MaxConsecutiveFailures int
}

// ResolvedRoutine is a RoutineSpec and a Repository's RoutineBinding merged and validated.
type ResolvedRoutine struct {
	Name              string
	Description       string
	Mode              string
	Paused            bool
	Cron              string
	Workflow          string
	DataClass         string
	DefaultPrompt     string
	Probe             ProbeSpec
	ProbeWorkflow     string
	ProbeArtifact     string
	ProbeFile         string
	ProbeAuthor       string
	OracleClass       string
	ProtectedPaths    []string
	Egress            []string
	MaxOpenPRs        int
	MaxCostUSD        float64
	Cooldown          time.Duration
	Breaker           ResolvedBreaker
	RepoPrompt        bool
	Skill             string
	RoutineGeneration int64
}

var routineRequiresKeys = []string{"probeWorkflow", "probeArtifact", "probeFile", "probeAuthor", "oracleClass"}

// ResolveRoutine merges a RoutineSpec and a Repository's RoutineBinding, enforcing UP2 ownership,
// fill-once parameters, and probe rules (P1-R1, A1, A8).
func ResolveRoutine(name string, rt RoutineSpec, b RoutineBinding, rc *Repository, c *Config) (ResolvedRoutine, string, error) {
	res, reason, problems := resolveRoutine(name, rt, b, rc, c)
	if reason != "" {
		return ResolvedRoutine{}, reason, errors.New(strings.Join(problems, "; "))
	}
	return res, "", nil
}

// ValidateRoutineSpec validates a standalone Routine object by checking its own shape and running
// resolveRoutine with synthetic placeholders for parameters supplied by bindings.
func ValidateRoutineSpec(name string, rt RoutineSpec, c *Config) []string {
	if !routineNameRE.MatchString(name) {
		return []string{fmt.Sprintf("routine %q is not a valid routine name", name)}
	}
	if problems := routineShapeProblems(name, rt, c); len(problems) > 0 {
		return problems
	}
	b := RoutineBinding{
		Name: name,
		Mode: RoutineModePR,
		Cron: "0 3 * * *",
	}
	for _, req := range rt.Requires {
		switch req {
		case "probeWorkflow":
			b.ProbeWorkflow = "placeholder.yaml"
		case "probeArtifact":
			b.ProbeArtifact = "placeholder"
		case "probeFile":
			b.ProbeFile = "placeholder.json"
		case "probeAuthor":
			b.ProbeAuthor = defaultProbeAuthor
		case "oracleClass":
			b.OracleClass = "placeholder"
		}
	}
	if rt.Probe.Kind == ProbeActionsFindings && rt.ProbeWorkflow == "" && b.ProbeWorkflow == "" {
		b.ProbeWorkflow = "placeholder.yaml"
	}
	if rt.Probe.Kind == ProbeDeadLinks && rt.OracleClass == "" && b.OracleClass == "" {
		b.OracleClass = "placeholder"
	}
	_, _, problems := resolveRoutine(name, rt, b, nil, c)
	return problems
}

func routineShapeProblems(name string, rt RoutineSpec, c *Config) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf("routine %s: ", name)+fmt.Sprintf(f, a...)) }

	maxText := MaxTextCeiling
	if c != nil && c.Caps.MaxTextBytes > 0 {
		maxText = c.Caps.MaxTextBytes
	}
	if rt.DefaultPrompt == "" || !utf8.ValidString(rt.DefaultPrompt) || len(rt.DefaultPrompt) > maxText {
		bad("defaultPrompt must be valid UTF-8 of 1..%d bytes", maxText)
	}
	if utf8.RuneCountInString(rt.Description) > 280 {
		bad("description must be at most 280 characters")
	}
	if rt.Workflow != "" {
		if rt.Workflow == TemplateNone {
			bad("workflow cannot be %q", TemplateNone)
		} else if c != nil && len(c.HarnessProfiles) > 0 && !c.IsWorkflow(rt.Workflow) {
			bad("workflow %q is not a configured workflow", rt.Workflow)
		}
	}
	if rt.DataClass != "" && rt.DataClass != "public" && rt.DataClass != "internal" {
		bad("dataClass %q must be public or internal", rt.DataClass)
	}
	switch cmp.Or(rt.Probe.Kind, ProbeNone) {
	case ProbeNone, ProbeActionsFindings, ProbeDeadLinks, ProbeRenovateRed:
	default:
		bad("probe.kind %q is invalid", rt.Probe.Kind)
	}
	if rt.Probe.RequireConsecutive < 0 || rt.Probe.RequireConsecutive > 5 {
		bad("probe.requireConsecutive must be 1..5")
	}
	if rt.Probe.MaxFindings < 0 || rt.Probe.MaxFindings > MaxRoutineFindings {
		bad("probe.maxFindings must be 1..%d", MaxRoutineFindings)
	}
	seenReq := map[string]bool{}
	for _, req := range rt.Requires {
		if !slices.Contains(routineRequiresKeys, req) {
			bad("requires %q is unknown", req)
		} else if seenReq[req] {
			bad("requires %q is listed twice", req)
		}
		seenReq[req] = true
		if routineParamValue(rt, req) != "" {
			bad("requires %q cannot also be set on the Routine", req)
		}
	}
	if rt.MaxOpenPRs < 0 || rt.MaxOpenPRs > MaxRoutineOpenPRs {
		bad("maxOpenPRs must be 1..%d", MaxRoutineOpenPRs)
	}
	if !finite(rt.MaxCostUSD) || rt.MaxCostUSD < 0 {
		bad("maxCostUSD must be 0 or more")
	} else if rt.MaxCostUSD > 0 && (MicroUSD(rt.MaxCostUSD) < 1 || MicroUSD(rt.MaxCostUSD) > 1_000_000_000) {
		bad("maxCostUSD must convert to 1..1000000000 micro-USD")
	}
	if rt.Cooldown.Duration < 0 {
		bad("cooldown must be 0 or more")
	}
	if rt.ProbeWorkflow != "" && !workflowRE.MatchString(rt.ProbeWorkflow) {
		bad("probeWorkflow %q must be a workflow filename (.yml/.yaml)", rt.ProbeWorkflow)
	}
	if probs := breakerShapeProblems("breaker", rt.Breaker); len(probs) > 0 {
		for _, p := range probs {
			bad("%s", p)
		}
	} else {
		w := cmp.Or(rt.Breaker.Window, DefaultBreakerWindow)
		ms := cmp.Or(rt.Breaker.MinSamples, DefaultBreakerMinSamples)
		if ms > w {
			bad("breaker.minSamples (%d) cannot exceed breaker.window (%d)", ms, w)
		}
	}
	for _, p := range rt.ProtectedPaths {
		if _, err := regexp.Compile(p); err != nil || p == "" {
			bad("protectedPaths %q is not a valid regular expression", p)
		}
	}
	for i, p := range rt.Egress {
		if !slices.Contains(EgressProfiles, p) || slices.Index(rt.Egress, p) != i {
			bad("egress %q is not one of %v, or is listed twice", p, EgressProfiles)
		}
	}
	return out
}

func breakerShapeProblems(prefix string, b BreakerSpec) []string {
	var out []string
	if b.Window < 0 || b.Window > 100 {
		out = append(out, fmt.Sprintf("%s.window must be 1..100", prefix))
	}
	if b.MinSamples < 0 || b.MinSamples > 100 {
		out = append(out, fmt.Sprintf("%s.minSamples must be 1..100", prefix))
	}
	if b.MinMergeRate != nil && (!finite(*b.MinMergeRate) || *b.MinMergeRate < 0 || *b.MinMergeRate > 1) {
		out = append(out, fmt.Sprintf("%s.minMergeRate must be 0..1", prefix))
	}
	if b.MaxConsecutiveClosed < 0 || b.MaxConsecutiveClosed > 50 {
		out = append(out, fmt.Sprintf("%s.maxConsecutiveClosed must be 1..50", prefix))
	}
	if b.MaxConsecutiveFailures < 0 || b.MaxConsecutiveFailures > 50 {
		out = append(out, fmt.Sprintf("%s.maxConsecutiveFailures must be 1..50", prefix))
	}
	return out
}

func routineParamValue(rt RoutineSpec, key string) string {
	switch key {
	case "probeWorkflow":
		return rt.ProbeWorkflow
	case "probeArtifact":
		return rt.ProbeArtifact
	case "probeFile":
		return rt.ProbeFile
	case "probeAuthor":
		return rt.ProbeAuthor
	case "oracleClass":
		return rt.OracleClass
	default:
		return ""
	}
}

func resolveRoutine(name string, rt RoutineSpec, b RoutineBinding, rc *Repository, c *Config) (ResolvedRoutine, string, []string) {
	refuse := func(reason, f string, a ...any) (ResolvedRoutine, string, []string) {
		return ResolvedRoutine{}, reason, []string{fmt.Sprintf(f, a...)}
	}

	if !routineNameRE.MatchString(name) {
		return refuse(ReasonInvalid, "routine %q is not a valid routine name", name)
	}
	if b.Name != name {
		return refuse(ReasonInvalid, "routine %s: binding names Routine %q", name, b.Name)
	}
	switch b.Mode {
	case RoutineModePropose, RoutineModeDraft, RoutineModePR, RoutineModeDisabled:
	default:
		return refuse(ReasonInvalid, "routine %s: mode %q is not one of propose, draft, pr, disabled", name, b.Mode)
	}
	if b.Skill != "" && !skillNameRE.MatchString(b.Skill) {
		return refuse(ReasonInvalid, "routine %s: skill %q is not a valid skill name", name, b.Skill)
	}
	if probs := routineShapeProblems(name, rt, c); len(probs) > 0 {
		return ResolvedRoutine{}, ReasonInvalid, probs
	}

	if b.Mode != RoutineModeDisabled && b.Cron == "" {
		return refuse(ReasonMissingParameter, "routine %s: cron is required when mode is %s", name, b.Mode)
	}
	if b.Cron != "" {
		if _, err := cron.ParseStandard(b.Cron); err != nil {
			return refuse(ReasonInvalid, "routine %s: cron %q does not parse: %v", name, b.Cron, err)
		}
	}

	if b.Workflow != "" {
		if b.Workflow == TemplateNone {
			return refuse(ReasonInvalid, "routine %s: workflow cannot be %q", name, TemplateNone)
		}
		if c != nil && len(c.HarnessProfiles) > 0 && !c.IsWorkflow(b.Workflow) {
			return refuse(ReasonInvalid, "routine %s: workflow %q is not a configured workflow", name, b.Workflow)
		}
	}
	if b.DataClass != "" && b.DataClass != "public" && b.DataClass != "internal" {
		return refuse(ReasonInvalid, "routine %s: dataClass %q must be public or internal", name, b.DataClass)
	}
	if rt.DataClass == "internal" && b.DataClass == "public" {
		return refuse(ReasonOverRoutine, "routine %s: binding cannot widen dataClass from internal to public", name)
	}
	if b.MaxOpenPRs < 0 || b.MaxOpenPRs > MaxRoutineOpenPRs {
		return refuse(ReasonInvalid, "routine %s: maxOpenPRs must be 1..%d", name, MaxRoutineOpenPRs)
	}
	if rt.MaxOpenPRs > 0 && b.MaxOpenPRs > rt.MaxOpenPRs {
		return refuse(ReasonOverRoutine, "routine %s: maxOpenPRs %d is over Routine's %d", name, b.MaxOpenPRs, rt.MaxOpenPRs)
	}
	if !finite(b.MaxCostUSD) || b.MaxCostUSD < 0 {
		return refuse(ReasonInvalid, "routine %s: maxCostUSD must be 0 or more", name)
	}
	if b.MaxCostUSD > 0 && (MicroUSD(b.MaxCostUSD) < 1 || MicroUSD(b.MaxCostUSD) > 1_000_000_000) {
		return refuse(ReasonInvalid, "routine %s: maxCostUSD must convert to 1..1000000000 micro-USD", name)
	}
	if rt.MaxCostUSD > 0 && b.MaxCostUSD > rt.MaxCostUSD {
		return refuse(ReasonOverRoutine, "routine %s: maxCostUSD %.2f is over Routine's %.2f", name, b.MaxCostUSD, rt.MaxCostUSD)
	}
	if b.MaxFindings < 0 || b.MaxFindings > MaxRoutineFindings {
		return refuse(ReasonInvalid, "routine %s: maxFindings must be 1..%d", name, MaxRoutineFindings)
	}
	if rt.Probe.MaxFindings > 0 && b.MaxFindings > rt.Probe.MaxFindings {
		return refuse(ReasonOverRoutine, "routine %s: maxFindings %d is over Routine's %d", name, b.MaxFindings, rt.Probe.MaxFindings)
	}
	if b.Cooldown != nil && b.Cooldown.Duration < 0 {
		return refuse(ReasonInvalid, "routine %s: cooldown must be 0 or more", name)
	}
	if b.ProbeWorkflow != "" && !workflowRE.MatchString(b.ProbeWorkflow) {
		return refuse(ReasonInvalid, "routine %s: probeWorkflow %q must be a workflow filename (.yml/.yaml)", name, b.ProbeWorkflow)
	}
	for _, p := range b.ProtectedPaths {
		if _, err := regexp.Compile(p); err != nil || p == "" {
			return refuse(ReasonInvalid, "routine %s: protectedPaths %q is not a valid regular expression", name, p)
		}
	}
	for i, p := range b.Egress {
		if !slices.Contains(EgressProfiles, p) || slices.Index(b.Egress, p) != i {
			return refuse(ReasonInvalid, "routine %s: egress %q is not one of %v, or is listed twice", name, p, EgressProfiles)
		}
	}

	// Fill-once parameters.
	var probeWorkflow, probeArtifact, probeFile, probeAuthor, oracleClass string
	for _, f := range []struct {
		key, rtVal, bVal string
		dst              *string
	}{
		{"probeWorkflow", rt.ProbeWorkflow, b.ProbeWorkflow, &probeWorkflow},
		{"probeArtifact", rt.ProbeArtifact, b.ProbeArtifact, &probeArtifact},
		{"probeFile", rt.ProbeFile, b.ProbeFile, &probeFile},
		{"probeAuthor", rt.ProbeAuthor, b.ProbeAuthor, &probeAuthor},
		{"oracleClass", rt.OracleClass, b.OracleClass, &oracleClass},
	} {
		if f.rtVal != "" && f.bVal != "" {
			return refuse(ReasonParameterTwice, "%s is set by Routine %s and by the binding", f.key, name)
		}
		*f.dst = cmp.Or(f.bVal, f.rtVal)
		if slices.Contains(rt.Requires, f.key) && *f.dst == "" {
			return refuse(ReasonMissingParameter, "routine %s: required parameter %s is missing", name, f.key)
		}
	}

	// Probe defaults and requirements.
	probeKind := cmp.Or(rt.Probe.Kind, ProbeNone)
	maxFindings := cmp.Or(b.MaxFindings, rt.Probe.MaxFindings, 1)
	requireConsecutive := cmp.Or(rt.Probe.RequireConsecutive, 1)

	switch probeKind {
	case ProbeActionsFindings:
		if probeWorkflow == "" {
			return refuse(ReasonMissingParameter, "routine %s: actions-findings probe requires probeWorkflow", name)
		}
		probeArtifact = cmp.Or(probeArtifact, DefaultProbeArtifact)
		probeFile = cmp.Or(probeFile, DefaultProbeFile)
	case ProbeDeadLinks:
		if oracleClass == "" {
			return refuse(ReasonMissingParameter, "routine %s: dead-links probe requires oracleClass", name)
		}
		if rc != nil && (c == nil || !c.RepositoryAuthoritative(rc)) {
			cl, ok := rc.Class(oracleClass)
			if !ok {
				if !rc.declares(oracleClass) {
					return refuse(ReasonInvalid, "routine %s: oracleClass %q is not a declared oracle policy", name, oracleClass)
				}
			} else if cl.Oracle == nil || cl.Oracle.Probe != ProbeDeadLinks {
				return refuse(ReasonInvalid, "routine %s: oracleClass %q does not declare oracle.probe %s", name, oracleClass, ProbeDeadLinks)
			}
		}
	case ProbeRenovateRed:
		probeAuthor = cmp.Or(probeAuthor, defaultProbeAuthor)
	}

	// Breaker resolution and tightening.
	rtWindow := cmp.Or(rt.Breaker.Window, DefaultBreakerWindow)
	rtMinSamples := cmp.Or(rt.Breaker.MinSamples, DefaultBreakerMinSamples)
	rtMinMergeRate := DefaultBreakerMinMergeRate
	if rt.Breaker.MinMergeRate != nil {
		rtMinMergeRate = *rt.Breaker.MinMergeRate
	}
	rtMaxClosed := cmp.Or(rt.Breaker.MaxConsecutiveClosed, DefaultBreakerMaxConsecutiveClosed)
	rtMaxFailures := cmp.Or(rt.Breaker.MaxConsecutiveFailures, DefaultBreakerMaxConsecutiveFailures)

	breaker := ResolvedBreaker{
		Window:                 rtWindow,
		MinSamples:             rtMinSamples,
		MinMergeRate:           rtMinMergeRate,
		MaxConsecutiveClosed:   rtMaxClosed,
		MaxConsecutiveFailures: rtMaxFailures,
	}
	if b.Breaker != nil {
		if probs := breakerShapeProblems("binding.breaker", *b.Breaker); len(probs) > 0 {
			return refuse(ReasonInvalid, "routine %s: %s", name, strings.Join(probs, "; "))
		}
		if b.Breaker.MinMergeRate != nil && *b.Breaker.MinMergeRate < rtMinMergeRate {
			return refuse(ReasonOverRoutine, "routine %s: breaker.minMergeRate %.2f is below Routine's %.2f", name, *b.Breaker.MinMergeRate, rtMinMergeRate)
		}
		if b.Breaker.MaxConsecutiveClosed > 0 && b.Breaker.MaxConsecutiveClosed > rtMaxClosed {
			return refuse(ReasonOverRoutine, "routine %s: breaker.maxConsecutiveClosed %d is over Routine's %d", name, b.Breaker.MaxConsecutiveClosed, rtMaxClosed)
		}
		if b.Breaker.MaxConsecutiveFailures > 0 && b.Breaker.MaxConsecutiveFailures > rtMaxFailures {
			return refuse(ReasonOverRoutine, "routine %s: breaker.maxConsecutiveFailures %d is over Routine's %d", name, b.Breaker.MaxConsecutiveFailures, rtMaxFailures)
		}
		breaker.Window = cmp.Or(b.Breaker.Window, rtWindow)
		breaker.MinSamples = cmp.Or(b.Breaker.MinSamples, rtMinSamples)
		if b.Breaker.MinMergeRate != nil {
			breaker.MinMergeRate = *b.Breaker.MinMergeRate
		}
		breaker.MaxConsecutiveClosed = cmp.Or(b.Breaker.MaxConsecutiveClosed, rtMaxClosed)
		breaker.MaxConsecutiveFailures = cmp.Or(b.Breaker.MaxConsecutiveFailures, rtMaxFailures)
	}
	if breaker.MinSamples > breaker.Window {
		return refuse(ReasonInvalid, "routine %s: breaker.minSamples (%d) cannot exceed breaker.window (%d)", name, breaker.MinSamples, breaker.Window)
	}

	// Egress tightening and repository subset check.
	egress := slices.Clone(rt.Egress)
	if len(b.Egress) > 0 {
		if len(rt.Egress) > 0 {
			for _, p := range b.Egress {
				if !slices.Contains(rt.Egress, p) {
					return refuse(ReasonOverRoutine, "routine %s: egress %q is not allowed by Routine", name, p)
				}
			}
		}
		egress = slices.Clone(b.Egress)
	}
	if rc != nil {
		for _, p := range egress {
			if !slices.Contains(rc.Checks.Egress, p) {
				return refuse(ReasonInvalid, "routine %s: egress %q is not in repository checks.egress", name, p)
			}
		}
	}

	cooldown := rt.Cooldown.Duration
	if b.Cooldown != nil {
		cooldown = b.Cooldown.Duration
	}

	dataClass := cmp.Or(b.DataClass, rt.DataClass)
	if dataClass == "" && c != nil {
		dataClass = c.Defaults.DataClass
	}

	return ResolvedRoutine{
		Name:           name,
		Description:    rt.Description,
		Mode:           b.Mode,
		Paused:         b.Paused,
		Cron:           b.Cron,
		Workflow:       cmp.Or(b.Workflow, rt.Workflow),
		DataClass:      dataClass,
		DefaultPrompt:  rt.DefaultPrompt,
		Probe:          ProbeSpec{Kind: probeKind, RequireConsecutive: requireConsecutive, MaxFindings: maxFindings},
		ProbeWorkflow:  probeWorkflow,
		ProbeArtifact:  probeArtifact,
		ProbeFile:      probeFile,
		ProbeAuthor:    probeAuthor,
		OracleClass:    oracleClass,
		ProtectedPaths: union(rt.ProtectedPaths, b.ProtectedPaths),
		Egress:         egress,
		MaxOpenPRs:     cmp.Or(b.MaxOpenPRs, rt.MaxOpenPRs, 1),
		MaxCostUSD:     cmp.Or(b.MaxCostUSD, rt.MaxCostUSD, DefaultRoutineMaxCostUSD),
		Cooldown:       cooldown,
		Breaker:        breaker,
		RepoPrompt:     b.RepoPrompt == nil || *b.RepoPrompt,
		Skill:          b.Skill,
	}, "", nil
}

// ValidateDisabledRoutineBinding validates a mode: disabled routine binding's workflow and egress
// against repository checks.egress and the factory's configured workflows.
func ValidateDisabledRoutineBinding(b RoutineBinding, checks Checks, cfgCtx *Config) string {
	if b.Workflow != "" {
		if b.Workflow == TemplateNone {
			return fmt.Sprintf("routine %s: workflow cannot be %q", b.Name, TemplateNone)
		}
		if cfgCtx != nil && len(cfgCtx.HarnessProfiles) > 0 && !cfgCtx.IsWorkflow(b.Workflow) {
			return fmt.Sprintf("routine %s: workflow %q is not a configured workflow", b.Name, b.Workflow)
		}
	}
	for i, p := range b.Egress {
		if !slices.Contains(EgressProfiles, p) || slices.Index(b.Egress, p) != i {
			return fmt.Sprintf("routine %s: egress %q is not one of %v, or is listed twice", b.Name, p, EgressProfiles)
		}
		if !slices.Contains(checks.Egress, p) {
			return fmt.Sprintf("routine %s: egress %q is not in repository checks.egress", b.Name, p)
		}
	}
	return ""
}
