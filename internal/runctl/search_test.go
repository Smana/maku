// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

func bridgeEnv(t *testing.T, r *v1alpha1.AgentRun) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range Pod(r, testConfig(t), testModel, "abcdefgh").Spec.InitContainers {
		if c.Name == "room-bridge" {
			for _, e := range c.Env {
				out[e.Name] = e.Value
			}
			return out
		}
	}
	t.Fatal("no room-bridge container")
	return nil
}

// v3 feature 5: spec.loop.search hands an implementer's harness and bridge AGENT_SEARCH=1; without it,
// or for another role, the pod is today's (the harness offers the tool to an implementer only).
func TestLoopSearchReachesTheHarnessAndTheBridge(t *testing.T) {
	on := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Loop.Search = true })
	if env := harnessEnv(Pod(on, testConfig(t), testModel, "abcdefgh")); env["AGENT_SEARCH"] != "1" {
		t.Fatalf("harness AGENT_SEARCH %q, want 1", env["AGENT_SEARCH"])
	}
	if env := bridgeEnv(t, on); env["AGENT_SEARCH"] != "1" {
		t.Fatalf("bridge AGENT_SEARCH %q, want 1", env["AGENT_SEARCH"])
	}
	off := run("run-abcdefgh")
	tester := run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
		r.Spec.Loop.Search = true
		r.Spec.Role = "tester"
	})
	for name, r := range map[string]*v1alpha1.AgentRun{"no search": off, "tester": tester} {
		if _, ok := harnessEnv(Pod(r, testConfig(t), testModel, "abcdefgh"))["AGENT_SEARCH"]; ok {
			t.Fatalf("%s: AGENT_SEARCH on the harness", name)
		}
		if _, ok := bridgeEnv(t, r)["AGENT_SEARCH"]; ok {
			t.Fatalf("%s: AGENT_SEARCH on the bridge", name)
		}
	}
}

func i32(v int32) *int32 { return &v }

// The loop policy reaches the harness as AGENT_LOOP, a JSON object of the fields the run sets; a
// judge also gets the pod's own model endpoint (no identity proxy here). No loop, no AGENT_LOOP.
func TestLoopPolicyReachesTheHarness(t *testing.T) {
	cases := map[string]struct {
		set       func(*v1alpha1.AgentRun)
		loop      string
		judgeBase bool
	}{
		"stall only":        {func(r *v1alpha1.AgentRun) { r.Spec.Loop.StallSteps = i32(90) }, `{"judgeModel":"none","stallSteps":90}`, false},
		"zero continuation": {func(r *v1alpha1.AgentRun) { r.Spec.Loop.Continuations = i32(0) }, `{"continuations":0,"judgeModel":"none"}`, false},
		"all three": {func(r *v1alpha1.AgentRun) {
			r.Spec.Loop = v1alpha1.AgentRunLoop{StallSteps: i32(60), Continuations: i32(3), JudgeModel: "tier-light"}
		}, `{"continuations":3,"judgeModel":"tier-light","stallSteps":60}`, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := harnessEnv(Pod(run("run-abcdefgh", c.set), testConfig(t), testModel, "abcdefgh"))
			if env["AGENT_LOOP"] != c.loop {
				t.Fatalf("AGENT_LOOP %q, want %q", env["AGENT_LOOP"], c.loop)
			}
			if c.judgeBase {
				if env["JUDGE_BASE_URL"] != env["LLM_BASE_URL"] || env["JUDGE_MODEL"] != env["MODEL"] || env["JUDGE_MODEL"] == "" {
					t.Fatalf("judge env %q %q, want the pod's base URL and model", env["JUDGE_BASE_URL"], env["JUDGE_MODEL"])
				}
			} else if _, ok := env["JUDGE_BASE_URL"]; ok {
				t.Fatal("JUDGE_BASE_URL without a judge")
			}
		})
	}
	for _, k := range []string{"AGENT_LOOP", "JUDGE_BASE_URL", "JUDGE_MODEL"} {
		if _, ok := harnessEnv(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"))[k]; ok {
			t.Fatalf("%s set with no loop", k)
		}
	}
}
