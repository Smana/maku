// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// v4 step 6 (Task 9): the stub is an OIDC issuer a web front door (oauth2-proxy) can sign a person in
// at: the authorization endpoint answers at once with a code, and the token endpoint redeems it, once,
// for an ID token naming the client and the request's nonce.
func TestAuthorizationCodeFlow(t *testing.T) {
	_, ts := newStubServer(t)
	_, raw := request(t, ts, http.MethodGet, "/.well-known/openid-configuration", nil)
	if disc := decodeResponse[map[string]any](t, raw); disc["authorization_endpoint"] != stubIssuer+"/oauth/v2/authorize" {
		t.Fatalf("discovery has no authorization endpoint: %v", disc)
	}
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(q string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/oauth/v2/authorize?"+q, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	resp := get("response_type=code&client_id=rooms-web&redirect_uri=https%3A%2F%2Frooms.e2e.test%2Foauth2%2Fcallback&state=s1&nonce=n1")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Host != "rooms.e2e.test" || loc.Path != "/oauth2/callback" || loc.Query().Get("state") != "s1" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect %q", resp.Header.Get("Location"))
	}
	for _, q := range []string{
		"response_type=code&redirect_uri=https%3A%2F%2Fx.test%2Fcb&state=s",              // no client
		"response_type=code&client_id=c&state=s",                                         // no redirect
		"response_type=token&client_id=c&redirect_uri=https%3A%2F%2Fx.test%2Fcb&state=s", // not the code flow
	} {
		if resp := get(q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, resp.StatusCode)
		}
	}

	redeem := func(code string) (int, map[string]any) {
		t.Helper()
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"https://rooms.e2e.test/oauth2/callback"}}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("rooms-web", "secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	status, tok := redeem(loc.Query().Get("code"))
	if status != http.StatusOK {
		t.Fatalf("redeem status %d %v", status, tok)
	}
	_, c := claimsOf(t, tok["id_token"].(string))
	if c["iss"] != stubIssuer || c["aud"] != "rooms-web" || c["nonce"] != "n1" || c["sub"] != "dev" || c["email"] != "dev@e2e.test" ||
		c["email_verified"] != true || !reflect.DeepEqual(c["groups"], []any{"agents-admin"}) {
		t.Fatalf("ID token %v", c)
	}
	if at, _ := tok["access_token"].(string); at == "" || at == tok["id_token"] {
		t.Fatalf("access token %q must be present and differ from the ID token", at)
	}
	if status, _ := redeem(loc.Query().Get("code")); status != http.StatusBadRequest {
		t.Fatalf("a code redeemed twice: %d, want 400", status)
	}
	if status, _ := redeem("unknown"); status != http.StatusBadRequest {
		t.Fatalf("an unknown code: %d, want 400", status)
	}
}

// The minted ID token can carry an email, and its groups under a nested claim (Task 9's X5).
func TestTestIDTokenEmailAndNestedGroups(t *testing.T) {
	_, ts := newStubServer(t)
	_, raw := request(t, ts, http.MethodGet, "/test/id-token?sub=alice&aud=roomctl&groups=agents-member&email=alice@e2e.test&groupsClaim=realm_access.roles", nil)
	_, c := claimsOf(t, decodeResponse[map[string]string](t, raw)["id_token"])
	if c["email"] != "alice@e2e.test" || c["email_verified"] != true || c["groups"] != nil ||
		!reflect.DeepEqual(c["realm_access"], map[string]any{"roles": []any{"agents-member"}}) {
		t.Fatalf("%v", c)
	}
}
