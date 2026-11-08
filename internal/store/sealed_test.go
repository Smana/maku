// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// SealedSince lists the rooms sealed at or after since, the last closed first, at most limit:
// the rooms list's closed view, whose Room objects the factory deleted.
func TestSealedSince(t *testing.T) {
	s, _, _, _ := open(t)
	ctx := context.Background()
	for _, id := range []string{"a2a2a2a2", "b3b3b3b3", "c4c4c4c4"} {
		if _, err := s.EnsureRoom(ctx, NewRoom{ID: id, Driver: "system:factory", Retention: 90 * 24 * time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a2a2a2a2", "c4c4c4c4"} { // b3b3b3b3 and the fixture's room stay open
		if err := s.CloseRoom(ctx, id, "done"); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(since time.Time, limit int) []string {
		t.Helper()
		got, err := s.SealedSince(ctx, since, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, st := range got {
			if !st.Sealed || st.ClosedAt == nil {
				t.Fatalf("%s: want sealed with a closed_at, got %+v", st.ID, st)
			}
			out = append(out, st.ID)
		}
		return out
	}
	hour := time.Hour
	if got := ids(time.Now().Add(-hour), 10); !slices.Equal(got, []string{"c4c4c4c4", "a2a2a2a2"}) {
		t.Fatalf("the last closed first, the open ones never: got %v", got)
	}
	if got := ids(time.Now().Add(-hour), 1); !slices.Equal(got, []string{"c4c4c4c4"}) {
		t.Fatalf("limit 1: got %v", got)
	}
	if got := ids(time.Now().Add(hour), 10); len(got) != 0 {
		t.Fatalf("since after every close: got %v", got)
	}
}
