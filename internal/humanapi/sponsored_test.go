// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/summary"
)

var sponsoredP = authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "t",
	Expiry: time.Now().Add(24 * time.Hour)}

// sponsoredTokens is the broker's sponsored authenticator in the tests: a bearer names its principal.
type sponsoredTokens map[string]authn.Principal

func (m sponsoredTokens) Authenticate(_ context.Context, raw string) (authn.Principal, error) {
	if p, ok := m[raw]; ok {
		return p, nil
	}
	return authn.Principal{}, authn.ErrUnauthenticated
}

// withSponsored declares ps on the broker; each one's bearer is its name after system:.
func withSponsored(ps ...authn.Principal) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		m := sponsoredTokens{}
		for _, p := range ps {
			m[strings.TrimPrefix(p.ID, "system:")] = p
		}
		s.Sponsored = m
	}
}

// bearer is a sponsored principal's request headers.
func bearer(name string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+name)
	return h
}

// sponsoredRooms serves room A (roomID) with system:slack-app a watcher and room B without it, on
// w's GitHub, whose clock counts every access check.
func sponsoredRooms(t *testing.T, w *gitHubWorld) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		mine := repoRoom(roomID, "Smana/a")
		mine.Spec.Members = []v1alpha1.Member{{Principal: "system:slack-app", Role: "watcher"}}
		s.Rooms = roomClient(t, mine, repoRoom(roomB, "Smana/b"))
		w.gate(s)
	}
}

// toolNames is tools/list's names, in order, as h sees them.
func toolNames(t *testing.T, e env, h http.Header) string {
	t.Helper()
	_, out := rpc(t, e, h, "tools/list", nil)
	var res struct{ Tools []struct{ Name string } }
	b, _ := json.Marshal(out["result"])
	_ = json.Unmarshal(b, &res)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ",")
}

// R13, invariant 8: a sponsored principal's tools read, and none labels, approves, steers, queues,
// forks or stops; a call to a human's tool is an unknown tool (C2).
func TestSponsoredToolList(t *testing.T) {
	// human.intake.plan on: the planner's two tools join both lists, the App's last and the human's
	// after issue_draft (PR 2): intake_plan, then plan_check, the door a consumer re-validates through.
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "slack"))
	if got := toolNames(t, e, bearer("slack-app")); got != "run_create,issue_draft,room_summary,room_tail,room_changes,intake_plan,plan_check" {
		t.Fatalf("%s", got)
	}
	if got := toolNames(t, e, viaRoomctl("dev")); got != "rooms_list,room_summary,room_tail,room_changes,room_queue,room_fork,issue_draft,needs_list,intake_plan,plan_check" {
		t.Fatalf("a human's list changed: %s", got)
	}
	for _, name := range []string{"room_queue", "room_fork", "rooms_list"} {
		_, out := rpc(t, e, bearer("slack-app"), "tools/call", map[string]any{"name": name,
			"arguments": map[string]any{"room": roomID, "text": "x", "at": 1}})
		if rpcCode(out) != -32602 || !strings.Contains(fmt.Sprint(out["error"]), "unknown tool: "+name) {
			t.Errorf("%s: %v", name, out)
		}
	}
}

// R13: a sponsored principal reads a room only as its member, never through GitHub, never another.
func TestSponsoredReadsItsRoomsOnly(t *testing.T) {
	w := world(nil)
	e := setup(t, mcpOn, withSponsored(sponsoredP), sponsoredRooms(t, w))
	if got := call(t, e, bearer("slack-app"), "room_summary", map[string]any{"room": roomID}); got.isError {
		t.Fatalf("its own room: %s", got.text)
	}
	if got := call(t, e, bearer("slack-app"), "room_summary", map[string]any{"room": roomB}); !got.isError || !strings.Contains(got.text, noSuchRoom) {
		t.Fatalf("another room: %+v", got)
	}
	if n := w.checked(); n != 0 {
		t.Fatalf("a sponsored read asked GitHub %d times", n)
	}
}

