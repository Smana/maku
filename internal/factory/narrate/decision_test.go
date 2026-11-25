// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
)

func TestDecisionDetailsRendersMeasurementsAndEveryRefusal(t *testing.T) {
	rows := decision.Rows{Counted: true, Files: 2, MaxFiles: 5, Lines: 131, MaxLines: 100, Reviewer: "glm-5.3",
		Considered: "docs:patch_too_large,docs-links:class_mismatch"}
	classes := map[string]config.Class{
		"docs":       {MaxPatchFiles: 5, MaxModifiedLines: 100},
		"docs-links": {DiffShape: "links-only"},
	}
	got := DecisionDetails(rows, classes)
	for _, want := range []string{"2 of 5 files", "131 of 100 lines", "reviewer approved", "`docs`: 131 lines changed, limit 100", "`docs-links`: not a links-only diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestMeasurementsLeavesUnreadValuesOut(t *testing.T) {
	if got := Measurements(decision.Rows{Reviewer: "none", Considered: "docs:patch_too_large"}); got != "" {
		t.Fatalf("unread diff rendered as measured: %q", got)
	}
}
