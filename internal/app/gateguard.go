// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/reconciler"
)

// rulesReader reads a repository's default branch and the status checks its rulesets require
// there; *forge.GitHub implements it with the factory App's token (metadata read).
type rulesReader interface {
	RepositoryInfo(ctx context.Context) (forge.RepositoryInfo, error)
	BranchRules(ctx context.Context, branch string) ([]forge.RequiredStatus, error)
}

// GateRequired reports whether rules require maku/merge-gate from the App appID. A requirement
// without an integration id, or with another App's, lets a status anyone else posts count, so it
// is no requirement (D11).
func GateRequired(rules []forge.RequiredStatus, appID int64) bool {
	if appID < 1 {
		return false
	}
	for _, r := range rules {
		if r.Context == reconciler.GateStatusContext && r.IntegrationID == appID {
			return true
		}
	}
	return false
}

// gateGuardObserver is a fence observer that, on each pass over a repository with
// merge.gateStatus, reads whether its default branch's rulesets require maku/merge-gate from the
// App that posts it: the merger App, whose id file names the factory App's when the merger is
// folded into it. It records the answer, and warns once each time the requirement goes. It never
// refuses: the factory merges by its own decision, and the rule guards against everyone else. A
// read that fails keeps the last value and is counted through unread, so a value gone stale shows.
func gateGuardObserver(live func() *config.Config, readers func(repo string) rulesReader,
	record func(ctx context.Context, repo string, required bool), unread func(ctx context.Context, repo string), log *slog.Logger) fenceObserver {
	var mu sync.Mutex
	last := map[string]bool{} // lowercased repository → required, as last recorded
	return func(ctx context.Context, repo string, _ []byte) {
		cfg := live()
		r := cfg.RepositoryFor(repo)
		if r == nil || !r.Merge.PostsGateStatus() {
			return
		}
		rr := readers(repo)
		if rr == nil {
			return
		}
		appID, err := readAppID(cfg.GitHub.MergerAppIDFile)
		if err != nil {
			log.Warn("the merge gate's App id is unreadable: whether branch protection requires maku/merge-gate keeps its last value", "repository", repo, "err", err)
			unread(ctx, repo)
			return
		}
		info, err := rr.RepositoryInfo(ctx)
		if err != nil {
			log.Warn("the default branch is unreadable: whether branch protection requires maku/merge-gate keeps its last value", "repository", repo, "err", err)
			unread(ctx, repo)
			return
		}
		rules, err := rr.BranchRules(ctx, info.DefaultBranch)
		if err != nil {
			log.Warn("the branch rules are unreadable: whether branch protection requires maku/merge-gate keeps its last value", "repository", repo, "err", err)
			unread(ctx, repo)
			return
		}
		required := GateRequired(rules, appID)
		record(ctx, repo, required)
		key := strings.ToLower(repo)
		mu.Lock()
		was, seen := last[key]
		last[key] = required
		mu.Unlock()
		if !required && (!seen || was) {
			log.Warn("maku/merge-gate is not required from the App that posts it on "+info.DefaultBranch+": anyone with write access can merge an agents pull request",
				"repository", repo, "app.id", appID)
		}
	}
}

// readAppID is the App id an id file holds, a positive integer.
func readAppID(file string) (int64, error) {
	if file == "" {
		return 0, errors.New("no merger App id file")
	}
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("app id: not a positive integer")
	}
	return id, nil
}
