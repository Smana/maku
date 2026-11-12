// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// fakeSTS is octo-sts's exchange: it takes the projected token as the bearer and answers the
// scope's installation token for the identity.
type fakeSTS struct {
	mu      sync.Mutex
	srv     *httptest.Server
	bearers []string
	queries []string
	reply   func(n int) string // the n-th exchange's JSON reply, from 1
	status  int
}

func newFakeSTS(t *testing.T) *fakeSTS {
	t.Helper()
	s := &fakeSTS{reply: func(int) string { return `{"token":"ghs_published"}` }}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.bearers = append(s.bearers, r.Header.Get("Authorization"))
		s.queries = append(s.queries, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		n, status, reply := len(s.bearers), s.status, s.reply
		s.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"message":"no policy","token":"ghs_never"}`, status)
			return
		}
		_, _ = io.WriteString(w, reply(n))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeSTS) client() *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(s.srv.Certificate())
	return httpx.New(5*time.Second, roots)
}

func (s *fakeSTS) calls() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bearers...), append([]string(nil), s.queries...)
}

// The publisher's token (HR11): the factory's projected ServiceAccount token, read at each
// exchange, traded at octo-sts for the repository's publisher identity, cached until ten minutes
// before it expires, and never written to a log.
func TestOctoSTSTokens(t *testing.T) {
	sts := newFakeSTS(t)
	clk := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	file := filepath.Join(t.TempDir(), "token")
	write := func(v string) {
		if err := os.WriteFile(file, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("sa-token-1")
	var logs bytes.Buffer
	o := &OctoSTS{URL: sts.srv.URL, TokenFile: file, Scope: "owner/repo", Identity: "publisher",
		HTTP: sts.client(), Now: clk.now, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	sts.reply = func(n int) string {
		if n == 1 {
			return `{"token":"ghs_one","expires_at":"2026-11-12T11:00:00Z"}`
		}
		return `{"token":"ghs_two"}`
	}

	tok, err := o.Token(t.Context())
	if err != nil || tok != "ghs_one" {
		t.Fatalf("first token %q %v", tok, err)
	}
	clk.add(49 * time.Minute) // 11 minutes before expires_at: still cached
	if tok, err = o.Token(t.Context()); err != nil || tok != "ghs_one" {
		t.Fatalf("cached token %q %v", tok, err)
	}
	write("sa-token-2")      // the kubelet rotated the projected token
	clk.add(2 * time.Minute) // 9 minutes before expires_at: renewed
	if tok, err = o.Token(t.Context()); err != nil || tok != "ghs_two" {
		t.Fatalf("renewed token %q %v", tok, err)
	}
	bearers, queries := sts.calls()
	if len(bearers) != 2 || bearers[0] != "Bearer sa-token-1" || bearers[1] != "Bearer sa-token-2" {
		t.Errorf("bearers %q: the projected token is read at each exchange", bearers)
	}
	for _, q := range queries {
		if q != "GET /sts/exchange?identity=publisher&scope=owner%2Frepo" && q != "GET /sts/exchange?scope=owner%2Frepo&identity=publisher" {
			t.Errorf("exchange %q", q)
		}
	}
	// A reply with no expires_at is an installation token's hour from its exchange.
	clk.add(49 * time.Minute)
	if tok, _ = o.Token(t.Context()); tok != "ghs_two" {
		t.Errorf("an hour's token is cached for 50 minutes: %q", tok)
	}
	clk.add(2 * time.Minute)
	if _, _ = o.Token(t.Context()); len(func() []string { b, _ := sts.calls(); return b }()) != 3 {
		t.Error("an hour's token is renewed past 50 minutes")
	}

	for name, tc := range map[string]struct {
		o    *OctoSTS
		fail int
	}{
		"a non-https URL":           {o: &OctoSTS{URL: "http://sts.example/", TokenFile: file, Scope: "owner/repo", Identity: "publisher"}},
		"a URL with credentials":    {o: &OctoSTS{URL: "https://u:p@sts.example/", TokenFile: file, Scope: "owner/repo", Identity: "publisher"}},
		"a scope that is no repo":   {o: &OctoSTS{URL: sts.srv.URL, TokenFile: file, Scope: "owner", Identity: "publisher"}},
		"no identity":               {o: &OctoSTS{URL: sts.srv.URL, TokenFile: file, Scope: "owner/repo"}},
		"no token file":             {o: &OctoSTS{URL: sts.srv.URL, TokenFile: filepath.Join(t.TempDir(), "absent"), Scope: "owner/repo", Identity: "publisher"}},
		"octo-sts refuses":          {o: &OctoSTS{URL: sts.srv.URL, TokenFile: file, Scope: "owner/repo", Identity: "publisher"}, fail: http.StatusForbidden},
		"octo-sts answers no token": {o: &OctoSTS{URL: sts.srv.URL, TokenFile: file, Scope: "owner/repo", Identity: "publisher"}, fail: -1},
	} {
		t.Run(name, func(t *testing.T) {
			sts.mu.Lock()
			sts.status = max(tc.fail, 0)
			if tc.fail == -1 {
				sts.reply = func(int) string { return `{"token":""}` }
			}
			sts.mu.Unlock()
			tc.o.HTTP, tc.o.Now, tc.o.Log = sts.client(), clk.now, o.Log
			if tok, err := tc.o.Token(t.Context()); err == nil || tok != "" {
				t.Fatalf("token %q, error %v", tok, err)
			} else if strings.Contains(err.Error(), "ghs_") || strings.Contains(err.Error(), "sa-token") {
				t.Errorf("the error quotes a token: %v", err)
			}
		})
	}
	if strings.Contains(logs.String(), "ghs_") || strings.Contains(logs.String(), "sa-token") {
		t.Errorf("a token reached the log:\n%s", logs.String())
	}
}

// fakeAPI is GitHub's REST API for a connection whose token comes from a TokenSource: every call
// must carry the source's token.
type fakeAPI struct {
	mu     sync.Mutex
	srv    *httptest.Server
	token  string
	calls  []string
	bodies []map[string]any
}

func newFakeAPI(t *testing.T, token string) *fakeAPI {
	t.Helper()
	a := &fakeAPI{token: token}
	mux := http.NewServeMux()
	record := func(r *http.Request) bool {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.calls = append(a.calls, r.Method+" "+r.URL.Path)
		a.bodies = append(a.bodies, in)
		return r.Header.Get("Authorization") == "Bearer "+a.token
	}
	mux.HandleFunc("GET /rate_limit", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"resources":{}}`)
	})
	mux.HandleFunc("POST /repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		a.mu.Lock()
		head := a.bodies[len(a.bodies)-1]["head"]
		a.mu.Unlock()
		if head == "agent/exists" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for owner:agent/exists."}]}`)
			return
		}
		if head == "agent/invalid" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"base","code":"invalid"}]}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":12,"node_id":"PR_12","html_url":"https://github.com/owner/repo/pull/12","state":"open",
		 "title":"docs: fix","body":"b","draft":true,"user":{"login":"ogenki-agents[bot]"},"head":{"ref":"agent/abc","sha":"h1"}}`)
	})
	mux.HandleFunc("PATCH /repos/owner/repo/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"number":12,"node_id":"PR_12","html_url":"https://github.com/owner/repo/pull/12","state":"open",
		 "title":"docs: fixed","body":"b2","user":{"login":"ogenki-agents[bot]"},"head":{"ref":"agent/abc","sha":"h1"}}`)
	})
	a.srv = httptest.NewTLSServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAPI) options() Options {
	roots := x509.NewCertPool()
	roots.AddCert(a.srv.Certificate())
	return Options{Repository: "owner/repo", APIURL: a.srv.URL + "/", UserAgent: "agent-factory/test",
		HTTP: httpx.New(5*time.Second, roots), Now: time.Now}
}

