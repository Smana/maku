// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var testModel = Model{BaseURL: "https://api.anthropic.com/v1", Name: "claude-sonnet-5-5", Host: "api.anthropic.com", Port: 443}

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs; rerun with -update and review the diff:\n%s", name, got)
	}
}

func goRun(mut ...func(*v1alpha1.AgentRun)) *v1alpha1.AgentRun {
	return run("run-abcdefgh", append([]func(*v1alpha1.AgentRun){func(r *v1alpha1.AgentRun) {
		r.Spec.Egress.Profiles = []v1alpha1.AgentRunEgressProfile{"golang"}
	}}, mut...)...)
}

func TestPodGolden(t *testing.T) {
	r := goRun()
	golden(t, "pod.golden.yaml", Pod(r, testConfig(t), testModel, "abcdefgh"))
}

func TestOpenCodePodGolden(t *testing.T) {
	c := testConfig(t)
	c.Images.Opencode = "ghcr.io/smana/maku-opencode:v0.16.0"
	r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.Harness = "opencode" })
	golden(t, "pod-opencode.golden.yaml", Pod(r, c, testModel, "abcdefgh"))
}

func TestOpenCodeRunUsesItsOwnImageAndDriver(t *testing.T) {
	c := testConfig(t)
	c.Images.Opencode = "ghcr.io/smana/maku-opencode:v0.16.0"
	r := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Harness = "opencode" })
	p := Pod(r, c, testModel, "abcdefgh")
	if p.Spec.Containers[0].Image != c.Images.Opencode {
		t.Fatalf("harness image = %q, want %q", p.Spec.Containers[0].Image, c.Images.Opencode)
	}
	if got := harnessEnv(p)["AGENT_HARNESS"]; got != "opencode" {
		t.Fatalf("harness AGENT_HARNESS = %q, want opencode", got)
	}
	if got := harnessEnv(p)["HOME"]; got != "/workspace/.opencode/home" {
		t.Fatalf("harness HOME = %q, want /workspace/.opencode/home", got)
	}
	bridgeEnv := map[string]string{}
	for _, e := range p.Spec.InitContainers[0].Env {
		bridgeEnv[e.Name] = e.Value
	}
	if got := bridgeEnv["AGENT_HARNESS"]; got != "opencode" {
		t.Fatalf("bridge AGENT_HARNESS = %q, want opencode", got)
	}
	var mountedWorkspace bool
	for _, m := range p.Spec.InitContainers[0].VolumeMounts {
		if m.Name == "workspace" && m.MountPath == "/workspace" && m.ReadOnly {
			mountedWorkspace = true
		}
	}
	if !mountedWorkspace {
		t.Fatal("opencode room-bridge must mount workspace read-only at /workspace")
	}
	// Default (empty) and explicit openhands runs keep the OpenHands image and omit AGENT_HARNESS.
	for _, h := range []string{"", "openhands"} {
		def := Pod(run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Harness = h }), c, testModel, "abcdefgh")
		if def.Spec.Containers[0].Image != c.Images.Harness {
			t.Fatalf("harness %q image = %q, want %q", h, def.Spec.Containers[0].Image, c.Images.Harness)
		}
		if _, ok := harnessEnv(def)["AGENT_HARNESS"]; ok {
			t.Fatalf("harness %q should not set AGENT_HARNESS", h)
		}
	}
}

func TestCNPGolden(t *testing.T) {
	r := goRun()
	golden(t, "cnp.golden.yaml", NetworkPolicy(r, testConfig(t), testModel, "abcdefgh").Object)
}

// The properties a reviewer must not lose in a golden refresh.
func TestPodInvariants(t *testing.T) {
	c := testConfig(t)
	p := Pod(run("run-abcdefgh"), c, testModel, "abcdefgh")
	if p.Name != "run-abcdefgh" || p.Spec.ServiceAccountName != "run-abcdefgh" {
		t.Fatalf("name %q sa %q", p.Name, p.Spec.ServiceAccountName)
	}
	if p.Spec.RuntimeClassName == nil || *p.Spec.RuntimeClassName != "gvisor" {
		t.Fatal("RuntimeClass must be the configured one")
	}
	if p.Spec.ActiveDeadlineSeconds == nil || *p.Spec.ActiveDeadlineSeconds != 30*60 {
		t.Fatal("deadline must be maxMinutes")
	}
	if len(p.Spec.InitContainers) != 2 || p.Spec.InitContainers[0].Name != "room-bridge" || p.Spec.InitContainers[1].Name != "room-gate" {
		t.Fatalf("init containers %v", p.Spec.InitContainers)
	}
	harness := p.Spec.Containers[0]
	for _, m := range harness.VolumeMounts {
		if m.Name == "room-token" {
			t.Fatal("only the bridge mounts the room token")
		}
	}
	env := map[string]string{}
	for _, e := range harness.Env {
		env[e.Name] = e.Value
	}
	if _, ok := env["MCP_URL"]; ok {
		t.Fatal("no MCP_URL: no room_* tools on this runtime")
	}
	if env["LLM_API_KEY_FILE"] != "/run/agent/model/api_key" || env["GIT_TOKEN_FILE"] != "/run/agent/github/token" || env["LLM_BASE_URL"] != testModel.BaseURL {
		t.Fatalf("harness env %v", env)
	}
	noRoom := Pod(run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = "" }), c, testModel, "abcdefgh")
	if len(noRoom.Spec.InitContainers) != 0 {
		t.Fatal("a run without a room has no bridge")
	}
	tasked := Pod(run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Labels = map[string]string{LabelTask: "T-7"} }), c, testModel, "abcdefgh")
	var taskID string
	for _, e := range tasked.Spec.Containers[0].Env {
		if e.Name == "TASK_ID" {
			taskID = e.Value
		}
	}
	if taskID != "T-7" || tasked.Labels[LabelTask] != "T-7" {
		t.Fatalf("task label must reach the env and the labels: %q %v", taskID, tasked.Labels)
	}
	c.RuntimeClassName, c.AllowUnsandboxed = "", true
	if Pod(run("run-abcdefgh"), c, testModel, "abcdefgh").Spec.RuntimeClassName != nil {
		t.Fatal("allowUnsandboxed: no RuntimeClass")
	}
}

