// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// Q8, plan PR6: the Factory's merge defaults fill a repository's merge key by key, each key whole.
func TestApplyMergeDefaults(t *testing.T) {
	defaults := map[string]any{
		"policyBotLogin": "x[bot]",
		"breaker":        map[string]any{"window": 10, "maxReverts": 1},
		"requiredChecks": []any{"lint"},
	}
	merge := map[string]any{"breaker": map[string]any{"window": 5}, "autoMergesPerDay": 2}
	before := map[string]any{"breaker": map[string]any{"window": 5}, "autoMergesPerDay": 2}

	got, keys := ApplyMergeDefaults(defaults, merge)
	want := map[string]any{"policyBotLogin": "x[bot]", "breaker": map[string]any{"window": 5}, "requiredChecks": []any{"lint"}, "autoMergesPerDay": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !slices.Equal(keys, []string{"policyBotLogin", "requiredChecks"}) {
		t.Fatalf("defaulted keys %q", keys)
	}
	if !reflect.DeepEqual(merge, before) {
		t.Fatalf("merge was mutated: %v", merge)
	}
	// A deep copy: writing into the result reaches neither input.
	got["breaker"].(map[string]any)["window"] = 99
	got["requiredChecks"].([]any)[0] = "changed"
	if merge["breaker"].(map[string]any)["window"] != 5 || defaults["requiredChecks"].([]any)[0] != "lint" {
		t.Fatal("the result shares a map or a list with an input")
	}

	got, keys = ApplyMergeDefaults(nil, merge)
	if !reflect.DeepEqual(got, before) || len(keys) != 0 {
		t.Fatalf("nil defaults: %v, %q", got, keys)
	}
	got, keys = ApplyMergeDefaults(defaults, nil)
	if !reflect.DeepEqual(got, defaults) || len(keys) != 3 {
		t.Fatalf("no merge: %v, %q", got, keys)
	}
}

// MR9: merge.maxPerDay is the name a policy's cap already has; autoMergesPerDay still loads.
func TestMergePerDay(t *testing.T) {
	for name, tc := range map[string]struct {
		m    Merge
		want int
	}{
		"maxPerDay":        {Merge{MaxPerDay: 2}, 2},
		"autoMergesPerDay": {Merge{AutoMergesPerDay: 3}, 3},
		"both, equal":      {Merge{MaxPerDay: 4, AutoMergesPerDay: 4}, 4},
		"neither":          {Merge{}, 0},
	} {
		if got := tc.m.PerDay(); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	r := Repository{Name: "o/r", Merge: Merge{MaxPerDay: 2, AutoMergesPerDay: 3}}
	var got []string
	r.Merge.validatePerDay(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) })
	if !slices.Equal(got, []string{"merge.maxPerDay and merge.autoMergesPerDay differ: set maxPerDay alone"}) {
		t.Fatalf("both, differing: %q", got)
	}
	for _, m := range []Merge{{MaxPerDay: -1}, {AutoMergesPerDay: -1}} {
		got = nil
		m.validatePerDay(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) })
		if len(got) != 1 {
			t.Fatalf("negative %+v: %q", m, got)
		}
	}
}

// A repository that sets either name of the daily cap takes neither from the defaults, so a
// renamed key never meets the old one's default.
func TestMergeDefaultsPerDayIsOneKey(t *testing.T) {
	for _, tc := range []struct {
		defaults, merge map[string]any
		taken           []string
	}{
		{map[string]any{"autoMergesPerDay": 5}, map[string]any{"maxPerDay": 2}, nil},
		{map[string]any{"maxPerDay": 5}, map[string]any{"autoMergesPerDay": 2}, nil},
		{map[string]any{"maxPerDay": 5}, map[string]any{}, []string{"maxPerDay"}},
	} {
		got, keys := ApplyMergeDefaults(tc.defaults, tc.merge)
		if !slices.Equal(keys, tc.taken) || len(got) != max(len(tc.merge), len(tc.taken)) {
			t.Errorf("%v over %v: %v, taken %q", tc.defaults, tc.merge, got, keys)
		}
	}
}
