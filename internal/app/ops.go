// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Smana/maku/internal/version"
)

// drainFlag is /readyz's "shutting down": true once the process context ended (a signal) or Begin
// ran. A serving listener that dies cancels the run's own context, not the process's, so the
// signal alone would leave /readyz answering 200 while the other listeners drain.
type drainFlag struct {
	process context.Context
	begun   atomic.Bool
}

func newDrainFlag(process context.Context) *drainFlag { return &drainFlag{process: process} }

// Begin marks the shutdown as started, whatever started it.
func (d *drainFlag) Begin() { d.begun.Store(true) }

// Draining reports whether the process has begun shutting down.
func (d *drainFlag) Draining() bool { return d.begun.Load() || d.process.Err() != nil }

// probeTimeout bounds each probe's check, under the kubelet's default 1 s timeout
// plus a margin the kubelet can be told to wait.
const probeTimeout = 2 * time.Second

// opsHandler serves :9090: /metrics, /healthz (the process answers), /readyz
// (not draining, the dependency answers and the watch has synced) and /startupz.
// The broker's dependency is PostgreSQL and its startup the migrated schema, so a
// pod waits for Atlas instead of crash-looping; the factory's are GitHub and the
// parsed config (factoryOps).
func opsHandler(ping func(context.Context) error, schemaReady func(context.Context) (bool, error),
	synced func(context.Context) bool, draining func() bool, metrics http.Handler,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok " + version.Version + "\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if draining() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			http.Error(w, "dependency unreachable", http.StatusServiceUnavailable)
			return
		}
		if !synced(ctx) {
			http.Error(w, "watch not synced", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /startupz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
		defer cancel()
		if ok, err := schemaReady(ctx); err != nil || !ok {
			http.Error(w, "not started", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("started\n"))
	})
	return mux
}

// opsServer bounds :9090. Nothing on it streams, so WriteTimeout is set.
func opsServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
		// /preflight runs its checks within preflight.AllTimeout (30 s), on demand.
		WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
}

// syncCheck bounds readiness's wait on the run watch: a synced one answers at once.
const syncCheck = 250 * time.Millisecond

// boundedSync waits on synced for at most syncCheck.
func boundedSync(synced func(context.Context) bool) func(context.Context) bool {
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, syncCheck)
		defer cancel()
		return synced(ctx)
	}
}

// preflightOn reports whether the chart's preflight.enabled turned /preflight on (MAKU_PREFLIGHT=true).
// Off, the component serves no /preflight: a default install's pods cannot make it spend its
// credentials (a GitHub App call, a model completion, the OAuth client) on demand.
func preflightOn(getenv func(string) string) bool { return getenv("MAKU_PREFLIGHT") == "true" }

// withPreflight adds GET /preflight (v4 D24) to an ops handler when on: the component's own checks,
// served beside /metrics on the same unexposed port, unauthenticated like it. pf is built only when on.
func withPreflight(ops http.Handler, on bool, pf func() http.Handler) http.Handler {
	if !on {
		return ops
	}
	mux := http.NewServeMux()
	mux.Handle("GET /preflight", pf())
	mux.Handle("/", ops)
	return mux
}
