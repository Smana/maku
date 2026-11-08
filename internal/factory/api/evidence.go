// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// EvidenceSource is each repository's promotion evidence (merge policy stage 6); *evidence.Service
// implements it.
type EvidenceSource interface {
	Report(ctx context.Context, repository string) (evidence.Report, error)
}

// ReadChecker answers whether a human may read a repository on GitHub, as WriteChecker answers
// write; GitHubAccess implements both.
type ReadChecker interface {
	CanRead(ctx context.Context, p authn.Principal, repository string) (bool, error)
}

// promotionOn is whether policyManagement.promotion serves the evidence routes: off is a 404.
func (s *Server) promotionOn() bool {
	return s.Cfg.PolicyManagement.Promotion && s.Evidence != nil
}

// policyReader is a human caller; sponsored and system principals read no evidence.
func (s *Server) policyReader(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return p, false
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return p, false
	case p.Sponsored || p.Sub == "":
		refuse(w, http.StatusForbidden, "human_only")
		return p, false
	}
	return p, true
}

// reads is whether p may read repository on GitHub; an error is no answer.
func (s *Server) reads(ctx context.Context, p authn.Principal, repository string) (bool, error) {
	rc, ok := s.Access.(ReadChecker)
	if !ok {
		return false, nil
	}
	return rc.CanRead(ctx, p, repository)
}

// configured is repository as the config spells it, "" when it lists none.
func (s *Server) configured(repository string) string {
	for _, r := range s.Cfg.Repos() {
		if strings.EqualFold(r.Name, repository) {
			return r.Name
		}
	}
	return ""
}

// policyEvidence is GET /v1/policy/evidence?repository=owner/name: the repository's promotion
// evidence, for a reader of it on GitHub (MP17). A repository the caller cannot read, or the
// config does not list, is a 404 alike, so the answer tells no repository's existence.
func (s *Server) policyEvidence(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	asked := r.URL.Query().Get("repository")
	if asked == "" {
		refuse(w, http.StatusBadRequest, "repository_required")
		return
	}
	repo := s.readable(w, r, p, asked)
	if repo == "" {
		return
	}
	rep, err := s.Evidence.Report(r.Context(), repo)
	if err != nil {
		if s.Log != nil {
			s.Log.WarnContext(r.Context(), "policy evidence unread", "repository", repo, "error", err)
		}
		refuse(w, http.StatusServiceUnavailable, "evidence_unavailable")
		return
	}
	reply(w, http.StatusOK, rep)
}

// policyRepositories is GET /v1/policy/repositories: the configured repositories the caller reads.
// One that cannot be checked is left out, never shown.
func (s *Server) policyRepositories(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	out := []string{}
	for _, rc := range s.Cfg.Repos() {
		if can, err := s.reads(r.Context(), p, rc.Name); err == nil && can {
			out = append(out, rc.Name)
		}
	}
	reply(w, http.StatusOK, map[string][]string{"repositories": out})
}

// PolicyEditor is the settings editor's check and the decision timeline (stage 6), each answered
// as the JSON document it is; app wires *policyview.Service through it. The documents are any so
// this package does not import the reconciler: intake's tests import this package, and the
// reconciler imports intake.
type PolicyEditor interface {
	Preview(ctx context.Context, repository, file string) (any, error)
	Timeline(ctx context.Context, repository string, pr int) (any, error)
}

// maxPreviewBody bounds a proposed policy: the file's own bound and its envelope.
const maxPreviewBody = mergepolicy.MaxFileBytes + 4<<10

// readable answers the caller's read of repository, refusing for them when not: the repository as
// the config spells it, or "".
func (s *Server) readable(w http.ResponseWriter, r *http.Request, p authn.Principal, asked string) string {
	repo := s.configured(asked)
	if repo == "" {
		refuse(w, http.StatusNotFound, "not_found")
		return ""
	}
	switch can, err := s.reads(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return ""
	case !can:
		refuse(w, http.StatusNotFound, "not_found")
		return ""
	}
	return repo
}

// policyPreview is POST /v1/policy/preview {repository, file}: a maintainer's proposed policy file,
// checked strictly, compared structurally and simulated (MP8, MP16). It writes nothing: the answer
// is the file to submit and the GitHub page to submit it from.
func (s *Server) policyPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository string `json:"repository"`
		File       string `json:"file"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPreviewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	repo := s.readable(w, r, p, in.Repository)
	if repo == "" {
		return
	}
	switch can, err := s.Access.CanWrite(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return
	case !can:
		refuse(w, http.StatusForbidden, "not_a_maintainer")
		return
	}
	pv, err := s.Editor.Preview(r.Context(), repo, in.File)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "evidence_unavailable")
		return
	}
	reply(w, http.StatusOK, pv)
}

// policyDecisions is GET /v1/policy/decisions?repository=owner/name&pr=N: the pull request's
// decision timeline, for a reader of the repository.
func (s *Server) policyDecisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	pr, err := strconv.Atoi(r.URL.Query().Get("pr"))
	if err != nil || pr < 1 {
		refuse(w, http.StatusBadRequest, "pr_required")
		return
	}
	repo := s.readable(w, r, p, r.URL.Query().Get("repository"))
	if repo == "" {
		return
	}
	tl, err := s.Editor.Timeline(r.Context(), repo, pr)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "decisions_unavailable")
		return
	}
	reply(w, http.StatusOK, tl)
}
