// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// upstream is a fake Playwright MCP server: it answers tools/list with seven tools, a screenshot
// with a 900 KiB PNG and a navigation with 0.0.85's text, as JSON or as one SSE event.
type upstream struct {
	*httptest.Server
	sse bool
	png []byte

	mu      sync.Mutex
	called  map[string]int
	headers []http.Header
}

func fakeUpstream(t *testing.T, sse bool) *upstream {
	t.Helper()
	u := &upstream{sse: sse, png: noisePNG(t, 560, 560), called: map[string]int{}}
	if len(u.png) < 800<<10 {
		t.Fatalf("fixture %d bytes", len(u.png))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", u.post)
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		img := base64.StdEncoding.EncodeToString(u.png[:64])
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		_, _ = fmt.Fprintf(w, "event: message\nid: 7\ndata: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"content\":[{\"type\":\"image\",\"mimeType\":\"image/png\",\"data\":%q}]}}\n\n", img)
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) calls(name string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.called[name]
}

func (u *upstream) post(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	u.mu.Lock()
	u.called[req.Params.Name]++
	u.headers = append(u.headers, r.Header.Clone())
	u.mu.Unlock()
	w.Header().Set("Mcp-Session-Id", "s-1")
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch {
	case req.Method == "tools/list":
		var tools []map[string]any
		for _, n := range []string{"browser_navigate", "browser_snapshot", "browser_take_screenshot", "browser_evaluate", "browser_run_code_unsafe", "browser_click", "browser_file_upload"} {
			tools = append(tools, map[string]any{"name": n, "inputSchema": map[string]any{"type": "object"}})
		}
		result = map[string]any{"tools": tools}
	case req.Params.Name == ScreenshotTool:
		result = map[string]any{"content": []map[string]any{
			{"type": "text", "text": "### Result\n- [Screenshot of viewport](../../tmp/out/page-1.png)\n### Ran Playwright code\n"},
			{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(u.png)},
		}}
	default:
		result = map[string]any{"content": []map[string]any{{"type": "text",
			"text": "### Page\n- Page URL: http://127.0.0.1:8080/docs\n- Page Title: Docs\n- [Snapshot](../../tmp/out/page-2.yml)\n"}}}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	if u.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

type gate struct {
	h    http.Handler
	keep *Keep
	logs *bytes.Buffer
}

func newGate(t *testing.T, up *upstream, maxShots int, vision bool) gate {
	t.Helper()
	dir := t.TempDir()
	k, err := NewKeep(filepath.Join(dir, "shots"))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := NewProxy(up.URL+"/mcp", &Counter{Path: filepath.Join(dir, "count"), Max: maxShots}, k, vision,
		slog.New(slog.NewTextHandler(&logs, nil)))
	return gate{h: h, keep: k, logs: &logs}
}

func serve(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:9223"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// messages reads every JSON-RPC message of a response, a JSON body or each SSE event's data.
func messages(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var raw []string
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(w.Body)
		sc.Buffer(nil, 16<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				raw = append(raw, d)
			}
		}
	} else {
		raw = []string{w.Body.String()}
	}
	var out []map[string]any
	for _, s := range raw {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("%v: %.200s", err, s)
		}
		out = append(out, m)
	}
	return out
}

func rpc(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	w := serve(t, h, http.MethodPost, "/mcp", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	m := messages(t, w)
	if len(m) != 1 {
		t.Fatalf("%d messages", len(m))
	}
	return m[0]
}

func content(res map[string]any) []map[string]any {
	r, _ := res["result"].(map[string]any)
	parts, _ := r["content"].([]any)
	var out []map[string]any
	for _, p := range parts {
		out = append(out, p.(map[string]any))
	}
	return out
}

func toolNames(res map[string]any) []string {
	var out []string
	for _, tl := range res["result"].(map[string]any)["tools"].([]any) {
		out = append(out, tl.(map[string]any)["name"].(string))
	}
	return out
}

func isToolError(res map[string]any, code string) bool {
	r, _ := res["result"].(map[string]any)
	c := content(res)
	return r["isError"] == true && len(c) == 1 && strings.HasPrefix(c[0]["text"].(string), code+": ")
}

func imageBytes(t *testing.T, res map[string]any) int {
	t.Helper()
	for _, p := range content(res) {
		if p["type"] == "image" {
			b, err := base64.StdEncoding.DecodeString(p["data"].(string))
			if err != nil {
				t.Fatal(err)
			}
			return len(b)
		}
	}
	return 0
}

func allText(res map[string]any) string {
	var s []string
	for _, p := range content(res) {
		if t, ok := p["text"].(string); ok {
			s = append(s, t)
		}
	}
	return strings.Join(s, "\n")
}

func screenshotCall(id int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"browser_take_screenshot","arguments":{}}}`, id)
}

const navigate = `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"http://127.0.0.1:8080/docs"}}}`

func TestProxyFiltersListAndRefusesCalls(t *testing.T) {
	up := fakeUpstream(t, false)
	g := newGate(t, up, 1, true)
	list := rpc(t, g.h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if got := toolNames(list); !slices.Equal(got, []string{"browser_navigate", "browser_snapshot", "browser_take_screenshot", "browser_click"}) {
		t.Error("filtered:", got)
	}
	res := rpc(t, g.h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_evaluate","arguments":{}}}`)
	if !isToolError(res, "tool_not_allowed") || up.calls("browser_evaluate") != 0 {
		t.Error("refused before upstream:", res)
	}
	if res["id"] != 2.0 {
		t.Error("the refusal answers the request's id:", res["id"])
	}
	res = rpc(t, g.h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"http://169.254.169.254/"}}}`)
	if !isToolError(res, "url_refused") || up.calls("browser_navigate") != 0 {
		t.Error("metadata IP refused:", res)
	}
	res = rpc(t, g.h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"browser_resize","arguments":{"width":4000,"height":800}}}`)
	if !isToolError(res, "viewport") {
		t.Error("viewport refused:", res)
	}
	if !strings.Contains(g.logs.String(), "tool=browser_evaluate reason=tool_not_allowed") {
		t.Error("a refusal is logged:", g.logs)
	}
}

