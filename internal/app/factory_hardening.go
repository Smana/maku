// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
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
	// Tokens is the publisher's token source; nil is octo-sts from hardening.publisher.octoSTS.
	Tokens applier.PublisherTokens
}

// safeOutputApplier is the applier for cfg (hardening H2, HR15): nil, and no publisher built, while
// no repository has an enforceOutputs list, in the demo profile (never hardened, R7), or without the
// hardening ports. Otherwise it answers enforced requests with T1 to T4 at enforce, as the agents
// App: under HR11 the publisher's octo-sts token is the agents App's, so its login is agentsLogin,
// the one the applier recognises its own pull requests and comments by. Turning enforcement on from
// none takes a restart, as audit does. The start is refused while the publisher's projected token
// cannot be read: every enforced request would otherwise wait unanswered for its run's whole
// answerWait.
func safeOutputApplier(cfg *config.Config, d applierDeps) (*applier.Applier, error) {
	if cfg.Demo || !cfg.Hardening.Enforcing() || d.Hardened == nil {
		return nil, nil
	}
	tokens := d.Tokens
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
	return &applier.Applier{Reader: d.Reader, Namespace: d.Namespace, Rooms: d.Rooms, Runs: d.Runs,
		Publisher: applier.ConnectPublishers(tokens, base), Factory: d.Factory,
		Checker: func(repo string) hardening.Checker {
			c, h := ports.Checker, d.Live().Hardening
			p, err := ports.Policies.For(repo, h.MinTrust())
			if err != nil {
				d.Log.Warn("hardening block unreadable: the applier checks the floor's protected paths", "repository", repo, "err", err)
			}
			c.Paths, c.LabelAllowlist = p.Paths, slices.Clone(h.LabelAllowlist)
			return c
		},
		Enforced:    func(repo string) []hardening.OutputType { return d.Live().Hardening.EnforcedFor(repo) },
		AgentsLogin: cfg.AgentsLogin, FactoryLogin: cfg.FactoryLogin,
		Interval: cfg.Hardening.Applier.Interval.Duration, AnswerWait: cfg.Hardening.Applier.AnswerWait.Duration,
		Now: time.Now, Metrics: d.Metrics, Log: d.Log}, nil
}

// policyReader reads a repository's default-branch file; *forge.GitHub implements it.
type policyReader interface {
	DefaultBranchFile(ctx context.Context, path string) (text, sha string, found bool, err error)
}

// sandboxPermissionObserver is a fence observer that, on each pass over a repository with an
// enforceOutputs list, reads each sandbox role's and the publisher's octo-sts policy and sets the
// permission gauge from them (H2, R2 as evidence): the role policy losing pull_requests: write shows
// there. A policy that cannot be read or does not parse is logged and keeps its last values.
func sandboxPermissionObserver(live func() *config.Config, files func(repo string) policyReader,
	record func(ctx context.Context, repo string, grants map[string]map[string]string), log *slog.Logger) fenceObserver {
	return func(ctx context.Context, repo string, _ []byte) {
		if len(live().Hardening.EnforcedFor(repo)) == 0 {
			return
		}
		f := files(repo)
		if f == nil {
			return
		}
		grants := map[string]map[string]string{}
		paths := map[string]string{applier.PublisherIdentity: hardening.PublisherPolicyPath}
		for _, role := range hardening.SandboxRoles() {
			paths[role] = hardening.RolePolicyPath(role)
		}
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
		record(ctx, repo, grants)
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
