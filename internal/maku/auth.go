// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Smana/maku/internal/authn"
)

// idTokenSkew refreshes an ID token this long before its exp, so a request never carries one the
// broker is about to refuse.
const idTokenSkew = 30 * time.Second

// errNoIDToken is a refresh with no id_token under token use id: OIDC Core makes it optional, and
// without it the session has no credential.
var errNoIDToken = errors.New("the IdP returned no ID token on refresh: run maku login")

// errIDTokenChanged is a refreshed ID token for another person or client (OIDC Core 12.2: its iss,
// sub and aud must be the original's).
var errIDTokenChanged = errors.New("the refreshed ID token names another issuer, subject or audience: run maku login")

// oauthConfig is the device grant's client: under the zitadel preset ZITADEL's fixed paths and the
// project's audience scope, as before; otherwise the issuer's discovered endpoints (cached by
// configure, else read from its discovery document now) and the configured scopes.
func oauthConfig(ctx context.Context, hc *http.Client, c Config) (*oauth2.Config, error) {
	scopes := []string{"openid", "profile", "email", "offline_access"}
	ep := oauth2.Endpoint{DeviceAuthURL: c.DeviceAuthURL, TokenURL: c.TokenURL,
		AuthStyle: oauth2.AuthStyleInParams} // a native app: client_id in the form, no secret
	if c.EffectivePreset() == PresetZITADEL {
		// ZITADEL puts the project id in aud only when asked (ruling AS), and
		// offline_access brings the refresh token.
		scopes = append(scopes, "urn:zitadel:iam:org:project:id:"+c.ProjectID+":aud")
		if ep.DeviceAuthURL == "" || ep.TokenURL == "" {
			iss := strings.TrimSuffix(c.Issuer, "/")
			ep.DeviceAuthURL, ep.TokenURL = iss+"/oauth/v2/device_authorization", iss+"/oauth/v2/token"
		}
	} else {
		scopes = append(scopes, c.Scopes...)
		if ep.DeviceAuthURL == "" || ep.TokenURL == "" {
			d, err := authn.Discover(ctx, hc, c.Issuer)
			if err != nil {
				return nil, fmt.Errorf("the issuer %s: %w", c.Issuer, err)
			}
			if d.DeviceAuthorizationEndpoint == "" || d.TokenEndpoint == "" {
				return nil, fmt.Errorf("the issuer %s serves no device authorization endpoint: maku cannot sign in to it", c.Issuer)
			}
			ep.DeviceAuthURL, ep.TokenURL = d.DeviceAuthorizationEndpoint, d.TokenEndpoint
		}
		// Checked again at use, cached or discovered: the refresh token and the device code go there.
		if !authn.HTTPSEndpoint(ep.DeviceAuthURL) || !authn.HTTPSEndpoint(ep.TokenURL) {
			return nil, fmt.Errorf("the issuer %s: its device or token endpoint is not an https URL: run maku configure again", c.Issuer)
		}
	}
	return &oauth2.Config{ClientID: c.ClientID, Scopes: scopes, Endpoint: ep}, nil
}

// authParams are the device authorization's extra parameters: the audience under generic token
// use access, which Auth0 reads instead of a scope.
func authParams(c Config) []oauth2.AuthCodeOption {
	if c.EffectivePreset() == PresetGeneric && c.EffectiveTokenUse() == TokenUseAccess && c.Audience != "" {
		return []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("audience", c.Audience)}
	}
	return nil
}

// Login runs the OAuth device authorization grant: no browser on this machine
// needs a redirect, and no client secret exists to leak. hc carries every call.
func Login(ctx context.Context, hc *http.Client, c Config, prompt io.Writer) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	conf, err := oauthConfig(ctx, hc, c)
	if err != nil {
		return nil, err
	}
	da, err := conf.DeviceAuth(ctx, authParams(c)...)
	if err != nil {
		return nil, fmt.Errorf("device authorization: %w%s", err, staleClientHint(err))
	}
	if _, err := fmt.Fprintf(prompt, "Open %s and enter the code %s\n", da.VerificationURI, da.UserCode); err != nil {
		return nil, err
	}
	tok, err := conf.DeviceAccessToken(ctx, da)
	if err != nil {
		return nil, fmt.Errorf("device token: %w%s", err, staleClientHint(err))
	}
	if c.EffectiveTokenUse() == TokenUseID && idTokenOf(tok) == "" {
		return nil, errors.New("the IdP returned no ID token: check that the client may use the openid scope")
	}
	return tok, nil
}

// StoredToken is the token file: the OAuth token, and the ID token its response carried, which
// oauth2.Token keeps only in memory. A file written before it reads with no ID token.
type StoredToken struct {
	oauth2.Token
	IDToken string `json:"id_token,omitempty"`
}