func TestProxyCapsScreenshots(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			up := fakeUpstream(t, sse)
			g := newGate(t, up, 1, true)
			rpc(t, g.h, navigate)
			res := rpc(t, g.h, screenshotCall(1))
			if n := imageBytes(t, res); n == 0 || n > ImageLimit {
				t.Errorf("image %d bytes", n)
			}
			if txt := allText(res); strings.Contains(txt, "tmp/out") {
				t.Errorf("file link left: %q", txt)
			}
			if !isToolError(rpc(t, g.h, screenshotCall(2)), "screenshot_cap_reached") || up.calls(ScreenshotTool) != 1 {
				t.Error("cap")
			}
			shots, err := g.keep.List(0)
			if err != nil || len(shots) != 1 {
				t.Fatal(shots, err)
			}
			s := shots[0]
			if s.N != 1 || s.Mime != "image/jpeg" || s.Bytes > ImageLimit || s.Width != 560 || s.URL != "http://127.0.0.1:8080/docs" || !s.Vision || s.TakenAt.IsZero() {
				t.Errorf("kept %+v", s)
			}
			if !strings.Contains(g.logs.String(), "msg=screenshot n=1 max=1") {
				t.Error("a screenshot is logged:", g.logs)
			}
		})
	}
}

func TestProxyWithoutVisionKeepsTheImageAndAnswersText(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			g := newGate(t, fakeUpstream(t, sse), 5, false)
			if !slices.Contains(toolNames(rpc(t, g.h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)), ScreenshotTool) {
				t.Error("the screenshot tool stays listed without vision")
			}
			res := rpc(t, g.h, screenshotCall(2))
			if n := imageBytes(t, res); n != 0 {
				t.Error("the model got an image it cannot see")
			}
			want := "screenshot #1 kept for the room; your model cannot see images. Use browser_snapshot to check structure."
			if !strings.Contains(allText(res), want) {
				t.Errorf("text %q", allText(res))
			}
			shots, _ := g.keep.List(0)
			if len(shots) != 1 || shots[0].Vision {
				t.Errorf("kept %+v", shots)
			}
		})
	}
}

func TestProxyRewritesSidecarFileLinks(t *testing.T) {
	g := newGate(t, fakeUpstream(t, false), 1, true)
	txt := allText(rpc(t, g.h, navigate))
	if strings.Contains(txt, "../../tmp") || !strings.Contains(txt, "- Snapshot (kept in the browser sidecar; call browser_snapshot for the page tree)") {
		t.Errorf("%q", txt)
	}
	if !strings.Contains(txt, "Page URL: http://127.0.0.1:8080/docs") {
		t.Error("the rest of the text is untouched")
	}
}

