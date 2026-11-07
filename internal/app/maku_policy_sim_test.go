// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// simulationConfig is policyConfig with policyManagement.simulation on.
func simulationConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "factory.yaml")
	if err := os.WriteFile(p, append(raw, []byte("policyManagement: {simulation: true}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func exportedPolicy(t *testing.T, cfg string) string {
	t.Helper()
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).run(t.Context(), []string{"policy", "export", "--config", cfg, "--repository", policyRepo(t)}); err != nil {
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
	cfg := simulationConfig(t)
	policy := exportedPolicy(t, cfg)
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "simulate", "--config", cfg,
		"--repository", policyRepo(t), policy, fixtures + "01-arms.json", fixtures + "16-docs-unarmed.json"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 || strings.Count(out.String(), "unchanged") != 2 {
		t.Fatalf("a header and two unchanged rows:\n%s", out.String())
	}
}

func TestPolicyDiff(t *testing.T) {
	cfg := simulationConfig(t)
	old := exportedPolicy(t, cfg)
	raw, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "next.yaml")
	if err := os.WriteFile(next, []byte(strings.Replace(string(raw), "mode: shadow", "mode: live", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).run(t.Context(), []string{"policy", "diff", "--config", cfg, "--repository", policyRepo(t), old, next}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "relaxation") || !strings.Contains(out.String(), "mode raised") {
		t.Fatalf("%s", out.String())
	}
}

// The switch a simulator that disagrees with the gate turns off (stage 2).
func TestSimulateAndDiffNeedTheSwitch(t *testing.T) {
	policy := exportedPolicy(t, policyConfig)
	for _, verb := range []string{"simulate", "diff"} {
		var out bytes.Buffer
		err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", verb, "--config", policyConfig,
			"--repository", policyRepo(t), policy, policy})
		if err == nil || !strings.Contains(err.Error(), "policyManagement.simulation") {
			t.Fatalf("%s: got %v", verb, err)
		}
	}
}
