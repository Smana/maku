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
