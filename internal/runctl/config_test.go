// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const goodConfig = `
namespace: agents
namePrefix: run-
runtimeClassName: gvisor
modelSecret: agent-model
gitSecret: agent-git
gitReadSecret: agent-git-read
broker:
  url: https://room-broker.agent-platform.svc.cluster.local:8443
  namespace: agent-platform
  labels: {app.kubernetes.io/name: room-broker}
  caSecret: room-broker-tls
images:
  harness: ghcr.io/smana/maku-harness:v0.16.0
  bridge: ghcr.io/smana/maku-bridge:v0.16.0
`

func cfgFrom(t *testing.T, body string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(p)
}

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := cfgFrom(t, goodConfig)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func run(name string, mut ...func(*v1alpha1.AgentRun)) *v1alpha1.AgentRun {
	r := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agents", UID: "uid-1"},
		Spec: v1alpha1.AgentRunSpec{Role: "implementer", Repository: "o/r", BaseRef: "main", Principal: "human:a",
			DataClass: "public", RoomRef: "roomroom", Task: v1alpha1.AgentRunTask{Text: "fix it"},
			Budget: v1alpha1.AgentRunBudget{MaxMinutes: 30}, Size: "small"}}
	for _, m := range mut {
		m(r)
	}
	return r
}

func TestLoadConfigDefaultsTheEgressProfiles(t *testing.T) {
	c := testConfig(t)
	if got := c.EgressProfiles["github"]; len(got) != 6 || got[0] != "github.com" {
		t.Fatalf("github profile %v", got)
	}
	for _, p := range []string{"pypi", "npm", "golang", "crates"} {
		if len(c.EgressProfiles[p]) == 0 {
			t.Errorf("profile %s missing", p)
		}
	}
}

func TestLoadConfigNetworkPolicy(t *testing.T) {
	if got := testConfig(t).NetworkPolicy; got != "cilium" {
		t.Errorf("default networkPolicy = %q, want cilium", got)
	}
	c, err := cfgFrom(t, goodConfig+"networkPolicy: none\n")
	if err != nil || c.NetworkPolicy != "none" {
		t.Errorf("none: %q %v", c.NetworkPolicy, err)
	}
}

func TestLoadConfigRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"no images":     strings.Replace(goodConfig, "images:", "imagesX:", 1),
		"http broker":   strings.Replace(goodConfig, "https://room-broker", "http://room-broker", 1),
		"short svc":     strings.Replace(goodConfig, ".svc.cluster.local", ".svc", 1),
		"bad prefix":    strings.Replace(goodConfig, "namePrefix: run-", "namePrefix: run", 1),
		"unknown field": goodConfig + "surprise: 1\n",
		"no model":      strings.Replace(goodConfig, "modelSecret: agent-model\n", "", 1),
		"bad policy":    goodConfig + "networkPolicy: calico\n",
	} {
		if _, err := cfgFrom(t, body); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestInvalid(t *testing.T) {
	c := testConfig(t)
	for name, tc := range map[string]struct {
		r    *v1alpha1.AgentRun
		want string
	}{
		"valid":        {run("run-abcdefgh"), ""},
		"other prefix": {run("xplane-run-abcdefgh"), "namePrefix"},
		"bad id":       {run("run-ABCDEFGH"), "namePrefix"},
		"other ns":     {run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Namespace = "default" }), "namespace"},
		"suspendable":  {run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Suspendable = true }), "suspendable"},
		"bogus profile": {run("run-abcdefgh", func(r *v1alpha1.AgentRun) {
			r.Spec.Egress.Profiles = []v1alpha1.AgentRunEgressProfile{"bogus"}
		}), "egress"},
		"internal": {run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal" }), "internal"},
	} {
		if got := c.Invalid(tc.r); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: Invalid = %q, want it to mention %q", name, got, tc.want)
		}
	}
	c.RuntimeClassName = ""
	if got := c.Invalid(run("run-abcdefgh")); !strings.Contains(got, "runtimeClassName") {
		t.Errorf("no RuntimeClass: %q", got)
	}
	c.AllowUnsandboxed = true
	if got := c.Invalid(run("run-abcdefgh")); got != "" {
		t.Errorf("allowUnsandboxed: %q", got)
	}
}

// gatewayConfig is goodConfig in model.mode gateway (v4 step 4, GW3): no model Secret.
var gatewayConfig = strings.Replace(goodConfig, "modelSecret: agent-model\n", "", 1) + `model:
  mode: gateway
  gateway:
    url: http://model-gateway.maku.svc.cluster.local
    aliases:
      public: [agent-default, tier-light, tier-standard, kimi-frontier]
      internal: [tier-standard]
