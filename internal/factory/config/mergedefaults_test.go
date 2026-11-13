// SPDX-License-Identifier: Apache-2.0

package config

import (
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

	got, keys = ApplyMergeDefaults(nil, merge)
	if !reflect.DeepEqual(got, before) || len(keys) != 0 {
		t.Fatalf("nil defaults: %v, %q", got, keys)
	}
	got, keys = ApplyMergeDefaults(defaults, nil)
	if !reflect.DeepEqual(got, defaults) || len(keys) != 3 {
		t.Fatalf("no merge: %v, %q", got, keys)
	}
}
