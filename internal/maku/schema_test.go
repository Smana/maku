// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"testing"

	"github.com/Smana/maku/internal/schemacheck"
)

// Strict providers (Moonshot, OpenAI strict mode, Gemini) refuse a tool whose schema has a node
// without a type, and then every run fails on its first turn. Every local tool's schema is held
// to it.
func TestEveryLocalToolSchemaNodeNamesItsType(t *testing.T) {
	if len(localEntries) < 6 {
		t.Fatalf("only %d local tools: the registry changed shape", len(localEntries))
	}
	for _, e := range localEntries {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("local entry is %T, want a map", e)
		}
		schema, _ := json.Marshal(entry["inputSchema"])
		if got := schemacheck.Untyped(schema); len(got) > 0 {
			t.Errorf("%v: nodes without a type: %v", entry["name"], got)
		}
	}
}
