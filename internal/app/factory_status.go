// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/statuswriter"
)

// factoryStatusInputs is what the status writer reads: the Source's outcomes and published
// config, each repository's GitHub health, the policy watch's snapshots (none while
// policyManagement.read is off), the latest decisions and the stop object. A stop that cannot be
// read counts as not stopped and is logged.
func factoryStatusInputs(ns string, src *configsource.Source, c client.Reader, conns *repoConns,
	watch func() *mergepolicy.Watch, rec *decision.Recorder, log *slog.Logger) func(context.Context) statuswriter.Inputs {
	return func(ctx context.Context) statuswriter.Inputs {
		outcomes, synced := src.Outcomes()
		in := statuswriter.Inputs{
			Outcomes: outcomes, Synced: synced, Config: src.Live.Current(),
			WireErr: conns.wireErr,
			Connected: func(repository string) (bool, string) {
				rc, ok := conns.get(repository)
				if !ok {
					return false, "no GitHub connection"
				}
				if rc.gh.Healthy(time.Now()) {
					return true, ""
				}
				return false, "no successful GitHub call in the last 3 minutes"
			},
			Decisions: rec.Latest,
			GitHubUp:  conns.healthy(time.Now()),
		}
		if w := watch(); w != nil {
			in.Snapshot = w.Snapshot
		}
		stopped, err := killswitch.Engaged(ctx, c, ns)
		if err != nil {
			log.Warn("status: stop object not read", "error", err)
		}
		in.Stopped = stopped && err == nil
		return in
	}
}
