// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

// splitRoom is PR 3's split-proposal room: a task an Escalated scoper bounced with a proposed split (G4).
const splitRoom = "26zfnuxm"

// needsBody is GET /api/needs' answer, as a client reads it (CD28).
type needsBody struct {
	APIVersion string         `json:"apiVersion"`
	Items      []summary.Need `json:"items"`
	Cursor     string         `json:"cursor"`
	Access     string         `json:"access"`
	Unread     []string       `json:"unread"`
}

// needsOn turns the human door on, as human.needs.enabled does (D4: a switch ships with the code
// that reads it).
func needsOn(s *Server, _ *fanout.Hub, _ *hubView) { s.Needs = true }

// needsFacts is a room's last task facts, as the factory's write lands them in its log.
func needsFacts(room string, seq int64, f envelope.TaskFacts) envelope.Event {
	return envelope.Event{V: 1, Seq: seq, RoomID: room, Type: envelope.StateChanged, TS: time.Now(),
		Payload: envelope.TaskStatePayload(f)}
}

// needsRooms is the fixture behind every /api/needs test: the split proposal on splitRoom (G4), the
// pull request waiting for a maintainer's merge on roomID (CD26), and a room with no task at all — which
// is not unread, it has no news. approval, when set, is a live approval of splitRoom.
func needsRooms(t *testing.T, approval *store.Approval) option {
	t.Helper()
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		split := repoRoom(splitRoom, "Smana/a")
		split.Status.Task = &v1alpha1.TaskStatus{Phase: "Escalated", Issue: 2238}
		merge := repoRoom(roomID, "Smana/a")
		merge.Status.Task = &v1alpha1.TaskStatus{Phase: "AwaitingHuman", PR: 2239}
		if approval != nil {
			split.Status.PendingApprovals = 1 // the room projects it; the store below holds it
			v.approvals = []store.Approval{*approval}
		}
		s.Rooms = roomClient(t, split, merge, repoRoom(roomC, "Smana/a"))
		s.PublicURL = "https://rooms.test"
		v.evs = append(v.evs,
			needsFacts(splitRoom, 1, envelope.TaskFacts{Phase: "Escalated", Reason: "split_proposed",
				Issue: &envelope.IssueFact{Number: 2238, URL: "https://github.com/Smana/a/issues/2238"}}),
			needsFacts(roomID, 11, envelope.TaskFacts{Phase: "AwaitingHuman",
				PR: &envelope.PRFact{Number: 2239, URL: "https://github.com/Smana/a/pull/2239"}}))
	}
}

// needsWorld is needsRooms, and a GitHub on which dev may write Smana/a: the merge on roomID is theirs
// whether or not they own the room (CD26).
func needsWorld(t *testing.T, approval *store.Approval) option {
	t.Helper()
	w := world(nil)
	w.set(func() { w.writes = map[string][]string{"Smana/a": {"dev"}} })
	gate, rooms := w.option(), needsRooms(t, approval)
	return func(s *Server, h *fanout.Hub, v *hubView) {
		gate(s, h, v)
		rooms(s, h, v)
	}
}

// unreadableFacts is a log whose task facts cannot be read for the rooms named, as a store read that
// fails does: the other rooms' are still read (Review Focus 1).
type unreadableFacts struct {
	*memLog
	rooms map[string]bool
}

func (l unreadableFacts) LastTaskState(ctx context.Context, room string, after int64) (json.RawMessage, error) {
	if l.rooms[room] {
		return nil, errors.New("store down")
	}
	return l.memLog.LastTaskState(ctx, room, after)
}

// unreadableFactsRooms makes those rooms' task facts unreadable, every other read unchanged.
func unreadableFactsRooms(rooms ...string) option {
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		set := map[string]bool{}
		for _, r := range rooms {
			set[r] = true
		}
		s.Log = unreadableFacts{memLog: v.memLog, rooms: set}
	}
}

// needsOf is GET /api/needs+q for h: the answer, and the status it came with.
func needsOf(t *testing.T, e env, q string, h http.Header) (needsBody, int) {
	t.Helper()
	code, body := get(t, e, "/api/needs"+q, h)
	var got needsBody
	if code == http.StatusOK && json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("GET /api/needs%s: %d %s", q, code, body)
	}
	return got, code
}

