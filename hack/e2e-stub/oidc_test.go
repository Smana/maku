// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOIDCJWKSAndToken(t *testing.T) {
	_, ts := newStubServer(t)
	status, raw := request(t, ts, http.MethodGet, "/oauth/v2/keys", nil)
	if status != http.StatusOK {
		t.Fatalf("JWKS status = %d", status)
	}
	jwks := decodeResponse[struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			D   string `json:"d"`
			P   string `json:"p"`
			Q   string `json:"q"`
		} `json:"keys"`
	}](t, raw)
	if len(jwks.Keys) != 1 {
		t.Fatalf("JWKS keys = %d, want 1", len(jwks.Keys))
	}
	key := jwks.Keys[0]
	if key.Kty != "RSA" || key.Use != "sig" || key.Alg != "RS256" || key.Kid == "" || key.N == "" || key.E == "" {
		t.Fatalf("unexpected JWKS key: %+v", key)
	}
	if key.D != "" || key.P != "" || key.Q != "" {
		t.Fatal("JWKS contains RSA private parameters")
	}
	n, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		t.Fatal(err)
	}
	e, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		t.Fatal(err)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	if pub.N.BitLen() < 2048 || pub.E < 3 {
		t.Fatalf("weak JWKS key: %d bits, exponent %d", pub.N.BitLen(), pub.E)
	}

	status, raw = request(t, ts, http.MethodPost, "/token", nil)
	if status != http.StatusOK {
		t.Fatalf("token status = %d", status)
	}
	reply := decodeResponse[struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}](t, raw)
	if reply.TokenType != "Bearer" || reply.ExpiresIn != 3600 {
		t.Fatalf("token reply = %+v", reply)
	}
	parts := strings.Split(reply.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("access token has %d segments", len(parts))
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	header := decodeResponse[struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}](t, headerRaw)
	if header.Alg != "RS256" || header.Kid != key.Kid || header.Typ != "JWT" {
		t.Fatalf("token header = %+v", header)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeResponse[struct {
		Issuer    string   `json:"iss"`
		Subject   string   `json:"sub"`
		ClientID  string   `json:"client_id"`
		Audience  []string `json:"aud"`
		Groups    []string `json:"groups"`
		ExpiresAt int64    `json:"exp"`
		IssuedAt  int64    `json:"iat"`
	}](t, claimsRaw)
	if claims.Issuer != "https://e2e-stub.e2e.svc.cluster.local" || claims.Subject != "dev" || claims.ClientID != "roomctl" {
		t.Errorf("token identity claims = %+v", claims)
	}
	if !reflect.DeepEqual(claims.Audience, []string{"project", "roomctl"}) {
		t.Errorf("token audience = %v", claims.Audience)
	}
	if !reflect.DeepEqual(claims.Groups, []string{"agents-admin"}) {
		t.Errorf("token groups = %v", claims.Groups)
	}
	now := time.Now().Unix()
	if claims.IssuedAt < now-5 || claims.IssuedAt > now+5 || claims.ExpiresAt-claims.IssuedAt != 3600 {
		t.Errorf("token times: iat=%d exp=%d now=%d", claims.IssuedAt, claims.ExpiresAt, now)
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, signed[:], signature); err != nil {
		t.Fatalf("RS256 signature does not verify against JWKS: %v", err)
	}

	var arbitrary map[string]any
	if err := json.Unmarshal(claimsRaw, &arbitrary); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "private_key"} {
		if _, found := arbitrary[private]; found {
			t.Fatalf("token claims contain private field %q", private)
		}
	}
}

func TestOIDCUserAndGitHubLink(t *testing.T) {
	_, ts := newStubServer(t)
	status, raw := request(t, ts, http.MethodGet, "/v2/users/dev", nil)
	if status != http.StatusOK {
		t.Fatalf("user status = %d", status)
	}
	user := decodeResponse[struct {
		User struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"user"`
	}](t, raw)
	if user.User.ID != "dev" || user.User.State != "USER_STATE_ACTIVE" {
		t.Fatalf("user = %+v", user)
	}

	status, raw = request(t, ts, http.MethodPost, "/v2/users/dev/links/_search", []byte("{}"))
	if status != http.StatusOK {
		t.Fatalf("links status = %d", status)
	}
	links := decodeResponse[struct {
		Result []struct {
			IDPID  string `json:"idpId"`
			UserID string `json:"userId"`
		} `json:"result"`
	}](t, raw)
	if len(links.Result) != 1 || links.Result[0].IDPID != "github" || links.Result[0].UserID != "123" {
		t.Fatalf("links = %+v", links)
	}
}
