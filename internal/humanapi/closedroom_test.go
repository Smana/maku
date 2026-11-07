// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"net/http"
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
