// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// The workflows the evaluation tests name, and their loops rows, each named after its workflow or
// default (WF10, stage B).
const evaluationProfiles = `workflows:
  review-floor:  {template: pair, loop: review-floor, description: "default: scope, implement, review, judge"}
  review-search: {template: pair, loop: review-search, description: "the task depends on code the issue does not name"}
  unlooped:      {template: pair, loop: "off"}
  bare:          {}
loops:
  review-floor:  {scope: scoper, judge: tier-light}
  review-search: {scope: scoper, judge: tier-light, search: true}
`

const evaluationExample = "evaluation: {controlPercent: 10, mode: observe}\n"

func evaluated(t *testing.T, extra string) string {
	t.Helper()
	return good + evaluationProfiles + extra
}

func TestEvaluationTheSpecsExampleLoads(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, evaluationExample)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Evaluation.ControlPercent != 10 || c.Evaluation.Mode != EvaluationObserve || c.Applies() {
		t.Fatalf("%+v", c.Evaluation)
	}
	if p, ok := c.Workflow("review-floor"); !ok || p.Loop != "review-floor" {
		t.Fatalf("Workflow(review-floor) = %+v, %t", p, ok)
	}
	if _, ok := c.Workflow("none"); ok {
		t.Fatal("Workflow: none runs nothing, so it is no workflow a task runs")
	}
	if _, ok := c.Workflow("nope"); ok {
		t.Fatal("Workflow: an unknown name")
	}
}

func TestEvaluationRefuses(t *testing.T) {
	long := strings.Repeat("x", 121)
	for name, c := range map[string]struct{ extra, err string }{
		"mode":           {"evaluation: {mode: maybe}\n", `evaluation.mode is observe or apply`},
		"controlPercent": {"evaluation: {controlPercent: 101}\n", `evaluation.controlPercent is 0 to 100`},
		"description":    {"", `description is 120 bytes at most`},
	} {
		t.Run(name, func(t *testing.T) {
			raw := evaluated(t, c.extra)
			if name == "description" {
				raw = strings.Replace(raw, `description: "the task depends on code the issue does not name"`, `description: "`+long+`"`, 1)
			}
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
}

func TestEvaluationApplies(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, "evaluation: {mode: apply}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Applies() {
		t.Fatal("mode apply applies")
	}
}

// RowFor: a workflow's loop, else the row named after it, which rowOf reads as default's when the
// workflow has none (WF10, stage B).
func TestRowForOff(t *testing.T) {
	c, err := Parse([]byte(evaluated(t, "") + "  default: {scope: scoper, judge: tier-light, search: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.RowFor("unlooped", c.HarnessProfiles["unlooped"]); got != "off" {
		t.Fatalf("RowFor = %q, want off", got)
	}
	if got := c.RowFor("bare", c.HarnessProfiles["bare"]); got != "bare" {
		t.Fatalf("RowFor without a loop = %q, want the workflow's own name", got)
	}
	if got := c.RowFor("review-search", c.HarnessProfiles["review-search"]); got != "review-search" {
		t.Fatalf("RowFor = %q, want the workflow's row", got)
	}
	if c.LoopFor("off") != nil || c.ScopeFor("off") != ScopeOff || c.SearchFor("off") {
		t.Fatal("row off is inactive, never default's")
	}
	if c.LoopFor("bare") == nil || c.ScopeFor("bare") != ScopeScoper {
		t.Fatal("a workflow without a row still reads default's")
	}
}

// WF8: evaluation.treatment names the workflow the treatment cohort runs; an unknown name, or
// none, is refused, a known one loads.
func TestEvaluationTreatmentMustBeAWorkflow(t *testing.T) {
	for _, n := range []string{"nope", "none"} {
		_, err := Parse([]byte(evaluated(t, "evaluation: {controlPercent: 50, treatment: "+n+"}\n")))
		if err == nil || !strings.Contains(err.Error(), `evaluation.treatment "`+n+`" is not a workflow`) {
			t.Fatalf("%s: got %v", n, err)
		}
	}
	c, err := Parse([]byte(evaluated(t, "evaluation: {controlPercent: 50, treatment: review-search}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Evaluation.Treatment != "review-search" {
		t.Fatalf("%+v", c.Evaluation)
	}
}
