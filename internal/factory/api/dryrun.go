// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/stages"
)

// workflowDryRunRoute is the workflow dry-run path: POST /v1/workflows/dry-run.
const workflowDryRunRoute = "POST /v1/workflows/dry-run"

// maxDryRunBody bounds a POST /v1/workflows/dry-run request body.
const maxDryRunBody = 128 << 10

// workflowDryRun is POST /v1/workflows/dry-run (WR11, Task I5): simulates a named workflow (or an
// unstored candidate YAML definition compiled in memory without mutating config or cluster state)
// over recent ended Tasks readable by the caller.
func (s *Server) workflowDryRun(w http.ResponseWriter, r *http.Request) {
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

	var req stages.DryRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDryRunBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if req.Workflow == "" {
		refuse(w, http.StatusBadRequest, "workflow_required")
		return
	}
	if req.Last < 0 {
		refuse(w, http.StatusBadRequest, "bad_last")
		return
	}
	last := req.Last
	if last == 0 {
		last = stages.DefaultDryRunLast
	}
	if last > stages.MaxLast {
		last = stages.MaxLast
	}

	// Validate & compile the candidate definition in memory without ever storing it,
	// or resolve an existing workflow from the current config.
	var profile config.HarnessProfile
	if strings.TrimSpace(req.Definition) != "" {
		compiled, _, cerr := cfg.CompileCandidate(req.Workflow, []byte(req.Definition))
		if cerr != nil {
			reply(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid_definition",
				"detail": cerr.Error(),
			})
			return
		}
		profile = compiled
	} else {
		wf, ok := cfg.Workflow(req.Workflow)
		if !ok {
			refuse(w, http.StatusNotFound, "unknown_workflow")
			return
		}
		profile = wf
	}

	// Resolve readable repositories for this caller.
	var repoFilter string
	readableRepos := map[string]bool{}
	if req.Repository != "" {
		repoFilter = s.readable(cfg, w, r, p, req.Repository)
		if repoFilter == "" {
			return
		}
		readableRepos[strings.ToLower(repoFilter)] = true
	} else {
		for _, name := range catalogue.Repositories(cfg) {
			if can, rerr := s.reads(r.Context(), p, name); rerr == nil && can {
				readableRepos[strings.ToLower(name)] = true
			}
		}
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

	// Filter tasks strictly to repositories the caller can read.
	visible := make([]v1alpha1.Task, 0, len(list.Items))
	for _, tk := range list.Items {
		if readableRepos[strings.ToLower(tk.Spec.Repository)] {
			visible = append(visible, tk)
		}
	}

	res := stages.DryRun(cfg, req.Workflow, profile, repoFilter, visible, last)
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, res)
}
