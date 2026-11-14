// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// noTemplates is good without its templates block: a Factory that names no template, profile or
// loops row, which the shipped workflows fill.
var noTemplates = strings.Replace(good, `templates:
  solo: {roles: [implementer]}
  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}
  trio: {roles: [implementer, tester, reviewer], maxReviewRounds: 2}
  investigate: {roles: [triager]}  # R38: proposes a public issue text, never writes
`, "", 1)

func mustParse(t *testing.T, raw string) *Config {
	t.Helper()
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestShippedWorkflowsFillAnEmptyFactory(t *testing.T) {
	if noTemplates == good {
		t.Fatal("the edit did not apply")
	}
	c := mustParse(t, noTemplates)
	for _, n := range []string{"quick", "standard", "careful", "none"} {
		if _, ok := c.HarnessProfiles[n]; !ok {
			t.Fatalf("workflow %s is not shipped", n)
		}
	}
	if got := c.Templates["solo"].Roles; !slices.Equal(got, []string{"implementer"}) {
		t.Fatalf("solo roles = %v", got)
	}
	if p := c.Templates["pair"]; !slices.Equal(p.Roles, []string{"implementer", "reviewer"}) || p.MaxReviewRounds != 2 {
		t.Fatalf("pair = %+v", p)
	}
	if got := c.Templates["investigate"].Roles; !slices.Equal(got, []string{"triager"}) {
		t.Fatalf("investigate roles = %v", got)
	}
	if p := c.HarnessProfiles["careful"]; p.Template != "pair" || p.Tier != "frontier" || p.Loop != "careful" || p.Description == "" {
		t.Fatalf("careful = %+v", p)
	}
	if p := c.HarnessProfiles["none"]; p != (HarnessProfile{Template: TemplateNone}) {
		t.Fatalf("none = %+v", p)
	}
	if c.ScopeFor("careful") != ScopeScoper || c.LoopFor("careful") == nil || c.LoopFor("careful").JudgeModel != "tier-light" || !c.SearchFor("careful") {
		t.Fatal("the careful row is not the shipped one")
	}
	if c.ScopeFor("default") != ScopeOff || c.LoopFor("docs") != nil {
		t.Fatal("the shipped default row switches something on")
	}
	for _, p := range c.HarnessProfiles {
		if len(p.Description) > MaxDescription {
			t.Fatalf("description over %d bytes: %q", MaxDescription, p.Description)
		}
	}
}

func TestShippedWorkflowsNeverOverride(t *testing.T) {
	c := mustParse(t, strings.Replace(noTemplates, "caps:", `harnessProfiles: {standard: {template: solo, checks: true}}
loops: {careful: {scope: steering}}
templates: {solo: {roles: [implementer, reviewer], maxReviewRounds: 2}}
caps:`, 1))
	if p := c.HarnessProfiles["standard"]; p.Template != "solo" || !p.Checks || p.Description != "" {
		t.Fatalf("the platform's standard was touched: %+v", p)
	}
	if c.ScopeFor("careful") != ScopeSteering || c.LoopFor("careful") != nil || c.SearchFor("careful") || c.Loops["careful"].Split != nil {
		t.Fatal("the platform's careful row was merged with the shipped one")
	}
	if got := c.Templates["solo"].Roles; len(got) != 2 {
		t.Fatalf("the platform's solo was replaced: %v", got)
	}
}

// A platform's own careful profile that names no loop keeps the shipped careful row out: the row
// would be a loops key nothing names, which validateLoops refuses.
func TestShippedCarefulRowFollowsTheProfile(t *testing.T) {
	c := mustParse(t, strings.Replace(noTemplates, "caps:", "harnessProfiles: {careful: {template: pair}}\ncaps:", 1))
	if _, ok := c.Loops["careful"]; ok {
		t.Fatalf("the shipped careful row was added beside the platform's careful: %+v", c.Loops["careful"])
	}
}

// The shipped careful row offers a split only where one can run: a split's family cap is in USD,
// so it needs a meter with prices and runs that carry a gateway identity (validateLoops).
func TestShippedCarefulSplitNeedsAMeter(t *testing.T) {
	if s := mustParse(t, noTemplates).Loops["careful"].Split; s != nil {
		t.Fatalf("a factory without prices or a gateway identity was shipped a split: %+v", s)
	}
	withPrices := strings.Replace(noTemplates, "  logsURL:",
		"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1)
	s := mustParse(t, withPrices+gatewayIdentity).Loops["careful"].Split
	if s == nil || s.MaxChildren != 4 || s.ChildTier != "standard" {
		t.Fatalf("careful split = %+v", s)
	}
}

func TestTrioIsOptional(t *testing.T) {
	c := mustParse(t, noTemplates)
	if _, ok := c.Templates["trio"]; ok {
		t.Fatal("trio is shipped")
	}
}

// warnsOf reports whether warnings name path as refused from the next minor (WF13).
func warnsOf(warnings []string, path string) bool {
	return slices.ContainsFunc(warnings, func(w string) bool {
		return strings.HasPrefix(w, path+":") && strings.Contains(w, "refused from the next minor (WF13)")
	})
}

// clean is noTemplates without the dead settings: defaults without template and tier, triage
// without classifierURL.
var clean = strings.NewReplacer(
	"defaults: {template: solo, tier: standard, dataClass: public, predictedClass: review}", "defaults: {dataClass: public, predictedClass: review}",
	`triage: {classifierURL: "http://complexity-classifier.agent-system.svc.cluster.local:8080/v1/classify", controlPercent: 10}`, "triage: {controlPercent: 10}",
).Replace(noTemplates)

// WF9, WF13: each setting release N+1 refuses loads with a warning naming it, and the Factory's
// Deprecated condition lists it.
func TestDeadSettingsWarn(t *testing.T) {
	c := mustParse(t, good+`harnessProfiles: {mine: {template: pair}}
loops: {docs: {scope: scoper}}
`)
	paths := []string{"templates.trio", "triage.classifierURL", "defaults.template", "defaults.tier", "harnessProfiles", "loops.docs"}
	for _, want := range paths {
		if !warnsOf(c.Warnings, want) {
			t.Errorf("no warning for %s: %q", want, c.Warnings)
		}
		if !slices.Contains(FactoryDeprecations(c), want) {
			t.Errorf("%s not in the Deprecated condition: %q", want, FactoryDeprecations(c))
		}
	}
}

// A file without the dead settings, its profiles under workflows, warns of none of them: the
// shipped workflows and a row named by a workflow or default are not dead.
func TestNoDeadSettingNoWarning(t *testing.T) {
	if clean == noTemplates {
		t.Fatal("the edit did not apply")
	}
	c := mustParse(t, clean+`workflows: {mine: {template: pair, loop: mine}}
loops: {default: {scope: "off"}, mine: {scope: scoper}, careful: {scope: scoper}}
`)
	if _, ok := c.HarnessProfiles["mine"]; !ok {
		t.Fatal("workflows is not read as harnessProfiles")
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "(WF13)") {
			t.Errorf("warned: %s", w)
		}
	}
	if got := FactoryDeprecations(c); len(got) != 0 {
		t.Errorf("deprecated: %q", got)
	}
}

// WF9: workflows and harnessProfiles are one map under two names; both at once is refused.
func TestWorkflowsAndHarnessProfilesTogetherRefused(t *testing.T) {
	_, err := Parse([]byte(clean + `workflows: {a: {template: pair}}
harnessProfiles: {b: {template: pair}}
`))
	if err == nil || !strings.Contains(err.Error(), "workflows and harnessProfiles: set one") {
		t.Fatalf("got %v", err)
	}
}
