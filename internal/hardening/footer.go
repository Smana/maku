// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// harnessKeys are the footer keys pr_footer.py writes (images/maku-harness/pr_footer.py, FIELDS).
var harnessKeys = []string{"Agent-Room", "Agent-Run", "Agent-Role", "Agent-Task", "Agent-Task-URL", "Agent-Model"}

// Provenance is what the factory's footer says about an applied pull request (HR4): the harness's
// fields, and the output that wrote it (HR16).
type Provenance struct {
	Room, Run, Role, Task, TaskURL, Model, Output string
}

const (
	agentMark     = "(agent-written) "
	maxFooterRune = 256
)

var (
	// agentLine is any line a line-anchored reader would take for an Agent-* key: the whole
	// namespace, so a prefix reader never takes Agent-Run-Id: for Agent-Run (pr_footer.py, OWNED).
	agentLine = regexp.MustCompile(`(?im)^[ \t]*agent-[a-z0-9-]*[ \t]*:`)
	// lineBreak is every separator Python's str.splitlines honours, as pr_footer.py's lines() does.
	lineBreak = regexp.MustCompile("\r\n|[\r\v\f\x1c\x1d\x1e\u0085  ]")
	marker    = regexp.MustCompile(`^<!-- maku-output:([0-9a-f]{16}) -->$`)
)

// Neutralise is pr_footer.py's neutralise: every line break as \n, then every Agent-* key line
// prefixed "(agent-written) ", so the model's text stays readable but never reads as provenance.
func Neutralise(text string) string {
	return agentLine.ReplaceAllStringFunc(lineBreak.ReplaceAllString(text, "\n"), func(m string) string { return agentMark + m })
}

// Footer is "---" and one "Key: value" line per non-empty field, in pr_footer.py's order with
// Agent-Output last. A value of more than 256 characters, or holding a control character or a line
// separator, is left out, and so is a TaskURL that is not https://github.com/….
func Footer(p Provenance) string {
	fields := [][2]string{{"Agent-Room", p.Room}, {"Agent-Run", p.Run}, {"Agent-Role", p.Role}, {"Agent-Task", p.Task},
		{"Agent-Task-URL", p.TaskURL}, {"Agent-Model", p.Model}, {"Agent-Output", p.Output}}
	lines := []string{"---"}
	for _, f := range fields {
		if !footerValue(f[1]) || (f[0] == "Agent-Task-URL" && !githubURL(f[1])) {
			continue
		}
		lines = append(lines, f[0]+": "+f[1])
	}
	return strings.Join(lines, "\n")
}

func footerValue(v string) bool {
	if v == "" || !utf8.ValidString(v) || utf8.RuneCountInString(v) > maxFooterRune {
		return false
	}
	for _, r := range v {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func githubURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil
}

// WithFooter is body as the factory publishes it: neutralised, then the footer as its last
// paragraph, always. The factory writes the whole body, so nothing already in it is kept as a
// footer: a copy of the real one is marked like any other forged line.
func WithFooter(body string, p Provenance) string {
	text := strings.TrimRight(Neutralise(body), " \t\n")
	if text == "" {
		return Footer(p) + "\n"
	}
	return text + "\n\n" + Footer(p) + "\n"
}

// Marker is the line an applied comment or issue body ends with (HR16): the applier finds its own
// write by it after a crash, and never writes twice. T2 strips HTML comments from the model's
// text, so only the factory writes one.
func Marker(id string) string { return "<!-- maku-output:" + id + " -->" }

// WithMarker is body with Marker(id) as its last paragraph.
func WithMarker(body, id string) string {
	return strings.TrimRight(body, " \t\r\n") + "\n\n" + Marker(id)
}

// FindMarker reads the output id from body's last non-blank line only: a marker quoted anywhere
// else names nothing.
func FindMarker(body string) (string, bool) {
	text := strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\r\n")
	last := text[strings.LastIndex(text, "\n")+1:]
	m := marker.FindStringSubmatch(strings.TrimSpace(last))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// SplitFooter is a published pull request body split at the factory's footer: the text above it,
// line breaks as \n and trailing blanks trimmed, and the Agent-Output it names. A body whose last
// "---" paragraph names no output comes back whole, with an empty id: WithFooter marks any such
// line the model wrote above the footer, so only the last paragraph is read.
func SplitFooter(body string) (text, output string) {
	text = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	i := strings.LastIndex(text, "\n---\n")
	if i < 0 {
		return text, ""
	}
	for _, l := range strings.Split(text[i+len("\n---\n"):], "\n") {
		if id, ok := strings.CutPrefix(l, "Agent-Output: "); ok {
			return strings.TrimRight(text[:i], " \t\n"), id
		}
	}
	return text, ""
}

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
