// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/sponsored"
)

type rpc struct {
	Method string `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

// fakeBroker answers every tools/call with result, recording what it was sent. accept is the one
// bearer it takes; "" accepts any.
type fakeBroker struct {
	mu      sync.Mutex
	calls   []rpc
	headers []http.Header
	accept  string
	result  string
	srv     *httptest.Server
}

func newFakeBroker(t *testing.T, accept, result string) *fakeBroker {
	t.Helper()
	f := &fakeBroker{accept: accept, result: result}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var c rpc
		_ = json.Unmarshal(b, &c)
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if f.accept != "" && r.Header.Get("Authorization") != "Bearer "+f.accept {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, f.result)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func staticTokens(tok string) *Tokens {
	return &Tokens{Fetch: func(context.Context) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: tok, Expiry: time.Now().Add(time.Hour)}, nil
	}}
}

func TestBrokerIssueDraft(t *testing.T) {
	f := newFakeBroker(t, "tok", `{"structuredContent":{"repo":"Smana/x","title":"t2","body":"b2"}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	d, err := b.IssueDraft(context.Background(), Draft{Repo: "Smana/x", Title: "t", Body: "b"})
	if err != nil || d != (Draft{Repo: "Smana/x", Title: "t2", Body: "b2"}) {
		t.Fatalf("%+v %v", d, err)
	}
	c := f.calls[0]
	if c.Method != "tools/call" || c.Params.Name != "issue_draft" ||
		!maps.Equal(c.Params.Arguments, map[string]any{"repo": "Smana/x", "title": "t", "body": "b"}) {
		t.Fatalf("%+v", c)
	}
	if h := f.headers[0]; h.Get("MCP-Protocol-Version") != "2025-11-25" || h.Get("Authorization") != "Bearer tok" {
		t.Fatalf("%v", h)
	}
}

func TestBrokerRefusalIsRefused(t *testing.T) {
	f := newFakeBroker(t, "", `{"isError":true,"content":[{"type":"text","text":"rejected: template"}]}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	_, err := b.IssueDraft(context.Background(), Draft{Repo: "Smana/x", Title: "t"})
	var r Refused
	if !errors.As(err, &r) || string(r) != "rejected: template" || err.Error() != "rejected: template" {
		t.Fatalf("%T %v", err, err)
	}
}

func TestBrokerRunCreate(t *testing.T) {
	f := newFakeBroker(t, "", `{"structuredContent":{"room":"r-1","issue":42,"duplicate":true}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	body := "## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz"
	ref := "slack/T01ABC/C03/1729.0001"
	res, err := b.RunCreate(context.Background(), RunCreate{
		Draft:      Draft{Repo: "Smana/ai-native-ref", Title: "docs(agents): the link is dead", Body: body},
		MaxCostUSD: 2, OnBehalfOf: "slack:T01ABC/U02DEF", Ref: ref, Key: "T01ABC/C03/1729.0002"})
	if err != nil || res != (RunResult{Room: "r-1", Issue: 42, Duplicate: true}) {
		t.Fatalf("%+v %v", res, err)
	}
	want := map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(agents): the link is dead",
		"text": body, "dataClass": "public",
		"max_cost": 2.0, "on_behalf_of": "slack:T01ABC/U02DEF", "ref": ref,
		"idempotency_key": "T01ABC/C03/1729.0002"}
	c := f.calls[0]
	if c.Params.Name != "run_create" || !maps.Equal(c.Params.Arguments, want) {
		t.Fatalf("%+v", c.Params)
	}
	if !sponsored.ValidRef(ref) {
		t.Fatalf("broker would refuse ref %q", ref)
	}
}

func TestBrokerNeverSendsTasks(t *testing.T) {
	f := newFakeBroker(t, "", `{"structuredContent":{}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	_, _ = b.IssueDraft(context.Background(), Draft{})
	_, _ = b.RunCreate(context.Background(), RunCreate{})
	for _, c := range f.calls {
		if strings.HasPrefix(c.Method, "tasks/") {
			t.Fatalf("sent %s", c.Method)
		}
	}
}

// §1: Decide posts §1's body to the decide route as the App's principal — not to /mcp, no tools
// envelope — reads the closed code back, retries once on a 401, and reads a non-200 (or an answer
// that names no code) as transport rather than as a decision. This is the route's own test: the
// worker that calls it is interactions_test.go's.
func TestDecideCall(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]string
	var paths, auth []string
	answer := `{"result":"approved"}`
	setAnswer := func(a string) { mu.Lock(); answer = a; mu.Unlock() }
	var asks atomic.Int32
	var fails atomic.Int32 // the next n answers are 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		bodies, paths, auth = append(bodies, m), append(paths, r.URL.Path), append(auth, r.Header.Get("Authorization"))
		now := answer
		mu.Unlock()
		asks.Add(1)
		if fails.Load() > 0 {
			fails.Add(-1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, now)
	}))
	t.Cleanup(srv.Close)
	b := Broker{URL: srv.URL, Tokens: staticTokens("tok"), HC: srv.Client()}
	d := Decision{Team: "T01ABC", User: "U02DEF", Channel: "C03", MessageTS: "1729.0500",
		ApprovalID: "01M4A1B2C3D4E5F6G7H8J9K0MN", Branch: "agent/26zfnuxm", Decision: "approved"}

	res, err := b.Decide(context.Background(), d)
	if err != nil || res != (DecisionResult{Result: "approved"}) {
		t.Fatalf("%+v %v", res, err)
	}
	want := map[string]string{"team": "T01ABC", "user": "U02DEF", "channel": "C03", "messageTS": "1729.0500",
		"approvalId": "01M4A1B2C3D4E5F6G7H8J9K0MN", "branch": "agent/26zfnuxm", "decision": "approved"}
	if !maps.Equal(bodies[0], want) || paths[0] != "/api/slack/decisions" || auth[0] != "Bearer tok" {
		t.Fatalf("%+v %s %s", bodies[0], paths[0], auth[0])
	}

	// Every closed code is read back as is: the route answers 200 for all of them, and the App's
	// worker is what gives one its line.
	for _, code := range []string{"denied", "already_decided", "four_eyes", "not_supported", "sealed"} {
		setAnswer(`{"result":"` + code + `"}`)
		if res, err := b.Decide(context.Background(), d); err != nil || res.Result != code {
			t.Fatalf("%s: %+v %v", code, res, err)
		}
	}
	setAnswer(`{"result":"approved"}`)

	// A 401 drops the cached token and retries once, as every other call does.
	fails.Store(1)
	if _, err := b.Decide(context.Background(), d); err != nil || asks.Load() != 8 {
		t.Fatalf("err %v after %d asks, want a retried press", err, asks.Load())
	}

	// The route answers a closed code with 200; anything else is transport, never a decision.
	fails.Store(2)
	if _, err := b.Decide(context.Background(), d); err == nil {
		t.Fatal("two 401s were read as a decision")
	}
	// An answer that names no code is not a decision either.
	for _, body := range []string{`{}`, `{"result":""}`, `not json`} {
		setAnswer(body)
		if _, err := b.Decide(context.Background(), d); err == nil {
			t.Fatalf("%s was read as a decision", body)
		}
	}
}

// L9: a 401 drops the token and retries once on a new one; a second 401 fails.
func TestBrokerRefreshesOn401(t *testing.T) {
	f := newFakeBroker(t, "t2", `{"structuredContent":{"repo":"Smana/x","title":"t","body":"b"}}`)
	var n atomic.Int32
	tokens := &Tokens{Fetch: func(context.Context) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: fmt.Sprintf("t%d", n.Add(1)), Expiry: time.Now().Add(time.Hour)}, nil
	}}
	b := Broker{URL: f.srv.URL, Tokens: tokens, HC: f.srv.Client()}
	if _, err := b.IssueDraft(context.Background(), Draft{Repo: "Smana/x", Title: "t"}); err != nil || n.Load() != 2 {
		t.Fatalf("err %v fetches %d", err, n.Load())
	}

	f2 := newFakeBroker(t, "never", `{}`)
	n.Store(0)
	tokens = &Tokens{Fetch: func(context.Context) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: fmt.Sprintf("t%d", n.Add(1)), Expiry: time.Now().Add(time.Hour)}, nil
	}}
	b = Broker{URL: f2.srv.URL, Tokens: tokens, HC: f2.srv.Client()}
	if _, err := b.IssueDraft(context.Background(), Draft{Repo: "Smana/x", Title: "t"}); err == nil || n.Load() != 2 {
		t.Fatalf("err %v fetches %d", err, n.Load())
	}
}

// The token cache is reused until it expires.
func TestTokensCache(t *testing.T) {
	var n int
	s := &Tokens{Fetch: func(context.Context) (*oauth2.Token, error) {
		n++
		return &oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(time.Hour)}, nil
	}}
	for range 3 {
		if _, err := s.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("fetched %d times", n)
	}
}

// L9: the token fetch goes through the audited egress client, passed in the context.
func TestTokenFetchUsesHTTPClientFromContext(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	cc := clientcredentials.Config{ClientID: "i", ClientSecret: "s", TokenURL: srv.URL + "/oauth/v2/token"}
	hc := httpx.New(5*time.Second, nil)
	tokens := &Tokens{Fetch: func(ctx context.Context) (*oauth2.Token, error) {
		return cc.Token(context.WithValue(ctx, oauth2.HTTPClient, hc))
	}}
	if tok, err := tokens.Token(context.Background()); err != nil || tok != "x" || hits.Load() != 1 {
		t.Fatalf("%q %v %d", tok, err, hits.Load())
	}
}

// slack_link_code's answer reaches the user only as the link page with one code; slack_unlink says
// whether a link went; a broker without the tools is ErrUnknownTool.
func TestBrokerSlackLink(t *testing.T) {
	f := newFakeBroker(t, "tok", `{"structuredContent":{"url":"https://rooms.example/link/slack?code=abcdefghijklmnopqrstuvwxyz","expiresAt":"x"}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	u, err := b.SlackLinkCode(context.Background(), "T01ABC", "U02DEF", "dana.h", "dana")
	if err != nil || u != "https://rooms.example/link/slack?code=abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("%q %v", u, err)
	}
	if c := f.calls[0]; c.Params.Name != "slack_link_code" || c.Params.Arguments["team"] != "T01ABC" || c.Params.Arguments["user"] != "U02DEF" ||
		c.Params.Arguments["handle"] != "dana.h" || c.Params.Arguments["name"] != "dana" {
		t.Fatalf("%+v", c)
	}
	for _, bad := range []string{"https://evil.example/phish?code=abcdefghijklmnopqrstuvwxyz", "https://rooms.example/link/slack?code=x",
		"https://rooms.example/link/slack?code=abcdefghijklmnopqrstuvwxyz&next=evil", "javascript:alert(1)"} {
		f.result = `{"structuredContent":{"url":` + jsonString(bad) + `}}`
		if _, err := b.SlackLinkCode(context.Background(), "T01ABC", "U02DEF", "", ""); err == nil {
			t.Errorf("%s: passed on", bad)
		}
	}
	f.result = `{"structuredContent":{"unlinked":true}}`
	if gone, err := b.SlackUnlink(context.Background(), "T01ABC", "U02DEF"); err != nil || !gone {
		t.Fatalf("%v %v", gone, err)
	}
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool: slack_link_code"}}`)
	}))
	t.Cleanup(off.Close)
	b.URL, b.HC = off.URL, off.Client()
	if _, err := b.SlackLinkCode(context.Background(), "T01ABC", "U02DEF", "", ""); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("off: %v", err)
	}
}
