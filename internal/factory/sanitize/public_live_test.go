// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The forms below were read off GitHub's POST /markdown (mode=gfm, context=Smana/maku) on
// 2026-11-04: GitHub decodes entities and backslash escapes before it looks for references.
const sha40 = "18e264e480a87e87afcb971b08cf6f854f02f188"

// TestPublicExactForms pins what each form becomes: the span holds the form, nothing else changes.
func TestPublicExactForms(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		// entities: shown as written, in a span, so GitHub never decodes them
		{"named @", "&commat;Smana", "`&commat;`Smana"},
		{"hex @", "&#x40;Smana", "`&#x40;`Smana"},
		{"hex @, upper X", "&#X40;Smana", "`&#X40;`Smana"},
		{"decimal @", "&#64;Smana", "`&#64;`Smana"},
		{"padded @", "&#064;Smana", "`&#064;`Smana"},
		{"eight digits @", "&#00000064;Smana", "`&#00000064;`Smana"},
		{"named #", "&num;325", "`&num;`325"},
		{"decimal #", "&#35;325", "`&#35;`325"},
		{"hex #", "&#x23;325", "`&#x23;`325"},
		{"owner, named #", "Smana&num;325", "Smana`&num;`325"},
		{"a hyphen as an entity", "GH&#45;325", "GH`&#45;`325"},
		{"a slash as an entity", "Smana&sol;maku#325", "Smana`&sol;` `maku#325`"},
		{"an entity inside the login", "@&#83;mana", "@`&#83;`mana"},
		{"an email's @ as an entity", "someone&commat;evil.example", "someone`&commat;`evil.example"},
		{"an escaped backslash before an entity", `\\&commat;Smana`, "\\\\`&commat;`Smana"},
		{"an escaped ampersand", `\&commat;Smana`, "`&commat;`Smana"},
		{"an invisible character as an entity", "@&#x200b;Smana", "@`&#x200b;`Smana"},
		// the entities Text writes, and a literal one, stay
		{"Text's own escape", "<b>", "&lt;b>"},
		{"amp", "a &amp; b", "a &amp; b"},
		{"a mention after a kept entity", "&amp;#325", "&amp;`#325`"},
		// references with no entity
		{"owner#n", "Smana#325", "`Smana#325`"},
		{"lower owner#n", "smana#325", "`smana#325`"},
		{"owner with a hyphen", "Smana-#325", "`Smana-#325`"},
		{"owner after a dot", "x.Smana#325", "x.`Smana#325`"},
		{"GH-n", "GH-325", "`GH-325`"},
		{"gh-n", "gh-325", "`gh-325`"},
		{"GH-n after a word and a hyphen", "x-GH-325", "x-`GH-325`"},
		{"GH-n in brackets", "(GH-325)", "(`GH-325`)"},
		{"owner/repo/issues/n", "Smana/maku/issues/325", "`Smana/maku/issues/325`"},
		{"owner/repo/pull/n", "see Smana/maku/pull/325 x", "see `Smana/maku/pull/325` x"},
		{"owner/repo/discussions/n", "Smana/maku/discussions/325", "`Smana/maku/discussions/325`"},
		{"github.com with no scheme", "github.com/Smana/maku/issues/325", "`github.com/Smana/maku/issues/325`"},
		{"GITHUB.com with no scheme", "GITHUB.com/Smana/maku/issues/325", "`GITHUB.com/Smana/maku/issues/325`"},
		{"a relative issue path", "/issues/325", "`/issues/325`"},
		{"a relative pull path", "(/pull/325)", "(`/pull/325`)"},
		{"a relative path after a span", "`x`/issues/325", "`x` `/issues/325`"},
		{"a relative path after a team", "@org/team/issues/325", "`@org/team` `/issues/325`"},
		{"a word before a relative path", "x/issues/325", "x/issues/325"},
		{"what a refused match covered: a reference", "x@smana/maku#325", "x@`smana/maku#325`"},
		{"what a refused match covered: a path", "x@Smana/maku/issues/325", "x@`Smana/maku/issues/325`"},
		{"what a refused match covered: a URL", "text@https://evil.example/x", "text@`https://evil.example/x`"},
		{"what a refused match covered: an email", "x@y@evil.example", "x@`y@evil.example`"},
		{"what a refused match covered: an owner", "math@Smana#325", "math@`Smana#325`"},
		{"what an allowed word covered", "fix#9/fix#9", "fix#`9/fix#9`"},
		{"advisory", "GHSA-4jq9-2xhw-jpx7", "`GHSA-4jq9-2xhw-jpx7`"},
		{"cve", "CVE-2021-44228", "`CVE-2021-44228`"},
		// commits
		{"owner@sha", "Smana@" + sha40, "`Smana@" + sha40 + "`"},
		{"owner@short", "Smana@18e264e", "`Smana@18e264e`"},
		{"owner/repo@sha", "Smana/maku@" + sha40, "`Smana/maku@" + sha40 + "`"},
		{"owner/repo@short", "Smana/maku@18e264e", "`Smana/maku@18e264e`"},
		{"bare sha", sha40, "`" + sha40 + "`"},
		{"bare short sha", "see 18e264e here", "see `18e264e` here"},
		{"digits only", "8432076", "`8432076`"},
		{"a range", "18e264e...18e264e", "`18e264e`...`18e264e`"},
		{"emphasised sha", "_18e264e_", "_`18e264e`_"},
		// a backslash escape is decoded first too
		{"escaped hyphen", `GH\-325`, "`GH-325`"},
		{"escaped hash", `Smana\#325`, "`Smana#325`"},
		{"escaped slash", `Smana\/maku#325`, "`Smana/maku#325`"},
		{"escaped at", `Smana\@18e264e`, "`Smana@18e264e`"},
		{"escaped email", `foo\@evil.example`, "`foo@evil.example`"},
		{"escaped path", `Smana/maku\/issues/325`, "`Smana/maku/issues/325`"},
		{"a literal backslash stays and does not escape the span", `\Smana/x#7`, "\\\\`Smana/x#7`"},
		// schemes in any case
		{"upper scheme", "HTTP://evil.example", "`HTTP://evil.example`"},
		{"mixed scheme", "HtTpS://evil.example", "`HtTpS://evil.example`"},
		{"upper www", "WWW.evil.example", "`WWW.evil.example`"},
		// runs of mentions a boundary could shadow
		{"mentions sharing a hyphen", "@a-@b-@c-@d-@e-@f-@g-@h-@i", "`@a-` `@b-` `@c-` `@d-` `@e-` `@f-` `@g-` `@h-` `@i`"},
		// a span Public opens must not merge with the run beside it
		{"after a double span", "``x``@smana", "``x`` `@smana`"},
		{"before a double span", "@smana``x``", "`@smana` ``x``"},
		{"after a single span", "`x`@smana", "`x` `@smana`"},
		{"before a single span", "#3`x`", "`#3` `x`"},
		{"after a triple span", "```x```#3", "```x``` `#3`"},
		// a span must not read as GitHub's $`math`$, whose content is live
		{"dollars around a mention", "$@Smana$", "$ `@Smana`$"},
		{"dollars around a span", "$`@Smana`$", "$ `@Smana`$"},
		{"dollars around a double span", "$``@Smana``$", "$ ``@Smana``$"},
		{"an escaped dollar", `\$` + "`@Smana`$", `\$ ` + "`@Smana`$"},
		{"double dollars", "$$`@Smana`$$", "$$ `@Smana`$$"},
		// a math fence's content is live too
		{"math fence", "```math\n@Smana\n```", "```text\n@Smana\n```"},
		{"math fence, a space first", "``` math\n@Smana\n```", "``` text\n@Smana\n```"},
		{"math fence, tildes", "~~~math\n@Smana\n~~~", "~~~text\n@Smana\n~~~"},
		{"math fence, a second word", "```math x\n@Smana\n```", "```text\n@Smana\n```"},
		{"math fence, an entity", "```ma&#116;h\n@Smana\n```", "```text\n@Smana\n```"},
		{"math fence, in a quote", "> ```math\n> @Smana\n> ```", "> ```text\n> @Smana\n> ```"},
		{"math fence, in a list", "- ```math\n  @Smana\n  ```", "- ```text\n  @Smana\n  ```"},
		// what looked like a code span and is not one
		{"across a blank line", "`a\n\n@smana b`", "\\`a\n\n`@smana` b\\`"},
		{"across a list item", "`a\n- @smana b`", "\\`a\n- `@smana` b\\`"},
		{"across a quote", "`a @smana b\n> c`", "\\`a `@smana` b\n> c\\`"},
		{"across a heading", "`a\n# @smana b", "\\`a\n# `@smana` b"},
		{"across a table's pipe", "| a | b |\n|---|---|\n| `x | @smana b` | y |", "| a | b |\n|---|---|\n| \\`x | `@smana` b\\` | y |"},
		{"a header's pipe", "| `a | @smana b` |\n|---|---|\n| x | y |", "| \\`a | `@smana` b\\` |\n|---|---|\n| x | y |"},
		// a longer fence shields what it holds, and its neighbours stay live
		{"a fence inside a false span", "`a\n```\n@smana b\n```\nc @d", "\\`a\n```\n@smana b\n```\nc `@d`"},
		{"a longer fence", "````\n```\n@smana\n```\n````\n@d", "````\n```\n@smana\n```\n````\n`@d`"},
		{"an indented block", "    @smana", "    @smana"},
		{"a fence stays as written", "```go\nx := \"@smana #1 " + sha40 + " https://evil.example\"\n```\n", "```go\nx := \"@smana #1 " + sha40 + " https://evil.example\"\n```\n"},
		{"a table's escaped pipe keeps its span", "| a |\n|---|\n| `x \\| @smana b` |", "| a |\n|---|\n| `x \\| @smana b` |"},
		// ordinary text
		{"C#", "C# and fix#9.", "C# and fix#9."},
		{"Fix#9", "Fix#9 here", "Fix#9 here"},
		{"plain words", "a b, 50% & c; 1 2 3 #; x@y; abc123 v1.2.3 12345 #fff", "a b, 50% & c; 1 2 3 #; x@y; abc123 v1.2.3 12345 #fff"},
		{"a word before @", "a@Smana", "a@Smana"},
		{"a word before #", "1#325", "`1#325`"},
		{"markup stays", "## What\n\n- *a* **b** `c`\n- [ ] d\n\n> e\n\n| x |\n|---|\n| y |\n", "## What\n\n- *a* **b** `c`\n- [ ] d\n\n> e\n\n| x |\n|---|\n| y |\n"},
		{"a span's entity stays", "`&commat;` and `&amp;`", "`&commat;` and `&amp;`"},
		{"a span's dollar", "cost `$HOME` x", "cost `$HOME` x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, rep := Public(c.in)
			if got != c.want {
				t.Fatalf("Public(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
			if rep.Withheld {
				t.Fatalf("Public(%q) withheld", c.in)
			}
			assertNoLiveReference(t, c.in, got)
		})
	}
}

