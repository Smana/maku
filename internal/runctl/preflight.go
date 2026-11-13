// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The states of a preflight Result.
const (
	PreflightUnknown = "Unknown" // no check has finished yet: new runs wait
	PreflightPassed  = "Passed"
	PreflightFailed  = "Failed" // new runs are refused with the check's message
	// PreflightWaiting: a check that waits, rather than refuses, did not pass (a git Secret not yet
	// filled): new runs stay Pending with its message.
	PreflightWaiting = "Waiting"
)

// DefaultPreflightEvery is how often the preflight checks again after its first run.
const DefaultPreflightEvery = 10 * time.Minute

// Result is the preflight's latest outcome. Check and Message name the first failed check.
type Result struct {
	State, Check, Message string
	At                    time.Time
}

// check is one preflight check: a verdict, or an error when the check could not decide (the API
// server unreachable), which keeps the last result.
type check struct {
	name string
	run  func(ctx context.Context) (verdict, error)
}

// verdict is a decided check: refusal is empty when it passed. soon asks for the short retry on a
// refusal that may cure itself (RBAC propagating right after install). waits makes the refusal hold
// new runs Pending (Waiting) instead of failing them.
type verdict struct {
	refusal     string
	soon, waits bool
}

// Preflight proves, at start and every Every, that the cluster can run a sandboxed run (v4 D18):
// the RuntimeClass exists, and so do the git Secrets runs mount, each with a token key. The reconciler consults Result before it creates a run's first pod; a run
// that already has a pod is never refused by it (plan PR7). It is a manager.Runnable.
type Preflight struct {
	Client client.Reader // uncached: one get by name, never a cluster-wide list or watch
	Cfg    Config
	Every  time.Duration
	Log    *slog.Logger
	// Passed reports each check's outcome, for the maku_run_controller_preflight_passed gauge.
	Passed func(check string, ok bool)
	Now    func() time.Time
	// Kube creates and deletes the network check's probe pod and its NetworkPolicy (kubernetes mode).
	Kube client.Client
	// ProbeStart is how long the probe pod may stay Pending (default 2 minutes); ProbePoll how often
	// its phase is read (default 2 s).
	ProbeStart, ProbePoll time.Duration
	// ProbeSuffix names the probe pod (default 8 random characters of [a-z2-7]).
	ProbeSuffix func() string

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
// unreachable right after install), or one refused for a Forbidden read (the ClusterRole binding not
// yet propagated), is retried sooner, from 10 s with backoff, up to Every.
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

// undecidedRetry is the wait after the n-th short-retry tick in a row: 10 s, doubling, capped at every.
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
		cs = append(cs, check{name: "runtimeClass", run: p.runtimeClass})
	}
	if p.Cfg.Gateway() {
		cs = append(cs, check{name: "gateway-token-ttl", run: p.gatewayTokenTTL})
	}
	cs = append(cs, check{name: "gitSecrets", run: p.gitSecrets})
	if p.Cfg.NetworkPolicy == NetworkPolicyKubernetes {
		cs = append(cs, check{name: "network", run: p.network})
	}
	return cs
}

// gitSecrets: each git Secret a run may mount exists with a token key (PR B): gitSecret, and
// gitReadSecret when configured. Its refusal holds new runs Pending, never fails them, and is
// retried soon: the factory's run token writer, or an ExternalSecret, may fill it right after install.
func (p *Preflight) gitSecrets(ctx context.Context) (verdict, error) {
	refusal, soon, err := p.gitSecretsRefusal(ctx)
	return verdict{refusal: refusal, soon: soon, waits: true}, err
}

func (p *Preflight) gitSecretsRefusal(ctx context.Context) (string, bool, error) {
	type secret struct{ name, key, roles string }
	ss := []secret{{p.Cfg.GitSecret, "runController.gitSecret", "implementer and tester"}}
	if p.Cfg.GitReadSecret != "" {
		ss = append(ss, secret{p.Cfg.GitReadSecret, "runController.gitReadSecret", "reviewer and triager"})
	}
	for _, s := range ss {
		var sec corev1.Secret
		err := p.Client.Get(ctx, client.ObjectKey{Namespace: p.Cfg.Namespace, Name: s.name}, &sec)
		ref := p.Cfg.Namespace + "/" + s.name
		switch {
		case apierrors.IsNotFound(err):
			return fmt.Sprintf("Secret %s not found: %s runs mount it (%s); create it with a token key", ref, s.roles, s.key), true, nil
		case apierrors.IsForbidden(err):
			return fmt.Sprintf("the run-controller may not read Secret %s: it needs get on that name (the chart's run-controller Role): %v", ref, err), true, nil
		case err != nil:
			return "", false, err
		case len(sec.Data["token"]) == 0:
			return fmt.Sprintf("Secret %s has no token key: %s runs mount it (%s)", ref, s.roles, s.key), true, nil
		}
	}
	return "", false, nil
}

// runtimeClass: the configured RuntimeClass exists. With none configured (allowUnsandboxed) there is
// nothing to check, and Invalid already refuses a run with neither.
func (p *Preflight) runtimeClass(ctx context.Context) (verdict, error) {
	refusal, soon, err := p.runtimeClassRefusal(ctx)
	return verdict{refusal: refusal, soon: soon}, err
}

func (p *Preflight) runtimeClassRefusal(ctx context.Context) (string, bool, error) {
	name := p.Cfg.RuntimeClassName
	err := p.Client.Get(ctx, client.ObjectKey{Name: name}, &nodev1.RuntimeClass{})
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Sprintf("RuntimeClass %q not found: install the sandbox runtime on your nodes, or set sandbox.runtimeClassName", name), false, nil
	case apierrors.IsForbidden(err):
		// A decided failure, which names the missing grant; checked again soon, since right after
		// install the binding may not have reached the authorizer yet.
		return fmt.Sprintf("the run-controller may not read RuntimeClass %q: it needs get on node.k8s.io runtimeclasses for that name "+
			"(the chart's maku-<release namespace>-run-controller-runtimeclass ClusterRole): %v", name, err), true, nil
	case err != nil:
		return "", false, err
	}
	return "", false, nil
}

// Tick runs every check once. The first refusal fails the preflight; a check that cannot decide
// keeps the last result (Unknown before any, carrying the error), so an API server blip refuses no
// run. It reports whether to check again soon: a check was undecided, or refused with soon.
func (p *Preflight) Tick(ctx context.Context) (retrySoon bool) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	next := Result{State: PreflightPassed, At: now()}
	for _, c := range p.checks() {
		v, err := c.run(ctx)
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
			p.Passed(c.name, v.refusal == "")
		}
		if v.refusal != "" {
			state := PreflightFailed
			if v.waits {
				state = PreflightWaiting
			}
			next = Result{State: state, Check: c.name, Message: v.refusal, At: next.At}
			retrySoon = v.soon
			break
		}
	}
	p.mu.Lock()
	prev := p.res
	p.res = next
	p.mu.Unlock()
	if prev.State == next.State && prev.Check == next.Check && prev.Message == next.Message {
		return retrySoon
	}
	switch next.State {
	case PreflightPassed:
		p.log().Info("preflight passed")
	case PreflightWaiting:
		p.log().Warn("preflight waiting: "+next.Check+": "+next.Message, "check", next.Check)
	default:
		p.log().Error("preflight failed: "+next.Check+": "+next.Message, "check", next.Check)
	}
	return retrySoon
}

func (p *Preflight) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}
