// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// applyMergeDefaults applies the file's mergeDefaults to each repositories entry's merge (plan PR6).
// It returns j unchanged when mergeDefaults is unset, else the defaulted JSON and, per repository
// in order, the keys taken. Numbers are kept as written, so an integer never round-trips through a float.
func applyMergeDefaults(j []byte) ([]byte, [][]string, error) {
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, nil, err
	}
	defaults, ok := m["mergeDefaults"].(map[string]any)
	if !ok {
		return j, nil, nil
	}
	var taken [][]string
	fill := func(entry map[string]any) error {
		merge, ok := entry["merge"].(map[string]any)
		if !ok && entry["merge"] != nil {
			return fmt.Errorf("merge is not a map")
		}
		out, keys := ApplyMergeDefaults(defaults, merge)
		entry["merge"] = out
		taken = append(taken, keys)
		return nil
	}
	list, _ := m["repositories"].([]any)
	for _, e := range list {
		if entry, ok := e.(map[string]any); ok {
			if err := fill(entry); err != nil {
				return nil, nil, fmt.Errorf("repositories %v: %w", entry["name"], err)
			}
		}
	}
	out, err := json.Marshal(m)
	return out, taken, err
}