// liveRE holds the forms GitHub reads in decoded text. It is written apart from the code's pattern
// on purpose, and is wider where that is simpler: a test that shares the pattern proves nothing.
var liveRE = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])@[a-z0-9]` +
	`|(?:^|[^a-z0-9])#[0-9]` +
	`|(?:^|[^a-z0-9])gh-[0-9]` +
	`|(?:^|[^a-z0-9#@])(?-i:[0-9a-f]{7,40})(?:$|[^a-z0-9])` +
	`|(?:^|[^a-z0-9])/(?:issues|pull|discussions)/[0-9]` +
	`|[a-z0-9-]@[0-9a-f]{7}` +
	`|[a-z0-9.-]+@[a-z0-9-]+\.[a-z0-9]` +
	`|[a-z0-9_.-]+/[a-z0-9_.-]+#[0-9]` +
	`|[a-z0-9_.-]+/[a-z0-9_.-]+/(?:issues|pull|discussions)/[0-9]` +
	`|(?:https?|ftp)://|www\.|github\.com/` +
	`|ghsa-|cve-[0-9]`)

// ownerRE is an owner#n form, the one the allowed word fix#n can match.
var ownerRE = regexp.MustCompile(`(?i)[a-z0-9-]+#[0-9]+`)

// liveTexts is each stretch of text a reader sees outside a code span, entities and escapes
// decoded as GitHub decodes them before it looks for references. Anything that is not text ends a
// stretch: GitHub does not read a reference across an element.
func liveTexts(out string) []string {
	src := []byte(out)
	var texts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			texts = append(texts, cur.String())
			cur.Reset()
		}
	}
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(gmtext.NewReader(src))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch n := n.(type) {
		case *ast.CodeSpan:
			if entering {
				flush()
			}
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if entering {
				v := util.UnescapePunctuations(n.Segment.Value(src))
				cur.Write(util.ResolveEntityNames(util.ResolveNumericReferences(v)))
				if n.SoftLineBreak() || n.HardLineBreak() {
					cur.WriteByte('\n')
				}
			}
		default:
			flush()
		}
		return ast.WalkContinue, nil
	})
	flush()
	return texts
}

