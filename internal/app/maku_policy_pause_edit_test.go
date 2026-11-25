// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"strings"
	"testing"
)

// diffLines is the lines of b not in a and of a not in b, in order: enough to say an edit touched
// nothing else.
func diffLines(a, b string) (added, removed []string) {
	count := map[string]int{}
	for _, l := range strings.Split(a, "\n") {
		count[l]++
	}
	for _, l := range strings.Split(b, "\n") {
		if count[l] > 0 {
			count[l]--
			continue
		}
		added = append(added, l)
	}
	for l, n := range count {
		for range n {
			removed = append(removed, l)
		}
	}
	return added, removed
}

// A real manifest (ai-native-ref's, with its comments): pause adds one line, resume takes it out
// again, and nothing else moves.
func TestSetMergePausedTouchesOnlyPaused(t *testing.T) {
	raw, err := os.ReadFile("testdata/pause-repository.yaml")
	if err != nil {
		t.Fatal(err)
	}
	paused, err := setMergePaused(raw, true, `drill: "quoted" reason`)
	if err != nil {
		t.Fatal(err)
	}
	added, removed := diffLines(string(raw), string(paused))
	if len(removed) != 0 || len(added) != 1 || !strings.Contains(added[0], `paused: {reason: "drill: \"quoted\" reason"}`) {
		t.Fatalf("pause: added %q removed %q", added, removed)
	}
	again, err := setMergePaused(paused, true, "other")
	if err != nil {
		t.Fatal(err)
	}
	if added, removed := diffLines(string(paused), string(again)); len(added) != 1 || len(removed) != 1 {
		t.Fatalf("re-pause: added %q removed %q", added, removed)
	}
	resumed, err := setMergePaused(again, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(resumed) != string(raw) {
		t.Fatalf("resume did not restore the file:\n%s", resumed)
	}
}

func TestSetMergePausedShapes(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		pause          bool
	}{
		{"no merge, pause", "kind: Repository\nspec:\n  repository: a/b # keep\n", "kind: Repository\nspec:\n  merge:\n    paused: {reason: \"r\"}\n  repository: a/b # keep\n", true},
		{"no merge, resume", "kind: Repository\nspec:\n  repository: a/b\n", "kind: Repository\nspec:\n  repository: a/b\n", false},
		{"block paused, resume", "kind: Repository\nspec:\n  merge:\n    paused:\n      reason: old\n    maxPerDay: 2\n  repository: a/b\n", "kind: Repository\nspec:\n  merge:\n    maxPerDay: 2\n  repository: a/b\n", false},
		{"paused last in file", "kind: Repository\nspec:\n  repository: a/b\n  merge:\n    maxPerDay: 2\n    paused: {reason: old}\n", "kind: Repository\nspec:\n  repository: a/b\n  merge:\n    maxPerDay: 2\n    paused: {reason: \"r\"}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := setMergePaused([]byte(tc.in), tc.pause, "r")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestSetMergePausedRefusesFlowMerge(t *testing.T) {
	if _, err := setMergePaused([]byte("kind: Repository\nspec:\n  repository: a/b\n  merge: {maxPerDay: 2}\n"), true, "r"); err == nil {
		t.Fatal("a flow-style merge was edited")
	}
}
