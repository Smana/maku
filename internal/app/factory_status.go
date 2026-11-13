// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/statuswriter"
)

// factoryStatusInputs is what the status writer reads: the Source's outcomes and published
// config, each repository's GitHub health, the policy watch's snapshots (none while
// policyManagement.read is off), the latest decisions, each repository's visibility as the last
// fence pass read it (v4 D3) and the stop object. A stop that cannot be
// read counts as not stopped and is logged.
func factoryStatusInputs(ns string, src *configsource.Source, c client.Reader, conns *repoConns,
	watch func() *mergepolicy.Watch, rec *decision.Recorder, visibility func(string) string, log *slog.Logger) func(context.Context) statuswriter.Inputs {
	return func(ctx context.Context) statuswriter.Inputs {
		outcomes, synced := src.Outcomes()
		in := statuswriter.Inputs{
			Outcomes: outcomes, Synced: synced, Config: src.Live.Current(),
			WireErr: conns.wireErr,
			Connected: func(repository string) (metav1.ConditionStatus, string) {
				return conns.connection(repository, time.Now())
			},
			Decisions:  rec.Latest,
			GitHubUp:   conns.githubUp(time.Now()),
			Visibility: visibility,
		}
		if w := watch(); w != nil {
			in.Snapshot = w.Snapshot
		}
		stopped, err := killswitch.Engaged(ctx, c, ns)
		if err != nil {
			log.Warn("status: stop object not read", "error", err)
		}
		in.Stopped = stopped
		return in
	}
}

// connection is repository's Connected; one without a factory connection is not connected.
func (c *repoConns) connection(repository string, now time.Time) (metav1.ConditionStatus, string) {
	rc, ok := c.get(repository)
	if !ok || rc.gh == nil {
		return metav1.ConditionFalse, "no GitHub connection"
	}
	return connection(rc.gh, now)
}

// githubUp is githubUp over the served repositories' factory connections.
func (c *repoConns) githubUp(now time.Time) bool {
	var ghs []ghHealth
	for _, rc := range c.all() {
		if rc.gh != nil {
			ghs = append(ghs, rc.gh)
		}
	}
	return githubUp(ghs, now)
}

// ghHealth is a repository's GitHub connection as the status reads it; *forge.GitHub.
type ghHealth interface {
	Checked() bool
	Healthy(now time.Time) bool
}

// connection is a repository's Connected: Unknown before its first GitHub call, which says nothing
// about GitHub yet.
func connection(gh ghHealth, now time.Time) (metav1.ConditionStatus, string) {
	switch {
	case !gh.Checked():
		return metav1.ConditionUnknown, ""
	case gh.Healthy(now):
		return metav1.ConditionTrue, ""
	}
	return metav1.ConditionFalse, "no recent successful GitHub call"
}

// githubUp is the Factory's GitHub health: no checked connection is unhealthy. A connection never
// called yet is no outage.
func githubUp(ghs []ghHealth, now time.Time) bool {
	for _, gh := range ghs {
		if gh.Checked() && !gh.Healthy(now) {
			return false
		}
	}
	return true
}
