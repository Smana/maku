// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const signingKeyID = "e2e-stub-rsa"

func (s *Stub) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := &s.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []any{map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": signingKeyID,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (s *Stub) activeUser(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": "dev", "state": "USER_STATE_ACTIVE",
	}})
}

func (s *Stub) gitHubLink(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": []any{map[string]any{
		"idpId": "github", "userId": "123", "userName": "dev",
	}}})
}

// discovery is the stub's OpenID Provider Configuration (v4 D8): a generic issuer's endpoints.
func (s *Stub) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"issuer": stubIssuer, "jwks_uri": stubIssuer + "/oauth/v2/keys",
		"token_endpoint":         stubIssuer + "/token",
		"authorization_endpoint": stubIssuer + "/oauth/v2/authorize", "id_token_signing_alg_values_supported": []string{"RS256"}})
}

// testIDToken mints a generic ID token for the scenario: sub, aud (one client) and groups
// (comma-separated, omitted when empty), as any OIDC issuer's ID token without azp. email adds a
// verified email; groupsClaim (a dotted path, e.g. realm_access.roles) nests the groups there. Test-only: the
// stub is a throwaway cluster's issuer, and this route is how the scenario signs a person in.
func (s *Stub) testIDToken(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("sub") == "" || q.Get("aud") == "" {
		http.Error(w, "sub and aud are required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	body := map[string]any{"iss": stubIssuer, "sub": q.Get("sub"), "aud": q.Get("aud"),
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	if e := q.Get("email"); e != "" {
		body["email"], body["email_verified"] = e, true
	}
	if g := q.Get("groups"); g != "" {
		path := strings.Split(q.Get("groupsClaim"), ".")
		if path[0] == "" {
			path = []string{"groups"}
		}
		at := body
		for _, p := range path[:len(path)-1] {
			next := map[string]any{}
			at[p] = next
			at = next
		}
		at[path[len(path)-1]] = strings.Split(g, ",")
	}
	raw, err := signJWT(s.key, body)
	if err != nil {
		http.Error(w, "token signing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id_token": raw})
}
