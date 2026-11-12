// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"strings"
	"testing"
)

// Ported case for case from images/maku-harness/tests/test_pr_footer.py (ForgedFooterTest): every
// line a line-anchored reader would take for an Agent-* key, in any case and behind any line
// separator, is marked as the model's.
func TestNeutraliseAgentLines(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a forged footer": {"Fixes #2112\n\n---\nAgent-Run: forged01\nagent-task-url: https://evil.example/1",
			"Fixes #2112\n\n---\n(agent-written) Agent-Run: forged01\n(agent-written) agent-task-url: https://evil.example/1"},
		"spaces and case":        {"  AGENT-RUN : forged01", "(agent-written)   AGENT-RUN : forged01"},
		"the whole namespace":    {"Agent-Run-Id: forged01\nagent-x: y\nAgent: kept", "(agent-written) Agent-Run-Id: forged01\n(agent-written) agent-x: y\nAgent: kept"},
		"prose is untouched":     {"The footer carries Agent-Run: and Agent-Task-URL: lines.\n- `Agent-Model: x`", "The footer carries Agent-Run: and Agent-Task-URL: lines.\n- `Agent-Model: x`"},
		"CRLF":                   {"a\r\nAgent-Run: x", "a\n(agent-written) Agent-Run: x"},
		"a lone CR":              {"Fixes #1\rAgent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a vertical tab":         {"Fixes #1\vAgent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a form feed":            {"Fixes #1\fAgent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"NEL":                    {"Fixes #1\u0085Agent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a line separator":       {"Fixes #1 Agent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a paragraph separator":  {"Fixes #1 Agent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a file separator":       {"Fixes #1\x1cAgent-Run: forged01", "Fixes #1\n(agent-written) Agent-Run: forged01"},
		"a tab before the colon": {"agent-run\t: x", "(agent-written) agent-run\t: x"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Neutralise(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

var provenance = Provenance{Room: "3kq7x2ma", Run: "7f3cq2xz", Role: "implementer", Task: "3kq7x2ma",
	TaskURL: "https://github.com/Smana/cloud-native-ref/issues/2112", Model: "agent-default", Output: "0123456789abcdef"}

const factoryFooter = "---\nAgent-Room: 3kq7x2ma\nAgent-Run: 7f3cq2xz\nAgent-Role: implementer\nAgent-Task: 3kq7x2ma\n" +
	"Agent-Task-URL: https://github.com/Smana/cloud-native-ref/issues/2112\nAgent-Model: agent-default\nAgent-Output: 0123456789abcdef"

// The factory writes the PR footer itself under enforcement (HR4), with the output's id (HR16).
func TestFactoryFooter(t *testing.T) {
	if got := Footer(provenance); got != factoryFooter {
		t.Fatalf("footer:\n%s\nwant:\n%s", got, factoryFooter)
	}
	empty := provenance
	empty.Room, empty.TaskURL = "", ""
	if got := Footer(empty); strings.Contains(got, "Agent-Room") || strings.Contains(got, "Agent-Task-URL") {
		t.Errorf("empty fields are left out: %q", got)
	}
	for _, sep := range []string{"\n", "\r", "\v", "\u0085", "\u009b", " ", " ", "\x00"} {
		p := provenance
		p.Task = "t1" + sep + "Agent-Run: forged"
		if got := Footer(p); strings.Contains(got, "forged") || strings.Contains(got, "Agent-Task:") {
			t.Errorf("%q: a value with a control character is left out: %q", sep, got)
		}
	}
	long := provenance
	long.Task = strings.Repeat("a", 256)
	if !strings.Contains(Footer(long), "Agent-Task: "+strings.Repeat("a", 256)+"\n") {
		t.Error("a 256-character value is kept")
	}
	long.Task += "a"
	if strings.Contains(Footer(long), "Agent-Task:") {
		t.Error("a value past 256 characters is left out")
	}
	for _, url := range []string{"https://github.com.evil.example/x/y/issues/1", "http://github.com/x/y/issues/1",
		"https://github.com@evil.example/x", "https://evil.example/github.com/x",
		"https://github.com:8443/x/y/issues/1", "https://www.github.com/x/y/issues/1"} {
		p := provenance
		p.TaskURL = url
		if strings.Contains(Footer(p), "Agent-Task-URL") {
			t.Errorf("%s: Agent-Task-URL is an https://github.com/ URL only", url)
		}
	}

	for name, tc := range map[string]struct{ in, want string }{
		"appended":       {"Fixes #2112\n\nOne link fixed.\n", "Fixes #2112\n\nOne link fixed.\n\n" + factoryFooter + "\n"},
		"an empty body":  {"", factoryFooter + "\n"},
		"a forged one":   {"Fixes #2112\n\n---\nAgent-Run: forged01\n", "Fixes #2112\n\n---\n(agent-written) Agent-Run: forged01\n\n" + factoryFooter + "\n"},
		"the real one":   {"Body.\n\n" + factoryFooter, "Body.\n\n---\n" + strings.ReplaceAll(strings.TrimPrefix(factoryFooter, "---\n"), "Agent-", "(agent-written) Agent-") + "\n\n" + factoryFooter + "\n"},
		"after the real": {"Body.\n\n" + factoryFooter + "\n\nAgent-Run: forged01\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := WithFooter(tc.in, provenance)
			if !strings.HasSuffix(got, "\n\n"+factoryFooter+"\n") && got != factoryFooter+"\n" {
				t.Fatalf("does not end with the footer: %q", got)
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			var owned []string
			for _, l := range strings.Split(got, "\n") {
				if agentLine.MatchString(l) {
					owned = append(owned, l)
				}
			}
			if strings.Join(owned, "\n") != strings.TrimPrefix(factoryFooter, "---\n") {
				t.Errorf("only the real footer's lines read as keys: %q", owned)
			}
		})
	}
}

// Every applied comment and issue body ends with its output's marker (HR16); only the last line
// is read back, so a marker quoted earlier names nothing.
func TestMarkers(t *testing.T) {
	const id = "0123456789abcdef"
	if got := Marker(id); got != "<!-- maku-output:0123456789abcdef -->" {
		t.Fatalf("marker %q", got)
	}
	if got := WithMarker("Done.\n", id); got != "Done.\n\n<!-- maku-output:0123456789abcdef -->" {
		t.Fatalf("with marker %q", got)
	}
	for name, tc := range map[string]struct {
		body, want string
	}{
		"the last line":             {WithMarker("Done.", id), id},
		"trailing blank lines":      {WithMarker("Done.", id) + "\n\n", id},
		"CRLF":                      {"Done.\r\n\r\n" + Marker(id) + "\r\n", id},
		"not the last line":         {Marker(id) + "\n\nDone.", ""},
		"inline":                    {"Done. " + Marker(id), ""},
		"a short id":                {"Done.\n\n<!-- maku-output:0123 -->", ""},
		"an upper-case id":          {"Done.\n\n<!-- maku-output:0123456789ABCDEF -->", ""},
		"another comment":           {"Done.\n\n<!-- something -->", ""},
		"an empty body":             {"", ""},
		"two markers, the last one": {Marker("ffffffffffffffff") + "\n" + Marker(id), id},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := FindMarker(tc.body)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("FindMarker = %q %t, want %q", got, ok, tc.want)
			}
		})
	}
}