`

func gatewayTestConfig(t *testing.T) Config {
	t.Helper()
	c, err := cfgFrom(t, gatewayConfig)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadConfigModelMode(t *testing.T) {
	c := testConfig(t)
	if c.Model.Mode != ModelModeSecret || c.ModelSecret != "agent-model" {
		t.Fatalf("default: mode %q modelSecret %q, want secret and agent-model", c.Model.Mode, c.ModelSecret)
	}
	g := gatewayTestConfig(t)
	if g.Model.Mode != ModelModeGateway || g.Model.Gateway.Ports.Public != 8080 || g.Model.Gateway.Ports.Internal != 8081 ||
		g.Model.Gateway.AudiencePrefix != "maku-gateway" {
		t.Fatalf("gateway defaults: %+v", g.Model)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"both":         {gatewayConfig + "modelSecret: agent-model\n", "model.mode gateway and modelSecret: choose one (the gateway holds the keys)"},
		"no url":       {strings.Replace(gatewayConfig, "    url: http://model-gateway.maku.svc.cluster.local\n", "", 1), "model.gateway.url"},
		"https":        {strings.Replace(gatewayConfig, "http://model-gateway", "https://model-gateway", 1), "model.gateway.url"},
		"short svc":    {strings.Replace(gatewayConfig, ".maku.svc.cluster.local", ".maku.svc", 1), "model.gateway.url"},
		"public host":  {strings.Replace(gatewayConfig, "model-gateway.maku.svc.cluster.local", "gw.example.com", 1), "model.gateway.url"},
		"url port":     {strings.Replace(gatewayConfig, "cluster.local\n", "cluster.local:8080\n", 1), "model.gateway.url"},
		"url path":     {strings.Replace(gatewayConfig, "cluster.local\n", "cluster.local/v1\n", 1), "model.gateway.url"},
		"no aliases":   {strings.Replace(gatewayConfig, "      public: [agent-default, tier-light, tier-standard, kimi-frontier]\n", "", 1), "model.gateway.aliases.public"},
		"bad alias":    {strings.Replace(gatewayConfig, "[tier-standard]", "[Tier_X]", 1), "model.gateway.aliases.internal"},
		"bad port":     {gatewayConfig + "    ports: {public: 70000}\n", "model.gateway.ports.public"},
		"bad prefix":   {gatewayConfig + "    audiencePrefix: \"a b\"\n", "model.gateway.audiencePrefix"},
		"bad mode":     {goodConfig + "model: {mode: direct}\n", "model.mode"},
		"secret, none": {strings.Replace(goodConfig, "modelSecret: agent-model\n", "", 1), "modelSecret is required"},
		"secret with gateway": {goodConfig + "model:\n  gateway:\n    url: http://model-gateway.maku.svc.cluster.local\n",
			"model.gateway is set but model.mode is secret"},
	} {
		_, err := cfgFrom(t, tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestModelFrom(t *testing.T) {
	m, err := ModelFrom(map[string][]byte{"api_key": []byte("k"), "model": []byte("claude-sonnet-5-5")})
	if err != nil || m.BaseURL != "https://api.anthropic.com/v1" || m.Host != "api.anthropic.com" || m.Port != 443 {
		t.Fatalf("defaults: %+v %v", m, err)
	}
	m, err = ModelFrom(map[string][]byte{"api_key": []byte("k"), "model": []byte("m"), "base_url": []byte("http://stub.e2e.svc.cluster.local:8080/v1")})
	if err != nil || m.Host != "stub.e2e.svc.cluster.local" || m.Port != 8080 {
		t.Fatalf("explicit: %+v %v", m, err)
	}
	if _, err := ModelFrom(map[string][]byte{"api_key": []byte("k"), "model": []byte("m"), "base_url": []byte("http://stub.e2e.svc:8080/v1")}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("short .svc host cannot resolve under the DNS allowlist: want an error, got %v", err)
	}
	m, err = ModelFrom(map[string][]byte{"api_key": []byte("k"), "model": []byte("m"), "base_url": []byte("http://stub.e2e.svc.cluster.local:8080")})
	if err != nil || m.Host != "stub.e2e.svc.cluster.local" || m.Port != 8080 {
		t.Fatalf("cluster-local http: %+v %v", m, err)
	}
	if _, err := ModelFrom(map[string][]byte{"api_key": []byte("k"), "model": []byte("m"), "base_url": []byte("http://api.example.com/v1")}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("http public host: want a base_url error, got %v", err)
	}
	if _, err := ModelFrom(map[string][]byte{"model": []byte("m")}); err == nil {
		t.Fatal("no api_key: want an error")
	}
	if _, err := ModelFrom(map[string][]byte{"api_key": []byte("k")}); err == nil {
		t.Fatal("no model: want an error")
	}
}
