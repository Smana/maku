// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"strings"
	"testing"
)

const (
	oldHead = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newHead = "a431a2f6bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// Revised says what a revision a maintainer asked for pushed, in the factory's own facts only: the
// new head, the files GitHub says it changed, the room, and the paths it took out of the task's scope.
func TestRevisedSaysWhatTheRevisionChanged(t *testing.T) {
	const room = "https://rooms.priv.aws.ogenki.io/"
	for name, c := range map[string]struct {
		rv       Revision
		want     []string
		unwanted []string
	}{
		"a pushed revision names its head and files": {
			rv: Revision{PR: 2738, Repository: "Smana/cloud-native-ref", From: oldHead, Head: newHead, FilesRead: true,
				Files: []string{"opentofu/AGENTS.md", "website/content/docs/reference/commands.md"}},
			want: []string{"#2738", "[`a431a2f`](https://github.com/Smana/cloud-native-ref/commit/" + newHead + ")",
				"`opentofu/AGENTS.md`", "`website/content/docs/reference/commands.md`",
				"https://rooms.priv.aws.ogenki.io/r/3buqdlot", "ready for the maintainer's re-review"},
			unwanted: []string{"Scope changed", "without a new commit"},
		},
		"a dropped task path is said plainly": {
			rv: Revision{PR: 2738, Repository: "Smana/cloud-native-ref", From: oldHead, Head: newHead, FilesRead: true,
				Files:   []string{"opentofu/AGENTS.md", "website/content/docs/reference/commands.md"},
				Dropped: []string{"opentofu/AGENTS.md"}},
			want: []string{"**Scope changed:**", "took `opentofu/AGENTS.md` out of the pull request",
				"names in its `## Files` section or its acceptance check", "confirm the scope change"},
			unwanted: []string{"ready for the maintainer's re-review"},
		},
		"no new commit is said instead of files": {
			rv:       Revision{PR: 2738, Repository: "Smana/cloud-native-ref", From: newHead, Head: newHead},
			want:     []string{"the revision ended without a new commit; read the room for why", "/r/3buqdlot"},
			unwanted: []string{"changing", "ready for the maintainer's re-review"},
		},
		"files GitHub would not list are said unread": {
			rv:       Revision{PR: 2738, Repository: "Smana/cloud-native-ref", From: oldHead, Head: newHead},
			want:     []string{"`a431a2f`", "could not be listed", "check the diff against the task's scope"},
			unwanted: []string{"Scope changed"},
		},
		"a path cannot break out of its code span": {
			rv: Revision{PR: 2738, Repository: "Smana/cloud-native-ref", From: oldHead, Head: newHead, FilesRead: true,
				Files: []string{"a`@everyone`.md"}},
			want:     []string{"`a'@everyone'.md`"},
			unwanted: []string{"`@everyone`"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := Revised(task(), c.rv, room)
			if e.Key != "revised-0" {
				t.Errorf("key %q", e.Key)
			}
			for _, w := range c.want {
				if !strings.Contains(e.Body, w) {
					t.Errorf("missing %q in\n%s", w, e.Body)
				}
			}
			for _, w := range c.unwanted {
				if strings.Contains(e.Body, w) {
					t.Errorf("unexpected %q in\n%s", w, e.Body)
				}
			}
		})
	}
}

// A long revision lists its first files and counts the rest: a comment stays readable.
func TestRevisedBoundsItsFileList(t *testing.T) {
	var files []string
	for i := range 25 {
		files = append(files, "docs/f"+string(rune('a'+i))+".md")
	}
	e := Revised(task(), Revision{PR: 1, Repository: "o/r", From: oldHead, Head: newHead, FilesRead: true, Files: files}, "https://x/")
	if !strings.Contains(e.Body, "`docs/ft.md`") || strings.Contains(e.Body, "`docs/fu.md`") || !strings.Contains(e.Body, "and 5 more") {
		t.Fatalf("%s", e.Body)
	}
}
