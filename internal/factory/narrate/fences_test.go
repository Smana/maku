// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/Smana/maku/internal/envelope"
)

// render is the comment as a GFM renderer reads it.
func render(t *testing.T, md string) string {
	t.Helper()
	var out bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(md), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

var preBlock = regexp.MustCompile(`(?s)<pre><code[^>]*>(.*?)</code></pre>`)

// outsideCode is the rendered page with its code blocks and spans cut out.
func outsideCode(html string) string {
	html = preBlock.ReplaceAllString(html, "")
	return regexp.MustCompile(`(?s)<code>.*?</code>`).ReplaceAllString(html, "")
}

// Bug: an acceptance check holding a fence rendered as an empty code block, and its closer opened a
// fence that swallowed the answer section, the clip mark with it.
func TestPlanProposedKeepsAgentFencesInTheirItems(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	long := "grep -c '| pending |' results.md counts one per row " + strings.Repeat("and more words ", 40)
	sc := &envelope.Scope{
		Task:    "Add the results file.\n```\nnot a fence of ours\n```",
		Touches: []string{"docs/results.md"},
		Acceptance: []envelope.Acceptance{
			{Check: "```sh\ngit status --porcelain\n```", Proves: "only the new file"},
			{Check: long + "\n```\nunclosed", Proves: "the clip lands inside an open fence"},
			{Check: "task docs", Proves: "the gates pass"},
		},
	}
	sp := &envelope.Split{Children: []envelope.Scope{{Task: "part ```one```\n```\nopen"}, {Task: "part two"}}}
	body := PlanProposed(tk, sc, sp, "https://rooms.example").Body
	html := render(t, body)

	codes := preBlock.FindAllStringSubmatch(html, -1)
	if len(codes) != 6 { // the task, three checks, two parts
		t.Fatalf("want 6 code blocks, got %d:\n%s\n---\n%s", len(codes), body, html)
	}
	for _, c := range codes {
		if strings.TrimSpace(c[1]) == "" {
			t.Fatalf("an empty code block:\n%s", html)
		}
		if strings.Contains(c[1], "Answer on this issue") || strings.Contains(c[1], "⟦clipped") {
			t.Fatalf("our text inside a code block:\n%s", html)
		}
	}
	prose := outsideCode(html)
	for _, want := range []string{"<strong>Answer on this issue</strong>", "⟦clipped by the factory",
		`<a href="https://rooms.example/r/3buqdlot">the room</a>`} {
		if !strings.Contains(prose, want) {
			t.Fatalf("%q is not outside the code blocks:\n%s", want, html)
		}
	}
	if !strings.Contains(html, "<li><code>/factory plan approve</code>") {
		t.Fatalf("the answers are not a list:\n%s", html)
	}
	checks, parts := strings.Count(html, "<li>\n<pre><code"), strings.Count(html, "</p>\n<pre><code")
	if checks != 3 || parts != 3 { // the task and two parts follow their label
		t.Fatalf("%d checks and %d parts hold their code block, want 3 and 3:\n%s", checks, parts, html)
	}
}

func TestSummaryClipMarkIsOutsideTheFence(t *testing.T) {
	s := summary("```\n"+strings.Repeat("x ", 400), 300)
	if !strings.HasSuffix(s, "\n"+strings.TrimPrefix(clipMark, "\n")) || len(s) > 300 {
		t.Fatalf("clip mark not after the fence: %q", s)
	}
	if html := render(t, s); strings.Contains(preBlock.FindString(html), "⟦clipped") {
		t.Fatalf("clip mark inside the code block:\n%s", html)
	}
}

// Bug: findings in a code fence showed raw **bold** and scrolled sideways. They render as markdown
// now, with nothing in them able to mention, link or reference.
func TestFindingsPostedIsReadableAndInert(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	findings := "## Root cause\n\n**The gate** reads the wrong path; @octocat saw it in #123, see " +
		"https://evil.example/x and Smana/maku#9, GH-4, 18e264e480a87e87afcb971b08cf6f854f02f188, " +
		"[click](https://evil.example/y) ![img](https://evil.example/i.png) <img src=x> &commat;octocat\n\n" +
		"```go\nfmt.Println(\"@inside\")\n"
	body := FindingsPosted(tk, findings, "https://rooms.example").Body
	html := render(t, body)
	t.Log(body)
	if !strings.Contains(html, "<strong>The gate</strong>") || !strings.Contains(html, "<h2>Root cause</h2>") {
		t.Fatalf("the findings do not render as markdown:\n%s", html)
	}
	if strings.Contains(html, "<a ") || strings.Contains(html, "<img") {
		t.Fatalf("a link or an image renders:\n%s", html)
	}
	prose := outsideCode(html)
	for _, live := range []string{"@octocat", "#123", "evil.example", "Smana/maku#9", "GH-4", "18e264e", "&commat;", "@inside"} {
		if strings.Contains(prose, live) {
			t.Fatalf("%q is live outside a code span:\n%s", live, html)
		}
	}
	// The findings' unclosed fence is closed: the offer is ours, outside it.
	if !strings.Contains(prose, "To have this fixed") {
		t.Fatalf("the offer is inside a code block:\n%s", html)
	}
}

func TestFindingsPostedClippedStaysBalanced(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	findings := "**Summary**\n\n```\n" + strings.Repeat("a line of code in an open fence\n", 400)
	e := FindingsPosted(tk, findings, "https://rooms.example")
	if len(e.Body) > maxBody {
		t.Fatalf("len %d > %d", len(e.Body), maxBody)
	}
	prose := outsideCode(render(t, e.Body))
	for _, want := range []string{"⟦clipped by the factory", "To have this fixed", "<strong>Summary</strong>"} {
		if !strings.Contains(prose, want) {
			t.Fatalf("%q not outside code:\n%s", want, e.Body)
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap(strings.Repeat("word ", 50)+"\n"+strings.Repeat("x", 250), 100)
	for line := range strings.SplitSeq(got, "\n") {
		if len([]rune(line)) > 100 {
			t.Fatalf("line of %d runes: %q", len(line), line)
		}
	}
	if strings.ReplaceAll(strings.ReplaceAll(got, "\n", ""), " ", "") !=
		strings.Repeat("word", 50)+strings.Repeat("x", 250) {
		t.Fatalf("wrap lost text: %q", got)
	}
}

func TestCloseFence(t *testing.T) {
	for in, want := range map[string]string{
		"a\n```go\nb":         "a\n```go\nb\n```",
		"a\n````\n```\nb":     "a\n````\n```\nb\n````",
		"a\n```\nb\n```":      "a\n```\nb\n```",
		"~~~\nx":              "~~~\nx\n~~~",
		"``` not `a` fence\n": "``` not `a` fence\n",
	} {
		if got := closeFence(in); got != want {
			t.Errorf("closeFence(%q) = %q, want %q", in, got, want)
		}
	}
}

// What Public cannot make inert falls back to the fenced block, wrapped so no line scrolls.
func TestFindingsPostedWithheldFallsBackWrapped(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	findings := "```\n  ```\n- | a |\n|---|\n| x | @Smana |\n```\n```\n" + strings.Repeat("long words ", 30)
	body := FindingsPosted(tk, findings, "https://rooms.example").Body
	html := render(t, body)
	if strings.Contains(html, "<a ") || strings.Contains(outsideCode(html), "Smana") {
		t.Fatalf("the fallback is live:\n%s", html)
	}
	if !strings.Contains(outsideCode(html), "To have this fixed") || strings.Contains(body, "@Smana") {
		t.Fatalf("the fallback is not inert or swallows the offer:\n%s", body)
	}
	for line := range strings.SplitSeq(body, "\n") {
		if len([]rune(line)) > wrapAt {
			t.Fatalf("a line of %d runes:\n%s", len([]rune(line)), body)
		}
	}
}

// Bug (v0.54.8 drill): the comment offered /factory plan split for a plan that proposed no parts,
// and each check's description sat in its code block, so a long one scrolled. The split is offered
// only beside proposed parts, and the description follows its block as inert prose.
func TestPlanProposedOffersSplitOnlyForPartsAndWrapsWhatAChecksProves(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	long := "the intro paragraph links the gcp-0 results file and nothing else in the runbook changes " +
		strings.Repeat("and more words ", 10) + "@octocat see #12 https://evil.example/x"
	sc := &envelope.Scope{Task: "Add one sentence.", Acceptance: []envelope.Acceptance{{Check: "task docs", Proves: long}}}

	body := PlanProposed(tk, sc, nil, "https://rooms.example").Body
	if strings.Contains(body, "/factory plan split") {
		t.Fatalf("split offered for a plan without parts:\n%s", body)
	}
	html := render(t, body)
	for _, c := range preBlock.FindAllStringSubmatch(html, -1) {
		if strings.Contains(c[1], "nothing else in the runbook") {
			t.Fatalf("the description is inside a code block:\n%s", html)
		}
	}
	prose := outsideCode(html)
	if !strings.Contains(prose, "Proves: the intro paragraph links") {
		t.Fatalf("the description does not follow its check as prose:\n%s", html)
	}
	for _, live := range []string{"@octocat", "#12", "evil.example"} {
		if strings.Contains(prose, live) {
			t.Fatalf("%q is live outside a code span:\n%s", live, html)
		}
	}

	sp := &envelope.Split{Children: []envelope.Scope{{Task: "part one"}, {Task: "part two"}}}
	if body := PlanProposed(tk, sc, sp, "https://rooms.example").Body; !strings.Contains(body, "/factory plan split") {
		t.Fatalf("split not offered beside proposed parts:\n%s", body)
	}
}
