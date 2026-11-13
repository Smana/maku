// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/version"
)

// applierDeps are the factory's parts the safe-output applier is built from.
type applierDeps struct {
	Reader    client.Reader // Tasks and the kill switch
	Namespace string
	Rooms     applier.Rooms
	Runs      applier.Runs
	Factory   func(repo string) applier.Filer // the factory App's connection, nil when it has none
	Hardened  *reconciler.HardeningPorts      // the checker and each repository's protected paths
	Live      func() *config.Config           // the published config: enforceOutputs and the allowlist per call
	Metrics   applier.Metrics
	Log       *slog.Logger
	// Tokens is the publisher's token source; nil is the minter's with hardening.publisher.minter,
	// else octo-sts from hardening.publisher.octoSTS.
	Tokens applier.PublisherTokens
	// PushRooms is the broker's bundle and approval routes, and Tasks the writer a push's intent is
	// recorded with (H3); both are used only while a repository enforces push.
	PushRooms applier.PushRooms
	Tasks     client.Writer
	// Getenv reads MAKU_GIT_CACHE, the push applier's mirror root (os.Getenv when nil).
	Getenv func(string) string
	// Minter is the agents App's minter the publisher mints from; nil builds one from cfg.
	Minter *minter.Minter
}

// safeOutputApplier is the applier for cfg (hardening H2, HR15): nil, and no publisher built, while
// no repository has an enforceOutputs list, in the demo profile (never hardened, R7), or without the
// hardening ports. Otherwise it answers enforced requests with T1 to T4 at enforce, as the agents
// App: under HR11 the publisher's octo-sts token is the agents App's, so its login is agentsLogin,
// the one the applier recognises its own pull requests and comments by. With
// hardening.publisher.minter the token is minted from the agents App instead (v4 D10, plan PR7):
// the same App, so the same login. Turning enforcement on from none takes a restart, as audit does.
// Under octo-sts the start is refused while the publisher's projected token cannot be read: every
// enforced request would otherwise wait unanswered for its run's whole answerWait.
func safeOutputApplier(cfg *config.Config, d applierDeps) (*applier.Applier, error) {
	if cfg.Demo || !cfg.Hardening.Enforcing() || d.Hardened == nil {
		return nil, nil
	}
	tokens := d.Tokens
	if tokens == nil && cfg.Hardening.Publisher.Minter {
		mt := d.Minter
		if mt == nil {
			mt = agentsMinter(cfg, nil, d.Log)
		}
		if mt == nil {
			return nil, errors.New("hardening.publisher.minter needs runTokens.enabled: no agents App to mint the publisher's token from")
		}
		tokens = &minter.PublisherTokens{Minter: mt, Now: time.Now,
			Enforced: func(repo string) []hardening.OutputType { return d.Live().Hardening.EnforcedFor(repo) }}
	}
	if tokens == nil {
		p := cfg.Hardening.Publisher.OctoSTS
		if _, err := os.ReadFile(filepath.Clean(p.TokenFile)); err != nil {
			return nil, fmt.Errorf("hardening.enforceOutputs is set but the publisher token %s cannot be read: "+
				"set the chart's hardening.publisher.audience, or empty enforceOutputs: %w", p.TokenFile, err)
		}
		tokens = &applier.OctoSTSTokens{URL: p.URL, TokenFile: p.TokenFile, Identity: p.Identity,
			HTTP: httpx.New(githubTimeout, nil), Now: time.Now, Log: d.Log}
	}
	base := forge.Options{APIURL: githubAPIURL(), UserAgent: "agent-factory-publisher/" + version.Version,
		HTTP: httpx.New(githubTimeout, nil), Now: time.Now}
	ports := d.Hardened
	a := &applier.Applier{Reader: d.Reader, Namespace: d.Namespace, Rooms: d.Rooms, Runs: d.Runs,
		Publisher: applier.ConnectPublishers(tokens, base), Factory: d.Factory,
		Checker: func(repo string) hardening.Checker {
			c, h := ports.Checker, d.Live().Hardening
			p, err := ports.Policies.For(repo, h.MinTrust())
			if err != nil {
				d.Log.Warn("hardening block unreadable: the applier checks the floor's protected paths", "repository", repo, "err", err)
			}
			c.Paths, c.LabelAllowlist = p.Paths, slices.Clone(h.LabelAllowlist)
			c.ClassifierEnforce = h.ClassifierEnforceFor(repo) // HR40: T5 promoted per output type, read live
			return c
		},
		Enforced:    func(repo string) []hardening.OutputType { return d.Live().Hardening.EnforcedFor(repo) },
		AgentsLogin: cfg.AgentsLogin, FactoryLogin: cfg.FactoryLogin,
		Interval: cfg.Hardening.Applier.Interval.Duration, AnswerWait: cfg.Hardening.Applier.AnswerWait.Duration,
		Now: time.Now, Metrics: d.Metrics, Log: d.Log,
		// HR39, #520's review: a T5 call counts as a drill's only on a real drill task.
		Drill: func(repo string) bool { return d.Live().DrillTask(repo) }}
	if err := wirePush(cfg, d, tokens, a); err != nil {
		return nil, err
	}
	return a, nil
}

