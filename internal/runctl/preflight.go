// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The states of a preflight Result.
const (
	PreflightUnknown = "Unknown" // no check has finished yet: new runs wait
	PreflightPassed  = "Passed"
	PreflightFailed  = "Failed" // new runs are refused with the check's message
)

// DefaultPreflightEvery is how often the preflight checks again after its first run.
const DefaultPreflightEvery = 10 * time.Minute

// Result is the preflight's latest outcome. Check and Message name the first failed check.
type Result struct {
	State, Check, Message string
	At                    time.Time
}

// check is one preflight check: a refusal message when it fails, or an error when the check could
// not decide (the API server unreachable), which keeps the last result.
type check struct {
	name string
	run  func(ctx context.Context) (refusal string, err error)
}

// Preflight proves, at start and every Every, that the cluster can run a sandboxed run (v4 D18):
// the RuntimeClass exists. The reconciler consults Result before it creates a run's first pod; a run
// that already has a pod is never refused by it (plan PR7). It is a manager.Runnable.
type Preflight struct {
	Client client.Reader // uncached: one get by name, never a cluster-wide list or watch
	Cfg    Config
	Every  time.Duration
	Log    *slog.Logger
	// Passed reports each check's outcome, for the maku_run_controller_preflight_passed gauge.
	Passed func(check string, ok bool)
	Now    func() time.Time

	mu  sync.Mutex
	res Result
}

// Result is the latest outcome; Unknown before the first check finishes.
func (p *Preflight) Result() Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.res.State == "" {
		return Result{State: PreflightUnknown}
	}
	return p.res
}

// NeedLeaderElection is true: only the leader creates pods, so only it needs the result.
func (p *Preflight) NeedLeaderElection() bool { return true }

// Start checks at once, then every Every, until ctx ends. An undecided tick (the API server
// unreachable right after install) is retried sooner, from 10 s with backoff, up to Every.
func (p *Preflight) Start(ctx context.Context) error {
	every := p.Every
	if every <= 0 {
		every = DefaultPreflightEvery
	}
	undecided := 0
	for {
		wait := every
		if p.Tick(ctx) {
			undecided++
			wait = undecidedRetry(undecided, every)
		} else {
			undecided = 0
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// undecidedRetry is the wait after the n-th undecided tick in a row: 10 s, doubling, capped at every.
func undecidedRetry(n int, every time.Duration) time.Duration {
	d := 10 * time.Second
	for i := 1; i < n && d < every; i++ {
		d *= 2
	}
	return min(d, every)
}

func (p *Preflight) checks() []check {
	var cs []check
	if p.Cfg.RuntimeClassName != "" {
		cs = append(cs, check{"runtimeClass", p.runtimeClass})
	}
	return cs
}

// runtimeClass: the configured RuntimeClass exists. With none configured (allowUnsandboxed) there is
// nothing to check, and Invalid already refuses a run with neither.
func (p *Preflight) runtimeClass(ctx context.Context) (string, error) {
	name := p.Cfg.RuntimeClassName
	err := p.Client.Get(ctx, client.ObjectKey{Name: name}, &nodev1.RuntimeClass{})
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Sprintf("RuntimeClass %q not found: install the sandbox runtime on your nodes, or set sandbox.runtimeClassName", name), nil
	case apierrors.IsForbidden(err):
		// Retrying cannot grant the verb: a decided failure, which names the missing grant.
		return fmt.Sprintf("the run-controller may not read RuntimeClass %q: it needs get on node.k8s.io runtimeclasses for that name "+
			"(the chart's maku-<release namespace>-run-controller-runtimeclass ClusterRole): %v", name, err), nil
	case err != nil:
		return "", err
	}
	return "", nil
}

// Tick runs every check once. The first refusal fails the preflight; a check that cannot decide
// keeps the last result (Unknown before any, carrying the error), so an API server blip refuses no
// run. It reports whether a check was undecided, for Start's sooner retry.
func (p *Preflight) Tick(ctx context.Context) (undecided bool) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	next := Result{State: PreflightPassed, At: now()}
	for _, c := range p.checks() {
		refusal, err := c.run(ctx)
		if err != nil {
			p.log().Warn("preflight check undecided", "check", c.name, "err", err)
			p.mu.Lock()
			if p.res.State == "" || p.res.State == PreflightUnknown {
				p.res = Result{State: PreflightUnknown, Check: c.name, Message: err.Error(), At: next.At}
			}
			p.mu.Unlock()
			return true
		}
		if p.Passed != nil {
			p.Passed(c.name, refusal == "")
		}
		if refusal != "" {
			next = Result{State: PreflightFailed, Check: c.name, Message: refusal, At: next.At}
			break
		}
	}
	p.mu.Lock()
	prev := p.res
	p.res = next
	p.mu.Unlock()
	if prev.State == next.State && prev.Check == next.Check && prev.Message == next.Message {
		return false
	}
	if next.State == PreflightPassed {
		p.log().Info("preflight passed")
	} else {
		p.log().Error("preflight failed: "+next.Check+": "+next.Message, "check", next.Check)
	}
	return false
}

func (p *Preflight) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}
