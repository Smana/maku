// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

type testWireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func writeTestMessage(t *testing.T, w http.ResponseWriter, id json.RawMessage, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestHTTPCallInitializesOnceAndReturnsStructuredContent(t *testing.T) {
	var initializes atomic.Int32
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("path/auth = %q %q", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var m testWireMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&m); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch m.Method {
		case "initialize":
			initializes.Add(1)
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
				Capabilities    struct {
					Elicitation map[string]any `json:"elicitation"`
				} `json:"capabilities"`
				ClientInfo map[string]string `json:"clientInfo"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil || p.ProtocolVersion != "2025-06-18" ||
				p.Capabilities.Elicitation["form"] == nil || p.ClientInfo["name"] != "maku-e2e" {
				t.Errorf("initialize params = %s, err %v", m.Params, err)
			}
			writeTestMessage(t, w, m.ID, map[string]any{"protocolVersion": "2025-06-18"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			calls.Add(1)
			if got := r.Header.Get("MCP-Protocol-Version"); got != "2025-06-18" {
				t.Errorf("protocol header = %q", got)
			}
			dec := json.NewDecoder(bytes.NewReader(m.Params))
			dec.DisallowUnknownFields()
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := dec.Decode(&p); err != nil || p.Name != "intake_plan" || string(p.Arguments) != `{"text":"document it"}` {
				t.Errorf("strict tools/call params = %s, err %v", m.Params, err)
			}
			writeTestMessage(t, w, m.ID, map[string]any{
				"content":           []map[string]string{{"type": "text", "text": "ok"}},
				"structuredContent": map[string]any{"hash": "sha256:new", "call": calls.Load()},
			})
		default:
			t.Errorf("unexpected method %q", m.Method)
		}
	}))
	t.Cleanup(srv.Close)

	c := &Client{BrokerURL: srv.URL, Token: "token", HTTPClient: srv.Client()}
	for i := int32(1); i <= 2; i++ {
		raw, err := c.Call(t.Context(), "intake_plan", map[string]string{"text": "document it"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Hash string `json:"hash"`
			Call int32  `json:"call"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.Hash != "sha256:new" || got.Call != i {
			t.Fatalf("Call() = %s, unmarshal %v; want new hash and call %d", raw, err, i)
		}
	}
	if initializes.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("initialize/calls = %d/%d, want 1/2", initializes.Load(), calls.Load())
	}
}

func TestCallPropagatesToolAndRPCErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m testWireMessage
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.Method == "initialize" {
			writeTestMessage(t, w, m.ID, map[string]any{"protocolVersion": "2025-06-18"})
			return
		}
		if m.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.Name {
		case "tool-failure":
			writeTestMessage(t, w, m.ID, map[string]any{"isError": true,
				"content": []map[string]string{{"type": "text", "text": "issue_changed: nothing was written"}}})
		case "rpc-failure":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32602, "message": "unknown tool"}})
		default:
			writeTestMessage(t, w, m.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": "plain"}}})
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BrokerURL: srv.URL, HTTPClient: srv.Client()}
	for name, want := range map[string]string{
		"tool-failure": "issue_changed: nothing was written",
		"rpc-failure":  "unknown tool",
		"missing-data": "structuredContent",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Call(t.Context(), name, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Call() error = %v, want substring %q", err, want)
			}
		})
	}
}

// TestStdioRelay is the fake local `maku mcp` process. It validates ordering and returns the
// elicitation content so the parent test can assert exactly what the scripted human answered.
func TestStdioRelay(_ *testing.T) {
	if os.Getenv("GO_WANT_E2E_STDIO_RELAY") != "1" {
		return
	}
	mode := os.Getenv("E2E_STDIO_RELAY_MODE")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), maxJSONLine)
	read := func() testWireMessage {
		if !sc.Scan() {
			os.Exit(2)
		}
		var m testWireMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			os.Exit(3)
		}
		return m
	}
	write := func(v any) {
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			os.Exit(4)
		}
	}
	init := read()
	if init.Method != "initialize" {
		os.Exit(5)
	}
	write(map[string]any{"jsonrpc": "2.0", "id": init.ID, "result": map[string]any{"protocolVersion": "2025-06-18"}})
	if note := read(); note.Method != "notifications/initialized" || len(note.ID) != 0 {
		os.Exit(6)
	}
	call := read()
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if call.Method != "tools/call" || json.Unmarshal(call.Params, &p) != nil {
		os.Exit(7)
	}
	if mode == "hang" {
		select {}
	}
	if mode == "oversized" {
		write(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": map[string]any{
			"structuredContent": map[string]string{"padding": strings.Repeat("x", maxJSONLine)},
		}})
		return
	}
	elicitID := json.RawMessage(`"relay-1"`)
	write(map[string]any{"jsonrpc": "2.0", "id": elicitID, "method": "elicitation/create", "params": map[string]any{
		"message":         "Question:\nWhich check?\n\nScoper's recommendation:\ntask check",
		"requestedSchema": map[string]any{"properties": map[string]any{}},
	}})
	answer := read()
	if answer.Method != "" || string(answer.ID) != string(elicitID) || len(answer.Error) != 0 {
		os.Exit(8)
	}
	var form struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	}
	if json.Unmarshal(answer.Result, &form) != nil || form.Action != "accept" {
		os.Exit(9)
	}
	if p.Name == "need_answer" {
		if _, err := os.Stat(os.Getenv("E2E_STDIO_CONFLICT_MARK")); err != nil {
			os.Exit(10)
		}
		if form.Content["answer"] != "task check" || form.Content["send"] != true {
			os.Exit(11)
		}
	} else if form.Content["file"] != true || form.Content["start"] != false {
		os.Exit(12)
	}
	write(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": map[string]any{
		"structuredContent": map[string]any{"answered": p.Name == "need_answer", "filed": p.Name == "issue_file",
			"started": false, "hash": "sha256:new", "form": form.Content},
	}})
}