// pushCaps are the task's class caps on a push (HR25). A class the repository does not declare
// never means no cap (#513's review): each dimension, files and lines alone, takes the strictest
// cap any declared class sets for it, and -1 when no class caps that dimension. The applier
// refuses a push push_size when either dimension is -1, so a dimension no class caps refuses it.
func pushCaps(cfg *config.Config, t *v1alpha1.Task) (files, lines int) {
	r := cfg.RepositoryFor(t.Spec.Repository)
	if cl, ok := r.Class(t.Spec.PredictedClass); ok {
		return cl.MaxPatchFiles, cl.MaxModifiedLines
	}
	files, lines = -1, -1
	if r == nil {
		return files, lines
	}
	strictest := func(cur, v int) int {
		if v > 0 && (cur < 0 || v < cur) {
			return v
		}
		return cur
	}
	for _, cl := range r.Classes {
		files, lines = strictest(files, cl.MaxPatchFiles), strictest(lines, cl.MaxModifiedLines)
	}
	return files, lines
}

// gitCacheEnv is the factory's mirror root, which the chart sets with hardening.gitCache.sizeLimit.
const gitCacheEnv = "MAKU_GIT_CACHE"

// wirePush gives the applier its push half (H3, HR24 to HR27) while any repository enforces push:
// a gitbundle mirror per repository under MAKU_GIT_CACHE, git sending the publisher's token; the
// broker's bundle and approval routes; the task writer that records a push's intent before git
// push; the class caps on the push's cumulative change; and the approval TTL. A repository that
// enforces push with no MAKU_GIT_CACHE, no broker or no task writer refuses the start: every push
// would otherwise wait unanswered for its run's whole answerWait. Without push nothing is built.
func wirePush(cfg *config.Config, d applierDeps, tokens applier.PublisherTokens, a *applier.Applier) error {
	if !cfg.Hardening.EnforcesPush() {
		return nil
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	root := getenv(gitCacheEnv)
	switch {
	case root == "" || !filepath.IsAbs(root):
		return fmt.Errorf("a repository enforces push but %s is not an absolute path: set the chart's hardening.gitCache.sizeLimit, "+
			"or take push out of enforceOutputs", gitCacheEnv)
	case d.PushRooms == nil:
		return errors.New("a repository enforces push but the factory has no broker system client: set broker.url and the system token")
	case d.Tasks == nil:
		return errors.New("a repository enforces push but the applier has no task writer")
	}
	a.Pushers = applier.ConnectPushers(tokens, root, cfg.Hardening.Applier.MirrorMaxBytes)
	a.PushRooms, a.Tasks = d.PushRooms, d.Tasks
	a.ApprovalTTL = cfg.Hardening.Applier.ApprovalTTL.Duration
	a.Caps = func(t *v1alpha1.Task) (files, lines int) { return pushCaps(d.Live(), t) }
	return nil
}

// policyReader reads a repository's default-branch file; *forge.GitHub implements it.
type policyReader interface {
	DefaultBranchFile(ctx context.Context, path string) (text, sha string, found bool, err error)
}

// tokenGrants is readiness's and the gauge's Grants under the minter (plan PR8, HR42 option A):
// for a repository, each sandbox role's and the publisher's permissions from table for the
// repository's enforced outputs, read live: what the minter mints for its runs now.
type tokenGrants = func(repo string) (roles map[string]map[string]string, publisher map[string]string)

// tableGrants is tokenGrants from table (minter.Roles in the factory).
func tableGrants(live func() *config.Config, table minter.Table) tokenGrants {
	return func(repo string) (map[string]map[string]string, map[string]string) {
		enforced := live().Hardening.EnforcedFor(repo)
		roles := map[string]map[string]string{}
		for _, role := range hardening.SandboxRoles() {
			if p, err := table.For(role, enforced); err == nil {
				roles[role] = p
			}
		}
		pub, err := table.For(minter.Publisher, enforced)
		if err != nil {
			pub = nil
		}
		return roles, pub
	}
}

// roleTableWarning is HR13's warning under per-run tokens (plan PR13): the enforced repositories in
// which table leaves the implementer pull_requests: write although the factory publishes its pull
// requests, comments or labels (D10 as written), and those enforcing push in which it leaves the
// implementer or the tester contents: write (H3), so a run could still write them directly. Empty
// under any other token source, or a table that takes the write away (HR12, the owner's ruling). It
// lives here, not in the config's warnings, because the config cannot import the minter's table.
func roleTableWarning(cfg *config.Config, table minter.Table) string {
	if cfg.TokenSource() != hardening.TokenSourceMinter {
		return ""
	}
	var prs, pushes []string
	for _, r := range cfg.Repos() {
		enforced := cfg.Hardening.EnforcedFor(r.Name)
		if slices.ContainsFunc(enforced, func(t hardening.OutputType) bool {
			return t == hardening.PR || t == hardening.Comment || t == hardening.Labels
		}) {
			if p, err := table.For(minter.Implementer, enforced); err != nil || p["pull_requests"] == "write" {
				prs = append(prs, r.Name)
			}
		}
		// H3: with push enforced, neither writer role may keep contents: write (#509's note).
		if slices.Contains(enforced, hardening.Push) {
			for _, role := range []string{minter.Implementer, minter.Tester} {
				if p, err := table.For(role, enforced); err != nil || p["contents"] == "write" {
					pushes = append(pushes, r.Name+" ("+role+")")
				}
			}
		}
	}
	var out []string
	if len(prs) > 0 {
		out = append(out, fmt.Sprintf("runTokens.perRun: the role table leaves the implementer pull_requests: write in %s although the factory publishes "+
			"its pull requests, comments or labels, so that enforcement is advisory; keep these repositories on the XR with octo-sts (HR13, D10 as written)",
			strings.Join(prs, ", ")))
	}
	if len(pushes) > 0 {
		out = append(out, fmt.Sprintf("runTokens.perRun: the role table leaves contents: write in %s although the factory publishes "+
			"its pushes, so push enforcement is advisory; keep these repositories on the XR with octo-sts (HR13, D10 as written)",
			strings.Join(pushes, ", ")))
	}
	return strings.Join(out, "; ")
}

// sandboxPermissionObserver is a fence observer that, on each pass over a repository with an
// enforceOutputs list, sets the permission gauge from the token source the runs and the publisher
// actually get (H2, R2 as evidence; plan PR8): on the XR, each sandbox role's octo-sts policy; under
// runTokens.perRun, the minter's role table through grants; and the publisher's policy, or the
// table's publisher row with hardening.publisher.minter. A shared token's roles are not recorded:
// HR13 refuses enforcement on one. A policy that cannot be read or does not parse is logged and
// keeps its last values.
func sandboxPermissionObserver(live func() *config.Config, files func(repo string) policyReader, grants tokenGrants,
	record func(ctx context.Context, repo string, grants map[string]map[string]string), log *slog.Logger) fenceObserver {
	return func(ctx context.Context, repo string, _ []byte) {
		cfg := live()
		if len(cfg.Hardening.EnforcedFor(repo)) == 0 {
			return
		}
		src, pubMinter := cfg.TokenSource(), cfg.Hardening.Publisher.Minter
		out := map[string]map[string]string{}
		if grants != nil && (src == hardening.TokenSourceMinter || pubMinter) {
			roles, pub := grants(repo)
			if src == hardening.TokenSourceMinter {
				maps.Copy(out, roles)
			}
			if pubMinter {
				out[applier.PublisherIdentity] = pub
			}
		}
		paths := map[string]string{}
		if !pubMinter {
			paths[applier.PublisherIdentity] = hardening.PublisherPolicyPath
		}
		if src == hardening.TokenSourceXR {
			for _, role := range hardening.SandboxRoles() {
				paths[role] = hardening.RolePolicyPath(role)
			}
		}
		if len(paths) > 0 {
			f := files(repo)
			if f == nil {
				return
			}
			readPolicies(ctx, f, repo, paths, out, log)
		}
		record(ctx, repo, out)
	}
}

// readPolicies reads each role's octo-sts policy at paths into grants.
func readPolicies(ctx context.Context, f policyReader, repo string, paths map[string]string, grants map[string]map[string]string, log *slog.Logger) {
	for role, path := range paths {
		text, _, found, err := f.DefaultBranchFile(ctx, path)
		switch {
		case err != nil:
			log.Warn("octo-sts policy unreadable: its permissions keep their last values", "repository", repo, "role", role, "err", err)
			continue
		case !found:
			grants[role] = map[string]string{} // no policy, no token
			continue
		}
		perms, err := hardening.PolicyPermissions([]byte(text))
		if err != nil {
			log.Warn("octo-sts policy does not parse: its permissions keep their last values", "repository", repo, "role", role, "err", err)
			continue
		}
		grants[role] = perms
	}
}

// hardeningPorts are the reconciler's hardening ports for cfg (stage H1): nil while hardening is
// off centrally and for every repository, so nothing is planted, recorded or shadowed. Otherwise a
// Checker with every layer at audit (T5 at its effective mode: off without an agent-guard URL),
// secrets as T3's scanner, and an empty RepositoryPolicies the fence's OnFile feeds. The ports are
// built once at startup: turning audit on from off takes a restart. The demo profile is never
// hardened (R7), so it gets none either, whatever its hardening block says.
func hardeningPorts(cfg *config.Config, secrets hardening.SecretScanner) *reconciler.HardeningPorts {
	h := cfg.Hardening
	if cfg.Demo || !hardeningOn(h) {
		return nil
	}
	c := hardening.Checker{Layers: hardening.LayersFor(hardening.ModeAudit, h.EffectiveClassifier()), Secrets: secrets,
		LabelAllowlist: slices.Clone(h.LabelAllowlist)}
	if h.AgentGuard.URL != "" {
		c.Classifier = hardening.WebhookClassifier{URL: h.AgentGuard.URL, HC: httpx.New(h.AgentGuard.Timeout.Duration, nil)}
	}
	return &reconciler.HardeningPorts{Policies: &hardening.RepositoryPolicies{}, Checker: c}
}

// hardeningOn reports whether any repository runs at least audit: centrally, or one stricter.
func hardeningOn(h config.Hardening) bool {
	if h.Mode.AtLeast(hardening.ModeAudit) {
		return true
	}
	for _, r := range h.Repositories {
		if r.Mode.AtLeast(hardening.ModeAudit) {
			return true
		}
	}
	return false
}

// fenceObserver is the fence's OnFile.
type fenceObserver = func(ctx context.Context, repository string, body []byte)

// chainOnFile is one OnFile that hands each fence file to every non-nil observer in order; nil
// when there is none.
func chainOnFile(observers ...fenceObserver) fenceObserver {
	var fs []fenceObserver
	for _, f := range observers {
		if f != nil {
			fs = append(fs, f)
		}
	}
	switch len(fs) {
	case 0:
		return nil
	case 1:
		return fs[0]
	}
	return func(ctx context.Context, repository string, body []byte) {
		for _, f := range fs {
			f(ctx, repository, body)
		}
	}
}
