// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/drills"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// drillDeps are the factory's parts the drill runner is built from (hardening HR38).
type drillDeps struct {
	Client    client.Client // a live client: manifests' ConfigMaps and Tasks
	Namespace string
	Rooms     drills.Rooms
	Claims    drills.Claims
	// Pushers is the applier's git mirror, nil while no repository enforces push: an applied push
	// is then never read, and the scorer fails it unscorable.
	Pushers func(ctx context.Context, repo string) (applier.Pusher, error)
	Live    func() *config.Config
	Metrics drills.Metrics
	Log     *slog.Logger
}

// drillRunner is the drill runner for cfg: nil in the demo profile (never hardened, R7) or while
// hardening.drills.gatewayLog.url is unset, since withheld text reaching a model could then not be
// checked; the manifest route is mounted under the same condition. It runs on the leader alone.
func drillRunner(cfg *config.Config, d drillDeps) (*drills.Runner, error) {
	gl := cfg.Hardening.Drills.GatewayLog
	if !cfg.DrillsOn() {
		return nil, nil
	}
	suite, err := hardening.DefaultSuite()
	if err != nil {
		return nil, fmt.Errorf("drill runner: %w", err)
	}
	r := &drills.Runner{Client: d.Client, Namespace: d.Namespace, Rooms: d.Rooms, Claims: d.Claims, Suite: suite,
		Gateway: drills.NewGatewayLog(gl.URL, gl.Timeout.Duration, nil), Metrics: d.Metrics, Log: d.Log, Now: time.Now,
		Key: func() hardening.DrillKey {
			return hardening.DrillKey{Version: version.Version, ConfigHash: d.Live().Hardening.Hash()}
		},
		Timeout: func() time.Duration { return d.Live().Hardening.Drills.Timeout.Duration }}
	if d.Pushers != nil {
		r.Readers = func(ctx context.Context, repo string) (drills.BundleReader, error) {
			p, err := d.Pushers(ctx, repo)
			if err != nil {
				return nil, err
			}
			br, ok := p.(drills.BundleReader)
			if !ok {
				return nil, fmt.Errorf("drill runner: the pusher of %s reads no bundle", repo)
			}
			return br, nil
		}
	}
	return r, nil
}
