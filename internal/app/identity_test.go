// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/api/v1alpha1"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/humanapi"
)

// oidcIssuer serves an issuer's discovery document and one RSA key over TLS, as any OIDC IdP does.
func oidcIssuer(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/protocol/certs"})
		case "/protocol/certs":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// v4 D8: the generic preset, its keys read from the issuer's discovery document. A Keycloak-shaped
// ID token, its groups under /realm_access/roles, reaches GET /api/rooms as a member; one with no
// roles claim is a principal with no group, refused as not in an agents group, and counted.
func TestGenericIssuerReachesRooms(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := oidcIssuer(t, key)
	dir := t.TempDir()
	web, cli := filepath.Join(dir, "web-client-id"), filepath.Join(dir, "cli-client-id")
	writeID(t, web, "rooms-web")
	writeID(t, cli, "maku-cli")
	cfg := config.Config{
		PublicURL:  "https://rooms.example.test",
		RunIssuers: []config.IssuerConfig{{Issuer: clusterIssuer, JWKSURL: clusterIssuer + "/keys", SubPattern: `^run-(\w+)$`}},
		Human: config.HumanConfig{Preset: config.PresetGeneric, Issuer: idp.URL, ClientIDFile: web, RoomctlClientIDFile: cli,
			RolesClaim: "/realm_access/roles", Origin: "https://rooms.example.test",
			Groups: config.GroupsConfig{Admin: "agents-admin", Member: "agents-member"}},
	}
	exp, m := newMetrics(t)
	defer func() { _ = exp.Shutdown(context.WithoutCancel(t.Context())) }()
	discovering := jwksVerifier(slog.New(slog.DiscardHandler), authn.WithHTTPClient(idp.Client()))
	build := func(ctx context.Context, issuer, jwksURL string, lazy bool) (*authn.Verifier, error) {
		if issuer == clusterIssuer {
			return authn.NewVerifierWithKeyfunc(issuer, nil), nil
		}
		if jwksURL != "" {
			t.Fatalf("the human verifier was given a JWKS URL %q: discovery names it", jwksURL)
		}
		return discovering(ctx, issuer, jwksURL, lazy)
	}
	a, err := authenticators(t.Context(), cfg, m, build, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	srv, err := humanServer(cfg, a.humans, fake.NewClientBuilder().WithScheme(scheme).Build(), "agent-system", fakeRoomLog{},
		fanout.New(fakeRoomLog{}, nil, log), fakeRuns{}, &humanapi.Actor{}, nil, m, log)
	if err != nil {
		t.Fatal(err)
	}
	routes := srv.Routes()
	token := func(claims jwt.MapClaims) string {
		c := jwt.MapClaims{"iss": idp.URL, "sub": "f:3c1e:alice", "aud": "rooms-web", "azp": "rooms-web",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix()}
		for k, v := range claims {
			c[k] = v
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "k1"
		raw, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	get := func(raw string) int {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/rooms", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set(authn.ForwardedAccessHeader, "an-opaque-access-token")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w.Code
	}
	member := token(jwt.MapClaims{"realm_access": map[string]any{"roles": []string{"agents-member", "offline_access"}}})
	if code := get(member); code != http.StatusOK {
		t.Fatalf("a member's ID token: %d, want 200", code)
	}
	if code := get(token(nil)); code != http.StatusForbidden {
		t.Fatalf("no roles claim: %d, want 403", code)
	}
	if code := get(token(jwt.MapClaims{"aud": "grafana", "azp": "grafana",
		"realm_access": map[string]any{"roles": []string{"agents-member"}}})); code != http.StatusUnauthorized {
		t.Fatalf("another client's ID token: %d, want 401", code)
	}
	rec := httptest.NewRecorder()
	exp.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `maku_rooms_human_roles_claim_refused_total{reason="missing"} 1`) {
		t.Fatalf("the refused roles claim is not counted:\n%s", body)
	}
}

// Review of #524: the broker's sponsored principals take human.mcp.sponsoredClientClaim, as the
// factory's take api.sponsoredClientClaim, so both admit the same Okta-shaped Slack App token.
func TestBrokerSponsoredClientClaim(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	slack := filepath.Join(dir, "slack-app")
	writeID(t, slack, "0oa-slack")
	const iss = "https://okta.example/oauth2/default"
	cfg := config.Config{
		RunIssuers: []config.IssuerConfig{{Issuer: clusterIssuer, JWKSURL: clusterIssuer + "/keys", SubPattern: `^run-(\w+)$`}},
		Human: config.HumanConfig{Preset: config.PresetGeneric, Issuer: iss, JWKSURL: iss + "/v1/keys", RolesClaim: "groups",
			Audience: "api://rooms", Origin: "https://rooms.example.test",
			MCP: config.MCPConfig{Enabled: true, SponsoredClientClaim: "cid",
				Sponsored: []config.SponsoredConfig{{Name: "slack-app", ClientIDFile: slack}}}},
	}
	build := func(_ context.Context, issuer, _ string, _ bool) (*authn.Verifier, error) {
		return authn.NewVerifierWithKeyfunc(issuer, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }), nil
	}
	_, m := newMetrics(t)
	a, err := authenticators(t.Context(), cfg, m, build, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := func(claims jwt.MapClaims) string {
		c := jwt.MapClaims{"iss": iss, "sub": "0oa-slack", "aud": "api://rooms", "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range claims {
			c[k] = v
		}
		raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if p, err := a.sponsored.Authenticate(t.Context(), tok(jwt.MapClaims{"cid": "0oa-slack"})); err != nil || p.ID != "system:slack-app" {
		t.Fatalf("Okta's cid: %+v %v", p, err)
	}
	if _, err := a.sponsored.Authenticate(t.Context(), tok(jwt.MapClaims{"cid": "0oa-slack", "azp": "0oa-other"})); err == nil {
		t.Fatal("cid outvoted azp naming another client")
	}
}
