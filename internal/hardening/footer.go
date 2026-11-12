// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"slices"
	"strings"
)

// harnessKeys are the footer keys pr_footer.py writes (images/maku-harness/pr_footer.py, FIELDS).
var harnessKeys = []string{"Agent-Room", "Agent-Run", "Agent-Role", "Agent-Task", "Agent-Task-URL", "Agent-Model"}

// StripHarnessFooter removes the harness's provenance footer from a PR body, so the shadow pass
// measures the body as the model wrote it (HR4). The footer is the body's last paragraph: a "---"
// line, then only harness `Key: value` lines. A body without one, or whose last paragraph holds
// any other line, comes back unchanged; an Agent-* line elsewhere stays, for T2 to measure.
func StripHarnessFooter(body string) string {
	text := strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	var rule int
	switch i := strings.LastIndex(text, "\n---\n"); {
	case i >= 0:
		rule = i + 1
	case strings.HasPrefix(text, "---\n"):
		rule = 0
	default:
		return body
	}
	tail := strings.Split(text[rule+len("---\n"):], "\n")
	for _, l := range tail {
		key, _, ok := strings.Cut(l, ": ")
		if !ok || !slices.Contains(harnessKeys, key) {
			return body
		}
	}
	if rule > 0 && !strings.HasSuffix(text[:rule], "\n\n") {
		return body // the rule ends a paragraph the footer would have started
	}
	return strings.TrimRight(text[:rule], " \t\n")
}
