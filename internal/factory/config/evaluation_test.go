// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// The spec's U1 example: the floor, three variants and the two loop rows.
const evaluationProfiles = `harnessProfiles:
  review-floor:    {template: pair, loop: review, description: "default: scope, implement, review, judge"}
  review-search:   {template: pair, loop: review-search, description: "the task depends on code the issue does not name"}
  review-tested:   {template: pair, loop: review, checks: true, testFirst: true, description: "a testable behaviour under tests.paths"}
  review-frontier: {template: pair, loop: review, tier: frontier, description: "a large or long change"}
  solo:            {template: solo, loop: review}
  light:           {template: pair, loop: review, tier: light}
  standard:        {template: pair, loop: review, tier: standard}
  unlooped:        {template: pair, loop: "off"}
  implicit:        {template: pair, loop: stalled}
  unjudged:        {template: pair, loop: nojudge}
  unscoped:        {template: pair, loop: judged}
  untemplated:     {loop: review}
  bare:            {}
  bare-checked:    {checks: true}
loops:
  stalled:       {stallSteps: 90}
  nojudge:       {stallSteps: 90, judge: none}
  judged:        {stallSteps: 90, judge: tier-light}
  review:        {scope: scoper, judge: tier-light}
  review-search: {scope: scoper, judge: tier-light, search: true}
`

const evaluationExample = "evaluation: {controlPercent: 10, mode: observe}\n"

const reviewClass = "review: {harness: review-floor, harnessAllowed: [review-search, review-tested, review-frontier], escalation: {outOfBudget: review-frontier, notConverged: review-search}}"

const contractAnchor = `  leakScanCheck: "Security scanning 🔒"`

func evaluated(t *testing.T, class, extra string) string {
	t.Helper()
	raw := withClasses(t, "docs-links: {shadow: true}\n  revert: {shadow: true}\n  docs: {}\n  "+class) + evaluationProfiles + extra
	if !strings.Contains(raw, contractAnchor) {
		t.Fatal("the fixture's leakScanCheck line moved")
	}
	return strings.Replace(raw, contractAnchor, contractAnchor+"\n      contractCheck: Test contract", 1)
}

func TestEvaluationTheSpecsExampleLoads(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, reviewClass, evaluationExample)))
	if err != nil {
		t.Fatal(err)
	}
	cl := c.RepositoryFor("Smana/cloud-native-ref").Classes["review"]
	if cl.Harness != "review-floor" || len(cl.HarnessAllowed) != 3 || cl.Escalation == nil || cl.Escalation.OutOfBudget != "review-frontier" || cl.Escalation.NotConverged != "review-search" {
		t.Fatalf("%+v", cl)
	}
	if c.Evaluation.ControlPercent != 10 || c.Evaluation.Mode != EvaluationObserve || c.Applies() {
		t.Fatalf("%+v", c.Evaluation)
	}
	if p, ok := c.ProfileFor(cl, "review-tested"); !ok || !p.TestFirst {
		t.Fatalf("ProfileFor(review-tested) = %+v, %t", p, ok)
	}
	if _, ok := c.ProfileFor(cl, "solo"); ok {
		t.Fatal("ProfileFor: a profile the class does not allow is not the class's")
	}
	if p, ok := c.ProfileFor(cl, "review-floor"); !ok || p.Loop != "review" {
		t.Fatalf("ProfileFor(floor) = %+v, %t", p, ok)
	}
}

