// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/mcpwire"
)

// untyped lists every schema node that constrains a value (enum, items, properties, pattern) without
// naming its type. JSON Schema allows it; stricter model providers do not: Moonshot refused a whole
// session's tool list over room_fork's egress items (gcp-0, 2026-10-27).
func untyped(node any, path string) []string {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	_, typed := m["type"]
	for _, k := range []string{"enum", "items", "properties", "pattern"} {
		if _, has := m[k]; has && !typed {
			out = append(out, fmt.Sprintf("%s: %s without type", path, k))
			break
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name, child := range props {
			out = append(out, untyped(child, path+".properties."+name)...)
		}
	}
	out = append(out, untyped(m["items"], path+".items")...)
	return out
}

func TestEveryToolSchemaNodeNamesItsType(t *testing.T) {
	// Both lists, with the planner on: a tool a deployment can switch on passes the same test as
	// the ones it always has. needs_list too: every tool's input and output alike.
	e := setup(t, mcpOn, needsOn, withSponsored(sponsoredP), withIntake(t, "session"), withCatalogue(&fakeFactoryCatalogue{}))
	for name, h := range map[string]http.Header{"a human's": viaRoomctl("dev"), "the App's": bearer("slack-app")} {
		_, out := rpc(t, e, h, "tools/list", nil)
		b, _ := json.Marshal(out["result"])
		var r struct {
			Tools []struct {
				Name         string
				InputSchema  any
				OutputSchema any
			}
		}
		if json.Unmarshal(b, &r) != nil || len(r.Tools) == 0 {
			t.Fatalf("%s: %s", name, string(b))
		}
		names := make([]string, 0, len(r.Tools))
		for _, tool := range r.Tools {
			names = append(names, tool.Name)
			for _, bad := range untyped(tool.InputSchema, tool.Name) {
				t.Errorf("%s: %s", name, bad)
			}
			for _, bad := range untyped(tool.OutputSchema, tool.Name+".output") {
				t.Errorf("%s: %s", name, bad)
			}
		}
		if !slices.Contains(names, ClassesList) {
			t.Errorf("%s list does not carry classes_list: %v", name, names)
		}
		if !slices.Contains(names, mcpwire.IntakePlan) || !slices.Contains(names, mcpwire.PlanCheck) {
			t.Errorf("%s list does not carry the planner's two tools: %v", name, names)
		}
	}
}

// jsonType is a decoded JSON value's schema type, as encoding/json decodes it: numbers are float64.
func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}

// schemaFaults reports where value does not satisfy schema, one level of composition deep plus items: the
// declared type of every property the schema names, the presence of every required name, object members
// and array items included. It is what makes an outputSchema a claim rather than a comment.
func schemaFaults(schema, value any, path string) []string {
	s, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	// "integer" is the one declared type encoding/json's float64 decodes to as "number".
	actual := jsonType(value)
	declared, hasType := s["type"].(string)
	compatible := declared == actual || (declared == "integer" && actual == "number")
	if hasType && !compatible {
		return []string{fmt.Sprintf("%s: %s, want %s", path, actual, declared)}
	}
	var faults []string
	switch v := value.(type) {
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, name := range req {
				if _, has := v[fmt.Sprint(name)]; !has {
					faults = append(faults, fmt.Sprintf("%s: missing required %q", path, name))
				}
			}
		}
		if props, ok := s["properties"].(map[string]any); ok {
			for name, sub := range props {
				if member, has := v[name]; has {
					faults = append(faults, schemaFaults(sub, member, path+"."+name)...)
				}
			}
		}
	case []any:
		if items, has := s["items"]; has {
			for i, element := range v {
				faults = append(faults, schemaFaults(items, element, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return faults
}
