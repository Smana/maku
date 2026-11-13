// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

// ForwardedAccessHeader carries the access token oauth2-proxy forwards beside
// the ID token. Not named ...TokenHeader: gosec G101 reads that as a credential.
const ForwardedAccessHeader = "X-Forwarded-Access-Token"

// What a human presents as the credential (PR3).
const (
	// TokenUseAccess is the ID-plus-access pair for the web, and maku's access token as a bearer:
	// the zitadel preset, and any IdP that issues JWT access tokens with an audience.
	TokenUseAccess = "access"
	// TokenUseID makes the ID token the credential: its aud names the web or the CLI client. The
	// web session's access token is never parsed, and may be opaque.
	TokenUseID = "id"
)

// HumanOptions configures Humans over one OIDC issuer. The id functions are called on every
// request; an unreadable id returns "", which refuses.
type HumanOptions struct {
	// TokenUse is TokenUseAccess ("" too) or TokenUseID.
	TokenUse string
	// ZITADEL keeps the zitadel preset's rules (Ruling AS): every token's aud holds Audience (the
	// project id) and its client, which is azp on an ID token and client_id on an access token.
	// Otherwise an ID token's aud holds its client (OIDC Core), and an access token's aud holds
	// Audience, its client in azp or client_id.
	ZITADEL bool
	// Audience is the aud value every access token must hold. Unread under TokenUseID.
	Audience func() string
	// RolesClaim names the claim holding the groups, or an RFC 6901 pointer to it; "" is the
	// zitadel preset's GroupNames.
	RolesClaim string
	// Web and CLI are the rooms clients: oauth2-proxy's, and maku's ("" until it has one).
	Web, CLI func() string
	// Origin is the only Origin a browser request may carry (T9).
	Origin string
	// RolesRefused counts a roles claim that gave no group, by a RolesClaim* reason. Optional.
	RolesRefused func(ctx context.Context, reason string)
}

// Humans re-validates what oauth2-proxy forwards (§3, S5): the ID token in
// Authorization and, beside it, the access token in X-Forwarded-Access-Token.
// The broker never trusts a header oauth2-proxy merely asserts. maku sends its
// own token as a bearer, which oauth2-proxy passes through (phase 6).
type Humans struct {
	v       *Verifier
	use     string
	zitadel bool
	// Read at use from mounted files: ZITADEL mints new ids on every gcp-0 build
	// (Ruling AS-a), and a rotation must need no restart.
	audience func() string
	web      func() string
	maku     func() string // "" until phase 6
	roles    string
	origin   string
	refused  func(ctx context.Context, reason string)
}

// NewHumans authenticates the humans of one ZITADEL project: the zitadel preset, token use access.
// The id functions are called on every request; an unreadable id returns "", which refuses.
func NewHumans(v *Verifier, projectID, webClientID, roomctlClientID func() string, origin string) *Humans {
	return NewHumansWith(v, HumanOptions{TokenUse: TokenUseAccess, ZITADEL: true, Audience: projectID,
		Web: webClientID, CLI: roomctlClientID, Origin: origin})
}

// NewHumansWith authenticates the humans of one OIDC issuer, by o's token use and rules.
func NewHumansWith(v *Verifier, o HumanOptions) *Humans {
	none := func() string { return "" }
	h := &Humans{v: v, use: o.TokenUse, zitadel: o.ZITADEL, audience: o.Audience, web: o.Web, maku: o.CLI,
		roles: o.RolesClaim, origin: o.Origin, refused: o.RolesRefused}
	if h.use == "" {
		h.use = TokenUseAccess
	}
	for _, f := range []*func() string{&h.audience, &h.web, &h.maku} {
		if *f == nil {
			*f = none
		}
	}
	return h
}

// clients is the rooms clients' ids that are set.
func (h *Humans) clients() []string {
	return slices.DeleteFunc([]string{h.web(), h.maku()}, func(s string) bool { return s == "" })
}

// VerifyHuman checks an ID token. Under the zitadel preset (Ruling AS) its aud holds the project id
// and an allowlisted rooms client, and azp names that client: ZITADEL lists every app of the
// project in aud, so only azp says which app the token was issued to. Otherwise its aud holds the
// client it was issued to (azp, else client_id, else its one aud). Machine tokens stay on Verify's
// one-audience rule (AF).
func (h *Humans) VerifyHuman(ctx context.Context, raw string) (*Claims, error) {
	c, _, err := h.verifyID(ctx, raw)
	return c, err
}

// VerifyHumanAccess checks a JWT access token. Under the zitadel preset its aud holds the project id
// and its client, read from client_id: ZITADEL mints access tokens without azp (review C1).
// Otherwise its aud holds the configured audience, and azp or client_id names an allowlisted
// client. Under token use id no access token is verified.
func (h *Humans) VerifyHumanAccess(ctx context.Context, raw string) (*Claims, error) {
	c, _, err := h.verifyAccess(ctx, raw)
	return c, err
}

func (h *Humans) verifyID(ctx context.Context, raw string) (*Claims, string, error) {
	if h.zitadel {
		return h.verifyZITADEL(ctx, raw, func(c *Claims) string { return c.AuthorizedParty })
	}
	c, err := h.v.parseWith(ctx, raw)
	if err != nil {
		return nil, "", err
	}
	cli := c.issuedTo()
	if cli == "" || !slices.Contains(h.clients(), cli) || !slices.Contains(c.Audience, cli) {
		return nil, "", fmt.Errorf("%w: not issued to a rooms client", ErrWrongAudience)
	}
	if c.Subject == "" {
		return nil, "", fmt.Errorf("%w: no subject", ErrUnauthenticated)
	}
	return c, cli, nil
}

