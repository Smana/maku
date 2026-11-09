// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// counting stands in for each poller's Start: it records which repositories run.
type counting struct {
	mu      sync.Mutex
	running map[string]int
	started map[string]int
}

func (c *counting) run(ctx context.Context, p *IssuePoller) error {
	c.mu.Lock()
	c.running[p.Repository]++
	c.started[p.Repository]++
	c.mu.Unlock()
	<-ctx.Done()
	c.mu.Lock()
	c.running[p.Repository]--
	c.mu.Unlock()
	return nil
}

func (c *counting) snapshot() (running, started map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	running, started = map[string]int{}, map[string]int{}
	for k, v := range c.running {
		if v > 0 {
			running[k] = v
		}
	}
	for k, v := range c.started {
		started[k] = v
	}
	return running, started
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Factory resources D12: the poller set follows the served repositories. Sync before Start
// records the set; Start runs it; a later Sync starts a new repository's poller and stops a
// removed one's, leaving the others running; the end of Start stops them all.
func TestPollersFollowTheServedRepositories(t *testing.T) {
	c := &counting{running: map[string]int{}, started: map[string]int{}}
	p := &Pollers{New: func(repo string) (*IssuePoller, bool) {
		if repo == "o/unwired" {
			return nil, false
		}
		return &IssuePoller{Repository: repo}, true
	}, start: c.run}
	p.Sync([]string{"o/a", "o/b", "o/unwired"})
	if r, _ := c.snapshot(); len(r) != 0 {
		t.Fatalf("running before Start: %v", r)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()
	is := func(want ...string) func() bool {
		return func() bool {
			r, _ := c.snapshot()
			got := make([]string, 0, len(r))
			for k := range r {
				got = append(got, k)
			}
			slices.Sort(got)
			return slices.Equal(got, want)
		}
	}
	eventually(t, "a and b run", is("o/a", "o/b"))
	p.Sync([]string{"o/b", "o/c"})
	eventually(t, "b and c run, a stopped", is("o/b", "o/c"))
	if _, s := c.snapshot(); s["o/b"] != 1 {
		t.Fatalf("b restarted: started %v", s)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r, _ := c.snapshot(); len(r) != 0 {
		t.Fatalf("still running after Start returned: %v", r)
	}
	if !p.NeedLeaderElection() {
		t.Fatal("the pollers are leader-only")
	}
}
