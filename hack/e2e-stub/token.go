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

func (s *Stub) token(w http.ResponseWriter, _ *http.Request) {
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
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": signingKeyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":       stubIssuer,
		"sub":       "dev",
		"client_id": "roomctl",
		"aud":       []string{"project", "roomctl"},
		"groups":    []string{"agents-admin"},
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
	})
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
