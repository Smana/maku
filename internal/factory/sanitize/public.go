// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"bytes"
	"cmp"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
)

var (
	// mdLink is an inline link on one line; its URL stops at the first space, ")" or backtick.
	mdLink = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\s` + "`" + `]*)[^)\n` + "`" + `]*\)`)
	// liveForm is what GitHub turns into something live without markup, in one pass so a match is
	// never matched again. Each form is what POST /markdown linked on 2026-11-04. It runs over
	// text with its backslash escapes decoded, as GitHub reads it. The groups need a check the
	// pattern cannot state; each is named by a constant below. A scheme is in any case; a URL stops
	// at a backslash, which it could not hold in a span. What runs before an "@", "/" or "#" is
	// capped at GitHub's own limits, wider for an email: a form still ends up in a span, and a scan
	// that a rejected match restarts cannot go quadratic over one long word.
	liveForm = regexp.MustCompile("" +
		`(&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);)` + // an entity
		"|(?i:(?:https?|ftp)://|www\\.|github\\.com/)[^\\s<>`\\\\]+" + // a URL, a www. host, a github.com path
		`|[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}(?:#[0-9]+|@[A-Za-z0-9._-]+|/(?:issues|pull|pulls|discussions)/[0-9]+)` + // owner/repo#n, @ref, /issues/n
		`|[A-Za-z0-9._%+-]{1,256}@[A-Za-z0-9_.-]*\.[A-Za-z0-9_-]+` + // an email: GFM takes "_" and a leading "." in the domain
		`|[A-Za-z0-9][A-Za-z0-9-]{0,99}@[0-9a-f]{7,40}` + // owner@sha
		`|[A-Za-z0-9][A-Za-z0-9-]{0,99}#[0-9]+` + // owner#n: GitHub reads it as owner/<this repo>#n; any word can be an account
		`|(@[A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9_.-]+)?|#[0-9]+|(?i:gh)-[0-9]+` + // a mention, #n, GH-n
		`|/(?:issues|pull|discussions)/[0-9]+` + // the path of this repository's issue
		`|(?i:ghsa)(?:-[0-9a-zA-Z]{4}){3}|(?i:cve)-[0-9]{4}-[0-9]{4,})` + // an advisory
		`|([0-9a-f]{7,40})`) // a bare commit, digits alone included
	// fenceLang is a fence's info string that GitHub reads as a language and nothing more.
	fenceLang = regexp.MustCompile(`^[A-Za-z0-9_+#.-]+$`)
	// fenceCloser is the line a fence's content ends at, behind whatever indent or quote it has.
	fenceCloser = regexp.MustCompile("^[ \\t>]*(?:`{3,}|~{3,})[ \\t]*\r?\n?$")
)

// liveForm's groups.
const (
	formEntity  = 1
	formBounded = 2 // live only after a non-alphanumeric character
	formCommit  = 3 // live only as a whole word
)

// Public makes text written by an agent safe to publish as a GitHub issue (RunLore agent-to-agent
// RA9, R8): Text's defusing, then every link, mention and reference inert. An inline link keeps its
// text and shows its URL in a code span; a bare URL, www. host, email, mention, issue or commit
// reference becomes a code span, as does an entity, which GitHub would otherwise decode before it
// looks for references. GFM never links inside a code span, and goldmark reads where those are.
//
// Unlike Text it parses, because what shields text is structure: a backtick pair is no span across
// a blank line, a list item or a table's pipe. A column-0 fence stays as written; any other code
// block moves to one, the one code block every parser agrees on, so its content need not be
// refused below. A list or a quote around it is cut there.
//
// Like Text it fails closed. Output that does not settle, that goldmark still reads a link in, or
// in which scanLive finds a live form outside what no parser could read as live, is replaced by
// Withheld: goldmark is not GitHub's parser, and the scan does not depend on it.
func Public(s string) (string, Report) {
	out, rep := Text(s)
	if rep.Withheld {
		return out, rep
	}
	out, ok := settle(expandTabs(out), inertPass)
	if !ok || scanLive(out) {
		return Withheld, Report{Withheld: true}
	}
	return out, rep
}

