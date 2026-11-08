// SPDX-License-Identifier: Apache-2.0

package decision

import (
	"strings"
	"testing"
)

func TestTheMarkerIsOneLine(t *testing.T) {
	got := Marker("abc", "docs-links", "shadow", "shadow_would_arm", Rows{Files: 1, MaxFiles: 3, Lines: 2, MaxLines: 20, Counted: true, Gated: true,
		Shape: "ok", Holdout: "n/a", Oracle: "n/a", Age: "n/a", Evidence: "ok", Reviewer: "glm-5.3"})
	want := "<!-- agent-factory-decision head=abc class=docs-links decision=shadow:shadow_would_arm files=1/3 lines=2/20 shape=ok holdout=n/a oracle=n/a age=n/a evidence=ok paused=false capped=false reviewer=glm-5.3 -->"
	if got != want {
		t.Fatalf("%q\nwant %q", got, want)
	}
}

// Ruling R-m7: a refusal leaves its later rows unread, and the marker prints them as "-", so
// every field stays one token a tally can split on. Counts of a diff not read are unread too:
// the caps print, never a count of 0 nobody took.
func TestAnUnreadRowIsADash(t *testing.T) {
	got := Marker("abc", "", "human", "head_unreported", Rows{MaxFiles: 3, MaxLines: 20, Reviewer: "none"})
	want := "<!-- agent-factory-decision head=abc class=- decision=human:head_unreported files=-/3 lines=-/20 shape=- holdout=- oracle=- age=- evidence=- paused=- capped=- reviewer=none -->"
	if got != want {
		t.Fatalf("%q\nwant %q", got, want)
	}
}

// A diff read with no change is a count of 0, not an unread one.
func TestACountedEmptyDiffIsZero(t *testing.T) {
	if got := Marker("abc", "c", "human", "r", Rows{Counted: true}); !strings.Contains(got, " files=0/0 lines=0/0 ") {
		t.Fatal(got)
	}
}

// A decision that stopped before the breaker and the day's count were read prints them unread,
// not as a false nobody took; once read, a true prints as one.
func TestUnreadPausedAndCappedAreDashes(t *testing.T) {
	if got := Marker("abc", "c", "human", "policy_pending", Rows{}); !strings.Contains(got, " paused=- capped=- ") {
		t.Fatal(got)
	}
	if got := Marker("abc", "c", "shadow", "shadow_would_arm", Rows{Gated: true, Paused: true}); !strings.Contains(got, " paused=true capped=false ") {
		t.Fatal(got)
	}
}

// Dual evaluation (merge policy stage 3) binds the decision to the repository policy it was
// compared with, and says whether the two agreed. Off, the marker is as it was.
func TestTheDualFieldsPrintOnlyWhenRead(t *testing.T) {
	if got := Marker("abc", "c", "human", "r", Rows{}); strings.Contains(got, "policy=") || strings.Contains(got, "dual=") {
		t.Fatal(got)
	}
	got := Marker("abc", "c", "human", "r", Rows{Reviewer: "none", Policy: "0123456789ab", Dual: "agree"})
	if !strings.HasSuffix(got, " policy=0123456789ab dual=agree reviewer=none -->") {
		t.Fatal(got)
	}
	if got := Marker("abc", "c", "human", "r", Rows{Dual: "no_policy"}); !strings.Contains(got, " policy=- dual=no_policy ") {
		t.Fatal(got)
	}
}

// Repository authority (merge policy stage 4) binds the decision to the policy that made it,
// with nothing compared: policy= alone, before reviewer.
func TestThePolicyPrintsAloneUnderRepositoryAuthority(t *testing.T) {
	got := Marker("abc", "c", "arm", "", Rows{Reviewer: "none", Policy: "0123456789ab"})
	if !strings.HasSuffix(got, " capped=- policy=0123456789ab reviewer=none -->") || strings.Contains(got, "dual=") {
		t.Fatal(got)
	}
}
