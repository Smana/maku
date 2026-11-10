// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// Pollers runs one IssuePoller per served repository and follows the set as the config's
// publishes change it (factory resources D12). It is one leader-only manager.Runnable: Start
// keeps the parent context, and each poller runs in a goroutine of its own under a child of it.
type Pollers struct {
	// New builds repository's poller; false when it has no connection, so it is not polled.
	New func(repo string) (*IssuePoller, bool)
	Log *slog.Logger

	// start runs one poller; nil is IssuePoller.Start. A test counts with it.
	start func(context.Context, *IssuePoller) error

	mu       sync.Mutex
	ctx      context.Context // Start's while it runs; nil before and after
	want     []string
	running  map[string]*polling // by lowercased name
	stopping map[string]*polling // cancelled, not yet returned: a re-add waits for them
	wg       sync.WaitGroup
}

// polling is one running poller: its cancel, and done, closed once its Start returned.
type polling struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NeedLeaderElection is true: two pollers would race each label.
func (*Pollers) NeedLeaderElection() bool { return true }

// Sync makes repos the polled set: it starts a poller for each repository it adds and stops the
// poller of each it removes, leaving the others running. Outside Start (before, or after it
// returned) it only records the set, which the next Start runs.
func (p *Pollers) Sync(repos []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.want = slices.Clone(repos)
	if p.ctx != nil {
		p.apply()
	}
}

// Start runs the recorded set until ctx ends, then stops every poller and waits for them.
func (p *Pollers) Start(ctx context.Context) error {
	p.mu.Lock()
	p.ctx = ctx
	p.apply()
	p.mu.Unlock()
	<-ctx.Done()
	p.mu.Lock()
	p.ctx = nil // a late Sync records, and starts nothing
	for name, r := range p.running {
		r.cancel()
		delete(p.running, name)
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

// apply starts and stops pollers until the running set is want. p.mu is held.
func (p *Pollers) apply() {
	if p.running == nil {
		p.running, p.stopping = map[string]*polling{}, map[string]*polling{}
	}
	wanted := make(map[string]bool, len(p.want))
	for _, r := range p.want {
		wanted[strings.ToLower(r)] = true
	}
	for name, r := range p.running {
		if !wanted[name] {
			r.cancel()
			delete(p.running, name)
			p.stopping[name] = r
		}
	}
	for _, repo := range p.want {
		name := strings.ToLower(repo)
		if _, ok := p.running[name]; ok {
			continue
		}
		poller, ok := p.New(repo)
		if !ok {
			p.log().Warn("no issue poller: the repository has no connection", "repository", repo)
			continue
		}
		ctx, cancel := context.WithCancel(p.ctx)
		r := &polling{cancel: cancel, done: make(chan struct{})}
		prev := p.stopping[name] // removed then added again: one poller per repository at a time
		p.running[name] = r
		p.wg.Go(func() {
			defer close(r.done)
			if prev != nil {
				<-prev.done
			}
			err := p.run(ctx, poller)
			p.mu.Lock()
			if p.running[name] == r { // ended on its own: the next Sync starts it again
				delete(p.running, name)
			}
			if p.stopping[name] == r {
				delete(p.stopping, name)
			}
			p.mu.Unlock()
			cancel()
			if err != nil {
				p.log().Error("issue poller stopped", "repository", repo, "err", err)
			}
		})
	}
}

func (p *Pollers) run(ctx context.Context, poller *IssuePoller) error {
	if p.start != nil {
		return p.start(ctx, poller)
	}
	return poller.Start(ctx)
}

func (p *Pollers) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}
