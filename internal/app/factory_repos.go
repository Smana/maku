// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/version"
)

// repoConn is one repository's two GitHub connections (v2 D8): the factory App's and the merger's.
type repoConn struct {
	gh     *forge.GitHub
	merger forge.Merger
	// scope is what the merger connection was made for: a change reconnects it.
	scope mergerScope
}

// mergerScope is what a merger connection depends on: whether the repository needs the merger
// (a missing key fails it then), and the token's permission bits, which every repository shares.
type mergerScope struct{ needs, rerun, label, status bool }

func mergerScopeFor(cfg *config.Config, repository string) mergerScope {
	return mergerScope{needs: cfg.RepositoryFor(repository).NeedsMerger(), rerun: cfg.AnyCheckReruns(),
		label: cfg.AnyLive() || cfg.AnyMonitored(), status: cfg.AnyGateStatus()}
}

// repoConns holds a connection pair per served repository, keyed by GitHub's case-insensitive
// name. Every lookup that misses answers "no connection", so nothing acts on an unlisted one. The
// set follows the config's publishes (factory resources D12): sync wires what a publish adds.
type repoConns struct {
	log *slog.Logger
	// connectGH and connectMerger make a repository's two connections.
	connectGH     func(ctx context.Context, cfg *config.Config, repository string) (*forge.GitHub, error)
	connectMerger func(ctx context.Context, cfg *config.Config, repository string) (forge.Merger, error)
	// failed hears each repository a sync could not wire; nil hears nothing.
	failed func(ctx context.Context, repository string)

	syncMu sync.Mutex // serializes sync and retry

	mu    sync.RWMutex
	order []string
	by    map[string]repoConn
	// errs is each listed repository's last wiring error, keyed lowercased; a sync that wires it,
	// or drops it, clears it. Any entry makes the next retry run.
	errs map[string]error
}

// newRepoConns connects through GitHub: the factory App, and the merger App (connectMerger). In
// demo mode a repository must be public to be wired.
func newRepoConns(log *slog.Logger) *repoConns {
	c := &repoConns{log: log, by: map[string]repoConn{}}
	c.connectGH = func(ctx context.Context, cfg *config.Config, repository string) (*forge.GitHub, error) {
		gh, err := forge.Connect(ctx, forge.Options{Repository: repository, AppIDFile: cfg.GitHub.AppIDFile,
			PrivateKeyFile: cfg.GitHub.PrivateKeyFile, APIURL: githubAPIURL(), UserAgent: "agent-factory/" + version.Version,
			HTTP: httpx.New(githubTimeout, nil), Now: time.Now})
		if err != nil {
			return gh, err
		}
		return gh, demoPublic(ctx, cfg, repository, gh.Public)
	}
	connect := c.connectGH
	c.connectGH = func(ctx context.Context, cfg *config.Config, repository string) (*forge.GitHub, error) {
		if cfg.Demo { // one App installation serves one account: demoGitToken mints for one owner
			if _, _, err := oneOwner([]string{cfg.Primary(), repository}); err != nil {
				return nil, err
			}
		}
		return connect(ctx, cfg, repository)
	}
	c.connectMerger = func(ctx context.Context, cfg *config.Config, repository string) (forge.Merger, error) {
		return connectMerger(ctx, log, cfg, repository)
	}
	return c
}

// sync makes the connections cfg's repositories: it connects each new one, keeps the
// connections of those it had (reconnecting a merger whose scope moved), and drops the removed
// ones. A new repository whose installation is missing, or any whose class needs a merger key
// that is not mounted, is left out, never silently served (v2 D8). A served repository whose
// merger reconnect fails otherwise keeps the connection it had, and the reconnect is retried.
// Each failure is named in the error and heard by failed; at startup the error fails the start,
// on a publish it is logged.
func (c *repoConns) sync(ctx context.Context, cfg *config.Config) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	return c.syncLocked(ctx, cfg)
}

// retry syncs to cur() again while the last sync failed a repository; ran reports whether it did.
// cur is read under the sync lock, so a retry never wires an older config than a publish did.
func (c *repoConns) retry(ctx context.Context, cur func() *config.Config) (ran bool, err error) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.mu.RLock()
	pending := len(c.errs) > 0
	c.mu.RUnlock()
	if !pending {
		return false, nil
	}
	return true, c.syncLocked(ctx, cur())
}

func (c *repoConns) syncLocked(ctx context.Context, cfg *config.Config) error {
	c.mu.RLock()
	had := maps.Clone(c.by)
	c.mu.RUnlock()
	by := map[string]repoConn{}
	var order []string
	var errs []error
	failed := map[string]error{}
	fail := func(name string, err error) {
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		failed[strings.ToLower(name)] = err
		if c.failed != nil {
			c.failed(ctx, name)
		}
	}
	for _, r := range cfg.Repos() {
		key := strings.ToLower(r.Name)
		if _, dup := by[key]; dup {
			continue
		}
		rc, ok := had[key]
		if !ok {
			gh, err := c.connectGH(ctx, cfg, r.Name)
			if err != nil {
				fail(r.Name, err)
				continue
			}
			rc.gh = gh
		}
		if scope := mergerScopeFor(cfg, r.Name); !ok || rc.scope != scope {
			// R16 (owner, 2026-09-27): merge-side calls use the merger App's key, which no other
			// workload holds. TW4: its token asks the merger profile, not the factory App's scope.
			merger, err := c.connectMerger(ctx, cfg, r.Name)
			switch {
			case err == nil:
				rc.merger, rc.scope = merger, scope
			case ok && !errors.Is(err, errMergerKeyMissing):
				fail(r.Name, err) // served: keeps its connections, the old scope retries the merger
			default:
				fail(r.Name, err)
				continue
			}
		}
		by[key] = rc
		order = append(order, r.Name)
	}
	c.mu.Lock()
	c.by, c.order, c.errs = by, order, failed
	c.mu.Unlock()
	return errors.Join(errs...)
}

