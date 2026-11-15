// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
)

func TestInboxFallsBackToMine(t *testing.T) {
	mine := repoRoom(roomID, "Smana/a")
	mine.Status.Phase = "Active"
	mine.Status.Task = &v1alpha1.TaskStatus{Phase: "Executing", Issue: 101, IssueAuthor: "dev"}

	other := repoRoom(roomB, "Smana/a")
	other.Status.Phase = "Active"
	other.Status.Task = &v1alpha1.TaskStatus{Phase: "Executing", Issue: 102, IssueAuthor: "bob"}

	w := world(nil)
	roomsOpt := func(s *Server, _ *fanout.Hub, v *hubView) {
		s.Rooms = roomClient(t, mine, other)
		s.PublicURL = "https://rooms.test"
		w.gate(s)
		v.evs = append(v.evs,
			needsFacts(roomID, 1, envelope.TaskFacts{Phase: "Executing"}),
			needsFacts(roomB, 1, envelope.TaskFacts{Phase: "Executing"}),
		)
	}

	t.Run("needs off falls back to rooms_list mine", func(t *testing.T) {
		e := setup(t, mcpOn, roomsOpt)
		want := call(t, e, viaRoomctl("dev"), "rooms_list", map[string]any{"mine": true})
		got := call(t, e, viaRoomctl("dev"), "inbox", map[string]any{})
		if got.isError {
			t.Fatalf("inbox failed: %s", got.text)
		}
		if got.text != want.text {
			t.Fatalf("inbox text mismatch:\ngot:\n%s\nwant:\n%s", got.text, want.text)
		}
		if !strings.Contains(got.text, "#101") || strings.Contains(got.text, "#102") {
			t.Fatalf("expected only caller's #101 room in text:\n%s", got.text)
		}
		var env needsBody
		if err := json.Unmarshal(got.structured, &env); err != nil {
			t.Fatalf("unmarshal structured: %v (%s)", err, got.structured)
		}
		if env.APIVersion != needsVersion || len(env.Items) != 0 {
			t.Fatalf("unexpected envelope: %+v", env)
		}
		if _, err := time.Parse(time.RFC3339, env.Cursor); err != nil {
			t.Fatalf("bad cursor %q: %v", env.Cursor, err)
		}
	})

	t.Run("needs on with empty needs falls back to rooms_list mine", func(t *testing.T) {
		e := setup(t, mcpOn, needsOn, roomsOpt)
		want := call(t, e, viaRoomctl("dev"), "rooms_list", map[string]any{"mine": true})
		got := call(t, e, viaRoomctl("dev"), "inbox", map[string]any{})
		if got.isError {
			t.Fatalf("inbox failed: %s", got.text)
		}
		if got.text != want.text {
			t.Fatalf("inbox text mismatch:\ngot:\n%s\nwant:\n%s", got.text, want.text)
		}
		if !strings.Contains(got.text, "#101") || strings.Contains(got.text, "#102") {
			t.Fatalf("expected only caller's #101 room in text:\n%s", got.text)
		}
	})

	t.Run("needs on with active needs returns needs/v1 items", func(t *testing.T) {
		e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
		got := call(t, e, viaRoomctl("dev"), "inbox", map[string]any{})
		if got.isError {
			t.Fatalf("inbox failed: %s", got.text)
		}
		if keys := needKeysOf(t, got); !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238", "3kq7x2ma/merge/2239"}) {
			t.Fatalf("keys = %v", keys)
		}
		if !strings.Contains(got.text, "needs you") {
			t.Fatalf("expected needs text, got:\n%s", got.text)
		}
	})

	t.Run("sponsored caller narrows by room even when s.Needs is false", func(t *testing.T) {
		e := setup(t, mcpOn, withSponsored(sponsoredP), sponsoredMergeRoom(t))
		got := call(t, e, bearer("slack-app"), "inbox", map[string]any{"room": roomID})
		if got.isError {
			t.Fatalf("sponsored inbox failed: %s", got.text)
		}
		if keys := needKeysOf(t, got); !slices.Equal(keys, []string{"3kq7x2ma/merge/2239"}) {
			t.Fatalf("sponsored keys = %v", keys)
		}
	})
}
