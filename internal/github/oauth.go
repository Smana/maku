// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/httpx"
)

// OAuth is GitHub's web application flow, as the broker's GitHub account link runs it (v4 D8): the
// authorize URL, the code exchange with PKCE, GET /user, and the token's revocation. WebURL is
// GitHub's site (https://github.com, or a GHES host), APIURL its API base; both https. No error
// quotes a code, a verifier, a client secret or a token: a refusal names GitHub's error word or an
// HTTP status.
type OAuth struct {
	WebURL string
	APIURL string
	HC     *http.Client
}

// replyLimit bounds GitHub's answers here: a token reply or a user is a few hundred bytes.
const replyLimit = 64 << 10

// errorWordRE is GitHub's OAuth error code (bad_verification_code, incorrect_client_credentials …):
// anything else is not repeated.
var errorWordRE = regexp.MustCompile(`^[a-z_]{1,64}$`)

// loginRE is a GitHub login.
var loginRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)

func httpsBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("github oauth: the URL must be https://<host>, without credentials")
	}
	return u, nil
}

// AuthorizeURL is where the browser is sent: the user authorizes the client at GitHub, which
// redirects to redirect with a code and state. challenge is the PKCE S256 challenge.
func (o OAuth) AuthorizeURL(clientID, redirect, state, challenge string) string {
	u, err := url.Parse(o.WebURL)
	if err != nil {
		return ""
	}
	u = u.JoinPath("/login/oauth/authorize")
	u.RawQuery = url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}}.Encode()
	return u.String()
}

// ExchangeCode trades code and its PKCE verifier for a user token.
func (o OAuth) ExchangeCode(ctx context.Context, clientID, clientSecret, code, verifier, redirect string) (string, error) {
	base, err := httpsBase(o.WebURL)
	if err != nil {
		return "", err
	}
	if _, err := httpsBase(o.APIURL); err != nil {
		return "", err
	}
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.JoinPath("/login/oauth/access_token").String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("github oauth: the exchange request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "maku-broker")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := o.do(req, "the code exchange", &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		if !errorWordRE.MatchString(out.Error) {
			return "", errors.New("github oauth: the code exchange was refused")
		}
		return "", fmt.Errorf("github oauth: the code exchange was refused: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", errors.New("github oauth: the code exchange returned no token")
	}
	return out.AccessToken, nil
}

// User is the token's user: GitHub's numeric id and current login.
func (o OAuth) User(ctx context.Context, token string) (int64, string, error) {
	base, err := httpsBase(o.APIURL)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.JoinPath("/user").String(), nil)
	if err != nil {
		return 0, "", errors.New("github oauth: the user request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "maku-broker")
	req.Header.Set("Authorization", "Bearer "+token)
	var out struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := o.do(req, "GET /user", &out); err != nil {
		return 0, "", err
	}
	if out.ID <= 0 || !loginRE.MatchString(out.Login) {
		return 0, "", errors.New("github oauth: GET /user answered no id or login")
	}
	return out.ID, out.Login, nil
}

// RevokeUserToken deletes token at GitHub (DELETE /applications/{client_id}/token): the broker keeps
// none, so it revokes each one as soon as GET /user has answered.
func (o OAuth) RevokeUserToken(ctx context.Context, clientID, clientSecret, token string) error {
	base, err := httpsBase(o.APIURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return errors.New("github oauth: the revoke request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base.JoinPath("/applications", clientID, "token").String(), strings.NewReader(string(body)))
	if err != nil {
		return errors.New("github oauth: the revoke request")
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "maku-broker")
	req.Header.Set("Content-Type", "application/json")
	return o.do(req, "the token revocation", nil)
}

// do sends req and decodes a 2xx reply into out (nil: the body is dropped). A failure names what
// and the status, never the transport's error, whose text may hold the request.
func (o OAuth) do(req *http.Request, what string, out any) error {
	if o.HC == nil {
		return errors.New("github oauth: an HTTP client is required")
	}
	resp, err := o.HC.Do(req) //nolint:gosec // G704: the host is human.githubLink.webURL or the API base, both configuration, checked https; the tainted values are the form and the token
	if err != nil {
		return fmt.Errorf("github oauth: %s: GitHub is unreachable", what)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github oauth: %s: HTTP %s", what, strconv.Itoa(resp.StatusCode))
	}
	if out == nil {
		return nil
	}
	raw, err := httpx.ReadBody(resp.Body, replyLimit)
	if err != nil {
		return fmt.Errorf("github oauth: %s: the reply is unreadable", what)
	}
	if json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("github oauth: %s: the reply is not JSON", what)
	}
	return nil
}
