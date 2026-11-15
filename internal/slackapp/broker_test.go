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
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intake"
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

func TestBrokerRefusalIsRefused(t *testing.T) {
	f := newFakeBroker(t, "", `{"isError":true,"content":[{"type":"text","text":"rejected: template"}]}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	_, err := b.RunCreate(context.Background(), RunCreate{Repo: "Smana/x", Title: "t"})
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
		Repo: "Smana/ai-native-ref", Title: "docs(agents): the link is dead", Body: body,
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

// A draft's run_create arguments are exactly today's; a proposal's send handoff and plan_hash in
// place of title, text and plan (UX2).
func TestRunCreateSendsTheHandoff(t *testing.T) {
	f := newFakeBroker(t, "", `{"structuredContent":{"room":"r-1","issue":42}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
	body := "## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz"
	draft := RunCreate{
		Repo: "Smana/ai-native-ref", Title: "docs(agents): the link is dead", Body: body,
		MaxCostUSD: 2, OnBehalfOf: "slack:T01ABC/U02DEF", Ref: "slack/T01ABC/C03/1729.0001", Key: "T01ABC/C03/1729.0001"}

	if _, err := b.RunCreate(context.Background(), draft); err != nil {
		t.Fatalf("run_create: %v", err)
	}
	plain := map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(agents): the link is dead",
		"text": body, "dataClass": "public", "max_cost": 2.0, "on_behalf_of": "slack:T01ABC/U02DEF",
		"ref": "slack/T01ABC/C03/1729.0001", "idempotency_key": "T01ABC/C03/1729.0001"}
	if got := f.calls[0].Params.Arguments; !reflect.DeepEqual(got, plain) {
		t.Fatalf("the draft's arguments moved: %+v", got)
	}

	plan := planFixture(t, "slack-one-issue")
	proposal := RunCreate{
		Repo:       "Smana/ai-native-ref",
		Handoff:    defaultHandoffID,
		PlanHash:   intake.Hash(plan),
		MaxCostUSD: 2, OnBehalfOf: "slack:T01ABC/U02DEF", Ref: "slack/T01ABC/C03/1729.0001", Key: "T01ABC/C03/1729.0001"}
	if _, err := b.RunCreate(context.Background(), proposal); err != nil {
		t.Fatalf("run_create with a handoff: %v", err)
	}
	want := map[string]any{"repo": "Smana/ai-native-ref", "handoff": defaultHandoffID, "plan_hash": intake.Hash(plan),
		"dataClass": "public", "max_cost": 2.0, "on_behalf_of": "slack:T01ABC/U02DEF",
		"ref": "slack/T01ABC/C03/1729.0001", "idempotency_key": "T01ABC/C03/1729.0001"}
	got := f.calls[1].Params.Arguments
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a proposal's arguments: %+v, want %+v", got, want)
	}
}

