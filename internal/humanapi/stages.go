// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/runrequest"
)

// StagesSource reads a repository's per-stage report and workflow dry-run from the factory with
// the caller's token (WR5, WR11); runrequest.Factory satisfies it.
type StagesSource interface {
	Stages(ctx context.Context, accessToken, repository, class string, last int) (stages.Report, error)
	WorkflowDryRun(ctx context.Context, accessToken string, in stages.DryRunRequest) (stages.DryRunResult, error)
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

// maxWorkflowDryRunBody bounds a POST /api/workflows/dry-run request body.
const maxWorkflowDryRunBody = 128 << 10

// workflowDryRunRoute is POST /api/workflows/dry-run: forwards to the factory's
// POST /v1/workflows/dry-run with the caller's own token (WR11).
func (s *Server) workflowDryRunRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowedJSON(w, r, p) {
		return
	}
	if p.Kind != envelope.ActorHuman || p.Sponsored || p.AccessToken == "" {
		refuseJSON(w, http.StatusForbidden, "human_only")
		return
	}
	if s.Stages == nil {
		refuseJSON(w, http.StatusNotFound, "dry_run_unavailable")
		return
	}
	var in stages.DryRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflowDryRunBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	if in.Workflow == "" {
		refuseJSON(w, http.StatusBadRequest, "workflow_required")
		return
	}
	if in.Last < 0 {
		refuseJSON(w, http.StatusBadRequest, "bad_last")
		return
	}
	res, err := s.Stages.WorkflowDryRun(r.Context(), p.AccessToken, in)
	if err != nil {
		var ref runrequest.ErrRefused
		if errors.As(err, &ref) && ref.Status > 0 {
			refuseJSON(w, ref.Status, ref.Reason)
			return
		}
		refuseJSON(w, http.StatusServiceUnavailable, "dry_run_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, res)
}