// settle repeats pass until its output stops changing, and answers whether that output, read back,
// holds no link: the proof a rewrite made no new one, and that it missed none goldmark sees.
func settle(s string, pass func(string) string) (string, bool) {
	for range maxPasses {
		next := pass(s)
		if next == s {
			return s, !holdsLink(s)
		}
		s = next
	}
	return s, false
}

func holdsLink(s string) bool {
	found := false
	_ = ast.Walk(parseGFM(s), func(n ast.Node, _ bool) (ast.WalkStatus, error) {
		switch n.Kind() {
		case ast.KindLink, ast.KindAutoLink, ast.KindImage, ast.KindRawHTML, ast.KindHTMLBlock:
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

func parseGFM(s string) ast.Node {
	return goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(gmtext.NewReader([]byte(s)))
}

// piece is a stretch of the source a pass rewrites; what lies between pieces is copied as it is:
// block markers, fences, code blocks.
type piece struct {
	start, stop int
	kind        pieceKind
	text        string // pieceBlock: what replaces it
}

type pieceKind int

const (
	pieceLive  pieceKind = iota // inline text outside a code span
	pieceSpan                   // an author's code span
	pieceInfo                   // a column-0 fence's info string
	pieceBlock                  // a code block moved to column 0
)

// inertPass is one pass over s: it splits s by goldmark's reading of it, and rewrites each piece.
func inertPass(s string) string {
	pieces := piecesOf(s)
	var w inertWriter
	last := 0
	for _, p := range pieces {
		if p.start < last {
			continue // goldmark overlapped two: the piece stays as written, for the last scan to read
		}
		w.raw(s[last:p.start])
		text := s[p.start:p.stop]
		switch p.kind {
		case pieceSpan:
			w.span(text)
		case pieceInfo:
			w.raw(inertInfo(text))
		case pieceBlock:
			w.raw(p.text)
		case pieceLive:
			w.inertLinks(escapeBackticks(text))
		}
		last = p.stop
	}
	w.raw(s[last:])
	return w.b.String()
}

// piecesOf is what a pass rewrites in s, in source order: every line of a block holding inline text
// (paragraphs, headings, tight list items, table cells), cut around its code spans; and the info
// string of every fence.
func piecesOf(s string) []piece {
	src := []byte(s)
	var pieces []piece
	inSpan := make([]bool, len(src))
	var lines []gmtext.Segment
	_ = ast.Walk(parseGFM(s), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan:
			if start, stop, ok := spanRange(n, src); ok {
				pieces = append(pieces, piece{start: start, stop: stop, kind: pieceSpan})
				for i := start; i < stop; i++ {
					inSpan[i] = true
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			pieces = append(pieces, fencePieces(n, src)...)
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			if n.Lines().Len() > 0 {
				first, last := n.Lines().At(0), n.Lines().At(n.Lines().Len()-1)
				pieces = append(pieces, movedBlock(n, src, "", lineStart(src, first.Start), last.Stop))
			}
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		}
		if n.Type() == ast.TypeBlock {
			for i := range n.Lines().Len() {
				lines = append(lines, n.Lines().At(i))
			}
		}
		return ast.WalkContinue, nil
	})
	for _, l := range lines {
		for i := l.Start; i < l.Stop; {
			if inSpan[i] {
				i++
				continue
			}
			j := i
			for j < l.Stop && !inSpan[j] {
				j++
			}
			pieces = append(pieces, piece{start: i, stop: j, kind: pieceLive})
			i = j
		}
	}
	slices.SortFunc(pieces, func(a, b piece) int { return cmp.Compare(a.start, b.start) })
	return pieces
}

// spanRange is a code span's bytes in src, its backtick runs included. goldmark records only what
// lies between them, less a space it trims from each end.
func spanRange(n *ast.CodeSpan, src []byte) (start, stop int, ok bool) {
	first, isFirst := n.FirstChild().(*ast.Text)
	last, isLast := n.LastChild().(*ast.Text)
	if !isFirst || !isLast {
		return 0, 0, false
	}
	open := last.Segment.Stop
	for open < len(src) && src[open] != '`' {
		open++
	}
	stop = open
	for stop < len(src) && src[stop] == '`' {
		stop++
	}
	start = first.Segment.Start
	for start > 0 && src[start-1] != '`' {
		start--
	}
	start -= stop - open
	return start, stop, start >= 0
}

// fencePieces is what a pass does to a fence: one opening and closing at column 0 outside every
// container keeps its lines and has its info string checked; any other moves there. An empty one
// holds nothing to move.
func fencePieces(n *ast.FencedCodeBlock, src []byte) []piece {
	info := ""
	var infoPiece []piece
	if n.Info != nil {
		seg := n.Info.Segment
		info = string(src[seg.Start:seg.Stop])
		infoPiece = []piece{{start: seg.Start, stop: seg.Stop, kind: pieceInfo}}
	}
	if n.Lines().Len() == 0 {
		return infoPiece
	}
	first, last := n.Lines().At(0), n.Lines().At(n.Lines().Len()-1)
	open := lineStart(src, lineStart(src, first.Start)-1)
	root := n.Parent() != nil && n.Parent().Kind() == ast.KindDocument
	stop := last.Stop
	atZero := root && strings.IndexByte("`~", src[open]) >= 0
	if end := lineEnd(src, stop); stop < end && fenceCloser.Match(src[stop:end]) && (root || src[stop] == ' ' || src[stop] == '>') {
		atZero = atZero && src[stop] != ' '
		stop = end
	}
	if atZero {
		return infoPiece
	}
	return []piece{movedBlock(n, src, inertInfo(info), open, stop)}
}

// movedBlock is the code block n, whose lines lie between start and stop, as a fence at column 0,
// always closed, and long enough for any backticks it holds.
func movedBlock(n ast.Node, src []byte, info string, start, stop int) piece {
	var content strings.Builder
	longest, run := 0, 0
	for i := range n.Lines().Len() {
		seg := n.Lines().At(i)
		v := seg.Value(src)
		content.Write(v)
		for _, c := range v {
			if c == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
	}
	body := content.String()
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	fence := strings.Repeat("`", max(3, longest+1))
	text := fence + info + "\n" + body + fence
	if stop > 0 && src[stop-1] == '\n' {
		text += "\n"
	}
	return piece{start: start, stop: stop, kind: pieceBlock, text: text}
}

// lineStart is the index of the line holding i.
func lineStart(src []byte, i int) int {
	if i <= 0 {
		return 0
	}
	return bytes.LastIndexByte(src[:i], '\n') + 1
}

// lineEnd is the index just past the line starting at i, its newline included.
func lineEnd(src []byte, i int) int {
	if nl := bytes.IndexByte(src[i:], '\n'); nl >= 0 {
		return i + nl + 1
	}
	return len(src)
}

// inertInfo is a fence's info string reduced to the plain language GitHub reads in it. A fence of
// `math` renders what it holds as live text, those GitHub renders client-side are not ours to vouch
// for, and GitHub decodes an entity in the info string first, so anything else becomes "text": the
// block stays a code block.
func inertInfo(info string) string {
	lang, _, _ := strings.Cut(strings.TrimSpace(info), " ")
	if lang == "" {
		return ""
	}
	if !fenceLang.MatchString(lang) {
		return "text"
	}
	switch strings.ToLower(lang) {
	case "math", "mermaid", "geojson", "topojson", "stl":
		return "text"
	}
	return lang
}

// escapeBackticks escapes every backtick of t not already escaped. t lies outside every code span,
// so each is literal text: escaped, none can pair with a span Public adds.
func escapeBackticks(t string) string {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '\\':
			b.WriteByte(t[i])
			if i+1 < len(t) {
				i++
				b.WriteByte(t[i])
			}
		case '`':
			b.WriteString("\\`")
		default:
			b.WriteByte(t[i])
		}
	}
	return b.String()
}

// decoded is t with its backslash escapes resolved, as GitHub reads it, and off maps each of its
// bytes back to t: off[i] is where byte i's escape, or byte i itself, starts in t, and off[len] is
// len(t). A "\|" stays as written: a table cell needs it even inside a span.
func decoded(t string) (view string, off []int) {
	var b strings.Builder
	off = make([]int, 0, len(t)+1)
	for i := 0; i < len(t); i++ {
		off = append(off, i)
		if t[i] == '\\' && i+1 < len(t) && t[i+1] != '|' && isASCIIPunct(t[i+1]) {
			i++
		}
		b.WriteByte(t[i])
	}
	return b.String(), append(off, len(t))
}

func isASCIIPunct(c byte) bool {
	return c > ' ' && c < 0x7f && !isAlnum(c)
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// inertWriter builds a pass's output.
type inertWriter struct {
	b strings.Builder
}

func (w *inertWriter) raw(s string) { w.b.WriteString(s) }

// span writes a code span, whole, after what its opening backticks must not touch: an odd run of
// backslashes, which would escape the first backtick and leave what it wraps live (S-6: `\@smana`),
// gains one; a backtick, whose run would merge with the span's own, gains a space; so does a "$",
// because $`x`$ is GitHub's inline math and what it holds is live.
func (w *inertWriter) span(s string) {
	cur := w.b.String()
	if run := len(cur) - len(strings.TrimRight(cur, `\`)); run%2 == 1 {
		w.b.WriteByte('\\')
	} else if strings.HasSuffix(cur, "`") || strings.HasSuffix(cur, "$") {
		w.b.WriteByte(' ')
	}
	w.b.WriteString(s)
}

// inertLinks turns each inline link of t into its text and its URL in a code span, and wraps what is
// live in the rest. The URL span is written after the live scan and never rescanned: scanning it
// would put backticks inside it, and they would pair with the span's own (`[a](\@smana)`).
func (w *inertWriter) inertLinks(t string) {
	last := 0
	for _, m := range mdLink.FindAllStringSubmatchIndex(t, -1) {
		before := t[last:m[0]]
		if run := len(before) - len(strings.TrimRight(before, `\`)); run%2 == 1 {
			continue // an escaped "[" opens no link: it stays text, its "](" defused below
		}
		w.defuseText(before + t[m[2]:m[3]] + " (")
		if m[4] < m[5] { // an empty destination gets no span
			w.span("`" + t[m[4]:m[5]] + "`")
		}
		last = m[1] - 1 // the closing ")" starts the next run
	}
	w.defuseText(t[last:])
}

// defuseText handles a run outside every link destination: a destination the pattern did not take
// (a nested bracket, a title, a line break) is no link either, "](" is what makes one; then the live
// text.
func (w *inertWriter) defuseText(t string) {
	w.codeSpanLive(strings.ReplaceAll(t, "](", "] ("))
}

// codeSpanLive wraps every live form of t in a code span, as GitHub reads t: a match is found with
// the escapes decoded, and the span holds it decoded, so `GH\-325` comes out `GH-325`. An escape
// outside a match stays as written. A match its check turns down resumes the scan just after where
// it was found, never after its end: what it covered may hold a live form of its own
// (`x@smana/y#3`, whose "@smana/y" is no mention and whose "smana/y#3" is a reference).
func (w *inertWriter) codeSpanLive(t string) {
	view, off := decoded(t)
	last := 0
	for pos := 0; pos < len(view); {
		m := liveForm.FindStringSubmatchIndex(view[pos:])
		if m == nil {
			break
		}
		group := func(i int) (string, bool) {
			if m[2*i] < 0 {
				return "", false
			}
			return view[pos+m[2*i] : pos+m[2*i+1]], true
		}
		start, end := pos+m[0], pos+m[1]
		// Right after a span Public just wrote, a form has that span's edge before it, not a letter.
		afterWord := start > last && isAlnum(view[start-1])
		if _, ok := group(formEntity); ok && keptEntity(view[start:end]) {
			pos = end
			continue
		}
		if _, ok := group(formBounded); ok && afterWord {
			pos = start + 1
			continue
		}
		if _, ok := group(formCommit); ok && (afterWord || end < len(view) && isAlnum(view[end])) {
			pos = end // hex holds no form's start that an earlier match of the same word has not tried
			continue
		}
		w.raw(t[off[last]:off[start]])
		w.span("`" + view[start:end] + "`")
		last, pos = end, end
	}
	w.raw(t[off[last]:])
}

// keptEntity is an entity that renders the one character it escapes, none of which is part of a
// reference: Text writes "&lt;" itself, and a reader expects "&amp;" to stay readable.
func keptEntity(e string) bool {
	switch e {
	case "&lt;", "&gt;", "&amp;", "&quot;":
		return true
	}
	return false
}
