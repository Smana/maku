// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/schemacheck"
)

// Strict providers (Moonshot, OpenAI strict mode, Gemini) refuse a tool whose schema has a node
// without a type, and then every run fails on its first turn. Every schema this package lists to
// a model, for every role, is held to it.
func TestEveryToolSchemaNodeNamesItsType(t *testing.T) {
	tools := RoomTools(&memLog{}, fakeRedactor{}, time.Now)
	tools = append(tools, OutputTools(&memLog{}, fakeRedactor{}, time.Now, OutputConfig{})...)
	tools = append(tools, ForgeReadTool(nil, hardening.Policy{}))
	if len(tools) < 10 {
		t.Fatalf("only %d tools collected: the registry changed shape", len(tools))
	}
	check := func(name string, schema json.RawMessage) {
		t.Helper()
		if got := schemacheck.Untyped(schema); len(got) > 0 {
			t.Errorf("%s: nodes without a type: %v", name, got)
		}
	}
	for _, tl := range tools {
		check(tl.Name, tl.InputSchema)
		for role, s := range tl.RoleSchemas {
			check(tl.Name+" ("+role+")", s)
		}
	}
}
