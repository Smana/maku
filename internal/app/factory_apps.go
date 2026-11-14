// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"

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
// merger is folded into it (D12; appIdentities says).
func factoryAppNeed(cfg *config.Config, folded bool) minter.Permissions {
	return githubapps.Factory(githubapps.MergerOptions{Folded: folded, Reruns: cfg.AnyCheckReruns(),
		Labels: cfg.AnyLive() || cfg.AnyMonitored(), Status: cfg.AnyGateStatus()})
}

// appIdentities holds D12's two-App invariants on App ids, not file paths (review of #510): one
// Secret mounted under two paths is one App. read is readAppID. It answers whether the merger is
// the factory App, by path or by id; a merger whose key is not mounted is not compared. It refuses:
//   - under runTokens.perRun outside the demo, an agents App that is the factory App or the merger App: the merger is the
//     gate ruleset's bypass actor (#500), so a run token would merge past maku/merge-gate;
//   - a merger that is the factory App while a repository's merge.mergerLogin is not factoryLogin:
//     the gate would read the factory's own merges as a human's.
func appIdentities(cfg *config.Config, read func(string) (int64, error)) (folded bool, err error) {
	folded = cfg.MergerFolded()
	var factoryID, mergerID int64
	if cfg.GitHub.MergerAppIDFile != "" && !folded {
		if mergerID, err = read(cfg.GitHub.MergerAppIDFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("the merger App's id (%s): %w", cfg.GitHub.MergerAppIDFile, err)
		}
	}
	agentsFile, _, agentsOK := cfg.RunTokenApp()
	// Under perRun only, as the config's login and path rules; the same path is already the
	// config's refusal.
	agentsOK = agentsOK && cfg.RunTokens.PerRun && !cfg.Demo && agentsFile != cfg.GitHub.AppIDFile
	if mergerID > 0 || agentsOK {
		if factoryID, err = read(cfg.GitHub.AppIDFile); err != nil {
			return false, fmt.Errorf("the factory App's id (%s): %w", cfg.GitHub.AppIDFile, err)
		}
	}
	if mergerID > 0 && mergerID == factoryID {
		folded = true
		for _, r := range cfg.Repos() {
			if !strings.EqualFold(r.Merge.MergerLogin, cfg.FactoryLogin) {
				return false, fmt.Errorf("%s: merge.mergerLogin must be factoryLogin when the merger signs with the factory App (app id %d): %q is not %q",
					r.Name, factoryID, r.Merge.MergerLogin, cfg.FactoryLogin)
			}
		}
	}
	if !agentsOK {
		return folded, nil
	}
	agentsID, err := read(agentsFile)
	if err != nil {
		return false, fmt.Errorf("the agents App's id (%s): %w", agentsFile, err)
	}
	switch agentsID {
	case factoryID:
		return false, fmt.Errorf("the agents App is the factory App (app id %d): D12 needs two Apps, or a run token carries the factory's permissions", agentsID)
	case mergerID:
		return false, fmt.Errorf("the agents App is the merger App (app id %d): the merger bypasses the merge gate's ruleset, so a run token could merge past maku/merge-gate", agentsID)
	}
	return folded, nil
}

// checkAppGrants reads app's installation on each repository and compares it with need (D12): a
// permission lacking anywhere is logged once, naming the repositories, and set on the gauge.
// Nothing is refused: a run's mint fails on its own with permission_not_granted. The others read 0
// only when every installation was read: a 0 claims the grant on every served repository.
func checkAppGrants(ctx context.Context, app string, need minter.Permissions, chk grantChecker, repos []string, gauge permissionGauge, log *slog.Logger) {
	missing, err := chk.CheckGranted(ctx, repos)
	if err != nil {
		log.Warn("github app: an installation's permissions could not be read", "app", app, "err", err)
	}
	if len(missing) == 0 {
		return
	}
	complete := err == nil
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
		if gauge != nil && (len(where) > 0 || complete) {
			gauge(ctx, app, k, len(where) > 0)
		}
	}
}

// appGrantChecks is the startup check under runTokens.perRun (D12, plan Task 14): the agents App's
// installations against its required set (the role table's union), through agents (agentsMinter;
// nil skips it), and the factory App's against its own, the merger's included when folded. It runs
// once, when this replica becomes leader, over the repositories served then.
func appGrantChecks(cfg *config.Config, folded bool, agents *minter.Minter, served func() []string, gauge permissionGauge, log *slog.Logger) manager.Runnable {
	need := factoryAppNeed(cfg, folded)
	factory := factoryMinter(cfg, need, log)
	return manager.RunnableFunc(func(ctx context.Context) error {
		repos := served()
		if agents != nil {
			checkAppGrants(ctx, "agents", githubapps.Agents(), agents, repos, gauge, log)
		}
		checkAppGrants(ctx, "factory", need, factory, repos, gauge, log)
		return nil
	})
}

// factoryMinter reads the factory App's installations against need: its grants, never a token.
func factoryMinter(cfg *config.Config, need minter.Permissions, log *slog.Logger) *minter.Minter {
	return &minter.Minter{Client: &forge.AppClient{API: githubAPIURL(), HTTP: httpx.New(githubTimeout, nil),
		IDFile: cfg.GitHub.AppIDFile, KeyFile: cfg.GitHub.PrivateKeyFile, Installations: &forge.Installations{}},
		Table: minter.Table{"factory": {Base: need}}, Log: log}
}