// R13, C18: a sponsored token is unauthenticated everywhere but /mcp and the App's decide route
// (v3.x Slack push approval D2), and on /mcp while no principal is declared: principal() runs the
// human authenticator, which never accepts it.
func TestSponsoredTokenElsewhere(t *testing.T) {
	var decisions []decided
	e := setup(t, mcpOn, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
	for _, path := range []string{"/api/rooms", "/api/rooms/" + roomID + "/summary", "/v1/ws?room=" + roomID} {
		if code, _ := get(t, e, path, bearer("slack-app")); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", path, code)
		}
	}
	// D2: the one route it does reach, and its answer is a closed code rather than a 401.
	if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusOK || result != "approved" {
		t.Fatalf("the App's own route: %d %s, want 200 approved", got, result)
	}
	// A human's token on that route is the sponsored authenticator's 403, and no code: the App is
	// the only principal this path names.
	body, err := json.Marshal(press(slackTeam, "U02", ap1, slackBranch, "approved"))
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := postDecide(t, e, header("own"), string(body)); code != http.StatusForbidden || strings.Contains(raw, `"result"`) {
		t.Fatalf("a human on the App's route: %d %q", code, raw)
	}
	off := setup(t, mcpOn)
	if code, _ := rpc(t, off, bearer("slack-app"), "tools/list", nil); code != http.StatusUnauthorized {
		t.Fatalf("no principal declared: %d", code)
	}
}

// D2: the decide route is the Slack App's alone. Another sponsored principal and a human's
// token, web or maku, are refused before the body is read, with no code the App could act on.
func TestSlackDecideIsTheAppsOnly(t *testing.T) {
	var decisions []decided
	e := setup(t, withSponsored(sponsoredP, otherBot), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
	body := `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD","messageTS":"1730822400.123456",` +
		`"approvalId":"` + ap1 + `","branch":"` + slackBranch + `","decision":"approved"}`
	for name, h := range map[string]http.Header{
		"another sponsored principal": bearer("other-bot"),
		"a human's web session":       header("own"),
		"a human's maku token":        viaRoomctl("own"),
	} {
		code, out := postDecide(t, e, h, body)
		if code != http.StatusForbidden || strings.Contains(out, `"result"`) {
			t.Errorf("%s: %d %q", name, code, out)
		}
	}
	if len(decisions) != 0 {
		t.Fatalf("a principal that is not the App decided: %+v", decisions)
	}
	if code, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); code != http.StatusOK || result != "approved" {
		t.Fatalf("the control: %d %s", code, result)
	}
}

// C28: the instructions are built per principal: a sponsored one is not told of a developer's
// room page, maku or labels, and a human's text is unchanged.
func TestSponsoredInstructions(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP))
	text := func(h http.Header) string {
		_, out := rpc(t, e, h, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
		return fmt.Sprint(out["result"].(map[string]any)["instructions"])
	}
	human, sponsored := text(viaRoomctl("dev")), text(bearer("slack-app"))
	if human != mcpInstructions || sponsored == human || strings.Contains(sponsored, "maku") ||
		!strings.Contains(sponsored, "untrusted: true") {
		t.Fatalf("human %q, sponsored %q", human, sponsored)
	}
}

// R13: a sponsored principal reads; the summary holds no action and nothing that needs it, however
// the room stands, since `Allowed` grants it Read only (I1).
func TestSponsoredSummaryAdvertisesNothing(t *testing.T) {
	w := world(nil)
	e := setup(t, mcpOn, withSponsored(sponsoredP), sponsoredRooms(t, w))
	got := call(t, e, bearer("slack-app"), "room_summary", map[string]any{"room": roomID})
	var s struct {
		NeedsYou []json.RawMessage
		Actions  []json.RawMessage
	}
	if got.isError || json.Unmarshal(got.structured, &s) != nil || len(s.Actions) != 0 || len(s.NeedsYou) != 0 {
		t.Fatalf("%s", got.structured)
	}
}

