// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// The GitHub OAuth web flow the broker's GitHub account link runs (v4 D8): authorize answers a 302
// back to the redirect with a one-time code bound to the PKCE challenge, the exchange checks the
// verifier, GET /user is the stub's one user (dev, id 123), and a revocation is counted. Every token
// is e2e-user-<code>.
type oauthFlow struct {
	mu      sync.Mutex
	codes   map[string]string // code -> S256 challenge, until exchanged
	tokens  map[string]bool   // live user tokens
	revoked int
	ZITADEL int // reads of the ZITADEL user and link endpoints, which a linked human must never cause
}

func newOAuthFlow() *oauthFlow {
	return &oauthFlow{codes: map[string]string{}, tokens: map[string]bool{}}
}

func (o *oauthFlow) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login/oauth/authorize", o.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", o.exchange)
	mux.HandleFunc("GET /user", o.user)
	mux.HandleFunc("DELETE /applications/{client}/token", o.revoke)
	mux.HandleFunc("GET /__oauth", func(w http.ResponseWriter, _ *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"revoked": o.revoked, "liveTokens": len(o.tokens), "zitadel": o.ZITADEL})
	})
}

func (o *oauthFlow) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Host == "" || q.Get("state") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b)
	o.mu.Lock()
	o.codes[code] = q.Get("code_challenge")
	o.mu.Unlock()
	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound) //nolint:gosec // G710: the e2e stub redirects to the redirect_uri it was sent, as GitHub does to a registered callback
}

func (o *oauthFlow) exchange(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code, verifier := r.PostForm.Get("code"), r.PostForm.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	o.mu.Lock()
	defer o.mu.Unlock()
	challenge, ok := o.codes[code]
	delete(o.codes, code) // one exchange per code, right or wrong
	if !ok || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) || r.PostForm.Get("client_secret") == "" {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	tok := "e2e-user-" + code
	o.tokens[tok] = true
	writeJSON(w, http.StatusOK, map[string]string{"access_token": tok, "token_type": "bearer"})
}

func (o *oauthFlow) user(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	o.mu.Lock()
	live := o.tokens[tok]
	o.mu.Unlock()
	if !live {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": 123, "login": "dev", "type": "User"})
}

func (o *oauthFlow) revoke(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccessToken string `json:"access_token"`
	}
	if _, _, ok := r.BasicAuth(); !ok || !decodeBody(w, r, &in) {
		if !ok {
			http.Error(w, "no client credentials", http.StatusUnauthorized)
		}
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tokens[in.AccessToken] {
		http.Error(w, "unknown token", http.StatusNotFound)
		return
	}
	delete(o.tokens, in.AccessToken)
	o.revoked++
	w.WriteHeader(http.StatusNoContent)
}

// zitadelRead counts one read of the ZITADEL user or link endpoints.
func (o *oauthFlow) zitadelRead() {
	o.mu.Lock()
	o.ZITADEL++
	o.mu.Unlock()
}
