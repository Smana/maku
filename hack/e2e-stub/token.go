// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

const stubIssuer = "https://e2e-stub.e2e.svc.cluster.local"

func (s *Stub) token(w http.ResponseWriter, r *http.Request) {
	// v4 D8: client credentials with an audience are a generic issuer's (Auth0's shape): a JWT
	// naming the client in azp and holding the audience. Every other request keeps the ZITADEL
	// shaped access token the existing scenarios read. A code from the authorization endpoint
	// redeems for an ID token (plan v4 step 6, Task 9).
	_ = r.ParseForm()
	if r.PostForm.Get("grant_type") == "authorization_code" {
		s.redeemCode(w, r.PostForm.Get("code"))
		return
	}
	if r.PostForm.Get("grant_type") == "client_credentials" && r.PostForm.Get("audience") != "" {
		client, _, ok := r.BasicAuth()
		if !ok {
			client = r.PostForm.Get("client_id")
		}
		s.slack.mu.Lock()
		s.slack.grants = append(s.slack.grants, client)
		s.slack.mu.Unlock()
		now := time.Now()
		raw, err := signJWT(s.key, map[string]any{"iss": stubIssuer, "sub": client, "azp": client,
			"aud": r.PostForm.Get("audience"), "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix()})
		if err != nil {
			http.Error(w, "token signing failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": raw, "token_type": "Bearer", "expires_in": 3600})
		return
	}
	raw, err := signAccessToken(s.key, time.Now())
	if err != nil {
		http.Error(w, "token signing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": raw,
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

func signAccessToken(key *rsa.PrivateKey, now time.Time) (string, error) {
	return signJWT(key, map[string]any{
		"iss":       stubIssuer,
		"sub":       "dev",
		"client_id": "roomctl",
		"aud":       []string{"project", "roomctl"},
		"groups":    []string{"agents-admin"},
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
	})
}

// signJWT signs claims with the stub's key, as RS256 under its one kid.
func signJWT(key *rsa.PrivateKey, body map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": signingKeyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
