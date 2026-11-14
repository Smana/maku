// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// simulationArgs is boundManifests with policyManagement.simulation on.
func simulationArgs(t *testing.T) []string {
	t.Helper()
	args := boundManifests(t)
	raw, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[1], append(raw, []byte("policyManagement: {simulation: true}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	return args
}

// exportedPolicy is the bound repository's file as `policy export` writes it (B16).
func exportedPolicy(t *testing.T, args []string) string {
	t.Helper()
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).run(t.Context(), append([]string{"policy", "export"}, args...)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	p := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const fixtures = "../factory/reconciler/testdata/conformance/"

func TestPolicySimulate(t *testing.T) {
	args := simulationArgs(t)
	policy := exportedPolicy(t, args)
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), append(append([]string{"policy", "simulate"}, args...),
		policy, fixtures+"01-arms.json", fixtures+"16-docs-unarmed.json"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 || strings.Count(out.String(), "unchanged") != 2 {
		t.Fatalf("a header and two unchanged rows:\n%s", out.String())
	}
}

func TestPolicyDiff(t *testing.T) {
	args := simulationArgs(t)
	old := exportedPolicy(t, args)
	raw, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "next.yaml")
	if err := os.WriteFile(next, []byte(strings.Replace(string(raw), "mode: shadow", "mode: live", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).run(t.Context(), append(append([]string{"policy", "diff"}, args...), old, next)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "relaxation") || !strings.Contains(out.String(), "mode raised") {
		t.Fatalf("%s", out.String())
	}
}

// The switch a simulator that disagrees with the gate turns off (stage 2).
func TestSimulateAndDiffNeedTheSwitch(t *testing.T) {
	args := boundManifests(t)
	policy := exportedPolicy(t, args)
	for _, verb := range []string{"simulate", "diff"} {
		var out bytes.Buffer
		err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), append(append([]string{"policy", verb}, args...), policy, policy))
		if err == nil || !strings.Contains(err.Error(), "policyManagement.simulation") {
			t.Fatalf("%s: got %v", verb, err)
		}
	}
}
