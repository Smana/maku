// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"testing"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/schemacheck"
)

// Strict providers (Moonshot, OpenAI strict mode, Gemini) refuse a tool whose schema has a node
// without a type, and then every run fails on its first turn. Every input and output schema the
// /mcp list can carry is held to it.
func TestEveryMCPToolSchemaNodeNamesItsType(t *testing.T) {
	s := &Server{}
	tools := s.mcpTools(authn.Principal{})
	tools = append(tools, s.intakeTools()...)
	tools = append(tools, s.coordinationTool(), s.coordinationDraftTool())
	tools = append(tools, s.slackLinkTools()...)
	tools = append(tools, mcpTool{name: "run_create", schema: runCreateSchema})
	if len(tools) < 10 {
		t.Fatalf("only %d tools collected: the registry changed shape", len(tools))
	}
	for _, tl := range tools {
		if got := schemacheck.Untyped(tl.schema); len(got) > 0 {
			t.Errorf("%s input: nodes without a type: %v", tl.name, got)
		}
		if len(tl.output) > 0 {
			if got := schemacheck.Untyped(tl.output); len(got) > 0 {
				t.Errorf("%s output: nodes without a type: %v", tl.name, got)
			}
		}
	}
}
