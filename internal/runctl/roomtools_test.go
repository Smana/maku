// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const roomToolsURL = "http://room-broker.agent-platform.svc.cluster.local:8090/mcp"

// Task 6: roomToolsURL is the broker's :8090, by the broker's own host, so the run CNP's broker
// rule and its DNS allowlist already cover it.
func TestLoadConfigRoomToolsURL(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+"roomToolsURL: "+roomToolsURL+"\n")
	if err != nil || c.RoomToolsURL != roomToolsURL {
		t.Fatalf("%q, %v", c.RoomToolsURL, err)
	}
	for name, u := range map[string]string{
		"another host":   "http://elsewhere.agent-platform.svc.cluster.local:8090/mcp",
		"a short name":   "http://room-broker.agent-platform.svc:8090/mcp",
		"no path":        "http://room-broker.agent-platform.svc.cluster.local:8090",
		"another scheme": "ftp://room-broker.agent-platform.svc.cluster.local:8090/mcp",
		"userinfo":       "http://u:p@room-broker.agent-platform.svc.cluster.local:8090/mcp",
	} {
		if _, err := cfgFrom(t, goodConfig+"roomToolsURL: "+u+"\n"); err == nil || !strings.Contains(err.Error(), "roomToolsURL") {
			t.Errorf("%s: want a roomToolsURL refusal, got %v", name, err)
		}
	}
}

