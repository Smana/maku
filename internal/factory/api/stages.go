// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/stages"
)

// stagesRoute is the per-stage report path: GET /v1/stages.
const stagesRoute = "GET /v1/stages"

// stagesReport is GET /v1/stages?repository=<owner/name>&class=<class>&last=<n> (WR5): folds the
// factory's newest ended Tasks for repository into per-class, per-stage costs and effects.
// Requires a human caller with the member role (same group check as catalogue) who can read
// repository on GitHub.
func (s *Server) stagesReport(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	case p.Kind != envelope.ActorHuman || p.Sponsored || p.Internal || p.Sub == "":
		refuse(w, http.StatusForbidden, "human_only")
		return
	}
	if (s.Groups.Member != "" || s.Groups.Admin != "") &&
		!slices.Contains(p.Groups, s.Groups.Member) && !slices.Contains(p.Groups, s.Groups.Admin) {
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	}

	q := r.URL.Query()
	asked := q.Get("repository")
	if asked == "" {
		refuse(w, http.StatusBadRequest, "repository_required")
		return
	}
	last := stages.DefaultLast
	if raw := q.Get("last"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n <= 0 {
			refuse(w, http.StatusBadRequest, "bad_last")
			return
		}
		last = min(n, stages.MaxLast)
	}
	repo := s.readable(cfg, w, r, p, asked)
	if repo == "" {
		return
	}
	reader := s.taskReader()
	if reader == nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	var list v1alpha1.TaskList
	if err := reader.List(r.Context(), &list, client.InNamespace(s.Namespace)); err != nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	rep := stages.Fold(repo, list.Items, last)
	if s.StagesCache != nil && last == stages.MaxLast {
		s.StagesCache.Store(repo, rep)
	}
	if cl := q.Get("class"); cl != "" {
		rep = rep.FilterClass(cl)
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, rep)
}

func (s *Server) taskReader() client.Reader {
	if s.Rooms != nil {
		return s.Rooms
	}
	return s.APIReader
}
