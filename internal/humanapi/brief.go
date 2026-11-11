// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomctrl"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/summary"
)

// briefVersion is the resume brief's one envelope, on both doors (CD11).
const briefVersion = "brief/v1"

// briefLimit is how many milestones the brief's changed block shows: the page is cut, the rest counted as
// "N more", never dropped in silence.
const briefLimit = 20

// briefScopePage is the tail the scope search reads first, wider each round: a resumed room is the one the
// brief is for, and its scoper's handoff is early in the log, so one fixed window would lose the checks.
const briefScopePage = int64(pageSize)

// briefSinceForms is what a brief's `since` may be, said once: the route's 400 and the tool's refusal read
// the same, name both forms, and never read a malformed one as "everything" or as "nothing".
const briefSinceForms = "since is seq:N (the cursor of the last brief) or an RFC 3339 instant, and never in the future"

// briefQuestion is the scoper's question and its first assumption, for a client that can answer it.
type briefQuestion struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

// briefEnvelope is the brief's answer: the rendered text, the room it is of, and the cursor the caller
// passes back as `since`. The route and room_brief serve this one shape.
type briefEnvelope struct {
	APIVersion string         `json:"apiVersion"`
	Room       string         `json:"room"`
	Text       string         `json:"text"`
	Cursor     string         `json:"cursor"`
	Question   *briefQuestion `json:"question,omitempty"`
}

// briefSince is where the brief's changes start: the cursor a previous brief handed back (seq:N), an RFC
// 3339 instant, or neither — the last 24 hours, as room_changes reads without a cursor.
type briefSince struct {
	seq    int64
	at     time.Time
	hasSeq bool
}

// briefWindow is how far back the changed block looks without a `since`, as room_changes does.
const briefWindow = changesWindow

// parseBriefSince reads the brief's `since`. A future instant is refused with the malformed ones: it would
// filter every dated event away and answer a quiet brief nobody is in (Review Focus 3, at this door too).
func parseBriefSince(raw string, now time.Time) (briefSince, error) {
	switch {
	case raw == "":
		return briefSince{}, nil
	case strings.HasPrefix(raw, "seq:"):
		n, err := strconv.ParseInt(strings.TrimPrefix(raw, "seq:"), 10, 64)
		if err != nil || n < 0 {
			return briefSince{}, errors.New(briefSinceForms)
		}
		return briefSince{seq: n, hasSeq: true}, nil
	default:
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil || at.After(now) {
			return briefSince{}, errors.New(briefSinceForms)
		}
		return briefSince{at: at}, nil
	}
}

// roomBriefRoute is GET /api/rooms/{id}/brief?since=…: the resume brief, the human door (CD11). It is a
// room read like the summary, so the same authentication, D7 gate, Read check and read rate limit stand in
// front of it; a sponsored token reaches no route here.
func (s *Server) roomBriefRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowed(w, r, p) {
		return
	}
	since, err := parseBriefSince(r.URL.Query().Get("since"), s.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	answer, err := s.briefOf(ctx, r.PathValue("id"), p, since)
	if err != nil {
		http.Error(w, err.Error(), roomStatus(err))
		return
	}
	writeJSON(w, answer)
}

type briefArgs struct {
	Room  string `json:"room"`
	Since string `json:"since"`
}

// roomBriefTool is room_brief: the route's brief and cursor for a client that reaches the broker through
// /mcp (CD11), so an agent starting a session reads what a person reads. It is on the human list alone: a
// sponsored principal has no developer whose work it resumes (R13).
func (s *Server) roomBriefTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a briefArgs
	bad := toolError("invalid_arguments: " + briefSinceForms)
	if mcpwire.DecodeArgs(args, &a) != nil || a.Room == "" {
		return nil, bad
	}
	since, err := parseBriefSince(a.Since, s.now())
	if err != nil {
		return nil, bad
	}
	answer, err := s.briefOf(ctx, a.Room, p, since)
	if err != nil {
		return nil, err
	}
	return viewResult{text: answer.Text, data: answer}, nil
}

// briefOf is the resume brief of room id as p reads it: the room's facts, the last handoff's scope, the
// milestones since `since`, the one need that stands in the way (CD11) and the one failure that need is —
// with its fix (fast-follow 6). It is deterministic — the same log and the same cursor give the same text —
// and it decides nothing: every line is a statement, and the one thing to do is an action whose URL the
// broker built.
func (s *Server) briefOf(ctx context.Context, id string, p authn.Principal, since briefSince) (briefEnvelope, error) {
	room, row, sub, err := s.readRoom(ctx, id, p)
	if err != nil {
		return briefEnvelope{}, err
	}
	fold, err := s.foldOf(ctx, room, row)
	if err != nil {
		return briefEnvelope{}, err
	}
	scope, err := s.lastScope(ctx, room, row)
	if err != nil {
		return briefEnvelope{}, err
	}
	var question *briefQuestion
	if scope != nil && scope.State == envelope.ScopeQuestion {
		question = &briefQuestion{Text: scope.Question}
		if len(scope.Assumptions) > 0 {
			question.Recommended = scope.Assumptions[0]
		}
	}
	changed, cursor, at, err := s.briefChanges(ctx, room, row, since)
	if err != nil {
		return briefEnvelope{}, err
	}
	in := roomview.BriefIn{Room: id, RoomURL: s.PublicURL + "/r/" + id, Facts: fold.Facts, Sealed: fold.Sealed,
		Scope: scope, Since: at, Changed: changed, Now: s.now()}
	// Fast-follow 6: the one failure the room's facts and notes name, and its fix. The fold's notes are
	// already in hand, so the brief reads nothing new to say it.
	in.Cause, _ = summary.Cause(fold.Facts, fold.Notes)
	if f := fold.Facts; f != nil {
		in.Issue, in.PR = f.Issue, f.PR
	}
	if scope != nil {
		// The plan line's scope half (SC6) says which plan ran: the scope is what the brief prints, so the
		// caller names it here too and the state cannot go missing between one field and the next.
		in.Plan = roomview.PlanVersion{Source: "scope"}
	}
	coord := s.coordinationOnce(ctx, id, p) // one read serves the plan line and the blocker
	if v, ok := coord(); ok {
		hash := v.PlanHash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		in.Plan = roomview.PlanVersion{Source: "coordination", Rev: int(v.Plan.Version), Hash: hash, CeilingMicroUSD: v.Budget.TotalMicroUSD}
		in.Coordination = &v
	}
	web := sub
	web.WebUI = true
	live, _ := s.liveApprovals(ctx, room, web)
	if items, _ := s.needsFor(ctx, room, p, sub, false, needsRead{facts: s.taskFacts(ctx, room), live: live, coord: coord}); len(items) > 0 {
		in.Blocker = &items[0]
	}
	var text strings.Builder
	if err := roomview.Brief(&text, in, s.viewOptions(false)); err != nil {
		return briefEnvelope{}, err
	}
	return briefEnvelope{APIVersion: briefVersion, Room: id, Text: text.String(), Cursor: cursor, Question: question}, nil
}

