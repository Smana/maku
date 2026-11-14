// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func perRunConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t)
	c.GitSecret = "" // no laptop PAT: each run mounts its own
	return c
}

func podEnv(p *corev1.Pod, name string) (string, bool) {
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// D10: in perRun mode every role mounts its own run's Secret, and the harness revokes the token on exit.
func TestPerRunMountsTheRunsSecret(t *testing.T) {
	c := perRunConfig(t)
	for _, r := range []string{"implementer", "tester", "reviewer", "triager"} {
		p := Pod(run("run-abcdefgh", role(r)), c, testModel, "abcdefgh")
		if got := gitVolume(t, p); got != "run-abcdefgh-git" {
			t.Errorf("%s mounts %q, want run-abcdefgh-git", r, got)
		}
		if v, _ := podEnv(p, "GIT_TOKEN_REVOCABLE"); v != "1" {
			t.Errorf("%s: GIT_TOKEN_REVOCABLE %q, want 1", r, v)
		}
		if got := c.Invalid(run("run-abcdefgh", role(r))); got != "" {
			t.Errorf("%s needs no read Secret in perRun mode: %q", r, got)
		}
	}
	// The shared mode never sets it: a PAT is not revoked.
	if _, ok := podEnv(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"), "GIT_TOKEN_REVOCABLE"); ok {
		t.Fatal("shared mode sets GIT_TOKEN_REVOCABLE")
	}
}

func TestPodPerRunGolden(t *testing.T) {
	golden(t, "pod-perrun.golden.yaml", Pod(goRun(), perRunConfig(t), testModel, "abcdefgh"))
}

// Without gitSecret, the product path: every run mounts its own Secret.
func TestLoadConfigPerRunIsTheDefault(t *testing.T) {
	c, err := cfgFrom(t, strings.Replace(goodConfig, "gitSecret: agent-git\n", "", 1))
	if err != nil || !c.PerRun() {
		t.Fatalf("perRun %v err %v", c.PerRun(), err)
	}
	if c, err := cfgFrom(t, goodConfig); err != nil || c.PerRun() {
		t.Fatalf("gitSecret set: perRun %v err %v", c.PerRun(), err)
	}
}

// The preflight has no shared Secret to wait for in perRun mode: each run's is checked at its start.
func TestPreflightPerRunSkipsGitSecrets(t *testing.T) {
	p := &Preflight{Cfg: perRunConfig(t)}
	if slices.ContainsFunc(p.checks(), func(c check) bool { return c.name == "gitSecrets" }) {
		t.Fatal("perRun: the gitSecrets check still runs")
	}
}
