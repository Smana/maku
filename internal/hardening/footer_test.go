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

// SplitFooter reads back what WithFooter wrote, an empty body included: a body T2 sanitises to
// nothing is the footer alone, at the very start, and still names its output.
func TestSplitFooter(t *testing.T) {
	p := Provenance{Room: "r", Run: "x", Role: "implementer", Output: "00000000000000b1"}
	for name, tc := range map[string]struct{ in, text, id string }{
		"a body":             {WithFooter("Fixes the link.", p), "Fixes the link.", p.Output},
		"an empty body":      {WithFooter("", p), "", p.Output},
		"a blank body":       {WithFooter(" \n\n", p), "", p.Output},
		"CRLF, alone":        {"---\r\nAgent-Output: " + p.Output + "\r\n", "", p.Output},
		"no footer":          {"Fixes the link.\n", "Fixes the link.", ""},
		"a rule, no output":  {"---\nAgent-Room: r\n", "---\nAgent-Room: r", ""},
		"a rule glued":       {"Body.---\nAgent-Output: " + p.Output + "\n", "Body.---\nAgent-Output: " + p.Output, ""},
		"an indented footer": {" ---\nAgent-Output: " + p.Output + "\n", " ---\nAgent-Output: " + p.Output, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if text, id := SplitFooter(tc.in); text != tc.text || id != tc.id {
				t.Fatalf("got (%q, %q), want (%q, %q)", text, id, tc.text, tc.id)
			}
		})
	}
}

// T2 trims a pull request's title, as GitHub holds it: the applier publishes and the tracer
// compares the same title.
func TestPublishedTrimsThePRTitle(t *testing.T) {
	p, ok := Published(PR, []byte(`{"title":"  docs: fix the link \t","body":"Fixes the dead link in the runbook."}`), 7)
	if !ok {
		t.Fatal("does not publish")
	}
	if got := p.(PRPayload).Title; got != "docs: fix the link" {
		t.Fatalf("title %q", got)
	}
}