// SaveToken writes tok, its ID token included, owner-only.
func SaveToken(path string, tok *oauth2.Token) error {
	return SaveJSON(path, StoredToken{Token: *tok, IDToken: idTokenOf(tok)})
}

func idTokenOf(tok *oauth2.Token) string {
	s, _ := tok.Extra("id_token").(string)
	return s
}

// idClaims is what maku reads from an ID token it never verifies (the broker does): when to refresh
// it, and whom it names.
type idClaims struct {
	Iss string          `json:"iss"`
	Sub string          `json:"sub"`
	Aud json.RawMessage `json:"aud"`
	Exp int64           `json:"exp"`
}

func parseIDClaims(raw string) (idClaims, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return idClaims{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}, false
	}
	var c idClaims
	if json.Unmarshal(b, &c) != nil {
		return idClaims{}, false
	}
	return c, true
}

// audience is aud as a sorted list, whether the token wrote a string or an array.
func (c idClaims) audience() []string {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(c.Aud, &many)
	slices.Sort(many)
	return many
}

// sameIdentity reports whether next names the issuer, subject and audience old does (OIDC Core
// 12.2). An old token maku cannot read has nothing to compare.
func sameIdentity(old, next string) bool {
	o, ok := parseIDClaims(old)
	if !ok {
		return true
	}
	n, ok := parseIDClaims(next)
	return ok && o.Iss == n.Iss && o.Sub == n.Sub && slices.Equal(o.audience(), n.audience())
}

// idTokenFresh reports whether raw's exp is more than idTokenSkew away. maku never verifies a
// token, the broker does; it only reads when to refresh.
func idTokenFresh(raw string, now time.Time) bool {
	c, ok := parseIDClaims(raw)
	if !ok || c.Exp == 0 {
		return false
	}
	return now.Add(idTokenSkew).Before(time.Unix(c.Exp, 0))
}

// staleClientHint names the fix of invalid_client: after a platform rebuild the stored client id
// is no longer one, and ZITADEL's own sentence ("no active client not found") says nothing of it.
func staleClientHint(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorCode == "invalid_client" {
		return "\nhint: the stored client id is stale (the platform was rebuilt?): run maku configure --from https://rooms.<your private domain>, then maku login"
	}
	return ""
}

// Token returns a valid credential from the token file at path, refreshing and saving it when it
// has expired: the access token, or under token use id the ID token, refreshed on its own exp. A
// local broker takes none: there is no IdP, and the loopback listener authenticates the
// port-forward itself.
func Token(ctx context.Context, hc *http.Client, c Config, path string) (string, error) {
	if c.HumanMode == HumanModeLocal {
		return "", nil
	}
	var st StoredToken
	if err := LoadJSON(path, &st); err != nil {
		return "", fmt.Errorf("not logged in (%w): run maku login", err)
	}
	idUse := c.EffectiveTokenUse() == TokenUseID
	tok := st.Token
	if idUse && !idTokenFresh(st.IDToken, time.Now()) {
		tok.Expiry = time.Now().Add(-time.Minute) // the ID token is the credential: its exp decides
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	conf, err := oauthConfig(ctx, hc, c)
	if err != nil {
		return "", err
	}
	fresh, err := conf.TokenSource(ctx, &tok).Token()
	if err != nil {
		return "", fmt.Errorf("refresh the token (%w): run maku login", err)
	}
	if fresh.AccessToken != tok.AccessToken || !fresh.Expiry.Equal(tok.Expiry) {
		next := StoredToken{Token: *fresh, IDToken: idTokenOf(fresh)}
		if idUse && next.IDToken == "" {
			return "", errNoIDToken
		}
		if next.IDToken != "" && st.IDToken != "" && !sameIdentity(st.IDToken, next.IDToken) {
			return "", errIDTokenChanged
		}
		if next.IDToken == "" {
			next.IDToken = st.IDToken
		}
		if err := SaveJSON(path, next); err != nil {
			return "", fmt.Errorf("save the refreshed token: %w", err)
		}
		st = next
	}
	if idUse {
		return st.IDToken, nil
	}
	return st.AccessToken, nil
}

// SharedToken is Token for one process's concurrent callers, maku mcp's requests: one refresh
// at a time, so a refresh token the issuer rotates is never spent twice. Each call reads the file
// again, so a maku login in another terminal reaches a running relay without a restart.
func SharedToken(hc *http.Client, c Config, path string) func(context.Context) (string, error) {
	var mu sync.Mutex
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return Token(ctx, hc, c, path)
	}
}
