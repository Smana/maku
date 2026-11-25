// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/evidence"
)

// gcp-0, 2026-11-25: every settings page read each repository's decisions, and each read paged
// through every merged pull request of 30 days (cloud-native-ref: six search pages, 9-10 s). The
// read now asks only for the pull requests the factory commented on, once per RecentTTL, however
// many pages and callers ask.
func TestRecentReadsTheFactorysPullRequestsOncePerTTL(t *testing.T) {
	cfg, _ := replayFixture(t)
	repo := cfg.Repositories[0].Name
	var reads atomic.Int32
	var filters sync.Map
	release := make(chan struct{})
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var clock sync.Mutex
	s := &Service{Cfg: cfg, Now: func() time.Time { clock.Lock(); defer clock.Unlock(); return now },
		RecentReader: func(_ context.Context, _, filter string) ([]evidence.PR, error) {
			reads.Add(1)
			filters.Store(filter, true)
			<-release
			return nil, nil
		}}
	var wg sync.WaitGroup
	for range 8 { // one settings page per repository and tab, at once
		wg.Go(func() {
			if _, err := s.Recent(t.Context(), repo, "2026-10-26"); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Fatalf("8 concurrent callers ran %d reads, want 1", n)
	}
	if _, ok := filters.Load("merged:>=2026-10-26 commenter:app/ogenki-agent-factory"); !ok {
		t.Fatal("the read was not narrowed to the factory's pull requests")
	}
	if _, err := s.Recent(t.Context(), repo, "2026-10-26"); err != nil || reads.Load() != 1 {
		t.Fatalf("a read within the TTL went to GitHub again: %d reads, %v", reads.Load(), err)
	}
	clock.Lock()
	now = now.Add(RecentTTL)
	clock.Unlock()
	if _, err := s.Recent(t.Context(), repo, "2026-10-26"); err != nil || reads.Load() != 2 {
		t.Fatalf("an expired read was served: %d reads, %v", reads.Load(), err)
	}
}

func TestRecentFilterWithoutAFactoryLogin(t *testing.T) {
	if got := RecentFilter("", "2026-11-01"); got != "merged:>=2026-11-01" {
		t.Fatal(got)
	}
	if got := RecentFilter("maku-factory", "2026-11-01"); got != "merged:>=2026-11-01 commenter:maku-factory" {
		t.Fatal(got)
	}
}

func TestRecentCacheIsBounded(t *testing.T) {
	cfg, _ := replayFixture(t)
	s := &Service{Cfg: cfg, RecentReader: func(context.Context, string, string) ([]evidence.PR, error) { return nil, nil }}
	for i := range 2 * maxRecentEntries {
		if _, err := s.Recent(t.Context(), cfg.Repositories[0].Name, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(s.recent); n > maxRecentEntries {
		t.Fatalf("%d entries kept, bound %d", n, maxRecentEntries)
	}
}
