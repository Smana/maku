// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/policy"
)

type signer struct{ key *rsa.PrivateKey }

func newSigner(t *testing.T) signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signer{k}
}

func (s signer) verifier(iss string) *authn.Verifier {
	return authn.NewVerifierWithKeyfunc(iss, func(*jwt.Token) (any, error) { return &s.key.PublicKey, nil })
}

func (s signer) token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAuthenticate(t *testing.T) {
	zitadel, eks := newSigner(t), newSigner(t)
	a := &Authenticator{Humans: zitadel.verifier("https://auth.ogenki.io"),
		Groups:      policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs:   func() []string { return []string{"rooms-proxy-id", "maku-id"} },
		Systems:     eks.verifier("https://oidc.eks"),
		SystemAllow: map[string]string{"system:serviceaccount:agent-system:runner": "system:runner"}}
	call := func(tok string) (authn.Principal, error) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Authenticate(r)
	}
	human := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "291847362183", "azp": "maku-id", "groups": []string{"agents-member"}})
	p, err := call(human)
	if err != nil || p.ID != "human:291847362183" || p.ClientID != "maku-id" || p.AccessToken != human {
		t.Fatalf("%+v %v", p, err)
	}
	stranger := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "1", "azp": "maku-id", "groups": []string{"backend"}})
	if _, err := call(stranger); !errors.Is(err, authn.ErrForbidden) {
		t.Fatalf("outside the agents groups: %v", err)
	}
	// What the broker forwards and maku sends: a ZITADEL JWT access token,
	// which names its client in client_id and carries no azp (review C1).
	access := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "291847362183", "client_id": "rooms-proxy-id",
		"groups": []string{"agents-member"}})
	if p, err := call(access); err != nil || p.ID != "human:291847362183" || p.ClientID != "rooms-proxy-id" || p.AccessToken != access {
		t.Fatalf("an access token: %+v %v", p, err)
	}
	otherAccess := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "1", "client_id": "grafana", "groups": []string{"agents-admin"}})
	if _, err := call(otherAccess); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("an access token issued to another client: %v", err)
	}
	otherClient := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "1", "azp": "grafana", "groups": []string{"agents-admin"}})
	if _, err := call(otherClient); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("a token issued to another client: %v", err)
	}
	sys := eks.token(t, jwt.MapClaims{"iss": "https://oidc.eks", "sub": "system:serviceaccount:agent-system:runner", "aud": AudienceSystem})
	if p, err := call(sys); err != nil || p.ID != "system:runner" {
		t.Fatalf("%+v %v", p, err)
	}
	rogue := eks.token(t, jwt.MapClaims{"iss": "https://oidc.eks", "sub": "system:serviceaccount:default:x", "aud": AudienceSystem})
	if _, err := call(rogue); !errors.Is(err, authn.ErrForbidden) {
		t.Fatalf("an unlisted ServiceAccount: %v", err)
	}
	if _, err := call("garbage"); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatal(err)
	}
}

// F4, R2: the third branch runs after the human clients and before the cluster's issuer; a human
// token still reads as the human.
func TestAuthenticatorSponsoredBranch(t *testing.T) {
	zitadel := newSigner(t)
	v := zitadel.verifier("https://auth.ogenki.io")
	a := &Authenticator{Humans: v, Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-proxy-id", "maku-id"} },
		Sponsored: &authn.Sponsored{V: v, Clients: []authn.SponsoredClient{{Name: "slack-app", ClientID: func() string { return "slack-id" }}},
			Audience: func() string { return "proj" },
			Exclude:  func() []string { return []string{"rooms-proxy-id", "maku-id"} }}}
	call := func(tok string) (authn.Principal, error) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/tasks", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Authenticate(r)
	}
	sp := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "345", "client_id": "slack-id", "aud": []string{"proj"}})
	if p, err := call(sp); err != nil || p.ID != "system:slack-app" || !p.Sponsored || p.AccessToken != sp {
		t.Fatalf("a sponsored token: %+v %v", p, err)
	}
	human := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "291", "client_id": "maku-id",
		"groups": []string{"agents-member"}})
	if p, err := call(human); err != nil || p.ID != "human:291" || p.Sponsored {
		t.Fatalf("a human token: %+v %v", p, err)
	}
}

