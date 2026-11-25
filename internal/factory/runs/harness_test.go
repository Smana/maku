// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/runwatch"
)

func TestBuildOmitsEmptyHarnessAndRoundTripsOpenCode(t *testing.T) {
	k := runwatch.DefaultKind
	base := Spec{
		RunID: "aaaaaaaa", Role: "implementer", Repository: "o/r", BaseRef: "main", Branch: "agent/aaaaaaaa",
		TaskText: "fix", Principal: PrincipalFactory, DataClass: "public", Model: "agent-default",
		MaxTokens: 1000, MaxMinutes: 30,
	}
	uEmpty := Build(k, base)
	specMap := uEmpty.Object["spec"].(map[string]any)
	if _, exists := specMap["harness"]; exists {
		t.Fatalf("empty Spec.Harness must be omitted from claim spec: %v", specMap)
	}

	withOC := base
	withOC.Harness = "opencode"
	if err := withOC.Validate(); err != nil {
		t.Fatalf("valid opencode spec rejected: %v", err)
	}
	uOC := Build(k, withOC)
	got, ok := FromUnstructured(k, uOC)
	if !ok || got.Harness != "opencode" {
		t.Fatalf("round-trip harness = %q (ok=%v), want opencode", got.Harness, ok)
	}

	bad := base
	bad.Harness = "other"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "harness") {
		t.Fatalf("invalid harness must fail Validate: %v", err)
	}
}
