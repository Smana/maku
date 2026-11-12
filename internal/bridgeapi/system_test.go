// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// shadowed is a valid output_shadowed record: no payload, as no answer carries one.
func shadowed(id string) envelope.OutputRecord {
	return envelope.OutputRecord{ID: id, Type: "comment", Digest: strings.Repeat("a", 64), Bytes: 40,
		Object: "https://github.com/o/r/issues/7#issuecomment-1",
		Reason: "output_schema", Audit: []envelope.OutputFinding{{Layer: "t5_classifier", Reason: "injection_suspected"}}}
}

// outputServer is a broker that serves the outputs route to the factory, with runA watched in room.
func outputServer(t *testing.T) (*Server, *memLog, *runwatch.Watcher) {
	t.Helper()
	s, log, w := newServer(t)
	s.OutputClient = factory
	w.Upsert(t.Context(), agentRun(runA, room, "Succeeded"))
	return s, log, w
}

// HR2: the factory's output answers and shadows reach the room through one system route, stamped
// as the factory, keyed so a retry never writes a second answer.
func TestRoomOutputs(t *testing.T) {
	s, log, _ := outputServer(t)
	h := s.Routes()
	path := "/v1/rooms/" + room + "/outputs"
	body := map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": shadowed("0123456789abcdef")}
	rec := call(t, h, http.MethodPost, path, "sys:"+factory, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var first map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil || first["seq"] != 1 {
		t.Fatalf("reply %s", rec.Body)
	}
	evs := log.stored()
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	e := evs[0]
	if e.Type != envelope.Message || e.RunID != runA || e.Actor != (envelope.Actor{Kind: envelope.ActorSystem, ID: factory}) ||
		e.Origin != envelope.OriginClient {
		t.Fatalf("event %+v", e)
	}
	kind, got, ok := envelope.ReadOutput(e, runA)
	if !ok || kind != envelope.KindOutputShadowed || got.ID != "0123456789abcdef" || got.Reason != "output_schema" || len(got.Audit) != 1 {
		t.Fatalf("ReadOutput %q %+v %v (payload %s)", kind, got, ok, e.Payload)
	}
	var p envelope.MessagePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.Delivery != envelope.DeliveryNone || p.Text != "" {
		t.Fatalf("payload %s", e.Payload)
	}

	// The same id and kind again is a replay, whatever the body now says.
	again := shadowed("0123456789abcdef")
	again.Reason = "would_wait"
	if rec := call(t, h, http.MethodPost, path, "sys:"+factory,
		map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": again}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"seq":1`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	// Another kind for the same id is its own key.
	applied := shadowed("0123456789abcdef")
	applied.Reason = ""
	if rec := call(t, h, http.MethodPost, path, "sys:"+factory,
		map[string]any{"runId": runA, "kind": envelope.KindOutputApplied, "record": applied}); rec.Code != http.StatusCreated {
		t.Fatalf("applied: %d %s", rec.Code, rec.Body)
	}
	// An all-zero id still keys on a positive clientSeq.
	if rec := call(t, h, http.MethodPost, path, "sys:"+factory,
		map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": shadowed("0000000000000000")}); rec.Code != http.StatusCreated {
		t.Fatalf("zero id: %d %s", rec.Code, rec.Body)
	}
	if n := len(log.stored()); n != 3 {
		t.Fatalf("%d events, want 3", n)
	}
}

// memBundles is the store's bundle read, keyed room/run/digest.
type memBundles map[string][]byte

func (m memBundles) Bundle(_ context.Context, room, run, digest string) ([]byte, error) {
	if b, ok := m[room+"/"+run+"/"+digest]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("store: read bundle: %w", store.ErrNoBundle)
}

// HR22: the factory, and only the factory, reads a push request's bundle by room, run and digest.
func TestRoomBundle(t *testing.T) {
	b := []byte("# v2 git bundle\nbytes\n")
	d := strings.Repeat("b", 64)
	s, _, _ := outputServer(t)
	s.Bundles = memBundles{room + "/" + runA + "/" + d: b}
	h := s.Routes()
	path := func(room, digest, run string) string {
		return "/v1/rooms/" + room + "/bundles/" + digest + "?run=" + run
	}
	rec := call(t, h, http.MethodGet, path(room, d, runA), "sys:"+factory, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-git-bundle" || !bytes.Equal(rec.Body.Bytes(), b) {
		t.Fatalf("%d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	}
	for name, c := range map[string]struct {
		path, token string
		code        int
		reason      string
	}{
		"another listed system": {path(room, d, runA), "sys:system:triage", http.StatusForbidden, wire.ReasonNotPermitted},
		"an unlisted system":    {path(room, d, runA), "sys:forbidden", http.StatusForbidden, wire.ReasonNotPermitted},
		"a run token":           {path(room, d, runA), "run:" + runA, http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"a bad digest":          {path(room, "XYZ", runA), "sys:" + factory, http.StatusBadRequest, wire.ReasonBadBundle},
		"a bad run":             {path(room, d, "Run_A"), "sys:" + factory, http.StatusBadRequest, wire.ReasonBadBundle},
		"no run":                {"/v1/rooms/" + room + "/bundles/" + d, "sys:" + factory, http.StatusBadRequest, wire.ReasonBadBundle},
		"a bad room":            {path("NOPE", d, runA), "sys:" + factory, http.StatusBadRequest, wire.ReasonBadRoom},
		"a missing bundle":      {path(room, strings.Repeat("c", 64), runA), "sys:" + factory, http.StatusNotFound, wire.ReasonNoBundle},
		"another room's bundle": {path("abcdefgh", d, runA), "sys:" + factory, http.StatusNotFound, wire.ReasonNoBundle},
		"another run's bundle":  {path(room, d, runB), "sys:" + factory, http.StatusNotFound, wire.ReasonNoBundle},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, http.MethodGet, c.path, c.token, nil)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.reason)
			}
		})
	}
	t.Run("a broker with no bundle store", func(t *testing.T) {
		s.Bundles = nil
		if rec := call(t, s.Routes(), http.MethodGet, path(room, d, runA), "sys:"+factory, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%d", rec.Code)
		}
	})
}

