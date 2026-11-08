// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"testing"
)

// The editor simulates on exactly the fixtures the conformance test reads: every one, each on the
// repository asked about.
func TestConformanceCasesAreTheFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "conformance", "*.json"))
	cs, err := ConformanceCases("Smana/x")
	if err != nil || len(cs) != len(files) || len(cs) == 0 {
		t.Fatalf("%d cases, %d files: %v", len(cs), len(files), err)
	}
	for _, c := range cs {
		if c.Inputs.Task.Spec.Repository != "Smana/x" {
			t.Fatal(c.Name)
		}
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal(err)
	}
}
