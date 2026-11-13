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

// claimsOf decodes a stub token's payload: the stub's own signer, checked by TestOIDCJWKSAndToken.
func claimsOf(t *testing.T, raw string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("a token of %d segments", len(parts))
	}
	var head, body map[string]any
	for i, out := range []*map[string]any{&head, &body} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil || json.Unmarshal(b, out) != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
	}
	return head, body
}

// v4 D8: the stub is a generic OIDC issuer too: a discovery document, an ID token minted on demand
// for any sub, client and groups, and client credentials naming the client in azp.
func TestOIDCGenericIssuer(t *testing.T) {
	_, ts := newStubServer(t)
	status, raw := request(t, ts, http.MethodGet, "/.well-known/openid-configuration", nil)
	disc := decodeResponse[map[string]any](t, raw)
	if status != http.StatusOK || disc["issuer"] != stubIssuer || disc["jwks_uri"] != stubIssuer+"/oauth/v2/keys" ||
		disc["token_endpoint"] != stubIssuer+"/token" {
		t.Fatalf("%d %v", status, disc)
	}
	status, raw = request(t, ts, http.MethodGet, "/test/id-token?sub=alice&aud=rooms-web&groups=agents-member,other", nil)
	if status != http.StatusOK {
		t.Fatalf("id-token status %d", status)
	}
	head, c := claimsOf(t, decodeResponse[map[string]string](t, raw)["id_token"])
	if head["typ"] != "JWT" || c["iss"] != stubIssuer || c["sub"] != "alice" || c["aud"] != "rooms-web" || c["azp"] != nil ||
		!reflect.DeepEqual(c["groups"], []any{"agents-member", "other"}) {
		t.Fatalf("%v %v", head, c)
	}
	_, raw = request(t, ts, http.MethodGet, "/test/id-token?sub=bob&aud=rooms-web", nil)
	if _, c := claimsOf(t, decodeResponse[map[string]string](t, raw)["id_token"]); c["groups"] != nil {
		t.Fatalf("no groups asked, some given: %v", c)
	}
	if status, _ := request(t, ts, http.MethodGet, "/test/id-token?aud=rooms-web", nil); status != http.StatusBadRequest {
		t.Fatalf("no sub: %d", status)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/token",
		strings.NewReader("grant_type=client_credentials&audience=maku-api"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("slack-app", "secret")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	if _, c := claimsOf(t, tok["access_token"].(string)); c["azp"] != "slack-app" || c["sub"] != "slack-app" || c["aud"] != "maku-api" {
		t.Fatalf("client credentials: %v", c)
	}
}
