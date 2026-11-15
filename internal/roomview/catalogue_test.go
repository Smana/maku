// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// ainative is shaped like ai-native-ref's live config: docs-links live, review off, and the
// workflows every class offers (WF4), careful the default.
func ainative(maintainer bool) catalogue.Catalogue {
	return catalogue.Catalogue{
		Repository: "Smana/ai-native-ref", Maintainer: maintainer, ApproveToMerge: true, DefaultWorkflow: "careful",
		Evaluation: catalogue.Evaluation{Mode: "observe", ControlPercent: 20},
		Classes: []catalogue.Class{
			{Name: "docs-links", Description: "Dead links in docs", Mode: "live", CeilingUSD: 2},
			{Name: "review", Description: "Anything else", Mode: "off", CeilingUSD: 8},
		},
		Profiles: map[string]catalogue.Profile{
			"quick":    {Template: "solo", Roles: []string{"implementer"}, Tier: "light", Offered: true, Description: "One implementer"},
			"standard": {Checks: true, Description: "Triage picks the template", NotOffered: catalogue.NoTemplate},
			"careful": {Template: "pair", Roles: []string{"implementer", "reviewer"}, Tier: "frontier", Checks: true,
				TestFirst: true, Loop: "careful", Offered: true, Description: "Changes across several files",
				Escalation: &catalogue.Escalation{NotConverged: "standard"}},
		},
	}
}

func TestCatalogueTextSummary(t *testing.T) {
	got := CatalogueText(ainative(false), "")
	for _, want := range []string{
		"docs-links · live · merges on its own when the gate's conditions hold\n",
		"review · off · a maintainer's approval merges it\n",
		"Workflows, the same for every class:\n- default careful: ",
		"- quick: roles implementer · tier light",
		"Evaluation observe, control share 20 %.",
		"Only a maintainer of this repository can pin a workflow",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "profile") {
		t.Error("the word is workflow, never profile (WF1)")
	}
}

func TestCatalogueTextDetail(t *testing.T) {
	got := CatalogueText(ainative(true), "review")
	for _, want := range []string{
		"Ceiling $8.00 a task.",
		"- default careful: roles implementer, reviewer · tier frontier · tests first · checks on · loop careful · not converged goes to standard · \"Changes across several files\"",
		"- standard: roles chosen at triage, not pinnable",
		"You may pin careful, quick with a workflow:<name> label.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %q:\n%s", want, got)
		}
	}
	if got := CatalogueText(ainative(false), "review"); strings.Contains(got, "You may pin") {
		t.Errorf("a non-maintainer is offered a pin:\n%s", got)
	}
	// WF4: a live class runs any workflow; the gate holds what it may not merge.
	if live := CatalogueText(ainative(true), "docs-links"); !strings.Contains(live, "You may pin careful, quick") {
		t.Errorf("a live class offers the workflows:\n%s", live)
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
	pf := c.Profiles["careful"]
	pf.Description = "*bold* @here www.evil.example \u200b"
	c.Profiles["careful"] = pf
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
