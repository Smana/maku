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
// with the error and the refusals goes out once per servableLogEvery. Any other error returns at
// once; ctx ending returns errStoppedWaiting. refused may be nil.
func waitServable(ctx context.Context, log *slog.Logger, gate *startupGate, sync func(context.Context) error,
	refused func() []string, first, limit time.Duration,
) error {
	delay := first
	var logged time.Time
	for {
		err := sync(ctx)
		if !errors.Is(err, configsource.ErrNotServable) {
			gate.waiting.Store(nil)
			return err
		}
		gate.wait(err.Error())
		if now := time.Now(); logged.IsZero() || now.Sub(logged) >= servableLogEvery {
			var r []string
			if refused != nil {
				r = refused()
			}
			log.Warn("waiting for a servable config: apply the factory's Factory, Repository and MergePolicy resources",
				"error", err, "refused", r, "retry", delay)
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

// refusals lists the objects the last Sync did not serve, as Kind name: Reason message.
func refusals(src *configsource.Source) func() []string {
	return func() []string {
		outs, _ := src.Outcomes()
		var r []string
		for _, o := range outs {
			if !o.Accepted {
				r = append(r, fmt.Sprintf("%s %s: %s %s", o.Kind, o.Name, o.Reason, o.Message))
			}
		}
		return r
	}
}
