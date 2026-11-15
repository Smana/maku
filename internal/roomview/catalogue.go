// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// CatalogueText is a repository's catalogue as the text a person reads: one line per offered class
// (or the one class asked for, with its ceiling and description), then the workflows any task may
// run, the repository's default first, with each one's roles, tier, tests-first, checks, loop,
// escalation and description, and whether the caller may pin one. The word is "workflow" (WF1);
// since the workflow left the class (WF4), a class says only how its tasks merge.
//
// Names and descriptions come from the factory's config, which a repository's maintainers write,
// so they are untrusted like any other text a view quotes: a name is plain unless it holds markup
// (then a code span) and a description is quoted on one line, cut at the view's bound. The merge
// words are the broker's, from catalogue.Outcome, never a field of the config (P3).
func CatalogueText(c catalogue.Catalogue, class string) string {
	var b strings.Builder
	if class == "" {
		b.WriteString("Classes of " + inertWord(c.Repository, clean(c.Repository)) + ", with how each merges.\n")
		for _, cl := range c.Classes {
			b.WriteString(classLine(c, cl) + "\n")
		}
	} else {
		cl, ok := c.Class(class)
		if !ok {
			return "No class " + quote(class) + " is offered on " + inertWord(c.Repository, clean(c.Repository)) + ".\n"
		}
		b.WriteString(classLine(c, cl) + "\n")
		b.WriteString("Ceiling " + dollars(int64(math.Round(cl.CeilingUSD*1e6))) + " a task.\n")
		if d := strip(cl.Description); d != "" {
			b.WriteString("About " + quote(cl.Description) + "\n")
		}
	}
	b.WriteString("Workflows, the same for every class:\n")
	if _, ok := c.Profiles[c.DefaultWorkflow]; ok {
		b.WriteString("- default " + profileLine(c, c.DefaultWorkflow) + "\n")
	}
	for _, n := range slices.Sorted(maps.Keys(c.Profiles)) {
		if n != c.DefaultWorkflow {
			b.WriteString("- " + profileLine(c, n) + "\n")
		}
	}
	if class == "" {
		b.WriteString("Evaluation " + inertWord(c.Evaluation.Mode, clean(c.Evaluation.Mode)) + ", control share " + strconv.Itoa(c.Evaluation.ControlPercent) + " %.\n")
	}
	b.WriteString(pinLine(c))
	return b.String()
}

// MergeWords is how a task of the class merges, in the broker's words.
func MergeWords(outcome string) string {
	switch outcome {
	case catalogue.OutcomeAuto:
		return "merges on its own when the gate's conditions hold"
	case catalogue.OutcomeShadow:
		return "a maintainer merges; the factory records whether it would have"
	case catalogue.OutcomeApproval:
		return "a maintainer's approval merges it"
	}
	return "a maintainer merges it"
}

// classLine is `docs-links · live · merges on its own when the gate's conditions hold`.
func classLine(c catalogue.Catalogue, cl catalogue.Class) string {
	return name(cl.Name) + " · " + name(cl.Mode) + " · " + MergeWords(catalogue.Outcome(cl.Mode, c.ApproveToMerge))
}

// profileLine is one workflow: its roles, tier, tests-first, checks, loop, escalation and description.
func profileLine(c catalogue.Catalogue, n string) string {
	p, ok := c.Profiles[n]
	if !ok {
		return name(n) + ": not in the factory's list of workflows"
	}
	var parts []string
	if p.Offered {
		parts = append(parts, "roles "+strings.Join(mapStrings(p.Roles, name), ", "))
	} else {
		parts = append(parts, "roles chosen at triage, not pinnable")
	}
	if p.Tier != "" {
		parts = append(parts, "tier "+name(p.Tier))
	}
	if p.TestFirst {
		parts = append(parts, "tests first")
	}
	if p.Checks {
		parts = append(parts, "checks on")
	}
	if p.Loop != "" {
		parts = append(parts, "loop "+name(p.Loop))
	}
	if e := p.Escalation; e != nil && e.OutOfBudget != "" {
		parts = append(parts, "out of budget goes to "+name(e.OutOfBudget))
	}
	if e := p.Escalation; e != nil && e.NotConverged != "" {
		parts = append(parts, "not converged goes to "+name(e.NotConverged))
	}
	if strip(p.Description) != "" {
		parts = append(parts, quote(p.Description))
	}
	return name(n) + ": " + strings.Join(parts, " · ")
}

// pinLine says whether the caller may pin a workflow, and which. Only a maintainer may (P5), on any
// class (WF4): the workflow:<name> label or the confirmation's choice.
func pinLine(c catalogue.Catalogue) string {
	if !c.Maintainer {
		return "Only a maintainer of this repository can pin a workflow; the factory chooses it.\n"
	}
	offered := c.Workflows()
	if len(offered) == 0 {
		return "You may pin no workflow here: none is offered.\n"
	}
	return "You may pin " + strings.Join(mapStrings(offered, name), ", ") + " with a workflow:<name> label.\n"
}

// name is a config name on one line, plain unless it holds markup.
func name(s string) string { return inertWord(s, clean(s)) }

func mapStrings(in []string, f func(string) string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, f(s))
	}
	return slices.Clip(out)
}
