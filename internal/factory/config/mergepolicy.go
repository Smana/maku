// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"
)

// TemplateNone is the harness of an adopted class: another author's pull requests, no run.
const TemplateNone = "none"

// LoopOff is a profile with no loop.
const LoopOff LoopRef = "off"

// LoopRef names a row of loops, or off.
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

// HarnessProfile is a named, centrally approved way of running a class (merge policy MP2). Stage 1
// reads Checks and TestFirst; Template, Tier and Loop are validated here and read from repository
// cutover on. Empty keeps what the factory selects today.
type HarnessProfile struct {
	Template  string  `json:"template,omitempty"`
	Tier      string  `json:"tier,omitempty"`
	Checks    bool    `json:"checks,omitempty"`
	TestFirst bool    `json:"testFirst,omitempty"`
	Loop      LoopRef `json:"loop,omitempty"`
}

// MergeGuardrails bound every repository's class policy (MP3, MP6): a class may add protected
// paths and mandatory checks and choose lower caps, never the reverse. A cap of 0 is no guardrail.
type MergeGuardrails struct {
	ProtectedFiles   []string `json:"protectedFiles,omitempty"`
	MandatoryChecks  []string `json:"mandatoryChecks,omitempty"`
	MaxPatchFiles    int      `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines int      `json:"maxModifiedLines,omitempty"`
	MaxPerDay        int      `json:"maxPerDay,omitempty"`
	// MaxGrantLifetime caps a saved approval grant's life from its creation or last renewal (merge
	// policy stage 5): the factory's lookup expires an older grant. 0 is DefaultGrantLifetime.
	MaxGrantLifetime Duration `json:"maxGrantLifetime,omitzero"`
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

// PolicyManagement holds the rollout's switches, all off by default (merge policy stages).
type PolicyManagement struct {
	// Read observes each owned repository's class policy and compares it with this config. It
	// decides nothing.
	Read bool `json:"read,omitempty"`
	// Simulation lets `maku policy simulate` and `maku policy diff` run against this config: the
	// switch a simulator that disagrees with the gate turns off (stage 2). Offline, decides nothing.
	Simulation bool `json:"simulation,omitempty"`
	// DualEvaluation decides every merge decision a second time under the repository's policy, as
	// Read last observed it, and records whether the two agree (stage 3). It decides nothing: the
	// central decision is the one acted on. Needs Read.
	DualEvaluation bool `json:"dualEvaluation,omitempty"`
	// ApprovalMemory consults saved approval grants where a live class's requiresApproval subset
	// would ask a human (stage 5): an active grant whose predicate covers the decision lets it arm.
	// Off, no grant is read. Needs Read: a grant is bound to the policy Read observes.
	ApprovalMemory bool `json:"approvalMemory,omitempty"`
}

var profileRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)

func (c *Config) validateMergePolicy(bad func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(c.HarnessProfiles)) {
		p := c.HarnessProfiles[name]
		if !profileRE.MatchString(name) {
			bad("harnessProfiles: %q is not a profile name", name)
		}
		if p.Template != "" && p.Template != TemplateNone && !slices.Contains(templates(), p.Template) {
			bad("harnessProfiles.%s: template %q is not one of %v or %s", name, p.Template, templates(), TemplateNone)
		}
		if p.Tier != "" && !slices.Contains(tiers(), p.Tier) {
			bad("harnessProfiles.%s: tier %q is not one of %v", name, p.Tier, tiers())
		}
		if _, ok := c.Loops[string(p.Loop)]; p.Loop != "" && p.Loop != LoopOff && !ok {
			bad("harnessProfiles.%s: loop %q is off or a row of loops", name, p.Loop)
		}
		if p.TestFirst && !p.Checks {
			bad("harnessProfiles.%s: testFirst needs checks (R17)", name)
		}
		if p.Template == TemplateNone && (p.Checks || p.TestFirst || p.Tier != "" || p.Loop != "") {
			bad("harnessProfiles.%s: template none runs nothing, so it takes no other key", name)
		}
	}
	if pm := c.PolicyManagement; pm.DualEvaluation && !pm.Read {
		bad("policyManagement.dualEvaluation needs read: it evaluates the policy read observes")
	}
	if pm := c.PolicyManagement; pm.ApprovalMemory && !pm.Read {
		bad("policyManagement.approvalMemory needs read: a grant is bound to the policy read observes")
	}
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
