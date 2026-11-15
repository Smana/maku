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
	"github.com/Smana/maku/internal/summary"
)

// briefBody is GET /api/rooms/{id}/brief's answer, as a client reads it (CD11).
type briefBody struct {
	APIVersion string `json:"apiVersion"`
	Room       string `json:"room"`
	Text       string `json:"text"`
	Cursor     string `json:"cursor"`
}

type briefQuestionBody struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

type briefQuestionAnswer struct {
	Text     string             `json:"text"`
	Question *briefQuestionBody `json:"question"`
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

func handoffScope(scope *envelope.Scope) option {
	return func(_ *Server, _ *fanout.Hub, v *hubView) {
		v.mu.Lock()
		defer v.mu.Unlock()
		seq := int64(len(v.evs)) + 1
		v.evs = append(v.evs, envelope.Event{V: 1, Seq: seq, RoomID: roomID, Type: envelope.Handoff, TS: time.Now(),
			Payload: envelope.Must(envelope.HandoffPayload{FromRole: "triager", ToRole: "implementer", Summary: "scoped", Scope: scope})})
	}
}

// forbiddenNote is the last note an agent leaves when the App's token cannot do what it tried: gh's own 403
// write, which the brief reads into the one fix line a person acts on (fast-follow 6).
func forbiddenNote() option {
	return func(_ *Server, _ *fanout.Hub, v *hubView) {
		v.mu.Lock()
		defer v.mu.Unlock()
		seq := int64(len(v.evs))
		v.evs = append(v.evs, envelope.Event{V: 1, Seq: seq + 1, RoomID: roomID, Type: envelope.Message,
			Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:cf4ato2x", Role: "implementer"},
			TS:    time.Now().Add(-time.Minute),
			Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindProgress, Delivery: envelope.DeliveryNone,
				Text: "gh: Resource not accessible by integration (HTTP 403)"})})
	}
}

// Fast-follow 6: a room whose last note is the App's own 403 carries the fix line, from the notes the handler
// already folded — the brief reads nothing new to say it.
func TestBriefNamesTheFixOfAPermission(t *testing.T) {
	e := setup(t, worldWithSplitAndMerge(t).gate, forbiddenNote())
	got, code := briefAt(t, e, "", header("dev"))
	if code != http.StatusOK {
		t.Fatalf("%d %+v", code, got)
	}
	if n := strings.Count(got.Text, "\nFix: "); n != 1 {
		t.Fatalf("%d Fix lines, want one:\n%s", n, got.Text)
	}
	if fix := summary.Fix(summary.CauseAppPermission); !strings.Contains(got.Text, fix) {
		t.Errorf("no %q in\n%s", fix, got.Text)
	}
}

// CD11: the route hands back a cursor that is the next call's `since`, and a `since` that is neither a
// cursor nor an instant is refused rather than read as everything or as nothing.
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
	e := setup(t, scopedHandoffs(0))
	got, code := briefAt(t, e, "", header("dev"))
	if code != http.StatusOK {
		t.Fatalf("%d %+v", code, got)
	}
	for _, want := range []string{"Acceptance: `go test ./internal/roomview/`", "Plan: scope ready"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("no %q in\n%s", want, got.Text)
		}
	}
}

// A scoper's question is structured data on the brief envelope, never a new line in the rendered brief.
func TestBriefQuestion(t *testing.T) {
	tests := []struct {
		name            string
		state           string
		assumptions     []string
		wantQuestion    bool
		wantRecommended string
	}{
		{name: "question with recommendation", state: envelope.ScopeQuestion, assumptions: []string{"task check"}, wantQuestion: true, wantRecommended: "task check"},
		{name: "ready omits question", state: envelope.ScopeReady},
		{name: "question without recommendation", state: envelope.ScopeQuestion, wantQuestion: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope := &envelope.Scope{State: tc.state, Task: "prove the resume brief", Question: "Which command proves this?",
				Assumptions: tc.assumptions, Acceptance: []envelope.Acceptance{{Check: "go test ./internal/humanapi", Proves: "the brief still passes"}}}
			e := setup(t, handoffScope(scope))
			code, body := get(t, e, "/api/rooms/"+roomID+"/brief", header("dev"))
			var route briefQuestionAnswer
			if code != http.StatusOK || json.Unmarshal([]byte(body), &route) != nil {
				t.Fatalf("HTTP brief: %d %s", code, body)
			}
			if !tc.wantQuestion {
				if route.Question != nil {
					t.Fatalf("HTTP question = %+v, want nil", route.Question)
				}
			} else if route.Question == nil || route.Question.Text != "Which command proves this?" || route.Question.Recommended != tc.wantRecommended {
				t.Fatalf("HTTP question = %+v", route.Question)
			}
			for _, unwanted := range []string{scope.Question, tc.wantRecommended} {
				if unwanted != "" && strings.Contains(route.Text, unwanted) {
					t.Errorf("rendered brief gained structured question text %q:\n%s", unwanted, route.Text)
				}
			}
			for _, want := range []string{"Acceptance: `go test ./internal/humanapi`", "Plan: scope " + tc.state} {
				if !strings.Contains(route.Text, want) {
					t.Errorf("rendered brief lost %q:\n%s", want, route.Text)
				}
			}
		})
	}
}

// The scope is found however long the room's log has grown since: the scoper's handoff lands early and a
// resumed room is the one the brief is for, so the search widens past one tail rather than losing it.
func TestBriefScopeSurvivesALongLog(t *testing.T) {
	e := setup(t, scopedHandoffs(600))
	got, code := briefAt(t, e, "", header("dev"))
	if code != http.StatusOK || !strings.Contains(got.Text, "Acceptance: `go test ./internal/roomview/`") {
		t.Fatalf("the scope was lost past one page: %d\n%s", code, got.Text)
	}
}
