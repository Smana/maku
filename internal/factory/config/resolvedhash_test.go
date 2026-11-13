// SPDX-License-Identifier: Apache-2.0

package config

import (
	"regexp"
	"testing"
)

// B13, plan PR11: a grant binds to the resolved class, its mode excluded.
func TestResolvedHash(t *testing.T) {
	cl := Class{AllowedFiles: []string{"^docs/.+$"}, HoldoutChecks: []string{"a", "b"}}
	live, shadow := cl, cl
	live.Live, shadow.Shadow = true, true
	h := ResolvedHash(live)
	if h != ResolvedHash(shadow) || h != ResolvedHash(cl) {
		t.Fatal("the mode changed the hash")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(h) {
		t.Fatalf("hash %q is not 64 lowercase hex digits", h)
	}
	if ShortHash(h) != h[:12] {
		t.Fatalf("short hash %q", ShortHash(h))
	}
	swapped := cl
	swapped.HoldoutChecks = []string{"b", "a"}
	if ResolvedHash(swapped) == h {
		t.Fatal("holdoutChecks are hashed as written, as Structural compares them")
	}
	if ResolvedHash(cl, cl) == h {
		t.Fatal("two variants hash as one")
	}
	if ShortHash("abc") != "abc" {
		t.Fatal("a short input is its own short hash")
	}
}
