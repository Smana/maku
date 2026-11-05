// SPDX-License-Identifier: Apache-2.0

package maku

import "strings"

// OneLine is SafeText for a value that must stay on its line: every agent-written
// string passes through it, the broker's own links included.
func OneLine(s string) string {
	return SafeText(strings.Join(strings.Fields(s), " "))
}

// briefSections are the lines of a brief that say where a task stands, in the order the brief prints them
// (CD11): the room and its issue, what was asked, the plan, what changed since the last visit, what blocks
// it, and the one next step. Acceptance, Fix and Links are left to the full brief: a head that quoted them
// would be the brief again, and `maku status` prints the brief's head above the room's card, not instead of
// it.
var briefSections = []string{"**Resume", "Asked:", "Plan:", "Changed since ", "Blocked on:", "Next:"}

// briefHeadLines is how many section lines a head keeps: the six above, so a brief that somehow repeated a
// section cannot push the card down the page.
const briefHeadLines = 6

// BriefHead is the head of a resume brief's text (CD11): the first six lines that start a section, blank
// lines between them dropped, and nothing else — the changed block's list items, the acceptance checks and
// the links stay in the brief a client reads in full. The text is the broker's own, already made inert by
// the one renderer, so a line here is safe to print as it is; a text with no section line is no head at all.
func BriefHead(text string) string {
	var head []string
	for _, line := range strings.Split(text, "\n") {
		if len(head) == briefHeadLines {
			break
		}
		for _, prefix := range briefSections {
			if strings.HasPrefix(line, prefix) {
				head = append(head, line)
				break
			}
		}
	}
	return strings.Join(head, "\n")
}
