// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/wire"
)

const (
	defaultRange = 100
	maxRange     = 500
)

// systemAuth admits an allowlisted system:* caller (ruling P3).
func (s *Server) systemAuth(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, err := s.Systems.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		s.log().Info("system caller not allowlisted", "path", r.URL.Path)
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return p, false
	case err != nil || p.ID == "":
		s.log().Debug("system credential refused", "path", r.URL.Path, "why", authWhy(err))
		fail(w, http.StatusUnauthorized, wire.ReasonUnauthenticated)
		return p, false
	}
	return p, true
}

// roomEvents: GET /v1/rooms/{id}/events?afterSeq=&limit= (SP3 reads verdicts here).
func (s *Server) roomEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !envelope.ValidID(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadRoom)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("afterSeq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxRange {
		limit = defaultRange
	}
	// Review M4: never a lastSeq of 0 because the database is down.
	st, err := s.Log.Room(r.Context(), id)
	if err != nil {
		s.logFailure(w, err, "read a room", "room", id)
		return
	}
	evs, err := s.Log.Range(r.Context(), id, max(after, 0), limit)
	if err != nil {
		s.logFailure(w, err, "read a room's events", "room", id)
		return
	}
	if evs == nil {
		evs = []envelope.Event{}
	}
	reply(w, http.StatusOK, map[string]any{"events": evs, "lastSeq": st.LastSeq})
}

// roomTask: POST /v1/rooms/{id}/task, system:* only. The factory's structured task facts,
// stored as state_changed{kind:task} so the room summary needs nothing but the room log.
// Replays are keyed on (principal, clientSeq) under its own OriginClient, <principal>:task,
// apart from roomMessage's.
func (s *Server) roomTask(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !envelope.ValidID(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadRoom)
		return
	}
	var in struct {
		ClientSeq int64              `json:"clientSeq"`
		Facts     envelope.TaskFacts `json:"facts"`
	}
	if err := decodeStrict(r.Body, &in); err != nil || in.ClientSeq <= 0 || in.Facts.Validate() != nil {
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	payload, rules, err := s.Redactor.Payload(r.Context(), envelope.TaskStatePayload(in.Facts))
	if err != nil {
		s.log().Warn("task facts not redacted in time", "room", id, "principal", p.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, wire.ReasonTimedOut)
		return
	}
	// Its own OriginClient: a facts clientSeq never collides with a task_state message's.
	ev, dup, err := s.Log.Append(r.Context(), envelope.Draft{RoomID: id,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: p.ID}, Type: envelope.StateChanged,
		Origin: envelope.OriginClient, OriginClient: p.ID + ":task", OriginSeq: in.ClientSeq,
		Redactions: rules, Payload: payload})
	if err != nil {
		s.logFailure(w, err, "append task facts", "room", id, "principal", p.ID)
		return
	}
	code := http.StatusOK
	if !dup {
		code = http.StatusCreated
	}
	reply(w, code, map[string]int64{"seq": ev.Seq})
}

// roomOutputs: POST /v1/rooms/{id}/outputs, system:* only (hardening HR2). The factory's answer
// to a run's output request, or its shadow of a write the run made itself: {runId, kind, record}
// with kind output_applied, output_refused or output_shadowed, never output_requested, which is
// the run's own. Stored as a message stamped with the caller and the run, Delivery none and no
// text. Replays are keyed on <principal>:output:<kind> and a clientSeq derived from the output
// id, so a retry never writes a second answer of a kind, whatever its body now says. Only
// OutputClient may call it, and only for a run of the room (runOfRoom).
func (s *Server) roomOutputs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	if s.OutputClient == "" || p.ID != s.OutputClient {
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !envelope.ValidID(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadRoom)
		return
	}
	var in struct {
		RunID  string                `json:"runId"`
		Kind   envelope.MessageKind  `json:"kind"`
		Record envelope.OutputRecord `json:"record"`
	}
	if err := decodeStrict(r.Body, &in); err != nil || !envelope.ValidID(in.RunID) ||
		(in.Kind != envelope.KindOutputApplied && in.Kind != envelope.KindOutputRefused && in.Kind != envelope.KindOutputShadowed) ||
		in.Record.Validate(in.Kind) != nil {
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	if _, err := s.Log.Room(r.Context(), id); err != nil {
		s.logFailure(w, err, "read a room", "room", id)
		return
	}
	switch mine, err := s.runOfRoom(r, id, in.RunID); {
	case err != nil:
		s.logFailure(w, err, "read a run's events", "room", id)
		return
	case !mine:
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	payload, rules, err := s.Redactor.Payload(r.Context(), envelope.Must(envelope.MessagePayload{
		Kind: in.Kind, Delivery: envelope.DeliveryNone, Output: &in.Record}))
	if err != nil {
		s.log().Warn("output answer not redacted in time", "room", id, "principal", p.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, wire.ReasonTimedOut)
		return
	}
	ev, dup, err := s.Log.Append(r.Context(), envelope.Draft{RoomID: id, RunID: in.RunID,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: p.ID}, Type: envelope.Message,
		Origin: envelope.OriginClient, OriginClient: p.ID + ":output:" + string(in.Kind), OriginSeq: outputSeq(in.Record.ID),
		Redactions: rules, Payload: payload})
	if err != nil {
		s.logFailure(w, err, "append an output answer", "room", id, "principal", p.ID, "kind", string(in.Kind))
		return
	}
	code := http.StatusOK
	if !dup {
		code = http.StatusCreated
	}
	reply(w, code, map[string]int64{"seq": ev.Seq})
}

