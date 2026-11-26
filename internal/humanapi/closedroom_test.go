// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
)

// closedRoom is a room the factory closed: its Room is deleted, its log sealed and kept.
const closedRoom = "7kq7x2mf"

// closedLog is a sealed log whose task facts name the issue on repo, written by actor.
func closedLog(l *memLog, actor envelope.Actor, repo string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sealed = true
	l.evs = []envelope.Event{
		{V: 1, Seq: 1, RoomID: closedRoom, Type: envelope.StateChanged, TS: time.Now(), Actor: actor, Origin: envelope.OriginClient,
			Payload: envelope.TaskStatePayload(envelope.TaskFacts{Phase: "Done", Reason: "proposal_ready",
				Issue: &envelope.IssueFact{Number: 2740, URL: "https://github.com/" + repo + "/issues/2740"}})},
		{V: 1, Seq: 2, RoomID: closedRoom, Type: envelope.StateChanged, TS: time.Now(), Origin: envelope.OriginBroker,
			Actor:   envelope.Actor{Kind: envelope.ActorSystem, ID: "system:room-broker"},
			Payload: envelope.Must(map[string]string{"kind": "room_phase", "phase": "Closed", "reason": "room deleted"})},
	}
}

// A finished task's room closes (its Room is deleted), but the issue still asks its maintainer to read
// the proposal there: the sealed log stays readable, under D7 on the repository its factory facts name,
// for as long as it is retained.
func TestAClosedRoomStaysReadableToItsRepositoryReaders(t *testing.T) {
	factory := envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}
	withFactory := func(s *Server, _ *fanout.Hub, _ *hubView) { s.FactoryPrincipal = "system:factory" }
	for _, c := range []struct {
		name   string
		actor  envelope.Actor
		sealed bool
		h      http.Header
		want   int
	}{
		{"a reader of the repository", factory, true, header("dev1"), http.StatusOK},
		{"an admin", factory, true, admin("boss"), http.StatusOK},
		{"a reader of nothing", factory, true, header("dev2"), http.StatusNotFound},
		{"facts another writer forged", envelope.Actor{Kind: envelope.ActorSystem, ID: "system:runlore"}, true, header("dev1"), http.StatusNotFound},
		{"a log not sealed yet: its Room is not made", factory, false, header("dev1"), http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := world(map[string][]string{"Smana/a": {"dev1"}})
			e := setup(t, abc(t), w.option(), withFactory)
			closedLog(e.log, c.actor, "Smana/a")
			e.log.mu.Lock()
			e.log.sealed = c.sealed
			e.log.mu.Unlock()
			code, body := get(t, e, "/api/rooms/"+closedRoom+"/summary", c.h)
			if code != c.want {
				t.Fatalf("summary: %d %q, want %d", code, body, c.want)
			}
		})
	}
}

// GET /api/rooms?closed=1 lists the closed rooms of the last closedWindow, each through the very gate
// that opens one by its URL: the default list, which reads Room objects, never shows them.
func TestTheClosedListShowsWhatItsURLWouldOpen(t *testing.T) {
	factory := envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}
	withFactory := func(s *Server, _ *fanout.Hub, _ *hubView) { s.FactoryPrincipal = "system:factory" }
	for _, c := range []struct {
		name   string
		actor  envelope.Actor
		sealed bool
		h      http.Header
		want   []string
	}{
		{"a reader of the repository", factory, true, header("dev1"), []string{closedRoom}},
		{"an admin", factory, true, admin("boss"), []string{closedRoom}},
		{"a reader of nothing", factory, true, header("dev2"), nil},
		{"facts another writer forged", envelope.Actor{Kind: envelope.ActorSystem, ID: "system:runlore"}, true, header("dev1"), nil},
		{"a log not sealed", factory, false, header("dev1"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := world(map[string][]string{"Smana/a": {"dev1"}})
			e := setup(t, abc(t), w.option(), withFactory)
			closedLog(e.log, c.actor, "Smana/a")
			e.log.mu.Lock()
			e.log.sealed = c.sealed
			e.log.mu.Unlock()
			code, body := get(t, e, "/api/rooms?closed=1", c.h)
			if code != http.StatusOK {
				t.Fatalf("closed list: %d %q", code, body)
			}
			var rows []roomRow
			if err := json.Unmarshal([]byte(body), &rows); err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, r := range rows {
				got = append(got, r.ID)
			}
			if !slices.Equal(got, append([]string{}, c.want...)) {
				t.Fatalf("closed rows %v, want %v", got, c.want)
			}
			if len(rows) == 1 {
				r := rows[0]
				if r.URL != e.srv.PublicURL+"/r/"+closedRoom || r.Repository != "Smana/a" || r.Issue != 2740 || r.Phase != "Closed" ||
					r.Task != "Done" || r.ClosedAt == nil {
					t.Fatalf("closed row %+v", r)
				}
			}
			e.log.mu.Lock()
			since := e.log.sealedSince
			e.log.mu.Unlock()
			if d := time.Since(since); d < closedWindow-time.Minute || d > closedWindow+time.Minute {
				t.Fatalf("window: since %v ago, want %v", d, closedWindow)
			}
			// The default list is unchanged: it reads Room objects, and this one is gone.
			if _, body := get(t, e, "/api/rooms", c.h); strings.Contains(body, closedRoom) {
				t.Fatalf("default list shows the closed room: %s", body)
			}
		})
	}
}

// The default list's X-Rooms-Closed counts what the closed view would list the caller, asked for with
// closed_count=1: a room the caller cannot open is never counted, so the count tells no room's existence.
func TestTheClosedCountIsOnlyRoomsTheCallerMayOpen(t *testing.T) {
	factory := envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}
	withFactory := func(s *Server, _ *fanout.Hub, _ *hubView) { s.FactoryPrincipal = "system:factory" }
	for _, c := range []struct {
		name  string
		actor envelope.Actor
		path  string
		h     http.Header
		want  string // "" is no header
	}{
		{"a reader of the repository", factory, "/api/rooms?closed_count=1", header("dev1"), "1"},
		{"an admin", factory, "/api/rooms?closed_count=1", admin("boss"), "1"},
		{"a reader of nothing", factory, "/api/rooms?closed_count=1", header("dev2"), "0"},
		{"facts another writer forged", envelope.Actor{Kind: envelope.ActorSystem, ID: "system:runlore"}, "/api/rooms?closed_count=1", header("dev1"), "0"},
		{"another repository's filter", factory, "/api/rooms?closed_count=1&repo=Smana/b", header("dev1"), "0"},
		{"the same repository's filter", factory, "/api/rooms?closed_count=1&repo=smana/a", header("dev1"), "1"},
		{"not asked: a refresh pays no closed gates", factory, "/api/rooms", header("dev1"), ""},
		{"the closed view itself", factory, "/api/rooms?closed=1&closed_count=1", header("dev1"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := world(map[string][]string{"Smana/a": {"dev1"}})
			e := setup(t, abc(t), w.option(), withFactory)
			closedLog(e.log, c.actor, "Smana/a")
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+c.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header = c.h
			r, err := e.ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Fatalf("list: %d", r.StatusCode)
			}
			if got := r.Header.Get(closedHeader); got != c.want {
				t.Fatalf("%s = %q, want %q", closedHeader, got, c.want)
			}
		})
	}
}
