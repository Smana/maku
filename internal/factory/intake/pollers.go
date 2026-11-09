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

	mu      sync.Mutex
	ctx     context.Context // Start's; nil until Start
	want    []string
	running map[string]context.CancelFunc // by lowercased name
	wg      sync.WaitGroup
}

// NeedLeaderElection is true: two pollers would race each label.
func (*Pollers) NeedLeaderElection() bool { return true }

// Sync makes repos the polled set: it starts a poller for each repository it adds and stops the
// poller of each it removes, leaving the others running. Before Start, it records the set, which
// Start runs.
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
	for name, cancel := range p.running {
		cancel()
		delete(p.running, name)
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

// apply starts and stops pollers until the running set is want. p.mu is held.
func (p *Pollers) apply() {
	if p.running == nil {
		p.running = map[string]context.CancelFunc{}
	}
	wanted := make(map[string]bool, len(p.want))
	for _, r := range p.want {
		wanted[strings.ToLower(r)] = true
	}
	for name, cancel := range p.running {
		if !wanted[name] {
			cancel()
			delete(p.running, name)
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
		p.running[name] = cancel
		p.wg.Go(func() {
			if err := p.run(ctx, poller); err != nil {
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
