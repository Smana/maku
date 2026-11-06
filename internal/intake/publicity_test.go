// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

// The cache is the repoaccess one: one lookup per repository per TTL, names read as GitHub reads
// them, and a failure is never cached — an answer only GitHub gave is the only answer worth keeping.
func TestPublicityCachesForTTL(t *testing.T) {
	now := time.Unix(0, 0)
	calls := map[string]int{}
	fail := false
	p := intake.NewPublicity(func(_ context.Context, repository string) (bool, error) {
		calls[repository]++
		if fail {
			return false, errors.New("github: 502")
		}
		return repository == "smana/ai-native-ref", nil
	}, 5*time.Minute, func() time.Time { return now })

	for i := range 2 {
		if public, err := p.Public(t.Context(), "Smana/ai-native-ref"); !public || err != nil {
			t.Fatalf("read %d: %v %v", i, public, err)
		}
	}
	if calls["smana/ai-native-ref"] != 1 {
		t.Fatalf("two reads inside the TTL asked GitHub %d times", calls["smana/ai-native-ref"])
	}
	// Another case is the same repository, and the same entry.
	if public, err := p.Public(t.Context(), "smana/AI-NATIVE-REF"); !public || err != nil {
		t.Fatalf("another case: %v %v", public, err)
	}
	if calls["smana/ai-native-ref"] != 1 {
		t.Fatalf("a spelling the cache did not recognise asked GitHub again: %d", calls["smana/ai-native-ref"])
	}
	// Past the TTL the answer is read again: a repository that goes private is refused once the
	// cached answer is old, not for as long as the process lives.
	now = now.Add(5 * time.Minute)
	if _, err := p.Public(t.Context(), "Smana/ai-native-ref"); err != nil {
		t.Fatal(err)
	}
	if calls["smana/ai-native-ref"] != 2 {
		t.Fatalf("a read past the TTL asked GitHub %d times", calls["smana/ai-native-ref"])
	}
	// A failure is never cached: the caller fails closed, twice, and GitHub is asked twice.
	fail = true
	for i := range 2 {
		if public, err := p.Public(t.Context(), "smana/other"); public || err == nil {
			t.Fatalf("read %d of a failing lookup: %v %v", i, public, err)
		}
	}
	if calls["smana/other"] != 2 {
		t.Fatalf("a failure was cached: %d lookups for two reads", calls["smana/other"])
	}
}

// A repository that is not `owner/name` is not one GitHub answers for: it is an error and no
// lookup, so a malformed ask costs nothing and reaches nobody.
func TestPublicityAsksNothingForAMalformedRepository(t *testing.T) {
	asked := 0
	p := intake.NewPublicity(func(context.Context, string) (bool, error) { asked++; return true, nil },
		time.Minute, time.Now)
	for _, repository := range []string{"", "   ", "Smana", "/ai-native-ref", "Smana/", "/"} {
		if public, err := p.Public(t.Context(), repository); public || err == nil {
			t.Errorf("%q: %v %v", repository, public, err)
		}
	}
	if asked != 0 {
		t.Fatalf("GitHub asked %d times about nothing", asked)
	}
}

// The cache is bounded: a long-lived broker asks about as many repositories as it sees, and a map
// that only grows is a memory leak in a process that also answers rooms.
func TestPublicityCacheIsBounded(t *testing.T) {
	p := intake.NewPublicity(func(context.Context, string) (bool, error) { return true, nil },
		time.Minute, time.Now)
	intake.SetPublicityMaxEntries(p, 2)
	for _, repository := range []string{"a/one", "a/two", "a/three"} {
		if _, err := p.Public(t.Context(), repository); err != nil {
			t.Fatal(err)
		}
	}
	if got := intake.PublicityEntries(p); got != 2 {
		t.Fatalf("%d entries in a cache capped at 2", got)
	}
}
