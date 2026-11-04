// SPDX-License-Identifier: Apache-2.0

package entrydiff

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

var kb = config.Class{
	AllowedFiles:   []string{`^incidents/[^/]+\.md$`},
	CompanionFiles: []string{`^index\.md$`, `^log\.md$`},
}

const entryPath = "incidents/kube-pod-crashlooping-3f9a1c2e.md"

// runlore returns the shape of runlore-kb #163: the entry added, index.md +1/-0, log.md +4/-0.
func runlore() forge.Diff {
	entry := strings.Repeat("line\n", 48)
	return forge.Diff{
		Base: map[string]string{entryPath: "", "index.md": "# Index\n- a\n", "log.md": "l1\n"},
		Head: map[string]string{entryPath: entry, "index.md": "# Index\n- a\n- b\n", "log.md": "l1\nl2\nl3\nl4\nl5\n"},
		Files: []forge.FileStat{
			{Path: entryPath, Status: "added", Additions: 48},
			{Path: "index.md", Status: "modified", Additions: 1},
			{Path: "log.md", Status: "modified", Additions: 4},
		},
	}
}

func stat(d *forge.Diff, p string) *forge.FileStat {
	for i := range d.Files {
		if d.Files[i].Path == p {
			return &d.Files[i]
		}
	}
	panic(p)
}

func TestAdded(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(d *forge.Diff)
		want bool
	}{
		"as RunLore opens it": {func(d *forge.Diff) {}, true},
		"the entry alone": {func(d *forge.Diff) {
			d.Files = d.Files[:1]
		}, true},
		"index.md also deletes a line": {func(d *forge.Diff) {
			stat(d, "index.md").Deletions = 1
		}, false},
		"index.md rewrites a base line": {func(d *forge.Diff) {
			d.Base["index.md"] = "# Index\n- a\n- c\n"
			d.Head["index.md"] = "- c\n- a\n# Index\nx\n"
			stat(d, "index.md").Additions = 1
		}, false},
		"index.md rewrites a line, count kept": {func(d *forge.Diff) {
			d.Head["index.md"] = "# Index\n- z\n- b\n"
		}, false},
		"log.md gains 11 lines": {func(d *forge.Diff) {
			d.Head["log.md"] = "l1\n" + strings.Repeat("x\n", 11)
			stat(d, "log.md").Additions = 11
		}, false},
		"the stat understates the additions": {func(d *forge.Diff) {
			stat(d, "log.md").Additions = 2
		}, false},
		"index.md added, not modified": {func(d *forge.Diff) {
			d.Base["index.md"] = ""
			stat(d, "index.md").Status = "added"
		}, false},
		"README.md modified": {func(d *forge.Diff) {
			d.Base["README.md"], d.Head["README.md"] = "a\n", "a\nb\n"
			d.Files = append(d.Files, forge.FileStat{Path: "README.md", Status: "modified", Additions: 1})
		}, false},
		"a second added entry": {func(d *forge.Diff) {
			p := "incidents/other-00000000.md"
			d.Base[p], d.Head[p] = "", "x\n"
			d.Files = append(d.Files, forge.FileStat{Path: p, Status: "added", Additions: 1})
		}, false},
		"the entry modified": {func(d *forge.Diff) {
			d.Base[entryPath] = "old\n"
			stat(d, entryPath).Status = "modified"
		}, false},
		"the entry renamed": {func(d *forge.Diff) {
			stat(d, entryPath).Status = "renamed"
			stat(d, entryPath).Previous = "incidents/old.md"
		}, false},
		"the entry already on the base": {func(d *forge.Diff) {
			d.Base[entryPath] = "old\n"
		}, false},
		"the entry under notes/": {func(d *forge.Diff) {
			p := "notes/x.md"
			d.Base[p], d.Head[p] = "", d.Head[entryPath]
			stat(d, entryPath).Path = p
		}, false},
		"no added entry, companions only": {func(d *forge.Diff) {
			d.Files = d.Files[1:]
		}, false},
		"the entry empty": {func(d *forge.Diff) {
			d.Head[entryPath] = ""
		}, false},
		"the entry holds a bidi override": {func(d *forge.Diff) {
			d.Head[entryPath] = "a\u202eb\n"
		}, false},
		"the entry holds a zero-width space": {func(d *forge.Diff) {
			d.Head[entryPath] = "a\u200bb\n"
		}, false},
		"the entry holds NUL": {func(d *forge.Diff) {
			d.Head[entryPath] = "a\x00b\n"
		}, false},
		"the entry holds invalid UTF-8": {func(d *forge.Diff) {
			d.Head[entryPath] = "a\xffb\n"
		}, false},
		"the entry holds tabs": {func(d *forge.Diff) {
			d.Head[entryPath] = "a\tb\n"
		}, true},
		"an added log line holds a bidi override": {func(d *forge.Diff) {
			d.Head["log.md"] = "l1\nl2\nl3\nl4\nl\u202e5\n"
		}, false},
	} {
		d := runlore()
		c.edit(&d)
		got, why := Added(kb, d)
		if got != c.want || (!got && why == "") {
			t.Errorf("%s: %v %q, want %v", name, got, why, c.want)
		}
	}
}