func TestEffortReachesTheHarness(t *testing.T) {
	withEffort := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Effort = "medium" })
	if got := harnessEnv(Pod(withEffort, testConfig(t), testModel, "abcdefgh"))["LLM_REASONING_EFFORT"]; got != "medium" {
		t.Fatalf("LLM_REASONING_EFFORT = %q, want medium", got)
	}
	without := harnessEnv(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"))
	if value, ok := without["LLM_REASONING_EFFORT"]; ok {
		t.Fatalf("no spec.effort: LLM_REASONING_EFFORT = %q, want absent", value)
	}
}

// An ingress section is what enables ingress default-deny in Cilium; a missing one leaves the
// pod open to every peer.
func TestCNPAlwaysDeniesIngress(t *testing.T) {
	for _, room := range []string{"", "roomroom"} {
		r := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = room })
		spec := NetworkPolicy(r, testConfig(t), testModel, "abcdefgh").Object["spec"].(map[string]any)
		in, ok := spec["ingress"].([]any)
		if !ok || len(in) != 1 {
			t.Fatalf("room %q: ingress %v", room, spec["ingress"])
		}
		if _, hostOnly := in[0].(map[string]any)["fromEntities"]; hostOnly != (room != "") {
			t.Fatalf("room %q: the host rule belongs to the bridge's probe only: %v", room, in[0])
		}
	}
}

// An in-cluster model is reached by pod selector on the backend port: Cilium translates the
// Service before policy, so a toFQDNs rule on the ClusterIP would never match.
func TestCNPInClusterModel(t *testing.T) {
	m := Model{BaseURL: "http://stub-model.e2e.svc.cluster.local:8080/v1", Name: "stub", Host: "stub-model.e2e.svc.cluster.local", Port: 8080}
	spec := NetworkPolicy(goRun(), testConfig(t), m, "abcdefgh").Object["spec"].(map[string]any)
	var found bool
	for _, e := range spec["egress"].([]any) {
		rule := e.(map[string]any)
		ports, _ := rule["toPorts"].([]any)
		if len(ports) == 0 || ports[0].(map[string]any)["ports"] == nil {
			continue
		}
		if p := ports[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"]; p != "8080" {
			continue
		}
		found = true
		if _, fq := rule["toFQDNs"]; fq {
			t.Fatalf("in-cluster model must not be a toFQDNs rule: %v", rule)
		}
		sel := rule["toEndpoints"].([]any)[0].(map[string]any)["matchLabels"].(map[string]any)
		if sel["io.kubernetes.pod.namespace"] != "e2e" {
			t.Fatalf("selector %v", sel)
		}
	}
	if !found {
		t.Fatalf("no egress rule on 8080: %v", spec["egress"])
	}
	// Still resolvable, and never in the 443 list.
	dns := spec["egress"].([]any)[0].(map[string]any)["toPorts"].([]any)[0].(map[string]any)["rules"].(map[string]any)["dns"].([]any)
	if !slices.ContainsFunc(dns, func(d any) bool { return d.(map[string]any)["matchName"] == m.Host }) {
		t.Fatalf("DNS must allow the model name: %v", dns)
	}
	if _, ok := inClusterNamespace("api.anthropic.com"); ok {
		t.Fatal("an external host is not in-cluster")
	}
}

func TestPodDraftAndRoutineEnv(t *testing.T) {
	r := run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
		r.Spec.PRDraft = true
		r.Annotations = map[string]string{
			"agents.ogenki.io/routine":            "dead-code",
			"agents.ogenki.io/routine-prompt-sha": "0123456789ab",
		}
	})
	env := harnessEnv(Pod(r, testConfig(t), testModel, "abcdefgh"))
	if env["AGENT_PR_DRAFT"] != "1" || env["AGENT_ROUTINE"] != "dead-code" || env["AGENT_ROUTINE_PROMPT_SHA"] != "0123456789ab" {
		t.Fatalf("unexpected draft/routine env: %v", env)
	}

	plain := harnessEnv(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"))
	for _, k := range []string{"AGENT_PR_DRAFT", "AGENT_ROUTINE", "AGENT_ROUTINE_PROMPT_SHA"} {
		if _, ok := plain[k]; ok {
			t.Fatalf("plain run unexpectedly set %s=%q", k, plain[k])
		}
	}

	bad := harnessEnv(Pod(run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
		r.Annotations = map[string]string{
			"agents.ogenki.io/routine":            "BAD\nINJECT",
			"agents.ogenki.io/routine-prompt-sha": "not-hex",
		}
	}), testConfig(t), testModel, "abcdefgh"))
	for _, k := range []string{"AGENT_ROUTINE", "AGENT_ROUTINE_PROMPT_SHA"} {
		if _, ok := bad[k]; ok {
			t.Fatalf("invalid annotation leaked to %s=%q", k, bad[k])
		}
	}
}
