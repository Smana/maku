// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intake"
)

// The contract's reply bound and the key the fake gateway is told to expect: the two numbers this
// file holds a call to.
const (
	replyBound = 64 << 10
	plannerKey = "k-planner"
)

// fake is the one endpoint a model in these tests may call. It serves `POST /v1/chat/completions`,
// counts every call, and records what it was sent, so a test asserts on the governed request rather
// than on the client's own account of it.
type fake struct {
	t      *testing.T
	server *httptest.Server
	// keyPath is the planner key's file, written once per gateway: the model reads it at use, so a
	// test can point a model at a file that is not there.
	keyPath string
	// delay makes the answer late; status replaces 200 when a test wants a failing gateway.
	delay  time.Duration
	status int

	calls atomic.Int64

	mu  sync.Mutex
	hdr http.Header
	raw []byte
}

// fakeGateway is the endpoint above, serving body. opts adjust it before it starts — slow() is the
// one this file uses.
func fakeGateway(t *testing.T, body []byte, opts ...func(*fake)) *fake {
	t.Helper()
	f := &fake{t: t, status: http.StatusOK}
	for _, opt := range opts {
		opt(f)
	}
	f.keyPath = filepath.Join(t.TempDir(), "planner-key")
	if err := os.WriteFile(f.keyPath, []byte(plannerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.hdr, f.raw = r.Header.Clone(), raw
		f.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "the one call is POST /v1/chat/completions", http.StatusNotFound)
			return
		}
		if f.delay > 0 {
			// The answer is late until the caller stops waiting or the delay runs out: a handler
			// that sleeps anyway would hold the test server open for the whole delay.
			select {
			case <-time.After(f.delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// slow is the option that makes the gateway answer after d.
func slow(d time.Duration) func(*fake) { return func(f *fake) { f.delay = d } }

// status is the option that makes the gateway answer d's status instead of 200.
func status(code int) func(*fake) { return func(f *fake) { f.status = code } }

// n is how many calls the endpoint has received: the assertion the design makes of a refusal.
func (f *fake) n() int { return int(f.calls.Load()) }

// header is the value the endpoint was sent for name.
func (f *fake) header(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hdr.Get(name)
}

// rawBody is the request body the endpoint was sent, byte for byte.
func (f *fake) rawBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.raw)
}

// body is rawBody decoded, so a test reads the governed fields by name.
func (f *fake) body() map[string]any {
	f.t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(f.rawBody()), &body); err != nil {
		f.t.Fatalf("the request body is not JSON: %v", err)
	}
	return body
}

// modelFor is the planner's client over f: the pinned alias, the key in f's file, the audited egress
// client with a 3 s exchange timeout, and a deadline of 250 ms so that the deadline test asserts a
// real bound rather than racing a 20 s one.
func modelFor(f *fake) intake.Model {
	return intake.Model{
		URL:        f.server.URL + "/v1",
		APIKeyFile: f.keyPath,
		Alias:      "tier-frontier",
		HC:         httpx.New(3*time.Second, nil),
		Deadline:   250 * time.Millisecond,
	}
}

// req is one governed request: a complaint about one repository, with that repository as the only
// candidate.
func req() intake.DraftRequest {
	return intake.DraftRequest{
		Text:       "the install link in the getting-started page is dead",
		Candidates: []string{"smana/ai-native-ref"},
	}
}

// okReply is a valid reply body: one repository, one issue, no question — the shape the contract
// asks for, in the envelope a gateway answers with (an id, a model name, a finish reason and the
// usage counts, none of which the model's answer depends on).
var okReply = replyBody(`{"repository":"smana/ai-native-ref","evidence":[{"kind":"path","text":"website/content/docs/use/guide/terminal-and-coding-agent.md"}],"issues":[{"title":"docs: the install link is dead","body":"## What is wrong\nThe install link 404s.\n## What it should be\nA link that resolves.\n## Acceptance check\nThe link resolves.","class":"docs-links"}]}`)

// replyBody is draft wrapped in the chat/completions envelope, as one string, and then encoded: the
// content is a JSON document inside a JSON string, which is the shape a json_object reply arrives in.
func replyBody(draft string) []byte {
	content, err := json.Marshal(draft)
	if err != nil {
		panic(err)
	}
	return []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1762347600,"model":"glm-5.3",` +
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` + string(content) + `}}],` +
		`"usage":{"prompt_tokens":120,"completion_tokens":340,"total_tokens":460}}`)
}

