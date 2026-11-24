// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Smana/maku/internal/envelope"
)

// SponsoredClient is one sponsored principal's ZITADEL client, its id read at use (Ruling AS-a).
type SponsoredClient struct {
	Name     string
	ClientID func() string
	// Internal marks an internal principal's client (RunLore agent-to-agent RA2): the principal it
	// yields is a sponsored one that may also ask for internal data.
	Internal bool
}

// Sponsored authenticates a sponsored principal (factory interfaces F4, R2): a ZITADEL service
// account's access token, issued to one of Clients through client credentials. The client id is
// the claim ZITADEL sets from the authenticated client; nothing else in the token names the
// principal. Exclude lists the human clients' ids: a principal whose id equals one never matches.
// Every error fails closed: no verifier, an unreadable id, or two principals resolving to the
// same id (the runtime twin of the config's duplicate-file check) admit nobody on that id.
type Sponsored struct {
	V       *Verifier
	Clients []SponsoredClient
	Exclude func() []string
	// Audience is the value a token's aud must hold: the project id under the zitadel preset, as
	// the human path pins it, else the configured audience. Unset or unreadable, nobody is admitted.
	Audience func() string
	// ClientClaim names the string claim holding the client id, for an IdP that puts it elsewhere
	// (Okta's cid): one of ValidClientClaim's, else nobody is admitted. azp or client_id naming
	// another client still refuses. "" reads azp, else client_id.
	ClientClaim string
	// Generic is the generic preset: the roles claim is not judged here, and with no ClientClaim
	// a one-element aud may name the client. Unset is the zitadel preset, byte for byte.
	Generic bool
}

// clientClaims are the claims a sponsored token may name its client in: Okta's cid, Entra's appid,
// and the two OAuth names. Any other claim (sub, email) could be chosen by the token's subject.
var clientClaims = []string{"cid", "azp", "client_id", "appid"}

// ValidClientClaim reports whether claim may name a sponsored token's client.
func ValidClientClaim(claim string) bool { return slices.Contains(clientClaims, claim) }

// Authenticate maps raw to its sponsored principal, or ErrUnauthenticated.
func (s *Sponsored) Authenticate(ctx context.Context, raw string) (Principal, error) {
	refused := fmt.Errorf("%w: not a sponsored principal", ErrUnauthenticated)
	if s == nil || s.V == nil || len(s.Clients) == 0 {
		return Principal{}, refused
	}
	if s.ClientClaim != "" && !ValidClientClaim(s.ClientClaim) {
		return Principal{}, refused
	}
	var aud string
	if s.Audience != nil {
		aud = s.Audience()
	}
	if aud == "" {
		return Principal{}, refused
	}
	parse := s.V.parseWith
	if s.Generic {
		parse = s.V.parseGeneric
	}
	cl, err := parse(ctx, raw)
	if err != nil || cl.ExpiresAt == nil || cl.Subject == "" || !slices.Contains(cl.Audience, aud) {
		return Principal{}, refused
	}
	tokClient := s.clientID(cl)
	if tokClient == "" {
		return Principal{}, refused
	}
	var human []string
	if s.Exclude != nil {
		human = s.Exclude()
	}
	if slices.Contains(human, tokClient) {
		return Principal{}, refused
	}
	ids := make([]string, len(s.Clients))
	for i, c := range s.Clients {
		if c.ClientID != nil {
			ids[i] = c.ClientID()
		}
	}
	for i, c := range s.Clients {
		id := ids[i]
		if c.Name == "" || id != tokClient {
			continue
		}
		if slices.Index(ids, id) != i || slices.Contains(ids[i+1:], id) { // shared: ambiguous, so nobody's
			return Principal{}, refused
		}
		return Principal{Kind: envelope.ActorSystem, ID: "system:" + c.Name, Sub: cl.Subject, ClientID: id,
			Expiry: cl.ExpiresAt.Time, AccessToken: raw, Sponsored: true, Internal: c.Internal}, nil
	}
	return Principal{}, refused
}

// clientID extracts the client id from verified claims: by azp or client_id (or one-element aud
// under Generic), or by ClientClaim when set.
func (s *Sponsored) clientID(c *Claims) string {
	switch {
	case s.ClientClaim == "" && s.Generic:
		return c.issuedToGeneric()
	case s.ClientClaim == "":
		return c.issuedTo()
	}
	var got string
	if v, ok := c.raw[s.ClientClaim]; !ok || json.Unmarshal(v, &got) != nil || got == "" {
		return ""
	}
	// The configured claim never outvotes azp or client_id naming another client.
	if c.AuthorizedParty != "" && c.AuthorizedParty != got || c.ClientID != "" && c.ClientID != got {
		return ""
	}
	return got
}
