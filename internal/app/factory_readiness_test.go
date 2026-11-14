// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

type drillStore struct{ rec hardening.DrillRecord }

func (d drillStore) LastPass(_ context.Context, k hardening.DrillKey) (hardening.DrillRecord, bool, error) {
	return d.rec, d.rec.ID != "" && k.Version == version.Version, nil
}

// H5 (Task 53): the Evaluator reads the minter's role table, the live settings, and the broker's
// drills.
func TestReadinessReadsTheRoleTable(t *testing.T) {
	cfg := &config.Config{RunTokens: config.RunTokens{PerRun: true}}
	cfg.Hardening.ApplyDefaults()
	cfg.Hardening.Publisher.Minter = true
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	grants := func(string) (map[string]map[string]string, map[string]string) {
		return map[string]map[string]string{"implementer": {"contents": "read"}}, map[string]string{"pull_requests": "write"}
	}
	e := hardeningEvaluator(func() *config.Config { return cfg }, grants,
		drillStore{hardening.DrillRecord{ID: "drill-1", At: now.Add(-time.Hour)}}, func() time.Time { return now })

	r, err := e.Hardened(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	// Under the default (audit): the table reads clean, the drill passes, mode and the layers and
	// reads do not.
	for _, m := range []hardening.Missing{hardening.MissingWriteToken, hardening.MissingPublisher, hardening.MissingDrill} {
		if slices.Contains(r.Missing, m) {
			t.Errorf("%s missing: %v", m, r.Missing)
		}
	}
	if !slices.Contains(r.Missing, hardening.MissingEnforce) {
		t.Fatalf("%v", r.Missing)
	}
}

// AC9, offline (HR41): execute's gate is wired to class authority and readiness, both asked at
// every admission. Each of R1 to R7 made false refuses the next ReadyForExecute; a repository both
// authoritative and hardened passes.
func TestPrerequisitesWired(t *testing.T) {
	cfg := &config.Config{Repositories: []config.Repository{{Name: "o/r", ClassAuthority: config.ClassAuthorityRepository}}}
	cfg.PolicyManagement.RepositoryAuthority = true
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	good := func() hardening.Settings {
		return hardening.Settings{Mode: hardening.ModeEnforce, Layers: hardening.LayersFor(hardening.ModeEnforce, hardening.ModeAudit),
			ReadFilter: true, DrillMaxAge: 168 * time.Hour, ConfigHash: "h", Version: version.Version,
			TokenSource: hardening.TokenSourceMinter, PublisherMinter: true}
	}
	read := map[string]string{"contents": "read"}
	roles := map[string]map[string]string{}
	for _, r := range hardening.SandboxRoles() {
		roles[r] = read
	}
	pub := map[string]string{"pull_requests": "write", "contents": "write"}
	type knobs struct {
		s     hardening.Settings
		roles map[string]map[string]string
		pub   map[string]string
		drill hardening.DrillRecord
	}
	gate := func(k knobs) error {
		e := hardening.Evaluator{Settings: func(string) hardening.Settings { return k.s },
			Grants: func(string) (map[string]map[string]string, map[string]string) { return k.roles, k.pub },
			Drills: drillStore{k.drill}, Now: func() time.Time { return now }}
		p := coordinationPrerequisites(cfg, e)
		return coordination.ReadyForExecute(t.Context(), p, []string{"o/r"})
	}
	ok := func() knobs {
		return knobs{good(), roles, pub, hardening.DrillRecord{ID: "drill-1", At: now.Add(-time.Hour)}}
	}
	if err := gate(ok()); err != nil {
		t.Fatalf("authoritative and hardened: %v", err)
	}
	for name, mut := range map[string]func(*knobs){
		"R1 mode audit":             func(k *knobs) { k.s.Mode = hardening.ModeAudit },
		"R2 a writing sandbox role": func(k *knobs) { k.roles = map[string]map[string]string{"implementer": {"contents": "write"}} },
		"R3 no publisher write":     func(k *knobs) { k.pub = read },
		"R4 a layer in audit":       func(k *knobs) { k.s.Layers.Paths = hardening.ModeAudit },
		"R5 reads unfiltered":       func(k *knobs) { k.s.ReadFilter = false },
		"R6 a stale drill":          func(k *knobs) { k.drill.At = now.Add(-200 * time.Hour) },
		"R7 the demo":               func(k *knobs) { k.s.Demo = true },
	} {
		t.Run(name, func(t *testing.T) {
			k := ok()
			mut(&k)
			if err := gate(k); !errors.Is(err, coordination.ErrPrerequisites) {
				t.Fatalf("%v", err)
			}
		})
	}
	cfg.PolicyManagement.RepositoryAuthority = false
	if err := gate(ok()); !errors.Is(err, coordination.ErrPrerequisites) || !strings.Contains(err.Error(), "class authority") {
		t.Fatalf("no class authority: %v", err)
	}
	// The wiring reports readiness for repositories at audit or above only.
	cfg.Hardening = config.Hardening{Repositories: map[string]config.HardeningRepository{"o/r": {Mode: hardening.ModeAudit}}}
	cfg.Repositories = append(cfg.Repositories, config.Repository{Name: "o/off"})
	if got := readinessReporter(func() *config.Config { return cfg }, nil, nil, nil).Repositories(); !slices.Equal(got, []string{"o/r"}) {
		t.Fatalf("%v", got)
	}
}

// HR40: the applier's checker promotes T5 for a repository's promoted types, read live, and only
// at mode enforce.
func TestApplierCheckerPromotesT5(t *testing.T) {
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR}}})
	cfg.Hardening.AgentGuard.URL = "http://agent-guard:8080"
	a, err := safeOutputApplier(cfg, applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg },
		Log: slog.New(slog.DiscardHandler)})
	if err != nil || a == nil {
		t.Fatalf("%v %v", a, err)
	}
	cfg.Hardening.Layers.ClassifierEnforce = []hardening.OutputType{hardening.PR}
	if got := a.Checker("Smana/ai-native-ref").ClassifierEnforce; len(got) != 0 {
		t.Fatalf("a repository at audit: %v", got)
	}
	r := cfg.Hardening.Repositories["Smana/ai-native-ref"]
	r.Mode = hardening.ModeEnforce
	cfg.Hardening.Repositories["Smana/ai-native-ref"] = r
	if got := a.Checker("Smana/ai-native-ref").ClassifierEnforce; !slices.Equal(got, []hardening.OutputType{hardening.PR}) {
		t.Fatalf("%v", got)
	}
}
