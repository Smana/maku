// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

func harnessEnv(p *corev1.Pod) map[string]string {
	out := map[string]string{}
	for _, e := range p.Spec.Containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

// v3 feature 3: spec.loop.checks hands the harness AGENT_CHECKS=1 and the pod's deadline; without it
// the pod is today's.
func TestLoopChecksReachTheHarness(t *testing.T) {
	on := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Loop.Checks = true; r.Spec.Budget.MaxMinutes = 45 })
	env := harnessEnv(Pod(on, testConfig(t), testModel, "abcdefgh"))
	if env["AGENT_CHECKS"] != "1" || env["AGENT_RUN_SECONDS"] != "2700" {
		t.Fatalf("AGENT_CHECKS %q AGENT_RUN_SECONDS %q, want 1 and 2700", env["AGENT_CHECKS"], env["AGENT_RUN_SECONDS"])
	}
	off := harnessEnv(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"))
	for _, k := range []string{"AGENT_CHECKS", "AGENT_RUN_SECONDS"} {
		if _, ok := off[k]; ok {
			t.Fatalf("%s set on a run without checks", k)
		}
	}
}

// RC7: a GitHub release download lands on release-assets, which a check run reaches during its
// install; Go's archive is on dl.google.com, the golang profile's.
func TestTheDefaultProfilesReachTheChecksHosts(t *testing.T) {
	c := testConfig(t)
	r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.Loop.Checks = true })
	if fqdns := c.FQDNs(r); !slices.Contains(fqdns, "dl.google.com") {
		t.Fatalf("dl.google.com missing from %v", fqdns)
	}
	if install := c.InstallFQDNs(r); !slices.Contains(install, "release-assets.githubusercontent.com") {
		t.Fatalf("release-assets missing from the install set %v", install)
	}
}