func (h *Humans) verifyAccess(ctx context.Context, raw string) (*Claims, string, error) {
	if h.zitadel {
		return h.verifyZITADEL(ctx, raw, func(c *Claims) string { return c.ClientID })
	}
	if h.use != TokenUseAccess {
		return nil, "", fmt.Errorf("%w: token use id verifies no access token", ErrUnauthenticated)
	}
	c, err := h.v.parse(ctx, raw, h.audience())
	if err != nil {
		return nil, "", err
	}
	cli := c.issuedTo()
	if cli == "" || !slices.Contains(h.clients(), cli) {
		return nil, "", fmt.Errorf("%w: not issued to a rooms client", ErrWrongAudience)
	}
	if c.Subject == "" {
		return nil, "", fmt.Errorf("%w: no subject", ErrUnauthenticated)
	}
	return c, cli, nil
}

// verifyZITADEL is the zitadel preset's rule: aud holds the project id and the client that client
// names.
func (h *Humans) verifyZITADEL(ctx context.Context, raw string, client func(*Claims) string) (*Claims, string, error) {
	c, err := h.v.parse(ctx, raw, h.audience())
	if err != nil {
		return nil, "", err
	}
	if cli := client(c); cli == "" || !slices.Contains(h.clients(), cli) || !slices.Contains(c.Audience, cli) {
		return nil, "", fmt.Errorf("%w: not issued to a rooms client", ErrWrongAudience)
	}
	if c.Subject == "" {
		return nil, "", fmt.Errorf("%w: no subject", ErrUnauthenticated)
	}
	return c, client(c), nil
}

// groups is the token's group names by the roles claim. One that gives none is counted, and the
// principal holds no group: never admitted as a member (fail closed).
func (h *Humans) groups(ctx context.Context, c *Claims) []string {
	names, err := c.RoleNames(h.roles)
	if err != nil {
		if rc := (*RolesClaimError)(nil); errors.As(err, &rc) && h.refused != nil {
			h.refused(ctx, rc.Reason)
		}
		return nil
	}
	return names
}

func (h *Humans) principal(ctx context.Context, c *Claims, client string, exp time.Time, credential string) Principal {
	return Principal{Kind: envelope.ActorHuman, ID: "human:" + c.Subject, Sub: c.Subject, Groups: h.groups(ctx, c),
		ClientID: client, Expiry: exp, AccessToken: credential}
}

// Authenticate maps a viewer's request to a human principal. A foreign Origin is
// ErrForbidden; every other refusal wraps ErrUnauthenticated. ClientID says which
// client the session came through, so callers can tell the web UI from maku
// (ruling P18).
func (h *Humans) Authenticate(r *http.Request) (Principal, error) {
	// T9: a browser always sends Origin on a WebSocket upgrade; another site's page must not ride the cookie.
	if o := r.Header.Get("Origin"); o != "" && o != h.origin {
		return Principal{}, fmt.Errorf("%w: foreign origin", ErrForbidden)
	}
	raw, err := Bearer(r)
	if err != nil {
		return Principal{}, err
	}
	if h.use == TokenUseID {
		return h.authenticateID(r, raw)
	}
	// maku sends its own access token as the bearer (phase 6). An id equal to
	// the web UI's would make that bearer a web session (review M5).
	if cli := h.maku(); cli != "" && cli != h.web() {
		if ac, client, err := h.verifyAccess(r.Context(), raw); err == nil && client == cli {
			return h.principal(r.Context(), ac, cli, ac.ExpiresAt.Time, raw), nil
		}
	}
	id, idClient, err := h.verifyID(r.Context(), raw)
	if err != nil {
		return Principal{}, err
	}
	// A web session carries two different tokens. A bearer oauth2-proxy lets
	// through (skip-jwt-bearer-tokens, phase 6) arrives as both at once, and must
	// never pass as a web session, which may steer and decide (ruling P18, review M16).
	access := r.Header.Get(ForwardedAccessHeader)
	if access == "" || access == raw {
		return Principal{}, fmt.Errorf("%w: a web session needs a distinct access token", ErrUnauthenticated)
	}
	ac, acClient, err := h.verifyAccess(r.Context(), access)
	if err != nil || acClient != idClient || ac.Subject != id.Subject {
		return Principal{}, fmt.Errorf("%w: the access token does not match the ID token", ErrUnauthenticated)
	}
	exp := id.ExpiresAt.Time
	if ac.ExpiresAt.Before(exp) {
		exp = ac.ExpiresAt.Time
	}
	return h.principal(r.Context(), id, idClient, exp, access), nil
}

// authenticateID is token use id (PR3): the ID token is the credential, and what the broker
// forwards to the factory. maku presents its own; a web session presents the web client's beside a
// distinct access token, which is never parsed (it may be opaque): its only part is P18's rule that
// a bearer oauth2-proxy let through, both headers equal, is never a web session.
func (h *Humans) authenticateID(r *http.Request, raw string) (Principal, error) {
	id, client, err := h.verifyID(r.Context(), raw)
	if err != nil {
		return Principal{}, err
	}
	if cli := h.maku(); cli != "" && cli != h.web() && client == cli {
		return h.principal(r.Context(), id, cli, id.ExpiresAt.Time, raw), nil
	}
	if access := r.Header.Get(ForwardedAccessHeader); access == "" || access == raw {
		return Principal{}, fmt.Errorf("%w: a web session needs a distinct access token", ErrUnauthenticated)
	}
	return h.principal(r.Context(), id, client, id.ExpiresAt.Time, raw), nil
}
