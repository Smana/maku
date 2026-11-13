// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// codeStore holds the authorization codes the stub issued and not yet redeemed (plan v4 step 6,
// Task 9): a web front door signs a person in through the code flow, and each code redeems once.
type codeStore struct {
	mu    sync.Mutex
	codes map[string]codeGrant
}

type codeGrant struct{ client, nonce string }

// authorize is the authorization endpoint of a test-only issuer: it signs in the stub's one person
// (sub dev, agents-admin) at once and sends the browser back to redirect_uri with a code.
func (s *Stub) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("response_type") != "code" || q.Get("client_id") == "" || err != nil || back.Scheme == "" || back.Host == "" {
		http.Error(w, "response_type code, client_id and an absolute redirect_uri are required", http.StatusBadRequest)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	code := hex.EncodeToString(b)
	s.codes.mu.Lock()
	if s.codes.codes == nil {
		s.codes.codes = map[string]codeGrant{}
	}
	s.codes.codes[code] = codeGrant{client: q.Get("client_id"), nonce: q.Get("nonce")}
	s.codes.mu.Unlock()
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// redeemCode answers the token endpoint's authorization_code grant: the ID token for the code's
// client and nonce, a distinct access token, and a refresh token. A code redeems once.
func (s *Stub) redeemCode(w http.ResponseWriter, code string) {
	s.codes.mu.Lock()
	g, ok := s.codes.codes[code]
	delete(s.codes.codes, code)
	s.codes.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": stubIssuer, "sub": "dev", "aud": g.client, "email": "dev@e2e.test", "email_verified": true,
		"groups": []string{"agents-admin"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	id, err := signJWT(s.key, claims)
	if err != nil {
		http.Error(w, "token signing failed", http.StatusInternalServerError)
		return
	}
	access, err := signAccessToken(s.key, now)
	if err != nil {
		http.Error(w, "token signing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id_token": id, "access_token": access, "refresh_token": "refresh-" + code,
		"token_type": "Bearer", "expires_in": 600})
}
