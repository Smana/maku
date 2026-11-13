// SPDX-License-Identifier: Apache-2.0

// Package githubapps is each GitHub App's required permission set, as data (v4 D12): the agents App
// mints the runs' and the publisher's tokens, the factory App handles issues, labels, statuses and
// merges, and the merger may be a third App or fold into the factory App. Tokens are minted
// downscoped from these sets; an installation must grant them. `maku install` (rollout step 7)
// creates the Apps from them, and the factory checks each installation against them at start.
package githubapps

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
)

// Permissions is a permission name to its access, read or write.
type Permissions = minter.Permissions

// MergerOptions says what the merger asks for, as forge.MergerPermissions does, and whether it is
// folded into the factory App (github.mergerAppIDFile equal to github.appIDFile).
type MergerOptions struct {
	Folded bool // the merger signs with the factory App
	Reruns bool // merge.checkReruns on any repository: actions write
	Labels bool // a live or monitored class: issues write
	Status bool // merge.gateStatus on any repository: statuses write
}

// Agents is the agents App's required set: every permission the role table may mint, over every
// role and every enforced combination, the publisher included. It never holds statuses, workflows
// or administration (the role table's invariants).
func Agents() Permissions { return minter.Union(minter.Roles) }

// Factory is the factory App's required set: its own token's scope, and the merger's when the
// merger is folded into it.
func Factory(o MergerOptions) Permissions {
	p := Permissions(forge.FactoryPermissions())
	if o.Folded {
		p = union(p, Merger(o))
	}
	return p
}

// Merger is the merger App's required set (R16), whichever App holds it.
func Merger(o MergerOptions) Permissions {
	return Permissions(forge.MergerPermissions(o.Reruns, o.Labels, o.Status))
}

// union is a and b, write over read.
func union(a, b Permissions) Permissions {
	out := maps.Clone(a)
	for k, v := range b {
		if out[k] != "write" {
			out[k] = v
		}
	}
	return out
}

// DocTable is one permission table docs/github-apps.md carries between its markers.
type DocTable struct {
	Name        string
	Permissions Permissions
}

// DocTables are the documented sets: the agents App, the factory App with the merger folded in and
// every merger option on, the factory App alone, and a separate merger App with every option on.
func DocTables() []DocTable {
	all := MergerOptions{Reruns: true, Labels: true, Status: true}
	folded := all
	folded.Folded = true
	return []DocTable{
		{"apps:agents", Agents()},
		{"apps:factory-folded", Factory(folded)},
		{"apps:factory", Factory(MergerOptions{})},
		{"apps:merger", Merger(all)},
	}
}

// Render is p as a Markdown table, sorted by permission.
func Render(p Permissions) string {
	var b strings.Builder
	b.WriteString("| Permission | Access |\n|---|---|\n")
	for _, k := range slices.Sorted(maps.Keys(p)) {
		fmt.Fprintf(&b, "| `%s` | %s |\n", k, p[k])
	}
	return b.String()
}
