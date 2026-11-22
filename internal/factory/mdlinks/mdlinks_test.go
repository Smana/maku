// SPDX-License-Identifier: Apache-2.0

package mdlinks

import (
	"slices"
	"testing"
)

const testFile = "website/content/docs/a.md"

// A closing fence ends its block: each block is its own range, and only an unclosed one runs
// to the end of the text (cloud-native-ref#2517).
func TestFencesOf(t *testing.T) {
	for name, c := range map[string]struct {
		s    string
		want [][2]int
	}{
		"two blocks":            {"```\nx\n```\ntext\n```\ny\n```\n", [][2]int{{0, 9}, {15, 24}}},
		"indented blocks":       {"- a\n  ```\n  x\n  ```\n- b\n\t```\n\t```", [][2]int{{4, 19}, {24, 33}}},
		"unclosed at EOF":       {"a\n```\nb", [][2]int{{2, 7}}},
		"closed, then unclosed": {"```\n```\nt\n```\nu", [][2]int{{0, 7}, {10, 15}}},
		"tilde fence":           {"~~~\nx\n~~~\ntext\n", [][2]int{{0, 9}}},
		"four-backtick outer":   {"````md\n```\nx\n```\n````\ntext\n", [][2]int{{0, 21}}},
		"closer with info":      {"```\nx\n```sh\ny\n```\n", [][2]int{{0, 17}}},
		"inline triple tick":    {"```code```\n[a](x.md)\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := fencesOf(c.s); !slices.Equal(got, c.want) {
				t.Fatalf("fencesOf = %v, want %v", got, c.want)
			}
		})
	}
}

