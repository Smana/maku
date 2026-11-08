// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/evidence"
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
	repo := s.configured(asked)
	if repo == "" {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	switch can, err := s.reads(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return
	case !can:
		refuse(w, http.StatusNotFound, "not_found")
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
