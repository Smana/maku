// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/drills"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// Readiness's drill port (R6) is the broker client the runner posts to.
var _ hardening.Drills = (*rooms.Client)(nil)

// pusherOnly is an applier pusher that reads no bundle.
type pusherOnly struct{}

func (pusherOnly) Inspect(context.Context, []byte, string, string) (gitbundle.Inspection, error) {
	return gitbundle.Inspection{}, nil
}
func (pusherOnly) Push(context.Context, gitbundle.Inspection) error { return nil }
func (pusherOnly) Discard(context.Context, gitbundle.Inspection)    {}

// HR38: the drill runner runs on the leader only while a gateway log is configured, outside the
// demo; it proves the live hardening hash under this Maku version, waits drills.timeout, and reads
// pushes through the applier's mirror.
func TestFactoryDrillWiring(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hardening.ApplyDefaults()
	if r, err := drillRunner(cfg, drillDeps{}); r != nil || err != nil {
		t.Fatalf("no gateway log: %v %v", r, err)
	}
	cfg.Hardening.Drills.GatewayLog.URL = "http://victoria-logs:9428"
	cfg.Demo = true
	if r, _ := drillRunner(cfg, drillDeps{}); r != nil {
		t.Fatal("the demo runs drills")
	}
	cfg.Demo = false
	live := *cfg
	live.Hardening.Drills.Timeout.Duration = 3 * time.Hour
	r, err := drillRunner(cfg, drillDeps{Live: func() *config.Config { return &live }})
	if err != nil || r == nil {
		t.Fatalf("%v %v", r, err)
	}
	if !r.NeedLeaderElection() || r.Gateway == nil || len(r.Suite.Scenarios) == 0 || r.Readers != nil {
		t.Fatalf("%+v", r)
	}
	if k := r.Key(); k.Version != version.Version || k.ConfigHash != live.Hardening.Hash() {
		t.Fatalf("key %+v", k)
	}
	if r.Timeout() != 3*time.Hour {
		t.Fatalf("timeout %s", r.Timeout())
	}
	r, _ = drillRunner(cfg, drillDeps{Live: func() *config.Config { return &live },
		Pushers: func(context.Context, string) (applier.Pusher, error) { return pusherOnly{}, nil }})
	if _, err := r.Readers(t.Context(), "Smana/drill"); err == nil || !strings.Contains(err.Error(), "reads no bundle") {
		t.Fatalf("a pusher with no Read: %v", err)
	}
	r, _ = drillRunner(cfg, drillDeps{Live: func() *config.Config { return &live },
		Pushers: func(context.Context, string) (applier.Pusher, error) { return nil, errors.New("no cache") }})
	if _, err := r.Readers(t.Context(), "Smana/drill"); err == nil {
		t.Fatal("a pusher error was swallowed")
	}
	// The applier's mirror reads bundles.
	var _ drills.BundleReader = (*gitbundle.Mirror)(nil)
}

// #520's review: the applier counts a T5 call as a drill's only while the drills run, on the drill
// repository: a stray drill label never hides a false positive.
func TestApplierDrillGate(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	cfg.Repositories = []config.Repository{{Name: "Smana/ai-native-ref"}, {Name: "Smana/maku-drills"}}
	a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: slog.New(slog.DiscardHandler)})
	if err != nil || a == nil || a.Drill == nil {
		t.Fatalf("applier %v %v", a, err)
	}
	if a.Drill("Smana/maku-drills") {
		t.Fatal("drills off: a drill task")
	}
	cfg.Hardening.Drills.GatewayLog.URL, cfg.Hardening.Drills.Repository = "http://logs:9428", "Smana/maku-drills"
	if !a.Drill("Smana/maku-drills") || a.Drill("Smana/ai-native-ref") {
		t.Fatal("the live drill repository does not bound drill tasks")
	}
}
