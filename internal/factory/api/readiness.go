// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/hardening"
)

// ReadinessRoute lists each hardened repository's readiness (H5, HR41).
const ReadinessRoute = "GET /v1/hardening/readiness"

// readinessMissingError is the missing fact a view names when readiness could not be computed.
const readinessMissingError = "error"

// readinessTTL is how long the Evidence page and the readiness route serve one repository's
// readiness again: a read costs up to five GitHub reads with the factory App, and the page polls.
const readinessTTL = 60 * time.Second

// readinessCache is one read set per repository for readinessTTL, shared by concurrent callers
// through singleflight (#531's review). It serves the page and the route only: admission asks the
// Evaluator afresh. Safe for concurrent use.
type readinessCache struct {
	mu sync.Mutex
	m  map[string]cachedReadiness
	g  singleflight.Group
}

type cachedReadiness struct {
	at time.Time
	v  evidence.Readiness
}

// readinessOf is repository's readiness as the Evidence page and the route show it, at most
// readinessTTL old: an error is not hardened, missing error, and its text stays in the log.
func (s *Server) readinessOf(ctx context.Context, repository string) evidence.Readiness {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	c := s.readinessCache
	c.mu.Lock()
	if e, ok := c.m[repository]; ok && now.Sub(e.at) < readinessTTL {
		c.mu.Unlock()
		return e.v
	}
	c.mu.Unlock()
	v, _, _ := c.g.Do(repository, func() (any, error) {
		v := s.readinessFresh(context.WithoutCancel(ctx), repository)
		c.mu.Lock()
		if c.m == nil {
			c.m = map[string]cachedReadiness{}
		}
		c.m[repository] = cachedReadiness{at: now, v: v}
		c.mu.Unlock()
		return v, nil
	})
	return v.(evidence.Readiness)
}

// readinessFresh computes repository's readiness once.
func (s *Server) readinessFresh(ctx context.Context, repository string) evidence.Readiness {
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

// readerView is the Evidence page's row: ready, not ready, or error. Which facts are missing, and
// the drill, stay on the operators' route (#531's review).
func readerView(v evidence.Readiness) evidence.Readiness {
	out := evidence.Readiness{Repository: v.Repository, Hardened: v.Hardened}
	if slices.Contains(v.Missing, readinessMissingError) {
		out.Missing = []string{readinessMissingError}
	}
	return out
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
