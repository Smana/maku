// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// noTemplates is good without its templates block: a Factory that names no template, profile or
// loops row, which the shipped workflows fill.
var noTemplates = strings.Replace(good, `templates:
  solo: {roles: [implementer]}
  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}
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
	if p := c.HarnessProfiles["none"]; !reflect.DeepEqual(p, HarnessProfile{Template: TemplateNone}) {
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
	c := mustParse(t, strings.Replace(noTemplates, "caps:", `workflows: {standard: {template: solo, checks: true}}
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
	c := mustParse(t, strings.Replace(noTemplates, "caps:", "workflows: {careful: {template: pair}}\ncaps:", 1))
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

// removedIn reports whether err refuses path as removed and points to the upgrade notes.
func removedIn(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want) && strings.Contains(err.Error(), "docs/upgrading.md")
}

// WF9, WF13: each setting release N warned about is refused, naming what to write instead.
func TestRemovedSettingsRefused(t *testing.T) {
	for y, want := range map[string]string{
		"templates: {trio: {roles: [implementer]}}\n":         "templates.trio: removed",
		"triage: {classifierURL: \"http://c7\"}\n":            "triage.classifierURL: removed",
		"defaults: {template: solo}\n":                        "defaults.template: removed",
		"defaults: {tier: standard}\n":                        "defaults.tier: removed",
		"harnessProfiles: {a: {template: pair}}\n":            "harnessProfiles: renamed workflows",
		"workflows: {a: {template: pair, testFirst: true}}\n": "workflows.a.testFirst: removed",
	} {
		raw := strings.NewReplacer("defaults: {dataClass: public, predictedClass: review}\n", "", "triage: {controlPercent: 10}\n", "").Replace(clean)
		if _, err := Parse([]byte(raw + y)); !removedIn(err, want) {
			t.Errorf("%s: got %v", y, err)
		}
		if err := CheckShape([]byte(raw + y)); !removedIn(err, want) {
			t.Errorf("CheckShape %s: got %v", y, err)
		}
	}
}

// clean is noTemplates as release N+1 accepts it: the fixture writes no removed setting.
var clean = noTemplates

// A file without the removed settings, its profiles under workflows, loads with no warning about
// them: the shipped workflows and a row named by a workflow or default are kept.
func TestWorkflowsLoad(t *testing.T) {
	c := mustParse(t, clean+`workflows: {mine: {template: pair, loop: mine}}
loops: {default: {scope: "off"}, mine: {scope: scoper}, careful: {scope: scoper}}
`)
	if _, ok := c.HarnessProfiles["mine"]; !ok {
		t.Fatal("workflows is not read")
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

// WF10, stage B: a loops row is named default or a workflow; a class row is refused.
func TestLoopsKeyedByWorkflow(t *testing.T) {
	_, err := Parse([]byte(clean + "loops: {docs: {scope: scoper}}\n"))
	if err == nil || !strings.Contains(err.Error(), "loops.docs: a row is named default or a workflow") {
		t.Fatalf("got %v", err)
	}
}

// repoWith is clean with y added to its one repository's entry.
func repoWith(y string) string {
	return strings.Replace(clean, "    triggerLabel: factory/ready\n", "    triggerLabel: factory/ready\n    "+y+"\n", 1)
}

func TestRepositoryDefaultWorkflow(t *testing.T) {
	if got := (&Repository{}).DefaultWorkflow(); got != "standard" {
		t.Fatalf("got %q", got)
	}
	c := mustParse(t, repoWith("workflow: {default: careful}"))
	if got := c.Repos()[0].DefaultWorkflow(); got != "careful" {
		t.Fatalf("got %q", got)
	}
}

func TestRepositoryWorkflowProblems(t *testing.T) {
	for y, want := range map[string]string{
		"workflow: {default: nope}":   `workflow.default "nope" is not a workflow`,
		"workflow: {default: none}":   "workflow.default: none is for adopted pull requests",
		"workflow: {testFirst: true}": "workflow.testFirst needs merge.contractCheck",
	} {
		if _, err := Parse([]byte(repoWith(y))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", y, err)
		}
	}
}

func TestScheduleWorkflowDefaultsToQuick(t *testing.T) {
	c := mustParse(t, repoWith(`schedules: [{name: s, cron: "0 7 * * *", class: docs, text: x}, {name: t, cron: "0 8 * * *", class: docs, text: z, workflow: careful}]`))
	if got := c.Repos()[0].Schedules[0].WorkflowOr(); got != "quick" {
		t.Fatalf("got %q", got)
	}
	if got := c.Repos()[0].Schedules[1].WorkflowOr(); got != "careful" {
		t.Fatalf("got %q", got)
	}
	if _, err := Parse([]byte(repoWith(`schedules: [{name: s, cron: "0 7 * * *", class: docs, text: x, workflow: nope}]`))); err == nil ||
		!strings.Contains(err.Error(), `workflow "nope" is not a workflow`) {
		t.Fatalf("an unknown schedule workflow: got %v", err)
	}
}

// WF7: escalation lives on the workflow it steps from, and steps to a defined workflow.
func TestWorkflowEscalation(t *testing.T) {
	c := mustParse(t, clean+"workflows: {standard: {template: pair, escalation: {notConverged: careful}}}\n")
	if e := c.HarnessProfiles["standard"].Escalation; e == nil || e.NotConverged != "careful" {
		t.Fatalf("escalation = %+v", e)
	}
	for y, want := range map[string]string{
		"workflows: {standard: {template: pair, escalation: {notConverged: nope}}}\n": `workflows.standard: escalation notConverged "nope" is not a workflow`,
		"workflows: {standard: {template: pair, escalation: {outOfBudget: none}}}\n":  `workflows.standard: escalation outOfBudget "none" is not a workflow`,
	} {
		if _, err := Parse([]byte(clean + y)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", y, err)
		}
	}
}