// assertNoLiveReference is the property Public promises, read back from its output.
func assertNoLiveReference(t *testing.T, in, out string) {
	t.Helper()
	if l := links(out); len(l) != 0 {
		t.Errorf("Public(%q): %v survived in %q", in, l, out)
	}
	for _, text := range liveTexts(out) {
		if m := liveRE.FindString(text); m != "" {
			t.Errorf("Public(%q): %q is live in %q (text %q)", in, m, out, text)
		}
		for _, m := range ownerRE.FindAllString(text, -1) {
			// A leading hyphen is no part of a login, so "-fix#9" is the allowed word too.
			if word, _, _ := strings.Cut(strings.ToLower(m), "#"); strings.TrimLeft(word, "-") != allowedOwner {
				t.Errorf("Public(%q): %q is live in %q (text %q)", in, m, out, text)
			}
		}
	}
	src := []byte(out)
	_ = ast.Walk(parseGFM(out), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan:
			start, stop, ok := spanRange(n, src)
			if !ok {
				t.Errorf("Public(%q): a code span without a range in %q", in, out)
			} else if start > 0 && stop < len(src) && src[start-1] == '$' && src[stop] == '$' {
				t.Errorf("Public(%q): a span reads as math in %q", in, out)
			}
		case *ast.FencedCodeBlock:
			if lang := string(n.Language(src)); strings.EqualFold(lang, "math") {
				t.Errorf("Public(%q): a math fence survived in %q", in, out)
			}
		}
		return ast.WalkContinue, nil
	})
}

