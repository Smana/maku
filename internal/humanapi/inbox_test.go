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
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/summary"
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

// Phase G5: inbox and /api/needs expose `type`, enforce one entry per task (its most urgent type),
// and drop stale needs for ended tasks.
func TestInboxOneEntryPerTaskWithTypesAndDropsStale(t *testing.T) {
	approval := &store.Approval{ID: "01J9A", RoomID: roomID, Class: "shell", State: "pending",
		RequestedAt: time.Date(2026, 11, 4, 19, 0, 0, 0, time.UTC), ExpiresAt: time.Now().Add(time.Hour)}
	doneApproval := &store.Approval{ID: "01OLD", RoomID: roomB, Class: "shell", State: "pending",
		RequestedAt: time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC), ExpiresAt: time.Now().Add(time.Hour)}
	w := world(nil)
	w.set(func() { w.writes = map[string][]string{"Smana/a": {"dev"}} })
	e := setup(t, mcpOn, needsOn, w.option(), func(s *Server, _ *fanout.Hub, v *hubView) {
		// roomID has both a merge (type=review) and an approval (type=blocked): only review stays.
		merge := repoRoom(roomID, "Smana/a")
		merge.Status.PendingApprovals = 1
		merge.Status.Task = &v1alpha1.TaskStatus{Phase: "AwaitingHuman", PR: 2239}
		// splitRoom has a split_proposed (type=plan).
		split := repoRoom(splitRoom, "Smana/a")
		split.Status.Task = &v1alpha1.TaskStatus{Phase: "Escalated", Issue: 2238}
		// roomB ended (Done) with a lingering approval: dropped completely.
		done := repoRoom(roomB, "Smana/a")
		done.Status.PendingApprovals = 1
		done.Status.Task = &v1alpha1.TaskStatus{Phase: "Done", Issue: 99}
		s.Rooms = roomClient(t, split, merge, done)
		s.PublicURL = "https://rooms.test"
		v.approvals = []store.Approval{*approval, *doneApproval}
		v.evs = append(v.evs,
			needsFacts(splitRoom, 1, envelope.TaskFacts{Phase: "Escalated", Reason: "split_proposed",
				Issue: &envelope.IssueFact{Number: 2238, URL: "https://github.com/Smana/a/issues/2238"}}),
			needsFacts(roomID, 11, envelope.TaskFacts{Phase: "AwaitingHuman",
				PR: &envelope.PRFact{Number: 2239, URL: "https://github.com/Smana/a/pull/2239"}}),
			needsFacts(roomB, 21, envelope.TaskFacts{Phase: "Done",
				Issue: &envelope.IssueFact{Number: 99, URL: "https://github.com/Smana/a/issues/99"}}),
		)
	})

	apiGot := mustNeeds(t, e, "", admin("dev"))
	wantKeys := []string{splitRoom + "/plan_confirmation/2238", roomID + "/merge/2239"}
	wantTypes := []string{summary.TypePlan, summary.TypeReview}
	if keys := needKeys(apiGot.Items); !slices.Equal(keys, wantKeys) {
		t.Fatalf("/api/needs keys = %v, want %v", keys, wantKeys)
	}
	for i, n := range apiGot.Items {
		if n.Type != wantTypes[i] {
			t.Fatalf("/api/needs item %d type = %q, want %q", i, n.Type, wantTypes[i])
		}
	}

	toolGot := call(t, e, viaRoomctl("dev"), "inbox", map[string]any{})
	if toolGot.isError {
		t.Fatalf("inbox error: %s", toolGot.text)
	}
	var env needsBody
	if err := json.Unmarshal(toolGot.structured, &env); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
	if keys := needKeys(env.Items); !slices.Equal(keys, wantKeys) {
		t.Fatalf("inbox keys = %v, want %v", keys, wantKeys)
	}
	for i, n := range env.Items {
		if n.Type != wantTypes[i] {
			t.Fatalf("inbox item %d type = %q, want %q", i, n.Type, wantTypes[i])
		}
	}
}