// replyWithIssueBody is a reply whose one issue body is n bytes, the reply as a whole padded with the
// envelope's own whitespace to the contract's 64 KiB bound — legal JSON of exactly the bound, so that
// one more byte is one byte over it.
func replyWithIssueBody(n int) []byte {
	body, err := json.Marshal(map[string]any{
		"repository": "smana/ai-native-ref",
		"issues": []any{map[string]any{
			"title": "docs: the install link is dead",
			"body":  paddedBody(n),
			"class": "docs-links",
		}},
	})
	if err != nil {
		panic(err)
	}
	out := replyBody(string(body))
	if pad := replyBound - len(out); pad > 0 {
		out = append(out, bytes.Repeat([]byte(" "), pad)...)
	}
	return out
}

// paddedBody is a body of exactly n bytes holding the validator's three headings, then filler: the
// largest body the contract accepts is a body with room for the template.
func paddedBody(n int) string {
	const headings = "## What is wrong\nThe install link 404s.\n## What it should be\nA link that resolves.\n## Acceptance check\nThe link resolves.\n"
	if n < len(headings) {
		panic("the bound is smaller than the template")
	}
	return headings + strings.Repeat("x", n-len(headings))
}

// CD18: one call, one alias, one header, one shape. The fake records what it was sent.
func TestTheModelCallIsTheGovernedOne(t *testing.T) {
	f := fakeGateway(t, okReply)
	m := modelFor(f)
	if _, _, err := m.Draft(t.Context(), intake.DraftRequest{Text: "the install link is dead", Candidates: []string{"Smana/ai-native-ref"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.header("Authorization"); got != "Bearer "+plannerKey {
		t.Fatalf("the only header sent: %q", got)
	}
	if got := f.header("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type: %q", got)
	}
	if body := f.body(); body["model"] != "tier-frontier" || body["temperature"] != float64(0) || body["max_tokens"] != float64(4000) {
		t.Fatalf("%v", body)
	}
	if strings.Contains(f.rawBody(), plannerKey) || strings.Contains(f.rawBody(), "provider") {
		t.Fatal("the key or a provider hint reached the body")
	}
	// One call, one alias, and the candidate list the guard handed over is what the model is shown.
	if f.n() != 1 || !strings.Contains(strings.ToLower(f.rawBody()), "smana/ai-native-ref") {
		t.Fatalf("%d calls, body %s", f.n(), f.rawBody())
	}
}

// Review Focus 4: the reply at the bound decodes; one byte over is planner_invalid, never truncated.
func TestAReplyAtTheBoundIsAcceptedAndOneByteOverIsRefused(t *testing.T) {
	body := replyWithIssueBody(16<<10 - 1) // a legal 16 KiB issue body, the whole reply at 64 KiB
	if len(body) != replyBound {
		t.Fatalf("the fixture is %d bytes, the bound is %d", len(body), replyBound)
	}
	m := modelFor(fakeGateway(t, body))
	d, usage, err := m.Draft(t.Context(), req())
	if err != nil {
		t.Fatalf("at the bound: %v", err)
	}
	if len(d.Issues) != 1 || len(d.Issues[0].Body) != 16<<10-1 || usage.CompletionTokens != 340 {
		t.Fatalf("%d issues, %d bytes, %+v", len(d.Issues), len(d.Issues[0].Body), usage)
	}
	f := fakeGateway(t, append(body, ' ')) // one byte past the reply bound
	late := modelFor(f)
	_, _, err = late.Draft(t.Context(), req())
	if !errors.Is(err, intake.ErrInvalid) || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("%v", err)
	}
	if f.n() != 1 {
		t.Fatalf("a retry loop: %d calls", f.n())
	}
}

// Review Focus 5: the deadline, not the client's patience, ends the call — the endpoint sleeps 5 s,
// the model's own deadline is 250 ms, and the call returns well inside 2 s.
func TestADeadlineOverCallIsPlannerTimeout(t *testing.T) {
	f := fakeGateway(t, okReply, slow(5*time.Second))
	start := time.Now()
	m := modelFor(f)
	_, _, err := m.Draft(t.Context(), req())
	if !errors.Is(err, intake.ErrTimeout) || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
}

// planner_invalid: a reply that is not the shape, a reply with unknown fields, a reply with zero
// issues, a reply with four. Each is refused whole, with the attempt counted by the caller.
func TestAnUndecodableReplyIsPlannerInvalid(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		// detail must appear in the error: the field and the bound a person acts on.
		detail string
	}{
		{name: "not JSON at all", body: []byte(`<html><body>502 Bad Gateway</body></html>`), detail: "the gateway's JSON"},
		{name: "an unknown field in the reply",
			body:   replyBody(`{"repository":"smana/ai-native-ref","confidence":0.9,"issues":[{"title":"docs: x","body":"y","class":"docs-links"}]}`),
			detail: "confidence"},
		{name: "zero issues",
			body:   replyBody(`{"repository":"smana/ai-native-ref","issues":[]}`),
			detail: "issues: 0, the contract asks for 1 to 3"},
		{name: "four issues",
			body: replyBody(`{"repository":"smana/ai-native-ref","issues":[{"title":"docs: a","body":"b","class":"docs-links"},` +
				`{"title":"docs: b","body":"b","class":"docs-links"},{"title":"docs: c","body":"b","class":"docs-links"},` +
				`{"title":"docs: d","body":"b","class":"docs-links"}]}`),
			detail: "issues: 4, the contract asks for 1 to 3"},
		{name: "an issue body over 16 KiB",
			body:   replyBody(`{"repository":"smana/ai-native-ref","issues":[{"title":"docs: x","body":"` + strings.Repeat("x", 16<<10+1) + `","class":"docs-links"}]}`),
			detail: "the bound is 16 KiB"},
		{name: "six evidence items",
			body: replyBody(`{"repository":"smana/ai-native-ref","evidence":[{"kind":"path","text":"a"},{"kind":"path","text":"b"},` +
				`{"kind":"path","text":"c"},{"kind":"path","text":"d"},{"kind":"path","text":"e"},{"kind":"path","text":"f"}],` +
				`"issues":[{"title":"docs: x","body":"y","class":"docs-links"}]}`),
			detail: "evidence: 6 items, the bound is 5"},
		{name: "no token counts",
			body:   []byte(`{"choices":[{"message":{"content":"{\"repository\":\"smana/ai-native-ref\",\"issues\":[{\"title\":\"docs: x\",\"body\":\"y\",\"class\":\"docs-links\"}]}"}}]}`),
			detail: "usage"},
		{name: "no choices",
			body:   []byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
			detail: "choices: 0"},
		{name: "an empty answer",
			body:   []byte(`{"choices":[{"message":{"content":""}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
			detail: "content: empty"},
		{name: "a repository the reply did not name",
			body:   replyBody(`{"issues":[{"title":"docs: x","body":"y","class":"docs-links"}]}`),
			detail: "repository: empty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := fakeGateway(t, c.body)
			m := modelFor(f)
			_, _, err := m.Draft(t.Context(), req())
			if !errors.Is(err, intake.ErrInvalid) || !strings.Contains(err.Error(), c.detail) {
				t.Fatalf("%v", err)
			}
			if f.n() != 1 {
				t.Fatalf("%d calls for one attempt", f.n())
			}
		})
	}
}

// A gateway that is down, or answers anything but 200, is planner_unavailable — never a plan built
// on half an answer, and never a second attempt.
func TestAFailingGatewayIsPlannerUnavailable(t *testing.T) {
	down := fakeGateway(t, okReply, status(http.StatusServiceUnavailable))
	md := modelFor(down)
	if _, _, err := md.Draft(t.Context(), req()); !errors.Is(err, intake.ErrUnavailable) || down.n() != 1 {
		t.Fatalf("%v after %d calls", err, down.n())
	}
	// The key is read at use: a file that is not there is a gateway the broker cannot authenticate
	// to, so it is unavailable and no request leaves the process.
	f := fakeGateway(t, okReply)
	m := modelFor(f)
	m.APIKeyFile = filepath.Join(t.TempDir(), "absent")
	if _, _, err := m.Draft(t.Context(), req()); !errors.Is(err, intake.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if f.n() != 0 {
		t.Fatalf("%d calls without a key", f.n())
	}
	// And a model with nothing to call is the same: it fails towards the person, not towards a panic.
	m = modelFor(f)
	m.HC = nil
	if _, _, err := m.Draft(t.Context(), req()); !errors.Is(err, intake.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

// A request that outgrew its bound is refused with the bound, before the call: the model never sees
// a request body the contract does not allow, and the endpoint counts nothing.
func TestAnOverBoundRequestIsRefusedBeforeTheCall(t *testing.T) {
	for _, c := range []struct {
		name string
		req  intake.DraftRequest
		want string
	}{
		{name: "text over 16 KiB",
			req:  intake.DraftRequest{Text: strings.Repeat("a", 16<<10+1), Candidates: []string{"smana/ai-native-ref"}},
			want: "text: 16385 bytes, the bound is 16 KiB"},
		{name: "context over 4 KiB",
			req:  intake.DraftRequest{Text: "the install link is dead", Context: strings.Repeat("a", 4<<10+1), Candidates: []string{"smana/ai-native-ref"}},
			want: "context: 4097 bytes, the bound is 4 KiB"},
		{name: "more than 20 candidates",
			req:  intake.DraftRequest{Text: "the install link is dead", Candidates: make([]string, 21)},
			want: "candidates: 21, the bound is 20"},
		{name: "no candidates at all",
			req:  intake.DraftRequest{Text: "the install link is dead"},
			want: "candidates: 0, the contract asks for at least one"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := fakeGateway(t, okReply)
			m := modelFor(f)
			if _, _, err := m.Draft(t.Context(), c.req); !errors.Is(err, intake.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%v", err)
			}
			if f.n() != 0 {
				t.Fatalf("%d calls for a request that never had one", f.n())
			}
		})
	}
}

// Review Focus 2, re-proved at this task's boundary: a private repository, or one GitHub cannot
// answer for, is refused by the guard and the endpoint counts zero calls — the reply is never asked
// for, so no plan can exist for it.
func TestAPrivateOrUnverifiableRepositoryIsRefusedBeforeTheCall(t *testing.T) {
	f := fakeGateway(t, okReply)
	for _, c := range []struct {
		name   string
		asked  string
		public pub
		want   string
	}{
		{name: "a private repository the requester named",
			asked: "Smana/private", public: pubOf(map[string]bool{"Smana/private": false}),
			want: intake.ReasonPrivateRepository},
		{name: "a repository GitHub cannot answer for",
			asked: "Smana/ai-native-ref", public: pubOf(nil),
			want: intake.ReasonRepositoryUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := intake.Guard{
				Allow:    map[string]bool{"smana/private": true, "smana/ai-native-ref": true},
				MayWrite: func(context.Context, string) (bool, error) { return true, nil },
				Public:   c.public,
			}
			candidates, r := g.Candidates(t.Context(), []string{c.asked})
			if r == nil || r.Reason != c.want {
				t.Fatalf("%+v", r)
			}
			if candidates != nil {
				t.Fatalf("a candidate set for a refused request: %v", candidates)
			}
		})
	}
	if got := f.n(); got != 0 {
		t.Fatalf("%d calls for a candidate set that was refused", got)
	}
	if got := f.rawBody(); got != "" {
		t.Fatalf("the endpoint was sent %q", got)
	}
	// The same request, once the guard answers, does reach the endpoint: the zero above is the
	// refusal and not a client that never works.
	g := intake.Guard{Allow: map[string]bool{"smana/ai-native-ref": true},
		MayWrite: func(context.Context, string) (bool, error) { return true, nil },
		Public:   pubOf(map[string]bool{"Smana/ai-native-ref": true})}
	candidates, r := g.Candidates(t.Context(), []string{"Smana/ai-native-ref"})
	if r != nil {
		t.Fatalf("%+v", r)
	}
	m := modelFor(f)
	if _, _, err := m.Draft(t.Context(), intake.DraftRequest{Text: req().Text, Candidates: candidates}); err != nil {
		t.Fatal(err)
	}
	if got := f.n(); got != 1 {
		t.Fatalf("%d calls for an allowed request", got)
	}
}

// CD17: the classes the plan may carry are the broker's table, and the model is told them. A reply
// that has to guess the vocabulary makes invalid_class the default outcome rather than the exception.
func TestTheAllowedClassesReachThePrompt(t *testing.T) {
	f := fakeGateway(t, okReply)
	m := modelFor(f)
	req := req()
	req.Classes = []string{"ci", "docs-links"}
	if _, _, err := m.Draft(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	body := f.rawBody()
	for _, want := range []string{"ci", "docs-links"} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt does not name the class %q: %s", want, body)
		}
	}
}
