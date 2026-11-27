// SPDX-License-Identifier: Apache-2.0

package taskid

import (
	"regexp"
	"testing"
	"time"
)

// Pinned values: the names are the dedup, so they must never change between releases.
func TestNamesArePinned(t *testing.T) {
	c2 := regexp.MustCompile(`^[a-z2-7]{8}$`)
	for key, want := range map[string]string{
		IssueKey("Smana/cloud-native-ref", 2112, 1):                           "3buqdlot",
		IssueKey("Smana/cloud-native-ref", 2112, 2):                           "5assac2n",
		SplitKey("3buqdlot", 1):                                               "c7uac3kk",
		RunloreKey("KubePodCrashLooping", "apps/xplane-image-gallery"):        "654i4tdu",
		ScheduleKey("link-rot", time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)): "z3rxclhf",
	} {
		got := Name(key)
		if got != want || !c2.MatchString(got) {
			t.Errorf("Name(%q) = %q, want %q", key, got, want)
		}
	}
}

// Run ids and brief nonces are random C2 ids: 40 bits, so two draws differ.
func TestRandomIsAC2Id(t *testing.T) {
	c2 := regexp.MustCompile(`^[a-z2-7]{8}$`)
	seen := map[string]bool{}
	for range 64 {
		id := Random()
		if !c2.MatchString(id) || seen[id] {
			t.Fatalf("%q (seen %v)", id, seen[id])
		}
		seen[id] = true
	}
}

// A coordination child's key is its own lineage's, never a splitter part's.
func TestCoordinationChildKey(t *testing.T) {
	k := CoordinationChildKey("p4zt6wce", "consumer", 1, 1)
	if k != "coordination:p4zt6wce/consumer:v1:gen1" || k == SplitKey("p4zt6wce", 1) || Name(k) == Name(CoordinationChildKey("p4zt6wce", "consumer", 1, 2)) {
		t.Fatalf("key %q", k)
	}
}

func TestRoutineKeys(t *testing.T) {
	c2 := regexp.MustCompile(`^[a-z2-7]{8}$`)
	slot := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	k1 := RoutineKey("acme/alpha", "dead-links", slot)
	k2 := RoutineKey("acme/beta", "dead-links", slot)
	if k1 != "routine:acme/alpha:dead-links:2026-11-26T03:00:00Z" {
		t.Fatalf("RoutineKey = %q", k1)
	}
	if Name(k1) == Name(k2) || !c2.MatchString(Name(k1)) || !c2.MatchString(Name(k2)) {
		t.Fatalf("two repos with same routine name must differ: %q vs %q", Name(k1), Name(k2))
	}
	mk := ManualRoutineKey("acme/alpha", "dead-links", "req-42")
	if mk != "routine:acme/alpha:dead-links:manual:req-42" {
		t.Fatalf("ManualRoutineKey = %q", mk)
	}
	if !c2.MatchString(Name(mk)) || Name(mk) == Name(k1) {
		t.Fatalf("ManualRoutineKey Name = %q", Name(mk))
	}
}
