// SPDX-License-Identifier: Apache-2.0

// Package api is the run-request API (§4): once SP3 ships, the only way to create an AgentRun.
// The principal comes from the caller's token, never from the body.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/policy"
)

// AudienceSystem is what a system caller's projected token carries (R23).
const AudienceSystem = "agent-factory"

// Authenticator maps a run-request to its caller: a human through one of the
// two rooms clients (the broker forwards rooms-proxy's token, maku sends its
// own), or an allowlisted system caller on the cluster's issuer.
type Authenticator struct {
	Humans      *authn.Verifier   // ZITADEL, offline against its JWKS; nil in demo mode: every human token is refused
	Groups      policy.Groups     // the two agents groups; a human must be in one (R23)
	ClientIDs   func() []string   // rooms-proxy (the broker forwards it) and maku (the CLI)
	Systems     *authn.Verifier   // the cluster's issuer; nil when no system caller is allowed
	SystemAllow map[string]string // ServiceAccount sub → system:<component>; empty ships: none exists yet
	// Sponsored is F4's third branch; nil while api.sponsored is empty.
	Sponsored *authn.Sponsored
	// TokenUse is what the broker forwards and maku sends (v4 D8): "" or access, a JWT access
	// token; id, an ID token whose aud holds the client it was issued to.
	TokenUse string
	// RolesClaim names the claim holding the groups, or an RFC 6901 pointer; "" is the zitadel
	// preset's groups, else the project roles' keys.
	RolesClaim string
	// Generic is the generic preset: a one-element aud may name the client, and the roles claim
	// is judged by RoleNames. Unset is the zitadel preset, byte for byte.
	Generic bool
	// Audience is the aud value a generic access token must hold; nil or "" refuses. Unread under
	// the zitadel preset, whose human path pins the client alone, and under token use id.
	Audience func() string
	// RolesRefused counts a roles claim that gave no group, by its reason. Optional.
	RolesRefused func(ctx context.Context, reason string)
}

// Authenticate maps r to its caller's principal, from the bearer token alone.
// A valid token for a rejected caller is not mistaken for an invalid one: an
// unadmitted human and an unlisted system caller are ErrForbidden, every other
// refusal ErrUnauthenticated.
func (a *Authenticator) Authenticate(r *http.Request) (authn.Principal, error) {
	raw, err := authn.Bearer(r)
	if err != nil {
		return authn.Principal{}, err
	}
	for _, id := range a.ClientIDs() {
		if id == "" || a.Humans == nil {
			continue
		}
		// A refusal here is only a client mismatch until every id failed; it must
		// not mask the system branch, so it is skipped, not returned.
		c, err := a.verifyHuman(r.Context(), raw, id)
		if err != nil {
			continue
		}
		groups, err := c.RoleNames(a.RolesClaim) // a refused claim holds no group: refused below, never a member
		if rc := (*authn.RolesClaimError)(nil); errors.As(err, &rc) && a.RolesRefused != nil {
			a.RolesRefused(r.Context(), rc.Reason)
		}
		p := authn.Principal{Kind: envelope.ActorHuman, ID: "human:" + c.Subject, Sub: c.Subject, Groups: groups,
			ClientID: id, Expiry: c.ExpiresAt.Time, AccessToken: raw}
		if !a.Groups.Admitted(p) {
			// No claims in the message: it reaches logs (authn package rule).
			return authn.Principal{}, fmt.Errorf("%w: not in an agents group", authn.ErrForbidden)
		}
		return p, nil
	}
	if a.Sponsored != nil {
		if p, err := a.Sponsored.Authenticate(r.Context(), raw); err == nil {
			return p, nil
		}
	}
	if a.Systems != nil {
		if c, err := a.Systems.Verify(r.Context(), raw, AudienceSystem); err == nil {
			id, ok := a.SystemAllow[c.Subject]
			if !ok {
				return authn.Principal{}, fmt.Errorf("%w: the subject is not an allowlisted system caller", authn.ErrForbidden)
			}
			return authn.Principal{Kind: envelope.ActorSystem, ID: id, Sub: c.Subject, Expiry: c.ExpiresAt.Time}, nil
		}
	}
	if a.Humans == nil {
		return authn.Principal{}, fmt.Errorf("%w: demo_no_human_tokens", authn.ErrUnauthenticated)
	}
	return authn.Principal{}, fmt.Errorf("%w: no accepted issuer or client", authn.ErrUnauthenticated)
}

// verifyHuman checks raw, a human token issued to client, by the preset's and the token use's rule:
// an ID token whose aud holds the client (token use id); a generic access token whose aud holds the
// configured audience; or the zitadel preset's azp or client_id, aud unread, as before.
func (a *Authenticator) verifyHuman(ctx context.Context, raw, client string) (*authn.Claims, error) {
	switch {
	case a.TokenUse == authn.TokenUseID:
		return a.Humans.VerifyIDToken(ctx, raw, client)
	case a.Generic:
		c, err := a.Humans.VerifyClient(ctx, raw, client)
		if err != nil {
			return nil, err
		}
		var aud string
		if a.Audience != nil {
			aud = a.Audience()
		}
		if aud == "" || !slices.Contains(c.Audience, aud) {
			return nil, fmt.Errorf("%w: aud does not hold the audience", authn.ErrWrongAudience)
		}
		return c, nil
	}
	return a.Humans.VerifyAuthorizedParty(ctx, raw, client)
}
