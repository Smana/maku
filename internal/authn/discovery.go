// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Smana/maku/internal/httpx"
)

// maxDiscoveryBytes bounds an issuer's discovery document; real ones are a few KiB.
const maxDiscoveryBytes = 256 << 10

// Discovery is what Maku reads from an issuer's OpenID Provider Configuration.
type Discovery struct {
	Issuer                      string `json:"issuer"`
	JWKSURI                     string `json:"jwks_uri"`
	AuthorizationEndpoint       string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint               string `json:"token_endpoint,omitempty"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint,omitempty"`
}

// DiscoveryURL is issuer's OpenID Provider Configuration URL (OIDC Discovery §4): a trailing slash
// on the issuer is not doubled.
func DiscoveryURL(issuer string) string {
	return strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
}

// Discover fetches issuer's OpenID Provider Configuration through hc. The document's issuer must
// equal issuer exactly (OIDC Discovery §4.3), and its jwks_uri be an absolute https:// URL. An
// error names the HTTP status, never the body.
func Discover(ctx context.Context, hc *http.Client, issuer string) (Discovery, error) {
	if !httpsURL(issuer) {
		return Discovery{}, errors.New("authn: discovery needs an https:// issuer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL(issuer), nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("discovery fetch: status %d", resp.StatusCode)
	}
	body, err := httpx.ReadBody(resp.Body, maxDiscoveryBytes)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery fetch: %w", err)
	}
	var d Discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return Discovery{}, errors.New("discovery decode: not a JSON document")
	}
	if d.Issuer != issuer {
		return Discovery{}, errors.New("discovery: the document's issuer is not the configured one (OIDC Discovery §4.3)")
	}
	if !httpsURL(d.JWKSURI) {
		return Discovery{}, errors.New("discovery: jwks_uri is not an absolute https:// URL")
	}
	// A client sends a refresh token, a device code or a client secret to these: never in clear.
	for name, ep := range map[string]string{"token_endpoint": d.TokenEndpoint,
		"device_authorization_endpoint": d.DeviceAuthorizationEndpoint, "authorization_endpoint": d.AuthorizationEndpoint} {
		if ep != "" && !httpsURL(ep) {
			return Discovery{}, fmt.Errorf("discovery: %s is not an absolute https:// URL", name)
		}
	}
	return d, nil
}

// HTTPSEndpoint reports whether raw is an absolute https:// URL with no userinfo: what a client
// checks again before it sends a credential to an endpoint it read or stored.
func HTTPSEndpoint(raw string) bool { return httpsURL(raw) }

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// NewDiscoveringVerifier is NewLazyVerifier with the JWKS URL read from the issuer's discovery
// document, at each fetch: nothing is fetched at construction, so an IdP that is down does not
// stop the process, and until it answers every token is refused.
func NewDiscoveringVerifier(issuer string, opts ...Option) (*Verifier, error) {
	if !httpsURL(issuer) {
		return nil, errors.New("authn: discovery needs an https:// issuer")
	}
	v, err := NewLazyVerifier(issuer, DiscoveryURL(issuer), opts...)
	if err != nil {
		return nil, err
	}
	c := v.jwks
	c.discover = func(ctx context.Context) (string, error) {
		d, err := Discover(ctx, c.client, issuer)
		if err != nil {
			return "", err
		}
		return d.JWKSURI, nil
	}
	return v, nil
}