// HR27: the factory posts a held push's output_waiting through the same route, keyed apart from
// its later answer.
func TestRoomOutputsWaiting(t *testing.T) {
	s, log, _ := outputServer(t)
	h := s.Routes()
	path := "/v1/rooms/" + room + "/outputs"
	rec := envelope.OutputRecord{ID: "0123456789abcdef", Type: "push", Digest: strings.Repeat("a", 64), Bytes: 4096,
		Reason: envelope.WaitingReason, ApprovalID: "01JC0000000000000000000000"}
	if r := call(t, h, http.MethodPost, path, "sys:"+factory,
		map[string]any{"runId": runA, "kind": envelope.KindOutputWaiting, "record": rec}); r.Code != http.StatusCreated {
		t.Fatalf("waiting: %d %s", r.Code, r.Body)
	}
	refused := envelope.OutputRecord{ID: rec.ID, Type: "push", Digest: rec.Digest, Reason: "approval_denied"}
	if r := call(t, h, http.MethodPost, path, "sys:"+factory,
		map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}); r.Code != http.StatusCreated {
		t.Fatalf("refused after waiting: %d %s", r.Code, r.Body)
	}
	evs := log.stored()
	if len(evs) != 2 {
		t.Fatalf("%d events", len(evs))
	}
	if k, got, ok := envelope.ReadOutput(evs[0], runA); !ok || k != envelope.KindOutputWaiting || got.ApprovalID != rec.ApprovalID {
		t.Fatalf("ReadOutput %q %+v %v", k, got, ok)
	}
}

