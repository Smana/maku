// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
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
}

// PolicyManagement holds the rollout's switches, all off by default (merge policy stages).
type PolicyManagement struct {
	// Read observes each owned repository's class policy and compares it with this config. It
	// decides nothing.
	Read bool `json:"read,omitempty"`
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
