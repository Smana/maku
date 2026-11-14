// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// connectWithout connects over the source without the connect-time mint's ping: the token it
// mints is the cached one.
func connectWithout(t *testing.T, url string, roots *x509.CertPool, src TokenSource) (*GitHub, error) {
	t.Helper()
	return ConnectTokens(t.Context(), Options{Repository: "owner/repo", APIURL: url + "/", UserAgent: "agent-factory/test",
		HTTP: httpx.New(5*time.Second, roots), Now: time.Now}, src)
}

// Unappliable is a refusal that a retry cannot change: gone, locked, or a validation failure.
func TestUnappliable(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":        {nil, false},
		"permanent":  {&PermanentError{Status: 404, Err: errors.New("gone")}, true},
		"a 422":      {fmt.Errorf("forge: x: %w", &statusError{code: 422}), true},
		"a 404":      {&statusError{code: 404}, true},
		"a 500":      {&statusError{code: 500}, false},
		"a 429":      {&statusError{code: 429}, false},
		"a 401":      {&statusError{code: 401}, false},
		"a network":  {errors.New("dial tcp: refused"), false},
		"pr exists":  {ErrPRExists, false},
		"no factory": {ErrNoConnection, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Unappliable(tc.err); got != tc.want {
				t.Fatalf("Unappliable = %t, want %t", got, tc.want)
			}
		})
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
// from the minter (HR11). A source that cannot mint fails the connect.
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
	down := &sourceFunc{f: func() (string, error) { return "", errors.New("minter: status 403") }}
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

// invalidating is a TokenSource that mints ghs_<n> and counts the tokens GitHub refused.
type invalidating struct {
	mu      sync.Mutex
	n, drop int
	token   string
}

func (s *invalidating) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		s.n++
		s.token = fmt.Sprintf("ghs_%d", s.n)
	}
	return s.token, nil
}

func (s *invalidating) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop++
	s.token = ""
}

// A connection whose API answers 401 drops the source's token, so the next call mints another.
func TestConnectTokensInvalidate(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer ghs_1" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"resources":{}}`)
	}))
	t.Cleanup(api.Close)
	roots := x509.NewCertPool()
	roots.AddCert(api.Certificate())
	src := &invalidating{}
	conn, err := connectWithout(t, api.URL, roots, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Ping(t.Context()); err == nil {
		t.Fatal("a 401 answered")
	}
	if err := conn.Ping(t.Context()); err != nil {
		t.Fatalf("the next call did not mint again: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if src.drop != 1 || len(seen) < 2 || seen[len(seen)-1] != "Bearer ghs_2" {
		t.Errorf("dropped %d, bearers %q", src.drop, seen)
	}
}
