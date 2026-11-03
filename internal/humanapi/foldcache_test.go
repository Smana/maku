// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

func TestFoldCache(t *testing.T) {
	t.Run("past its bound it evicts the room read longest ago", func(t *testing.T) {
		c := &foldCache{max: 2}
		c.put("aaaaaaaa", "", summary.State{LastSeq: 1})
		c.put("bbbbbbbb", "", summary.State{LastSeq: 2})
		if _, ok := c.get("aaaaaaaa", ""); !ok {
			t.Fatal("a room within the bound was evicted")
		}
		c.put("cccccccc", "", summary.State{LastSeq: 3}) // bbbbbbbb is now the one read longest ago
		for id, want := range map[string]bool{"aaaaaaaa": true, "bbbbbbbb": false, "cccccccc": true} {
			if _, ok := c.get(id, ""); ok != want {
				t.Errorf("%s kept = %v, want %v", id, ok, want)
			}
		}
	})
	t.Run("a fold that went less far does not replace the kept one", func(t *testing.T) {
		c := &foldCache{}
		c.put(roomID, "", summary.State{LastSeq: 9})
		c.put(roomID, "", summary.State{LastSeq: 5}) // a slower request, racing the first
		if st, _ := c.get(roomID, ""); st.LastSeq != 9 {
			t.Fatalf("lastSeq %d, want 9", st.LastSeq)
		}
	})
	t.Run("a reader's fold is its own", func(t *testing.T) {
		c := &foldCache{}
		c.put(roomID, "", summary.Fold([]envelope.Event{logEvent(1, envelope.ApprovalRequested,
			envelope.Actor{Kind: envelope.ActorSystem, ID: "system:policy"},
			envelope.ApprovalRequestedPayload{ApprovalID: "01M4A", CallID: "c1", Class: "forge.pr",
				Action: envelope.Must(map[string]string{"command": "push"})})}))
		mine, _ := c.get(roomID, "")
		delete(mine.Pending, "01M4A")
		if again, _ := c.get(roomID, ""); len(again.Pending) != 1 {
			t.Fatalf("a reader's change reached the cache: %+v", again.Pending)
		}
	})
	t.Run("a room of another identity is a miss and takes the slot, whatever its seq", func(t *testing.T) {
		c := &foldCache{}
		c.put(roomID, "uid-1", summary.State{LastSeq: 9})
		if _, ok := c.get(roomID, "uid-2"); ok {
			t.Fatal("a re-created room read the old room's fold")
		}
		c.put(roomID, "uid-2", summary.State{LastSeq: 2})
		if st, ok := c.get(roomID, "uid-2"); !ok || st.LastSeq != 2 {
			t.Fatalf("the new room's fold was not kept: %v %d", ok, st.LastSeq)
		}
		if _, ok := c.get(roomID, "uid-1"); ok {
			t.Fatal("the old room's fold outlived its replacement")
		}
	})
}
