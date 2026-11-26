// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder for the kept screenshot's dimensions
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/mcpwire"
)

// MaxBody bounds a request the gate reads from the harness, the bound images/maku-opencode's MCP
// proxy keeps.
const MaxBody = 4 << 20

// maxUpstreamMessage bounds one answer, or one SSE event, read from Playwright MCP: a full-page
// screenshot, base64-encoded, before CapImage.
const maxUpstreamMessage = 64 << 20

// upstreamTimeout bounds the wait for Playwright MCP's response headers: a navigation or a wait_for
// can take a while, a stream's body is not timed.
const upstreamTimeout = 2 * time.Minute

// The headers the gate forwards: the request's to Playwright MCP, the answer's to the harness.
// Nothing else crosses, a credential header included.
var (
	requestHeaders  = []string{"Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID"}
	responseHeaders = []string{"Content-Type", "Mcp-Session-Id", "Cache-Control"}
)

// fileLink is a Markdown link to a file in Playwright MCP's outputDir, which the harness cannot open.
var fileLink = regexp.MustCompile(`\[([^\]\n]*)\]\((?:(?:\.\./)+tmp/|/tmp/)[^)\s]*\)`)

// pageURL is the line Playwright MCP reports the current page with.
var pageURL = regexp.MustCompile(`(?m)^\s*(?:-\s*)?Page URL:\s*(\S+)\s*$`)

// rewriteLinks replaces each outputDir link with its label and a note.
func rewriteLinks(s string) string {
	return fileLink.ReplaceAllString(s, "${1} (kept in the browser sidecar; call browser_snapshot for the page tree)")
}

// pending is the request a response answers: its id, its method, and the screenshot number the
// call reserved (0 for any other call).
type pending struct {
	id     json.RawMessage
	method string
	shot   int
}

type proxy struct {
	upstream string
	client   *http.Client
	counter  *Counter
	keep     *Keep
	vision   bool
	log      *slog.Logger
	now      func() time.Time

	mu   sync.Mutex
	page string
}

// NewProxy returns the gate's handler: /mcp (POST, GET, DELETE) proxied to Playwright MCP at
// upstream, /shots and /shots/{n} reading keep, and /healthz. Every tools/call passes CheckCall,
// and every screenshot Counter.Take and CapImage before it is kept and answered; with vision false
// the model gets text in the image's place. A request carrying an Origin header, or a Host that is
// not loopback, is refused: only the harness, never a page in the browser, talks to the gate.
func NewProxy(upstream string, counter *Counter, keep *Keep, vision bool, log *slog.Logger) http.Handler {
	client := httpx.NewStreaming(upstreamTimeout, nil)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	p := &proxy{upstream: upstream, client: client, counter: counter, keep: keep, vision: vision, log: log, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", p.post)
	mux.HandleFunc("GET /mcp", p.pass)
	mux.HandleFunc("DELETE /mcp", p.pass)
	read := http.NewServeMux()
	read.HandleFunc("GET /shots", p.list)
	read.HandleFunc("GET /shots/{n}", p.shot)
	read.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	bounded := http.TimeoutHandler(read, 10*time.Second, "timeout\n")
	mux.Handle("/shots", bounded)
	mux.Handle("/shots/", bounded)
	mux.Handle("/healthz", bounded)
	return loopbackOnly(mux)
}

// loopbackOnly refuses what a browser could send the gate: a request with an Origin (a page's
// fetch) or a Host that is not a loopback literal (DNS rebinding).
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if r.Header.Get("Origin") != "" || !slices.Contains(loopbackHosts, host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// inbound is one JSON-RPC message from the harness: a request, a notification, or its answer to a
// request of the server's.
type inbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func (p *proxy) post(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	msg, err := decodeOne(body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		mcpwire.Write(w, nil, nil, &mcpwire.Error{Code: mcpwire.InvalidRequest, Message: err.Error()})
		return
	}
	call := pending{id: msg.ID, method: msg.Method}
	if msg.Method == "tools/call" {
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		dec := json.NewDecoder(bytes.NewReader(msg.Params))
		dec.UseNumber()
		if err := dec.Decode(&params); err != nil {
			mcpwire.Write(w, msg.ID, nil, &mcpwire.Error{Code: mcpwire.InvalidParams, Message: "tools/call params are not an object"})
			return
		}
		if err := CheckCall(params.Name, params.Arguments); err != nil {
			p.refuse(w, msg.ID, params.Name, err)
			return
		}
		if params.Name == ScreenshotTool {
			n, err := p.counter.Take()
			if err != nil {
				if !errors.Is(err, ErrCapReached) {
					p.log.Error("screenshot count", "err", err)
					err = fmt.Errorf("%w: the run's screenshot count is unreadable", ErrCapReached)
				}
				p.refuse(w, msg.ID, params.Name, err)
				return
			}
			call.shot = n
		}
	}
	p.forward(w, r, body, call)
}

// decodeOne reads exactly one JSON-RPC 2.0 object: a batch, a second value or a message with
// neither a method nor an answer is refused.
func decodeOne(body []byte) (inbound, error) {
	var msg inbound
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
		return msg, errors.New("JSON-RPC batches are not served")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&msg); err != nil {
		return msg, errors.New("not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return msg, errors.New("one JSON-RPC message per request")
	}
	if msg.JSONRPC != "2.0" || msg.Method == "" && msg.Result == nil && msg.Error == nil {
		return msg, errors.New("not a JSON-RPC 2.0 message")
	}
	return msg, nil
}

// refuse answers a call the gate will not forward as a tool error the model can read: HTTP 200,
// isError, "<code>: <why>".
func (p *proxy) refuse(w http.ResponseWriter, id json.RawMessage, tool string, err error) {
	code := "refused"
	for _, e := range []error{ErrToolNotAllowed, ErrURLRefused, ErrViewport, ErrCapReached} {
		if errors.Is(err, e) {
			code = e.Error()
		}
	}
	p.log.Info("refused", "tool", tool, "reason", code)
	mcpwire.Write(w, id, map[string]any{"isError": true,
		"content": []map[string]string{{"type": "text", "text": err.Error()}}}, nil)
}

// pass forwards a GET (the server's SSE stream) or a DELETE (the session's end).
func (p *proxy) pass(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r, nil, pending{})
}

func (p *proxy) forward(w http.ResponseWriter, r *http.Request, body []byte, call pending) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream, rd)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusInternalServerError)
		return
	}
	for _, h := range requestHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := p.client.Do(req) //nolint:gosec // G704: the URL is the gate's -upstream, a checked loopback literal; the request only carries the body
	if err != nil {
		p.log.Warn("upstream unavailable", "err", err)
		http.Error(w, "browser unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range responseHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "text/event-stream":
		w.WriteHeader(resp.StatusCode)
		if err := p.stream(w, resp.Body, call); err != nil {
			p.log.Warn("upstream stream ended", "err", err)
		}
	case "application/json":
		b, err := httpx.ReadBody(resp.Body, maxUpstreamMessage)
		if err != nil {
			p.log.Warn("upstream answer", "err", err)
			http.Error(w, "browser answer unreadable", http.StatusBadGateway)
			return
		}
		out := p.rewrite(b, call)
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	default:
		// An empty 202, or an error page: no JSON-RPC message, so nothing to rewrite.
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, MaxBody))
	}
}

