// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"regexp"
	"strings"
)

// The last scan shares nothing with the rewrite on purpose, neither goldmark nor liveForm: a
// misparse the rewrite believes must not also fool what checks it.
var (
	// scanForm is every reference GitHub reads in decoded text, its boundaries as POST /markdown
	// showed them.
	scanForm = regexp.MustCompile(`(?:^|[^A-Za-z0-9])@[A-Za-z0-9]` +
		`|#[0-9]` + // #n after anything but a word is live, and after a word it is owner#n
		`|(?:^|[^A-Za-z0-9])(?i:gh)-[0-9]` +
		`|[A-Za-z0-9-]@[0-9a-f]{7}` +
		`|(?:^|[^A-Za-z0-9])[0-9a-f]{7,40}(?:$|[^A-Za-z0-9])` +
		`|(?:^|[^A-Za-z0-9])/(?:issues|pull|discussions)/[0-9]` +
		`|[A-Za-z0-9_.-]/[A-Za-z0-9_.-]+/(?:issues|pull|pulls|discussions)/[0-9]` +
		`|(?i:https?://|ftp://|www\.|github\.com/)` +
		`|[A-Za-z0-9._%+-]@[A-Za-z0-9_.-]*\.[A-Za-z0-9_-]` +
		`|(?i:ghsa-[0-9a-z]{4}-|cve-[0-9]{4}-[0-9])`)
	scanEntity = regexp.MustCompile(`&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);`)
	scanAngle  = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]*:`)
	// scanNested is a fence's opening or closing line behind any indent or container marker.
	scanNested = regexp.MustCompile("^[ \\t>*+\\-0-9.)]+(?:```+[^`]*|~~~+.*)$")
	// scanDelimiter is what a GFM table needs: without one, a pipe splits nothing.
	scanDelimiter = regexp.MustCompile(`(?m)^[ \t>*+\-0-9.)]*\|?[ \t:|-]*-[ \t:|-]*$`)
)

// scanLive is whether s holds a reference, a link or a live entity outside what every reasonable
// parser reads as inert: a backtick span on one line and a fence that opens and closes at column 0.
// Everything else counts as live, indented code, nested fences and table cells included. It also
// answers true for a column-0 fence left open, which would swallow what a caller appends.
func scanLive(s string) bool {
	table := false
	for _, m := range scanDelimiter.FindAllString(s, -1) {
		table = table || strings.Contains(m, "|")
	}
	var open fence
	trust, carry := true, false // trust: no nested fence yet, which could pair with a column-0 one
	for line := range strings.SplitSeq(s, "\n") {
		if open.char != 0 {
			if open.closedBy(line) {
				open = fence{}
			}
			continue
		}
		if f, ok := openingFence(line); ok && trust {
			open, carry = f, false
			continue
		}
		trust = trust && !scanNested.MatchString(line)
		if strings.TrimSpace(line) == "" {
			carry = false // a paragraph's end ends a backtick left open
			continue
		}
		if liveLine(blankSpans(line, table, &carry)) {
			return true
		}
	}
	return open.char != 0
}

// fence is a column-0 fence's opening: its character and length.
type fence struct {
	char byte
	n    int
}

// openingFence reads line as a fence opening at column 0. A backtick fence's info string holds no
// backtick: otherwise the line is an inline span.
func openingFence(line string) (fence, bool) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return fence{}, false
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	if n < 3 || (line[0] == '`' && strings.Contains(line[n:], "`")) {
		return fence{}, false
	}
	return fence{char: line[0], n: n}, true
}

// closedBy is whether line closes f at column 0.
func (f fence) closedBy(line string) bool {
	n := 0
	for n < len(line) && line[n] == f.char {
		n++
	}
	return n >= f.n && strings.TrimSpace(line[n:]) == ""
}

// blankSpans replaces each span of line that shields what it holds with a space. A span does not
// shield across a table's pipe, nor as $`math`$, and none is trusted while a backtick from an
// earlier line of the paragraph is left open: it would pair with this line's first.
func blankSpans(line string, table bool, carry *bool) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		switch line[i] {
		case '\\':
			b.WriteByte(line[i])
			i++
			if i < len(line) {
				b.WriteByte(line[i])
				i++
			}
		case '`':
			n := 0
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			end := closingRun(line, i+n, n)
			switch {
			case *carry:
				b.WriteString(line[i : i+n])
				i += n
			case end < 0:
				*carry = true
				b.WriteString(line[i : i+n])
				i += n
			case table && hasBarePipe(line[i:end]), i > 0 && line[i-1] == '$' && end < len(line) && line[end] == '$':
				b.WriteString(line[i:end])
				i = end
			default:
				b.WriteByte(' ')
				i = end
			}
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String()
}

// closingRun is the index just past the first run of exactly n backticks at or after from, or -1.
func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return j
		}
		i = j
	}
	return -1
}

func hasBarePipe(s string) bool {
	for i := range len(s) {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			return true
		}
	}
	return false
}

// liveLine is whether what is left of a line holds a live form. A link needs its "](" and an angle
// autolink its "<" unescaped, so those read the source; the rest reads the text as GitHub decodes it.
func liveLine(line string) bool {
	if strings.Contains(line, "](") || scanAngle.MatchString(line) {
		return true
	}
	view, _ := decoded(line)
	if scanForm.MatchString(view) {
		return true
	}
	for _, e := range scanEntity.FindAllString(view, -1) {
		if !keptEntity(e) {
			return true
		}
	}
	return false
}

// expandTabs turns the tabs of each line's indent and container markers into spaces, at GitHub's
// four-column stops: goldmark and cmark-gfm read a tab after a list marker differently, and a space
// they read alike. A fence's content keeps its tabs.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	var open fence
	for line := range strings.Lines(s) {
		bare := strings.TrimSuffix(line, "\n")
		if open.char != 0 {
			if open.closedBy(bare) {
				open = fence{}
			}
			b.WriteString(line)
			continue
		}
		if f, ok := openingFence(bare); ok {
			open = f
			b.WriteString(line)
			continue
		}
		i, col := 0, 0
		for ; i < len(line) && strings.IndexByte(" \t>*+-0123456789.)", line[i]) >= 0; i++ {
			if line[i] == '\t' {
				spaces := 4 - col%4
				b.WriteString(strings.Repeat(" ", spaces))
				col += spaces
				continue
			}
			b.WriteByte(line[i])
			col++
		}
		b.WriteString(line[i:])
	}
	return b.String()
}