// R52's LinksOnly: a docs-links or revert diff may move link targets and nothing else.
func TestLinksOnly(t *testing.T) {
	one := func(s string) map[string]string { return map[string]string{testFile: s} }
	for name, c := range map[string]struct {
		base, headText string
		ok             bool
	}{
		"a relative retarget":             {"see [the guide](docs/old.md).", "see [the guide](docs/new.md).", true},
		"a retarget within the host":      {"[a](https://example.com/old)", "[a](https://example.com/new)", true},
		"a retarget to the archive":       {"[a](https://example.com/gone)", "[a](https://web.archive.org/web/2020/https://example.com/gone)", true},
		"a definition retarget":           {"[a]: https://example.com/old\n[a] and text", "[a]: https://example.com/new\n[a] and text", true},
		"a title unchanged, target moved": {`[a](docs/old.md "The Guide")`, `[a](docs/new.md "The Guide")`, true},
		"relative to absolute":            {"[a](docs/x.md)", "[a](https://example.com/x)", false},
		"a protocol-relative jump":        {"[a](docs/x.md)", "[a](//evil.com/x)", false},
		"a fenced retarget":               {"```\n[a](docs/old.md)\n```", "```\n[a](docs/new.md)\n```", false},
		"an injected script":              {"plain text.", "plain text <script>alert(1)</script>.", false},
		"a changed fenced command":        {"```\ncurl https://example.com/old\n```", "```\ncurl https://example.com/new\n```", false},
		"a reworded sentence":             {"The link is broken.", "The link has rotted.", false},
		"a new external host":             {"[a](https://example.com/x)", "[a](https://evil.example/x)", false},
		"a javascript target":             {"[a](https://example.com/x)", "[a](javascript:alert(1))", false},
		"a link added":                    {"text.", "text. [a](new.md)", false},
		// cloud-native-ref#2517: a code block above the links must not fence everything after it.
		"a retarget after a block":           {"```\nx\n```\n[a](docs/old.md)", "```\nx\n```\n[a](docs/new.md)", true},
		"a retarget between two blocks":      {"```\nx\n```\n[a](old.md)\n```\ny\n```", "```\nx\n```\n[a](new.md)\n```\ny\n```", true},
		"a retarget after two blocks":        {"```\nx\n```\n```\ny\n```\n[a](old.md)", "```\nx\n```\n```\ny\n```\n[a](new.md)", true},
		"a retarget after a list's block":    {"- step:\n  ```sh\n  ls\n  ```\n[a](old.md)", "- step:\n  ```sh\n  ls\n  ```\n[a](new.md)", true},
		"a retarget in a list's block":       {"- step:\n  ```\n  [a](old.md)\n  ```\n", "- step:\n  ```\n  [a](new.md)\n  ```\n", false},
		"a retarget after an unclosed fence": {"```\nx\n[a](old.md)", "```\nx\n[a](new.md)", false},
		// SEC-H10: browser URL-normalisation host jumps and userinfo smuggling.
		"backslash protocol-relative jump":        {"[a](docs/x.md)", `[a](/\evil.com/x)`, false},
		"double-backslash protocol-relative jump": {"[a](docs/x.md)", `[a](\\evil.com/x)`, false},
		"triple-slash protocol-relative jump":     {"[a](docs/x.md)", "[a](///evil.com/x)", false},
		"angle-bracketed backslash jump":          {"[a](docs/x.md)", `[a](</\evil.com/x>)`, false},
		"angle-bracketed leading-space jump":      {"[a](docs/x.md)", "[a](< //evil.com/x>)", false},
		"angle-bracketed tab-smuggled jump":       {"[a](docs/x.md)", "[a](</\t/evil.com/x>)", false},
		"https userinfo on same host":             {"[a](https://example.com/old)", "[a](https://user@example.com/new)", false},
		"https userinfo host confusion":           {"[a](https://example.com/old)", "[a](https://example.com@evil.com/new)", false},
		"https userinfo on archive":               {"[a](https://example.com/gone)", "[a](https://user@web.archive.org/web/2020/https://example.com/gone)", false},
		"https opaque backslash":                  {"[a](https://example.com/old)", `[a](https:\evil.com/new)`, false},
		// SEC-H10: non-triple-backtick code constructs must not be treated as link targets.
		"a tilde-fenced retarget":              {"~~~\n[a](old.md)\n~~~", "~~~\n[a](new.md)\n~~~", false},
		"a retarget after a tilde fence":       {"~~~\nx\n~~~\n[a](old.md)", "~~~\nx\n~~~\n[a](new.md)", true},
		"a four-backtick fenced retarget":      {"````md\n```\nx\n```\n[a](old.md)\n````", "````md\n```\nx\n```\n[a](new.md)\n````", false},
		"a retarget after a four-backtick box": {"````md\n```\nx\n```\n````\n[a](old.md)", "````md\n```\nx\n```\n````\n[a](new.md)", true},
		"a closer with info string stays open": {"```\nx\n```sh\n[a](old.md)\n```", "```\nx\n```sh\n[a](new.md)\n```", false},
		"a four-space indented code mutation":  {"text\n\n    arr[0](old)\n", "text\n\n    arr[0](new)\n", false},
		"a tab indented code mutation":         {"text\n\n\tarr[0](old)\n", "text\n\n\tarr[0](new)\n", false},
		"an inline code span mutation":         {"run `[a](old.md)` now", "run `[a](new.md)` now", false},
		"a double-backtick inline code change": {"run ``[a](old.md)`` now", "run ``[a](new.md)`` now", false},
		"a link with inline code in its label": {"see [`code`](docs/old.md).", "see [`code`](docs/new.md).", true},
	} {
		t.Run(name, func(t *testing.T) {
			ok, why := LinksOnly(one(c.base), one(c.headText))
			if ok != c.ok {
				t.Fatalf("ok = %v (%s), want %v", ok, why, c.ok)
			}
			if ok && why != "" {
				t.Fatalf("why = %q on a links-only diff", why)
			}
		})
	}
	t.Run("files", func(t *testing.T) {
		if ok, _ := LinksOnly(map[string]string{"a.md": "keep", "b.md": "[x](old.md)"},
			map[string]string{"a.md": "keep", "b.md": "[x](new.md)"}); !ok {
			t.Fatal("an unchanged file beside a retarget is links-only")
		}
		if ok, _ := LinksOnly(map[string]string{"a.md": "x"}, map[string]string{"a.md": "x", "b.md": "[y](z.md)"}); ok {
			t.Fatal("an added file is not a link retarget")
		}
		if ok, _ := LinksOnly(nil, nil); !ok {
			t.Fatal("no changed file contradicts nothing")
		}
	})
}
