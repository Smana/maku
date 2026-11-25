// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/wire"
)

type fakeOpenCodeServer struct {
	mu          sync.Mutex
	sessionID   string
	password    string
	outcome     string
	variant     string
	active      bool
	messages    []json.RawMessage
	permissions []map[string]any
	jobs        []map[string]any
	prompts     []map[string]any
	replies     []map[string]any
	interrupted int
	killedShell []string
	killedChild []string
}

func (f *fakeOpenCodeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.password != "" {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+f.password))
			if r.Header.Get("Authorization") != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/active":
			active := map[string]any{}
			if f.active {
				active[f.sessionID] = map[string]string{"type": "running"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": active})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/"+f.sessionID:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"id":      f.sessionID,
				"outcome": f.outcome,
				"model":   map[string]string{"providerID": "maku", "id": "agent-default", "variant": f.variant},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/"+f.sessionID+"/message":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":   f.messages,
				"cursor": map[string]any{"next": nil},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/"+f.sessionID+"/prompt":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.prompts = append(f.prompts, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "inb_1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/"+f.sessionID+"/permission":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": f.permissions})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/session/"+f.sessionID+"/permission/") && strings.HasSuffix(r.URL.Path, "/reply"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["path"] = r.URL.Path
			f.replies = append(f.replies, body)
			f.permissions = nil
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]bool{"ok": true}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/"+f.sessionID+"/interrupt":
			f.interrupted++
			f.active = false
			f.outcome = "interrupted"
			_ = json.NewEncoder(w).Encode(map[string]any{"interrupted": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/experimental/job":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": f.jobs})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/shell/"):
			f.killedShell = append(f.killedShell, strings.TrimPrefix(r.URL.Path, "/api/shell/"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/session/") && strings.HasSuffix(r.URL.Path, "/interrupt"):
			child := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/interrupt")
			f.killedChild = append(f.killedChild, child)
			_ = json.NewEncoder(w).Encode(map[string]any{"interrupted": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setupOpenCodeFiles(t *testing.T, sid, pw string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	sf := filepath.Join(dir, ".agent-opencode-session-id")
	pf := filepath.Join(dir, ".agent-opencode-password")
	tf := filepath.Join(dir, ".agent-opencode-terminal.json")
	if sid != "" {
		if err := os.WriteFile(sf, []byte(sid+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if pw != "" {
		if err := os.WriteFile(pf, []byte(pw+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sf, pf, tf
}

func TestOpenCodeToolMapsToTheExistingRoomShape(t *testing.T) {
	b, err := os.ReadFile("testdata/opencode-tool.json")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(b, &msgs); err != nil {
		t.Fatal(err)
	}
	raws := normalizeOpenCode(msgs[0])
	if len(raws) != 2 {
		t.Fatalf("got %d raw events, want 2 (action + observation)", len(raws))
	}
	mapped := Map(raws[0], runID)
	if len(mapped) != 2 || mapped[0].Type != envelope.Message || mapped[1].Type != envelope.ToolCall {
		t.Fatalf("mapped action = %+v, want [message, tool_call]", mapped)
	}
	var p envelope.ToolCallPayload
	if err := json.Unmarshal(mapped[1].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.CallID != "call-1" || p.Tool != "shell" {
		t.Fatalf("tool_call = %+v", p)
	}
	c := Classifier{Branch: "agent/3kq7x2ma"}
	if got := c.Classify(p.Tool, p.Args, p.Risk); got != ForgePush {
		t.Fatalf("Classify(shell git push) = %q, want %q", got, ForgePush)
	}
	obs := Map(raws[1], runID)
	if len(obs) != 1 || obs[0].Type != envelope.ToolResult {
		t.Fatalf("mapped obs = %+v", obs)
	}
}

func TestOpenCodeUnknownMalformedAndOversizeStayBounded(t *testing.T) {
	unknown := normalizeOpenCode(json.RawMessage(`{"id":"m1","type":"future_kind","secret":"never-leak"}`))
	if len(unknown) != 1 {
		t.Fatalf("unknown len = %d", len(unknown))
	}
	mapped := Map(unknown[0], runID)
	if len(mapped) != 1 || mapped[0].Type != envelope.StateChanged ||
		!strings.Contains(string(mapped[0].Payload), `"harnessKind":"future_kind"`) ||
		strings.Contains(string(mapped[0].Payload), "never-leak") {
		t.Fatalf("unknown mapped = %+v", mapped)
	}

	malformed := normalizeOpenCode(json.RawMessage(`{"type":"user","text":"missing id"}`))
	if len(malformed) != 1 || !malformed[0].Malformed {
		t.Fatalf("malformed = %+v", malformed)
	}
	mm := Map(malformed[0], runID)
	if len(mm) != 1 || !strings.Contains(string(mm[0].Payload), `"harnessKind":"malformed"`) {
		t.Fatalf("malformed mapped = %+v", mm)
	}

	huge := strings.Repeat("x", envelope.MaxPayload+4096)
	rawOversize, _ := json.Marshal(map[string]any{
		"id":   "m2",
		"type": "assistant",
		"content": []map[string]any{{
			"type": "tool", "id": "c-big", "name": "shell",
			"state": map[string]any{
				"status":  "completed",
				"input":   map[string]string{"command": huge},
				"content": []map[string]string{{"type": "text", "text": huge}},
			},
		}},
	})
	evs := normalizeOpenCode(rawOversize)
	for _, e := range evs {
		for _, m := range Map(e, runID) {
			if len(m.Payload) > envelope.MaxPayload {
				t.Fatalf("mapped payload %s exceeded MaxPayload: %d", m.Type, len(m.Payload))
			}
		}
	}
}

func TestOpenCodeReplaySkipsAcknowledgedEventsAndDeduplicates(t *testing.T) {
	ctx := bounded(t)
	f := &fakeOpenCodeServer{
		sessionID: "ses_abc123",
		password:  "secret-pw",
		active:    true,
		messages: []json.RawMessage{
			json.RawMessage(`{"id":"m1","type":"user","text":"hello"}`),
			json.RawMessage(`{"id":"m2","type":"assistant","content":[{"type":"text","text":"working"}]}`),
			json.RawMessage(`{"id":"m2","type":"assistant","content":[{"type":"text","text":"working"}]}`), // duplicate
			json.RawMessage(`{"id":"m3","type":"assistant","content":[{"type":"text","text":"done"}]}`),
		},
	}
	srv := f.start(t)
	sf, pf, tf := setupOpenCodeFiles(t, f.sessionID, f.password)
	h := NewOpenCodeHarnessWithFiles(srv.URL, sf, pf, tf)

	cur, err := h.Skip(ctx, 2)
	if err != nil || cur.Count != 2 || cur.LastID != "m2:assistant" {
		t.Fatalf("Skip(2) = %+v, %v", cur, err)
	}
	evs, next, err := h.Next(ctx, cur)
	if err != nil || !slices.Equal(ids(evs), []string{"m3:assistant"}) || next.Count != 3 {
		t.Fatalf("Next after Skip = %v %+v %v", ids(evs), next, err)
	}
	// Idle poll returns nothing and holds cursor.
	again, still, err := h.Next(ctx, next)
	if err != nil || len(again) != 0 || still != next {
		t.Fatalf("idle Next = %v %+v %v", ids(again), still, err)
	}
	// Missing cursor ID returns ErrCursorLost.
	if _, _, err := h.Next(ctx, Cursor{LastID: "missing", Count: 1}); !errors.Is(err, ErrCursorLost) {
		t.Fatalf("missing cursor err = %v, want ErrCursorLost", err)
	}
}

func TestOpenCodeMissingOrInvalidSessionFileIs404(t *testing.T) {
	ctx := bounded(t)
	sf, pf, tf := setupOpenCodeFiles(t, "", "pw")
	h := NewOpenCodeHarnessWithFiles("http://127.0.0.1:1", sf, pf, tf)
	if err := h.Ready(ctx); err == nil {
		t.Fatal("missing session file should fail Ready")
	} else if se, ok := errors.AsType[*StatusError](err); !ok || se.Code != http.StatusNotFound {
		t.Fatalf("got %v, want 404 StatusError", err)
	}
	if err := os.WriteFile(sf, []byte("../evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Ready(ctx); err == nil {
		t.Fatal("invalid session id should fail Ready")
	}
}

func TestOpenCodeStatusPermissionsSteeringInterruptAndStuckPrecedence(t *testing.T) {
	ctx := bounded(t)
	f := &fakeOpenCodeServer{
		sessionID:   "ses_ctrl01",
		password:    "pw-ctrl",
		active:      true,
		variant:     "medium",
		permissions: []map[string]any{{"id": "perm_1", "action": "shell", "resources": []string{"git push"}}},
		jobs: []map[string]any{
			{"kind": "shell", "status": "running", "shellID": "sh_1"},
			{"kind": "subagent", "status": "running", "childSessionID": "ses_child1"},
		},
	}
	srv := f.start(t)
	sf, pf, tf := setupOpenCodeFiles(t, f.sessionID, f.password)
	h := NewOpenCodeHarnessWithFiles(srv.URL, sf, pf, tf)

	if err := h.AlwaysConfirm(ctx); err != nil {
		t.Fatalf("AlwaysConfirm: %v", err)
	}
	c, err := h.Conversation(ctx)
	if err != nil || c.Status != statusWaiting || c.Effort != "medium" {
		t.Fatalf("waiting Conversation = %+v, %v", c, err)
	}
	// Run while active returns 409.
	if err := h.Run(ctx); err == nil {
		t.Fatal("Run while active should return 409")
	} else if se, ok := errors.AsType[*StatusError](err); !ok || se.Code != http.StatusConflict {
		t.Fatalf("Run err = %v, want 409", err)
	}
	// Respond rejects with reason, never sending "always".
	if err := h.Respond(ctx, false, "denied by policy"); err != nil {
		t.Fatalf("Respond reject: %v", err)
	}
	if len(f.replies) != 1 || f.replies[0]["decision"] != "reject" || f.replies[0]["message"] != "denied by policy" {
		t.Fatalf("replies = %+v", f.replies)
	}
	// Steering sends via prompt (queue when run=false).
	if err := h.Message(ctx, "queued note", false); err != nil {
		t.Fatalf("Message queue: %v", err)
	}
	if err := h.Send(ctx, "steer now"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.prompts) != 2 || f.prompts[0]["delivery"] != "queue" || f.prompts[1]["text"] != "steer now" {
		t.Fatalf("prompts = %+v", f.prompts)
	}
	// Interrupt stops session AND owned shell/subagent jobs.
	if err := h.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if f.interrupted != 1 || !slices.Equal(f.killedShell, []string{"sh_1"}) || !slices.Equal(f.killedChild, []string{"ses_child1"}) {
		t.Fatalf("interrupt side effects: int=%d shells=%v children=%v", f.interrupted, f.killedShell, f.killedChild)
	}
	c, err = h.Conversation(ctx)
	if err != nil || c.Status != statusPaused {
		t.Fatalf("after interrupt Conversation = %+v, %v", c, err)
	}
	// Invalid terminal marker does NOT change status.
	if err := os.WriteFile(tf, []byte(`{"reason":"stuck","pattern":"unknown_pattern"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := h.Conversation(ctx); err != nil || c.Status != statusPaused {
		t.Fatalf("invalid marker changed status to %+v, %v", c, err)
	}
	// Valid stuck marker takes precedence over interrupted/paused.
	if err := os.WriteFile(tf, []byte(`{"reason":"stuck","pattern":"repeated_action"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := h.Conversation(ctx); err != nil || c.Status != "stuck" {
		t.Fatalf("valid stuck marker Conversation = %+v, %v", c, err)
	}
}

func TestOpenCodeFinalReadMirrorsLastEventThroughBridge(t *testing.T) {
	f := &fakeOpenCodeServer{
		sessionID: "ses_final1",
		password:  "pw-final",
		outcome:   "succeeded",
		messages: []json.RawMessage{
			json.RawMessage(`{"id":"m1","type":"assistant","content":[{"type":"text","text":"all done"}]}`),
		},
	}
	srv := f.start(t)
	sf, pf, tf := setupOpenCodeFiles(t, f.sessionID, f.password)
	fb := &fakeBroker{}
	r := newRig(t, NewOpenCodeHarnessWithFiles(srv.URL, sf, pf, tf), fb)
	ctx, stop := r.run(t)
	res, err := r.b.FinalRead(ctx, "stalled")
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	if res.Events != 1 || res.Unmirrored != 0 {
		t.Fatalf("FinalRead = %+v, want 1 event, 0 unmirrored", res)
	}
	stop()
	if !slices.ContainsFunc(fb.stored(wire.StreamEvents), func(it wire.Item) bool { return payloadHas(it, `"all done"`) }) {
		t.Fatalf("events = %+v, want final message mirrored", fb.stored(wire.StreamEvents))
	}
	if !slices.ContainsFunc(fb.stored(wire.StreamStatus), func(it wire.Item) bool { return payloadHas(it, `"harness_end"`) }) {
		t.Fatalf("status = %+v, want harness_end mirrored", fb.stored(wire.StreamStatus))
	}
}

func TestHarnessKindSelectsOpenCode(t *testing.T) {
	got, err := NewHarnessForKind(HarnessOpenCode, "http://127.0.0.1:8000", conv)
	if err != nil {
		t.Fatalf("NewHarnessForKind(opencode): %v", err)
	}
	if _, ok := got.(*OpenCodeHarness); !ok {
		t.Fatalf("got %T, want *OpenCodeHarness", got)
	}
}
