// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Smana/maku/internal/factory/configsource"
)

const (
	// servableFirst and servableMax bound the first Sync's retry while nothing can be served: the
	// delay doubles from the first to the cap. servableLogEvery spaces the wait's warnings.
	servableFirst    = 5 * time.Second
	servableMax      = 60 * time.Second
	servableLogEvery = time.Minute

	applyHint = "apply the factory's Factory, Repository and MergePolicy resources"
	fileHint  = "the factory CRDs are not installed and the config file alone holds nothing to serve: fix the file"
)

// errStoppedWaiting is the wait ended by the process's context: a clean exit, not a failed start.
var errStoppedWaiting = errors.New("stopped while waiting for a servable config")

// startupGate is /readyz before the manager serves: the reason the factory waits for a servable
// config, and the caches' sync check once the manager exists (false until then).
type startupGate struct {
	waiting atomic.Pointer[string]
	synced  atomic.Pointer[func(context.Context) bool]
}

func (g *startupGate) wait(reason string) { g.waiting.Store(&reason) }

// serve hands readiness to the manager's caches.
func (g *startupGate) serve(synced func(context.Context) bool) { g.synced.Store(&synced) }

// Synced is the caches' sync check, false before serve.
func (g *startupGate) Synced(ctx context.Context) bool {
	f := g.synced.Load()
	return f != nil && (*f)(ctx)
}

// handler answers /readyz with the wait's reason while there is one, and passes the rest on.
func (g *startupGate) handler(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/readyz" {
			if reason := g.waiting.Load(); reason != nil {
				http.Error(w, "waiting for a servable config: "+*reason, http.StatusServiceUnavailable)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
}

// waitServable runs sync until it serves. While it fails with configsource.ErrNotServable (the
// file holds only plumbing and the resources are not applied yet) the factory waits instead of
// failing the start (D14): /readyz says why, the delay doubles from first to limit, and a warning
// with the error and the refusals goes out once per logEvery; with no CRDs installed it points at
// the file instead of the resources. Any other error returns at once; ctx ending, even while a
// Sync is in flight, returns errStoppedWaiting. state may be nil.
func waitServable(ctx context.Context, log *slog.Logger, gate *startupGate, sync func(context.Context) error,
	state func() waitState, first, limit, logEvery time.Duration,
) error {
	delay := first
	var logged time.Time
	for {
		err := sync(ctx)
		if err != nil && ctx.Err() != nil {
			return fmt.Errorf("%w: %w", errStoppedWaiting, context.Cause(ctx))
		}
		if !errors.Is(err, configsource.ErrNotServable) {
			gate.waiting.Store(nil)
			return err
		}
		gate.wait(err.Error())
		if now := time.Now(); logged.IsZero() || now.Sub(logged) >= logEvery {
			var st waitState
			if state != nil {
				st = state()
			}
			hint := applyHint
			if st.crdsAbsent {
				hint = fileHint
			}
			log.Warn("waiting for a servable config: "+hint, "error", err, "refused", st.refused, "retry", delay)
			logged = now
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w: %w", errStoppedWaiting, context.Cause(ctx))
		case <-t.C:
		}
		delay = min(2*delay, limit)
	}
}

// waitState is what the last Sync left: the objects it refused, and whether it saw no CRDs.
type waitState struct {
	refused    []string
	crdsAbsent bool
}

// waitStateOf reads the Source's refusals, as Kind name: Reason message, and whether its CRDs are absent.
func waitStateOf(src *configsource.Source) func() waitState {
	return func() waitState {
		outs, _ := src.Outcomes()
		st := waitState{crdsAbsent: src.CRDsAbsent()}
		for _, o := range outs {
			if !o.Accepted {
				st.refused = append(st.refused, fmt.Sprintf("%s %s: %s %s", o.Kind, o.Name, o.Reason, o.Message))
			}
		}
		return st
	}
}
