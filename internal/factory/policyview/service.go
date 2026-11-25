// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
)

// RecentTTL is how long a repository's recent decisions are served from memory. Every settings
// page reads them for each repository, and one read is a paged GitHub search of seconds; a
// decision lands on a merged pull request, so a minute's lag hides nothing a maintainer acts on.
const RecentTTL = time.Minute

// maxRecentEntries bounds the cache: a caller picks since, so the keys are not the config's.
const maxRecentEntries = 64

// Reports is the repository's evidence report, cached; *evidence.Service implements it.
type Reports interface {
	Report(ctx context.Context, repository string) (evidence.Report, error)
}

// CommentReader reads a pull request's newest comments.
type CommentReader func(ctx context.Context, pr int) ([]Comment, error)

// Service serves the editor's check and the decision timeline for configured repositories.
// Comments is the repository's comment reader, false without a connection.
type Service struct {
	Cfg      config.Source
	Reports  Reports
	Comments func(repository string) (CommentReader, bool)
	// RecentReader lists a repository's merged pull requests matching filter (GitHub search
	// qualifiers Recent builds from config and since) and their comments, newest first.
	RecentReader   func(ctx context.Context, repository, filter string) ([]evidence.PR, error)
	BacktestReader func(repository string) (BacktestSource, bool)
	// Namespace is the MergePolicy objects' namespace, for a policy edit's manifest.
	Namespace string
	// Now is the clock of the recent decisions' cache, time.Now when nil.
	Now func() time.Time

	mu     sync.Mutex
	recent map[string]recentEntry
	reads  singleflight.Group
}

type recentEntry struct {
	at    time.Time
	value Recent
}

func (s *Service) repository(cfg *config.Config, name string) (*config.Repository, error) {
	for _, r := range cfg.Repos() {
		if strings.EqualFold(r.Name, name) {
			return &r, nil
		}
	}
	return nil, evidence.ErrUnknownRepository
}

// Preview checks file against repository's current policy (Check).
func (s *Service) Preview(ctx context.Context, repository, file string) (Preview, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Preview{}, err
	}
	rep, err := s.Reports.Report(ctx, rc.Name)
	if err != nil {
		return Preview{}, err
	}
	return Check(cfg, rc, rep, file), nil
}

// PreviewBindings checks bindings as repository's spec.mergePolicies (CheckBindings, B16).
func (s *Service) PreviewBindings(ctx context.Context, repository string, bindings []config.Binding) (BindingPreview, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return BindingPreview{}, err
	}
	rep, err := s.Reports.Report(ctx, rc.Name)
	if err != nil {
		return BindingPreview{}, err
	}
	return CheckBindings(cfg, rc, rep, bindings), nil
}

// PreviewPolicy checks spec as the MergePolicy name, with the consumers readable lets through
// (CheckPolicy, B11, B16).
func (s *Service) PreviewPolicy(_ context.Context, name string, spec config.PolicySpec, readable func(string) bool) (PolicyPreview, error) {
	return CheckPolicy(s.Cfg.Current(), name, spec, s.Namespace, readable), nil
}

// Timeline is pr's decisions, from the factory's own comments on it.
func (s *Service) Timeline(ctx context.Context, repository string, pr int) (Timeline, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Timeline{}, err
	}
	read, ok := s.Comments(rc.Name)
	if !ok {
		return Timeline{}, evidence.ErrUnknownRepository
	}
	cs, err := read(ctx, pr)
	if err != nil {
		return Timeline{}, fmt.Errorf("policyview: %w", err)
	}
	return Decisions(rc.Name, pr, cfg.FactoryLogin, cs), nil
}

// Recent is a repository's per-policy decisions since a GitHub merged:>= date. Only the pull
// requests the factory commented on are read: a decision is one of its comments, and the merged
// pull requests it never commented on are most of a busy repository's (cloud-native-ref: 48 of
// 284 in 30 days, six search pages down to one). A read is shared by concurrent callers and served
// for RecentTTL.
func (s *Service) Recent(ctx context.Context, repository, since string) (Recent, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Recent{}, err
	}
	if s.RecentReader == nil {
		return Recent{}, evidence.ErrUnknownRepository
	}
	filter := RecentFilter(cfg.FactoryLogin, since)
	key := rc.Name + "\x00" + filter
	if r, ok := s.cachedRecent(key); ok {
		return r, nil
	}
	v, err, _ := s.reads.Do(key, func() (any, error) {
		if r, ok := s.cachedRecent(key); ok {
			return r, nil
		}
		// The shared read outlives a caller that leaves: the others still wait on it.
		prs, err := s.RecentReader(context.WithoutCancel(ctx), rc.Name, filter)
		if err != nil {
			return Recent{}, fmt.Errorf("policyview: %w", err)
		}
		r := ReadRecent(rc.Name, since, cfg.FactoryLogin, prs)
		s.storeRecent(key, r)
		return r, nil
	})
	if err != nil {
		return Recent{}, err
	}
	return v.(Recent), nil
}

// RecentFilter is the search that finds a repository's decisions: its merged pull requests since
// the date that the factory commented on, every one of them when no factory login is configured.
func RecentFilter(factoryLogin, since string) string {
	filter := "merged:>=" + since
	if factoryLogin != "" {
		filter += " commenter:" + evidence.SearchLogin(factoryLogin)
	}
	return filter
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) cachedRecent(key string) (Recent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.recent[key]
	if !ok || s.now().Sub(e.at) >= RecentTTL {
		return Recent{}, false
	}
	return e.value, true
}

func (s *Service) storeRecent(key string, r Recent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.recent == nil {
		s.recent = map[string]recentEntry{}
	}
	for k, e := range s.recent {
		if now.Sub(e.at) >= RecentTTL {
			delete(s.recent, k)
		}
	}
	if len(s.recent) >= maxRecentEntries {
		return // full of fresh reads: this one is served, not kept
	}
	s.recent[key] = recentEntry{at: now, value: r}
}

// Backtest replays an edited policy over the repository's recent recorded diffs.
func (s *Service) Backtest(ctx context.Context, repository string, req BacktestRequest, since string) (Backtest, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Backtest{}, err
	}
	rep, err := s.Reports.Report(ctx, rc.Name)
	if err != nil {
		return Backtest{}, err
	}
	recent, err := s.Recent(ctx, rc.Name, since)
	if err != nil {
		return Backtest{}, err
	}
	var source BacktestSource
	if s.BacktestReader != nil {
		source, _ = s.BacktestReader(rc.Name)
	}
	return Replay(ctx, cfg, rc, rep, req, recent, source), nil
}
