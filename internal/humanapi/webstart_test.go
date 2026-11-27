// SPDX-License-Identifier: Apache-2.0

package humanapi

import "testing"

// Developer review gap 1: the web Start follows the catalogue's starter flag, so a writer where
// access.start: write is in effect gets Start, and a caller who may not start is refused before an
// issue the factory would refuse in public. No catalogue leaves the factory to decide.
func TestWebStartFollowsTheStarterFlag(t *testing.T) {
	yes, no := true, false
	for name, tc := range map[string]struct {
		starter *bool
		want    bool
	}{"a starter": {&yes, true}, "not a starter": {&no, false}, "no catalogue": {nil, true}} {
		got := webStartable(planChecked{CanStart: true, Starter: tc.starter})
		if got.CanStart != tc.want || (!tc.want && got.CanStartWhy != startNotMaintainer) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}
