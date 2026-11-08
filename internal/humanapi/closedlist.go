// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/roomctrl"
	"github.com/Smana/maku/internal/summary"
)

// The closed view's bounds: the rooms sealed in the last closedWindow, the last closed first, at
// most closedLimit of them.
const (
	closedWindow = 30 * 24 * time.Hour
	closedLimit  = 200
)

// closedRows is GET /api/rooms?closed=1: the sealed rooms p may open, newest first. The factory
// deletes a task's Room once it ends, so the default list, which reads Room objects, never shows
// them. Each goes through readRoom, the gate /r/<id> takes, so a row is exactly a room its URL
// would open. Its access value is the default list's: unverified when a check could not run.
func (s *Server) closedRows(ctx context.Context, p authn.Principal, repo string) ([]roomRow, string, error) {
	sealed, err := s.Log.SealedSince(ctx, s.now().Add(-closedWindow), closedLimit)
	if err != nil {
		s.log().Warn("closed rooms unreadable", "err", err)
		return nil, "", errLogUnavailable
	}
	access := accessOK
	out := []roomRow{}
	for _, st := range sealed {
		room, row, _, err := s.readRoom(ctx, st.ID, p)
		switch {
		case errors.Is(err, errNoSuchRoom):
			continue
		case err != nil:
			if errors.Is(err, errAccessUnverified) {
				access = accessUnverified
			}
			s.log().Warn("closed room unreadable", "room", st.ID, "err", err)
			continue
		}
		if repo != "" && !strings.EqualFold(room.Spec.Repository, repo) {
			continue
		}
		_, you := s.you(room, p, row.Driver)
		r := roomRow{ID: room.Name, URL: s.PublicURL + "/r/" + room.Name, Phase: "Closed", Owner: room.Spec.Owner,
			DataClass: room.Spec.DataClass, LastSeq: row.LastSeq, Repository: room.Spec.Repository, ClosedAt: st.ClosedAt, You: you}
		r.LastEventAt = st.LastEventAt
		// The task facts the room page folds, so the row names the issue the page does.
		if raw, err := s.Log.LastTaskState(ctx, room.Name, roomctrl.ForkSeq(room)); err == nil && raw != nil {
			var f envelope.TaskFacts
			if json.Unmarshal(raw, &f) == nil {
				r.Task = f.Phase
				if f.Issue != nil {
					r.Issue, r.Title = f.Issue.Number, f.Issue.Title
				}
				if f.PR != nil {
					r.PR = f.PR.Number
				}
			}
		}
		r.State = summary.Coarse(r.Task, r.Phase, false)
		out = append(out, r)
	}
	return out, access, nil
}