func TestRewriteLinks(t *testing.T) {
	cases := map[string]string{
		"- [Snapshot](../../tmp/out/page-1.yml)":               "- Snapshot (kept in the browser sidecar; call browser_snapshot for the page tree)",
		"[Shot](/tmp/out/a.png) and [Docs](http://127.0.0.1/)": "Shot (kept in the browser sidecar; call browser_snapshot for the page tree) and [Docs](http://127.0.0.1/)",
		"[x](../tmp/a)": "x (kept in the browser sidecar; call browser_snapshot for the page tree)",
		"no links":      "no links",
	}
	for in, want := range cases {
		if got := rewriteLinks(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestProxyRefusesMalformedRequests(t *testing.T) {
	g := newGate(t, fakeUpstream(t, false), 1, true)
	cases := []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
	}{
		{"a JSON-RPC batch", `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, nil, http.StatusBadRequest},
		{"a batch after whitespace", " \n[]", nil, http.StatusBadRequest},
		{"not JSON", `{`, nil, http.StatusBadRequest},
		{"two messages", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}{}`, nil, http.StatusBadRequest},
		{"not JSON-RPC 2.0", `{"id":1,"method":"tools/list"}`, nil, http.StatusBadRequest},
		{"a body over 4 MiB", `{"jsonrpc":"2.0","id":1,"method":"x","params":"` + strings.Repeat("a", MaxBody) + `"}`, nil, http.StatusRequestEntityTooLarge},
		{"a browser's request, with an Origin", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Origin": "http://127.0.0.1:8080"}, http.StatusForbidden},
		{"a non-JSON content type", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := serve(t, g.h, http.MethodPost, "/mcp", c.body, c.hdr); w.Code != c.status {
				t.Errorf("status %d, want %d: %s", w.Code, c.status, w.Body)
			}
		})
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	r.Host = "gate.example.com:9223"
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a non-loopback Host: %d", w.Code)
	}
}

func TestProxyCopiesSessionHeadersAndNotifications(t *testing.T) {
	up := fakeUpstream(t, false)
	g := newGate(t, up, 1, true)
	w := serve(t, g.h, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		map[string]string{"Mcp-Session-Id": "s-1", "Mcp-Protocol-Version": "2025-06-18", "Last-Event-ID": "4", "Authorization": "Bearer x"})
	if w.Code != http.StatusAccepted || w.Header().Get("Mcp-Session-Id") != "s-1" {
		t.Fatal(w.Code, w.Header())
	}
	h := up.headers[0]
	for k, v := range map[string]string{"Mcp-Session-Id": "s-1", "Mcp-Protocol-Version": "2025-06-18", "Last-Event-Id": "4",
		"Accept": "application/json, text/event-stream", "Content-Type": "application/json", "Authorization": ""} {
		if h.Get(k) != v {
			t.Errorf("%s: %q", k, h.Get(k))
		}
	}
}

func TestProxyGETStreamPassesThroughWithoutImages(t *testing.T) {
	g := newGate(t, fakeUpstream(t, false), 1, true)
	w := serve(t, g.h, http.MethodGet, "/mcp", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "id: 7\n") {
		t.Fatal(w.Code, w.Body)
	}
	m := messages(t, w)
	if len(m) != 2 || m[0]["method"] != "notifications/tools/list_changed" {
		t.Fatal(m)
	}
	if imageBytes(t, m[1]) != 0 {
		t.Error("an image outside a screenshot call reached the model")
	}
}

func TestProxyServesTheKeepReadOnly(t *testing.T) {
	g := newGate(t, fakeUpstream(t, false), 2, true)
	rpc(t, g.h, navigate)
	rpc(t, g.h, screenshotCall(1))
	rpc(t, g.h, screenshotCall(2))

	w := serve(t, g.h, http.MethodGet, "/shots?after=1", "", nil)
	var list struct {
		Shots []map[string]any `json:"shots"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || len(list.Shots) != 1 || list.Shots[0]["n"] != 2.0 {
		t.Fatal(w.Code, w.Body)
	}
	for _, k := range []string{"n", "mime", "bytes", "width", "height", "url", "takenAt", "vision"} {
		if _, ok := list.Shots[0][k]; !ok {
			t.Errorf("entry lacks %q", k)
		}
	}
	if w := serve(t, g.h, http.MethodGet, "/shots?after=2", "", nil); strings.TrimSpace(w.Body.String()) != `{"shots":[]}` {
		t.Errorf("empty list: %s", w.Body)
	}
	w = serve(t, g.h, http.MethodGet, "/shots/1", "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Body.Len() == 0 {
		t.Fatal(w.Code, w.Header())
	}
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/shots", http.StatusMethodNotAllowed},
		{http.MethodPut, "/shots/1", http.StatusMethodNotAllowed},
		{http.MethodGet, "/shots/9", http.StatusNotFound},
		{http.MethodGet, "/shots/abc", http.StatusNotFound},
		{http.MethodGet, "/shots?after=x", http.StatusBadRequest},
		{http.MethodGet, "/healthz", http.StatusOK},
	} {
		if w := serve(t, g.h, c.method, c.path, "", nil); w.Code != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, w.Code, c.status)
		}
	}
}

func TestProxyUpstreamDown(t *testing.T) {
	up := fakeUpstream(t, false)
	g := newGate(t, up, 1, true)
	up.Close()
	if w := serve(t, g.h, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); w.Code != http.StatusBadGateway {
		t.Fatal(w.Code)
	}
}
