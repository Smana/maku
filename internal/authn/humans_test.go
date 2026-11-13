// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ZITADEL-shaped ids; the real ones are read from mounted files (Ruling AS-a).
const (
	project   = "project-1"
	webClient = "web-client"
	cliClient = "maku-client"
	uiOrigin  = "https://rooms.example.test"
)

// humanToken mints a ZITADEL-shaped ID token: aud holds the client and the
// project, and the client is in both azp and client_id.
func (s signer) humanToken(t *testing.T, sub string, aud []string, azp string, groups []string, ttl time.Duration) string {
	t.Helper()
	return sign(t, jwt.SigningMethodRS256, s.key, "", Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: sub, Audience: aud,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl))},
		Groups: groups, AuthorizedParty: azp, ClientID: azp,
	})
}

// accessToken mints a ZITADEL-shaped JWT access token: the client is in
// client_id, never in azp (zitadel/oidc NewAccessTokenClaims).
func (s signer) accessToken(t *testing.T, sub string, aud []string, clientID string, groups []string, ttl time.Duration) string {
	t.Helper()
	return sign(t, jwt.SigningMethodRS256, s.key, "", Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: sub, Audience: aud,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl))},
		Groups: groups, ClientID: clientID,
	})
}

// azpOnlyToken is an access token of the shape ZITADEL never mints: azp set, no client_id.
func (s signer) azpOnlyToken(t *testing.T, sub string, aud []string, azp string) string {
	t.Helper()
	return sign(t, jwt.SigningMethodRS256, s.key, "", Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: sub, Audience: aud,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
		AuthorizedParty: azp,
	})
}

func (s signer) humans(projectID, maku string) *Humans {
	return NewHumans(s.verifier(), func() string { return projectID },
		func() string { return webClient }, func() string { return maku }, uiOrigin)
}

