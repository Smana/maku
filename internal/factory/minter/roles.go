// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/hardening"
)

// Permissions is an installation token's permission map, as GitHub's access_tokens call takes it:
// {"contents": "write"}.
type Permissions map[string]string

// Change sets Permission to To when any of When is among the run's enforced output types.
type Change struct {
	When       []hardening.OutputType
	Permission string
	To         string
}

// Rule is one role's permissions: Base, then each Change that applies, in order.
type Rule struct {
	Base    Permissions
	Changes []Change
}

// Table is a role's Rule by role name: implementer, tester, reviewer, triager and publisher. The
// scoper and integration runs mint as the triager.
type Table map[string]Rule

// Role names, in the order the docs table lists them.
const (
	Implementer = "implementer"
	Tester      = "tester"
	Reviewer    = "reviewer"
	Triager     = "triager"
	Publisher   = "publisher"
)

// RoleNames is every role a Table holds, in docs order.
func RoleNames() []string { return []string{Implementer, Tester, Reviewer, Triager, Publisher} }

// shared is every sandbox role's reads (R): issues, checks, workflow runs and metadata.
func shared(p Permissions) Permissions {
	out := Permissions{"issues": "read", "checks": "read", "actions": "read", "metadata": "read"}
	maps.Copy(out, p)
	return out
}

// publisher is the safe-output publisher's rule in both variants (hardening H2): it opens pull
// requests, comments, labels and issues for the run, and pushes once push is enforced (H3).
func publisher() Rule {
	return Rule{
		Base:    Permissions{"contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
		Changes: []Change{{When: []hardening.OutputType{hardening.Push}, Permission: "contents", To: "write"}},
	}
}

// TableHR12 is the hardening plan's HR12 table: a role loses each write the factory publishes for
// it once that output type is enforced, and the reviewer and the triager are read-only.
var TableHR12 = Table{
	Implementer: {
		Base: shared(Permissions{"contents": "write", "pull_requests": "write"}),
		Changes: []Change{
			{When: []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels}, Permission: "pull_requests", To: "read"},
			{When: []hardening.OutputType{hardening.Push}, Permission: "contents", To: "read"},
		},
	},
	Tester: {
		Base:    shared(Permissions{"contents": "write", "pull_requests": "read"}),
		Changes: []Change{{When: []hardening.OutputType{hardening.Push}, Permission: "contents", To: "read"}},
	},
	Reviewer:  {Base: shared(Permissions{"contents": "read", "pull_requests": "read"})},
	Triager:   {Base: shared(Permissions{"contents": "read", "pull_requests": "read"})},
	Publisher: publisher(),
}

// TableD10 is the v4 spec's D10 as written: enforcement changes no sandbox role, and the reviewer
// gets read plus review (pull_requests: write).
var TableD10 = Table{
	Implementer: {Base: shared(Permissions{"contents": "write", "pull_requests": "write"})},
	Tester:      {Base: shared(Permissions{"contents": "write", "pull_requests": "read"})},
	Reviewer:    {Base: shared(Permissions{"contents": "read", "pull_requests": "write"})},
	Triager:     {Base: shared(Permissions{"contents": "read", "pull_requests": "read"})},
	Publisher:   publisher(),
}

// Roles is the role table every run and publisher token is minted from.
// The owner's ruling on HR12 (hardening plan) sets this line; TableD10 is the spec's D10 as written.
var Roles = TableHR12

// For is role's permissions for a run whose repository enforces the given output types: the run's
// own agents.ogenki.io/safe-outputs annotation, written at CREATE, never the live config. A role
// absent from the table is an error, never a read token.
func (t Table) For(role string, enforced []hardening.OutputType) (Permissions, error) {
	r, ok := t[role]
	if !ok {
		return nil, fmt.Errorf("minter: no role %q in the role table", role)
	}
	out := maps.Clone(r.Base)
	for _, c := range r.Changes {
		if slices.ContainsFunc(c.When, func(w hardening.OutputType) bool { return slices.Contains(enforced, w) }) {
			out[c.Permission] = c.To
		}
	}
	return out, nil
}

// Render is t as the Markdown table docs/factory.md carries between its roles markers.
func Render(t Table) string {
	var b strings.Builder
	b.WriteString("| Role | Permissions | Once enforced |\n|---|---|---|\n")
	for _, role := range RoleNames() {
		r, ok := t[role]
		if !ok {
			continue
		}
		changes := make([]string, 0, len(r.Changes))
		for _, c := range r.Changes {
			when := make([]string, len(c.When))
			for i, w := range c.When {
				when[i] = "`" + string(w) + "`"
			}
			changes = append(changes, strings.Join(when, ", ")+": `"+c.Permission+": "+c.To+"`")
		}
		change := "none"
		if len(changes) > 0 {
			change = strings.Join(changes, "; ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", role, render(r.Base), change)
	}
	return b.String()
}

func render(p Permissions) string {
	keys := slices.Sorted(maps.Keys(p))
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = "`" + k + ": " + p[k] + "`"
	}
	return strings.Join(out, ", ")
}
