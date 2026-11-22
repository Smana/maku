// SPDX-License-Identifier: Apache-2.0

// Package mdlinks reads Markdown link targets the way the merge gate's links-only shape does
// (R52): a link target is an inline one's or a reference definition's, outside a code fence. The
// gate, the dead-links probe and the oracle read targets through it alone, so the three agree.
package mdlinks

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// reLinkTarget matches one link's target: a reference definition's ([label]: target at a line
// start) or an inline one's (](target), angle-bracketed or bare). Titles, labels and every other
// byte stay in the compared text, so a change beside a link is visible. Deliberately not a full
// markdown parser: a diff that hides a change inside what this reads as a link target is refused
// by the target rule below, and the classes it gates stay shadow until the wave re-reads this
// against a month of forecasts (task 7.9, R52).
var reLinkTarget = regexp.MustCompile(`(?m)^ {0,3}\[[^()\]\r\n]+\]:[ \t]*(<[^<>\r\n]*>|\S+)|\]\((<[^<>\r\n]*>|[^\s()<]*)`)

// linkPlaceholder blanks a target so the rest of the text compares byte for byte. NUL: not in
// any file a class like this edits, and counted below if one ever appears.
const linkPlaceholder = "\x00"

// Targets is the text with every link target outside a code fence replaced by the placeholder,
// and those targets in order of appearance. Fenced lines are left unsubstituted on purpose:
// fenced content is code, and a docs-links diff that moved a link inside a fence moved code,
// which the byte compare below then sees.
func Targets(s string) (string, []string) {
	var out []string
	m := reLinkTarget.FindAllStringSubmatchIndex(s, -1)
	if m == nil {
		return s, nil
	}
	code := codeRanges(s)
	var b strings.Builder
	last := 0
	for _, ix := range m {
		if overlapsRange(code, ix[0], ix[1]) {
			continue // code text still gets written: last never jumps over it
		}
		start, end := ix[4], ix[5] // the inline alternative
		if ix[2] >= 0 {
			start, end = ix[2], ix[3] // the reference-definition alternative
		}
		b.WriteString(s[last:start])
		b.WriteString(linkPlaceholder)
		t := s[start:end]
		if len(t) >= 2 && strings.HasPrefix(t, "<") {
			t = t[1 : len(t)-1]
		}
		out = append(out, t)
		last = end
	}
	b.WriteString(s[last:])
	return b.String(), out
}

// fenceMarker parses a CommonMark fenced code block delimiter on one line (without trailing \n):
// optional leading spaces/tabs (preserving list-nested fences), a run of n >= 3 identical '`' or
// '~' characters, and the remainder of the line (info string, without trailing \r).
func fenceMarker(line string) (ch byte, n int, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	ch = trimmed[0]
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	return ch, n, strings.TrimRight(trimmed[n:], "\r"), true
}