// wireErr is repository's last wiring error, nil when its last sync wired it or it is not listed.
// Plan 3's Connected condition reads it.
func (c *repoConns) wireErr(repository string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.errs[strings.ToLower(repository)]
}

// names are the served repositories, in the config's order.
func (c *repoConns) names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.order)
}

func (c *repoConns) get(name string) (repoConn, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rc, ok := c.by[strings.ToLower(name)]
	return rc, ok
}

// all is every served connection, a snapshot.
func (c *repoConns) all() []repoConn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]repoConn, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.by[strings.ToLower(name)])
	}
	return out
}

func (c *repoConns) task(name string) (reconciler.TaskForge, forge.Merger, bool) {
	rc, ok := c.get(name)
	return rc.gh, rc.merger, ok
}

func (c *repoConns) class(name string) (triage.ClassForge, bool) {
	rc, ok := c.get(name)
	return rc.gh, ok
}

func (c *repoConns) schedule(name string) (intake.ScheduleForge, intake.ScheduleMerger, bool) {
	rc, ok := c.get(name)
	return rc.gh, rc.merger, ok
}

// fenceFile reads repository's fence file through its factory App (contents: read).
func (c *repoConns) fenceFile(ctx context.Context, repository string) ([]byte, bool, error) {
	rc, ok := c.get(repository)
	if !ok {
		return nil, false, fmt.Errorf("no GitHub connection for %s", repository)
	}
	return rc.gh.FenceFile(ctx)
}

// ping keeps every installation token warm; the first failure is the answer.
func (c *repoConns) ping(ctx context.Context) error {
	var errs []error
	for _, rc := range c.all() {
		errs = append(errs, rc.gh.Ping(ctx))
	}
	return errors.Join(errs...)
}

// healthy: every repository's connection answered recently.
func (c *repoConns) healthy(now time.Time) bool {
	for _, rc := range c.all() {
		if !rc.gh.Healthy(now) {
			return false
		}
	}
	return true
}

// permissions is GitHub's collaborator permission through the factory App on the repository asked
// about (v2 D4, D5), cached for api.access.ttl or the most D4 trusts. A repository with no
// connection is one nobody writes through the factory.
func (c *repoConns) permissions(access *config.Access) *repoaccess.Checker {
	ttl := config.MaxAccessTTL
	if access != nil {
		ttl = access.TTL.Duration
	}
	return &repoaccess.Checker{TTL: ttl, Now: time.Now,
		Perm: func(ctx context.Context, owner, repo, login string) (string, error) {
			rc, ok := c.get(owner + "/" + repo)
			if !ok {
				return "", repoaccess.ErrNoRepository
			}
			return rc.gh.Permission(ctx, login)
		}}
}

// access is v2 D4's check for the run-request API: the caller's GitHub login, then write on the
// repository. The login comes from the broker's GitHub account links (api.access.links: broker,
// through links), the only identity source (v4 D8). Without api.access it is nil, and the API refuses
// every human run.
func (c *repoConns) access(log *slog.Logger, cfg *config.Config, perms *repoaccess.Checker, links brokerGitHubLinks) api.WriteChecker {
	a := cfg.API.Access
	if a == nil {
		log.Warn("api.access is not set: the run-request API refuses every human run (repository_permission, v2 D4)")
		return nil
	}
	id := &ghidentity.Resolver{IDPID: githubIDP, TTL: a.TTL.Duration, Now: time.Now,
		LoginOf: func(ctx context.Context, repo string, gid int64) (string, error) {
			rc, ok := c.get(repo)
			if !ok {
				return "", repoaccess.ErrNoRepository
			}
			return rc.gh.UserLogin(ctx, gid)
		}}
	id.Links, id.Active = factoryLinks(links)
	return api.GitHubAccess{Identity: id, Perms: perms}
}

// brokerGitHubLinks is the broker's GET /v1/links/github; *rooms.Client has it.
type brokerGitHubLinks interface {
	GitHubLink(ctx context.Context, sub string) (rooms.GitHubLink, bool, error)
}

// factoryLinks is the resolver's source from api.access: the broker's GitHub account links. The
// broker answers whether a link is active (its activeWindow), so one window rules the broker and the
// factory. A sub with no link resolves to no login and is active: it has no GitHub identity to act
// with. A nil links (no broker) resolves no login for anyone.
func factoryLinks(links brokerGitHubLinks) (func(context.Context, string) ([]ghidentity.Link, error), func(context.Context, string) (bool, error)) {
	if links == nil {
		return func(context.Context, string) ([]ghidentity.Link, error) { return nil, nil }, nil
	}
	brokerLinks := func(ctx context.Context, sub string) ([]ghidentity.Link, error) {
		l, found, err := links.GitHubLink(ctx, sub)
		if err != nil || !found {
			return nil, err
		}
		return []ghidentity.Link{{IDPID: githubIDP, UserID: strconv.FormatInt(l.GitHubID, 10)}}, nil
	}
	active := func(ctx context.Context, sub string) (bool, error) {
		l, found, err := links.GitHubLink(ctx, sub)
		if err != nil {
			return false, err
		}
		return !found || l.Active, nil
	}
	return brokerLinks, active
}