// lastScope is the last handoff's scope (SC6): the state the plan line prints and the acceptance checks the
// brief quotes — the newest handoff carrying one, or nil when no handoff ever did. The fold keeps no scope,
// so this reads the log: the last briefScopePage events, then widening towards its start. A fixed tail
// would not do — the scoper hands off early, and a room resumed after many runs has a long log since.
func (s *Server) lastScope(ctx context.Context, room *v1alpha1.Room, row store.RoomState) (*envelope.Scope, error) {
	fork := roomctrl.ForkSeq(room)
	for window := briefScopePage; ; window *= 4 {
		start := max(fork, row.LastSeq-window)
		var found *envelope.Scope
		for cursor := start; cursor < row.LastSeq; {
			n := int(min(int64(pageSize), row.LastSeq-cursor))
			page, err := s.Log.Range(ctx, room.Name, cursor, n)
			if err != nil {
				s.log().Warn("brief: log unreadable", "room", room.Name, "err", err)
				return nil, errLogUnreadable
			}
			for _, e := range page {
				cursor = e.Seq
				if e.RoomID != room.Name || e.Type != envelope.Handoff {
					continue
				}
				// The newest one wins: a scope is replaced by the scoper's next handoff, never by the
				// handoffs after it that carry none.
				var h envelope.HandoffPayload
				if json.Unmarshal(e.Payload, &h) == nil && h.Scope != nil {
					found = h.Scope
				}
			}
			if len(page) < n {
				break
			}
		}
		if found != nil || start <= fork {
			return found, nil
		}
	}
}

// briefChanges is what changed since `since`: room_changes' scan, so one page covers at most tailScan
// events and the page is cut at briefLimit with the rest counted. It returns the milestones, the cursor the
// caller passes back next time (the last event read), and the instant the "Changed since" header prints —
// the instant the caller named, or the cursor event's own ts. Base is nil: the page is measured against an
// empty state, so its first run, pull request and merge are news (MilestoneInput's contract for a window
// the caller cannot date).
func (s *Server) briefChanges(ctx context.Context, room *v1alpha1.Room, row store.RoomState,
	since briefSince) (roomview.ChangeSet, string, time.Time, error) {
	fork, cutoff, at := roomctrl.ForkSeq(room), s.now().Add(-briefWindow), since.at
	from := max(row.LastSeq-tailScan, fork)
	if since.hasSeq {
		from = min(max(since.seq, fork), row.LastSeq)
		// One event back: the cursor's own ts is the header's instant, and the event itself is no change.
		from = max(from-1, fork)
	}
	var events []envelope.Event
	cursor := from
	for scanned := 0; scanned < tailScan && cursor < row.LastSeq; {
		n := int(min(int64(pageSize), int64(tailScan-scanned), row.LastSeq-cursor))
		page, err := s.Log.Range(ctx, room.Name, cursor, n)
		if err != nil {
			s.log().Warn("brief: log unreadable", "room", room.Name, "err", err)
			return roomview.ChangeSet{}, "", time.Time{}, errLogUnreadable
		}
		for _, e := range page {
			cursor = e.Seq
			if e.RoomID != room.Name {
				continue
			}
			switch {
			case since.hasSeq && e.Seq <= since.seq:
				at = e.TS
			case since.hasSeq:
				events = append(events, e)
			case since.at.IsZero() && e.TS.Before(cutoff):
				// Older than the window without a cursor: room_changes' baseline, not a milestone.
			case !since.at.IsZero() && !e.TS.After(since.at):
				// At or before the instant the caller named: the previous brief already said it.
			default:
				events = append(events, e)
			}
		}
		scanned += len(page)
		if len(page) < n {
			break
		}
	}
	set := roomview.Milestones(roomview.MilestoneInput{Room: room.Name, URL: s.PublicURL + "/r/" + room.Name,
		Events: events, Limit: briefLimit})
	if cursor < row.LastSeq && set.More == 0 {
		set.More = 1 // the scan stopped at its bound: there is more to read, not how much
	}
	if set.Cursor == "" {
		set.Cursor = "seq:" + strconv.FormatInt(cursor, 10)
	}
	return set, set.Cursor, at, nil
}
