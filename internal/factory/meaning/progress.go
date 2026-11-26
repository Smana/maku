// SPDX-License-Identifier: Apache-2.0

package meaning

import (
	"fmt"
	"strings"
)

// Counts are a policy's evidence as its words read them: agreed (the wire `clean`) of the
// minimum, the shadow decisions a maintainer changed, the wrong calls (`bad`), the live merges and
// the quiet ones among them, and the repository's rollback drill.
type Counts struct {
	Agreed, Minimum, Changed, WrongCalls int
	Merges, Quiet                        int
	Drill                                string
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// DrillWords is the repository's rollback drill in words.
func DrillWords(drill string) string {
	switch drill {
	case "passed":
		return "done"
	case "", "none":
		return "not done"
	}
	return strings.ReplaceAll(drill, "_", " ")
}

// Progress is a policy's progress line: in shadow, the agreed decisions toward the minimum with
// their judge; once live, the merges with what checked them. A number is always followed by its
// judge, never "clean" or "healthy".
func Progress(mode string, s Sentence, c Counts) string {
	wrong := plural(c.WrongCalls, "wrong call", "wrong calls")
	if mode == "live" {
		line := "Live. " + plural(c.Merges, "merged alone", "merged alone")
		switch {
		case s.Signal != None && s.Signal != "":
			line += ", all signal-checked by `" + string(s.Signal) + "`"
		case s.Prose:
			line += ", unchecked: nothing checks the meaning of prose"
		default:
			line += ", unchecked: nothing checks their meaning"
		}
		return fmt.Sprintf("%s. %d quiet. %s.", line, c.Quiet, wrong)
	}
	return fmt.Sprintf("%d of %d agreed: you merged %d unchanged, changed %d. %s. Rollback drill: %s.",
		c.Agreed, c.Minimum, c.Agreed, c.Changed, wrong, DrillWords(c.Drill))
}

// Blocker is why promotion is refused, in words, and the next step: one per code the evidence
// names. The plan 2 codes (Unchecked, candidates_open) have words only under the rules that emit
// them.
func Blocker(code string, c Counts, r Rules) (says, next string) {
	switch code {
	case "below_minimum":
		n := max(1, c.Minimum-c.Agreed)
		return "Needs " + plural(n, "more decision", "more decisions") + " you agree with.",
			"Merge unchanged the pull requests it would have merged."
	case "bad_outcome":
		return plural(max(1, c.WrongCalls), "wrong call", "wrong calls") + " among its recent merges.",
			"Read each one in the decisions list: a wrong call blocks promotion."
	case "no_drill", "drill_not_passed":
		return "No rollback drill on this repository yet.",
			"Run `maku policy drill owner/name#PR` on a merged pull request, then merge the revert it opens."
	case "Unchecked":
		if r.UncheckedStaysShadow {
			return "Nothing checks what these changes mean, so this policy cannot merge alone.",
				"Add a signal: require a plan and tests, or a shape."
		}
	case "candidates_open":
		if r.WrongLabel {
			return "Later fixes may have corrected a merge of this policy. Answer them first.", "Answer each candidate."
		}
	}
	return strings.ReplaceAll(code, "_", " ") + ".", ""
}
