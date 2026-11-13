// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// upstream is a fake knowledge MCP server over Streamable HTTP. answer builds the tools/call
// response's JSON-RPC body; sse sends every answer as one event-stream event.
type upstream struct {
	mu      sync.Mutex
	auth    []string
	methods []string
	args    []json.RawMessage
	session string
	sse     bool
	// hang keeps an event stream open after its answer, as a server that streams more would.
	hang     bool
	delay    time.Duration
	redirect string
	versions []string // MCP-Protocol-Version, by request after initialize
	answer   func(id json.RawMessage) string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if u.redirect != "" {
		http.Redirect(w, r, u.redirect, http.StatusTemporaryRedirect)
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	if r.Method == http.MethodDelete {
		req.Method = "DELETE"
	}
	u.mu.Lock()
	u.auth = append(u.auth, r.Header.Get("Authorization"))
	u.methods = append(u.methods, req.Method)
	if req.Method != "initialize" {
		u.versions = append(u.versions, r.Header.Get("MCP-Protocol-Version"))
	}
	if req.Method == "tools/call" {
		u.args = append(u.args, req.Params.Arguments)
	}
	u.mu.Unlock()
	if u.session != "" && req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != u.session {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var out string
	switch req.Method {
	case "initialize":
		if u.session != "" {
			w.Header().Set("Mcp-Session-Id", u.session)
		}
		out = `{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"runlore","version":"1"}}}`
	case "notifications/initialized", "DELETE":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/call":
		if u.delay > 0 {
			select {
			case <-time.After(u.delay):
			case <-r.Context().Done():
				return
			}
		}
		out = u.answer(req.ID)
	default:
		out = `{"jsonrpc":"2.0","id":` + string(req.ID) + `,"error":{"code":-32601,"message":"no"}}`
	}
	if u.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: "+out+"\n\n")
		if u.hang {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, out)
}

func textAnswer(text string) func(json.RawMessage) string {
	return func(id json.RawMessage) string {
		b, _ := json.Marshal(text)
		return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"content":[{"type":"text","text":` + string(b) + `}]}}`
	}
}

// outcomes records what the knowledge counter was told.
type outcomes struct {
	mu  sync.Mutex
	got []string
}

func (o *outcomes) rec(_ context.Context, tool, outcome string) {
	o.mu.Lock()
	o.got = append(o.got, tool+":"+outcome)
	o.mu.Unlock()
}

func (o *outcomes) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.got...)
}

func knowledgeServer(t *testing.T, role string, u *upstream, cfg KnowledgeConfig) (*Server, *outcomes) {
	t.Helper()
	ts := httptest.NewServer(u)
	t.Cleanup(ts.Close)
	cfg.URL = ts.URL + "/mcp"
	if cfg.Tools == nil {
		cfg.Tools = []string{"kb_search", "kb_get"}
	}
	if cfg.Roles == nil {
		cfg.Roles = []string{"triager"}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 1 << 10
	}
	o := &outcomes{}
	s, _ := testServer(watcher(t, role), newClock())
	// Each call a second apart, so the server's one call a second never refuses one of these.
	var mu sync.Mutex
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); at = at.Add(2 * time.Second); return at }
	s.Tools = append(s.Tools, KnowledgeTools(cfg, httpx.New(cfg.Timeout, nil), o.rec)...)
	return s, o
}

func callArgs(t *testing.T, s *Server, name string, args any) (map[string]any, string) {
	t.Helper()
	res, _ := rpc(t, s, "k", sub, "tools/call", map[string]any{"name": name, "arguments": args}).out["result"].(map[string]any)
	text := ""
	if c, ok := res["content"].([]any); ok && len(c) == 1 {
		text, _ = c[0].(map[string]any)["text"].(string)
	}
	return res, text
}

// EX11: a triager's kb_search reaches the configured endpoint over Streamable HTTP, one stateless
// initialize per call, and comes back as data. The run's token never leaves the broker.
func TestKnowledgeSearchForwardsToTheEndpoint(t *testing.T) {
	for _, sse := range []bool{false, true} {
		u := &upstream{sse: sse, session: "s-1", answer: textAnswer(`[{"path":"incidents/harbor-down.md","score":3.2}]`)}
		s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{})
		if names := listed(t, s); !slices.Contains(names, "kb_search") || !slices.Contains(names, "kb_get") {
			t.Fatalf("sse=%v: tools/list %v", sse, names)
		}
		res, text := callArgs(t, s, "kb_search", map[string]any{"query": "harbor helmrelease upgrade", "k": 3})
		if res["isError"] == true || !strings.Contains(text, "incidents/harbor-down.md") {
			t.Fatalf("sse=%v: result %v", sse, res)
		}
		if sc, _ := res["structuredContent"].(map[string]any); sc["clipped"] != false {
			t.Fatalf("sse=%v: structured %v", sse, sc)
		}
		u.mu.Lock()
		methods, auth, args := u.methods, u.auth, u.args
		u.mu.Unlock()
		if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/call,DELETE" {
			t.Fatalf("sse=%v: methods %v", sse, methods)
		}
		for _, a := range auth {
			if a != "" {
				t.Fatalf("sse=%v: an Authorization reached the endpoint: %q", sse, a)
			}
		}
		if len(args) != 1 || string(args[0]) != `{"query":"harbor helmrelease upgrade","k":3}` {
			t.Fatalf("sse=%v: forwarded arguments %s", sse, args)
		}
		if got := o.list(); len(got) != 1 || got[0] != "kb_search:ok" {
			t.Fatalf("sse=%v: outcomes %v", sse, got)
		}
	}
}

// Only the listed roles see or may call the tools: another role's call is refused and counted.
func TestKnowledgeIsTheTriagersOnly(t *testing.T) {
	u := &upstream{answer: textAnswer("never")}
	s, o := knowledgeServer(t, "implementer", u, KnowledgeConfig{})
	if names := listed(t, s); slices.Contains(names, "kb_search") || slices.Contains(names, "kb_get") {
		t.Fatalf("an implementer lists %v", names)
	}
	res, text := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
	if res["isError"] != true || !strings.HasPrefix(text, "not_permitted") {
		t.Fatalf("result %v", res)
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_search:refused" {
		t.Fatalf("outcomes %v", got)
	}
	if len(u.methods) != 0 {
		t.Fatalf("a refused call reached the endpoint: %v", u.methods)
	}
}

// Only the configured subset is served.
func TestKnowledgeServesOnlyTheConfiguredTools(t *testing.T) {
	s, _ := knowledgeServer(t, "triager", &upstream{answer: textAnswer("x")}, KnowledgeConfig{Tools: []string{"kb_search"}})
	if names := listed(t, s); !slices.Contains(names, "kb_search") || slices.Contains(names, "kb_get") {
		t.Fatalf("tools/list %v", names)
	}
}

func TestKnowledgeTimeoutIsAToolError(t *testing.T) {
	u := &upstream{delay: 2 * time.Second, answer: textAnswer("late")}
	s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{Timeout: 200 * time.Millisecond})
	res, text := callArgs(t, s, "kb_get", map[string]any{"path": "incidents/harbor-down.md"})
	if res["isError"] != true || !strings.HasPrefix(text, "knowledge_timeout") || strings.Contains(text, "late") {
		t.Fatalf("result %v", res)
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_get:timeout" {
		t.Fatalf("outcomes %v", got)
	}
}

func TestKnowledgeClipsAnAnswerOverMaxBytes(t *testing.T) {
	u := &upstream{answer: textAnswer(strings.Repeat("é", 600))} // 1200 bytes, two each
	s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{MaxBytes: 101})
	res, _ := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
	sc, _ := res["structuredContent"].(map[string]any)
	got, _ := sc["text"].(string)
	if res["isError"] == true || sc["clipped"] != true || !strings.HasSuffix(got, knowledgeClipMarker) {
		t.Fatalf("result %v", res)
	}
	if body := strings.TrimSuffix(got, knowledgeClipMarker); len(body) > 101 || len(body) != 100 {
		t.Fatalf("clipped to %d bytes, not at a rune boundary under 101", len(body))
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_search:clipped" {
		t.Fatalf("outcomes %v", got)
	}
}

// A JSON-RPC error, or the tool's own error, is a tool error whose upstream message is cut at 200
// bytes: never a partial answer, never a whole upstream page.
func TestKnowledgeUpstreamErrorIsCut(t *testing.T) {
	long := strings.Repeat("x", 500)
	for name, answer := range map[string]func(json.RawMessage) string{
		"json-rpc error": func(id json.RawMessage) string {
			return `{"jsonrpc":"2.0","id":` + string(id) + `,"error":{"code":-32000,"message":"` + long + `"}}`
		},
		"tool error": func(id json.RawMessage) string {
			return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"isError":true,"content":[{"type":"text","text":"` + long + `"}]}}`
		},
	} {
		s, o := knowledgeServer(t, "triager", &upstream{answer: answer}, KnowledgeConfig{})
		res, text := callArgs(t, s, "kb_get", map[string]any{"path": "a.md"})
		if res["isError"] != true || !strings.HasPrefix(text, "knowledge_unavailable") {
			t.Fatalf("%s: result %v", name, res)
		}
		if strings.Contains(text, strings.Repeat("x", 201)) || !strings.Contains(text, strings.Repeat("x", 200)) {
			t.Fatalf("%s: the upstream message is not cut at 200 bytes: %q", name, text)
		}
		if got := o.list(); len(got) != 1 || got[0] != "kb_get:upstream_error" {
			t.Fatalf("%s: outcomes %v", name, got)
		}
	}
}

