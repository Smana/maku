// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// HandoffStarts reads the broker's records of web New task starts; *store.Store implements it.
type HandoffStarts interface {
	GetHandoffStart(ctx context.Context, repository string, issue int, now time.Time) (store.HandoffStart, bool, error)
}

var handoffRepositoryRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// handoffStartReply is a start record as the factory reads it: the handoff, the issue it filed and
// the ZITADEL sub it was started for, never a login: the factory resolves that itself.
type handoffStartReply struct {
	Handoff    string    `json:"handoff"`
	Repository string    `json:"repository"`
	Issue      int       `json:"issue"`
	Sub        string    `json:"sub"`
	Labels     []string  `json:"labels"`
	StartedAt  time.Time `json:"startedAt"`
}

// handoffStart: GET /v1/handoff-starts?repository=&issue=, the link reader's only (LinkReader, the
// factory): the broker's record that it filed issue on repository from a handoff a signed-in human
// started (broker-confirmed handoff). An issue it did not file is a 404 no_handoff_start.
func (s *Server) handoffStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	if s.HandoffStarts == nil || s.LinkReader == "" || p.ID != s.LinkReader {
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	repository := r.URL.Query().Get("repository")
	issue, err := strconv.Atoi(r.URL.Query().Get("issue"))
	if len(repository) > 140 || !handoffRepositoryRE.MatchString(repository) || err != nil || issue <= 0 {
		fail(w, http.StatusBadRequest, wire.ReasonBadHandoffStart)
		return
	}
	h, found, err := s.HandoffStarts.GetHandoffStart(r.Context(), repository, issue, s.now())
	switch {
	case err != nil:
		s.logFailure(w, err, "read a handoff start")
	case !found:
		fail(w, http.StatusNotFound, wire.ReasonNoHandoffStart)
	default:
		reply(w, http.StatusOK, handoffStartReply{Handoff: h.Handoff, Repository: h.Repository, Issue: h.Issue, Sub: h.Sub,
			Labels: h.Labels, StartedAt: h.StartedAt})
	}
}