// A run is the room's while the watch names the room as its roomRef, or, once the watch has let
// it go, while its bridge's harness events are in the room's log: a bridge only ever appends to
// its own run's room.
func TestRoomOutputsOnlyForTheRoomsRuns(t *testing.T) {
	ended := "bbbbbbbb"
	for _, c := range []struct {
		name  string
		runID string
		seed  func(*memLog, *runwatch.Watcher)
		code  int
	}{
		{"a watched run of the room", runA, nil, http.StatusCreated},
		{"a watched run of another room", runB, func(_ *memLog, w *runwatch.Watcher) {
			w.Upsert(t.Context(), agentRun(runB, "abcdefgh", "Running"))
		}, http.StatusBadRequest},
		{"a run the watch let go, its events in the room", ended, func(l *memLog, _ *runwatch.Watcher) {
			appendHarness(t, l, room, ended, "agent:"+ended)
		}, http.StatusCreated},
		{"a run the watch let go, only its status in the room", ended, func(l *memLog, _ *runwatch.Watcher) {
			appendHarness(t, l, room, ended, "agent:"+ended+":status")
		}, http.StatusCreated},
		{"a run the watch let go, its events in another room", ended, func(l *memLog, _ *runwatch.Watcher) {
			appendHarness(t, l, "abcdefgh", ended, "agent:"+ended)
		}, http.StatusBadRequest},
		{"a run the broker never saw", "cccccccc", nil, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, log, w := outputServer(t)
			if c.seed != nil {
				c.seed(log, w)
			}
			before := len(log.stored())
			rec := call(t, s.Routes(), http.MethodPost, "/v1/rooms/"+room+"/outputs", "sys:"+factory,
				map[string]any{"runId": c.runID, "kind": envelope.KindOutputShadowed, "record": shadowed("0123456789abcdef")})
			if rec.Code != c.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, c.code)
			}
			if c.code == http.StatusBadRequest && (reason(t, rec) != wire.ReasonBadMessage || len(log.stored()) != before) {
				t.Fatalf("refused as %q, %d events written", reason(t, rec), len(log.stored())-before)
			}
		})
	}
}

// appendHarness appends one harness event of run to roomID under originClient, as its bridge does.
func appendHarness(t *testing.T, l *memLog, roomID, run, originClient string) {
	t.Helper()
	if _, _, err := l.Append(t.Context(), envelope.Draft{RoomID: roomID, RunID: run, Type: envelope.Message,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run}, Origin: envelope.OriginHarness,
		OriginClient: originClient, OriginSeq: 1, Payload: chat(1, "hi").Payload}); err != nil {
		t.Fatal(err)
	}
}

func TestRoomOutputsRefuseBadRequests(t *testing.T) {
	ok := shadowed("0123456789abcdef")
	bad := ok
	bad.Object = ""
	carried := ok
	carried.Payload = json.RawMessage(`{"body":"the written text"}`)
	refused := envelope.OutputRecord{ID: ok.ID, Type: "pr", Digest: ok.Digest, Reason: "output_schema"}
	for name, c := range map[string]struct {
		path, token string
		body        any
		unserved    bool
		code        int
		reason      string
	}{
		"a request is the run's, never the factory's": {"", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRequested, "record": envelope.OutputRecord{ID: ok.ID, Type: "pr", Digest: ok.Digest}},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"another kind": {"", "sys:" + factory, map[string]any{"runId": runA, "kind": envelope.KindChat, "record": ok},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"a record that fails Validate": {"", "sys:" + factory, map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": bad},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"a shadow carrying the written text": {"", "sys:" + factory, map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": carried},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"no runId": {"", "sys:" + factory, map[string]any{"kind": envelope.KindOutputShadowed, "record": ok},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"a malformed runId": {"", "sys:" + factory, map[string]any{"runId": "Run_A", "kind": envelope.KindOutputShadowed, "record": ok},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"an unknown field": {"", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused, "extra": true},
			false, http.StatusBadRequest, wire.ReasonBadMessage},
		"a bad room": {"/v1/rooms/NOPE/outputs", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, false, http.StatusBadRequest, wire.ReasonBadRoom},
		"a run token": {"", "run:" + runA, map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused},
			false, http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"an unlisted system": {"", "sys:forbidden", map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused},
			false, http.StatusForbidden, wire.ReasonNotPermitted},
		"another listed system": {"", "sys:system:triage", map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused},
			false, http.StatusForbidden, wire.ReasonNotPermitted},
		"a principal that only starts like the factory's": {"", "sys:" + factory + "-b",
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, false, http.StatusForbidden, wire.ReasonNotPermitted},
		"a broker that names no output client": {"", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, true, http.StatusForbidden, wire.ReasonNotPermitted},
		"no room log": {"/v1/rooms/zzzzzzzz/outputs", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, false, http.StatusNotFound, wire.ReasonNoRoom},
	} {
		t.Run(name, func(t *testing.T) {
			s, log, _ := outputServer(t)
			if c.unserved {
				s.OutputClient = ""
			}
			path := c.path
			if path == "" {
				path = "/v1/rooms/" + room + "/outputs"
			}
			rec := call(t, s.Routes(), http.MethodPost, path, c.token, c.body)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.reason)
			}
			log.untouched(t)
		})
	}
}
