// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
)

// CoordinationViews reads the core's unfiltered view of a coordination parent (v4 MR13): an
// uncached, read-only reader. Its errors wrap coordinationview.ErrNoCoordination or
// ErrCoordinationUnavailable. The unfiltered value never leaves this package: every caller goes
// through filterCoordination.
type CoordinationViews interface {
	View(ctx context.Context, parentTask string) (coordination.CoordinationView, error)
}

// coordinationRoute is GET /api/coordinations/{task}: the parent room's ordinary gate first, then
// the family filtered by each child's repository for this viewer. A parent's room is named as its
// task. There is no route serving the unfiltered view.
func (s *Server) coordinationRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowed(w, r, p) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	task := r.PathValue("task")
	if _, _, _, err := s.readRoom(ctx, task, p); err != nil {
		http.Error(w, err.Error(), roomStatus(err))
		return
	}
	raw, err := s.CoordinationViews.View(ctx, task)
	switch {
	case errors.Is(err, coordinationview.ErrNoCoordination):
		http.Error(w, errNoSuchRoom.Error(), http.StatusNotFound)
		return
	case err != nil:
		s.log().Warn("coordination: view unreadable", "room", task, "err", err)
		http.Error(w, coordinationview.ErrCoordinationUnavailable.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.filterCoordination(ctx, raw, p))
}

// coordinationFor is room id's family as p may read it, when it is a coordination parent whose view
// is readable: a summary is served without one rather than refused.
func (s *Server) coordinationFor(ctx context.Context, id string, p authn.Principal) (coordinationview.View, bool) {
	if s.CoordinationViews == nil {
		return coordinationview.View{}, false
	}
	raw, err := s.CoordinationViews.View(ctx, id)
	if err != nil {
		if !errors.Is(err, coordinationview.ErrNoCoordination) {
			s.log().Warn("coordination: view unreadable", "room", id, "err", err)
		}
		return coordinationview.View{}, false
	}
	return s.filterCoordination(ctx, raw, p), true
}

// filterCoordination applies MR13's intersection for p: a human through their own GitHub read of
// each child's repository, a sponsored principal through its configured repositories alone. No
// admin standing reaches it.
func (s *Server) filterCoordination(ctx context.Context, raw coordination.CoordinationView, p authn.Principal) coordinationview.View {
	viewer := coordinationview.Viewer{Sub: p.Sub, Sponsored: p.Sponsored}
	if p.Sponsored {
		viewer.Repositories = s.SponsoredRepositories[p.ID]
	}
	actx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	return coordinationview.Filter(actx, raw, viewer, githubReader{s})
}

// githubReader is D7's two checks as coordinationview reads them; unwired, every child is
// restricted.
type githubReader struct{ s *Server }

var errAccessUnwired = errors.New("access check not wired")

func (g githubReader) Login(ctx context.Context, sub, repository string) (string, error) {
	if g.s.Identity == nil || g.s.Access == nil {
		return "", errAccessUnwired
	}
	return g.s.Identity.Login(ctx, sub, repository)
}

func (g githubReader) CanRead(ctx context.Context, repository, login string) (bool, error) {
	return g.s.Access.CanRead(ctx, repository, login)
}
