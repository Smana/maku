// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/oauth2"

	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/wire"
)

// makuBroker is a fake broker over TLS: it records every act maku sends
// and the token it came with, and answers a fork with a new room.
type makuBroker struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   int // every request, whatever its path
	acts   []map[string]any
	bearer []string
	query  string // the last REST query
	access string // the X-Rooms-Access the room list sends
	needs  string // the answer to GET /api/needs; empty answers an empty list
	// tool answers a tools/call to /mcp: the tool's name, its arguments, and the answer's JSON-RPC
	// body after the id; nil answers every call with an empty result.
	tool  func(name string, args map[string]any) string
	calls []map[string]any // the arguments of each tools/call, in order
	proto []string         // the MCP-Protocol-Version header of each /mcp request
	// brief answers GET /api/rooms/{id}/brief: a status and a body; nil answers 404.
	brief func() (int, string)
	// handoff answers POST /api/handoffs and /api/handoffs/{id}/check|start; nil falls through.
	handoff func(path string, body []byte) (int, string)
	// dryRun answers POST /api/workflows/dry-run when set.
	dryRun func(req stages.DryRunRequest) (int, any)
}

func newMakuBroker(t *testing.T) *makuBroker {
	t.Helper()
	b := &makuBroker{}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.reqs++
		b.mu.Unlock()
		if r.URL.Path == "/.well-known/maku" {
			_, _ = w.Write([]byte(`{"url":"https://elsewhere","issuer":"https://auth.example","clientID":"cli","projectID":"29184"}`))
			return
		}
		if r.URL.Path == "/mcp" {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&m)
			b.mu.Lock()
			b.bearer = append(b.bearer, r.Header.Get("Authorization"))
			b.proto = append(b.proto, r.Header.Get("MCP-Protocol-Version"))
			if m.Method == "tools/call" {
				b.calls = append(b.calls, m.Params.Arguments)
			}
			b.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if m.Method == "tools/call" && b.tool != nil {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,` + b.tool(m.Params.Name, m.Params.Arguments) + `}`))
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{}}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if strings.HasPrefix(r.URL.Path, "/api/handoffs") && b.handoff != nil {
				raw, _ := io.ReadAll(r.Body)
				status, body := b.handoff(r.URL.Path, raw)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/brief") {
				status, body := http.StatusNotFound, ""
				if b.brief != nil {
					status, body = b.brief()
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			b.mu.Lock()
			b.query = r.URL.Path + "?" + r.URL.RawQuery
			b.mu.Unlock()
			switch {
			case r.URL.Path == "/api/workflows/dry-run":
				var in stages.DryRunRequest
				_ = json.NewDecoder(r.Body).Decode(&in)
				if b.dryRun != nil {
					code, resp := b.dryRun(in)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				http.NotFound(w, r)
			case r.URL.Path == "/api/rooms":
				if b.access != "" {
					w.Header().Set("X-Rooms-Access", b.access)
				}
				_, _ = w.Write([]byte(`[{"id":"3kq7x2ma","repository":"Smana/a","phase":"Active","owner":"human:own","lastSeq":42,"you":{"role":"watcher"}}]`))
			case r.URL.Path == "/api/needs":
				if b.access != "" {
					w.Header().Set("X-Rooms-Access", b.access)
				}
				if b.needs != "" {
					_, _ = w.Write([]byte(b.needs))
					return
				}
				_, _ = w.Write([]byte(`{"apiVersion":"needs/v1","items":[],"cursor":"2026-11-04T19:10:00Z"}`))
			case strings.Contains(r.URL.Path, "/gone/"):
				http.Error(w, "no such room", http.StatusNotFound)
			default:
				_, _ = w.Write([]byte(`{"apiVersion":"summary/v1","room":"26zfnuxm","url":"https://rooms.example/r/26zfnuxm","status":{"phase":"Implementing"},"needsYou":[],"actions":[],` +
					`"notes":{"untrusted":true,"items":[{"at":"2026-10-08T19:14:00Z","run":"cf4ato2x","text":"hi\u001b[2J"}]},"cursor":"seq:142"}`))
			}
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		var hello wire.ClientFrame
		if wsjson.Read(r.Context(), c, &hello) != nil {
			return
		}
		for {
			var f wire.ClientFrame
			if wsjson.Read(r.Context(), c, &f) != nil {
				return
			}
			var act map[string]any
			_ = json.Unmarshal(f.Action, &act)
			b.mu.Lock()
			b.acts, b.bearer = append(b.acts, act), append(b.bearer, r.Header.Get("Authorization"))
			b.mu.Unlock()
			ack := wire.ServerFrame{Type: wire.FrameAck, ClientSeq: f.ClientSeq, Seq: 43}
			if act["kind"] == "fork" {
				ack.Result = json.RawMessage(`{"roomId":"abcdefgh","run":{"kind":"AgentRun"},"runError":"over_budget"}`)
			}
			if act["kind"] == "retry" && act["deeper"] == true {
				ack.Result = json.RawMessage(`{"result":"stepped_up","workflow":"careful","ceilingMicroUSD":13600000}`)
				if hello.RoomID == "4cappedx" {
					ack.Result = json.RawMessage(`{"result":"stepped_up","workflow":"careful","ceilingMicroUSD":5100000,"confirmedMicroUSD":5100000,"workflowCeilingMicroUSD":13600000}`)
				}
			}
			if act["kind"] == "answer" {
				if act["text"] == "author only" {
					ack.Result = json.RawMessage(`{"result":"answered"}`)
				} else {
					ack.Result = json.RawMessage(`{"result":"answered_restarted"}`)
				}
			}
			if act["kind"] == "split" {
				if act["decision"] == "decline" {
					ack.Result = json.RawMessage(`{"result":"split_declined"}`)
				} else {
					ack.Result = json.RawMessage(`{"result":"split_accepted"}`)
				}
			}
			if act["kind"] == "plan" {
				ack.Result = json.RawMessage(`{"result":"plan_approved"}`)
			}
			if act["text"] == "reject-me" || act["decision"] == "reject" {
				ack.Rejected = "act_not_enabled"
				ack.Result = nil
			}
			_ = wsjson.Write(r.Context(), c, ack)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// configured is a maku directory configured for b, logged in with a token
// valid for an hour.
func configured(t *testing.T, b *makuBroker) (makuApp Maku, out *strings.Builder) {
	t.Helper()
	dir := t.TempDir()
	out = &strings.Builder{}
	r := Maku{Dir: dir, HC: b.srv.Client(), Stream: b.srv.Client(), Out: out, Err: &strings.Builder{}}
	if err := r.Run(t.Context(), []string{"configure", "--url", b.srv.URL, "--issuer", "https://auth.example",
		"--client-id", "cli", "--project-id", "29184"}); err != nil {
		t.Fatal(err)
	}
	if err := maku.SaveJSON(filepath.Join(dir, "token.json"), &oauth2.Token{AccessToken: "tok-dev", TokenType: "Bearer",
		RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return r, out
}

func TestMakuConfigure(t *testing.T) {
	dir := t.TempDir()
	r := Maku{Dir: dir, Out: &strings.Builder{}}
	if err := r.Run(t.Context(), []string{"rooms"}); err == nil || !strings.Contains(err.Error(), "maku configure") {
		t.Fatalf("before configure: %v", err)
	}
	if err := r.Run(t.Context(), []string{"configure", "--url", "http://rooms.priv.example", "--issuer", "https://auth.example",
		"--client-id", "cli", "--project-id", "29184"}); err == nil {
		t.Fatal("a plain-http broker would carry the token in clear")
	}
	if err := r.Run(t.Context(), []string{"configure", "--url", "https://rooms.priv.example/", "--issuer", "https://auth.example",
		"--client-id", "cli", "--project-id", "29184"}); err != nil {
		t.Fatal(err)
	}
	var cfg maku.Config
	fi, _ := os.Stat(filepath.Join(dir, "config.json"))
	if err := maku.LoadJSON(filepath.Join(dir, "config.json"), &cfg); err != nil || fi.Mode().Perm() != 0o600 ||
		!reflect.DeepEqual(cfg, maku.Config{URL: "https://rooms.priv.example", Issuer: "https://auth.example", ClientID: "cli", ProjectID: "29184"}) {
		t.Fatalf("%+v %v %v", cfg, err, fi.Mode().Perm())
	}
	if err := r.Run(t.Context(), []string{"token"}); err == nil || !strings.Contains(err.Error(), "maku login") {
		t.Fatalf("before login: %v", err)
	}
}

// Ruling TU: `maku token` is the human's own access token, for scripts
// such as task agent:run against the factory's POST /v1/runs.
func TestMakuToken(t *testing.T) {
	r, out := configured(t, newMakuBroker(t))
	if err := r.Run(t.Context(), []string{"token"}); err != nil || out.String() != "tok-dev\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestMakuPostAndFork(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	for _, args := range [][]string{
		{"post", "3kq7x2ma", "--queue", "address", "L42"}, // a flag after the room, as the usage shows it
		{"post", "3kq7x2ma", "looks good"},
		{"fork", "3kq7x2ma", "--at", "12", "--role", "implementer", "--egress", "pypi,npm", "--note", "try uv"},
		{"fork", "3kq7x2ma", "--at", "12", "--approvals", "unattended"},
	} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	want := []string{
		`{"delivery":"queued","kind":"message","text":"address L42"}`,
		`{"delivery":"none","kind":"message","text":"looks good"}`,
		`{"egressProfiles":["pypi","npm"],"kind":"fork","note":"try uv","role":"implementer","seq":12}`,
		`{"approvals":"unattended","kind":"fork","seq":12}`, // asked for: sent; left out, the broker's default is attended
	}
	for i, w := range want {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w || b.bearer[i] != "Bearer tok-dev" {
			t.Errorf("act %d: %s (%s), want %s", i, got, b.bearer[i], w)
		}
	}
	for _, s := range []string{"seq 43", "forked into abcdefgh: " + b.srv.URL + "/r/abcdefgh", "over your budget", `"kind": "AgentRun"`} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("the output lacks %q:\n%s", s, out)
		}
	}
	for _, bad := range [][]string{{"fork", "3kq7x2ma"}, {"post", "3kq7x2ma"}, {"watch"}, {"fork", "3kq7x2ma", "--at", "0"}, {"fork", "3kq7x2ma", "--at", "1", "--approvals", "yolo"}} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

// #644: maku cancel sends the room page's cancel act, with --discard its discard.
func TestMakuCancel(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	for _, args := range [][]string{{"cancel", "3kq7x2ma"}, {"cancel", "3kq7x2ma", "--discard"}} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	b.mu.Lock()
	for i, w := range []string{`{"kind":"cancel"}`, `{"discard":true,"kind":"cancel"}`} {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w {
			t.Errorf("act %d: %s, want %s", i, got, w)
		}
	}
	b.mu.Unlock()
	if !strings.Contains(out.String(), "cannot delete an issue") {
		t.Errorf("a discard says the issue is closed, not deleted:\n%s", out)
	}
	for _, bad := range [][]string{{"cancel"}, {"cancel", "a", "b"}, {"cancel", "3kq7x2ma", "--yes"}} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestRetryCommandSendsTheAct(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	for _, args := range [][]string{{"retry", "3buqdlot"}, {"retry", "3buqdlot", "--deeper"}} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	b.mu.Lock()
	for i, w := range []string{`{"kind":"retry"}`, `{"deeper":true,"kind":"retry"}`} {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w {
			t.Errorf("act %d: %s, want %s", i, got, w)
		}
	}
	b.mu.Unlock()
	if !strings.Contains(out.String(), "3buqdlot: running again under careful (up to $13.60), on the same branch and pull request") {
		t.Errorf("output:\n%s", out)
	}
	if err := r.Run(t.Context(), []string{"retry", "4cappedx", "--deeper"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "4cappedx: running again under careful (capped at your confirmed $5.10; careful's ceiling is $13.60), on the same branch and pull request") {
		t.Errorf("a capped step-up says why:\n%s", out)
	}
	for _, bad := range [][]string{{"retry"}, {"retry", "a", "b"}} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestAnswerCommandSendsTheAct(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"answer", "3buqdlot", "use task check"}); err != nil {
		t.Fatalf("maintainer answer: %v", err)
	}
	if err := r.Run(t.Context(), []string{"answer", "3buqdlot", "author only"}); err != nil {
		t.Fatalf("author answer: %v", err)
	}
	b.mu.Lock()
	wantActs := []string{
		`{"kind":"answer","text":"use task check"}`,
		`{"kind":"answer","text":"author only"}`,
	}
	for i, w := range wantActs {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w {
			t.Errorf("act %d: %s, want %s", i, got, w)
		}
	}
	b.mu.Unlock()
	for _, want := range []string{
		"3buqdlot: answered; the decision is on the issue and the task is restarting",
		"3buqdlot: answered; the decision is on the issue and a maintainer restarts the task",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if err := r.Run(t.Context(), []string{"answer", "3buqdlot", "reject-me"}); err == nil || !strings.Contains(err.Error(), "human.roomActs") {
		t.Fatalf("refusal sentence: got %v", err)
	}
	for _, bad := range [][]string{{"answer"}, {"answer", "3buqdlot"}, {"answer", "3buqdlot", "   "}} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestAnAnswerIsOneLine(t *testing.T) {
	b := newMakuBroker(t)
	r, _ := configured(t, b)
	for _, multi := range []string{"first line\nsecond line", "first line\r\nsecond line"} {
		err := r.Run(t.Context(), []string{"answer", "3buqdlot", multi})
		if err == nil || !strings.Contains(err.Error(), "one line") {
			t.Fatalf("multiline %q: got %v, want one-line refusal", multi, err)
		}
	}
	b.mu.Lock()
	sent := len(b.acts)
	b.mu.Unlock()
	if sent != 0 {
		t.Fatalf("multiline answer was sent to the broker (%d acts)", sent)
	}
}

func TestSplitCommandSendsTheAct(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	for _, args := range [][]string{{"split", "accept", "3buqdlot"}, {"split", "decline", "3buqdlot"}} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	b.mu.Lock()
	for i, w := range []string{`{"decision":"accept","kind":"split"}`, `{"decision":"decline","kind":"split"}`} {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w {
			t.Errorf("act %d: %s, want %s", i, got, w)
		}
	}
	b.mu.Unlock()
	for _, want := range []string{
		"3buqdlot: split accepted; its parts open one at a time, charged to you",
		"3buqdlot: split declined; the task is queued to run whole",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range [][]string{{"split"}, {"split", "accept"}, {"split", "maybe", "3buqdlot"}, {"split", "accept", "3buqdlot", "extra"}} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestPlanCommandSendsTheAct(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	for _, args := range [][]string{
		{"plan", "approve", "3buqdlot"},
		{"plan", "split", "3buqdlot"},
		{"plan", "revise", "3buqdlot", "keep cache untouched"},
	} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	b.mu.Lock()
	wantActs := []string{
		`{"decision":"approve","kind":"plan"}`,
		`{"decision":"split","kind":"plan"}`,
		`{"decision":"revise","kind":"plan","note":"keep cache untouched"}`,
	}
	for i, w := range wantActs {
		if got, _ := json.Marshal(b.acts[i]); string(got) != w {
			t.Errorf("act %d: %s, want %s", i, got, w)
		}
	}
	b.mu.Unlock()
	for _, want := range []string{
		"3buqdlot: plan approved; implementation is queued",
		"3buqdlot: plan split; its parts open one at a time",
		"3buqdlot: revision requested; the planner runs once more with your comment",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range [][]string{
		{"plan"},
		{"plan", "approve"},
		{"plan", "approve", "3buqdlot", "extra"},
		{"plan", "revise", "3buqdlot"},
		{"plan", "revise", "3buqdlot", "line1\nline2"},
		{"plan", "unknown", "3buqdlot"},
	} {
		if err := r.Run(t.Context(), bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

// Ruling P18: maku has no command that steers, interrupts, moves the driver
// token or decides, and what it sends is a chat, a queued message or a fork.
func TestMakuNeverSteersOrDecides(t *testing.T) {
	b := newMakuBroker(t)
	r, _ := configured(t, b)
	for _, cmd := range []string{"steer", "interrupt", "promote", "take", "give", "request", "driver", "decide", "approve", "deny", "invite", "close"} {
		if err := r.Run(t.Context(), []string{cmd, "3kq7x2ma", "now"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
	for _, args := range [][]string{{"post", "3kq7x2ma", "--queue", "x"}, {"post", "3kq7x2ma", "x"}, {"fork", "3kq7x2ma", "--at", "1"}} {
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
	}
	// run starts a run only on a local broker: on this OIDC one it refuses and sends nothing.
	if err := r.Run(t.Context(), []string{"run", "--repo", "Smana/a", "--task", "x"}); err == nil || !strings.Contains(err.Error(), "quickstart-only") {
		t.Errorf("run on an OIDC broker: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.acts {
		chat := a["kind"] == "message" && slices.Contains([]any{"none", "queued"}, a["delivery"])
		if a["kind"] != "fork" && !chat {
			t.Errorf("maku sent %v", a)
		}
	}
}

func TestMakuSkillInstall(t *testing.T) {
	skills := filepath.Join(t.TempDir(), "skills")
	out := &strings.Builder{}
	// No configure first: the skill needs neither a config nor a login.
	r := Maku{Dir: t.TempDir(), Out: out}
	if err := r.Run(t.Context(), []string{"skill", "install", "--dir", skills}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"SKILL.md", filepath.Join("references", "issue-template.md")} {
		p := filepath.Join(skills, "maku-handoff", f)
		if !strings.Contains(out.String(), p) {
			t.Errorf("output lacks %s:\n%s", p, out)
		}
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	if err := r.Run(t.Context(), []string{"skill"}); err == nil {
		t.Error("skill without a subcommand must fail")
	}
}

func TestMakuStatus(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--after", "5"}); err != nil {
		t.Fatal(err)
	}
	if b.query != "/api/rooms/26zfnuxm/summary?after=5" || !strings.Contains(out.String(), "running · an agent is coding") ||
		!strings.Contains(out.String(), "hi[2J") || strings.ContainsRune(out.String(), 0x1b) || !strings.HasSuffix(out.String(), "\ncursor: seq:142\n") {
		t.Fatalf("%q\n%q", b.query, out)
	}
	// The rooms base is the broker's own (the summary's URL), not the address maku reached it at.
	if !strings.Contains(out.String(), "open the room (https://rooms.example/r/26zfnuxm)") {
		t.Fatalf("no room link: %q", out)
	}
	out.Reset()
	if err := r.Run(t.Context(), []string{"status", "--json", "26zfnuxm"}); err != nil || !strings.HasPrefix(out.String(), `{"apiVersion":"summary/v1"`) || !strings.Contains(out.String(), `\u001b`) {
		t.Fatalf("--json is the broker's body: %q %v", out, err)
	}
	if err := r.Run(t.Context(), []string{"status", "gone"}); err == nil || !strings.Contains(err.Error(), "no such room (or you cannot read it)") {
		t.Fatalf("404: %v", err)
	}
	if err := r.Run(t.Context(), []string{"status"}); err == nil {
		t.Fatal("a room is required")
	}
}

// status --format md|text picks the view; anything else is refused with the usage.
func TestMakuStatusFormat(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--format", "md"}); err != nil {
		t.Fatal(err)
	}
	md := out.String()
	out.Reset()
	if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--format", "text"}); err != nil {
		t.Fatal(err)
	}
	if md == out.String() || !strings.Contains(out.String(), "hi[2J") || strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("md and text differ, and text is inert:\n%q\n%q", md, out)
	}
	if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--format", "html"}); err == nil || !strings.Contains(err.Error(), "md or text") {
		t.Fatalf("%v", err)
	}
	if err := r.Run(t.Context(), []string{"status"}); err == nil || !strings.Contains(err.Error(), "--format md|text") {
		t.Fatalf("the usage names --format: %v", err)
	}
}

// rig is a configured maku over a fake broker that also serves the brief route: the two reads `maku status`
// makes.
type rig struct {
	Maku
	broker *makuBroker
}

// makuRig is a configured, logged-in maku and the fake broker behind it.
func makuRig(t *testing.T) (*rig, *strings.Builder) {
	t.Helper()
	b := newMakuBroker(t)
	m, out := configured(t, b)
	return &rig{Maku: m, broker: b}, out
}

// stderr is what maku wrote to its hint stream so far.
func (r *rig) stderr() string { return r.Err.(*strings.Builder).String() }

// servesBrief makes the broker answer GET /api/rooms/{id}/brief with text, in the route's envelope.
func (r *rig) servesBrief(text string) {
	quoted, _ := json.Marshal(text)
	r.broker.brief = func() (int, string) {
		return http.StatusOK, `{"apiVersion":"brief/v1","room":"26zfnuxm","text":` + string(quoted) + `,"cursor":"seq:142"}`
	}
}

// briefText is a brief of the room the fake broker serves: five section lines, then one that the head leaves
// to the full brief (Links).
const briefText = "**Resume: 26zfnuxm · #2238 docs: the install link points at a moved page**\n\n" +
	"Asked: #2238, the install link points at a moved page\n" +
	"Plan: scope ready · budget $2.00 ceiling\n" +
	"Blocked on: `failed_check` — the docs link check fails on the install page\n" +
	"Next: read the failing check → https://github.com/Smana/a/pull/2238\n" +
	"Links: room https://rooms.example/r/26zfnuxm\n"

// CD11: maku status leads with the brief's head — the lines that say where the task stands — and the room's
// card follows unchanged. A broker that serves no the brief route, and one whose brief fails for another reason,
// still print the card: it is what the developer asked for.
func TestStatusLeadsWithTheBrief(t *testing.T) {
	const head = "**Resume: 26zfnuxm · #2238 docs: the install link points at a moved page**\n" +
		"Asked: #2238, the install link points at a moved page\n" +
		"Plan: scope ready · budget $2.00 ceiling\n" +
		"Blocked on: `failed_check` — the docs link check fails on the install page\n" +
		"Next: read the failing check → https://github.com/Smana/a/pull/2238\n"
	t.Run("a broker that serves the brief route", func(t *testing.T) {
		r, out := makuRig(t)
		r.servesBrief(briefText)
		if err := r.Run(t.Context(), []string{"status", "26zfnuxm"}); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		lines := strings.Split(got, "\n")
		if len(lines) < 8 || strings.Join(lines[:5], "\n")+"\n" != head || lines[5] != "" {
			t.Fatalf("the head is not the brief's first section lines above the card:\n%s", got)
		}
		if strings.Contains(strings.Join(lines[:5], "\n"), "Links:") || strings.ContainsRune(strings.Join(lines[:5], "\n"), 0x1b) {
			t.Fatalf("the head carries a line the full brief owns, or an escape: %q", lines[:5])
		}
		// The card follows, cursor last, as it did before the head existed.
		if !strings.Contains(got, "hi[2J") || !strings.HasSuffix(got, "\ncursor: seq:142\n") {
			t.Fatalf("the card does not follow the head:\n%s", got)
		}
	})
	t.Run("an older broker", func(t *testing.T) {
		r, out := makuRig(t)
		before := r.stderr()
		if err := r.Run(t.Context(), []string{"status", "26zfnuxm"}); err != nil {
			t.Fatalf("an older broker is no failure: %v", err)
		}
		if strings.Contains(out.String(), "Resume:") || !strings.Contains(out.String(), "hi[2J") {
			t.Fatalf("the card alone:\n%s", out)
		}
		if msg := r.stderr(); msg != before {
			t.Fatalf("a broker without the brief route says nothing: %q", strings.TrimPrefix(msg, before))
		}
	})
	t.Run("a brief that fails for another reason", func(t *testing.T) {
		r, out := makuRig(t)
		r.broker.brief = func() (int, string) { return http.StatusInternalServerError, "log_unreadable" }
		before := r.stderr()
		if err := r.Run(t.Context(), []string{"status", "26zfnuxm"}); err != nil {
			t.Fatalf("the card prints anyway: %v", err)
		}
		if !strings.Contains(out.String(), "hi[2J") {
			t.Fatalf("the card:\n%s", out)
		}
		// One line says why the head is missing, naming the tool the broker refused.
		if msg := strings.TrimPrefix(r.stderr(), before); !strings.Contains(msg, "brief") || strings.Count(msg, "\n") != 1 {
			t.Fatalf("no one-line reason: %q", msg)
		}
	})
}

// --after takes the summary's cursor as printed, seq:N, or the bare seq.
func TestMakuStatusAfterTakesTheCursor(t *testing.T) {
	b := newMakuBroker(t)
	r, _ := configured(t, b)
	for _, after := range []string{"seq:142", "142"} {
		if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--after", after}); err != nil {
			t.Fatal(err)
		}
		if b.query != "/api/rooms/26zfnuxm/summary?after=142" {
			t.Fatalf("--after %s: query %q", after, b.query)
		}
	}
	for _, bad := range []string{"seq:", "seq:-1", "-1", "142x", "cursor:142"} {
		if err := r.Run(t.Context(), []string{"status", "26zfnuxm", "--after", bad}); err == nil {
			t.Errorf("--after %q accepted", bad)
		}
	}
}

// --mine matches the linked GitHub login on the room's task, not the room's owner.
func TestMakuHelpSaysWhatMineMatches(t *testing.T) {
	var out strings.Builder
	if err := (Maku{Dir: t.TempDir(), Out: &out}).Run(t.Context(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "issues you filed or labelled, PRs you authored or review") || strings.Contains(s, "rooms you own") {
		t.Fatalf("help:\n%s", s)
	}
}

func TestMakuRoomsFiltersAndHint(t *testing.T) {
	b := newMakuBroker(t)
	b.access = "unlinked"
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"rooms", "--repo", "Smana/a", "--mine", "--needs-me"}); err != nil {
		t.Fatal(err)
	}
	if b.query != "/api/rooms?mine=1&needs_me=1&repo=Smana%2Fa" || !strings.Contains(out.String(), "ROOM") {
		t.Fatalf("%q %q", b.query, out)
	}
	if got := r.Err.(*strings.Builder).String(); !strings.Contains(got, "https://auth.example") {
		t.Fatalf("the hint goes to stderr and names the issuer: %q", got)
	}
}

// needsAnswer is GET /api/needs' answer: one merge in one room, and the cursor to ask from next.
const needsAnswer = `{"apiVersion":"needs/v1","access":"ok","items":[{"kind":"merge","id":"2239","room":"3kq7x2ma",` +
	`"what":"a maintainer approves PR #2239","why":"the pull request waits for a maintainer",` +
	`"url":"https://github.com/Smana/a/pull/2239","action":{"kind":"merge","label":"open the pull request",` +
	`"url":"https://github.com/Smana/a/pull/2239"}}],"cursor":"2026-11-04T19:10:00Z"}`

// CD21, CD28: the CLI's list is the route's, rendered by the one view package, and its empty case says so
// once. What an item asks is quoted, as the room card quotes an agent's words.
func TestMakuNeeds(t *testing.T) {
	b := newMakuBroker(t)
	b.needs = needsAnswer
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"needs"}); err != nil {
		t.Fatal(err)
	}
	if b.query != "/api/needs?" {
		t.Fatalf("query %q", b.query)
	}
	for _, want := range []string{"Needs across rooms · 1 needs you", `merge — "a maintainer approves PR #2239"`,
		"→ open the pull request (https://github.com/Smana/a/pull/2239)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the output lacks %q:\n%s", want, out)
		}
	}
	// Nothing to list says so once, and takes no argument.
	b.needs = ""
	out.Reset()
	if err := r.Run(t.Context(), []string{"needs"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "nothing needs you") {
		t.Fatalf("%q", out)
	}
	// Review Focus 1: a room whose state could not be read is named, from the answer's own unread list, so
	// the CLI never passes an unreadable room off as quiet.
	b.needs = `{"apiVersion":"needs/v1","items":[],"cursor":"2026-11-04T19:10:00Z","unread":["5kq7x2mc"]}`
	out.Reset()
	if err := r.Run(t.Context(), []string{"needs"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 room's state could not be read: 5kq7x2mc") {
		t.Fatalf("%q", out)
	}
	if err := r.Run(t.Context(), []string{"needs", "3kq7x2ma"}); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("a positional argument: %v", err)
	}
}

// `maku needs` asks from the cursor the last call stored (cursors.json's "needs" entry) unless --since names
// an instant, stores the answer's own cursor, and narrows by --room and --repo.
func TestMakuNeedsAsksFromTheStoredCursor(t *testing.T) {
	b := newMakuBroker(t)
	b.needs = needsAnswer
	r, _ := configured(t, b)
	run := func(args ...string) string {
		t.Helper()
		if err := r.Run(t.Context(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return b.query
	}
	if got := run("needs", "--room", "3kq7x2ma", "--repo", "Smana/a"); got != "/api/needs?repo=Smana%2Fa&room=3kq7x2ma" {
		t.Fatalf("the filters: %q", got)
	}
	store := maku.Cursors{Path: filepath.Join(r.Dir, "cursors.json")}
	if got := store.Get("needs"); got != "2026-11-04T19:10:00Z" {
		t.Fatalf("the answer's cursor was not stored: %q", got)
	}
	if got := run("needs"); got != "/api/needs?since=2026-11-04T19%3A10%3A00Z" {
		t.Fatalf("the stored cursor: %q", got)
	}
	if got := run("needs", "--since", "2026-11-01T00:00:00Z"); got != "/api/needs?since=2026-11-01T00%3A00%3A00Z" {
		t.Fatalf("--since overrides the stored cursor: %q", got)
	}
	// The file is a convenience, never a source of truth: a cursor it no longer holds as an instant reads as
	// none, rather than sending a `since` the route would refuse.
	if err := maku.SaveJSON(store.Path, map[string]any{"cursors": []map[string]string{{"room": "needs", "cursor": "yesterday"}}}); err != nil {
		t.Fatal(err)
	}
	if got := run("needs"); got != "/api/needs?" {
		t.Fatalf("a corrupt stored cursor: %q", got)
	}
}

// Review Focus 3: a --since that is not an instant is refused with the format the answer's own cursor has,
// before any request — never read as everything, never as nothing.
func TestMakuNeedsRefusesAMalformedSince(t *testing.T) {
	b := newMakuBroker(t)
	b.needs = needsAnswer
	r, _ := configured(t, b)
	before := b.reqs
	for _, bad := range []string{"yesterday", "2026-11-04", "2026-11-04T19:10:00", "2026-11-04T19:10:00Zjunk"} {
		if err := r.Run(t.Context(), []string{"needs", "--since", bad}); err == nil || !strings.Contains(err.Error(), "RFC 3339") {
			t.Errorf("--since %q: %v", bad, err)
		}
	}
	if b.reqs != before {
		t.Fatalf("%d requests were made before a malformed --since was refused", b.reqs-before)
	}
}

// --json is the route's answer as sent: a script reads what the browser reads, apiVersion and access
// included, and the stored cursor still moves on.
func TestMakuNeedsJSONIsTheBrokersAnswer(t *testing.T) {
	b := newMakuBroker(t)
	b.needs = needsAnswer
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"needs", "--json"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != needsAnswer+"\n" {
		t.Fatalf("%q", out)
	}
	if got := (maku.Cursors{Path: filepath.Join(r.Dir, "cursors.json")}).Get("needs"); got != "2026-11-04T19:10:00Z" {
		t.Fatalf("--json did not store the answer's cursor: %q", got)
	}
}

// F2: maku mcp relays stdio to the broker's /mcp with the developer's token, until stdin ends.
func TestMakuMCP(t *testing.T) {
	b := newMakuBroker(t)
	r, _ := configured(t, b)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r.In, r.Out = inR, outW
	done := make(chan error, 1)
	go func() { done <- r.Run(t.Context(), []string{"mcp"}) }()
	if _, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil || line != `{"id":1,"jsonrpc":"2.0","result":{}}`+"\n" {
		t.Fatalf("%q %v", line, err)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.bearer) != 1 || b.bearer[0] != "Bearer tok-dev" {
		t.Fatalf("%v", b.bearer)
	}
	if err := r.Run(t.Context(), []string{"mcp", "extra"}); err == nil {
		t.Fatal("mcp takes no arguments")
	}
}

func TestMakuSetup(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	b := newLocalBroker(t)
	run := func(args ...string) (string, error) {
		r, out := configuredLocal(t, b)
		r.Home, r.Cwd = home, cwd
		r.LookPath = func(string) (string, error) { return "", errors.New("absent") }
		r.RunCmd = func(context.Context, []string) error { t.Error("ran a command"); return nil }
		err := r.Run(context.Background(), args)
		return out.String(), err
	}
	if _, err := run("setup", "vim"); err == nil || !strings.Contains(err.Error(), "copilot") {
		t.Errorf("setup vim: %v, want the six clients named", err)
	}
	if _, err := run("setup"); err == nil {
		t.Error("setup with no client: want usage")
	}
	plain := Maku{Dir: t.TempDir(), Out: &strings.Builder{}, Home: home, Cwd: cwd,
		LookPath: func(string) (string, error) { return "", errors.New("absent") },
		RunCmd:   func(context.Context, []string) error { t.Error("ran a command"); return nil }}
	if err := plain.Run(context.Background(), []string{"setup", "cursor", "--print"}); err != nil ||
		!strings.Contains(plain.Out.(*strings.Builder).String(), "~/.cursor/mcp.json") {
		t.Errorf("setup cursor --print: %q, %v", plain.Out, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err == nil {
		t.Error("--print wrote")
	}
	if out, err := exec.CommandContext(context.Background(), "git", "-C", cwd, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if _, err := run("setup", "cursor", "--project"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".cursor", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Error(err)
	}
}

func TestMakuSetupProjectIsRootedAtTheRepository(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.CommandContext(context.Background(), "git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := newLocalBroker(t)
	run := func(cwd string) error {
		r, _ := configuredLocal(t, b)
		r.Home, r.Cwd = t.TempDir(), cwd
		r.LookPath = func(string) (string, error) { return "", errors.New("absent") }
		return r.Run(context.Background(), []string{"setup", "cursor", "--project"})
	}
	if err := run(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".cursor", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Errorf("not at the repository top: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".cursor")); err == nil {
		t.Error("wrote under the subdirectory")
	}
	// Not a repository: a clear refusal, and nothing written.
	plain := t.TempDir()
	if err := run(plain); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("outside a repository: %v, want a refusal naming the repository", err)
	}
	if _, err := os.Stat(filepath.Join(plain, ".cursor")); err == nil {
		t.Error("wrote outside a repository")
	}
}

func TestSetupRunsWholeSequence(t *testing.T) {
	var mu sync.Mutex
	deviceCalls := 0
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/maku":
			_, _ = w.Write([]byte(`{"url":"` + srv.URL + `","issuer":"` + srv.URL + `","clientID":"cli","projectID":"29184"}`))
		case "/oauth/v2/device_authorization":
			mu.Lock()
			deviceCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://auth.example/device","expires_in":300,"interval":1}`))
		case "/oauth/v2/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-dev","token_type":"Bearer","expires_in":3600,"refresh_token":"rt"}`))
		case "/api/rooms":
			_, _ = w.Write([]byte(`[{"id":"3kq7x2ma","repository":"Smana/a"}]`))
		case "/mcp":
			var m struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&m)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{"content":[],"structuredContent":{"canStart":true}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	var ran [][]string
	out := &strings.Builder{}
	r := Maku{
		Dir: dir, HC: srv.Client(), Stream: srv.Client(),
		In: strings.NewReader(srv.URL + "\n"), Out: out, Err: &strings.Builder{},
		Home: home, Cwd: cwd,
		LookPath: func(string) (string, error) { return "/usr/bin/x", nil },
		RunCmd:   func(_ context.Context, argv []string) error { ran = append(ran, argv); return nil },
	}
	if err := r.Run(t.Context(), []string{"setup", "claude"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := out.String()
	for _, want := range []string{"Rooms URL", "ABCD-EFGH", "registered the maku MCP server", "ok   dns", "ok   login", "ok   github", "ok   skill", "factory capabilities (claude)"} {
		if !strings.Contains(got, want) {
			t.Errorf("setup output lacks %q:\n%s", want, got)
		}
	}
	mu.Lock()
	firstDevices := deviceCalls
	mu.Unlock()
	if firstDevices != 1 {
		t.Fatalf("deviceCalls = %d, want 1", firstDevices)
	}
	wantArgv := []string{"claude", "mcp", "add", "--scope", "user", "maku", "--", "maku", "mcp"}
	if len(ran) != 1 || !slices.Equal(ran[0], wantArgv) {
		t.Fatalf("ran %v, want %v", ran, wantArgv)
	}

	// Second run: valid config and token already on disk, so configure and login are skipped.
	out.Reset()
	ran = nil
	r.In = strings.NewReader("")
	if err := r.Run(t.Context(), []string{"setup", "claude", "--advanced"}); err != nil {
		t.Fatalf("second setup: %v\n%s", err, out)
	}
	mu.Lock()
	afterDevices := deviceCalls
	mu.Unlock()
	if afterDevices != 1 {
		t.Errorf("second run signed in again: deviceCalls = %d", afterDevices)
	}
	if strings.Contains(out.String(), "Rooms URL") || strings.Contains(out.String(), "ABCD-EFGH") {
		t.Errorf("second run prompted or logged in again:\n%s", out)
	}
	wantAdv := []string{"claude", "mcp", "add", "--scope", "user", "maku", "--", "maku", "mcp", "--advanced"}
	if len(ran) != 1 || !slices.Equal(ran[0], wantAdv) {
		t.Fatalf("ran %v, want %v", ran, wantAdv)
	}
}

func TestSetupUnlinkedPrintsLinkURL(t *testing.T) {
	b := newMakuBroker(t)
	b.access = "unlinked"
	r, out := configured(t, b)
	r.Home, r.Cwd = t.TempDir(), t.TempDir()
	r.LookPath = func(string) (string, error) { return "", errors.New("absent") }
	err := r.Run(t.Context(), []string{"setup", "cursor"})
	if err == nil {
		t.Fatalf("want doctor failure for unlinked user:\n%s", out)
	}
	wantURL := b.srv.URL + "/link/github"
	if !strings.Contains(out.String(), wantURL) {
		t.Fatalf("unlinked output lacks %q:\n%s", wantURL, out)
	}
}

func TestMakuDoctorReadsTheRepositorySkillDirs(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.CommandContext(context.Background(), "git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	sub := filepath.Join(repo, "a")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	r := Maku{Home: home, Cwd: sub}
	dirs, err := r.skillDirs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	has := func(p string) bool {
		return slices.ContainsFunc(dirs, func(d maku.SkillDir) bool { return d.Path == p })
	}
	if !has(filepath.Join(repo, ".claude", "skills")) || has(filepath.Join(sub, ".claude", "skills")) {
		t.Errorf("dirs %v: want the repository top, not the subdirectory", dirs)
	}
	r.Cwd = t.TempDir()
	dirs, err = r.skillDirs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !strings.HasPrefix(d.Path, home) {
			t.Errorf("outside a repository the project scope is skipped, got %s", d.Path)
		}
	}
}

// Fast-follow 4: `maku doctor --client <name>` reports what that client declared at initialize, and
// says a client maku never met was not measured rather than describing it (the flag's only test).
func TestMakuDoctorReportsTheClientFlag(t *testing.T) {
	b := newLocalBroker(t)
	r, _ := configuredLocal(t, b)
	if err := (maku.CapsStore{Path: maku.CapsPath(r.Dir)}).Put(maku.Caps{Client: "claude-code",
		Version: "2.0.1", Protocol: "2025-06-18", Forms: true, Prompts: true}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r.Out = &out
	if err := r.Run(t.Context(), []string{"doctor", "--client", "claude-code"}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "factory capabilities (claude-code 2.0.1, MCP 2025-06-18)") ||
		!strings.Contains(out.String(), "forms yes") {
		t.Fatalf("doctor:\n%s", &out)
	}

	// A client the record does not hold: doctor says so and describes nothing.
	out.Reset()
	if err := r.Run(t.Context(), []string{"doctor", "--client", "codex"}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "factory capabilities (codex)") || !strings.Contains(out.String(), "not measured yet") {
		t.Fatalf("doctor:\n%s", &out)
	}
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "factory capabilities") && strings.Contains(l, "forms") {
			t.Errorf("a client maku never met is described: %s", l)
		}
	}
}

func TestRoomsBase(t *testing.T) {
	for _, tc := range []struct{ name, url, room, want string }{
		{"the broker's public URL", "https://rooms.example/r/26zfnuxm", "26zfnuxm", "https://rooms.example"},
		{"a trailing slash", "https://rooms.example/r/26zfnuxm/", "26zfnuxm", "https://configured.example"},
		{"another room's page", "https://rooms.example/r/other", "26zfnuxm", "https://configured.example"},
		{"not https", "http://rooms.example/r/26zfnuxm", "26zfnuxm", "https://configured.example"},
		{"no url", "", "26zfnuxm", "https://configured.example"},
	} {
		if got := roomsBase(summary.Summary{URL: tc.url, Room: tc.room}, "https://configured.example"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTextFormat(t *testing.T) {
	tty, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0) // a character device, like a terminal
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = tty.Close() }()
	file, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	t.Setenv("NO_COLOR", "")
	for name, tc := range map[string]struct {
		w    io.Writer
		want roomview.Format
	}{"a terminal": {tty, roomview.TextColor}, "a file": {file, roomview.Text}, "a buffer": {&strings.Builder{}, roomview.Text}} {
		if got := textFormat(tc.w); got != tc.want {
			t.Errorf("%s: format %d, want %d", name, got, tc.want)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if got := textFormat(tty); got != roomview.Text {
		t.Errorf("NO_COLOR set: format %d", got)
	}
}

// changesAnswer is room_changes' structuredContent for a page of two milestones, next its cursor.
func changesAnswer(cursor string) string {
	sc := `{"room":"26zfnuxm","url":"https://rooms.example/r/26zfnuxm","since":"2026-10-08T09:00:00Z","items":[` +
		`{"seq":5,"at":"2026-10-08T09:10:00Z","kind":"run_started","run":"cf4ato2x","role":"implementer"},` +
		`{"seq":9,"at":"2026-10-08T09:40:00Z","kind":"note","run":"cf4ato2x","text":"hi\u001b[2J"}],` +
		`"more":0,"cursor":"` + cursor + `","untrusted":true}`
	return `"result":{"content":[{"type":"text","text":"**26zfnuxm**"}],"structuredContent":` + sc + `}`
}

func TestMakuRoomsRendersTheOverview(t *testing.T) {
	b := newMakuBroker(t)
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"rooms", "--format", "text"}); err != nil {
		t.Fatal(err)
	}
	if h := strings.Fields(strings.Split(out.String(), "\n")[2]); !slices.Equal(h, []string{"TASK", "STATE", "WHERE", "IT", "IS", "PR", "UPDATED", "ROOM"}) {
		t.Fatalf("the terminal overview's header: %q", out)
	}
	out.Reset()
	if err := r.Run(t.Context(), []string{"rooms", "--format", "md"}); err != nil || !strings.Contains(out.String(), "**Factory rooms**") {
		t.Fatalf("--format md: %q %v", out, err)
	}
	out.Reset()
	if err := r.Run(t.Context(), []string{"rooms", "--json"}); err != nil || !strings.HasPrefix(out.String(), `[{"id":"3kq7x2ma"`) {
		t.Fatalf("--json is the body as received: %q %v", out, err)
	}
	if err := r.Run(t.Context(), []string{"rooms", "--all", "--format", "text"}); err != nil {
		t.Fatalf("--all: %v", err)
	}
	if err := r.Run(t.Context(), []string{"rooms", "--format", "html"}); err == nil {
		t.Fatal("an unknown --format is refused")
	}
}

func TestMakuChanges(t *testing.T) {
	b := newMakuBroker(t)
	b.tool = func(name string, _ map[string]any) string {
		if name != "task" {
			t.Errorf("tool %q", name)
		}
		return changesAnswer("seq:9")
	}
	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"changes", "26zfnuxm", "--format", "text"}); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || b.calls[0]["task"] != "26zfnuxm" || b.calls[0]["since"] != nil || b.proto[0] != "2025-06-18" ||
		!strings.Contains(out.String(), "26zfnuxm") || strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("first run: %v %v\n%q", b.calls, b.proto, out)
	}
	if err := r.Run(t.Context(), []string{"changes", "26zfnuxm"}); err != nil {
		t.Fatal(err)
	}
	if b.calls[1]["since"] != "seq:9" {
		t.Fatalf("the second run sends the cursor the first returned: %v", b.calls)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "cursors.json")); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(t.Context(), []string{"changes", "26zfnuxm", "--cursor", "seq:2"}); err != nil || b.calls[2]["since"] != "seq:2" {
		t.Fatalf("--cursor overrides: %v %v", b.calls, err)
	}
	out.Reset()
	if err := r.Run(t.Context(), []string{"changes", "26zfnuxm", "--json"}); err != nil || !strings.HasPrefix(out.String(), `{"room":"26zfnuxm"`) || !strings.Contains(out.String(), `\u001b`) {
		t.Fatalf("--json is the structured content as received: %q %v", out, err)
	}
	if err := r.Run(t.Context(), []string{"changes"}); err == nil {
		t.Fatal("a room is required")
	}
	if err := r.Run(t.Context(), []string{"changes", "26zfnuxm", "--cursor", "x"}); err == nil {
		t.Fatal("a cursor is seq:N")
	}
}

func TestMakuChangesOldBroker(t *testing.T) {
	b := newMakuBroker(t)
	b.tool = func(string, map[string]any) string {
		return `"error":{"code":-32602,"message":"unknown tool task"}`
	}
	r, _ := configured(t, b)
	err := r.Run(t.Context(), []string{"changes", "26zfnuxm"})
	if err == nil || !strings.Contains(err.Error(), "maku changes needs a broker with task") {
		t.Fatalf("%v", err)
	}
}

func TestMakuHelpNamesTheNewCommands(t *testing.T) {
	var out strings.Builder
	if err := (Maku{Dir: t.TempDir(), Out: &out}).Run(t.Context(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "changes <room>") || !strings.Contains(s, "rooms [--repo owner/name]") || !strings.Contains(s, "--all") {
		t.Fatalf("help:\n%s", s)
	}
}

// maku coordination checkout reads the parent's document from the broker before any git call, and
// refuses a malformed --workspace, a missing parent and a document it does not know.
func TestMakuCoordinationCheckout(t *testing.T) {
	b := newMakuBroker(t)
	r, _ := configured(t, b)
	r.Cwd = t.TempDir()
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no subcommand":         {[]string{"coordination"}, "coordination checkout <parent>"},
		"an unknown subcommand": {[]string{"coordination", "merge", "p"}, "coordination checkout <parent>"},
		"no parent":             {[]string{"coordination", "checkout"}, "one parent"},
		"a malformed workspace": {[]string{"coordination", "checkout", "p", "--workspace", "acme"}, "owner/name=path"},
		"a twice-named clone":   {[]string{"coordination", "checkout", "p", "--workspace", "a/b=x", "--workspace", "a/b=y"}, "given twice"},
		"not a parent":          {[]string{"coordination", "checkout", "gone"}, "no such coordination parent"},
		"an unknown document":   {[]string{"coordination", "checkout", "p1"}, "unknown document"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := r.Run(t.Context(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestTerminalStartPrintsTheCardAndAsks(t *testing.T) {
	b := newMakuBroker(t)
	var seenPaths []string
	var seenBodies []map[string]any
	b.handoff = func(path string, raw []byte) (int, string) {
		seenPaths = append(seenPaths, path)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		seenBodies = append(seenBodies, m)
		switch path {
		case "/api/handoffs":
			return http.StatusOK, `{"apiVersion":"handoff/v1","id":"h1234567890","text":"Handoff h1234567890","ready":true,"workflow":"standard","budget":5.10,"repository":"Smana/maku","title":"docs: fix dead install link","body":"## What is wrong\nDead link."}`
		case "/api/handoffs/h1234567890/check":
			if wf, _ := m["workflow"].(string); wf == "careful" {
				return http.StatusForbidden, `{"error":"not_maintainer","message":"only a repository maintainer can pin a workflow"}`
			}
			outcome := ""
			if o, _ := m["outcome"].(string); o != "" {
				outcome = o
			}
			return http.StatusOK, `{"id":"h1234567890","hash":"sha256:` + strings.Repeat("a", 64) + `","canStart":true,"linked":true,"workflow":"standard",` +
				`"repository":"Smana/maku","title":"docs: fix dead install link","body":"## What is wrong\nDead link.",` +
				`"activeChoice":{"name":"standard","ceilingUSD":5.10,"path":["implementer","reviewer","CI","merge gate"]},` +
				`"plan":{"repository":"Smana/maku","budget":{"ceilingUSD":5.10},"issues":[{"index":1,"title":"docs: fix dead install link","body":"## What is wrong\nDead link.","outcome":"` + outcome + `"}]}}`
		default:
			return http.StatusNotFound, `{"error":"not_found"}`
		}
	}

	r, out := configured(t, b)
	var prompts []string
	r.Ask = func(prompt string) (bool, error) {
		prompts = append(prompts, prompt)
		return true, nil
	}
	var filedRepo, filedTitle, filedBody string
	var addedLabels []string
	r.FileIssue = func(_ context.Context, repo, title, body string) (string, error) {
		filedRepo, filedTitle, filedBody = repo, title, body
		return "https://github.com/" + repo + "/issues/42", nil
	}
	r.AddLabel = func(_ context.Context, _ string, _ int, label string) error {
		addedLabels = append(addedLabels, label)
		return nil
	}
	r.LabelIssue = func(_ context.Context, _ string, _ int, label string) error {
		addedLabels = append(addedLabels, label)
		return nil
	}

	if err := r.Run(t.Context(), []string{"start", "--repository", "Smana/maku", "--workflow", "careful", "--plan-first", "fix the dead install link"}); err != nil {
		t.Fatalf("maku start: %v", err)
	}
	wantPaths := []string{"/api/handoffs", "/api/handoffs/h1234567890/check", "/api/handoffs/h1234567890/check"}
	if !slices.Equal(seenPaths, wantPaths) {
		t.Fatalf("paths = %v, want %v", seenPaths, wantPaths)
	}
	if len(prompts) != 1 || prompts[0] != "File and start? [y/N] " {
		t.Fatalf("prompts = %v, want [File and start? [y/N] ]", prompts)
	}
	gotOut := out.String()
	for _, want := range []string{
		"only a repository maintainer can pin a workflow",
		"Outcome",
		"a plan to approve, then a pull request",
		"Workflow",
		"standard · this repository's default",
		"Will run",
		"implementer → reviewer → CI → merge gate",
		"Cost",
		"up to $5.10",
		"docs: fix dead install link",
		"Dead link.",
		"https://github.com/Smana/maku/issues/42",
	} {
		if !strings.Contains(gotOut, want) {
			t.Errorf("output lacks %q:\n%s", want, gotOut)
		}
	}
	if filedRepo != "Smana/maku" || filedTitle != "docs: fix dead install link" || !strings.Contains(filedBody, "Dead link.") {
		t.Fatalf("filed = (%q, %q, %q)", filedRepo, filedTitle, filedBody)
	}
	if !slices.Equal(addedLabels, []string{"plan-first", "factory/ready"}) {
		t.Fatalf("labels = %v, want [plan-first factory/ready]", addedLabels)
	}
}

func TestTerminalStartFilesNothingOnNo(t *testing.T) {
	b := newMakuBroker(t)
	b.handoff = func(path string, _ []byte) (int, string) {
		switch path {
		case "/api/handoffs":
			return http.StatusOK, `{"apiVersion":"handoff/v1","id":"h9876543210","text":"Handoff h9876543210","ready":true,"workflow":"standard","budget":2.00,"repository":"Smana/maku","title":"docs: fix typo","body":"## What is wrong\nTypo."}`
		case "/api/handoffs/h9876543210/check":
			return http.StatusOK, `{"id":"h9876543210","hash":"sha256:` + strings.Repeat("b", 64) + `","canStart":true,"linked":true,"workflow":"standard",` +
				`"repository":"Smana/maku","title":"docs: fix typo","body":"## What is wrong\nTypo.",` +
				`"activeChoice":{"name":"standard","ceilingUSD":2.00,"path":["implementer","reviewer","CI","merge gate"]},` +
				`"plan":{"repository":"Smana/maku","budget":{"ceilingUSD":2.00},"issues":[{"index":1,"title":"docs: fix typo","body":"## What is wrong\nTypo."}]}}`
		default:
			return http.StatusNotFound, `{"error":"not_found"}`
		}
	}

	r, out := configured(t, b)
	asked := false
	r.Ask = func(prompt string) (bool, error) {
		asked = true
		if prompt != "File and start? [y/N] " {
			t.Errorf("prompt = %q, want File and start? [y/N] ", prompt)
		}
		return false, nil
	}
	filed := 0
	labelled := 0
	r.FileIssue = func(context.Context, string, string, string) (string, error) {
		filed++
		return "https://github.com/Smana/maku/issues/99", nil
	}
	r.AddLabel = func(context.Context, string, int, string) error { labelled++; return nil }
	r.LabelIssue = func(context.Context, string, int, string) error { labelled++; return nil }

	if err := r.Run(t.Context(), []string{"start", "fix typo in README"}); err != nil {
		t.Fatalf("maku start on No: %v", err)
	}
	if !asked {
		t.Fatal("did not ask File and start? [y/N]")
	}
	if filed != 0 || labelled != 0 {
		t.Fatalf("filed=%d labelled=%d on No, want 0", filed, labelled)
	}
	if !strings.Contains(out.String(), "Nothing filed.") {
		t.Fatalf("output lacks Nothing filed.:\n%s", out.String())
	}
}

func TestMakuWorkflowDryRun(t *testing.T) {
	b := newMakuBroker(t)
	var gotReq stages.DryRunRequest
	est := 0.63
	b.dryRun = func(req stages.DryRunRequest) (int, any) {
		gotReq = req
		return http.StatusOK, stages.DryRunResult{
			Workflow:     req.Workflow,
			Tasks:        2,
			ActualUSD:    1.10,
			EstimatedUSD: 0.63,
			EstimatedOf:  1,
			MedianUsed:   1,
			UnknownCount: 1,
			Rows: []stages.DryRunRow{
				{
					Task:        "3buqdlot",
					Class:       "docs",
					RanWorkflow: "standard",
					Ran:         []string{"implementer", "reviewer"},
					WouldRun:    []string{"implementer", "reviewer", "security-reviewer"},
					CostUSD:     0.54,
					EstimateUSD: &est,
					UsedMedian:  true,
				},
				{
					Task:        "7f3cq2xz",
					Class:       "code",
					RanWorkflow: "standard",
					Ran:         []string{"implementer", "reviewer"},
					WouldRun:    []string{"implementer", "reviewer", "security-reviewer"},
					CostUSD:     0.56,
					Unknown:     true,
				},
			},
		}
	}

	defPath := filepath.Join(t.TempDir(), "thorough.yaml")
	defYAML := "budget: frontier\nstages:\n  - {role: implementer, stall: true}\n  - {role: reviewer, maxRounds: 2}\n  - {role: security-reviewer}\n"
	if err := os.WriteFile(defPath, []byte(defYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	r, out := configured(t, b)
	if err := r.Run(t.Context(), []string{"workflow", "dry-run", "thorough", "--file", defPath, "--repository", "acme/app", "--last", "20"}); err != nil {
		t.Fatalf("workflow dry-run: %v", err)
	}
	if gotReq.Workflow != "thorough" || gotReq.Definition != defYAML || gotReq.Repository != "acme/app" || gotReq.Last != 20 {
		t.Fatalf("request = %+v", gotReq)
	}
	s := out.String()
	for _, want := range []string{
		"TASK",
		"RAN (standard)",
		"WOULD RUN (thorough)",
		"3buqdlot",
		"implementer → reviewer → security-reviewer",
		"$0.54",
		"$0.63",
		"7f3cq2xz",
		"?",
		"2 tasks: $1.10 as run · about $0.63 under thorough",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}

	// JSON mode returns the broker's JSON verbatim.
	out.Reset()
	if err := r.Run(t.Context(), []string{"workflow", "dry-run", "thorough", "--json"}); err != nil {
		t.Fatalf("workflow dry-run --json: %v", err)
	}
	var parsed stages.DryRunResult
	if err := json.Unmarshal([]byte(out.String()), &parsed); err != nil {
		t.Fatalf("invalid json output: %v\n%s", err, out.String())
	}
	if parsed.Workflow != "thorough" || len(parsed.Rows) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}
