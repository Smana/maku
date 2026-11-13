// SPDX-License-Identifier: Apache-2.0

// Package hardeningreport reports each repository's hardening readiness (H5, HR41): a gauge every
// five minutes and one log line per change. It reports; it never decides: the coordination gate
// and every other reader compute their own answer afresh, so the gauge is never read as one.
package hardeningreport

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/hardening"
)

// DefaultInterval is the report's cadence.
const DefaultInterval = 5 * time.Minute

// MissingError is the missing fact a report names when readiness could not be computed.
const MissingError = "error"

// Metrics records one report; fmetrics.Set implements it.
type Metrics interface {
	HardeningReady(ctx context.Context, repository string, ready bool, missing []string)
}

// Reporter computes readiness for every repository Repositories names, on the leader.
type Reporter struct {
	Readiness hardening.Readiness
	// Repositories are the repositories to report: those at hardening mode audit or above.
	Repositories func() []string
	Metrics      Metrics
	Interval     time.Duration
	Log          *slog.Logger

	last map[string]string // repository → its last report, for logging changes only
}

// NeedLeaderElection is true: one replica reports.
func (*Reporter) NeedLeaderElection() bool { return true }

// Start reports at once, then every Interval, until ctx ends.
func (r *Reporter) Start(ctx context.Context) error {
	every := r.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.Report(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Report computes and records every repository's readiness once. An evaluation error reports the
// repository not ready, missing error; the error itself is logged, never put on a label.
func (r *Reporter) Report(ctx context.Context) {
	if r.last == nil {
		r.last = map[string]string{}
	}
	for _, repo := range r.Repositories() {
		rep, err := r.Readiness.Hardened(ctx, repo)
		missing := make([]string, 0, len(rep.Missing))
		for _, m := range rep.Missing {
			missing = append(missing, string(m))
		}
		ready := err == nil && rep.Hardened
		if err != nil {
			missing = []string{MissingError}
		}
		if r.Metrics != nil {
			r.Metrics.HardeningReady(ctx, repo, ready, missing)
		}
		slices.Sort(missing)
		state := strings.Join(missing, ",")
		if prev, seen := r.last[repo]; !seen || prev != state {
			args := []any{"repository", repo, "hardened", ready, "missing", missing}
			if rep.Drill != "" {
				args = append(args, "drill", rep.Drill)
			}
			if err != nil {
				args = append(args, "err", err)
			}
			r.Log.Info("hardening readiness changed", args...)
			r.last[repo] = state
		}
	}
}
