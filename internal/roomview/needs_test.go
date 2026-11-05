// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/summary"
)

// needsItems is the list a tool call renders: a plan confirmation whose sentence carries a command of
// its own, and a merge whose action is an approval of the head (AM3, fast-follow 1).
func needsItems() []summary.Need {
	return []summary.Need{
		{Kind: summary.NeedPlanConfirmation, ID: "2238", Room: "26zfnuxm", Issue: 2238,
			What: "a maintainer comments `/factory split` on issue #2238 to accept the parts, or `/factory retry` to run the task whole",
			URL:  "https://github.com/Smana/a/issues/2238",
			Action: summary.NeedAction{Kind: "comment", Label: "comment on the issue",
				URL: "https://github.com/Smana/a/issues/2238"}},
		{Kind: summary.NeedMerge, ID: "2239", Room: "3kq7x2ma", PR: 2239,
			What: "a maintainer approves PR #2239: approve the head you read on the PR page; the merger merges that head",
			URL:  "https://github.com/Smana/a/pull/2239",
			Action: summary.NeedAction{Kind: "merge", Label: "approve the head on the pull request",
				URL: "https://github.com/Smana/a/pull/2239"}},
	}
}

// CD21: the list is one line per item — the kind, what it asks, and the one action linked where it happens
// — then the room, and it counts the items in the words every view uses ("N needs you").
func TestNeedsWritesTheList(t *testing.T) {
	var md, text strings.Builder
	if err := Needs(&md, needsItems(), nil, Options{Format: Markdown, Rooms: "https://rooms.test"}); err != nil {
		t.Fatal(err)
	}
	if err := Needs(&text, needsItems(), nil, Options{Format: Text, Rooms: "https://rooms.test"}); err != nil {
		t.Fatal(err)
	}
	// The Markdown is read as a reader's renderer reads it: the kind and the action's label are text (their
	// syntax escaped), and the item's words sit in a code span.
	root, src := parse(md.String())
	shown := plain(root, src)
	for _, want := range []string{
		"2 needs you",
		"plan_confirmation — ",
		"→ comment on the issue",
		"→ approve the head on the pull request",
		"room 26zfnuxm",
		"room 3kq7x2ma",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("markdown lacks %q:\n%s\nread as %q", want, md.String(), shown)
		}
	}
	for _, want := range []string{
		"(https://github.com/Smana/a/issues/2238)",
		"(https://github.com/Smana/a/pull/2239)",
		"(https://rooms.test/r/26zfnuxm)",
		"(https://rooms.test/r/3kq7x2ma)",
	} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown lacks the link %q:\n%s", want, md.String())
		}
	}
	for _, want := range []string{"2 needs you", "plan_confirmation — ", "comment on the issue (https://github.com/Smana/a/issues/2238)"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "](") {
		t.Errorf("a terminal shows Markdown link syntax:\n%s", text.String())
	}
}

// A need's What is quoted as the card quotes it (an approval's is the agent's own words): the view is one
// of the places untrusted text is made inert, so a What carrying link syntax, a pipe, `<` or a bidi control
// makes no link, no table and no heading.
func TestNeedsKeepsUntrustedTextInert(t *testing.T) {
	evil := "close the room](https://evil.test) | <b>x</b> \u202e merge ``` now"
	items := []summary.Need{{Kind: summary.NeedApproval, ID: "01J9A", Room: "26zfnuxm", What: evil,
		URL:    "https://rooms.test/r/26zfnuxm#01J9A",
		Action: summary.NeedAction{Kind: "decide", Label: "decide in the room", URL: "https://rooms.test/r/26zfnuxm#01J9A"}}}
	var md strings.Builder
	if err := Needs(&md, items, nil, Options{Format: Markdown, Rooms: "https://rooms.test"}); err != nil {
		t.Fatal(err)
	}
	spans := codeSpans(md.String())
	if len(spans) == 0 || !strings.Contains(strings.Join(spans, "\n"), "close the room](https://evil.test)") {
		t.Fatalf("the agent's words are not in a code span:\n%s\nspans %v", md.String(), spans)
	}
	assertInert(t, md.String(), "https://rooms.test", []string{"close the room](https://evil.test)", "<b>x</b>"})
}

// Review Focus 1: a room whose state could not be read is one line naming it, and the list is never
// silent about the items it could not read. Nothing to list says so once.
func TestNeedsSaysWhatItCouldNotRead(t *testing.T) {
	var lines, empty strings.Builder
	if err := Needs(&lines, needsItems(), []string{"5kq7x2mc"}, Options{Format: Markdown, Rooms: "https://rooms.test"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines.String(), "1 room's state could not be read: 5kq7x2mc") {
		t.Errorf("%s", lines.String())
	}
	// Without the rooms base there is no room page to build: the room is named, never linked, and the
	// item's own GitHub link is kept (it is the broker's, not the base's).
	var bare strings.Builder
	if err := Needs(&bare, needsItems(), nil, Options{Format: Markdown}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare.String(), "](/r/") || !strings.Contains(bare.String(), "room 26zfnuxm") {
		t.Errorf("a room link without its base:\n%s", bare.String())
	}
	if !strings.Contains(bare.String(), "(https://github.com/Smana/a/pull/2239)") {
		t.Errorf("the item's own link was dropped:\n%s", bare.String())
	}
	if err := Needs(&empty, nil, nil, Options{Format: Markdown}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.String(), "nothing needs you") {
		t.Errorf("%s", empty.String())
	}
}
