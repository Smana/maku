// SPDX-License-Identifier: Apache-2.0

package issuebody

import (
	"regexp"
	"strings"
)

// The acceptance the issue states, read as the judge reads it (loop_policy.acceptance, HR3): a heading
// starting with "acceptance", its body running to the next heading or fence line; else a paragraph whose
// first line starts "Acceptance:" (any case, optionally bold: "**Acceptance**:", "**Acceptance:**"),
// running to the next blank line or fence line. The paragraph may start on the label's line or on the
// line after it, so a label followed by a list states the list. The two readers share
// images/maku-harness/tests/acceptance.
var (
	acceptanceHeadingRE = regexp.MustCompile(`(?im)^#{1,6}[ \t]*acceptance\b[^\n]*$`)
	sectionEnd          = regexp.MustCompile(`(?m)^(?:#{1,6}[ \t]|TASK-DATA-)`)
	inlineAcceptance    = regexp.MustCompile(`(?im)^[ \t]*(?:\*\*|__)?acceptance(?:\*\*|__)?[ \t]*:(?:\*\*|__)?[ \t]*(.*)$`)
	paragraphEnd        = regexp.MustCompile(`(?m)\n[ \t]*\n|^TASK-DATA-\S+$`)
)

// Acceptance is the acceptance the issue states: the heading's section when non-empty, else the
// inline paragraph, else "". Pure text matching: nothing in the issue is followed or run.
func Acceptance(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if m := acceptanceHeadingRE.FindStringIndex(text); m != nil {
		rest := text[m[1]:]
		if n := sectionEnd.FindStringIndex(rest); n != nil {
			rest = rest[:n[0]]
		}
		if s := strings.TrimSpace(rest); s != "" {
			return s
		}
	}
	for _, m := range inlineAcceptance.FindAllStringSubmatchIndex(text, -1) {
		rest := text[m[2]:]
		if n := paragraphEnd.FindStringIndex(rest); n != nil {
			rest = rest[:n[0]]
		}
		if s := strings.TrimSpace(rest); s != "" {
			return s
		}
	}
	return ""
}