// stream copies an SSE stream event by event, rewriting each event's data (its data lines joined,
// as the SSE spec joins them) as one message, and flushing after each event.
func (p *proxy) stream(w http.ResponseWriter, body io.Reader, call pending) error {
	rc := http.NewResponseController(w)
	br := bufio.NewReaderSize(body, 64<<10)
	var data, other [][]byte
	size := 0
	emit := func() error {
		var out bytes.Buffer
		for _, l := range other {
			out.Write(l)
			out.WriteByte('\n')
		}
		if len(data) > 0 {
			out.WriteString("data: ")
			out.Write(p.rewrite(bytes.Join(data, []byte("\n")), call))
			out.WriteByte('\n')
		}
		out.WriteByte('\n')
		data, other, size = nil, nil, 0
		if _, err := w.Write(out.Bytes()); err != nil {
			return err
		}
		return rc.Flush()
	}
	for {
		line, err := readLine(br, maxUpstreamMessage-size)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		size += len(line)
		switch {
		case len(line) == 0 && err == nil:
			if eerr := emit(); eerr != nil {
				return eerr
			}
		case bytes.HasPrefix(line, []byte("data:")):
			v := line[len("data:"):]
			data = append(data, bytes.TrimPrefix(v, []byte(" ")))
		case len(line) > 0:
			other = append(other, line)
		}
		if err != nil {
			if len(data)+len(other) > 0 {
				return emit()
			}
			return nil
		}
	}
}

// errEventTooLarge ends a stream whose event passes maxUpstreamMessage.
var errEventTooLarge = errors.New("SSE event too large")

// readLine reads one line without its line ending, failing once it passes limit bytes.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > limit {
			return nil, errEventTooLarge
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		return line, err
	}
}

// rewrite applies the gate to one message from Playwright MCP. The answer to a tools/list is
// filtered; the answer to the screenshot call keeps and caps its image; every other result loses
// any image or non-text part and its outputDir links. Notifications and errors pass unchanged.
func (p *proxy) rewrite(msg []byte, call pending) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return msg
	}
	res, ok := m["result"]
	if !ok {
		return msg
	}
	matched := call.id != nil && sameID(m["id"], call.id)
	var out json.RawMessage
	var changed bool
	if matched && call.method == "tools/list" {
		out, changed = filterList(res)
	} else {
		shot := 0
		if matched {
			shot = call.shot
		}
		out, changed = p.content(res, shot)
	}
	if !changed {
		return msg
	}
	m["result"] = out
	b, err := json.Marshal(m)
	if err != nil {
		return msg
	}
	return b
}

