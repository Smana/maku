// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
)

// catalogueRoute is the catalogue's path: GET /v1/catalogue/{owner}/{name}.
const catalogueRoute = "GET /v1/catalogue/{owner}/{name}"

// catalogue is GET /v1/catalogue/{owner}/{name}[?onBehalfOf=slack:<team>/<user>]: what the
// repository offers at intake, from the accepted config snapshot (P2). It reads nothing on GitHub
// but the caller's own access. A human reads it as themselves; a sponsored principal reads it for
// the Slack user it names, resolved as POST /v1/tasks resolves it (P17). A repository the config
// does not serve, or the caller cannot read, is a 404 that names nothing. The route is mounted
// whenever the API is: it is not behind policyManagement.promotion.
func (s *Server) catalogue(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	asked := r.PathValue("owner") + "/" + r.PathValue("name")
	var login string
	switch {
	case p.Internal:
		refuse(w, http.StatusForbidden, "human_only")
		return
	case p.Sponsored:
		repo, code, reason := "", 0, ""
		if login, repo, code, reason = s.catalogueSponsored(r.Context(), cfg, p, asked, r.URL.Query().Get("onBehalfOf")); reason != "" {
			refuse(w, code, reason)
			return
		}
		asked = repo
	case p.Sub == "":
		refuse(w, http.StatusForbidden, "human_only")
		return
	default:
		repo := s.readable(cfg, w, r, p, asked)
		if repo == "" {
			return
		}
		asked = repo
		if s.Logins != nil {
			if login, err = s.Logins.Login(r.Context(), p.Sub, repo); err != nil {
				refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
				return
			}
		}
	}
	rc := cfg.RepositoryFor(asked)
	if rc == nil {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	c, ok := catalogue.Build(cfg, asked, login != "" && rc.IsMaintainer(login))
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, c)
}

// catalogueSponsored is a sponsored principal's reading: the login its onBehalfOf resolves to and
// the repository as the config spells it. Without onBehalfOf there is no one to be a maintainer
// for, so the answer is human_only; a repository outside the principal's list is a 404.
func (s *Server) catalogueSponsored(ctx context.Context, cfg *config.Config, p authn.Principal, asked, onBehalfOf string) (login, repo string, code int, reason string) {
	sp := cfg.SponsoredBy(p.ID)
	if sp == nil || onBehalfOf == "" {
		return "", "", http.StatusForbidden, "human_only"
	}
	repo = canonicalRepository(sp.Repositories, asked)
	if !slices.Contains(sp.Repositories, repo) {
		return "", "", http.StatusNotFound, "not_found"
	}
	login, code, reason = s.onBehalfLogin(ctx, sp, onBehalfOf, repo)
	return login, repo, code, reason
}
