// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"math/big"
	"net/http"
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
	s.oauth.zitadelRead()
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": "dev", "state": "USER_STATE_ACTIVE",
	}})
}

func (s *Stub) gitHubLink(w http.ResponseWriter, r *http.Request) {
	s.oauth.zitadelRead()
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": []any{map[string]any{
		"idpId": "github", "userId": "123", "userName": "dev",
	}}})
}
