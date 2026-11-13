// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeJWT is an unsigned JWT-shaped token expiring at exp: maku never verifies a token, it only
// reads an ID token's exp to know when to refresh it.
func fakeJWT(exp time.Time, tag string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{"exp": exp.Unix(), "tag": tag}) + ".sig"
}

// genericIssuer is any OIDC issuer: its endpoints are only where its discovery document says, so a
// call to ZITADEL's fixed paths is a 404. refreshID says whether a refresh returns an ID token.
func genericIssuer(t *testing.T, refreshID bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/keys",
			"device_authorization_endpoint": srv.URL + "/device/code", "token_endpoint": srv.URL + "/token"})
	})
	mux.HandleFunc("POST /device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls = append(calls, "device "+r.Form.Encode())
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "WXYZ",
			"verification_uri": srv.URL + "/device", "expires_in": 300, "interval": 1})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls = append(calls, "token "+r.Form.Encode())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"access_token": "opaque-at", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "rt"}
		if r.Form.Get("grant_type") != "refresh_token" || refreshID {
			resp["id_token"] = fakeJWT(time.Now().Add(time.Hour), r.Form.Get("grant_type"))
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

// v4 D8: maku signs in through the issuer's discovery document, never ZITADEL's fixed paths, with
// the configured scopes, and under token use id its credential is the ID token.
func TestLoginGenericIssuer(t *testing.T) {
	srv, calls := genericIssuer(t, true)
	c := Config{URL: "https://rooms.example", Issuer: srv.URL, ClientID: "maku-cli", Preset: PresetGeneric, TokenUse: TokenUseID,
		Scopes: []string{"groups"}}
	tok, err := Login(t.Context(), srv.Client(), c, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix((*calls)[0], "device ") || !strings.Contains((*calls)[0], "groups") ||
		strings.Contains((*calls)[0], "zitadel") || !strings.Contains((*calls)[0], "offline_access") {
		t.Fatalf("the device authorization: %v", *calls)
	}
	path := filepath.Join(t.TempDir(), "token.json")
	if err := SaveToken(path, tok); err != nil {
		t.Fatal(err)
	}
	got, err := Token(t.Context(), srv.Client(), c, path)
	if err != nil || !strings.Contains(got, ".") || got == "opaque-at" {
		t.Fatalf("token use id sends the ID token: %q %v", got, err)
	}
}

// The endpoints configure cached are used as they are, with no discovery call.
func TestLoginUsesTheCachedEndpoints(t *testing.T) {
	srv, calls := genericIssuer(t, true)
	c := Config{Issuer: srv.URL, ClientID: "maku-cli", Preset: PresetGeneric, TokenUse: TokenUseAccess, Audience: "https://api.rooms",
		DeviceAuthURL: srv.URL + "/device/code", TokenURL: srv.URL + "/token"}
	if _, err := Login(t.Context(), srv.Client(), c, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*calls)[0], "audience=https%3A%2F%2Fapi.rooms") {
		t.Fatalf("token use access sends the audience (Auth0): %v", *calls)
	}
}

// A refresh that returns no ID token cannot keep a token-use-id session: OIDC Core makes it optional.
func TestTokenRefreshWithoutIDToken(t *testing.T) {
	srv, _ := genericIssuer(t, false)
	c := Config{Issuer: srv.URL, ClientID: "maku-cli", Preset: PresetGeneric, TokenUse: TokenUseID}
	path := filepath.Join(t.TempDir(), "token.json")
	stale := StoredToken{Token: oauth2.Token{AccessToken: "opaque-at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)},
		IDToken: fakeJWT(time.Now().Add(-time.Minute), "old")}
	if err := SaveJSON(path, stale); err != nil {
		t.Fatal(err)
	}
	_, err := Token(t.Context(), srv.Client(), c, path)
	if err == nil || !strings.Contains(err.Error(), "the IdP returned no ID token on refresh: run maku login") {
		t.Fatalf("got %v", err)
	}
}

// An ID token past its own exp is refreshed even while the access token is still valid.
func TestTokenRefreshesAnExpiredIDToken(t *testing.T) {
	srv, calls := genericIssuer(t, true)
	c := Config{Issuer: srv.URL, ClientID: "maku-cli", Preset: PresetGeneric, TokenUse: TokenUseID}
	path := filepath.Join(t.TempDir(), "token.json")
	if err := SaveJSON(path, StoredToken{Token: oauth2.Token{AccessToken: "opaque-at", RefreshToken: "rt",
		Expiry: time.Now().Add(time.Hour)}, IDToken: fakeJWT(time.Now().Add(-time.Minute), "old")}); err != nil {
		t.Fatal(err)
	}
	got, err := Token(t.Context(), srv.Client(), c, path)
	if err != nil || !strings.Contains(strings.Join(*calls, " "), "grant_type=refresh_token") {
		t.Fatalf("no refresh: %q %v %v", got, err, *calls)
	}
	var saved StoredToken
	if err := LoadJSON(path, &saved); err != nil || saved.IDToken != got {
		t.Fatalf("the refreshed ID token is not saved: %v", err)
	}
	n := len(*calls)
	if again, err := Token(t.Context(), srv.Client(), c, path); err != nil || again != got || len(*calls) != n {
		t.Fatalf("a valid ID token is used as is: %v, %d calls", err, len(*calls)-n)
	}
}

// Under the zitadel preset nothing changes: the fixed paths, the project's audience scope, the
// access token.
func TestZITADELConfigUnchanged(t *testing.T) {
	c := Config{Issuer: "https://auth.example", ClientID: "maku-client", ProjectID: "29184"}
	conf, err := oauthConfig(t.Context(), &http.Client{}, c)
	if err != nil || conf.Endpoint.DeviceAuthURL != "https://auth.example/oauth/v2/device_authorization" ||
		conf.Endpoint.TokenURL != "https://auth.example/oauth/v2/token" ||
		strings.Join(conf.Scopes, " ") != "openid profile email offline_access urn:zitadel:iam:org:project:id:29184:aud" {
		t.Fatalf("%+v %v", conf, err)
	}
	if err := (Config{URL: "https://rooms.example", Issuer: "https://idp.example", ClientID: "c", Preset: PresetGeneric}).Validate(); err != nil {
		t.Fatalf("generic needs no project id: %v", err)
	}
	if err := (Config{URL: "https://rooms.example", Issuer: "https://idp.example", ClientID: "c"}).Validate(); err == nil {
		t.Fatal("zitadel without a project id was accepted")
	}
	if err := (Config{URL: "https://rooms.example", Issuer: "https://idp.example", ClientID: "c", Preset: "okta"}).Validate(); err == nil {
		t.Fatal("an unknown preset was accepted")
	}
}

// maku doctor names an issuer whose discovery document cannot be read.
func TestDoctorNamesAFailingIssuer(t *testing.T) {
	r := newDoctorRig(t)
	r.d.Config.Preset, r.d.Config.ProjectID = PresetGeneric, ""
	var out strings.Builder
	_ = r.d.Run(t.Context(), &out)
	if !strings.Contains(out.String(), "FAIL issuer") || !strings.Contains(out.String(), "https://auth.example") {
		t.Fatalf("no issuer check:\n%s", &out)
	}
}

// Discover takes the generic fields from /.well-known/roomctl, and needs no project id there.
func TestDiscoverGeneric(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://rooms.example", "issuer": "https://idp.example",
			"clientID": "maku-cli", "projectID": "", "preset": "generic", "tokenUse": "id", "scopes": []string{"groups"}})
	}))
	defer srv.Close()
	got, err := Discover(t.Context(), srv.Client(), srv.URL)
	if err != nil || got.Preset != PresetGeneric || got.TokenUse != TokenUseID || len(got.Scopes) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
