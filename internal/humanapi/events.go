// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Smana/maku/internal/envelope"
)

// roomEvents is GET /api/rooms/{id}/events?after=N&limit=M: one page of the room's log in seq
// order, for the run view. It is a room read like the WebSocket, whose replay filters nothing
// per viewer either, so it takes the same D7 gate and Read check and answers an unreadable
// room as a missing one.
func (s *Server) roomEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var after int64
	limit := pageSize
	q := r.URL.Query()
	if v := q.Get("after"); v != "" {
		var err error
		if after, err = strconv.ParseInt(v, 10, 64); err != nil || after < 0 {
			http.Error(w, "after is a sequence number", http.StatusBadRequest)
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		var err error
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > pageSize {
			http.Error(w, "limit is 1 to "+strconv.Itoa(pageSize), http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	id := r.PathValue("id")
	if _, _, _, err := s.readRoom(ctx, id, p); err != nil {
		http.Error(w, err.Error(), roomStatus(err))
		return
	}
	page, err := s.Log.Range(ctx, id, after, limit)
	if err != nil {
		s.log().Warn("events: log unreadable", "room", id, "err", err)
		http.Error(w, errLogUnreadable.Error(), roomStatus(errLogUnreadable))
		return
	}
	if page == nil {
		page = []envelope.Event{}
	}
	writeJSON(w, struct {
		Events []envelope.Event `json:"events"`
	}{page})
}