// runOfRoom reports whether runID is a run of room: while the watch holds the run, its roomRef
// decides; once the watch has let it go, the room's log does, through the run's harness events,
// which its bridge appends only to the room of its own run (hello checks the roomRef). A run that
// never reached its bridge has no events, and no writes to answer for.
func (s *Server) runOfRoom(r *http.Request, room, runID string) (bool, error) {
	if s.Watch != nil {
		if run, _, _ := s.Watch.Live(runID); run.ID != "" { // watched, terminal or not
			return run.Room == room, nil
		}
	}
	for _, st := range []wire.Stream{wire.StreamEvents, wire.StreamStatus} {
		switch n, err := s.Log.Cursor(r.Context(), room, originClient(runID, st)); {
		case err != nil:
			return false, err
		case n > 0:
			return true, nil
		}
	}
	return false, nil
}

// outputSeq is an output id's replay key: its 8 bytes as a positive int64, 1 for the all-zero id.
// The id is 16-hex (OutputRecord.Validate), so the parse cannot fail.
func outputSeq(id string) int64 {
	u, _ := strconv.ParseUint(id, 16, 64)
	if n := int64(u & math.MaxInt64); n != 0 {
		return n
	}
	return 1
}

// roomMessage: POST /v1/rooms/{id}/messages, system:* only, reserved kind task_state
// (C4). 201 for a new event, 200 for a replayed clientSeq; both carry its seq.
// A replay is keyed on (principal, clientSeq) alone: a different body under a
// seen clientSeq also answers 200 with the original seq, and is not stored.
func (s *Server) roomMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !envelope.ValidID(id) { // review M4: 400, not a 503 from the draft check
		fail(w, http.StatusBadRequest, wire.ReasonBadRoom)
		return
	}
	var in struct {
		Kind      envelope.MessageKind `json:"kind"`
		Text      string               `json:"text"`
		ClientSeq int64                `json:"clientSeq"`
	}
	if err := decodeStrict(r.Body, &in); err != nil ||
		in.Kind != envelope.KindTaskState || in.ClientSeq <= 0 || strings.TrimSpace(in.Text) == "" || len(in.Text) > envelope.MaxHumanMessage {
		fail(w, http.StatusBadRequest, wire.ReasonBadMessage)
		return
	}
	payload, rules, err := s.Redactor.Payload(r.Context(), envelope.Must(envelope.MessagePayload{
		Kind: in.Kind, Text: in.Text, Delivery: envelope.DeliveryNone}))
	if err != nil { // the struct always marshals: only an ended request fails here
		s.log().Warn("system message not redacted in time", "room", id, "principal", p.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, wire.ReasonTimedOut)
		return
	}
	ev, dup, err := s.Log.Append(r.Context(), envelope.Draft{RoomID: id,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: p.ID}, Type: envelope.Message,
		Origin: envelope.OriginClient, OriginClient: p.ID, OriginSeq: in.ClientSeq,
		Redactions: rules, Payload: payload})
	if err != nil {
		s.logFailure(w, err, "append a system message", "room", id, "principal", p.ID)
		return
	}
	code := http.StatusOK
	if !dup {
		code = http.StatusCreated
	}
	reply(w, code, map[string]int64{"seq": ev.Seq})
}