// The property over every position a form can sit in: whatever surrounds it, Public's output holds
// no link node and no reference form outside a code span. The forms are the reported bypasses, the
// surroundings each thing that once shielded or unshielded one.
func TestPublicLeavesNothingLive(t *testing.T) {
	forms := []string{
		"@smana", "@org/team", "#325", "Smana#325", "Smana/maku#325", "GH-325", "gh-325",
		"Smana/maku/issues/325", "Smana/maku/pull/325", "github.com/Smana/maku/issues/325",
		"Smana@" + sha40, "Smana@18e264e", "Smana/maku@" + sha40, "Smana/maku@18e264e", sha40, "18e264e", "8432076",
		"&commat;Smana", "&#x40;Smana", "&#X40;Smana", "&#64;Smana", "&#064;Smana", "&#0000064;Smana", "&#00000064;Smana",
		"&num;325", "&#35;325", "&#x23;325", "&#X23;325", "&#035;325", "Smana&num;325", "Smana&#35;325", "GH&#45;325",
		"Smana&commat;" + sha40, "Smana&#64;18e264e", "&amp;#35;325", "&amp;#325", "&#38;#35;325", "&#38;num;325",
		`\@smana`, `\#325`, `GH\-325`, `Smana\#325`, `Smana\/maku#325`, `Smana\@18e264e`,
		"https://evil.example/x", "HTTP://evil.example", "www.evil.example", "WWW.evil.example", "ftp://evil.example",
		"someone@evil.example", "someone&commat;evil.example", `someone\@evil.example`,
		"[a](https://evil.example/x)", "[@smana](x)", "[a](x \"@smana\")", "[`x`](https://evil.example/x)", "[a](<https://evil.example/x y>)",
		"<https://evil.example/x>", "<a href=x>", "![a](https://evil.example/x)", "[a][r]\n\n[r]: https://evil.example/x",
		"GHSA-4jq9-2xhw-jpx7", "CVE-2021-44228",
	}
	around := []struct{ before, after string }{
		{"", ""},
		{"see ", " here"},
		{"(", ")."},
		{"_", "_"},
		{"*", "*"},
		{"**", "**"},
		{"~~", "~~"},
		{"$", "$"},
		{"$`", "`$"},
		{"\\", ""},
		{"\\\\", ""},
		{"\\\\\\", "\\"},
		{"`", ""},
		{"", "`"},
		{"``", ""},
		{"`x`", ""},
		{"", "`x`"},
		{"``x``", ""},
		{"", "``x``"},
		{"```x```", "```x```"},
		{"`x` ", " ``y``"},
		{"[", "]"},
		{"[l](", ")"},
		{"[", "](u)"},
		{"# ", ""},
		{"## ", " ##"},
		{"> ", ""},
		{"- ", ""},
		{"1. ", ""},
		{"- [ ] ", ""},
		{"x\n===\n", ""},
		{"", "\n==="},
		{"| a |\n|---|\n| ", " |"},
		{"| ", " |\n|---|\n| x |"},
		{"| a | b |\n|---|---|\n| `x | ", " b` | y |"},
		{"`a\n\n", " b`"},
		{"`a\n- ", " b`"},
		{"`a ", " b\n> c`"},
		{"`a\n# ", " b`"},
		{"``a\n\n", "``"},
		{"```\nx\n```\n", "\n```\ny\n```"},
		{"```math\n", "\n```"},
		{"~~~ math\n", "\n~~~"},
		{"```x\n", ""}, // an unclosed fence: code to the end for both parsers
	}
	for _, f := range forms {
		for _, a := range around {
			in := a.before + f + a.after
			out, rep := Public(in)
			if rep.Withheld {
				t.Errorf("Public(%q): withheld: nothing here should need it", in)
			}
			assertNoLiveReference(t, in, out)
			// The fixpoint: another pass finds nothing.
			if again := inertPass(out); again != out {
				t.Errorf("Public(%q) is not stable:\n once  %q\n twice %q", in, out, again)
			}
		}
	}
}

