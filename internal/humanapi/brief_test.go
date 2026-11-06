// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/maku"
)

// briefBody is GET /api/rooms/{id}/brief's answer, as a client reads it (CD11): the brief's own envelope,
// the one room_brief serves too.
type briefBody struct {
	APIVersion string `json:"apiVersion"`
	Room       string `json:"room"`
	Text       string `json:"text"`
	Cursor     string `json:"cursor"`
}

// briefAt is GET /api/rooms/{id}/brief+q for h: the answer, and the status it came with.
func briefAt(t *testing.T, e env, q string, h http.Header) (briefBody, int) {
	t.Helper()
	code, body := get(t, e, "/api/rooms/"+roomID+"/brief"+q, h)
	var got briefBody
	if code == http.StatusOK && json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("GET /api/rooms/%s/brief%s: %d %s", roomID, q, code, body)
	}
	return got, code
}

// scopedHandoffs is a room whose log holds the scoper's handoff, then a later handoff that carries none,
// then padding events (SC6): the scope is early and the log grew since, so a brief that reads one fixed
// tail of a resumed room cannot see it at all.
func scopedHandoffs(padding int) option {
	return func(_ *Server, _ *fanout.Hub, v *hubView) {
		now := time.Now()
		v.mu.Lock()
		defer v.mu.Unlock()
		seq := int64(len(v.evs))
		v.evs = append(v.evs,
			envelope.Event{V: 1, Seq: seq + 1, RoomID: roomID, Type: envelope.Handoff, TS: now.Add(-2 * time.Hour),
				Payload: envelope.Must(envelope.HandoffPayload{FromRole: "triager", ToRole: "implementer", Summary: "scoped",
					Scope: &envelope.Scope{State: "ready", Task: "the install link points at a moved page",
						Touches: []string{"README.md"}, Plan: []string{"read where the link points today"},
						Acceptance: []envelope.Acceptance{{Check: "go test ./internal/roomview/", Proves: "the renderer still passes"}}}})},
			envelope.Event{V: 1, Seq: seq + 2, RoomID: roomID, Type: envelope.Handoff, TS: now.Add(-2 * time.Hour),
				Payload: envelope.Must(envelope.HandoffPayload{FromRole: "implementer", ToRole: "reviewer", Summary: "done"})})
		seq += 2
		for range padding {
			seq++
			v.evs = append(v.evs, envelope.Event{V: 1, Seq: seq, RoomID: roomID, Type: envelope.Message, TS: now.Add(-time.Hour),
				Payload: []byte(`{"kind":"chat","text":"x","delivery":"none"}`)})
		}
	}
}

// room_brief returns the brief and the cursor the caller passes back next time, as room_changes does.
func TestRoomBrief(t *testing.T) {
	e := setup(t, mcpOn, worldWithSplitAndMerge(t).gate)
	got := call(t, e, viaRoomctl("dev"), "room_brief", map[string]any{"room": roomID})
	if !strings.HasPrefix(got.text, "**Resume: "+roomID) {
		t.Fatalf("%s", got.text)
	}
	var data struct{ Cursor string }
	if err := json.Unmarshal([]byte(got.structured), &data); err != nil || !maku.IsCursor(data.Cursor) {
		t.Fatalf("%q %v", data.Cursor, err)
	}
}

// CD11: the route and the tool are one brief, the cursor it hands back is the next call's `since`, and a
// `since` that is neither a cursor nor an instant is refused rather than read as everything or as nothing.
func TestBriefRoute(t *testing.T) {
	e := setup(t, worldWithSplitAndMerge(t).gate)
	got, code := briefAt(t, e, "", header("dev"))
	if code != http.StatusOK || got.APIVersion != "brief/v1" || got.Room != roomID ||
		!strings.HasPrefix(got.Text, "**Resume: "+roomID) || !maku.IsCursor(got.Cursor) {
		t.Fatalf("%d %+v", code, got)
	}
	next, code := briefAt(t, e, "?since="+got.Cursor, header("dev"))
	if code != http.StatusOK || strings.Contains(next.Text, "\n- ") {
		t.Fatalf("the cursor as since: %d\n%s", code, next.Text)
	}
	if code, _ := get(t, e, "/api/rooms/"+roomID+"/brief", header("")); code != http.StatusUnauthorized {
		t.Errorf("no caller: %d", code)
	}
	if code, _ := get(t, e, "/api/rooms/aaaaaaaa/brief", header("dev")); code != http.StatusNotFound {
		t.Errorf("a missing room: %d", code)
	}
	for _, q := range []string{"?since=yesterday", "?since=2026-11-05", "?since=seq:", "?since=seq:x", "?since=2999-01-01T00:00:00Z"} {
		code, body := get(t, e, "/api/rooms/"+roomID+"/brief"+q, header("dev"))
		if code != http.StatusBadRequest || !strings.Contains(body, "seq:N") || !strings.Contains(body, "RFC 3339") {
			t.Errorf("%s: %d %s", q, code, body)
		}
	}
}

// A sponsored token on the route is refused like every other human route.
func TestBriefRouteRefusesASponsoredToken(t *testing.T) {
	e := setup(t, withSponsored(sponsoredP))
	if code, _ := get(t, e, "/api/rooms/"+roomID+"/brief", bearer("slack-app")); code != http.StatusUnauthorized {
		t.Fatalf("%d", code)
	}
}

// CD11 with SC6: the brief quotes the scope the task ran under — its acceptance checks and the plan line —
// from the newest handoff that carries one, so a resumed room says what "done" means.
func TestBriefScope(t *testing.T) {
	e := setup(t, mcpOn, scopedHandoffs(0))
	got := call(t, e, viaRoomctl("dev"), "room_brief", map[string]any{"room": roomID})
	for _, want := range []string{"Acceptance: `go test ./internal/roomview/`", "Plan: scope ready"} {
		if !strings.Contains(got.text, want) {
			t.Errorf("no %q in\n%s", want, got.text)
		}
	}
}

// The scope is found however long the room's log has grown since: the scoper's handoff lands early and a
// resumed room is the one the brief is for, so the search widens past one tail rather than losing it.
func TestBriefScopeSurvivesALongLog(t *testing.T) {
	e := setup(t, mcpOn, scopedHandoffs(600))
	got := call(t, e, viaRoomctl("dev"), "room_brief", map[string]any{"room": roomID})
	if !strings.Contains(got.text, "Acceptance: `go test ./internal/roomview/`") {
		t.Fatalf("the scope was lost past one page:\n%s", got.text)
	}
}

// CD11: room_brief declares its answer, as rooms_list and needs_list do: the envelope the route serves,
// field for field, and the structuredContent the tool gives satisfies it.
func TestRoomBriefDeclaresItsOutput(t *testing.T) {
	e := setup(t, mcpOn, worldWithSplitAndMerge(t).gate)
	schema, ok := toolEntry(t, e, viaRoomctl("dev"), "room_brief")["outputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("room_brief declares no outputSchema")
	}
	got := call(t, e, viaRoomctl("dev"), "room_brief", map[string]any{"room": roomID})
	if got.isError || got.structured == nil {
		t.Fatalf("%+v", got)
	}
	var served any
	if err := json.Unmarshal(got.structured, &served); err != nil {
		t.Fatalf("%s: %v", got.structured, err)
	}
	for _, fault := range schemaFaults(schema, served, "structuredContent") {
		t.Errorf("%s", fault)
	}
}
