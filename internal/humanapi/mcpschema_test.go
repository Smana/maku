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
		if !slices.Contains(names, mcpwire.Handoff) || !slices.Contains(names, mcpwire.Task) || !slices.Contains(names, mcpwire.Inbox) {
			t.Errorf("%s list does not carry handoff, task and inbox: %v", name, names)
		}
	}
}
