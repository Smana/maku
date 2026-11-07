// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// conformanceCfg is the config the fixtures under testdata/conformance were decided under.
func conformanceCfg() *config.Config {
	c := gateInputs().Cfg
	c.Factory = "aws-0"
	c.HarnessProfiles = map[string]config.HarnessProfile{"plain": {}}
	return c
}

func conformanceCases(t *testing.T) []Case {
	t.Helper()
	paths, err := filepath.Glob("testdata/conformance/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	var out []Case
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		c, err := ReadCase(raw)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out = append(out, c)
	}
	return out
}

// exported is the central config's classes through the migration's round trip: Export, Marshal,
// Parse, Validate and Resolve, as a repository file the factory reads.
func exported(t *testing.T, c *config.Config) map[string]mergepolicy.Resolved {
	t.Helper()
	f, err := mergepolicy.Export(c.RepositoryFor(""), c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := mergepolicy.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = mergepolicy.Parse(raw); err != nil {
		t.Fatal(err)
	}
	if err := mergepolicy.Validate(f, c); err != nil {
		t.Fatal(err)
	}
	out := map[string]mergepolicy.Resolved{}
	for name, p := range f.Classes {
		if out[name], err = mergepolicy.Resolve(name, p, c); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Stage 2's exit, offline: every fixture still decides as recorded (the runtime has not moved),
// and the central classes exported as a repository policy decide every fixture the same way, the
// final push provenance included (the simulator is the gate).
func TestConformance(t *testing.T) {
	c := conformanceCfg()
	for _, row := range Simulate(c, exported(t, c), conformanceCases(t)) {
		if !row.Conforms {
			t.Errorf("%s: the gate now decides %+v: rewrite the fixture or find the regression", row.Name, row.Central)
		}
		if row.Change != ChangeUnchanged {
			t.Errorf("%s: central %+v, exported %+v", row.Name, row.Central, row.Repository)
		}
	}
}

// A policy that differs shows in the change column.
func TestSimulateNamesTheChange(t *testing.T) {
	c := conformanceCfg()
	policy := exported(t, c)
	loose := policy["dead-links"]
	loose.Class.MaxModifiedLines = 0 // no line cap: 21 lines would merge
	loose.Approval = []string{"^README"}
	policy["dead-links"] = loose
	delete(policy, "docs")
	got := map[string]string{}
	for _, row := range Simulate(c, policy, conformanceCases(t)) {
		got[row.Name] = row.Change
	}
	for name, want := range map[string]string{
		"arms":                   ChangeUnchanged,
		"too-many-lines":         ChangeNewlyMerged,
		"a-file-outside-allowed": ChangeUnchanged, // refused by allowedFiles before the subset
		"docs-unarmed":           ChangeUnchanged, // absent from the policy: still no class
	} {
		if got[name] != want {
			t.Errorf("%s: got %q, want %q", name, got[name], want)
		}
	}
	policy["dead-links"] = mergepolicy.Resolved{Class: loose.Class, Approval: []string{"^website/"}}
	for _, row := range Simulate(c, policy, conformanceCases(t)[:1]) {
		if row.Change != ChangeNewlyApproval {
			t.Errorf("%s: %q", row.Name, row.Change)
		}
	}
	strict := policy["dead-links"]
	strict.Approval, strict.Class.MaxModifiedLines = nil, 1
	policy["dead-links"] = strict
	if row := Simulate(c, policy, conformanceCases(t)[:1])[0]; row.Change != ChangeNewlyBlocked {
		t.Errorf("%s: %q", row.Name, row.Change)
	}
}

func TestReadCaseRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"not json":       {"{", "case"},
		"a later format": {`{"version": 2, "name": "x"}`, "version 2"},
		"no task":        {`{"version": 1, "name": "x", "pushed": "factory", "inputs": {}}`, "Task"},
		"bad pushed":     {`{"version": 1, "name": "x", "pushed": "who", "inputs": {"Task": {}}}`, "pushed"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadCase([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
