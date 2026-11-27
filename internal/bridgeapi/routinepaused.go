// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"net/http"
	"slices"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/webhook"
	"github.com/Smana/maku/internal/wire"
)

const maxRoutinePausedBody = 4 << 10

// RoutinePausedEmitter enqueues a routine.paused webhook event for matching subscribers.
type RoutinePausedEmitter interface {
	RoutinePaused(ctx context.Context, repository, routine, reason string) bool
}

// routinePaused: POST /v1/events/routine-paused, system:* only. Validates closed repository,
// routine name, and breaker reason fields, then enqueues routine.paused delivery on the broker.
func (s *Server) routinePaused(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	var in struct {
		Repository string `json:"repository"`
		Routine    string `json:"routine"`
		Reason     string `json:"reason"`
	}
	if err := decodeStrict(r.Body, &in); err != nil {
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	if len(in.Repository) > 140 || !handoffRepositoryRE.MatchString(in.Repository) ||
		!envelope.ValidRoutineName(in.Routine) ||
		!slices.Contains(webhook.BreakerReasons(), in.Reason) {
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	if s.RoutinePaused == nil {
		reply(w, http.StatusOK, map[string]bool{"enqueued": false})
		return
	}
	enqueued := s.RoutinePaused.RoutinePaused(r.Context(), in.Repository, in.Routine, in.Reason)
	reply(w, http.StatusAccepted, map[string]bool{"enqueued": enqueued})
}