func withRoomTools(t *testing.T) Config {
	t.Helper()
	c, err := cfgFrom(t, goodConfig+"roomToolsURL: "+roomToolsURL+"\n")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// With roomToolsURL the harness gets MCP_URL and a token of its own, audience room-tools, mounted in
// the harness alone: the bridge's room-broker token, which opens :8443, stays the bridge's.
func TestRoomToolsReachTheHarness(t *testing.T) {
	p := Pod(run("run-abcdefgh"), withRoomTools(t), testModel, "abcdefgh")
	env := harnessEnv(p)
	if env["MCP_URL"] != roomToolsURL || env["MCP_TOKEN_FILE"] != "/var/run/secrets/agents/room-tools/token" {
		t.Fatalf("harness env %v", env)
	}
	var vol *corev1.Volume
	for i := range p.Spec.Volumes {
		if p.Spec.Volumes[i].Name == "room-tools-token" {
			vol = &p.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Projected == nil || len(vol.Projected.Sources) != 1 {
		t.Fatalf("room-tools-token volume %+v", vol)
	}
	sa := vol.Projected.Sources[0].ServiceAccountToken
	if sa == nil || sa.Audience != "room-tools" || sa.Path != "token" || sa.ExpirationSeconds == nil || *sa.ExpirationSeconds != roomTokenTTL {
		t.Fatalf("projected token %+v", sa)
	}
	mounts := func(c corev1.Container) map[string]string {
		out := map[string]string{}
		for _, m := range c.VolumeMounts {
			out[m.Name] = m.MountPath
		}
		return out
	}
	if m := mounts(p.Spec.Containers[0]); m["room-tools-token"] != "/var/run/secrets/agents/room-tools" || m["room-token"] != "" {
		t.Fatalf("harness mounts %v", m)
	}
	for _, c := range p.Spec.InitContainers {
		if _, ok := mounts(c)["room-tools-token"]; ok {
			t.Fatalf("%s mounts the room tools token", c.Name)
		}
	}

	// Unset, or a run with no room (room tools act on the run's room): today's pod.
	for name, p := range map[string]*corev1.Pod{
		"unset":   Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"),
		"no room": Pod(run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = "" }), withRoomTools(t), testModel, "abcdefgh"),
	} {
		env := harnessEnv(p)
		if _, ok := env["MCP_URL"]; ok {
			t.Errorf("%s: MCP_URL %q", name, env["MCP_URL"])
		}
		for _, v := range p.Spec.Volumes {
			if v.Name == "room-tools-token" {
				t.Errorf("%s: a room tools token", name)
			}
		}
	}
}

// The run CNP lets the pod reach the broker on roomToolsURL's port too, beside 8443.
func TestCNPRoomTools(t *testing.T) {
	ports := func(c Config) string {
		spec := NetworkPolicy(run("run-abcdefgh"), c, testModel, "abcdefgh").Object["spec"].(map[string]any)
		for _, e := range spec["egress"].([]any) {
			rule := e.(map[string]any)
			if _, ok := rule["toEndpoints"]; !ok {
				continue
			}
			sel := rule["toEndpoints"].([]any)[0].(map[string]any)["matchLabels"].(map[string]any)
			if sel["io.kubernetes.pod.namespace"] != "agent-platform" {
				continue
			}
			var out []string
			for _, tp := range rule["toPorts"].([]any) {
				for _, p := range tp.(map[string]any)["ports"].([]any) {
					out = append(out, p.(map[string]any)["port"].(string))
				}
			}
			return strings.Join(out, ",")
		}
		return ""
	}
	if got := ports(testConfig(t)); got != "8443" {
		t.Fatalf("unset: broker ports %q", got)
	}
	if got := ports(withRoomTools(t)); got != "8443,8090" {
		t.Fatalf("roomToolsURL: broker ports %q", got)
	}
}

func bridgeEnvOf(p *corev1.Pod) map[string]string {
	out := map[string]string{}
	for _, c := range p.Spec.InitContainers {
		if c.Name == "room-bridge" {
			for _, e := range c.Env {
				out[e.Name] = e.Value
			}
		}
	}
	return out
}

// Task 7: the harness calls the broker directly, so its room tools reach the bridge bare. With
// roomToolsURL the bridge is told the server and the read-only set; without, neither.
// HR14: a run whose claim names safe outputs reaches the output tools through the bridge as
// room-only appends, and the harness's gh wrapper reads SAFE_OUTPUTS; any other run is today's.
func TestRunctlReadOnlyTools(t *testing.T) {
	const tools = "room-broker__room_read,room-broker__room_post,room-broker__room_progress,room-broker__room_handoff,room-broker__room_verdict"
	enforced := run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
		r.Annotations = map[string]string{"agents.ogenki.io/safe-outputs": "comment,pr"}
	})
	p := Pod(enforced, withRoomTools(t), testModel, "abcdefgh")
	if got := bridgeEnvOf(p)["MCP_READ_ONLY_TOOLS"]; got != tools+",room-broker__output_comment,room-broker__output_pr" {
		t.Fatalf("MCP_READ_ONLY_TOOLS %q", got)
	}
	if got := harnessEnv(p)["SAFE_OUTPUTS"]; got != "comment,pr" {
		t.Fatalf("SAFE_OUTPUTS %q", got)
	}
	p = Pod(run("run-abcdefgh"), withRoomTools(t), testModel, "abcdefgh")
	if got := bridgeEnvOf(p)["MCP_READ_ONLY_TOOLS"]; got != tools {
		t.Fatalf("not enforced: MCP_READ_ONLY_TOOLS %q", got)
	}
	if v, ok := harnessEnv(p)["SAFE_OUTPUTS"]; ok {
		t.Fatalf("not enforced: SAFE_OUTPUTS=%q", v)
	}
	// A type the factory never writes is not served; push is, from stage H3.
	pushed := run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
		r.Annotations = map[string]string{"agents.ogenki.io/safe-outputs": "push,pr,review"}
	})
	if got := harnessEnv(Pod(pushed, withRoomTools(t), testModel, "abcdefgh"))["SAFE_OUTPUTS"]; got != "pr,push" {
		t.Fatalf("SAFE_OUTPUTS %q", got)
	}
}

func TestRoomToolsPassTheBridgesPolicy(t *testing.T) {
	env := bridgeEnvOf(Pod(run("run-abcdefgh"), withRoomTools(t), testModel, "abcdefgh"))
	const tools = "room-broker__room_read,room-broker__room_post,room-broker__room_progress,room-broker__room_handoff,room-broker__room_verdict"
	if env["MCP_BARE_SERVER"] != "room-broker" || env["MCP_READ_ONLY_TOOLS"] != tools {
		t.Fatalf("bridge env %v", env)
	}
	env = bridgeEnvOf(Pod(run("run-abcdefgh"), testConfig(t), testModel, "abcdefgh"))
	for _, k := range []string{"MCP_BARE_SERVER", "MCP_READ_ONLY_TOOLS"} {
		if v, ok := env[k]; ok {
			t.Errorf("unset: %s=%q", k, v)
		}
	}
}