// R13 with CD21 and B1: one sponsored principal, one room, and one answer on both surfaces. A pull
// request whose reviewers name someone else would be a merge — or a review — for a caller GitHub lets
// write; a sponsored principal has no login and only reads, so the room's rows and its room_summary both
// list no need. The rows' derivation (needsFor) leaves summary.Caller.RoomScoped to PR 3's needs_list,
// whose room-scoped read is its own contract: the two legacy surfaces say what View says.
func TestSponsoredRowsAndSummaryAgreeOnNoNeeds(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), func(s *Server, _ *fanout.Hub, v *hubView) {
		mine := repoRoom(roomID, "Smana/a")
		mine.Spec.Members = []v1alpha1.Member{{Principal: "system:slack-app", Role: "watcher"}}
		mine.Status.Task = &v1alpha1.TaskStatus{Phase: "AwaitingHuman", PR: 2239, PRReviewers: []string{"dev1"}}
		s.Rooms = roomClient(t, mine)
		v.evs = append(v.evs, envelope.Event{V: 1, Seq: 11, RoomID: roomID, Type: envelope.StateChanged, TS: time.Now(),
			Payload: envelope.TaskStatePayload(envelope.TaskFacts{Phase: "AwaitingHuman",
				PR: &envelope.PRFact{Number: 2239, URL: "https://github.com/Smana/a/pull/2239"}})})
	})
	// The room's row, as the list computes it: a sponsored bearer reaches no /api/rooms route (C18), so the
	// list's own function is called with the principal the sponsored tools authenticate.
	rows, _, err := e.srv.roomRows(t.Context(), sponsoredP, &roomFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v, %v", rows, err)
	}
	if r := rows[0]; r.NeedsMe || r.NeedsYou != 0 || r.Need != nil || r.Step != nil {
		t.Fatalf("a sponsored row lists %+v", r)
	}
	// The room's summary, as the sponsored tool reads it.
	got := call(t, e, bearer("slack-app"), "room_summary", map[string]any{"room": roomID})
	var s struct {
		NeedsYou []json.RawMessage
		Step     *summary.Step
	}
	if got.isError || json.Unmarshal(got.structured, &s) != nil || len(s.NeedsYou) != 0 || s.Step != nil {
		t.Fatalf("the summary: %s", got.structured)
	}
}

// R13: another room's log is denied by tail as it is by summary, and a task id forged for another
// room, or for another principal, reads nothing.
func TestSponsoredTailAndTasksStayInTheirRooms(t *testing.T) {
	w := world(nil)
	e := setup(t, mcpOn, withSponsored(sponsoredP), sponsoredRooms(t, w))
	if got := call(t, e, bearer("slack-app"), "room_tail", map[string]any{"room": roomID}); got.isError {
		t.Fatalf("its own room: %s", got.text)
	}
	if got := call(t, e, bearer("slack-app"), "room_tail", map[string]any{"room": roomB}); !got.isError || !strings.Contains(got.text, noSuchRoom) {
		t.Fatalf("another room: %+v", got)
	}
	now := time.Now()
	mk := func(by, room string) string {
		return tailTask{V: 1, By: by, Room: room, Created: now.UnixMilli(), TTL: 60000}.id()
	}
	_, out := rpc(t, e, bearer("slack-app"), "tasks/get", map[string]any{"taskId": mk("system:slack-app", roomID)})
	if out["error"] != nil {
		t.Fatalf("its own room's task: %v", out)
	}
	// another room: the task fails as no such room, and holds no event
	forged := map[string]any{"taskId": mk("system:slack-app", roomB)}
	_, out = rpc(t, e, bearer("slack-app"), "tasks/get", forged)
	if res, _ := out["result"].(map[string]any); res["status"] != "failed" || res["statusMessage"] != noSuchRoom {
		t.Errorf("another room, tasks/get: %v", out)
	}
	_, out = rpc(t, e, bearer("slack-app"), "tasks/result", forged)
	if res, _ := out["result"].(map[string]any); res["isError"] != true || !strings.Contains(fmt.Sprint(res["content"]), noSuchRoom) || res["structuredContent"] != nil {
		t.Errorf("another room, tasks/result: %v", out)
	}
	// another principal's id is no task of its
	for _, method := range []string{"tasks/get", "tasks/result"} {
		_, out := rpc(t, e, bearer("slack-app"), method, map[string]any{"taskId": mk("human:dev", roomID)})
		if out["error"] == nil || out["result"] != nil {
			t.Errorf("another principal, %s: %v", method, out)
		}
	}
}

// R13: an expired token, and a principal that is not sponsored, are refused on /mcp.
func TestSponsoredPrincipalRefusals(t *testing.T) {
	expired := sponsoredP
	expired.Expiry = time.Now().Add(-time.Minute)
	notSponsored := sponsoredP
	notSponsored.ID, notSponsored.Sponsored = "system:other", false
	var e env
	for name, token := range map[string]string{"expired": "slack-app", "not sponsored": "other"} {
		if name == "expired" {
			e = setup(t, mcpOn, withSponsored(expired))
		} else {
			e = setup(t, mcpOn, withSponsored(notSponsored))
		}
		if code, _ := rpc(t, e, bearer(token), "tools/list", nil); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, code)
		}
	}
	e = setup(t, mcpOn, withSponsored(sponsoredP))
	if code, _ := rpc(t, e, bearer("slack-app"), "tools/list", nil); code != http.StatusOK {
		t.Fatalf("the control: %d", code)
	}
}