// F4: with the cluster's issuer wired too, each token is admitted by its own branch; and a client
// the config excludes (a human's) is never a sponsored principal.
func TestAuthenticatorSponsoredBesideSystems(t *testing.T) {
	zitadel, eks := newSigner(t), newSigner(t)
	v := zitadel.verifier("https://auth.ogenki.io")
	mk := func(exclude ...string) *Authenticator {
		return &Authenticator{Humans: v, Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
			ClientIDs:   func() []string { return []string{"maku-id"} },
			Systems:     eks.verifier("https://oidc.eks"),
			SystemAllow: map[string]string{"system:serviceaccount:agent-system:runner": "system:runner"},
			Sponsored: &authn.Sponsored{V: v, Clients: []authn.SponsoredClient{{Name: "slack-app", ClientID: func() string { return "slack-id" }}},
				Audience: func() string { return "proj" },
				Exclude:  func() []string { return exclude }}}
	}
	call := func(a *Authenticator, tok string) (authn.Principal, error) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Authenticate(r)
	}
	a := mk("maku-id")
	sp := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "345", "client_id": "slack-id", "aud": []string{"proj"}})
	if p, err := call(a, sp); err != nil || p.ID != "system:slack-app" || !p.Sponsored || p.ClientID != "slack-id" {
		t.Fatalf("a sponsored token beside Systems: %+v %v", p, err)
	}
	sys := eks.token(t, jwt.MapClaims{"iss": "https://oidc.eks", "sub": "system:serviceaccount:agent-system:runner", "aud": AudienceSystem})
	if p, err := call(a, sys); err != nil || p.ID != "system:runner" || p.Sponsored || p.AccessToken != "" {
		t.Fatalf("a cluster token: %+v %v", p, err)
	}
	// slack-id is also a human client's id: the exclusion wins, and nothing else admits it.
	if p, err := call(mk("slack-id"), sp); err == nil || p.Sponsored {
		t.Fatalf("an excluded client's token was admitted: %+v", p)
	}
}