func TestEvaluationRefuses(t *testing.T) {
	long := strings.Repeat("x", 121)
	for name, c := range map[string]struct{ class, extra, err string }{
		"unknown harness":              {"review: {harness: nope}", "", `harness "nope" is not in harnessProfiles (TE2)`},
		"unknown allowed":              {"review: {harness: review-floor, harnessAllowed: [nope]}", "", `harnessAllowed "nope" is not in harnessProfiles (TE2)`},
		"escalation not allowed":       {"review: {harness: review-floor, harnessAllowed: [review-search], escalation: {outOfBudget: review-frontier}}", "", `escalation outOfBudget "review-frontier" is not in harnessAllowed (TE7)`},
		"fewer roles":                  {"review: {harness: review-floor, harnessAllowed: [solo]}", "", `"solo" drops roles of the floor (TE2)`},
		"lower tier":                   {"review: {harness: standard, harnessAllowed: [light]}", "", `"light" is below the floor's tier (TE2)`},
		"loop off under a judged loop": {"review: {harness: review-floor, harnessAllowed: [unlooped]}", "", `"unlooped" drops the floor's judge (TE6)`},
		"implicit judge, loop off":     {"review: {harness: implicit, harnessAllowed: [unlooped]}", "", `"unlooped" drops the floor's judge (TE6)`},
		"implicit judge, judge none":   {"review: {harness: implicit, harnessAllowed: [unjudged]}", "", `"unjudged" drops the floor's judge (TE6)`},
		"judged, scope dropped":        {"review: {harness: review-floor, harnessAllowed: [unscoped]}", "", `"unscoped" drops the floor's scope (TE6)`},
		"template under triage's":      {"review: {harness: untemplated, harnessAllowed: [solo]}", "", `"solo" sets a template the floor leaves to triage (TE2)`},
		"testFirst without contract":   {reviewClass, "", `class review: profile "review-tested": testFirst needs merge.contractCheck`},
		"live with variants":           {"review: {live: true, harness: review-floor, harnessAllowed: [review-search]}", "", `a live or shadow class has one profile (TE2)`},
		"shadow with escalation":       {"review: {shadow: true, harness: review-floor, harnessAllowed: [review-search], escalation: {notConverged: review-search}}", "", `a live or shadow class has one profile (TE2)`},
		"mode":                         {reviewClass, "evaluation: {mode: maybe}\n", `evaluation.mode is observe or apply`},
		"controlPercent":               {reviewClass, "evaluation: {controlPercent: 101}\n", `evaluation.controlPercent is 0 to 100`},
		"description":                  {reviewClass, "", `description is 120 bytes at most`},
	} {
		t.Run(name, func(t *testing.T) {
			raw := evaluated(t, c.class, c.extra)
			if name == "testFirst without contract" {
				raw = strings.Replace(raw, "\n      contractCheck: Test contract", "", 1)
			}
			if name == "description" {
				raw = strings.Replace(raw, `description: "a large or long change"`, `description: "`+long+`"`, 1)
			}
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
			if !strings.Contains(c.err, "evaluation.") && name != "description" && !strings.Contains(err.Error(), "Smana/cloud-native-ref: ") {
				t.Fatalf("%v: a class's refusal names its repository", err)
			}
		})
	}
}

// Both profiles without a loop run the class's own row: nothing is dropped.
func TestEvaluationSameClassRowLoads(t *testing.T) {
	if _, err := Parse([]byte(evaluated(t, "review: {harness: bare, harnessAllowed: [bare-checked]}", ""))); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationApplies(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, reviewClass, "evaluation: {mode: apply}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Applies() {
		t.Fatal("mode apply applies")
	}
}

func TestRowForOff(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, reviewClass, "") + "  default: {scope: scoper, judge: tier-light, search: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	unlooped := c.HarnessProfiles["unlooped"]
	if got := c.RowFor("docs", unlooped); got != "off" {
		t.Fatalf("RowFor = %q, want off", got)
	}
	if got := c.RowFor("docs", HarnessProfile{}); got != "docs" {
		t.Fatalf("RowFor without a loop = %q, want the class", got)
	}
	if got := c.RowFor("docs", c.HarnessProfiles["review-search"]); got != "review-search" {
		t.Fatalf("RowFor = %q, want the profile's row", got)
	}
	if c.LoopFor("off") != nil || c.ScopeFor("off") != ScopeOff || c.SearchFor("off") {
		t.Fatal("row off is inactive, never default's")
	}
	if c.LoopFor("docs") == nil || c.ScopeFor("docs") != ScopeScoper {
		t.Fatal("a class without a row still reads default's")
	}
}
