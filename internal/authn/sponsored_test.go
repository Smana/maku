// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"maps"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// R2: a sponsored token is recognised by the client it was issued to, read at use; nothing else
// about it is trusted, and a human client's id can never name a principal.
func TestSponsored(t *testing.T) {
	sg := newSigner(t)
	id := "345@platform"
	s := &Sponsored{V: sg.verifier(), Clients: []SponsoredClient{{Name: "slack-app", ClientID: func() string { return id }}},
		Exclude: func() []string { return []string{"web-1", "maku-1"} }, Audience: func() string { return "proj" }}
	tok := func(client, sub string) string {
		return sg.accessToken(t, sub, []string{"proj"}, client, nil, time.Hour)
	}
	p, err := s.Authenticate(t.Context(), tok(id, "345"))
	if err != nil || p.ID != "system:slack-app" || !p.Sponsored || p.Sub != "345" || p.ClientID != id || p.AccessToken == "" {
		t.Fatalf("%+v %v", p, err)
	}
	other := sign(t, jwt.SigningMethodRS256, sg.key, "", Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://evil.example",
		Subject: "345", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, ClientID: id})
	for name, raw := range map[string]string{
		"another client": tok("maku-1", "345"),
		"no subject":     tok(id, ""),
		"another issuer": other,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Authenticate(t.Context(), raw); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// signed by a key the verifier does not trust, with every claim right
	rogue := newSigner(t).accessToken(t, "345", []string{"proj"}, id, nil, time.Hour)
	if _, err := s.Authenticate(t.Context(), rogue); err == nil {
		t.Fatal("a token from an untrusted key was admitted")
	}
	id = "maku-1" // a rotation that lands on a human client's id must refuse, never admit maku's tokens
	if _, err := s.Authenticate(t.Context(), tok("maku-1", "345")); err == nil {
		t.Fatal("a principal sharing a human client id was admitted")
	}
	id = "" // an unreadable file refuses
	if _, err := s.Authenticate(t.Context(), tok("", "345")); err == nil {
		t.Fatal("an empty client id was admitted")
	}
}

// Two principals resolving to one client id is ambiguous: the first would win by order, so both
// refuse, and an unambiguous principal beside them still works. A missing verifier refuses too.
func TestSponsoredDuplicateClientID(t *testing.T) {
	sg := newSigner(t)
	same := func() string { return "shared@platform" }
	s := &Sponsored{V: sg.verifier(), Clients: []SponsoredClient{
		{Name: "a", ClientID: same}, {Name: "b", ClientID: same}, {Name: "c", ClientID: func() string { return "own@platform" }}},
		Audience: func() string { return "proj" }}
	tok := func(client string) string { return sg.accessToken(t, "345", []string{"proj"}, client, nil, time.Hour) }
	if p, err := s.Authenticate(t.Context(), tok("shared@platform")); err == nil {
		t.Fatalf("an ambiguous client id was admitted as %s", p.ID)
	}
	if p, err := s.Authenticate(t.Context(), tok("own@platform")); err != nil || p.ID != "system:c" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := (&Sponsored{Clients: s.Clients}).Authenticate(t.Context(), tok("own@platform")); err == nil {
		t.Fatal("no verifier admitted")
	}
}

// The project audience is pinned as the human path pins it: a token minted for another project, or
// with no project configured, is refused.
func TestSponsoredAudience(t *testing.T) {
	sg := newSigner(t)
	id, project := "345@platform", "proj"
	s := &Sponsored{V: sg.verifier(), Clients: []SponsoredClient{{Name: "slack-app", ClientID: func() string { return id }}},
		Audience: func() string { return project }}
	tok := func(aud string) string { return sg.accessToken(t, "345", []string{aud}, id, nil, time.Hour) }
	if _, err := s.Authenticate(t.Context(), tok("proj")); err != nil {
		t.Fatalf("the right project: %v", err)
	}
	if _, err := s.Authenticate(t.Context(), tok("other-project")); err == nil {
		t.Fatal("a token for another project was admitted")
	}
	project = ""
	if _, err := s.Authenticate(t.Context(), tok("proj")); err == nil {
		t.Fatal("no project configured admitted a token")
	}
	s.Audience = nil
	if _, err := s.Authenticate(t.Context(), tok("proj")); err == nil {
		t.Fatal("no audience func admitted a token")
	}
}

