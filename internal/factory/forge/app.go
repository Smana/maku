// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Smana/maku/internal/httpx"
)

const (
	// An App JWT lives at most ten minutes; it is backdated a minute for clock drift and
	// expires after nine, so the two together stay inside GitHub's limit.
	jwtBackdate = time.Minute
	jwtLifetime = 9 * time.Minute
	// refreshBefore renews an installation token (one hour) while it still has this long to live.
	refreshBefore = 5 * time.Minute
	// maxAuthReply bounds an installation or token reply: a few hundred bytes in practice.
	maxAuthReply = 64 << 10
)

// permissions is the factory App's installation-token scope: exactly what it holds (R16),
// on the one repository. A token asking for more fails to mint, which is the point.
func permissions() map[string]string {
	return map[string]string{"contents": "read", "issues": "write", "pull_requests": "write", "metadata": "read"}
}

// MergerPermissions is the merger App's installation-token scope (TW4, R16): the merge-side
// calls read checks and statuses, write contents on the merger's own revert branches, and arm
// and merge pull requests — and nothing else. Like the factory's, it is the App's ceiling: a
// mint asking for more fails. rerun adds actions: write for RerunCheck: it is asked only when
// merge.checkReruns is on, so an App that was not granted it keeps minting. label adds issues:
// write for the breaker's labels (v2 D1) and the monitor's outcome labels (merge gate R30),
// written once a class is live or declares a monitor: an App with neither keeps minting without
// it, and still reads them (pull_requests covers the read). status raises statuses to write for
// the gate's own commit status, asked only when merge.gateStatus is on.
func MergerPermissions(rerun, label, status bool) map[string]string {
	p := map[string]string{"checks": "read", "statuses": "read", "contents": "write", "pull_requests": "write", "metadata": "read"}
	if status {
		p["statuses"] = "write"
	}
	if rerun {
		p["actions"] = "write"
	}
	if label {
		p["issues"] = "write"
	}
	return p
}

// tokens mints and caches installation tokens for one App on one repository. The App id and key
// are mounted files read at each mint, so a rotation needs no restart (AGENTS.md).
type tokens struct {
	api                *url.URL
	hc                 *http.Client
	owner, name, agent string
	idFile, keyFile    string
	now                func() time.Time
	// perms is the connection's scope; nil is the factory App's (Options.Permissions).
	perms map[string]string

	mu           sync.Mutex
	token        string
	expires      time.Time
	installation int64
}

// Token returns a token with more than refreshBefore to live, minting one when needed. Holding the
// lock across the mint makes concurrent callers wait for one mint instead of racing to many.
func (s *tokens) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.expires.Add(-refreshBefore)) {
		return s.token, nil
	}
	app := &AppClient{API: s.api.String(), HTTP: s.hc, IDFile: s.idFile, KeyFile: s.keyFile, UserAgent: s.agent, Now: s.now}
	if s.installation == 0 {
		id, err := app.Installation(ctx, s.owner, s.name)
		if err != nil {
			return "", err
		}
		s.installation = id
	}
	perms := s.perms
	if perms == nil {
		perms = permissions()
	}
	tok, err := app.unchecked(ctx, s.installation, []string{s.name}, perms)
	if err != nil {
		return "", err
	}
	s.token, s.expires = tok.Value, tok.ExpiresAt
	return tok.Value, nil
}

// MintOptions names the App, the repositories and the permissions of one installation token.
type MintOptions struct {
	AppIDFile, KeyFile string
	Owner              string            // the installation's account
	Repositories       []string          // names only, no owner
	Permissions        map[string]string // e.g. {"contents": "write"}
}

// Token is an installation token and when GitHub says it expires.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Mint signs a JWT from the App's files, finds the installation and mints one token, uncached.
// The installation is looked up on the first repository; every repository must belong to Owner.
func Mint(ctx context.Context, c *http.Client, apiURL string, o MintOptions) (Token, error) {
	app := &AppClient{API: apiURL, HTTP: c, IDFile: o.AppIDFile, KeyFile: o.KeyFile} // no cache: uncached
	if _, err := app.caller(); err != nil {
		return Token{}, err
	}
	if o.Owner == "" || len(o.Repositories) == 0 {
		return Token{}, errors.New("forge: a mint needs an owner and at least one repository")
	}
	id, err := app.Installation(ctx, o.Owner, o.Repositories[0])
	if err != nil {
		return Token{}, err
	}
	return app.unchecked(ctx, id, o.Repositories, o.Permissions)
}

// unchecked mints without comparing the reply to the request: the factory's and the merger's
// connections and step 2's shared run tokens, whose behaviour predates AccessToken's check.
func (c *AppClient) unchecked(ctx context.Context, id int64, repos []string, perms map[string]string) (Token, error) {
	ac, err := c.caller()
	if err != nil {
		return Token{}, err
	}
	appJWT, err := ac.sign(c.IDFile, c.KeyFile)
	if err != nil {
		return Token{}, err
	}
	r, err := ac.mint(ctx, appJWT, id, repos, perms)
	if err != nil {
		return Token{}, err
	}
	return Token{Value: r.token, ExpiresAt: r.expires}, nil
}

