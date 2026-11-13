// SPDX-License-Identifier: Apache-2.0

package config

import (
	"maps"
	"slices"
)

// ApplyMergeDefaults fills a repository's merge from the Factory's mergeDefaults, key by key (Q8,
// plan PR6): a top-level key the repository omits is taken whole from the defaults, and a key it
// sets wins whole, so a partial breaker replaces the default breaker and a list is never unioned.
// It returns a new map, and the keys taken from the defaults, sorted; merge is never mutated.
func ApplyMergeDefaults(defaults, merge map[string]any) (map[string]any, []string) {
	if defaults == nil {
		return merge, nil
	}
	out := maps.Clone(merge)
	if out == nil {
		out = map[string]any{}
	}
	var taken []string
	for _, k := range slices.Sorted(maps.Keys(defaults)) {
		if _, ok := out[k]; !ok {
			out[k] = defaults[k]
			taken = append(taken, k)
		}
	}
	return out, taken
}