// fencesOf are the byte ranges of CommonMark fenced code blocks (backtick or tilde, length >= 3,
// closing fence matching character with length >= opener and no info string), fence lines included.
// An unbalanced fence runs to the end of the text: its links go uncounted, which fails the byte
// compare for any change there — the safe direction.
func fencesOf(s string) [][2]int {
	var out [][2]int
	in, start := false, 0
	var openCh byte
	openN := 0
	for off := 0; off < len(s); {
		end := strings.IndexByte(s[off:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += off
		}
		if ch, n, rest, ok := fenceMarker(s[off:end]); ok {
			if in {
				if ch == openCh && n >= openN && strings.Trim(rest, " \t") == "" {
					out, in = append(out, [2]int{start, end}), false
				}
			} else if ch == '~' || !strings.ContainsRune(rest, '`') {
				start, openCh, openN, in = off, ch, n, true
			}
		}
		if end == len(s) {
			break
		}
		off = end + 1
	}
	if in {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// codeRanges returns all byte ranges in s that represent code: fenced code blocks, 4-space/tab
// indented code lines outside fences, and inline backtick code spans outside both.
func codeRanges(s string) [][2]int {
	fences := fencesOf(s)
	blocked := append(slices.Clone(fences), indentedCodeRanges(s, fences)...)
	return append(blocked, inlineCodeSpans(s, blocked)...)
}

// indentedCodeRanges returns line ranges outside fences whose leading indentation reaches at least
// 4 columns (spaces or tab stops of 4).
func indentedCodeRanges(s string, fences [][2]int) [][2]int {
	var out [][2]int
	for off := 0; off < len(s); {
		end := strings.IndexByte(s[off:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += off
		}
		if !overlapsRange(fences, off, end) && indentCols(s[off:end]) >= 4 {
			out = append(out, [2]int{off, end})
		}
		if end == len(s) {
			break
		}
		off = end + 1
	}
	return out
}

// indentCols returns the column width of leading spaces and tabs (tab stop = 4) before the first
// non-whitespace byte, or 0 for blank lines.
func indentCols(line string) int {
	col := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		case '\r':
			return 0
		default:
			return col
		}
	}
	return 0
}

// inlineCodeSpans returns CommonMark inline backtick code spans outside blocked ranges.
func inlineCodeSpans(s string, blocked [][2]int) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); {
		if r, ok := rangeAt(blocked, i); ok {
			i = r[1]
			continue
		}
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		if s[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		closed := false
		for j := i + n; j < len(s); {
			if _, ok := rangeAt(blocked, j); ok {
				break
			}
			if s[j] == '\n' && isBlankAfter(s, j+1) {
				break
			}
			if s[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(s) && s[j+m] == '`' {
				m++
			}
			if m == n {
				out = append(out, [2]int{i, j + m})
				i = j + m
				closed = true
				break
			}
			j += m
		}
		if !closed {
			i += n
		}
	}
	return out
}

// isBlankAfter reports whether s starting at off begins with a blank line (only spaces/tabs/\r
// before the next \n or EOF).
func isBlankAfter(s string, off int) bool {
	for i := off; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return true
}

func rangeAt(ranges [][2]int, off int) ([2]int, bool) {
	for _, r := range ranges {
		if r[0] <= off && off < r[1] {
			return r, true
		}
	}
	return [2]int{}, false
}

func overlapsRange(ranges [][2]int, start, end int) bool {
	return slices.ContainsFunc(ranges, func(r [2]int) bool { return r[0] < end && start < r[1] })
}

func hasSpaceOrControl(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// targetKind is a link target's scheme and host, with ("", "") only for a target relative to
// the current document. A schemeless one with a host is protocol-relative — host-absolute, and
// its non-empty host keeps it out of the relative clause. Backslashes, leading double slashes,
// whitespace, control characters, opaque URLs and userinfo are rejected with ("!", "") so no
// browser URL-normalisation quirk (such as /\evil.com or ///evil.com) can smuggle a host jump.
func targetKind(t string) (scheme, host string) {
	if strings.ContainsRune(t, '\\') || strings.HasPrefix(t, "//") || hasSpaceOrControl(t) {
		return "!", ""
	}
	u, err := url.Parse(t)
	if err != nil || u.Opaque != "" || u.User != nil {
		return "!", ""
	}
	if u.Scheme == "" && u.Host == "" {
		return "", "" // a path, a query, a fragment: relative
	}
	if u.Host == "" {
		return "!", ""
	}
	return strings.ToLower(u.Scheme), strings.ToLower(u.Host)
}

// retargetOK is R52's rule for a changed pair: relative to relative — hosts empty on both sides,
// so a protocol-relative //host is never "relative" — or https staying on its host, or landing
// on the archive. Never another scheme, never a host jump.
func retargetOK(from, to string) bool {
	fs, fh := targetKind(from)
	ts, th := targetKind(to)
	switch {
	case fs == "" && fh == "" && ts == "" && th == "":
		return true
	case fs == "https" && ts == "https" && fh != "" && (fh == th || th == "web.archive.org"):
		return true
	default:
		return false
	}
}

// LinksOnly is R52's promise of the docs-links and revert classes: between two commits of the
// tree, every changed file changed only its link targets, the counts are equal, and every
// retarget moves relatively, keeps its host, or lands on web.archive.org. The bool decides;
// the string is the first violation, named for the shadow forecast's record (task 7.9).
func LinksOnly(base, head map[string]string) (bool, string) {
	paths := make([]string, 0, len(base)+len(head))
	for p := range base {
		paths = append(paths, p)
	}
	for p := range head {
		if _, ok := base[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		b, h := base[p], head[p]
		if b == h {
			continue
		}
		nb, tb := Targets(b)
		nh, th := Targets(h)
		if nb != nh {
			return false, p + ": changed beyond link targets"
		}
		if len(tb) != len(th) { // redundant with nb == nh unless the text holds a real NUL
			return false, p + ": added or removed a link"
		}
		for i := range tb {
			if tb[i] != th[i] && !retargetOK(tb[i], th[i]) {
				return false, p + ": retargeted " + tb[i] + " to " + th[i]
			}
		}
	}
	return true, ""
}