// F4, invariant 1, end to end: a real signed sponsored token through the real Authenticator is
// refused on /v1/runs, and nothing is created or reserved.
func TestRunsRefusesRealSponsoredToken(t *testing.T) {
	zitadel := newSigner(t)
	v := zitadel.verifier("https://auth.ogenki.io")
	st := &store{}
	c := fake.NewClientBuilder().WithScheme(scheme()).Build()
	s := mkServer(t, st, c, false, false, "7f3cq2xz", 5_000_000)
	s.Auth = &Authenticator{Humans: v, Groups: s.Groups, ClientIDs: func() []string { return []string{"maku-id"} },
		Sponsored: &authn.Sponsored{V: v, Clients: []authn.SponsoredClient{{Name: "slack-app", ClientID: func() string { return "slack-id" }}},
			Audience: func() string { return "proj" },
			Exclude:  func() []string { return []string{"maku-id"} }}}
	tok := zitadel.token(t, jwt.MapClaims{"iss": "https://auth.ogenki.io", "sub": "345", "client_id": "slack-id", "aud": []string{"proj"}})
	w := post(s.Handler(), tok, base())
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"sponsored_only_tasks"`) || len(st.created) != 0 {
		t.Fatalf("%d %s %d", w.Code, w.Body, len(st.created))
	}
	var cms corev1.ConfigMapList
	if err := c.List(t.Context(), &cms); err != nil || len(cms.Items) != 0 {
		t.Fatalf("a ledger was written: %d %v", len(cms.Items), err)
	}
}

// v4 D8: token use id over a generic issuer. The broker forwards the human's ID token, whose aud
// holds the client it was issued to; the groups are read from api.rolesClaim.
func TestAuthenticateTokenUseID(t *testing.T) {
	idp := newSigner(t)
	const iss = "https://keycloak.example/realms/agents"
	a := &Authenticator{Humans: idp.verifier(iss), Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-web", "maku-cli"} }, TokenUse: authn.TokenUseID, RolesClaim: "/realm_access/roles"}
	call := func(tok string) (authn.Principal, error) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Authenticate(r)
	}
	roles := func(names ...string) map[string]any { return map[string]any{"roles": names} }
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   error // nil accepts
		client string
	}{
		{"the forwarded web ID token", jwt.MapClaims{"aud": "rooms-web", "azp": "rooms-web", "realm_access": roles("agents-member")}, nil, "rooms-web"},
		{"maku's ID token, no azp", jwt.MapClaims{"aud": "maku-cli", "realm_access": roles("agents-admin")}, nil, "maku-cli"},
		{"no agents group", jwt.MapClaims{"aud": "rooms-web", "realm_access": roles("offline_access")}, authn.ErrForbidden, ""},
		{"no roles claim", jwt.MapClaims{"aud": "rooms-web"}, authn.ErrForbidden, ""},
		{"azp ours, aud another's", jwt.MapClaims{"aud": "grafana", "azp": "rooms-web", "realm_access": roles("agents-member")},
			authn.ErrUnauthenticated, ""},
		{"aud ours and another, no azp", jwt.MapClaims{"aud": []string{"rooms-web", "grafana"}, "realm_access": roles("agents-member")},
			authn.ErrUnauthenticated, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims := jwt.MapClaims{"iss": iss, "sub": "f:3c1e:alice"}
			maps.Copy(claims, c.claims)
			tok := idp.token(t, claims)
			p, err := call(tok)
			if c.want != nil {
				if !errors.Is(err, c.want) {
					t.Fatalf("got %+v, %v; want %v", p, err, c.want)
				}
				return
			}
			if err != nil || p.ID != "human:f:3c1e:alice" || p.ClientID != c.client || p.AccessToken != tok {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}

// Token use access over a generic issuer: the access token's aud holds api.audience.
func TestAuthenticateGenericAccess(t *testing.T) {
	idp := newSigner(t)
	const iss = "https://tenant.auth0.example/"
	aud := "https://api.rooms.example"
	a := &Authenticator{Humans: idp.verifier(iss), Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-web", "maku-cli"} }, TokenUse: authn.TokenUseAccess, Generic: true,
		RolesClaim: "https://rooms.example/roles", Audience: func() string { return aud }}
	call := func(claims jwt.MapClaims) error {
		c := jwt.MapClaims{"iss": iss, "sub": "auth0|1", "azp": "maku-cli", "https://rooms.example/roles": []string{"agents-member"}}
		maps.Copy(c, claims)
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+idp.token(t, c))
		_, err := a.Authenticate(r)
		return err
	}
	if err := call(jwt.MapClaims{"aud": []string{aud, iss + "userinfo"}}); err != nil {
		t.Fatalf("the audience: %v", err)
	}
	if err := call(jwt.MapClaims{"aud": "another-api"}); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("another audience: %v", err)
	}
	aud = "" // an unreadable file refuses
	if err := call(jwt.MapClaims{"aud": ""}); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("no audience: %v", err)
	}
}

// The sponsored branch reads the client from api.sponsoredClientClaim when set (Okta's cid).
func TestAuthenticatorSponsoredClientClaim(t *testing.T) {
	idp := newSigner(t)
	const iss = "https://okta.example/oauth2/default"
	v := idp.verifier(iss)
	a := &Authenticator{Humans: v, Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-web"} }, TokenUse: authn.TokenUseID, RolesClaim: "groups",
		Sponsored: &authn.Sponsored{V: v, Clients: []authn.SponsoredClient{{Name: "slack-app", ClientID: func() string { return "0oa-slack" }}},
			Audience: func() string { return "api://rooms" }, ClientClaim: "cid",
			Exclude: func() []string { return []string{"rooms-web"} }}}
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/tasks", nil)
	tok := idp.token(t, jwt.MapClaims{"iss": iss, "sub": "0oa-slack", "cid": "0oa-slack", "aud": "api://rooms"})
	r.Header.Set("Authorization", "Bearer "+tok)
	if p, err := a.Authenticate(r); err != nil || p.ID != "system:slack-app" || !p.Sponsored {
		t.Fatalf("%+v %v", p, err)
	}
}

// The zitadel preset stays byte for byte (review of #524): a token naming no client in azp or
// client_id is no one's, even when its one aud is a rooms client; and a malformed groups claim
// refuses the token whole, as before.
func TestAuthenticateZITADELIsUnchanged(t *testing.T) {
	zitadel := newSigner(t)
	a := &Authenticator{Humans: zitadel.verifier("https://auth.ogenki.io"),
		Groups:    policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-proxy-id", "maku-id"} }}
	for name, claims := range map[string]jwt.MapClaims{
		"aud the client alone, no azp or client_id": {"aud": "maku-id", "groups": []string{"agents-member"}},
		"a string groups claim":                     {"azp": "maku-id", "groups": "agents-member"},
	} {
		t.Run(name, func(t *testing.T) {
			claims["iss"], claims["sub"] = "https://auth.ogenki.io", "291847362183"
			r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
			r.Header.Set("Authorization", "Bearer "+zitadel.token(t, claims))
			if p, err := a.Authenticate(r); !errors.Is(err, authn.ErrUnauthenticated) {
				t.Fatalf("got %+v, %v; want ErrUnauthenticated", p, err)
			}
		})
	}
}

// Under token use id an RFC 9068 access token is not the forwarded ID token, and a refused roles
// claim is counted by its reason.
func TestAuthenticateTokenUseIDRefusals(t *testing.T) {
	idp := newSigner(t)
	const iss = "https://dex.example/dex"
	var refused []string
	a := &Authenticator{Humans: idp.verifier(iss), Groups: policy.Groups{Admin: "agents-admin", Member: "agents-member"},
		ClientIDs: func() []string { return []string{"rooms-web"} }, TokenUse: authn.TokenUseID, Generic: true, RolesClaim: "groups",
		RolesRefused: func(_ context.Context, reason string) { refused = append(refused, reason) }}
	call := func(typ string, claims jwt.MapClaims) error {
		c := jwt.MapClaims{"iss": iss, "sub": "alice", "aud": "rooms-web", "exp": time.Now().Add(time.Hour).Unix()}
		maps.Copy(c, claims)
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		if typ != "" {
			tok.Header["typ"] = typ
		}
		raw, err := tok.SignedString(idp.key)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		_, err = a.Authenticate(r)
		return err
	}
	if err := call("at+jwt", jwt.MapClaims{"groups": []string{"agents-member"}}); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("an access token passed as the ID token: %v", err)
	}
	if err := call("JWT", jwt.MapClaims{"groups": []string{"agents-member"}}); err != nil {
		t.Fatalf("the ID token: %v", err)
	}
	if err := call("", jwt.MapClaims{"groups": 7}); !errors.Is(err, authn.ErrForbidden) || !slices.Equal(refused, []string{"type"}) {
		t.Fatalf("a malformed roles claim: %v, counted %v", err, refused)
	}
}
