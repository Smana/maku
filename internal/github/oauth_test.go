// SPDX-License-Identifier: Apache-2.0

package github

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeOAuth is GitHub's web flow endpoints and GET /user, on one TLS server.
type fakeOAuth struct {
	mu       sync.Mutex
	revoked  []string
	exchange func(form url.Values) (int, string)
}

func (f *fakeOAuth) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || r.ParseForm() != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		status, body := f.exchange(r.PostForm)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"id": 4242, "login": "dev", "name": "x"}`)
	})
	mux.HandleFunc("DELETE /applications/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		var in struct {
			AccessToken string `json:"access_token"`
		}
		if !ok || u != r.PathValue("id") || p != "csecret" || json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.revoked = append(f.revoked, in.AccessToken)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func oauthFor(t *testing.T, f *fakeOAuth) OAuth {
	t.Helper()
	ts := httptest.NewTLSServer(f.handler())
	t.Cleanup(ts.Close)
	return OAuth{WebURL: ts.URL, APIURL: ts.URL + "/", HC: ts.Client()}
}

// The authorize URL carries the client, the redirect, the state, the S256 challenge and no signup.
func TestOAuthAuthorizeURL(t *testing.T) {
	o := OAuth{WebURL: "https://github.example.test/"}
	got, err := url.Parse(o.AuthorizeURL("Iv1.abc", "https://rooms.example.test/link/github/callback", "st", "ch"))
	if err != nil || got.Scheme != "https" || got.Host != "github.example.test" || got.Path != "/login/oauth/authorize" {
		t.Fatalf("url: %v %v", got, err)
	}
	want := url.Values{"client_id": {"Iv1.abc"}, "redirect_uri": {"https://rooms.example.test/link/github/callback"}, "state": {"st"},
		"code_challenge": {"ch"}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}}
	if got.Query().Encode() != want.Encode() {
		t.Fatalf("query = %s, want %s", got.Query().Encode(), want.Encode())
	}
}

// The exchange sends the code with its verifier; GitHub's error word, or a status, names a refusal
// and no error ever quotes the code, the verifier, the secret or the token.
func TestOAuthExchangeUserRevoke(t *testing.T) {
	var seen url.Values
	f := &fakeOAuth{exchange: func(form url.Values) (int, string) {
		seen = form
		switch form.Get("code") {
		case "good":
			return http.StatusOK, `{"access_token": "gho_secret", "token_type": "bearer"}`
		case "denied":
			return http.StatusOK, `{"error": "bad_verification_code", "error_description": "gho_leak"}`
		default:
			return http.StatusInternalServerError, `gho_leak`
		}
	}}
	o := oauthFor(t, f)
	ctx := t.Context()
	tok, err := o.ExchangeCode(ctx, "Iv1.abc", "csecret", "good", "pkce-v3r1f1er", "https://rooms.example.test/link/github/callback")
	if err != nil || tok != "gho_secret" {
		t.Fatalf("exchange: %q %v", tok, err)
	}
	if seen.Get("client_id") != "Iv1.abc" || seen.Get("client_secret") != "csecret" || seen.Get("code_verifier") != "pkce-v3r1f1er" ||
		seen.Get("redirect_uri") != "https://rooms.example.test/link/github/callback" {
		t.Fatalf("the exchange sent %v", seen)
	}
	for _, code := range []string{"denied", "boom"} {
		_, err := o.ExchangeCode(ctx, "Iv1.abc", "csecret", code, "pkce-v3r1f1er", "https://r/cb")
		if err == nil {
			t.Fatalf("%s: no error", code)
		}
		for _, leak := range []string{"gho_", "csecret", "pkce-v3r1f1er", code} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("%s: the error quotes %q: %v", code, leak, err)
			}
		}
	}
	id, login, err := o.User(ctx, "gho_secret")
	if err != nil || id != 4242 || login != "dev" {
		t.Fatalf("user: %d %q %v", id, login, err)
	}
	if _, _, err := o.User(ctx, "gho_other"); err == nil || strings.Contains(err.Error(), "gho_") {
		t.Fatalf("a refused token: %v", err)
	}
	if err := o.RevokeUserToken(ctx, "Iv1.abc", "csecret", "gho_secret"); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "gho_secret" {
		t.Fatalf("revoked %v", f.revoked)
	}
	if err := o.RevokeUserToken(ctx, "Iv1.abc", "wrong", "gho_secret"); err == nil || strings.Contains(err.Error(), "gho_") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a refused revoke: %v", err)
	}
}

// Plain http, credentials in the URL, or a user reply out of shape are refused.
func TestOAuthRefusesBadEndpointsAndReplies(t *testing.T) {
	ctx := t.Context()
	never := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("a refused endpoint was called") }))
	t.Cleanup(never.Close)
	hc := never.Client()
	for _, o := range []OAuth{{WebURL: "http://github.example.test", APIURL: "https://api.example.test/", HC: hc},
		{WebURL: "https://u:p@github.example.test", APIURL: "https://api.example.test/", HC: hc},
		{WebURL: "https://github.example.test", APIURL: "http://api.example.test/", HC: hc}} {
		if _, err := o.ExchangeCode(ctx, "c", "s", "x", "v", "https://r/cb"); err == nil {
			t.Fatalf("%+v: exchanged", o)
		}
		if _, _, err := o.User(ctx, "t"); err == nil && strings.HasPrefix(o.APIURL, "http:") {
			t.Fatalf("%+v: read the user over http", o)
		}
	}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 0, "login": "dev"}`)
	}))
	t.Cleanup(ts.Close)
	o := OAuth{WebURL: ts.URL, APIURL: ts.URL + "/", HC: ts.Client()}
	if _, _, err := o.User(ctx, "t"); err == nil {
		t.Fatal("a zero id was accepted")
	}
}
