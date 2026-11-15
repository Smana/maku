// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// planFixture is a golden plan/v1, decoded through encoding/json from the planner package's own
// testdata: the plan these tests hand issue_file is the plan plan_check's own tests use, so a
// change to plan/v1 breaks one place.
func planFixture(t *testing.T, name string) intake.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "intake", "testdata", "plan", name+".json"))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var p intake.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}