// soup is a text of n tokens picked by seed: markup that opens or closes something, next to forms
// that must not end up live. It finds the arrangements no table thought of.
func soup(seed int64, n int) string {
	tokens := []string{
		"`", "``", "```", "````", "~~~", "\\", "\\\\", "$", "$$", "&", "&amp;", "&lt;", "&#64;", "&commat;", "&num;", "&#35;",
		"|", "\\|", "|---|", "\n", "\n", "\n\n", " ", " ", " ", "    ", "> ", "- ", "1. ", "# ", "===", "---", "***", "*", "_", "**", "~~",
		"[", "]", "](", "(", ")", "<", ">", "!", "!\\[", ":", "]:", ".", "/", "-", "@", "#", "=",
		"@smana", "#325", "Smana#325", "GH-325", "Smana/maku#325", "18e264e", "8432076", "Smana@18e264e", "Smana/maku/issues/325",
		"https://evil.example/x", "www.evil.example", "a@evil.example", "fix#9", "C#", "word", "x", "math", "```math\n", "text",
	}
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // a reproducible test corpus
	var b strings.Builder
	for range n {
		b.WriteString(tokens[r.Intn(len(tokens))])
	}
	return b.String()
}

// The same property over arrangements nobody wrote down. Seeded, so a failure reproduces.
func TestPublicLeavesNothingLiveInASoup(t *testing.T) {
	for seed := range int64(1000) {
		in := soup(seed, 8+int(seed%60))
		out, rep := Public(in)
		if rep.Withheld {
			t.Errorf("Public(%q): withheld", in)
			continue
		}
		assertNoLiveReference(t, in, out)
		if again := inertPass(out); again != out {
			t.Errorf("Public(%q) is not stable:\n once  %q\n twice %q", in, out, again)
		}
	}
}

// Public fails closed as Text does: a rewrite that does not settle publishes nothing.
func TestPublicWithholdsWhatDoesNotSettle(t *testing.T) {
	if _, ok := settle("@smana", func(s string) string { return s + "x" }); ok {
		t.Fatal("a rewrite that never settles must not publish")
	}
	if _, ok := settle("[a](b)", func(s string) string { return s }); ok {
		t.Fatal("a link the rewrite left must not publish")
	}
	if out, rep := Public("\u202e@smana"); rep.Withheld || rep.Invisible != 1 || out != "`@smana`" {
		t.Fatalf("the report survives: got %q, %+v", out, rep)
	}
}