func TestBrokerNeverSendsTasks(t *testing.T) {
	f := newFakeBroker(t, "", `{"structuredContent":{}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}
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
	if _, err := b.RunCreate(context.Background(), RunCreate{Repo: "Smana/x", Title: "t"}); err != nil || n.Load() != 2 {
		t.Fatalf("err %v fetches %d", err, n.Load())
	}

	f2 := newFakeBroker(t, "never", `{}`)
	n.Store(0)
	tokens = &Tokens{Fetch: func(context.Context) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: fmt.Sprintf("t%d", n.Add(1)), Expiry: time.Now().Add(time.Hour)}, nil
	}}
	b = Broker{URL: f2.srv.URL, Tokens: tokens, HC: f2.srv.Client()}
	if _, err := b.RunCreate(context.Background(), RunCreate{Repo: "Smana/x", Title: "t"}); err == nil || n.Load() != 2 {
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

// handoff's schema exactly: a mention sends text and candidates, a reply continues by id and answer;
// handoff_check reads or pins the stored plan's workflow.
func TestHandoffCall(t *testing.T) {
	f := newFakeBroker(t, "", `{"structuredContent":{"id":"`+defaultHandoffID+`","text":"Repository Smana/ai-native-ref","question":{"text":"Which repository should this go to?","recommended":"Smana/ai-native-ref"},"ready":false,"workflow":"links-solo","budget":1.5}}`)
	b := Broker{URL: f.srv.URL, Tokens: staticTokens("tok"), HC: f.srv.Client()}

	got, err := b.Handoff(context.Background(), HandoffRequest{Text: "the install link in the README is dead",
		OnBehalfOf: "slack:T01ABC/U02DEF", Candidates: []string{"Smana/ai-native-ref"}, CeilingUSD: 1.5})
	want := HandoffResult{ID: defaultHandoffID, Text: "Repository Smana/ai-native-ref",
		Question: &HandoffQuestion{Text: "Which repository should this go to?", Recommended: "Smana/ai-native-ref"},
		Ready:    false, Workflow: "links-solo", Budget: 1.5}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("the result is %+v (%v), want %+v", got, err, want)
	}
	c := f.calls[0]
	if c.Method != "tools/call" || c.Params.Name != "handoff" {
		t.Fatalf("%+v", c)
	}
	wantArgs := map[string]any{"text": "the install link in the README is dead",
		"candidates": []any{"Smana/ai-native-ref"}, "on_behalf_of": "slack:T01ABC/U02DEF", "ceiling": 1.5}
	if !reflect.DeepEqual(c.Params.Arguments, wantArgs) {
		t.Fatalf("the arguments are %+v, want exactly %+v", c.Params.Arguments, wantArgs)
	}

	if _, err := b.Handoff(context.Background(), HandoffRequest{ID: defaultHandoffID, Answer: "the README in root",
		OnBehalfOf: "slack:T01ABC/U02DEF", CeilingUSD: 1.5}); err != nil {
		t.Fatalf("handoff continuation: %v", err)
	}
	cont := f.calls[1].Params.Arguments
	wantCont := map[string]any{"id": defaultHandoffID, "answer": "the README in root",
		"on_behalf_of": "slack:T01ABC/U02DEF", "ceiling": 1.5}
	if !reflect.DeepEqual(cont, wantCont) {
		t.Fatalf("continuation arguments: %+v, want %+v", cont, wantCont)
	}
}

// UX2, UX4: over a mention, revise, workflow select, confirm, and status / needs me / what changed
// flow, the App calls handoff, handoff_check, run_create, task, and inbox — never intake_plan,
// classes_list, room_summary, room_changes, or needs_list.
func TestAppCallsOnlyNewTools(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	plan.Issues[0].Suggestion = &intake.Suggestion{Profile: "links-solo", Roles: []string{"implementer"}}

	var mu sync.Mutex
	var tools []string
	var runCreateArgs map[string]any
	curPlan := clonePlan(t, plan)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpc
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode rpc: %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		tools = append(tools, req.Params.Name)
		var out string
		switch req.Params.Name {
		case "handoff":
			if req.Params.Arguments["id"] != nil {
				curPlan.Issues[0].Title = "docs: fix the install and quickstart links"
			}
			out = `{"structuredContent":{"id":"` + defaultHandoffID + `","text":"Repository Smana/ai-native-ref","ready":true,"workflow":"links-solo","budget":2}}`
		case "handoff_check":
			if wf, ok := req.Params.Arguments["workflow"].(string); ok {
				curPlan.Issues[0].Profile = wf
			}
			raw, err := curPlan.JSON()
			if err != nil {
				t.Errorf("plan json: %v", err)
			}
			hash := intake.Hash(curPlan)
			out = `{"structuredContent":{"plan":` + string(raw) + `,"hash":"` + hash + `","workflows":["links-solo","links-pair"],"repository":` + jsonString(curPlan.Repository) + `,"title":` + jsonString(curPlan.Issues[0].Title) + `}}`
		case "run_create":
			runCreateArgs = maps.Clone(req.Params.Arguments)
			out = `{"structuredContent":{"room":"26zfnuxm","issue":42,"sponsor":"dana"}}`
		case "task":
			out = `{"structuredContent":{"room":"26zfnuxm","url":"https://rooms.example/r/26zfnuxm","status":{"phase":"Running","state":"running","issue":{"number":42,"title":"docs: fix the install and quickstart links"}},"items":[{"seq":1,"kind":"run_started","text":"run 1 started"}]}}`
		case "inbox":
			out = `{"structuredContent":{"items":[{"kind":"review","id":"7","what":"review PR #7","action":{"kind":"review","label":"review PR #7","url":"https://github.com/Smana/ai-native-ref/pull/7"}}]}}`
		default:
			t.Errorf("unexpected tool call %q", req.Params.Name)
			out = `{"structuredContent":{}}`
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":`+out+`}`)
	}))
	t.Cleanup(srv.Close)

	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.app.Broker = Broker{URL: srv.URL, Tokens: staticTokens("tok"), HC: srv.Client()}

	// 1. Mention -> handoff + handoff_check
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("posts after mention: %+v", posts)
	}
	propTS := posts[0].ts
	p1, ok := posts[0].meta.proposalPayload()
	if !ok || p1.ID != defaultHandoffID {
		t.Fatalf("proposal payload: %+v (%v)", p1, ok)
	}

	// 2. Revise reply -> handoff {id, answer} + handoff_check
	rig.replySaid("U02DEF", "1729.9001", "also fix the quickstart link")
	edits := rig.slack.proposalEdits()
	if len(edits) != 1 {
		t.Fatalf("edits after reply: %+v", edits)
	}
	p2, ok := edits[0].meta.proposalPayload()
	if !ok || p2.Rev != 2 {
		t.Fatalf("revised payload: %+v (%v)", p2, ok)
	}

	// 3. Workflow select -> handoff_check {id, workflow}
	if st := rig.interact(selectPressOf(t, propTS, proposalPresser, profileValue(p2.Hash, 2, "links-pair"))); st != http.StatusOK {
		t.Fatalf("select status: %d", st)
	}
	edits = rig.slack.proposalEdits()
	if len(edits) != 2 {
		t.Fatalf("edits after workflow select: %+v", edits)
	}
	p3, ok := edits[1].meta.proposalPayload()
	if !ok || p3.Rev != 3 {
		t.Fatalf("pinned payload: %+v (%v)", p3, ok)
	}

	// 4. Confirm -> run_create {handoff, plan_hash}
	if st := rig.interact(proposalPressOf(t, propTS, proposalPresser, actionProposalConfirm, proposalValue(p3.Hash, 3))); st != http.StatusOK {
		t.Fatalf("confirm status: %d", st)
	}

	// 5. Thread questions -> task and inbox
	rig.replySaid("U02DEF", "1729.9002", "status")
	rig.replySaid("U02DEF", "1729.9003", "what changed")
	rig.replySaid("U02DEF", "1729.9004", "needs me")

	mu.Lock()
	called := slices.Clone(tools)
	rc := maps.Clone(runCreateArgs)
	mu.Unlock()

	for _, must := range []string{"handoff", "handoff_check", "run_create", "task", "inbox"} {
		if !slices.Contains(called, must) {
			t.Errorf("flow never called %q; called %v", must, called)
		}
	}
	for _, replaced := range []string{"intake_plan", "classes_list", "room_summary", "room_changes", "needs_list", "rooms_list", "issue_draft"} {
		if slices.Contains(called, replaced) {
			t.Errorf("flow called replaced tool %q; called %v", replaced, called)
		}
	}
	if rc["handoff"] != defaultHandoffID || rc["plan"] != nil || rc["text"] != nil || rc["title"] != nil {
		t.Fatalf("run_create args: %+v", rc)
	}
}
