// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/runrequest"
)

// StagesSource reads a repository's per-stage report from the factory with the caller's token (WR5);
// runrequest.Factory satisfies it.
type StagesSource interface {
	Stages(ctx context.Context, accessToken, repository, class string, last int) (stages.Report, error)
}

// stagesRoute is GET /api/stages?repository=<owner/name>[&class=<c>][&last=<n>]: forwards to the
// factory's GET /v1/stages with the caller's own token.
func (s *Server) stagesRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowedJSON(w, r, p) {
		return
	}
	if p.Kind != envelope.ActorHuman || p.Sponsored || p.AccessToken == "" {
		refuseJSON(w, http.StatusForbidden, "human_only")
		return
	}
	if s.Stages == nil {
		refuseJSON(w, http.StatusNotFound, "stages_unavailable")
		return
	}
	q := r.URL.Query()
	repo := q.Get("repository")
	if repo == "" {
		refuseJSON(w, http.StatusBadRequest, "repository_required")
		return
	}
	last := 0
	if raw := q.Get("last"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			refuseJSON(w, http.StatusBadRequest, "bad_last")
			return
		}
		last = n
	}
	rep, err := s.Stages.Stages(r.Context(), p.AccessToken, repo, q.Get("class"), last)
	if err != nil {
		var ref runrequest.ErrRefused
		if errors.As(err, &ref) && ref.Status > 0 {
			refuseJSON(w, ref.Status, ref.Reason)
			return
		}
		refuseJSON(w, http.StatusServiceUnavailable, "stages_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, rep)
}
