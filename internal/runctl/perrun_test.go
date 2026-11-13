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
	c.GitTokens, c.GitReadSecret = GitTokensPerRun, ""
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

func TestLoadConfigGitTokens(t *testing.T) {
	noRead := strings.Replace(goodConfig, "gitReadSecret: agent-git-read\n", "", 1)
	for name, tc := range map[string]struct {
		file, want, err string
	}{
		"default":           {goodConfig, GitTokensShared, ""},
		"shared":            {goodConfig + "gitTokens: shared\n", GitTokensShared, ""},
		"perRun":            {noRead + "gitTokens: perRun\n", GitTokensPerRun, ""},
		"perRun, no shared": {strings.Replace(noRead, "gitSecret: agent-git\n", "", 1) + "gitTokens: perRun\n", GitTokensPerRun, ""},
		"perRun with read":  {goodConfig + "gitTokens: perRun\n", "", "gitTokens perRun and gitReadSecret: choose one"},
		"perRun with unsafe": {noRead + "gitTokens: perRun\nunsafeSharedGitToken: true\n", "",
			"gitTokens perRun and unsafeSharedGitToken: choose one"},
		"unknown": {goodConfig + "gitTokens: mine\n", "", `gitTokens "mine" must be "shared" or "perRun"`},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := cfgFrom(t, tc.file)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || c.GitTokens != tc.want {
				t.Fatalf("gitTokens %q err %v, want %q", c.GitTokens, err, tc.want)
			}
		})
	}
}

// The preflight has no shared Secret to wait for in perRun mode: each run's is checked at its start.
func TestPreflightPerRunSkipsGitSecrets(t *testing.T) {
	p := &Preflight{Cfg: perRunConfig(t)}
	if slices.ContainsFunc(p.checks(), func(c check) bool { return c.name == "gitSecrets" }) {
		t.Fatal("perRun: the gitSecrets check still runs")
	}
}