func TestKnowledgeBadAnswer(t *testing.T) {
	for name, answer := range map[string]func(json.RawMessage) string{
		"not json":  func(json.RawMessage) string { return "<html>" },
		"no result": func(id json.RawMessage) string { return `{"jsonrpc":"2.0","id":` + string(id) + `}` },
		"no text": func(id json.RawMessage) string {
			return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"content":[]}}`
		},
		"another id": func(json.RawMessage) string {
			return `{"jsonrpc":"2.0","id":99,"result":{"content":[{"type":"text","text":"x"}]}}`
		},
	} {
		s, o := knowledgeServer(t, "triager", &upstream{answer: answer}, KnowledgeConfig{})
		res, text := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
		if res["isError"] != true || !strings.HasPrefix(text, "knowledge_bad_answer") {
			t.Fatalf("%s: result %v", name, res)
		}
		if got := o.list(); len(got) != 1 || got[0] != "kb_search:bad_answer" {
			t.Fatalf("%s: outcomes %v", name, got)
		}
	}
}

// The broker checks the arguments itself and forwards only what it checked.
func TestKnowledgeArgumentsAreChecked(t *testing.T) {
	u := &upstream{answer: textAnswer("x")}
	s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{})
	for name, c := range map[string]struct {
		tool string
		args any
	}{
		"empty query":     {"kb_search", map[string]any{"query": ""}},
		"k over 20":       {"kb_search", map[string]any{"query": "x", "k": 21}},
		"k zero":          {"kb_search", map[string]any{"query": "x", "k": 0}},
		"unknown field":   {"kb_search", map[string]any{"query": "x", "room": "other"}},
		"no path":         {"kb_get", map[string]any{}},
		"path with a NUL": {"kb_get", map[string]any{"path": "a\x00b"}},
	} {
		res, text := callArgs(t, s, c.tool, c.args)
		if res["isError"] != true || !strings.HasPrefix(text, "invalid_arguments") {
			t.Fatalf("%s: result %v", name, res)
		}
	}
	if len(u.methods) != 0 {
		t.Fatalf("a refused argument reached the endpoint: %v", u.methods)
	}
	got := o.list()
	if len(got) != 6 {
		t.Fatalf("outcomes %v", got)
	}
	for _, g := range got {
		if !strings.HasSuffix(g, ":invalid_arguments") {
			t.Fatalf("outcomes %v", got)
		}
	}
}

// Unset, the room tools are what they were.
func TestNoKnowledgeNoTools(t *testing.T) {
	if got := KnowledgeTools(KnowledgeConfig{}, nil, nil); len(got) != 0 {
		t.Fatalf("tools %v", got)
	}
}

// An endpoint the broker cannot reach is upstream_error, and the model is not told its address.
func TestKnowledgeUnreachableNamesNoAddress(t *testing.T) {
	s, o := knowledgeServer(t, "triager", &upstream{answer: textAnswer("x")}, KnowledgeConfig{})
	i := slices.IndexFunc(s.Tools, func(tl Tool) bool { return tl.Name == "kb_search" })
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	s.Tools[i] = KnowledgeTools(KnowledgeConfig{URL: dead.URL + "/mcp", Tools: []string{"kb_search"}, Roles: []string{"triager"},
		Timeout: time.Second, MaxBytes: 1 << 10}, httpx.New(time.Second, nil), o.rec)[0]
	res, text := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
	if res["isError"] != true || text != "knowledge_unavailable: the knowledge base is unreachable" {
		t.Fatalf("result %v", res)
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_search:upstream_error" {
		t.Fatalf("outcomes %v", got)
	}
}

// Every request after initialize names the protocol version, and a session the endpoint handed out
// is closed with DELETE at the end of the call; with none, nothing is deleted.
func TestKnowledgeProtocolVersionAndSessionEnd(t *testing.T) {
	for _, session := range []string{"s-1", ""} {
		u := &upstream{session: session, answer: textAnswer("x")}
		s, _ := knowledgeServer(t, "triager", u, KnowledgeConfig{})
		if res, _ := callArgs(t, s, "kb_search", map[string]any{"query": "x"}); res["isError"] == true {
			t.Fatalf("session %q: %v", session, res)
		}
		u.mu.Lock()
		methods, versions := strings.Join(u.methods, ","), u.versions
		u.mu.Unlock()
		want := "initialize,notifications/initialized,tools/call"
		if session != "" {
			want += ",DELETE"
		}
		if methods != want {
			t.Fatalf("session %q: methods %s, want %s", session, methods, want)
		}
		for _, v := range versions {
			if v != knowledgeProtocol {
				t.Fatalf("session %q: versions %v", session, versions)
			}
		}
	}
}

// A redirect is the endpoint's answer, never followed: the run's call goes nowhere else.
func TestKnowledgeDoesNotFollowRedirects(t *testing.T) {
	var hits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { mu.Lock(); hits++; mu.Unlock() }))
	t.Cleanup(other.Close)
	s, o := knowledgeServer(t, "triager", &upstream{redirect: other.URL + "/mcp", answer: textAnswer("x")}, KnowledgeConfig{})
	res, text := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
	if res["isError"] != true || !strings.HasPrefix(text, "knowledge_unavailable") {
		t.Fatalf("result %v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("the redirect was followed %d times", hits)
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_search:upstream_error" {
		t.Fatalf("outcomes %v", got)
	}
}

// An event stream is read up to the answer with the call's id, not to its end.
func TestKnowledgeSSEStopsAtItsAnswer(t *testing.T) {
	u := &upstream{sse: true, hang: true, answer: textAnswer("found")}
	s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{Timeout: 3 * time.Second})
	start := time.Now()
	res, _ := callArgs(t, s, "kb_search", map[string]any{"query": "x"})
	if res["isError"] == true {
		t.Fatalf("result %v", res)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the call waited %s for the stream to end", d)
	}
	if got := o.list(); len(got) != 1 || got[0] != "kb_search:ok" {
		t.Fatalf("outcomes %v", got)
	}
}

// A response over max(8 × maxBytes, 1 MiB) is not read to its end and not clipped: it is bad_answer.
func TestKnowledgeOversizeIsABadAnswer(t *testing.T) {
	for _, sse := range []bool{false, true} {
		u := &upstream{sse: sse, answer: textAnswer(strings.Repeat("a", 2<<20))}
		s, o := knowledgeServer(t, "triager", u, KnowledgeConfig{MaxBytes: 1 << 10})
		res, text := callArgs(t, s, "kb_get", map[string]any{"path": "a.md"})
		if res["isError"] != true || !strings.HasPrefix(text, "knowledge_bad_answer") {
			t.Fatalf("sse=%v: result %v", sse, text)
		}
		if got := o.list(); len(got) != 1 || got[0] != "kb_get:bad_answer" {
			t.Fatalf("sse=%v: outcomes %v", sse, got)
		}
	}
}
