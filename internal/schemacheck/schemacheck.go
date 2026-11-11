// SPDX-License-Identifier: Apache-2.0

// Package schemacheck holds the rule every JSON schema Maku hands a model must meet: strict
// providers (Moonshot, OpenAI strict mode, Gemini) refuse a tool whose schema has a node without
// a type, and refuse the whole request, so every run fails on its first turn.
package schemacheck

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Untyped returns the JSON pointer of every schema node in schema that names no type: a node needs
// "type" (a name or a list of names), or an anyOf or oneOf whose every branch names one. The nodes
// walked are the root, each property, items, prefixItems, a schema-valued additionalProperties,
// $defs and the allOf, anyOf and oneOf branches. An unparseable schema is reported as a node.
func Untyped(schema []byte) []string {
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		return []string{fmt.Sprintf("<unparseable: %v>", err)}
	}
	var out []string
	walk(root, "", &out)
	sort.Strings(out)
	return out
}

func walk(n any, path string, out *[]string) {
	m, ok := n.(map[string]any)
	if !ok {
		*out = append(*out, pointer(path))
		return
	}
	if !typed(m) {
		*out = append(*out, pointer(path))
	}
	for _, k := range []string{"properties", "$defs", "patternProperties"} {
		if sub, ok := m[k].(map[string]any); ok {
			for name, v := range sub {
				walk(v, path+"/"+k+"/"+name, out)
			}
		}
	}
	for _, k := range []string{"items", "additionalProperties"} {
		if sub, ok := m[k].(map[string]any); ok {
			walk(sub, path+"/"+k, out)
		}
	}
	for _, k := range []string{"prefixItems", "allOf", "anyOf", "oneOf"} {
		if list, ok := m[k].([]any); ok {
			for i, v := range list {
				walk(v, fmt.Sprintf("%s/%s/%d", path, k, i), out)
			}
		}
	}
}

// typed is whether the node names its type itself, or through branches that each do.
func typed(m map[string]any) bool {
	switch t := m["type"].(type) {
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		if list, ok := m[k].([]any); ok && len(list) > 0 {
			all := true
			for _, b := range list {
				bm, ok := b.(map[string]any)
				all = all && ok && typed(bm)
			}
			if all {
				return true
			}
		}
	}
	return false
}

func pointer(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}
