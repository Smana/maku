// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The OAuth web flow as the broker runs it: authorize answers a 302 back with a code and the state;
// the exchange needs the verifier of that code's challenge, once; GET /user is dev (123) for a live
// token; a revocation kills the token and is counted.
func TestOAuthWebFlow(t *testing.T) {
	_, ts := newStubServer(t)
	c := ts.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	verifier := "a-verifier-of-at-least-forty-three-characters-x"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {"Iv1.e2e"}, "redirect_uri": {"https://rooms.e2e.test/link/github/callback"}, "state": {"st8"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	get := func(path string) *http.Response {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("/login/oauth/authorize?" + q.Encode())
	_ = resp.Body.Close()
	loc, perr := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || perr != nil || loc.Host != "rooms.e2e.test" || loc.Query().Get("state") != "st8" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code := loc.Query().Get("code")
	exchange := func(v string) map[string]string {
		form := url.Values{"client_id": {"Iv1.e2e"}, "client_secret": {"s"}, "code": {code}, "code_verifier": {v}}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	out := exchange(verifier)
	tok := out["access_token"]
	if !strings.HasPrefix(tok, "e2e-user-") {
		t.Fatalf("exchange: %v", out)
	}
	if again := exchange(verifier); again["error"] != "bad_verification_code" {
		t.Fatalf("a second exchange of one code: %v", again)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"login":"dev"`) || !strings.Contains(string(raw), `"id":123`) {
		t.Fatalf("user: %d %s", resp.StatusCode, raw)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodDelete, ts.URL+"/applications/Iv1.e2e/token", strings.NewReader(`{"access_token":"`+tok+`"}`))
	req.SetBasicAuth("Iv1.e2e", "s")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	status, raw := request(t, ts, http.MethodGet, "/__oauth", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"revoked":1`) || !strings.Contains(string(raw), `"liveTokens":0`) {
		t.Fatalf("counts: %s", raw)
	}
	// A wrong verifier gets nothing.
	resp = get("/login/oauth/authorize?" + q.Encode())
	_ = resp.Body.Close()
	loc, _ = url.Parse(resp.Header.Get("Location"))
	code = loc.Query().Get("code")
	if bad := exchange("not-the-verifier-not-the-verifier-not-the-v"); bad["error"] != "bad_verification_code" {
		t.Fatalf("a wrong verifier: %v", bad)
	}
}