// appCaller is App-authenticated access to the API: what tokens and Mint share.
type appCaller struct {
	api   *url.URL
	hc    *http.Client
	agent string
	now   func() time.Time
}

// sign is the App JWT: RS256, issued by the App id.
func (s appCaller) sign(idFile, keyFile string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(idFile))
	if err != nil {
		return "", fmt.Errorf("app id: %w", err)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || id < 1 {
		return "", errors.New("app id: not a positive integer")
	}
	pemKey, err := os.ReadFile(filepath.Clean(keyFile))
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemKey)
	if err != nil {
		return "", errors.New("private key: not an RSA key in PEM") // never the parser's text: it may quote the file
	}
	now := s.now()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer: strconv.FormatInt(id, 10), IssuedAt: jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign the App JWT: %w", err)
	}
	return signed, nil
}

// find is the App's installation on the repository.
func (s appCaller) find(ctx context.Context, appJWT, owner, name string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	status, err := s.call(ctx, http.MethodGet, "repos/"+owner+"/"+name+"/installation", appJWT, nil, &out)
	switch {
	case status == http.StatusNotFound:
		return 0, fmt.Errorf("find the App's installation on %s/%s: %w", owner, name, ErrNotInstalled)
	case err != nil:
		return 0, fmt.Errorf("find the App's installation: %w", err)
	case out.ID < 1:
		return 0, errors.New("find the App's installation: no id in the reply")
	}
	return out.ID, nil
}

// minted is a mint's reply: the token and what GitHub says it granted.
type minted struct {
	token   string
	expires time.Time
	granted map[string]string
	repos   []string // names only
}

// mint is a new installation token, scoped to the repositories and permissions. A 422 is
// ErrPermissionNotGranted and a 404 ErrNotInstalled.
func (s appCaller) mint(ctx context.Context, appJWT string, installation int64, repos []string, perms map[string]string) (minted, error) {
	var out struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	in := map[string]any{"repositories": repos, "permissions": perms}
	path := "app/installations/" + strconv.FormatInt(installation, 10) + "/access_tokens"
	status, err := s.call(ctx, http.MethodPost, path, appJWT, in, &out)
	switch {
	case status == http.StatusUnprocessableEntity:
		return minted{}, fmt.Errorf("mint an installation token: %w", ErrPermissionNotGranted)
	case status == http.StatusNotFound:
		return minted{}, fmt.Errorf("mint an installation token: %w", ErrNotInstalled)
	case err != nil:
		return minted{}, fmt.Errorf("mint an installation token: %w", err)
	case out.Token == "" || !out.ExpiresAt.After(s.now()):
		return minted{}, errors.New("mint an installation token: the reply has no live token")
	}
	m := minted{token: out.Token, expires: out.ExpiresAt, granted: out.Permissions}
	for _, r := range out.Repositories {
		m.repos = append(m.repos, r.Name)
	}
	return m, nil
}

// call is one App-authenticated request. Its errors name the status only: a reply body may
// quote a credential.
func (s appCaller) call(ctx context.Context, method, path, appJWT string, in, out any) (int, error) {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return 0, fmt.Errorf("encode: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.api.JoinPath(path).String(), &body)
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", s.agent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxAuthReply)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, errors.New("the reply is not JSON")
	}
	return resp.StatusCode, nil
}

// auth puts the installation token on requests to the API host, and only there: anything else,
// a redirect's target included, goes out without it.
type auth struct {
	base   http.RoundTripper
	host   string
	agent  string
	tokens TokenSource
}

// RoundTrip implements http.RoundTripper.
func (a *auth) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, a.host) {
		return a.base.RoundTrip(req)
	}
	tok, err := a.tokens.Token(req.Context())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("User-Agent", a.agent)
	resp, err := a.base.RoundTrip(r)
	if inv, ok := a.tokens.(invalidator); ok && err == nil && resp.StatusCode == http.StatusUnauthorized {
		inv.Invalidate() // GitHub refused the token: the next call mints another
	}
	return resp, err
}

// statusError is a 4xx or 5xx answer, carried by status alone: the body is drained and dropped, so
// no payload text reaches an error or a log. errors.As reads the code back through the client's
// *url.Error.
type statusError struct{ code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("status %d %s", e.code, http.StatusText(e.code))
}

// statusRT turns a 4xx or 5xx response into a *statusError. Only the GraphQL client is wrapped
// with it: githubv4 reports any non-200 as an untyped error and keeps no status, while go-github
// needs the response of a non-2xx to build its own typed error.
type statusRT struct{ base http.RoundTripper }

// RoundTrip implements http.RoundTripper.
func (s statusRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.base.RoundTrip(req)
	if err != nil || resp.StatusCode < 400 { // 3xx passes: the client follows it through checkRedirect
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return nil, &statusError{code: resp.StatusCode}
}
