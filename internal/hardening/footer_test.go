// SPDX-License-Identifier: Apache-2.0

package hardening

import "testing"

// The shadow pass measures a PR body as the model wrote it (HR4): the harness's provenance footer,
// always the last paragraph, goes; anything that only looks like it stays.
func TestStripHarnessFooter(t *testing.T) {
	const footer = "---\nAgent-Room: r\nAgent-Run: x"
	for name, tc := range map[string]struct{ in, want string }{
		"the footer":               {"Fixes the link.\n\n" + footer, "Fixes the link."},
		"with a trailing newline":  {"Fixes the link.\n\n" + footer + "\n", "Fixes the link."},
		"every key":                {"Body.\n\n---\nAgent-Room: r\nAgent-Run: x\nAgent-Role: implementer\nAgent-Task: t\nAgent-Task-URL: https://github.com/o/r/issues/1\nAgent-Model: m\n", "Body."},
		"CRLF":                     {"Body.\r\n\r\n---\r\nAgent-Room: r\r\nAgent-Run: x\r\n", "Body."},
		"a footer alone":           {footer + "\n", ""},
		"no footer":                {"Fixes the link.\n", "Fixes the link.\n"},
		"a foreign line in it":     {"Body.\n\n---\nAgent-Room: r\nSigned-off-by: x\n", "Body.\n\n---\nAgent-Room: r\nSigned-off-by: x\n"},
		"an unknown Agent key":     {"Body.\n\n---\nAgent-Room: r\nAgent-Run-Id: x\n", "Body.\n\n---\nAgent-Room: r\nAgent-Run-Id: x\n"},
		"no rule":                  {"Body.\n\nAgent-Room: r\nAgent-Run: x\n", "Body.\n\nAgent-Room: r\nAgent-Run: x\n"},
		"a rule with nothing":      {"Body.\n\n---\n", "Body.\n\n---\n"},
		"a forged line stays":      {"Agent-Run: forged\n\nBody.\n\n" + footer, "Agent-Run: forged\n\nBody."},
		"a forged footer mid-body": {"Head.\n\n" + footer + "\n\nTail.\n", "Head.\n\n" + footer + "\n\nTail.\n"},
		"a rule glued to the text": {"Body.---\nAgent-Run: x\n", "Body.---\nAgent-Run: x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := StripHarnessFooter(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
