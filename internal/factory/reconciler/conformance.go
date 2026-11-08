// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

// conformance is the versioned decision fixtures (MP16), the cases the settings editor's
// simulation decides a proposed policy on (merge policy stage 6) with the gate's own evaluator.
//
//go:embed testdata/conformance/*.json
var conformance embed.FS

// ConformanceCases are the embedded fixtures, in name order, each set to repository.
func ConformanceCases(repository string) ([]Case, error) {
	names, err := fs.Glob(conformance, "testdata/conformance/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Case, 0, len(names))
	for _, n := range names {
		raw, err := conformance.ReadFile(n)
		if err != nil {
			return nil, err
		}
		c, err := ReadCase(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		c.Inputs.Task.Spec.Repository = repository
		out = append(out, c)
	}
	return out, nil
}