// sameID compares two JSON-RPC ids as compact JSON.
func sameID(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func filterList(res json.RawMessage) (json.RawMessage, bool) {
	var r map[string]json.RawMessage
	if json.Unmarshal(res, &r) != nil {
		return res, false
	}
	var tools []map[string]any
	dec := json.NewDecoder(bytes.NewReader(r["tools"]))
	dec.UseNumber()
	if dec.Decode(&tools) != nil {
		return res, false
	}
	b, err := json.Marshal(FilterTools(tools))
	if err != nil {
		return res, false
	}
	r["tools"] = b
	out, err := json.Marshal(r)
	if err != nil {
		return res, false
	}
	return out, true
}

// content rewrites a tool result's content parts: text keeps its words, minus outputDir links,
// and reports the page URL; the first image of screenshot call shot is capped and kept; any other
// part is replaced by a note.
func (p *proxy) content(res json.RawMessage, shot int) (json.RawMessage, bool) {
	var r map[string]json.RawMessage
	if json.Unmarshal(res, &r) != nil {
		return res, false
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(r["content"], &parts) != nil {
		return res, false
	}
	changed, kept := false, false
	for i, part := range parts {
		typ := str(part["type"])
		switch {
		case typ == "text":
			t := str(part["text"])
			p.notePage(t)
			if nt := rewriteLinks(t); nt != t {
				parts[i]["text"] = mustJSON(nt)
				changed = true
			}
		case typ == "image" && shot > 0 && !kept:
			kept = true
			parts[i] = p.screenshot(shot, str(part["mimeType"]), str(part["data"]))
			changed = true
		default:
			parts[i] = textPart(fmt.Sprintf("(the gate does not serve this %q part)", typ))
			changed = true
		}
	}
	if shot > 0 && !kept {
		p.log.Warn("screenshot answer carried no image", "n", shot)
	}
	if !changed {
		return res, false
	}
	r["content"] = mustJSON(parts)
	out, err := json.Marshal(r)
	if err != nil {
		return res, false
	}
	return out, true
}

// screenshot caps and keeps screenshot n, and returns the part the model gets: the capped image,
// or without vision a note saying where it went.
func (p *proxy) screenshot(n int, mimeType, data string) map[string]json.RawMessage {
	raw, err := base64.StdEncoding.DecodeString(data)
	var capped []byte
	var cmime string
	if err == nil {
		capped, cmime, err = CapImage(raw, mimeType, ImageLimit)
	}
	if err != nil {
		p.log.Warn("screenshot refused", "n", n, "err", err)
		return textPart(fmt.Sprintf("screenshot #%d refused: %v", n, err))
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(capped))
	s := Shot{N: n, Mime: cmime, Width: cfg.Width, Height: cfg.Height, URL: p.currentPage(), TakenAt: p.now().UTC(), Vision: p.vision}
	kerr := p.keep.Put(s, capped)
	if kerr != nil {
		p.log.Error("screenshot not kept", "n", n, "err", kerr)
	}
	p.log.Info("screenshot", "n", n, "max", p.counter.Max, "bytes", len(capped), "kept", kerr == nil)
	if !p.vision {
		return textPart(fmt.Sprintf("screenshot #%d kept for the room; your model cannot see images. Use browser_snapshot to check structure.", n))
	}
	return map[string]json.RawMessage{"type": mustJSON("image"), "mimeType": mustJSON(cmime),
		"data": mustJSON(base64.StdEncoding.EncodeToString(capped))}
}

// notePage records the last loopback "Page URL:" a result reports, for the next kept screenshot.
func (p *proxy) notePage(text string) {
	m := pageURL.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return
	}
	u := m[len(m)-1][1]
	if CheckURL(u) != nil {
		u = ""
	}
	p.mu.Lock()
	p.page = u
	p.mu.Unlock()
}

func (p *proxy) currentPage() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.page
}

func (p *proxy) list(w http.ResponseWriter, r *http.Request) {
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "after must be a screenshot number", http.StatusBadRequest)
			return
		}
		after = n
	}
	shots, err := p.keep.List(after)
	if err != nil {
		p.log.Error("list screenshots", "err", err)
		http.Error(w, "screenshots unreadable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string][]Shot{"shots": shots})
}

func (p *proxy) shot(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		http.NotFound(w, r)
		return
	}
	b, mimeType, err := p.keep.Open(n)
	if errors.Is(err, ErrNoShot) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		p.log.Error("read screenshot", "n", n, "err", err)
		http.Error(w, "screenshot unreadable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(b)
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only strings and parts built here: always encodable
	}
	return b
}

func textPart(s string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"type": mustJSON("text"), "text": mustJSON(s)}
}
