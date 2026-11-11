// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// ainative is shaped like ai-native-ref's live config: docs-links live, review off with variants.
func ainative(maintainer bool) catalogue.Catalogue {
	return catalogue.Catalogue{
		Repository: "Smana/ai-native-ref", Maintainer: maintainer, ApproveToMerge: true,
		Evaluation: catalogue.Evaluation{Mode: "observe", ControlPercent: 20},
		Classes: []catalogue.Class{
			{Name: "docs-links", Description: "Dead links in docs", Mode: "live", CeilingUSD: 2, Floor: "links-solo", Allowed: []string{}},
			{Name: "review", Description: "Anything else", Mode: "off", CeilingUSD: 8, Floor: "review-floor", Allowed: []string{"review-frontier"},
				Escalation: &catalogue.Escalation{OutOfBudget: "review-frontier", NotConverged: "review-frontier"}},
		},
		Profiles: map[string]catalogue.Profile{
			"links-solo":   {Template: "solo", Roles: []string{"implementer"}, Tier: "light", Offered: true, Description: "One file of links, no logic"},
			"review-floor": {Checks: true, Description: "The default: triage picks pair or trio", NotOffered: catalogue.NoTemplate},
			"review-frontier": {Template: "trio", Roles: []string{"implementer", "tester", "reviewer"}, Tier: "frontier", Checks: true,
				TestFirst: true, Loop: "review", Offered: true, Description: "Changes across several files"},
		},
	}
}

func TestCatalogueTextSummary(t *testing.T) {
	got := CatalogueText(ainative(false), "")
	for _, want := range []string{
		"docs-links · live · merges on its own when the gate's conditions hold · profile links-solo\n",
		"review · off · a maintainer's approval merges it · profile chosen at triage · variants review-frontier\n",
		"Evaluation observe, control share 20 %.",
		"Only a maintainer of this repository can pin a profile",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "workflow") {
		t.Error("the word is profile, never workflow (P1)")
	}
}

func TestCatalogueTextDetail(t *testing.T) {
	got := CatalogueText(ainative(true), "review")
	for _, want := range []string{
		"Ceiling $8.00 a task.",
		"Floor review-floor: roles chosen at triage, not pinnable",
		"- review-frontier: roles implementer, tester, reviewer · tier frontier · tests first · checks on · loop review · \"Changes across several files\"",
		"Escalation: out of budget goes to review-frontier; not converged goes to review-frontier.",
		"You may pin review-frontier.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %q:\n%s", want, got)
		}
	}
	if got := CatalogueText(ainative(false), "review"); strings.Contains(got, "You may pin") {
		t.Errorf("a non-maintainer is offered a pin:\n%s", got)
	}
	live := CatalogueText(ainative(true), "docs-links")
	if !strings.Contains(live, "Fixed: this class's merge policy runs it under links-solo only") || strings.Contains(live, "You may pin") {
		t.Errorf("a live class has one profile:\n%s", live)
	}
	if got := CatalogueText(ainative(true), "nope"); !strings.Contains(got, `No class "nope" is offered`) {
		t.Errorf("unknown class:\n%s", got)
	}
}

// A class or profile description is a maintainer's text: control text, markup and links must stay
// inert, and a hidden character never reaches the reader.
func TestCatalogueTextInert(t *testing.T) {
	c := ainative(true)
	c.Classes[1].Description = "line one\n# heading <!channel> [x](http://evil.example) \u202e`rm`"
	pf := c.Profiles["review-frontier"]
	pf.Description = "*bold* @here www.evil.example \u200b"
	c.Profiles["review-frontier"] = pf
	c.Classes[1].Name = "re*view_"
	for _, class := range []string{"", "re*view_"} {
		got := CatalogueText(c, class)
		for _, bad := range []string{"\u202e", "\u200b", "\x1b"} {
			if strings.Contains(got, bad) {
				t.Errorf("hidden character %q reached the text:\n%s", bad, got)
			}
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "#") {
				t.Errorf("a description made a heading: %q", line)
			}
		}
	}
	got := CatalogueText(c, "re*view_")
	if !strings.Contains(got, `About "line one # heading <!channel> [x](http://evil.example) `+"`rm`"+`"`) {
		t.Errorf("a description is one quoted line:\n%s", got)
	}
	if !strings.Contains(got, "`re*view_`") {
		t.Errorf("a name with markup is a code span:\n%s", got)
	}
}
