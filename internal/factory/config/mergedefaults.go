// SPDX-License-Identifier: Apache-2.0

package config

import (
	"maps"
	"slices"
)

// ApplyMergeDefaults fills a repository's merge from the Factory's mergeDefaults, key by key (Q8,
// plan PR6): a top-level key the repository omits is taken whole from the defaults, and a key it
// sets wins whole, so a partial breaker replaces the default breaker and a list is never unioned.
// It returns a deep copy, sharing no map or slice with either input, and the keys taken from the
// defaults, sorted.
func ApplyMergeDefaults(defaults, merge map[string]any) (map[string]any, []string) {
	out, _ := deepCopyValue(merge).(map[string]any)
	if defaults == nil {
		return out, nil
	}
	if out == nil {
		out = map[string]any{}
	}
	var taken []string
	for _, k := range slices.Sorted(maps.Keys(defaults)) {
		if _, ok := out[k]; !ok {
			out[k] = deepCopyValue(defaults[k])
			taken = append(taken, k)
		}
	}
	return out, taken
}

// deepCopyValue copies a decoded JSON or YAML value: maps and lists recursively, scalars as they are.
func deepCopyValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = deepCopyValue(e)
		}
		return out
	case []any:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = deepCopyValue(e)
		}
		return out
	}
	return v
}
