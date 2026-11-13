// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// listShape is good in the repositories shape, its one entry handed to edit before it is written
// back: the shape Assemble writes.
func listShape(t *testing.T, base string, edit func(top, entry map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{}
	for _, k := range []string{"maintainers", "triggerLabel", "classes", "merge"} {
		entry[k] = m[k]
		delete(m, k)
	}
	entry["name"] = m["repository"]
	delete(m, "repository")
	api, _ := m["api"].(map[string]any)
	delete(api, "repositories")
	if edit != nil {
		edit(m, entry)
	}
	m["repositories"] = []any{entry}
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// PR7: the repository rules that name classes read the bound names, so a binding that did not
// resolve (its class absent) never refuses the repository.
func TestBoundNamesHoldTheRepositoryRules(t *testing.T) {
	bound := func(names ...string) func(top, e map[string]any) {
		return func(top, e map[string]any) {
			// A loops row and the label allowlist read the bound names too.
			top["loops"] = map[string]any{"dead-links": map[string]any{"stallSteps": 10}}
			e["classes"] = map[string]any{"docs-links": map[string]any{"shadow": true}, "revert": map[string]any{"shadow": true}}
			e["bound"] = toAnySlice(names)
			e["schedules"] = []any{map[string]any{"name": "dead-links-weekly", "cron": "0 6 * * 1", "class": "dead-links",
				"probe": "dead-links", "text": "Retarget the dead links the detector found."}}
		}
	}
	c, err := ParseAssembled(listShape(t, good, bound("docs-links", "revert", "dead-links")))
	if err != nil {
		t.Fatalf("a dropped dead-links must leave the repository served: %v", err)
	}
	if r := c.Repos()[0]; !slices.Equal(r.Bound, []string{"docs-links", "revert", "dead-links"}) {
		t.Fatalf("bound %v", r.Bound)
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"docs-links", "dead-links"}, "class revert is required (OD-8)"},
		{[]string{"docs-links", "revert"}, `schedule dead-links-weekly: class "dead-links" is review or a declared class`},
		{[]string{"docs-links", "revert"}, `loops.dead-links: "dead-links" is not default, review or a class a repository declares`},
	} {
		if _, err := ParseAssembled(listShape(t, good, bound(tc.names...))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("bound %v: %v, want %q", tc.names, err, tc.want)
		}
	}
}

// PR6, Q8: mergeDefaults fill a repository's merge key by key; a key the repository sets wins whole.
func TestMergeDefaultsApplyKeyByKey(t *testing.T) {
	raw := listShape(t, good, func(top, e map[string]any) {
		top["mergeDefaults"] = map[string]any{"policyBotLogin": "x[bot]", "breaker": map[string]any{"window": 10, "maxReverts": 1}}
		merge, _ := e["merge"].(map[string]any)
		delete(merge, "policyBotLogin")
		merge["breaker"] = map[string]any{"window": 5, "maxReverts": 2}
	})
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Repos()[0]
	if r.Merge.PolicyBotLogin != "x[bot]" || r.Merge.Breaker.Window != 5 || r.Merge.Breaker.MaxReverts != 2 {
		t.Fatalf("merge %+v", r.Merge)
	}
	if !slices.Equal(r.MergeDefaulted, []string{"policyBotLogin"}) {
		t.Fatalf("mergeDefaulted %v, want [policyBotLogin]", r.MergeDefaulted)
	}
	if c.MergeDefaults == nil || c.MergeDefaults.PolicyBotLogin != "x[bot]" {
		t.Fatalf("mergeDefaults %+v", c.MergeDefaults)
	}
	// Without mergeDefaults, nothing is defaulted and the hash is the file's own.
	plain, err := Parse(listShape(t, good, nil))
	if err != nil || plain.Repos()[0].MergeDefaulted != nil {
		t.Fatalf("no defaults: %v %v", err, plain.Repos()[0].MergeDefaulted)
	}
}

// Step 3's merger-fold rule reads the resolved merge: a mergerLogin set only in mergeDefaults counts.
func TestMergerFoldReadsResolvedMerge(t *testing.T) {
	folded := strings.Replace(good, "mergerAppIDFile: /etc/agent-factory-merger/app_id, mergerKeyFile: /etc/agent-factory-merger/private_key",
		"mergerAppIDFile: /etc/agent-factory-github/app_id, mergerKeyFile: /etc/agent-factory-github/private_key", 1)
	raw := listShape(t, folded, func(top, e map[string]any) {
		top["mergeDefaults"] = map[string]any{"mergerLogin": "ogenki-agent-factory[bot]"}
		delete(e["merge"].(map[string]any), "mergerLogin")
	})
	if _, err := Parse(raw); err != nil {
		t.Fatalf("a defaulted mergerLogin must satisfy the fold rule: %v", err)
	}
}

// bound and bindings are Assemble's: a config file that sets them is refused.
func TestConfigFileRefusesBound(t *testing.T) {
	for _, k := range []string{"bound", "bindings"} {
		raw := listShape(t, good, func(_, e map[string]any) {
			if k == "bound" {
				e[k] = []any{"docs-links", "revert"}
			} else {
				e[k] = []any{map[string]any{"name": "docs-links", "mode": "shadow"}}
			}
		})
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "set by the factory from Repository objects") {
			t.Errorf("%s in a config file: %v", k, err)
		}
	}
}

// B12: a bound class carries its own policy's generation; a legacy-served one the repository's
// MergePolicy's.
func TestClassGenerations(t *testing.T) {
	c := &Config{Generations: Generations{Repos: map[string]RepoGenerations{
		"smana/bound":  {Repository: 4, Classes: map[string]ClassGeneration{"dead-links": {Policy: 3, PolicyName: "dead-links"}}},
		"smana/legacy": {Repository: 2, MergePolicy: 7, PolicyName: "legacy"},
	}}}
	for _, tc := range []struct {
		name, class  string
		policy, rgen int64
		bound        bool
	}{
		{"Smana/Bound", "dead-links", 3, 4, true},
		{"Smana/legacy", "docs", 7, 2, false},
		{"Smana/none", "docs", 0, 0, false},
	} {
		p, r, b := c.ClassGenerations(tc.name, tc.class)
		if p != tc.policy || r != tc.rgen || b != tc.bound {
			t.Errorf("%s %s: (%d, %d, %v), want (%d, %d, %v)", tc.name, tc.class, p, r, b, tc.policy, tc.rgen, tc.bound)
		}
	}
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
