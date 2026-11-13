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
