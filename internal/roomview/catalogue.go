// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// CatalogueText is a repository's catalogue as the text a person reads: without class, one line per
// offered class; with it, that class's floor, variants and escalation, each profile's roles, tier,
// tests-first, loop and description, and whether the caller may pin. The word is "profile" (P1).
//
// Names and descriptions come from the factory's config, which a repository's maintainers write,
// so they are untrusted like any other text a view quotes: a name is plain unless it holds markup
// (then a code span) and a description is quoted on one line, cut at the view's bound. The merge
// words are the broker's, from catalogue.Outcome, never a field of the config (P3).
func CatalogueText(c catalogue.Catalogue, class string) string {
	var b strings.Builder
	if class == "" {
		b.WriteString("Classes of " + inertWord(c.Repository, clean(c.Repository)) + ", with the profile each runs and how it merges.\n")
		for _, cl := range c.Classes {
			b.WriteString(classLine(c, cl) + "\n")
		}
		b.WriteString("Evaluation " + inertWord(c.Evaluation.Mode, clean(c.Evaluation.Mode)) + ", control share " + strconv.Itoa(c.Evaluation.ControlPercent) + " %.\n")
		b.WriteString("Ask for one class to see what each of its profiles runs.\n")
		b.WriteString(pinLine(c, nil))
		return b.String()
	}
	cl, ok := c.Class(class)
	if !ok {
		return "No class " + quote(class) + " is offered on " + inertWord(c.Repository, clean(c.Repository)) + ".\n"
	}
	b.WriteString(classLine(c, cl) + "\n")
	b.WriteString("Ceiling " + dollars(int64(math.Round(cl.CeilingUSD*1e6))) + " a task.\n")
	if d := strip(cl.Description); d != "" {
		b.WriteString("About " + quote(cl.Description) + "\n")
	}
	if cl.Floor == "" {
		b.WriteString("Floor: none declared, the factory runs the class as configured.\n")
	} else {
		b.WriteString("Floor " + profileLine(c, cl.Floor) + "\n")
	}
	if len(cl.Allowed) > 0 {
		b.WriteString("Variants:\n")
		for _, name := range cl.Allowed {
			b.WriteString("- " + profileLine(c, name) + "\n")
		}
	}
	if e := cl.Escalation; e != nil && (e.OutOfBudget != "" || e.NotConverged != "") {
		var parts []string
		if e.OutOfBudget != "" {
			parts = append(parts, "out of budget goes to "+name(e.OutOfBudget))
		}
		if e.NotConverged != "" {
			parts = append(parts, "not converged goes to "+name(e.NotConverged))
		}
		b.WriteString("Escalation: " + strings.Join(parts, "; ") + ".\n")
	}
	b.WriteString(pinLine(c, &cl))
	return b.String()
}

// mergeWords is how a task of the class merges, in the broker's words.
func mergeWords(outcome string) string {
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

// classLine is `docs-links · live · merges on its own when the gate's conditions hold · profile links-solo`.
func classLine(c catalogue.Catalogue, cl catalogue.Class) string {
	line := name(cl.Name) + " · " + name(cl.Mode) + " · " + mergeWords(catalogue.Outcome(cl.Mode, c.ApproveToMerge))
	if p, ok := c.Profiles[cl.Floor]; ok && p.Offered {
		line += " · profile " + name(cl.Floor)
	} else {
		line += " · profile chosen at triage"
	}
	var more []string
	for _, v := range c.OfferedProfiles(cl.Name) {
		if v != cl.Floor {
			more = append(more, name(v))
		}
	}
	if len(more) > 0 {
		line += " · variants " + strings.Join(more, ", ")
	}
	return line
}

// profileLine is one profile: its roles, tier, tests-first, loop and description.
func profileLine(c catalogue.Catalogue, n string) string {
	p, ok := c.Profiles[n]
	if !ok {
		return name(n) + ": not in the factory's list of profiles"
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
	if strip(p.Description) != "" {
		parts = append(parts, quote(p.Description))
	}
	return name(n) + ": " + strings.Join(parts, " · ")
}

// pinLine says whether the caller may pin a profile, and which. Only a maintainer may (P5), and
// only on a class with an offered variant or an offered floor that is not fixed by a live or
// shadow merge policy (one profile by TE2).
func pinLine(c catalogue.Catalogue, only *catalogue.Class) string {
	if !c.Maintainer {
		return "Only a maintainer of this repository can pin a profile; the factory chooses it.\n"
	}
	if only == nil {
		return "You maintain this repository: you may pin a profile where a class lists variants.\n"
	}
	if only.Mode == catalogue.ModeLive || only.Mode == catalogue.ModeShadow {
		return "Fixed: this class's merge policy runs it under " + name(only.Floor) + " only, so there is no profile to pin.\n"
	}
	offered := c.OfferedProfiles(only.Name)
	if len(offered) == 0 {
		return "You may pin no profile here: none of this class's profiles is offered.\n"
	}
	return "You may pin " + strings.Join(mapStrings(offered, name), ", ") + ".\n"
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