func stdioClient(t *testing.T, mode string) *Client {
	t.Helper()
	t.Setenv("GO_WANT_E2E_STDIO_RELAY", "1")
	t.Setenv("E2E_STDIO_RELAY_MODE", mode)
	c := &Client{Command: []string{os.Args[0], "-test.run=^TestStdioRelay$"}, Timeout: 5 * time.Second}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestStdioCallAnswersIssueFileWithoutStarting(t *testing.T) {
	c := stdioClient(t, "file")
	raw, err := c.Call(t.Context(), "issue_file", map[string]any{"repo": "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Filed   bool           `json:"filed"`
		Started bool           `json:"started"`
		Hash    string         `json:"hash"`
		Form    map[string]any `json:"form"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || !got.Filed || got.Started || got.Hash != "sha256:new" ||
		got.Form["file"] != true || got.Form["start"] != false {
		t.Fatalf("Call() = %s, unmarshal %v", raw, err)
	}
}

func TestStdioNeedAnswerRunsConflictHookThenSendsSameAnswer(t *testing.T) {
	c := stdioClient(t, "answer")
	mark := filepath.Join(t.TempDir(), "changed")
	t.Setenv("E2E_STDIO_CONFLICT_MARK", mark)
	c.Answer = "task check"
	c.BeforeAnswer = func(context.Context) error { return os.WriteFile(mark, []byte("labels changed"), 0o600) }
	raw, err := c.Call(t.Context(), "need_answer", map[string]any{"repo": "owner/repo", "issue": 1})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Answered bool           `json:"answered"`
		Form     map[string]any `json:"form"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || !got.Answered || got.Form["answer"] != "task check" || got.Form["send"] != true {
		t.Fatalf("Call() = %s, unmarshal %v", raw, err)
	}
}

func TestStdioCallHonorsDeadlineAndLineBound(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		c := stdioClient(t, "hang")
		c.Timeout = 30 * time.Millisecond
		_, err := c.Call(t.Context(), "issue_file", map[string]any{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Call() error = %v, want deadline exceeded", err)
		}
	})
	t.Run("bounded JSON line", func(t *testing.T) {
		c := stdioClient(t, "oversized")
		_, err := c.Call(t.Context(), "issue_file", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "token too long") {
			t.Fatalf("Call() error = %v, want bounded scanner failure", err)
		}
	})
}

func TestAcceptRestCandidateReplacesMarkersAndAppendsAssumptions(t *testing.T) {
	plan := intake.Plan{APIVersion: "plan/v1", Repository: "owner/repo", Issues: []intake.Issue{{
		Index: 1, Title: "docs: verification", Class: "docs",
		Body: "## What is wrong\n[NEEDS CLARIFICATION: g1 which check?]\n## What it should be\nDocument it.\n## Acceptance check\nDone.",
	}}, Gaps: []intake.Gap{{ID: "g1", Text: "which check?", Recommended: intake.Answer{Answer: "task check", Why: "complete verification"}}}}
	got, err := acceptRestCandidate(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := "## What is wrong\ntask check\n## What it should be\nDocument it.\n## Acceptance check\nDone.\n\n## Decisions\nA1 [g1]: assumed task check, because complete verification"
	if got.Issues[0].Body != want {
		t.Fatalf("candidate body = %q, want %q", got.Issues[0].Body, want)
	}
}

func TestRunPlanWritesMachineReadableState(t *testing.T) {
	plan := intake.Plan{APIVersion: "plan/v1", Repository: "owner/repo", Issues: []intake.Issue{{Index: 1, Title: "docs: verification", Body: "body", Class: "docs"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m testWireMessage
		_ = json.NewDecoder(r.Body).Decode(&m)
		switch m.Method {
		case "initialize":
			writeTestMessage(t, w, m.ID, map[string]any{"protocolVersion": "2025-06-18"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			writeTestMessage(t, w, m.ID, map[string]any{"structuredContent": map[string]any{
				"plan": plan, "hash": "sha256:old", "question": map[string]any{"text": "Which check?", "recommended": map[string]string{"answer": "task check", "why": "verification"}},
			}})
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("E2E_BROKER_URL", srv.URL)
	t.Setenv("E2E_TOKEN", "token")
	out := filepath.Join(t.TempDir(), "plan.json")
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"plan", "--text", "document verification", "--repo", "owner/repo", "--out", out}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(mustReadFile(t, out))
	for _, want := range []string{"hash=sha256:old\n", "question=Which check?\n", "recommended=task check\n", "plan=" + encoded + "\n", "plan_file=" + out + "\n"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunGHUsesTheExactIssueArguments(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	if err := runGH(t.Context(), strings.NewReader("body"), &stdout, &stderr,
		"issue", "edit", "1", "--repo=owner/repo", "--body-file=-"); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, argsFile)); got != "issue\nedit\n1\n--repo=owner/repo\n--body-file=-\n" {
		t.Fatalf("gh arguments = %q", got)
	}
}
