// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// discoveryServer serves an OpenID Provider Configuration whose issuer is the server's URL unless
// doc overrides a field, and the signer's keys at /keys.
func discoveryServer(t *testing.T, s signer, doc map[string]any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			d := map[string]any{"issuer": srv.URL, "jwks_uri": srv.URL + "/keys", "token_endpoint": srv.URL + "/token",
				"device_authorization_endpoint": srv.URL + "/device", "authorization_endpoint": srv.URL + "/authorize"}
			for k, v := range doc {
				d[k] = v
			}
			_ = json.NewEncoder(w).Encode(d)
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK("k1", &s.key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscover(t *testing.T) {
	s := newSigner(t)
	t.Run("the endpoints", func(t *testing.T) {
		srv := discoveryServer(t, s, nil)
		d, err := Discover(t.Context(), srv.Client(), srv.URL)
		if err != nil || d.Issuer != srv.URL || d.JWKSURI != srv.URL+"/keys" || d.TokenEndpoint != srv.URL+"/token" ||
			d.DeviceAuthorizationEndpoint != srv.URL+"/device" || d.AuthorizationEndpoint != srv.URL+"/authorize" {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("no device endpoint is no error: the caller decides", func(t *testing.T) {
		srv := discoveryServer(t, s, map[string]any{"device_authorization_endpoint": ""})
		if _, err := Discover(t.Context(), srv.Client(), srv.URL); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a trailing slash on the issuer", func(t *testing.T) {
		srv := discoveryServer(t, s, nil)
		if _, err := Discover(t.Context(), srv.Client(), srv.URL+"/"); err == nil {
			t.Fatal("an issuer that differs by a slash was accepted (OIDC Discovery §4.3)")
		}
	})
	for name, doc := range map[string]map[string]any{
		"another issuer":              {"issuer": "https://elsewhere.example"},
		"a jwks_uri over http":        {"jwks_uri": "http://idp.example/keys"},
		"no jwks_uri":                 {"jwks_uri": ""},
		"a relative jwks_uri":         {"jwks_uri": "/keys"},
		"a token endpoint over http":  {"token_endpoint": "http://idp.example/token"},
		"a relative token endpoint":   {"token_endpoint": "/token"},
		"a device endpoint over http": {"device_authorization_endpoint": "http://idp.example/device"},
		"an issuer with a slash":      {"issuer": "x/"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := discoveryServer(t, s, doc)
			if d, err := Discover(t.Context(), srv.Client(), srv.URL); err == nil {
				t.Fatalf("accepted %+v", d)
			}
		})
	}
	t.Run("a non-https issuer", func(t *testing.T) {
		if _, err := Discover(t.Context(), &http.Client{}, "http://idp.example"); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("an error names the status only", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "secret-body", http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := Discover(t.Context(), srv.Client(), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "secret-body") {
			t.Fatalf("%v", err)
		}
	})
}

// A verifier on discovery fetches nothing at construction, and the document and keys on its first
// token: an IdP that is down does not stop the process.
func TestDiscoveringVerifier(t *testing.T) {
	s := newSigner(t)
	srv := discoveryServer(t, s, nil)
	v, err := NewDiscoveringVerifier("https://down.example", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("construction must not fetch: %v", err)
	}
	if err := v.Refresh(t.Context()); err == nil {
		t.Fatal("an unreachable issuer refreshed")
	}
	v, err = NewDiscoveringVerifier(srv.URL, WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	raw := sign(t, jwt.SigningMethodRS256, s.key, "k1", jwt.MapClaims{"iss": srv.URL, "sub": "2918", "aud": "web",
		"exp": time.Now().Add(time.Hour).Unix()})
	if _, err := v.parseWith(t.Context(), raw); err != nil {
		t.Fatalf("the first token discovers and fetches: %v", err)
	}
	if v.LastRefresh().IsZero() {
		t.Fatal("no refresh recorded")
	}
	if _, err := NewDiscoveringVerifier(""); err == nil {
		t.Fatal("no issuer was accepted")
	}
}
