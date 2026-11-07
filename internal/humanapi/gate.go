// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/summary"
)

// A room read's refusals. Each reads the same whatever the room, so none tells an unreadable room
// from a missing one; the text is the HTTP body, and the MCP server's tool error.
var (
	errNoSuchRoom       = errors.New(noSuchRoom)
	errRoomsUnavailable = errors.New("rooms unavailable")
	errAccessUnverified = errors.New(reasonAccessUnverified)
	errLogUnavailable   = errors.New("log unavailable")
	errLogUnreadable    = errors.New("room log unreadable")
)

// roomStatus is a room read's refusal as an HTTP status: only a missing room is a 404, and every
// other one is worth a retry.
func roomStatus(err error) int {
	if errors.Is(err, errNoSuchRoom) {
		return http.StatusNotFound
	}
	return http.StatusServiceUnavailable
}

// gateRoom reads the room, checks that p may see it (D7), then reads its log row. Its errors are
// errNoSuchRoom, errRoomsUnavailable, errAccessUnverified and errLogUnavailable.
func (s *Server) gateRoom(ctx context.Context, id string, p authn.Principal) (*v1alpha1.Room, store.RoomState, error) {
	room, found, err := s.room(ctx, id)
	if err != nil {
		return nil, store.RoomState{}, errRoomsUnavailable
	}
	if !found {
		if room, err = s.closedRoom(ctx, id); err != nil {
			return nil, store.RoomState{}, errLogUnavailable
		}
		if room == nil {
			return nil, store.RoomState{}, errNoSuchRoom
		}
	}
	// Before the log: a log error must not tell an unreadable room from a missing one.
	actx, acancel := context.WithTimeout(ctx, accessWait)
	ok, err := s.admits(actx, room, p)
	acancel()
	if err != nil {
		s.log().Warn("room access unverified", "room", id, "err", err)
		return nil, store.RoomState{}, errAccessUnverified
	}
	if !ok {
		return nil, store.RoomState{}, errNoSuchRoom
	}
	st, err := s.Log.Room(ctx, id)
	if errors.Is(err, store.ErrNoRoom) {
		return nil, store.RoomState{}, errNoSuchRoom
	}
	if err != nil {
		return nil, store.RoomState{}, errLogUnavailable
	}
	return room, st, nil
}

// readRoom is gateRoom, then the Read check on p's standing, which a room p may not read fails as
// a missing one.
func (s *Server) readRoom(ctx context.Context, id string, p authn.Principal) (*v1alpha1.Room, store.RoomState, policy.Subject, error) {
	room, st, err := s.gateRoom(ctx, id, p)
	if err != nil {
		return nil, st, policy.Subject{}, err
	}
	sub, _ := s.you(room, p, st.Driver)
	if !policy.Allowed(sub, policy.Read) {
		return nil, st, policy.Subject{}, errNoSuchRoom
	}
	return room, st, sub, nil
}

// closedPage is how many events closedRoom reads per call while it looks for the factory's facts.
const closedPage = 100

// closedRoom stands in for a Room the factory deleted when its task ended (roomclose.go): the log
// is sealed and retained, and the issue may still send its maintainer to read it. It carries only the
// repository the factory's own task facts name, so D7 decides on the repository as it did while the
// Room existed, and the standing rules see no owner and no member. Facts any other writer appended
// name nothing (as in the webhook emitter's fromLog). Nil, with no error, when the log is not sealed
// (a Room not in the cache yet), names no repository, or there is no factory principal.
func (s *Server) closedRoom(ctx context.Context, id string) (*v1alpha1.Room, error) {
	if s.FactoryPrincipal == "" || !envelope.ValidID(id) {
		return nil, nil
	}
	st, err := s.Log.Room(ctx, id)
	if errors.Is(err, store.ErrNoRoom) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Sealed {
		return nil, nil
	}
	for after := int64(0); ; {
		evs, err := s.Log.Range(ctx, id, after, closedPage)
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			after = ev.Seq
			if repo := s.factoryRepo(ev); repo != "" {
				return &v1alpha1.Room{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: s.Namespace},
					Spec: v1alpha1.RoomSpec{Repository: repo}}, nil
			}
		}
		if len(evs) < closedPage {
			return nil, nil
		}
	}
}

// factoryRepo is the repository ev's task facts name, when the factory wrote them.
func (s *Server) factoryRepo(ev envelope.Event) string {
	if ev.Type != envelope.StateChanged || ev.Actor.Kind != envelope.ActorSystem || ev.Actor.ID != s.FactoryPrincipal ||
		ev.Origin != envelope.OriginClient {
		return ""
	}
	var f struct {
		Kind string `json:"kind"`
		envelope.TaskFacts
	}
	if json.Unmarshal(ev.Payload, &f) != nil || f.Kind != "task" {
		return ""
	}
	return summary.Repo(&f.TaskFacts)
}
