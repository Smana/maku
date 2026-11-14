// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"net/http"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// GitHubLinks reads the GitHub account links (v4 D8); *store.Store implements it.
type GitHubLinks interface {
	GitHubLinkOf(ctx context.Context, sub string) (store.GitHubLink, bool, error)
}

// githubLinkReply is GET /v1/links/github's answer. Active is the broker's activeWindow applied, so
// the factory never holds a window of its own.
type githubLinkReply struct {
	GitHubID   int64     `json:"githubId"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	Active     bool      `json:"active"`
}

// githubLink: GET /v1/links/github?sub=, the link reader's only. The answer is the numeric GitHub id,
// never a login: the factory resolves that itself through its App, rename-safe. A sub with no link
// is a 404 not_linked.
func (s *Server) githubLink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	if s.GitHubLinks == nil || s.LinkReader == "" || p.ID != s.LinkReader {
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	sub := r.URL.Query().Get("sub")
	if sub == "" || len(sub) > 128 {
		fail(w, http.StatusBadRequest, wire.ReasonBadSub)
		return
	}
	l, found, err := s.GitHubLinks.GitHubLinkOf(r.Context(), sub)
	switch {
	case err != nil:
		s.logFailure(w, err, "read a github link")
	case !found:
		fail(w, http.StatusNotFound, wire.ReasonNotLinked)
	default:
		reply(w, http.StatusOK, githubLinkReply{GitHubID: l.GitHubID, LastSeenAt: l.LastSeenAt.UTC(),
			Active: s.now().Sub(l.LastSeenAt) <= s.GitHubActiveWindow})
	}
}