// sourceFunc is a TokenSource counting its mints.
type sourceFunc struct {
	mu sync.Mutex
	n  int
	f  func() (string, error)
}

func (s *sourceFunc) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.f()
}

// ConnectTokens is a connection whose every call carries a TokenSource's token: the publisher's,
// from octo-sts (HR11). A source that cannot mint fails the connect.
func TestConnectTokens(t *testing.T) {
	api := newFakeAPI(t, "ghs_published")
	src := &sourceFunc{f: func() (string, error) { return "ghs_published", nil }}
	g, err := ConnectTokens(t.Context(), api.options(), src)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Ping(t.Context()); err != nil {
		t.Fatalf("ping with the source's token: %v", err)
	}
	if src.n < 2 {
		t.Errorf("the source minted %d times: every call asks it", src.n)
	}
	down := &sourceFunc{f: func() (string, error) { return "", errors.New("octo-sts: status 403") }}
	if _, err := ConnectTokens(t.Context(), api.options(), down); err == nil {
		t.Error("a source that cannot mint connected")
	}
	if _, err := ConnectTokens(t.Context(), api.options(), nil); err == nil {
		t.Error("no source connected")
	}
}

// The publisher opens and edits the task's pull request (HR18: draft on open only).
func TestCreateEditPR(t *testing.T) {
	api := newFakeAPI(t, "ghs_published")
	g, err := ConnectTokens(t.Context(), api.options(), &sourceFunc{f: func() (string, error) { return "ghs_published", nil }})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := g.CreatePR(t.Context(), "main", "agent/abc", "docs: fix", "the body", true)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 12 || pr.URL != "https://github.com/owner/repo/pull/12" || pr.NodeID != "PR_12" || pr.HeadRef != "agent/abc" {
		t.Errorf("created %+v", pr)
	}
	api.mu.Lock()
	in := api.bodies[len(api.bodies)-1]
	api.mu.Unlock()
	if in["base"] != "main" || in["head"] != "agent/abc" || in["title"] != "docs: fix" || in["body"] != "the body" || in["draft"] != true {
		t.Errorf("create body %v", in)
	}
	if pr, err = g.EditPR(t.Context(), 12, "docs: fixed", "b2"); err != nil || pr.Title != "docs: fixed" {
		t.Fatalf("edit %+v %v", pr, err)
	}
	api.mu.Lock()
	in = api.bodies[len(api.bodies)-1]
	api.mu.Unlock()
	if _, ok := in["draft"]; ok || in["title"] != "docs: fixed" || in["body"] != "b2" || len(in) != 2 {
		t.Errorf("an edit changes the title and the body only: %v", in)
	}
	if _, err := g.CreatePR(t.Context(), "main", "agent/exists", "docs: fix", "the body", false); !errors.Is(err, ErrPRExists) {
		t.Errorf("an existing pull request: %v, want ErrPRExists", err)
	}
	if _, err := g.CreatePR(t.Context(), "main", "agent/invalid", "docs: fix", "the body", false); err == nil || errors.Is(err, ErrPRExists) {
		t.Errorf("another 422: %v, want an error that is not ErrPRExists", err)
	}
}