func TestVerifyHuman(t *testing.T) {
	s := newSigner(t)
	web := []string{webClient, project}
	cases := []struct {
		name       string
		token      string
		projectID  string
		maku       string
		wantErr    error // nil: accepted
		wantClient string
	}{
		{"a web ID token", s.humanToken(t, "2918", web, webClient, nil, time.Hour), project, "", nil, webClient},
		{"a maku token once its client is set",
			s.humanToken(t, "2918", []string{cliClient, project}, cliClient, nil, time.Hour), project, cliClient, nil, cliClient},
		{"another project app lists our client in aud, but azp names it",
			s.humanToken(t, "2918", []string{"other-app", webClient, project}, "other-app", nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"a maku token before maku has a client",
			s.humanToken(t, "2918", []string{cliClient, project}, cliClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"an aud without the project id",
			s.humanToken(t, "2918", []string{webClient}, webClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"a client outside the allowlist",
			s.humanToken(t, "2918", []string{"other-app", project}, "other-app", nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"an azp that is not in aud",
			s.humanToken(t, "2918", web, cliClient, nil, time.Hour), project, cliClient, ErrWrongAudience, ""},
		{"no subject", s.humanToken(t, "", web, webClient, nil, time.Hour), project, "", ErrUnauthenticated, ""},
		{"no azp with several audiences", s.humanToken(t, "2918", web, "", nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"an access token: client_id, no azp", s.accessToken(t, "2918", web, webClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"a machine token", s.token(t, issuer, runSub, AudienceRun, time.Hour), project, "", ErrWrongAudience, ""},
		{"no project id to check (an unreadable file)", s.humanToken(t, "2918", web, webClient, nil, time.Hour), "", "", ErrUnauthenticated, ""},
		{"an expired token", s.humanToken(t, "2918", web, webClient, nil, -time.Hour), project, "", ErrTokenExpired, ""},
		{"another issuer", sign(t, jwt.SigningMethodRS256, s.key, "", Claims{
			RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://elsewhere.test", Subject: "2918", Audience: web,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, AuthorizedParty: webClient}),
			project, "", ErrWrongIssuer, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.humans(c.projectID, c.maku).VerifyHuman(t.Context(), c.token)
			if c.wantErr == nil {
				if err != nil || got.AuthorizedParty != c.wantClient {
					t.Fatalf("got %+v, %v; want accepted for %s", got, err, c.wantClient)
				}
				return
			}
			if !errors.Is(err, c.wantErr) || !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// A ZITADEL JWT access token names its client in client_id and never in azp
// (review C1): the client must be allowlisted and in aud, beside the project.
func TestVerifyHumanAccess(t *testing.T) {
	s := newSigner(t)
	web := []string{webClient, project}
	cases := []struct {
		name       string
		token      string
		projectID  string
		maku       string
		wantErr    error // nil: accepted
		wantClient string
	}{
		{"a web access token", s.accessToken(t, "2918", web, webClient, nil, time.Hour), project, "", nil, webClient},
		{"a maku access token once its client is set",
			s.accessToken(t, "2918", []string{cliClient, project}, cliClient, nil, time.Hour), project, cliClient, nil, cliClient},
		{"azp but no client_id, a shape ZITADEL never mints", s.azpOnlyToken(t, "2918", web, webClient), project, "", ErrWrongAudience, ""},
		{"a maku access token before maku has a client",
			s.accessToken(t, "2918", []string{cliClient, project}, cliClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"a client outside the allowlist",
			s.accessToken(t, "2918", []string{"other-app", webClient, project}, "other-app", nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"a client_id that is not in aud",
			s.accessToken(t, "2918", []string{"other-app", project}, webClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"an aud without the project id", s.accessToken(t, "2918", []string{webClient}, webClient, nil, time.Hour), project, "", ErrWrongAudience, ""},
		{"no subject", s.accessToken(t, "", web, webClient, nil, time.Hour), project, "", ErrUnauthenticated, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.humans(c.projectID, c.maku).VerifyHumanAccess(t.Context(), c.token)
			if c.wantErr == nil {
				if err != nil || got.ClientID != c.wantClient {
					t.Fatalf("got %+v, %v; want accepted for %s", got, err, c.wantClient)
				}
				return
			}
			if !errors.Is(err, c.wantErr) || !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// Ruling AF stays for machine tokens: a ZITADEL token is good for two audiences.
func TestVerifyRefusesAHumanToken(t *testing.T) {
	s := newSigner(t)
	tok := s.humanToken(t, "2918", []string{webClient, project}, webClient, nil, time.Hour)
	for _, aud := range []string{webClient, project} {
		t.Run(aud, func(t *testing.T) {
			if _, err := s.verifier().Verify(t.Context(), tok, aud); !errors.Is(err, ErrWrongAudience) {
				t.Fatalf("got %v, want ErrWrongAudience", err)
			}
		})
	}
}

func TestHumansAuthenticate(t *testing.T) {
	s := newSigner(t)
	web := []string{webClient, project}
	id := s.humanToken(t, "2918", web, webClient, []string{"agents-member"}, time.Hour)
	access := s.accessToken(t, "2918", web, webClient, nil, 30*time.Minute)
	cli := s.accessToken(t, "2918", []string{cliClient, project}, cliClient, []string{"agents-member"}, time.Hour)
	type want struct {
		err         error // nil: accepted
		client      string
		accessToken string
	}
	cases := []struct {
		name               string
		bearer, access, og string
		want               want
	}{
		{"a web session from our origin", id, access, uiOrigin, want{client: webClient, accessToken: access}},
		{"a maku bearer", cli, "", "", want{client: cliClient, accessToken: cli}},
		{"a cross-site WebSocket (T9)", id, access, "https://evil.example", want{err: ErrForbidden}},
		{"an opaque origin (Origin: null)", id, access, "null", want{err: ErrForbidden}},
		{"our host over another scheme", id, access, "http://rooms.example.test", want{err: ErrForbidden}},
		{"our host on another port", id, access, "https://rooms.example.test:8443", want{err: ErrForbidden}},
		{"an access token for another sub", id, s.accessToken(t, "9999", web, webClient, nil, time.Hour), "", want{err: ErrUnauthenticated}},
		{"an access token with azp but no client_id (review C1)", id, s.azpOnlyToken(t, "2918", web, webClient), "",
			want{err: ErrUnauthenticated}},
		{"a maku bearer with azp but no client_id", s.azpOnlyToken(t, "2918", []string{cliClient, project}, cliClient), "", "",
			want{err: ErrUnauthenticated}},
		{"no access token", id, "", "", want{err: ErrUnauthenticated}},
		{"the ID token in both headers, a bearer oauth2-proxy let through (review M16)", id, id, "", want{err: ErrUnauthenticated}},
		{"an access token issued to maku", id, s.accessToken(t, "2918", []string{cliClient, project}, cliClient, nil, time.Hour), "",
			want{err: ErrUnauthenticated}},
		{"a token for another ZITADEL app", s.humanToken(t, "2918", []string{"other-app", project}, "other-app", nil, time.Hour), access, "",
			want{err: ErrUnauthenticated}},
		{"no bearer", "", access, "", want{err: ErrUnauthenticated}},
	}
	h := s.humans(project, cliClient)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := request(t, c.bearer)
			if c.access != "" {
				r.Header.Set(ForwardedAccessHeader, c.access)
			}
			if c.og != "" {
				r.Header.Set("Origin", c.og)
			}
			p, err := h.Authenticate(r)
			if c.want.err != nil {
				if !errors.Is(err, c.want.err) {
					t.Fatalf("got %+v, %v; want %v", p, err, c.want.err)
				}
				return
			}
			if err != nil || p.Kind != "human" || p.ID != "human:2918" || p.Sub != "2918" || p.ClientID != c.want.client ||
				p.AccessToken != c.want.accessToken || !slices.Equal(p.Groups, []string{"agents-member"}) {
				t.Fatalf("got %#v %v", p, err)
			}
		})
	}
	t.Run("a web session lives until the earlier of its two tokens", func(t *testing.T) {
		r := request(t, id)
		r.Header.Set(ForwardedAccessHeader, access)
		p, err := h.Authenticate(r)
		if err != nil || p.Expiry.After(time.Now().Add(31*time.Minute)) {
			t.Fatalf("expiry %v, %v", p.Expiry, err)
		}
	})
	// A misconfiguration that mounts the web UI's id as maku's must not let a
	// bearer pass as a web session, which may steer and decide (ruling P18).
	t.Run("a maku client id equal to the web UI's (review M5)", func(t *testing.T) {
		same := s.humans(project, webClient)
		bearer := s.accessToken(t, "2918", web, webClient, []string{"agents-member"}, time.Hour)
		if p, err := same.Authenticate(request(t, bearer)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("a bearer: got %+v, %v", p, err)
		}
		r := request(t, id)
		r.Header.Set(ForwardedAccessHeader, access)
		if p, err := same.Authenticate(r); err != nil || p.ClientID != webClient {
			t.Fatalf("the web session still signs in: %+v, %v", p, err)
		}
	})
	t.Run("a refusal never echoes a client id or the subject", func(t *testing.T) {
		r := request(t, s.humanToken(t, "2918", []string{"other-app", project}, "other-app", nil, time.Hour))
		_, err := h.Authenticate(r)
		for _, leak := range []string{"other-app", "2918", project} {
			if err == nil || strings.Contains(err.Error(), leak) {
				t.Fatalf("%v", err)
			}
		}
	})
}

func TestGroupNames(t *testing.T) {
	roles := func(names ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for _, n := range names {
			m[n] = json.RawMessage(`{"123":"org.example"}`)
		}
		return m
	}
	cases := []struct {
		name string
		c    Claims
		want []string
	}{
		{"the groups claim wins", Claims{Groups: []string{"g"}, ProjectRoles: roles("r")}, []string{"g"}},
		{"else the project role keys, sorted", Claims{ProjectRoles: roles("b", "a")}, []string{"a", "b"}},
		{"neither is no group", Claims{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.GroupNames(); !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The ZITADEL claim decodes into ProjectRoles.
func TestProjectRolesClaim(t *testing.T) {
	var c Claims
	if err := json.Unmarshal([]byte(`{"urn:zitadel:iam:org:project:roles":{"agents-admin":{"1":"o"}}}`), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.GroupNames(); !slices.Equal(got, []string{"agents-admin"}) {
		t.Fatalf("got %v", got)
	}
}

// oidcToken mints a token of any claim set from the test issuer, expiring in an hour unless set.
func (s signer) oidcToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	c := jwt.MapClaims{"iss": issuer, "exp": time.Now().Add(time.Hour).Unix()}
	maps.Copy(c, claims)
	return sign(t, jwt.SigningMethodRS256, s.key, "", c)
}

func (s signer) genericHumans(use, audience, roles string, refused func(string)) *Humans {
	return NewHumansWith(s.verifier(), HumanOptions{TokenUse: use, Audience: func() string { return audience }, RolesClaim: roles,
		Web: func() string { return webClient }, CLI: func() string { return cliClient }, Origin: uiOrigin, RolesRefused: refused})
}

// The zitadel preset through the options is the old constructor, case for case.
func TestHumansZITADELOptions(t *testing.T) {
	s := newSigner(t)
	h := NewHumansWith(s.verifier(), HumanOptions{TokenUse: TokenUseAccess, ZITADEL: true, Audience: func() string { return project },
		Web: func() string { return webClient }, CLI: func() string { return cliClient }, Origin: uiOrigin})
	web := []string{webClient, project}
	r := request(t, s.humanToken(t, "2918", web, webClient, []string{"agents-member"}, time.Hour))
	r.Header.Set(ForwardedAccessHeader, s.accessToken(t, "2918", web, webClient, nil, time.Hour))
	if p, err := h.Authenticate(r); err != nil || p.ClientID != webClient || !slices.Equal(p.Groups, []string{"agents-member"}) {
		t.Fatalf("%+v %v", p, err)
	}
	// The zitadel rule still needs the project in aud, where a generic ID token holds its client alone.
	if _, err := h.VerifyHuman(t.Context(), s.humanToken(t, "2918", []string{webClient}, webClient, nil, time.Hour)); !errors.Is(err, ErrWrongAudience) {
		t.Fatalf("the zitadel rule still needs the project in aud: %v", err)
	}
}

// Token use id: the ID token is the credential (PR3). A web session is the ID token for the web
// client plus a distinct access token, which is never parsed; the CLI presents its own ID token.
func TestHumansTokenUseID(t *testing.T) {
	s := newSigner(t)
	id := func(sub string, aud any, extra jwt.MapClaims) string {
		c := jwt.MapClaims{"sub": sub, "aud": aud, "groups": []string{"agents-member"}}
		maps.Copy(c, extra)
		return s.oidcToken(t, c)
	}
	webID := id("2918", webClient, nil)
	cliID := id("2918", cliClient, nil)
	const opaque = "opaque-access-token"
	type want struct {
		err    error
		client string
	}
	cases := []struct {
		name           string
		bearer, access string
		want           want
	}{
		{"a web session: the ID token and an opaque access token", webID, opaque, want{client: webClient}},
		{"a web session with azp", id("2918", []string{webClient}, jwt.MapClaims{"azp": webClient}), opaque, want{client: webClient}},
		{"the CLI's ID token as the bearer", cliID, "", want{client: cliClient}},
		{"the CLI's bearer let through oauth2-proxy (both headers)", cliID, cliID, want{client: cliClient}},
		{"the web ID token presented alone", webID, "", want{err: ErrUnauthenticated}},
		{"equal headers (P18)", webID, webID, want{err: ErrUnauthenticated}},
		{"aud for another client", id("2918", "grafana", nil), opaque, want{err: ErrWrongAudience}},
		{"aud holding ours and another, no azp", id("2918", []string{webClient, "grafana"}, nil), opaque, want{err: ErrWrongAudience}},
		{"azp naming another client", id("2918", []string{webClient, "grafana"}, jwt.MapClaims{"azp": "grafana"}), opaque,
			want{err: ErrWrongAudience}},
		{"azp ours, aud not", id("2918", "grafana", jwt.MapClaims{"azp": webClient}), opaque, want{err: ErrWrongAudience}},
		{"no subject", id("", webClient, nil), opaque, want{err: ErrUnauthenticated}},
		{"expired", id("2918", webClient, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}), opaque, want{err: ErrTokenExpired}},
	}
	h := s.genericHumans(TokenUseID, "", "groups", nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := request(t, c.bearer)
			if c.access != "" {
				r.Header.Set(ForwardedAccessHeader, c.access)
			}
			p, err := h.Authenticate(r)
			if c.want.err != nil {
				if !errors.Is(err, c.want.err) || !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("got %+v, %v; want %v", p, err, c.want.err)
				}
				return
			}
			if err != nil || p.ID != "human:2918" || p.ClientID != c.want.client || p.AccessToken != c.bearer ||
				!slices.Equal(p.Groups, []string{"agents-member"}) || p.Expiry.IsZero() {
				t.Fatalf("got %#v %v", p, err)
			}
		})
	}
	t.Run("the refusal of a web ID token alone names the rule", func(t *testing.T) {
		_, err := h.Authenticate(request(t, webID))
		if err == nil || !strings.Contains(err.Error(), "a web session needs a distinct access token") {
			t.Fatalf("%v", err)
		}
	})
}

// A token whose roles claim gives nothing is still a principal, with no group: the policy's
// "not in an agents group" refuses it, never this authenticator, and the reason is counted.
func TestHumansRolesClaim(t *testing.T) {
	s := newSigner(t)
	cases := []struct {
		name, roles string
		claims      jwt.MapClaims
		want        []string
		refused     string
	}{
		{"Keycloak's pointer", "/realm_access/roles", jwt.MapClaims{"realm_access": map[string]any{"roles": []string{"agents-admin"}}},
			[]string{"agents-admin"}, ""},
		{"Okta's groups", "groups", jwt.MapClaims{"groups": []string{"agents-member", "everyone"}}, []string{"agents-member", "everyone"}, ""},
		{"Auth0's namespaced claim", "https://ns.example/roles", jwt.MapClaims{"https://ns.example/roles": []string{"agents-member"}},
			[]string{"agents-member"}, ""},
		{"Authentik's single string", "groups", jwt.MapClaims{"groups": "agents-member"}, []string{"agents-member"}, ""},
		{"no roles claim", "groups", jwt.MapClaims{}, nil, RolesClaimMissing},
		{"a number", "groups", jwt.MapClaims{"groups": 7}, nil, RolesClaimType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			h := s.genericHumans(TokenUseID, "", c.roles, func(reason string) { got = append(got, reason) })
			claims := jwt.MapClaims{"sub": "2918", "aud": cliClient}
			maps.Copy(claims, c.claims)
			p, err := h.Authenticate(request(t, s.oidcToken(t, claims)))
			if err != nil || !slices.Equal(p.Groups, c.want) {
				t.Fatalf("got %+v, %v; want groups %v", p, err, c.want)
			}
			if c.refused == "" && len(got) > 0 || c.refused != "" && !slices.Equal(got, []string{c.refused}) {
				t.Fatalf("counted %v, want %q", got, c.refused)
			}
		})
	}
}

// Token use access over a generic issuer (Auth0): the ID token is checked as OIDC Core says (aud
// holds its client), the access token by the configured audience and its azp or client_id.
func TestHumansGenericAccess(t *testing.T) {
	s := newSigner(t)
	const api = "https://api.rooms.example"
	webID := s.oidcToken(t, jwt.MapClaims{"sub": "2918", "aud": webClient, "groups": []string{"agents-member"}})
	access := func(sub, azp string, aud any) string {
		return s.oidcToken(t, jwt.MapClaims{"sub": sub, "aud": aud, "azp": azp, "groups": []string{"agents-member"}})
	}
	h := s.genericHumans(TokenUseAccess, api, "groups", nil)
	cases := []struct {
		name           string
		bearer, access string
		client         string // "" refuses
	}{
		{"a web session", webID, access("2918", webClient, []string{api, "https://idp.example/userinfo"}), webClient},
		{"maku's access token", access("2918", cliClient, api), "", cliClient},
		{"an access token without the audience", webID, access("2918", webClient, "other-api"), ""},
		{"an access token for another sub", webID, access("9999", webClient, api), ""},
		{"an access token issued to maku in a web session", webID, access("2918", cliClient, api), ""},
		{"equal headers", webID, webID, ""},
		{"no access token", webID, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := request(t, c.bearer)
			if c.access != "" {
				r.Header.Set(ForwardedAccessHeader, c.access)
			}
			p, err := h.Authenticate(r)
			if c.client == "" {
				if !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("got %+v, %v", p, err)
				}
				return
			}
			if err != nil || p.ClientID != c.client || !slices.Equal(p.Groups, []string{"agents-member"}) {
				t.Fatalf("got %+v, %v", p, err)
			}
		})
	}
	// No audience configured (an unreadable file) refuses every access token.
	if _, err := s.genericHumans(TokenUseAccess, "", "groups", nil).Authenticate(request(t, access("2918", cliClient, api))); err == nil {
		t.Fatal("no audience admitted maku's access token")
	}
}