// needKeys is items' identities, in the order the list gives them (CD21: one order, every surface).
func needKeys(items []summary.Need) []string {
	keys := make([]string, 0, len(items))
	for _, n := range items {
		keys = append(keys, n.Key())
	}
	return keys
}

// CD10: the human door lists the caller's needs across rooms, in the one order, with a cursor.
func TestNeedsAcrossRooms(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil))
	got, code := needsOf(t, e, "", admin("dev"))
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	if got.APIVersion != "needs/v1" {
		t.Errorf("apiVersion %q, want needs/v1", got.APIVersion)
	}
	if keys := needKeys(got.Items); !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238", "3kq7x2ma/merge/2239"}) {
		t.Fatalf("%v", keys)
	}
	if _, err := time.Parse(time.RFC3339, got.Cursor); err != nil {
		t.Errorf("cursor %q is no RFC 3339 instant: %v", got.Cursor, err)
	}
	if len(got.Unread) != 0 || got.Access != accessOK {
		t.Errorf("unread %v, access %q; want none and ok", got.Unread, got.Access)
	}
	if h := accessOfNeeds(t, e, admin("dev")); h != accessOK {
		t.Errorf("%s = %q, want ok", accessHeader, h)
	}
}

// accessOfNeeds is the X-Rooms-Access header GET /api/needs gives h, the header the room list sets.
func accessOfNeeds(t *testing.T, e env, h http.Header) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+"/api/needs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/needs: %d", r.StatusCode)
	}
	return r.Header.Get(accessHeader)
}

// A broker with human.needs.enabled off serves no door: the route is not mounted, so it is a 404 like
// any path it does not know.
func TestNeedsRouteFollowsItsSwitch(t *testing.T) {
	e := setup(t, needsWorld(t, nil)) // needsOn left out: the switch is off
	if _, code := needsOf(t, e, "", admin("dev")); code != http.StatusNotFound {
		t.Fatalf("%d, want 404 while human.needs.enabled is off", code)
	}
}

// R13, C18: /api/needs is the human token's door. A sponsored token stays unauthenticated there, as on
// every route but /mcp: the App reads its rooms through its own tool, never through this one (CD28).
func TestNeedsRefusesASponsoredToken(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil), withSponsored(sponsoredP))
	if code, body := get(t, e, "/api/needs", bearer("slack-app")); code != http.StatusUnauthorized {
		t.Fatalf("%d %s, want 401", code, body)
	}
}

// Review Focus 3: a cursor that is not an RFC 3339 instant is refused, never read as everything or nothing.
func TestNeedsRefusesAMalformedSince(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil))
	for _, since := range []string{"yesterday", "2026-11-04 19:10:00", "9999-99-99T00:00:00Z"} {
		code, body := get(t, e, "/api/needs?since="+url.QueryEscape(since), admin("dev"))
		if code != http.StatusBadRequest || !strings.Contains(body, "since is an RFC 3339 instant") {
			t.Errorf("%s: %d %s", since, code, body)
		}
	}
}

// Review Focus 1: a room whose facts cannot be read contributes no item, is named in unread, and the other
// rooms still answer. A room with no task is not unread: it has no news to read.
func TestNeedsSaysWhenARoomsStateIsUnread(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil), unreadableFactsRooms(roomID))
	got, code := needsOf(t, e, "", admin("dev"))
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	if keys := needKeys(got.Items); !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238"}) {
		t.Fatalf("%v, want the readable room's item alone", keys)
	}
	if !slices.Equal(got.Unread, []string{roomID}) {
		t.Fatalf("unread %v, want [%s]", got.Unread, roomID)
	}
}

