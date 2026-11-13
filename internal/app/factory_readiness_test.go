// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// branchFiles is a repository's default branch as its factory-App connection reads it.
type branchFiles map[string]string

func (b branchFiles) DefaultBranchFile(_ context.Context, path string) (string, string, bool, error) {
	t, ok := b[path]
	return t, "sha", ok, nil
}

type drillStore struct{ rec hardening.DrillRecord }

func (d drillStore) LastPass(_ context.Context, k hardening.DrillKey) (hardening.DrillRecord, bool, error) {
	return d.rec, d.rec.ID != "" && k.Version == version.Version, nil
}

// H5 (Task 53): the Evaluator reads each repository's policy files through that repository's own
// factory-App connection (a missing file is no policy, a missing connection an error), the
// minter's role table for a run-controller repository, the live settings, and the broker's drills.
func TestPoliciesPerRunPath(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hardening.ApplyDefaults()
	readPolicy := "permissions:\n  contents: read\n"
	pubPolicy := "permissions:\n  pull_requests: write\n"
	files := map[string]policyReader{"o/xr": branchFiles{
		hardening.RolePolicyPath("implementer"): readPolicy, hardening.PublisherPolicyPath: pubPolicy}}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	grants := func(string) (map[string]map[string]string, map[string]string) {
		return map[string]map[string]string{"implementer": {"contents": "read"}}, map[string]string{"pull_requests": "write"}
	}
	e := hardeningEvaluator(func() *config.Config { return cfg }, func(repo string) policyReader { return files[repo] },
		grants, drillStore{hardening.DrillRecord{ID: "drill-1", At: now.Add(-time.Hour)}}, func() time.Time { return now })

	r, err := e.Hardened(t.Context(), "o/xr")
	if err != nil {
		t.Fatal(err)
	}
	// Under the default (audit, the XR): the policies read clean, the drill passes, mode and the
	// layers and reads do not.
	for _, m := range []hardening.Missing{hardening.MissingWriteToken, hardening.MissingPublisher, hardening.MissingDrill} {
		if slices.Contains(r.Missing, m) {
			t.Errorf("%s missing: %v", m, r.Missing)
		}
	}
	if !slices.Contains(r.Missing, hardening.MissingEnforce) {
		t.Fatalf("%v", r.Missing)
	}
	// A repository with no connection is a fact that could not be read: an error, a refusal.
	if _, err := e.Hardened(t.Context(), "o/none"); err == nil || !strings.Contains(err.Error(), "o/none") {
		t.Fatalf("no connection: %v", err)
	}
	var bf hardening.PolicyFiles = policyFiles(func(string) policyReader { return branchFiles{} })
	if b, ok, err := bf.DefaultBranchFile(t.Context(), "o/r", hardening.PublisherPolicyPath); err != nil || ok || b != nil {
		t.Fatalf("a missing file: %q %v %v", b, ok, err)
	}
	if _, _, err := policyFiles(func(string) policyReader { return nil }).DefaultBranchFile(t.Context(), "o/r", "x"); !errors.Is(err, errNoConnection) {
		t.Fatalf("%v", err)
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
