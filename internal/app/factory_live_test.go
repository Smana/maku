// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// fakeWiring connects every repository but those in refuse, and counts the connections.
type fakeWiring struct {
	mu      sync.Mutex
	refuse  map[string]bool
	gh      map[string]int
	mergers map[string]int
	failed  []string
}

func newFakeWiring() *fakeWiring {
	return &fakeWiring{refuse: map[string]bool{}, gh: map[string]int{}, mergers: map[string]int{}}
}

func (w *fakeWiring) conns() *repoConns {
	c := newRepoConns(slog.New(slog.DiscardHandler))
	c.connectGH = func(_ context.Context, _ *config.Config, name string) (*forge.GitHub, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.refuse[name] {
			return nil, errors.New("the App is not installed")
		}
		w.gh[name]++
		return &forge.GitHub{}, nil
	}
	c.connectMerger = func(_ context.Context, _ *config.Config, name string) (forge.Merger, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.mergers[name]++
		return forge.Absent{}, nil
	}
	c.failed = func(_ context.Context, name string) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.failed = append(w.failed, name)
	}
	return c
}

func repos(names ...string) *config.Config {
	c := &config.Config{}
	for _, n := range names {
		c.Repositories = append(c.Repositories, config.Repository{Name: n})
	}
	return c
}

// Factory resources D12: sync connects a repository a publish adds, keeps the connections it had,
// drops one it removes, and leaves out one it cannot wire, naming it in its error.
func TestRepoConnsSyncAddsDropsAndLeavesOutAFailure(t *testing.T) {
	w := newFakeWiring()
	c := w.conns()
	if err := c.sync(t.Context(), repos("o/a", "o/b")); err != nil {
		t.Fatal(err)
	}
	a, _ := c.get("o/a")
	if !slices.Equal(c.names(), []string{"o/a", "o/b"}) {
		t.Fatalf("names %v", c.names())
	}
	w.refuse["o/new"] = true
	err := c.sync(t.Context(), repos("o/a", "o/new", "o/c"))
	if err == nil || !strings.Contains(err.Error(), "o/new") {
		t.Fatalf("err = %v, want one naming o/new", err)
	}
	if !slices.Equal(c.names(), []string{"o/a", "o/c"}) {
		t.Fatalf("names %v, want the wired ones in the config's order", c.names())
	}
	if _, ok := c.get("o/b"); ok {
		t.Fatal("a removed repository keeps its connection")
	}
	if _, ok := c.get("o/new"); ok {
		t.Fatal("a repository that failed to wire is served")
	}
	if again, _ := c.get("o/a"); again.gh != a.gh || w.gh["o/a"] != 1 || w.mergers["o/a"] != 1 {
		t.Fatalf("o/a reconnected: %d, %d", w.gh["o/a"], w.mergers["o/a"])
	}
	if !slices.Equal(w.failed, []string{"o/new"}) {
		t.Fatalf("failed %v", w.failed)
	}
	delete(w.refuse, "o/new")
	if err := c.sync(t.Context(), repos("o/a", "o/new", "o/c")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.names(), []string{"o/a", "o/new", "o/c"}) {
		t.Fatalf("names %v: the next publish wires it", c.names())
	}
}

// A class that turns live needs the merger, so its repository's merger is connected again with
// the new scope; the others keep theirs when the scope bits do not move.
func TestRepoConnsSyncReconnectsTheMergerWhenItsNeedChanges(t *testing.T) {
	w := newFakeWiring()
	c := w.conns()
	if err := c.sync(t.Context(), repos("o/a", "o/b")); err != nil {
		t.Fatal(err)
	}
	next := repos("o/a", "o/b")
	next.Repositories[0].Classes = map[string]config.Class{"docs": {Shadow: true}}
	if err := c.sync(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if w.mergers["o/a"] != 2 || w.mergers["o/b"] != 1 || w.gh["o/a"] != 1 {
		t.Fatalf("mergers %v, apps %v", w.mergers, w.gh)
	}
}

// fakePublishMetrics records what a publish re-derives.
type fakePublishMetrics struct {
	repos    []string
	people   bool
	budgets  map[string]float64
	holdouts []string
}

func (m *fakePublishMetrics) ListRepositories(names ...string) { m.repos = slices.Clone(names) }
func (m *fakePublishMetrics) ChargePeople(on bool)             { m.people = on }
func (m *fakePublishMetrics) RunBudgetUSD(_ context.Context, tier string, usd float64) {
	m.budgets[tier] = usd
}
func (m *fakePublishMetrics) ClassWithoutHoldout(_ context.Context, repo, class string) {
	m.holdouts = append(m.holdouts, repo+"/"+class)
}

type fakePollers struct{ synced [][]string }

func (p *fakePollers) Sync(repos []string) { p.synced = append(p.synced, slices.Clone(repos)) }

// D12: a publish re-wires the repositories, re-syncs the pollers and the repository list, and
// re-derives the startup's gauges: per-person charging, each tier's run budget (0 for a tier the
// publish removed), and a live class without holdoutChecks. A failure is logged, never fatal.
func TestOnPublishRederivesWhatTheStartupDerived(t *testing.T) {
	w := newFakeWiring()
	conns := w.conns()
	prev := repos("o/a")
	prev.Tiers = map[string]config.Tier{"standard": {RunUSD: 2.55}, "gone": {RunUSD: 1}}
	if err := conns.sync(t.Context(), prev); err != nil {
		t.Fatal(err)
	}
	next := repos("o/a", "o/bad", "o/b")
	next.Tiers = map[string]config.Tier{"standard": {RunUSD: 3}, "free": {}}
	next.Budgets.PerPerson.Enabled = true
	next.Repositories[2].Classes = map[string]config.Class{"docs": {Live: true}}
	next.Warnings = []string{"a sentence"}
	w.refuse["o/bad"] = true
	m := &fakePublishMetrics{budgets: map[string]float64{}}
	p := &fakePollers{}
	var logs bytes.Buffer
	onPublish(t.Context(), slog.New(slog.NewTextHandler(&logs, nil)), conns, p, m)(prev, next)

	want := []string{"o/a", "o/b"}
	if !slices.Equal(m.repos, want) || len(p.synced) != 1 || !slices.Equal(p.synced[0], want) {
		t.Fatalf("listed %v, pollers %v, want %v", m.repos, p.synced, want)
	}
	if !m.people {
		t.Fatal("per-person charging not re-derived")
	}
	if wantB := map[string]float64{"standard": 3, "gone": 0}; !maps.Equal(m.budgets, wantB) {
		t.Fatalf("run budgets %v, want %v", m.budgets, wantB)
	}
	if !slices.Equal(m.holdouts, []string{"o/b/docs"}) {
		t.Fatalf("holdout gauge %v", m.holdouts)
	}
	for _, s := range []string{"level=ERROR", "o/bad", "a sentence"} {
		if !strings.Contains(logs.String(), s) {
			t.Errorf("log lacks %q:\n%s", s, logs.String())
		}
	}
}
