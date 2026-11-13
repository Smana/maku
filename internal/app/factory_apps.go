// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/githubapps"
	"github.com/Smana/maku/internal/httpx"
)

// grantChecker is, per repository, what an App's installation lacks of the set it checks.
type grantChecker interface {
	CheckGranted(ctx context.Context, repos []string) (map[string]minter.Permissions, error)
}

// permissionGauge records whether app lacks permission on a served repository.
type permissionGauge func(ctx context.Context, app, permission string, missing bool)

// factoryAppNeed is the factory App's required set under cfg: its own, and the merger's when the
// merger signs with it (D12).
func factoryAppNeed(cfg *config.Config) minter.Permissions {
	return githubapps.Factory(githubapps.MergerOptions{Folded: cfg.MergerFolded(), Reruns: cfg.AnyCheckReruns(),
		Labels: cfg.AnyLive() || cfg.AnyMonitored(), Status: cfg.AnyGateStatus()})
}

// checkAppGrants reads app's installation on each repository and compares it with need (D12): a
// permission lacking anywhere is logged once, naming the repositories, and set on the gauge; the
// others read 0. Nothing is refused: a run's mint fails on its own with permission_not_granted.
// When no installation could be read, no gauge is set.
func checkAppGrants(ctx context.Context, app string, need minter.Permissions, chk grantChecker, repos []string, gauge permissionGauge, log *slog.Logger) {
	missing, err := chk.CheckGranted(ctx, repos)
	if err != nil {
		log.Warn("github app: an installation's permissions could not be read", "app", app, "err", err)
	}
	if len(missing) == 0 {
		return
	}
	lacking := map[string][]string{}
	for repo, m := range missing {
		for k := range m {
			lacking[k] = append(lacking[k], repo)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(need)) {
		where := lacking[k]
		if len(where) > 0 {
			slices.Sort(where)
			log.Warn("github app: the installation lacks a required permission", "app", app, "permission", k,
				"access", need[k], "repositories", where)
		}
		if gauge != nil {
			gauge(ctx, app, k, len(where) > 0)
		}
	}
}

// appGrantChecks is the startup check under runTokens.perRun (D12, plan Task 14): the agents App's
// installations against its required set (the role table's union), through agents (agentsMinter;
// nil skips it), and the factory App's against its own. It runs once, on the leader, over the
// repositories served at start.
func appGrantChecks(cfg *config.Config, agents *minter.Minter, served func() []string, gauge permissionGauge, log *slog.Logger) manager.Runnable {
	factory := &minter.Minter{Client: &forge.AppClient{API: githubAPIURL(), HTTP: httpx.New(githubTimeout, nil),
		IDFile: cfg.GitHub.AppIDFile, KeyFile: cfg.GitHub.PrivateKeyFile, Installations: &forge.Installations{}},
		Table: minter.Table{"factory": {Base: factoryAppNeed(cfg)}}, Log: log}
	return manager.RunnableFunc(func(ctx context.Context) error {
		repos := served()
		if agents != nil {
			checkAppGrants(ctx, "agents", githubapps.Agents(), agents, repos, gauge, log)
		}
		checkAppGrants(ctx, "factory", factoryAppNeed(cfg), factory, repos, gauge, log)
		return nil
	})
}
