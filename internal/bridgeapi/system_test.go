// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/wire"
)

// shadowed is a valid output_shadowed record whose payload quotes a GitHub token.
func shadowed(id string) envelope.OutputRecord {
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 3) + "f6G7h8"
	return envelope.OutputRecord{ID: id, Type: "comment", Digest: strings.Repeat("a", 64),
		Payload: json.RawMessage(`{"body":"` + token + `"}`), Object: "https://github.com/o/r/issues/7#issuecomment-1",
		Reason: "output_schema", Audit: []envelope.OutputFinding{{Layer: "t5_classifier", Reason: "injection_suspected"}}}
}

// HR2: the factory's output answers and shadows reach the room through one system route, stamped
// as the factory, keyed so a retry never writes a second answer.
func TestRoomOutputs(t *testing.T) {
	s, log, _ := newServer(t)
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
	if strings.Contains(string(e.Payload), "ghp_") || len(e.Redactions) == 0 {
		t.Fatalf("not redacted: %s %v", e.Payload, e.Redactions)
	}

	// The same id and kind again is a replay, whatever the body now says.
	again := map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": shadowed("0123456789abcdef")}
	if rec := call(t, h, http.MethodPost, path, "sys:"+factory, again); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"seq":1`) {
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

func TestRoomOutputsRefuseBadRequests(t *testing.T) {
	ok := shadowed("0123456789abcdef")
	bad := ok
	bad.Object = ""
	refused := envelope.OutputRecord{ID: ok.ID, Type: "pr", Digest: ok.Digest, Reason: "output_schema"}
	for name, c := range map[string]struct {
		path, token string
		body        any
		code        int
		reason      string
	}{
		"a request is the run's, never the factory's": {"", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRequested, "record": envelope.OutputRecord{ID: ok.ID, Type: "pr", Digest: ok.Digest}},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"another kind": {"", "sys:" + factory, map[string]any{"runId": runA, "kind": envelope.KindChat, "record": ok},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"a record that fails Validate": {"", "sys:" + factory, map[string]any{"runId": runA, "kind": envelope.KindOutputShadowed, "record": bad},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"no runId": {"", "sys:" + factory, map[string]any{"kind": envelope.KindOutputShadowed, "record": ok},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"a malformed runId": {"", "sys:" + factory, map[string]any{"runId": "Run_A", "kind": envelope.KindOutputShadowed, "record": ok},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"an unknown field": {"", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused, "extra": true},
			http.StatusBadRequest, wire.ReasonBadMessage},
		"a bad room": {"/v1/rooms/NOPE/outputs", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, http.StatusBadRequest, wire.ReasonBadRoom},
		"a run token": {"", "run:" + runA, map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused},
			http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"an unlisted system": {"", "sys:forbidden", map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused},
			http.StatusForbidden, wire.ReasonNotPermitted},
		"no room log": {"/v1/rooms/zzzzzzzz/outputs", "sys:" + factory,
			map[string]any{"runId": runA, "kind": envelope.KindOutputRefused, "record": refused}, http.StatusNotFound, wire.ReasonNoRoom},
	} {
		t.Run(name, func(t *testing.T) {
			s, log, _ := newServer(t)
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
