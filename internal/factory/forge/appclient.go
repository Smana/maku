// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The App calls' failures a caller branches on (v4 D10, plan PR6). None carries a reply body.
var (
	// ErrNotInstalled is a 404 from the installation lookup or the mint: the App is not installed
	// on the repository, or the installation is gone.
	ErrNotInstalled = errors.New("the App is not installed on the repository")
	// ErrPermissionNotGranted is a 422 from the mint: the installation lacks a permission asked.
	ErrPermissionNotGranted = errors.New("the App's installation does not grant a permission asked")
	// ErrScopeMismatch is a minted token whose permissions or repositories differ from the request.
	// The token is revoked before the error returns.
	ErrScopeMismatch = errors.New("the minted token's scope differs from the request")
)

// installationTTL is how long a repository's installation id is reused without a lookup.
const installationTTL = time.Hour

// Installations caches each repository's App installation id, keyed by lowercased owner/repo, so
// one App serving many repositories and owners looks each up once an hour. The zero value is ready.
type Installations struct {
	TTL time.Duration    // installationTTL when zero
	Now func() time.Time // time.Now when nil

	mu  sync.Mutex
	ids map[string]installationEntry
}

type installationEntry struct {
	id int64
	at time.Time
}

func installationKey(owner, name string) string { return strings.ToLower(owner + "/" + name) }

func (c *Installations) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Installations) get(owner, name string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = installationTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.ids[installationKey(owner, name)]
	if !ok || !c.now().Before(e.at.Add(ttl)) {
		return 0, false
	}
	return e.id, true
}

func (c *Installations) put(owner, name string, id int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ids == nil {
		c.ids = map[string]installationEntry{}
	}
	c.ids[installationKey(owner, name)] = installationEntry{id: id, at: c.now()}
}

// Evict drops the repository's installation, so the next Installation looks it up again.
func (c *Installations) Evict(owner, name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ids, installationKey(owner, name))
}

// AppClient is one GitHub App's own calls: the installation lookup, a downscoped mint checked
// against its reply, revoke and the installation's granted permissions. The App id and key are
// mounted files read at each call, so a rotation needs no restart. Every URL is built on API, so a
// GHES base (https://ghe.example/api/v3/) works as api.github.com does. Its errors name the HTTP
// status only: a reply body may quote a credential.
type AppClient struct {
	API             string       // https://<host>/[path/]
	HTTP            *http.Client // from httpx.New
	IDFile, KeyFile string
	UserAgent       string           // "agent-factory" when empty
	Now             func() time.Time // time.Now when nil
	// Installations caches the lookups; nil looks up on every call.
	Installations *Installations
}

func (c *AppClient) caller() (appCaller, error) {
	api, err := url.Parse(c.API)
	switch {
	case err != nil || api.Scheme != "https" || api.Host == "" || api.User != nil:
		return appCaller{}, errors.New("forge: the API URL must be https://<host>/, without credentials")
	case c.HTTP == nil || c.HTTP.Transport == nil:
		return appCaller{}, errors.New("forge: an HTTP client from httpx.New is required")
	}
	if !strings.HasSuffix(api.Path, "/") {
		api.Path += "/"
	}
	agent, now := c.UserAgent, c.Now
	if agent == "" {
		agent = "agent-factory"
	}
	if now == nil {
		now = time.Now
	}
	return appCaller{api: api, hc: c.HTTP, agent: agent, now: now}, nil
}

// Installation is the App's installation on owner/name, from the cache while it is fresh. A 404 is
// ErrNotInstalled and evicts the repository.
func (c *AppClient) Installation(ctx context.Context, owner, name string) (int64, error) {
	if id, ok := c.Installations.get(owner, name); ok {
		return id, nil
	}
	ac, err := c.caller()
	if err != nil {
		return 0, err
	}
	appJWT, err := ac.sign(c.IDFile, c.KeyFile)
	if err != nil {
		return 0, err
	}
	id, err := ac.find(ctx, appJWT, owner, name)
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			c.Installations.Evict(owner, name)
		}
		return 0, err
	}
	c.Installations.put(owner, name, id)
	return id, nil
}

// Evict drops owner/name from the installation cache, if there is one.
func (c *AppClient) Evict(owner, name string) { c.Installations.Evict(owner, name) }

// AccessToken mints a token of installation id on exactly repos (names, no owner) with exactly
// perms, and checks GitHub's reply: permissions that differ from perms, or repositories that differ
// from repos, revoke the token and fail with ErrScopeMismatch. It returns the token with what the
// reply granted. A 422 is ErrPermissionNotGranted, a 404 ErrNotInstalled.
func (c *AppClient) AccessToken(ctx context.Context, id int64, repos []string, perms map[string]string) (Token, map[string]string, []string, error) {
	ac, err := c.caller()
	if err != nil {
		return Token{}, nil, nil, err
	}
	appJWT, err := ac.sign(c.IDFile, c.KeyFile)
	if err != nil {
		return Token{}, nil, nil, err
	}
	r, err := ac.mint(ctx, appJWT, id, repos, perms)
	if err != nil {
		return Token{}, nil, nil, err
	}
	if !maps.Equal(r.granted, perms) || !sameNames(r.repos, repos) {
		_ = c.revoke(ctx, ac, r.token) // best effort: the error that matters is the mismatch
		return Token{}, nil, nil, fmt.Errorf("mint an installation token: %w", ErrScopeMismatch)
	}
	return Token{Value: r.token, ExpiresAt: r.expires}, r.granted, r.repos, nil
}

// sameNames is whether a and b hold the same repository names, ignoring order and case.
func sameNames(a, b []string) bool {
	norm := func(s []string) []string {
		out := make([]string, len(s))
		for i, n := range s {
			out[i] = strings.ToLower(n)
		}
		slices.Sort(out)
		return out
	}
	return len(a) == len(b) && slices.Equal(norm(a), norm(b))
}

// Revoke revokes an installation token (DELETE /installation/token, as the token itself). A token
// GitHub no longer knows (401) is already revoked: nil.
func (c *AppClient) Revoke(ctx context.Context, token string) error {
	ac, err := c.caller()
	if err != nil {
		return err
	}
	return c.revoke(ctx, ac, token)
}

func (c *AppClient) revoke(ctx context.Context, ac appCaller, token string) error {
	status, err := ac.call(ctx, http.MethodDelete, "installation/token", token, nil, nil)
	if status == http.StatusUnauthorized {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revoke an installation token: %w", err)
	}
	return nil
}

// Granted is the permissions installation id grants (GET /app/installations/{id}).
func (c *AppClient) Granted(ctx context.Context, id int64) (map[string]string, error) {
	ac, err := c.caller()
	if err != nil {
		return nil, err
	}
	appJWT, err := ac.sign(c.IDFile, c.KeyFile)
	if err != nil {
		return nil, err
	}
	var out struct {
		Permissions map[string]string `json:"permissions"`
	}
	if _, err := ac.call(ctx, http.MethodGet, "app/installations/"+strconv.FormatInt(id, 10), appJWT, nil, &out); err != nil {
		return nil, fmt.Errorf("read the installation's permissions: %w", err)
	}
	if out.Permissions == nil {
		out.Permissions = map[string]string{}
	}
	return out.Permissions, nil
}
