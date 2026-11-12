// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
	// ErrPermissionNotGranted is a 422 from the mint: the installation lacks a permission asked, or
	// the repository is not among the installation's.
	ErrPermissionNotGranted = errors.New("permission or repository not granted")
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
	if err := CheckNames(owner, name); err != nil {
		return 0, err
	}
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

// revokeTimeout bounds the revoke of a token minted with the wrong scope: it runs even when the
// caller's context is done, so it needs its own bound.
const revokeTimeout = 10 * time.Second

// AccessToken mints a token of installation id on exactly owner's repos (names, no owner) with
// perms, and checks GitHub's reply against the request:
//   - every granted permission was asked at the same level, except the metadata: read GitHub adds;
//   - every asked permission was granted;
//   - the reply's repositories are exactly owner/<repo> by full name, case-insensitive, so a
//     repository transferred to another owner under the same name never matches.
//
// A reply that fails the check is revoked and the call fails with ErrScopeMismatch, joined with
// ErrRevokeFailed when the revoke failed too. It returns the token with what the reply granted and
// the repositories' full names. A 422 is ErrPermissionNotGranted, a 404 ErrNotInstalled.
func (c *AppClient) AccessToken(ctx context.Context, id int64, owner string, repos []string, perms map[string]string) (Token, map[string]string, []string, error) {
	if len(repos) == 0 {
		return Token{}, nil, nil, errors.New("forge: a mint needs at least one repository")
	}
	for _, r := range repos {
		if err := CheckNames(owner, r); err != nil {
			return Token{}, nil, nil, err
		}
	}
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
	if !scopeMatches(r.granted, perms) || !sameRepos(r.repos, owner, repos) {
		// The token exists until revoked: the revoke outlives the caller's context, bounded.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
		defer cancel()
		if rerr := c.revoke(rctx, ac, r.token); rerr != nil {
			return Token{}, nil, nil, fmt.Errorf("mint an installation token: %w; revoking it: %w", ErrScopeMismatch, rerr)
		}
		return Token{}, nil, nil, fmt.Errorf("mint an installation token: %w; the token was revoked", ErrScopeMismatch)
	}
	return Token{Value: r.token, ExpiresAt: r.expires}, r.granted, r.repos, nil
}

// scopeMatches is AccessToken's permission check of the reply against the request.
func scopeMatches(granted, asked map[string]string) bool {
	for k, v := range granted {
		a, ok := asked[k]
		switch {
		case ok && a != v:
			return false
		case !ok && (k != "metadata" || v != "read"):
			return false
		}
	}
	for k := range asked {
		if _, ok := granted[k]; !ok {
			return false
		}
	}
	return true
}

// sameRepos is whether fullNames are exactly owner's names, ignoring order and case.
func sameRepos(fullNames []string, owner string, names []string) bool {
	want := make([]string, len(names))
	for i, n := range names {
		want[i] = strings.ToLower(owner + "/" + n)
	}
	got := make([]string, len(fullNames))
	for i, n := range fullNames {
		got[i] = strings.ToLower(n)
	}
	slices.Sort(want)
	slices.Sort(got)
	return slices.Equal(got, want)
}

// nameRE is an owner or repository name as a path segment may carry it.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// CheckNames refuses an owner or repository name that could change the path it is put in:
// anything outside [A-Za-z0-9_.-], ".", or a name containing "..".
func CheckNames(names ...string) error {
	for _, n := range names {
		if !nameRE.MatchString(n) || n == "." || strings.Contains(n, "..") {
			return fmt.Errorf("forge: %q is not an owner or repository name", n)
		}
	}
	return nil
}

// Revoke revokes an installation token (DELETE /installation/token, as the token itself). A token
// GitHub no longer knows (401) is already revoked: nil. A failure is a *RevokeError.
func (c *AppClient) Revoke(ctx context.Context, token string) error {
	ac, err := c.caller()
	if err != nil {
		return &RevokeError{}
	}
	return c.revoke(ctx, ac, token)
}

// ErrRevokeFailed is a revoke that did not happen: the token lives until it expires.
var ErrRevokeFailed = errors.New("revoke an installation token failed")

// RevokeError is a failed revoke, carried by its HTTP status alone (0: no answer), so it can be
// passed on and logged whole: it never holds the token or a reply body.
type RevokeError struct{ Status int }

func (e *RevokeError) Error() string {
	if e.Status == 0 {
		return ErrRevokeFailed.Error()
	}
	return fmt.Sprintf("%s: status %d", ErrRevokeFailed, e.Status)
}

// Is makes errors.Is(err, ErrRevokeFailed) true.
func (e *RevokeError) Is(target error) bool { return target == ErrRevokeFailed }

func (c *AppClient) revoke(ctx context.Context, ac appCaller, token string) error {
	status, err := ac.call(ctx, http.MethodDelete, "installation/token", token, nil, nil)
	if status == http.StatusUnauthorized {
		return nil
	}
	if err != nil {
		return &RevokeError{Status: status}
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