// `since` keeps only the items whose cause is newer than it: an approval asked at 19:00 is out at 19:05.
// An item with no date is kept: a zero At is unknown, never old (Review Focus 3).
func TestNeedsSinceFiltersByTheItemsOwnTime(t *testing.T) {
	approval := &store.Approval{ID: "01J9A", RoomID: splitRoom, Class: "shell", State: "pending",
		RequestedAt: time.Date(2026, 11, 4, 19, 0, 0, 0, time.UTC), ExpiresAt: time.Now().Add(time.Hour)}
	e := setup(t, needsOn, needsWorld(t, approval))
	asked, split, merge := splitRoom+"/approval/01J9A", splitRoom+"/plan_confirmation/2238", "3kq7x2ma/merge/2239"
	// No cursor lists everything, in the one order: the approval's deadline first, then the kinds.
	if keys := needKeys(mustNeeds(t, e, "", admin("dev")).Items); !slices.Equal(keys, []string{asked, split, merge}) {
		t.Fatalf("no cursor: %v", keys)
	}
	// The approval was asked at 19:00, so a cursor past it drops it; the items this broker cannot date
	// (no room of theirs was folded here) stay, because unknown is not old.
	if keys := needKeys(mustNeeds(t, e, "?since="+url.QueryEscape("2026-11-04T19:01:00Z"), admin("dev")).Items); !slices.Equal(keys, []string{split, merge}) {
		t.Fatalf("since 19:01: %v", keys)
	}
	if keys := needKeys(mustNeeds(t, e, "?since="+url.QueryEscape("2026-11-04T18:59:00Z"), admin("dev")).Items); !slices.Equal(keys, []string{asked, split, merge}) {
		t.Fatalf("since 18:59: %v", keys)
	}
	// Read once, the split room's facts carry their date (summary.State.FactsAt), so a cursor past it
	// drops that item too, while the merge stays undated. The answer names the instant to ask from next.
	if code, body := get(t, e, "/api/rooms/"+splitRoom+"/summary", admin("dev")); code != http.StatusOK {
		t.Fatalf("summary: %d %s", code, body)
	}
	next := mustNeeds(t, e, "?since="+url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), admin("dev"))
	if keys := needKeys(next.Items); !slices.Equal(keys, []string{merge}) {
		t.Fatalf("since an hour from now: %v", keys)
	}
	if at, err := time.Parse(time.RFC3339, next.Cursor); err != nil || time.Since(at) > time.Minute || time.Until(at) > time.Minute {
		t.Fatalf("cursor %q, want this answer's instant", next.Cursor)
	}
}

// mustNeeds is GET /api/needs+q for h, which must answer 200.
func mustNeeds(t *testing.T, e env, q string, h http.Header) needsBody {
	t.Helper()
	got, code := needsOf(t, e, q, h)
	if code != http.StatusOK {
		t.Fatalf("GET /api/needs%s: %d", q, code)
	}
	return got
}

// The series the dashboard queries exist for this route, and under the spec's names: maku_needs_list_total
// counts the list, and maku_needs_items says what it held, by kind. MakuName's needs_ case is what makes
// those the names, so this is the test that keeps the dashboard's queries true rather than plausible.
func TestNeedsAreMeasured(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil))
	mustNeeds(t, e, "", admin("dev"))
	body := scrape(t, e)
	for _, want := range []string{
		`maku_needs_list_total{outcome="ok",principal="human:dev",surface="api"} 1`,
		`maku_needs_items{kind="merge"} 1`,
		`maku_needs_items{kind="plan_confirmation"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}
}

// The list narrows to one room and to one repository, and a filter never reveals an unreadable room: one
// that matches nothing answers an empty list, not an error and not another room's items.
func TestNeedsRoomAndRepoFilters(t *testing.T) {
	e := setup(t, needsOn, needsWorld(t, nil))
	if keys := needKeys(mustNeeds(t, e, "?room="+roomID, admin("dev")).Items); !slices.Equal(keys, []string{"3kq7x2ma/merge/2239"}) {
		t.Fatalf("room: %v", keys)
	}
	if keys := needKeys(mustNeeds(t, e, "?room="+splitRoom, admin("dev")).Items); !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238"}) {
		t.Fatalf("room: %v", keys)
	}
	if keys := needKeys(mustNeeds(t, e, "?repo=smana/A", admin("dev")).Items); len(keys) != 2 {
		t.Fatalf("repo, as the room list matches it: %v", keys)
	}
	for _, q := range []string{"?repo=Smana/b", "?room=" + roomC} {
		got := mustNeeds(t, e, q, admin("dev"))
		if len(got.Items) != 0 || len(got.Unread) != 0 {
			t.Errorf("%s: %+v", q, got)
		}
	}
}
