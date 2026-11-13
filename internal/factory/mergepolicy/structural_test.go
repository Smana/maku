// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

const base = `factory: aws-0
classes:
  docs-broad:
    mode: shadow
    author: agents
    harness: plain
    conditions:
      all:
        - allowedFiles: ['^docs/', '^README\.md$']
        - maxPatchFiles: 10
        - holdoutChecks: [links]
      deny:
        - protectedFiles: ['^docs/specs/']
    requiresApproval:
      - changedFiles: ['^docs/security/']
    limits:
      maxPerDay: 2
    monitor:
      followUpWindow: 168h
`

func structural(t *testing.T, old, next string) Change {
	t.Helper()
	c := central()
	c.HarnessProfiles["other"] = config.HarnessProfile{}
	parse := func(raw string) *File {
		f, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	ch, err := Structural(parse(old), parse(next), nil, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 1 {
		t.Fatalf("%+v", ch)
	}
	return ch[0]
}

// MP8: only a change proven to narrow what merges is a tightening; anything else, or anything
// unproven, is a relaxation and returns the class to shadow (from stage 4).
func TestStructural(t *testing.T) {
	r := strings.Replace
	for name, tc := range map[string]struct {
		next, kind, why string
	}{
		"the same":                {base, KindUnchanged, ""},
		"a lower cap":             {r(base, "maxPatchFiles: 10", "maxPatchFiles: 5", 1), KindTightening, ""},
		"a higher cap":            {r(base, "maxPatchFiles: 10", "maxPatchFiles: 11", 1), KindRelaxation, "maxPatchFiles"},
		"a cap removed":           {r(base, "        - maxPatchFiles: 10\n", "", 1), KindRelaxation, "maxPatchFiles"},
		"a cap added":             {r(base, "        - maxPatchFiles: 10\n", "        - maxPatchFiles: 10\n        - maxModifiedLines: 9\n", 1), KindTightening, ""},
		"a path removed":          {r(base, `, '^README\.md$'`, "", 1), KindTightening, ""},
		"a path added":            {r(base, `'^docs/', `, `'^docs/', '^site/', `, 1), KindRelaxation, "allowedFiles"},
		"a path rewritten":        {r(base, `'^docs/'`, `'^docs/a/'`, 1), KindRelaxation, "allowedFiles"},
		"a deny added":            {r(base, `['^docs/specs/']`, `['^docs/specs/', '^docs/adr/']`, 1), KindTightening, ""},
		"a deny removed":          {r(base, "      deny:\n        - protectedFiles: ['^docs/specs/']\n", "", 1), KindRelaxation, "protectedFiles"},
		"an approval narrowed":    {r(base, "    requiresApproval:\n      - changedFiles: ['^docs/security/']\n", "", 1), KindRelaxation, "requiresApproval"},
		"an approval widened":     {r(base, `['^docs/security/']`, `['^docs/security/', '^docs/arch/']`, 1), KindTightening, ""},
		"a holdout removed":       {r(base, "        - holdoutChecks: [links]\n", "", 1), KindRelaxation, "holdoutChecks"},
		"promoted":                {r(base, "mode: shadow", "mode: live", 1), KindRelaxation, "mode"},
		"demoted":                 {r(base, "mode: shadow", "mode: off", 1), KindTightening, ""},
		"another harness":         {r(base, "harness: plain", "harness: other", 1), KindRelaxation, "harness"},
		"the monitor removed":     {r(base, "    monitor:\n      followUpWindow: 168h\n", "", 1), KindRelaxation, "monitor"},
		"the monitor shortened":   {r(base, "168h", "24h", 1), KindRelaxation, "monitor"},
		"a day cap raised":        {r(base, "maxPerDay: 2", "maxPerDay: 3", 1), KindRelaxation, "maxPerDay"},
		"a shape added":           {r(base, "        - holdoutChecks: [links]\n", "        - holdoutChecks: [links]\n        - diffShape: links-only\n", 1), KindTightening, ""},
		"modifyOnly added":        {r(base, "        - holdoutChecks: [links]\n", "        - holdoutChecks: [links]\n        - modifyOnly: true\n", 1), KindTightening, ""},
		"split into two branches": {r(base, "      deny:\n", "      any:\n        - [{maxModifiedLines: 5}]\n        - [{modifyOnly: true}]\n      deny:\n", 1), KindTightening, ""},
	} {
		t.Run(name, func(t *testing.T) {
			ch := structural(t, base, tc.next)
			if ch.Kind != tc.kind || !strings.Contains(strings.Join(ch.Why, ";"), tc.why) {
				t.Fatalf("got %s %v, want %s naming %q", ch.Kind, ch.Why, tc.kind, tc.why)
			}
		})
	}
}

// The harness axes are not evidence rows: a variant or a step added or changed is unproven, as
// another harness is; a variant or the step removed runs a subset of what ran.
func TestTheHarnessAxesAreNotProvenNeutral(t *testing.T) {
	r := strings.Replace
	off := r(base, "mode: shadow", "mode: off", 1)
	allowed := r(off, "harness: plain\n", "harness: plain\n    harnessAllowed: [other]\n", 1)
	stepped := r(allowed, "harnessAllowed: [other]\n", "harnessAllowed: [other]\n    escalation: {outOfBudget: other}\n", 1)
	review := "factory: aws-0\nclasses:\n  review: {harness: plain, "
	for name, tc := range map[string]struct {
		old, next, kind, why string
	}{
		"a variant added":     {off, allowed, KindRelaxation, "harnessAllowed"},
		"a variant removed":   {allowed, off, KindTightening, ""},
		"a variant rewritten": {allowed, r(allowed, "[other]", "[checked]", 1), KindRelaxation, "harnessAllowed"},
		"a step added":        {allowed, stepped, KindRelaxation, "escalation"},
		"a step removed":      {stepped, allowed, KindTightening, ""},
		"a step retargeted":   {stepped, r(r(stepped, "[other]", "[other, checked]", 1), "outOfBudget: other", "outOfBudget: checked", 1), KindRelaxation, "escalation"},
		"a step regrouped":    {stepped, r(stepped, "outOfBudget", "notConverged", 1), KindRelaxation, "escalation"},
		// R30: review's axes read as any class's; it has no mode to promote or demote.
		"review unchanged":       {review + "harnessAllowed: [other]}\n", review + "harnessAllowed: [other]}\n", KindUnchanged, ""},
		"review variant added":   {review + "}\n", review + "harnessAllowed: [other]}\n", KindRelaxation, "harnessAllowed"},
		"review variant removed": {review + "harnessAllowed: [other]}\n", review + "}\n", KindTightening, ""},
		"review harness changed": {review + "}\n", r(review, "plain", "other", 1) + "}\n", KindRelaxation, "harness"},
	} {
		t.Run(name, func(t *testing.T) {
			ch := structural(t, tc.old, tc.next)
			if ch.Kind != tc.kind || !strings.Contains(strings.Join(ch.Why, ";"), tc.why) {
				t.Fatalf("got %s %v, want %s naming %q", ch.Kind, ch.Why, tc.kind, tc.why)
			}
		})
	}
}

// A branch that no old variant bounds is a relaxation, even when the others tighten.
func TestABroadeningBranchRelaxes(t *testing.T) {
	two := strings.Replace(strings.Replace(base, "        - maxPatchFiles: 10\n", "", 1), "      deny:\n", "      any:\n        - [{maxPatchFiles: 3}]\n        - [{maxPatchFiles: 5}]\n      deny:\n", 1)
	if ch := structural(t, two, strings.Replace(two, "maxPatchFiles: 5", "maxPatchFiles: 6", 1)); ch.Kind != KindRelaxation {
		t.Fatalf("%+v", ch)
	}
	if ch := structural(t, two, strings.Replace(two, "maxPatchFiles: 5", "maxPatchFiles: 2", 1)); ch.Kind != KindTightening {
		t.Fatalf("%+v", ch)
	}
}

// A class added is new authority; one removed merges nothing more.
func TestAddedAndRemovedClasses(t *testing.T) {
	c := central()
	old, _ := Parse([]byte(base))
	added, _ := Parse([]byte(base + "  more: {mode: shadow, author: agents, harness: plain}\n  idle: {mode: off, author: agents, harness: plain}\n"))
	ch, err := Structural(old, added, nil, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range ch {
		got[x.Class] = x.Kind
	}
	if got["more"] != KindRelaxation || got["idle"] != KindTightening || got["docs-broad"] != KindUnchanged {
		t.Fatalf("%+v", ch)
	}
	ch, err = Structural(added, old, nil, c)
	if err != nil || len(ch) != 3 || ch[1].Kind != KindTightening || ch[1].Class != "idle" || ch[2].Kind != KindTightening {
		t.Fatalf("%+v %v", ch, err)
	}
}
