// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"fmt"
	"strings"
	"unicode"
)

// ReadyGuide is the how-to a developer follows to declare a repository's agent checks.
const ReadyGuide = "https://github.com/Smana/maku/blob/main/docs/make-a-repository-ready.md"

// SkippedCheck is one ciOnly entry of a repository's .github/agent-checks.yaml: a part of a CI check
// the sandbox cannot run, and why. Every field is the repository's text.
type SkippedCheck struct{ Check, Part, Why string }

// Limits is what the sandbox does with a task's first implementer run: whether the run checks its
// work, whether the default branch declares checks, and what the declaration leaves to CI.
type Limits struct {
	Checks, Declared bool
	CIOnly           []SkippedCheck
}

const (
	maxSkipped     = 10
	maxSkippedRune = 120
)

// Silent is whether there is nothing to say: the run checks its work and the declaration leaves
// nothing to CI.
func (l Limits) Silent() bool { return l.Checks && l.Declared && len(l.CIOnly) == 0 }

// SandboxNote says what the sandbox did not run, and that CI is the judge of a merge (developer
// review gap 3): a paragraph the first implementer run's Started carries unless l.Silent().
func SandboxNote(l Limits) string {
	const judge = "CI is the judge of a merge: read its result on the pull request."
	var b strings.Builder
	switch {
	case !l.Checks:
		b.WriteString("This run does not run the repository's agent checks, so nothing was built or tested in the sandbox. " + judge)
	case !l.Declared:
		b.WriteString("This repository declares no agent checks (`.github/agent-checks.yaml`), so nothing was built or tested in the sandbox. " +
			judge + " To have the agent test its work first: " + ReadyGuide)
	default:
		b.WriteString("The sandbox does not run these parts of the checks. CI still runs them, and " + judge)
		for i, c := range l.CIOnly {
			if i == maxSkipped {
				fmt.Fprintf(&b, "\n\nand %d more in `.github/agent-checks.yaml`.", len(l.CIOnly)-maxSkipped)
				break
			}
			fmt.Fprintf(&b, "\n- `%s`: %s (%s)", quoted(c.Check), quoted(c.Part), quoted(c.Why))
		}
	}
	return b.String()
}

// quoted is the repository's text as a comment may carry it: one line, no backtick to end a code
// span, no mention, cut to maxSkippedRune characters.
func quoted(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "@", "@\u200b")
	if r := []rune(s); len(r) > maxSkippedRune {
		s = string(r[:maxSkippedRune-1]) + "…"
	}
	return s
}

// ImagesRemoved tells the author of issue n that images in it were removed, and that no run will see
// them (developer review gap 5): the factory strips images from every request it sanitises.
func ImagesRemoved(n, images int) Event {
	what := fmt.Sprintf("%d images in this issue were", images)
	if images == 1 {
		what = "1 image in this issue was"
	}
	return Event{Key: "images-removed", Class: ClassChanged, Body: fmt.Sprintf(
		"%s removed: the agent cannot see them. Describe what they show in words in the issue (#%d).", what, n)}
}
