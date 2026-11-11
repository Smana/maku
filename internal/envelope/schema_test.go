// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"testing"

	"github.com/Smana/maku/internal/schemacheck"
)

// A strict provider (Moonshot, OpenAI strict mode, Gemini) refuses a tool whose schema has a node
// without a type: scope.state once had only an enum, and every triager run failed on turn one.
func TestScopeAndSplitSchemasTypeEveryNode(t *testing.T) {
	for name, s := range map[string]string{"ScopeSchema": ScopeSchema, "SplitSchema": SplitSchema} {
		if got := schemacheck.Untyped([]byte(s)); len(got) > 0 {
			t.Errorf("%s: nodes without a type: %v", name, got)
		}
	}
}
