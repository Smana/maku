// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/hardening"
)

// ReadinessRoute lists each hardened repository's readiness (H5, HR41).
const ReadinessRoute = "GET /v1/hardening/readiness"

// readinessMissingError is the missing fact a view names when readiness could not be computed.
const readinessMissingError = "error"

// readinessOf computes repository's readiness afresh, as the Evidence page and the route show it:
// an error is not hardened, missing error, and its text stays in the log.
func (s *Server) readinessOf(ctx context.Context, repository string) evidence.Readiness {
	r, err := s.Readiness.Hardened(ctx, repository)
	if err != nil {
		s.log().WarnContext(ctx, "hardening readiness unread", "repository", repository, "err", err)
		return evidence.Readiness{Repository: repository, Missing: []string{readinessMissingError}}
	}
	v := evidence.Readiness{Repository: repository, Hardened: r.Hardened, Drill: r.Drill, Missing: []string{}}
	for _, m := range r.Missing {
		v.Missing = append(v.Missing, string(m))
	}
	return v
}

// hardenedRepos are the configured repositories at hardening mode audit or above: the ones
// readiness is reported for.
func hardenedRepos(cfg *config.Config) []string {
	var out []string
	for _, r := range cfg.Repos() {
		if cfg.Hardening.ModeFor(r.Name).AtLeast(hardening.ModeAudit) {
			out = append(out, r.Name)
		}
	}
	return out
}

// hardeningReadiness is GET /v1/hardening/readiness (HR41): for an operator, a human in the admin
// group, each repository at mode audit or above, [{repository, hardened, missing, drill}], computed
// afresh. Everyone else is 403.
func (s *Server) hardeningReadiness(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	if !s.Groups.IsAdmin(p) {
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	}
	out := []evidence.Readiness{}
	for _, repo := range hardenedRepos(s.Cfg.Current()) {
		out = append(out, s.readinessOf(r.Context(), repo))
	}
	reply(w, http.StatusOK, map[string]any{"repositories": out})
}