// RA2: a client declared Internal yields an internal principal; the same token through a client
// that is not, never does.
func TestSponsoredInternalFlag(t *testing.T) {
	sg := newSigner(t)
	id := "345@platform"
	tok := sg.accessToken(t, "345", []string{"proj"}, id, nil, time.Hour)
	for name, c := range map[string]struct{ internal, want bool }{
		"an internal client": {true, true},
		"a plain client":     {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Sponsored{V: sg.verifier(), Audience: func() string { return "proj" },
				Clients: []SponsoredClient{{Name: "runlore", ClientID: func() string { return id }, Internal: c.internal}}}
			p, err := s.Authenticate(t.Context(), tok)
			if err != nil || !p.Sponsored || p.Internal != c.want {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}

// A generic issuer's client-credentials token: the client in azp, or in a configured claim (Okta's
// cid), and the configured audience in aud.
func TestSponsoredGeneric(t *testing.T) {
	sg := newSigner(t)
	const slack, api = "0oa-slack", "api://rooms"
	tok := func(claims jwt.MapClaims) string {
		c := jwt.MapClaims{"iss": issuer, "sub": slack, "aud": api, "exp": time.Now().Add(time.Hour).Unix()}
		maps.Copy(c, claims)
		return sign(t, jwt.SigningMethodRS256, sg.key, "", c)
	}
	s := func(claim string) *Sponsored {
		return &Sponsored{V: sg.verifier(), Clients: []SponsoredClient{{Name: "slack-app", ClientID: func() string { return slack }}},
			Audience: func() string { return api }, ClientClaim: claim, Generic: true}
	}
	cases := []struct {
		name, claim string
		raw         string
		ok          bool
	}{
		{"azp", "", tok(jwt.MapClaims{"azp": slack}), true},
		{"azp, another audience", "", tok(jwt.MapClaims{"azp": slack, "aud": "other"}), false},
		{"cid, by the configured claim (Okta)", "cid", tok(jwt.MapClaims{"cid": slack}), true},
		{"cid without the configured claim", "", tok(jwt.MapClaims{"cid": slack}), false},
		{"the configured claim naming another client", "cid", tok(jwt.MapClaims{"cid": "0oa-other", "azp": slack}), false},
		{"the configured claim not a string", "cid", tok(jwt.MapClaims{"cid": 7}), false},
		{"the configured claim, another audience", "cid", tok(jwt.MapClaims{"cid": slack, "aud": "other"}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := s(c.claim).Authenticate(t.Context(), c.raw)
			if c.ok != (err == nil) || c.ok && (p.ID != "system:slack-app" || p.ClientID != slack) {
				t.Fatalf("got %+v, %v; want ok %v", p, err, c.ok)
			}
		})
	}
}

// ClientClaim is one of a closed set, and never outvotes azp or client_id naming another client
// (review of #524). Under the zitadel rules the one-element aud never names a client.
func TestSponsoredClientClaimBounds(t *testing.T) {
	sg := newSigner(t)
	const slack, api = "0oa-slack", "api://rooms"
	tok := func(claims jwt.MapClaims) string {
		c := jwt.MapClaims{"iss": issuer, "sub": slack, "aud": api, "exp": time.Now().Add(time.Hour).Unix()}
		maps.Copy(c, claims)
		return sign(t, jwt.SigningMethodRS256, sg.key, "", c)
	}
	s := func(claim string, generic bool) *Sponsored {
		return &Sponsored{V: sg.verifier(), Clients: []SponsoredClient{{Name: "slack-app", ClientID: func() string { return slack }}},
			Audience: func() string { return api }, ClientClaim: claim, Generic: generic}
	}
	for _, c := range []struct {
		name, claim string
		generic     bool
		raw         string
		ok          bool
	}{
		{"sub is not a client claim", "sub", true, tok(nil), false},
		{"appid (Entra)", "appid", true, tok(jwt.MapClaims{"appid": slack}), true},
		{"cid, azp naming another client", "cid", true, tok(jwt.MapClaims{"cid": slack, "azp": "0oa-other"}), false},
		{"cid, client_id naming another client", "cid", true, tok(jwt.MapClaims{"cid": slack, "client_id": "0oa-other"}), false},
		{"cid, azp the same client", "cid", true, tok(jwt.MapClaims{"cid": slack, "azp": slack}), true},
		{"generic: aud the client alone is not the audience", "", true, tok(jwt.MapClaims{"aud": slack}), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s(c.claim, c.generic).Authenticate(t.Context(), c.raw); c.ok != (err == nil) {
				t.Fatalf("got %v, want ok %v", err, c.ok)
			}
		})
	}
	for _, claim := range []string{"cid", "azp", "client_id", "appid"} {
		if !ValidClientClaim(claim) {
			t.Errorf("%s refused", claim)
		}
	}
	for _, claim := range []string{"sub", "aud", "email", "Cid"} {
		if ValidClientClaim(claim) {
			t.Errorf("%s accepted", claim)
		}
	}
}
