// SPDX-License-Identifier: Apache-2.0

package githubapps

import (
	"flag"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/hardening"
)

var update = flag.Bool("update", false, "rewrite the permission tables in docs/github-apps.md from the data")

// enforcedSubsets is every subset of the output types the role table reacts to.
func enforcedSubsets() [][]hardening.OutputType {
	types := []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels, hardening.Issue, hardening.Push}
	out := [][]hardening.OutputType{}
	for mask := range 1 << len(types) {
		var s []hardening.OutputType
		for i, t := range types {
			if mask&(1<<i) != 0 {
				s = append(s, t)
			}
		}
		out = append(out, s)
	}
	return out
}

// D12: the agents App's required set is every permission the role table may mint, over every role
// and every enforced subset, the publisher included.
func TestAgentsAppsIsTheRoleTablesUnion(t *testing.T) {
	want := Permissions{}
	for _, role := range minter.RoleNames() {
		for _, enforced := range enforcedSubsets() {
			p, err := minter.Roles.For(role, enforced)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range p {
				if want[k] != "write" {
					want[k] = v
				}
			}
		}
	}
	if got := Agents(); !maps.Equal(got, want) {
		t.Fatalf("Agents() = %v, want the table's union %v", got, want)
	}
	hr12 := Permissions{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "actions": "read", "metadata": "read"}
	if !maps.Equal(Agents(), hr12) {
		t.Errorf("Agents() under HR12 = %v, want %v", Agents(), hr12)
	}
}

func TestFactoryAppsPermissions(t *testing.T) {
	for name, tc := range map[string]struct {
		opts MergerOptions
		want Permissions
	}{
		"folded, every option": {MergerOptions{Folded: true, Reruns: true, Labels: true, Status: true},
			Permissions{"contents": "write", "issues": "write", "pull_requests": "write", "statuses": "write", "checks": "read", "actions": "write", "metadata": "read"}},
		"folded, no option": {MergerOptions{Folded: true},
			Permissions{"contents": "write", "issues": "write", "pull_requests": "write", "statuses": "read", "checks": "read", "metadata": "read"}},
		"a separate merger": {MergerOptions{Reruns: true, Labels: true, Status: true},
			Permissions{"contents": "read", "issues": "write", "pull_requests": "write", "metadata": "read"}},
	} {
		if got := Factory(tc.opts); !maps.Equal(got, tc.want) {
			t.Errorf("%s: Factory = %v, want %v", name, got, tc.want)
		}
	}
	if got, want := Merger(MergerOptions{Status: true}), (Permissions{"contents": "write", "pull_requests": "write", "statuses": "write", "checks": "read", "metadata": "read"}); !maps.Equal(got, want) {
		t.Errorf("Merger = %v, want %v", got, want)
	}
}

// D12: no App holds workflows or administration, and the agents App never holds statuses.
func TestNoAppHoldsWorkflowsOrAdministration(t *testing.T) {
	all := MergerOptions{Folded: true, Reruns: true, Labels: true, Status: true}
	for name, p := range map[string]Permissions{"agents": Agents(), "factory": Factory(all), "merger": Merger(all)} {
		for _, k := range []string{"workflows", "administration"} {
			if _, ok := p[k]; ok {
				t.Errorf("the %s App holds %s", name, k)
			}
		}
	}
	if _, ok := Agents()["statuses"]; ok {
		t.Error("the agents App holds statuses: an agent could post maku/merge-gate")
	}
}

const docsPath = "../../docs/github-apps.md"

// TestDocs: the permission tables in docs/github-apps.md are Render of the data.
func TestDocs(t *testing.T) {
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, table := range DocTables() {
		begin, end := "<!-- "+table.Name+":begin -->\n", "<!-- "+table.Name+":end -->"
		head, rest, ok := strings.Cut(doc, begin)
		if !ok {
			t.Fatalf("docs/github-apps.md has no %s marker", begin)
		}
		got, tail, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("docs/github-apps.md has no %s marker", end)
		}
		want := Render(table.Permissions)
		if got == want {
			continue
		}
		if !*update {
			t.Fatalf("docs/github-apps.md's %s table differs from the data; rerun with -update:\n%s", table.Name, want)
		}
		doc = head + begin + want + end + tail
	}
	if *update {
		if err := os.WriteFile(docsPath, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderIsSorted(t *testing.T) {
	got := Render(Permissions{"metadata": "read", "contents": "write"})
	if want := "| Permission | Access |\n|---|---|\n| `contents` | write |\n| `metadata` | read |\n"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
	if names := slices.Collect(maps.Keys(Agents())); len(names) == 0 {
		t.Fatal("Agents() is empty")
	}
}
