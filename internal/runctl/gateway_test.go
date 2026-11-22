// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// gatewayRun is a public implementer on tier-standard, judged on tier-light.
func gatewayRun(mut ...func(*v1alpha1.AgentRun)) *v1alpha1.AgentRun {
	return goRun(append([]func(*v1alpha1.AgentRun){func(r *v1alpha1.AgentRun) {
		r.Spec.Model, r.Spec.Loop.JudgeModel = "tier-standard", "tier-light"
	}}, mut...)...)
}

func gatewayPod(t *testing.T, r *v1alpha1.AgentRun) *corev1.Pod {
	t.Helper()
	c := gatewayTestConfig(t)
	return Pod(r, c, c.GatewayModel(r), "abcdefgh")
}

func TestPodGatewayGolden(t *testing.T) {
	golden(t, "pod-gateway.golden.yaml", gatewayPod(t, gatewayRun()))
}

// tokenSources maps each projected ServiceAccount token's path to its audience and lifetime.
func tokenSources(p *corev1.Pod, volume string) map[string]corev1.ServiceAccountTokenProjection {
	out := map[string]corev1.ServiceAccountTokenProjection{}
	for _, v := range p.Spec.Volumes {
		if v.Name != volume || v.Projected == nil {
			continue
		}
		for _, s := range v.Projected.Sources {
			if s.ServiceAccountToken != nil {
				out[s.ServiceAccountToken.Path] = *s.ServiceAccountToken
			}
		}
	}
	return out
}

// GW3, GW9: the run's own token per alias, living the pod's deadline plus 600 s; no Secret holds a key.
func TestPodGatewayTokens(t *testing.T) {
	p := gatewayPod(t, gatewayRun())
	for _, v := range p.Spec.Volumes {
		if v.Secret != nil && v.Name == "model" {
			t.Fatalf("gateway mode mounts no model Secret: %+v", v)
		}
	}
	srcs := tokenSources(p, "model-token")
	want := int64(30*60 + 600)
	for path, aud := range map[string]string{"token": "maku-gateway.public.tier-standard", "judge-token": "maku-gateway.public.tier-light"} {
		s, ok := srcs[path]
		if !ok || s.Audience != aud || s.ExpirationSeconds == nil || *s.ExpirationSeconds != want {
			t.Errorf("%s: %+v, want audience %s and %d s", path, s, aud, want)
		}
	}
	if len(srcs) != 2 {
		t.Errorf("token sources %v, want token and judge-token", srcs)
	}
	env := harnessEnv(p)
	for k, v := range map[string]string{
		"MODEL":              "tier-standard",
		"LLM_BASE_URL":       "http://model-gateway.maku.svc.cluster.local:8080/m/tier-standard/v1",
		"LLM_API_KEY_FILE":   "/run/agent/model/token",
		"JUDGE_BASE_URL":     "http://model-gateway.maku.svc.cluster.local:8080/m/tier-light/v1",
		"JUDGE_MODEL":        "tier-light",
		"JUDGE_API_KEY_FILE": "/run/agent/model/judge-token",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	// No judge: one token, no judge variables.
	solo := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Loop.JudgeModel = "" }))
	if s := tokenSources(solo, "model-token"); len(s) != 1 {
		t.Errorf("no judge: token sources %v", s)
	}
	if _, ok := harnessEnv(solo)["JUDGE_API_KEY_FILE"]; ok {
		t.Error("no judge: no JUDGE_API_KEY_FILE")
	}
	// spec.model unset is its CRD default.
	if got := harnessEnv(gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Model = "" })))["MODEL"]; got != "agent-default" {
		t.Errorf("unset spec.model: MODEL %q", got)
	}
}

// GW6: an internal run calls the internal listener with an internal audience.
func TestPodGatewayInternal(t *testing.T) {
	r := gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = "internal", "" })
	if why := gatewayTestConfig(t).Invalid(r); why != "" {
		t.Fatalf("an internal run on a served alias is admitted: %s", why)
	}
	p := gatewayPod(t, r)
	if got := harnessEnv(p)["LLM_BASE_URL"]; got != "http://model-gateway.maku.svc.cluster.local:8081/m/tier-standard/v1" {
		t.Errorf("LLM_BASE_URL %q", got)
	}
	if got := tokenSources(p, "model-token")["token"].Audience; got != "maku-gateway.internal.tier-standard" {
		t.Errorf("audience %q", got)
	}
}

func TestInvalidGateway(t *testing.T) {
	c := gatewayTestConfig(t)
	for name, tc := range map[string]struct {
		r    *v1alpha1.AgentRun
		want string
	}{
		"served":         {gatewayRun(), ""},
		"default alias":  {gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Model = "" }), ""},
		"public unknown": {gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Model = "tier-frontier" }), "spec.model tier-frontier is not served on the public listener (maku-gateway aliases.public)"},
		"judge unknown":  {gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Loop.JudgeModel = "mistral-light" }), "spec.loop.judgeModel mistral-light is not served on the public listener (maku-gateway aliases.public)"},
		"internal kimi": {gatewayRun(func(r *v1alpha1.AgentRun) {
			r.Spec.DataClass, r.Spec.Model, r.Spec.Loop.JudgeModel = "internal", "kimi-frontier", ""
		}), "spec.model kimi-frontier is not served on the internal listener (maku-gateway aliases.internal)"},
		"internal judge": {gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = "internal", "kimi-frontier" }),
			"spec.loop.judgeModel kimi-frontier is not served on the internal listener"},
	} {
		if got := c.Invalid(tc.r); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: Invalid = %q, want it to mention %q", name, got, tc.want)
		}
	}
	c.Model.Gateway.Aliases.Internal = nil
	got := c.Invalid(gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = "internal", "" }))
	if !strings.Contains(got, "spec.dataClass internal needs the model gateway") || !strings.Contains(got, "model.gateway.aliases.internal") {
		t.Errorf("internal with no internal aliases: %q", got)
	}
}

// Gateway mode needs no model Secret: the apiserver accepts the pod with its projected tokens, and an
// internal run on a served alias starts.
func TestEnvtestGatewayRunNeedsNoModelSecret(t *testing.T) {
	e := startEnv(t)
	e.r.Cfg = gatewayTestConfig(t)
	for name, mut := range map[string]func(*v1alpha1.AgentRun){
		"run-gwpubbbb": func(*v1alpha1.AgentRun) {},
		"run-gwintttt": func(r *v1alpha1.AgentRun) {
			r.Spec.DataClass, r.Spec.Loop.JudgeModel, r.Spec.Egress.Profiles = "internal", "", nil // an internal run takes no profiles
		},
	} {
		create(t, e, gatewayRun(func(r *v1alpha1.AgentRun) { r.Name = name }, mut))
		e.reconcile(t, name)
		got := e.get(t, name)
		if !apimeta.IsStatusConditionTrue(got.Status.Conditions, "Ready") {
			t.Fatalf("%s: %+v", name, got.Status)
		}
		p := &corev1.Pod{}
		if err := e.c.Get(e.ctx, key(name), p); err != nil {
			t.Fatal(err)
		}
		if s := tokenSources(p, "model-token"); s["token"].Audience != "maku-gateway."+got.Spec.DataClass+".tier-standard" {
			t.Fatalf("%s: token sources %v", name, s)
		}
	}
}

// GW13: the run's policies admit only the gateway's pods in its namespace on its class's port.
func TestGatewayNetworkPolicies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		podLabels  map[string]string
		wantLabels map[string]string
	}{
		{
			name:       "default gateway pod label",
			wantLabels: map[string]string{"gateway.networking.k8s.io/gateway-name": "model-gateway"},
		},
		{
			name:       "custom gateway pod labels",
			podLabels:  map[string]string{"app.kubernetes.io/name": "custom-gw", "tier": "llm"},
			wantLabels: map[string]string{"app.kubernetes.io/name": "custom-gw", "tier": "llm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := gatewayTestConfig(t)
			if tc.podLabels != nil {
				c.Model.Gateway.PodLabels = tc.podLabels
			}
			for class, port := range map[string]string{"public": "8080", "internal": "8081"} {
				r := gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = class, "" })
				m := c.GatewayModel(r)
				var found bool
				for _, e := range NetworkPolicy(r, c, m, "abcdefgh").Object["spec"].(map[string]any)["egress"].([]any) {
					rule := e.(map[string]any)
					eps, ok := rule["toEndpoints"].([]any)
					if !ok {
						continue
					}
					sel := eps[0].(map[string]any)["matchLabels"].(map[string]any)
					p := rule["toPorts"].([]any)[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"]
					if sel["io.kubernetes.pod.namespace"] == "maku" && p == port {
						if len(sel) != len(tc.wantLabels)+1 {
							t.Errorf("%s: CNP matchLabels %v, want namespace + %v", class, sel, tc.wantLabels)
						}
						for k, v := range tc.wantLabels {
							if sel[k] != v {
								t.Errorf("%s: CNP matchLabels[%q] = %v, want %q", class, k, sel[k], v)
							}
						}
						found = true
					}
				}
				if !found {
					t.Errorf("%s: the CNP does not admit gateway pods in namespace maku on %s", class, port)
				}
				kc := c
				kc.NetworkPolicy, kc.EgressProxy = NetworkPolicyKubernetes, EgressProxy{URL: "http://egress-proxy.maku.svc.cluster.local:3128", Namespace: "maku", Labels: map[string]string{"app": "egress-proxy"}}
				found = false
				for _, e := range KubeNetworkPolicy(r, kc, m, "abcdefgh").Spec.Egress {
					if len(e.To) == 1 && e.To[0].NamespaceSelector != nil &&
						e.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "maku" &&
						e.Ports[0].Port.String() == port {
						if e.To[0].PodSelector == nil {
							t.Errorf("%s: NetworkPolicy gateway rule lacks podSelector", class)
							continue
						}
						if len(e.To[0].PodSelector.MatchLabels) != len(tc.wantLabels) {
							t.Errorf("%s: NetworkPolicy podSelector %v, want %v", class, e.To[0].PodSelector.MatchLabels, tc.wantLabels)
						}
						for k, v := range tc.wantLabels {
							if e.To[0].PodSelector.MatchLabels[k] != v {
								t.Errorf("%s: NetworkPolicy podSelector[%q] = %q, want %q", class, k, e.To[0].PodSelector.MatchLabels[k], v)
							}
						}
						found = true
					}
				}
				if !found {
					t.Errorf("%s: the NetworkPolicy does not admit gateway pods in namespace maku on %s", class, port)
				}
				if got := Egress(r, kc, m); strings.Contains(strings.Join(got, ","), "model-gateway") {
					t.Errorf("%s: the egress proxy's allow-list names the gateway: %v", class, got)
				}
			}
		})
	}
}

// CP12: an internal run gets the light alias; a public run without search, and any non-implementer
// with search, get none.
func TestAnInternalRunGetsTheLightAlias(t *testing.T) {
	p := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal"; r.Spec.Loop.JudgeModel = "" }))
	env := harnessEnv(p)
	for k, v := range map[string]string{
		"LIGHT_BASE_URL":     "http://model-gateway.maku.svc.cluster.local:8081/m/tier-light/v1",
		"LIGHT_MODEL":        "tier-light",
		"LIGHT_API_KEY_FILE": "/run/agent/model/light-token",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if s := tokenSources(p, "model-token")["light-token"]; s.Audience != "maku-gateway.internal.tier-light" {
		t.Errorf("light-token audience %+v, want maku-gateway.internal.tier-light", s)
	}
}

func TestAPublicRunWithoutSearchGetsNoLightAlias(t *testing.T) {
	p := gatewayPod(t, gatewayRun())
	env := harnessEnv(p)
	for _, k := range []string{"LIGHT_BASE_URL", "LIGHT_MODEL", "LIGHT_API_KEY_FILE"} {
		if _, ok := env[k]; ok {
			t.Errorf("public run without search: %s is set", k)
		}
	}
	if _, ok := tokenSources(p, "model-token")["light-token"]; ok {
		t.Error("public run without search: a light-token source exists")
	}
}

func TestASearchImplementerGetsTheLightAlias(t *testing.T) {
	p := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Loop.Search = true; r.Spec.Loop.JudgeModel = "" }))
	if env := harnessEnv(p); env["LIGHT_MODEL"] != "tier-light" || env["LIGHT_API_KEY_FILE"] != "/run/agent/model/light-token" {
		t.Errorf("search implementer: %+v", env)
	}
	if s := tokenSources(p, "model-token")["light-token"]; s.Audience != "maku-gateway.public.tier-light" {
		t.Errorf("light-token audience %+v, want maku-gateway.public.tier-light", s)
	}
	// A reviewer with search still gets none: the tool is the implementer's only.
	reviewer := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.Loop.Search = true; r.Spec.Role = "reviewer" }))
	if _, ok := harnessEnv(reviewer)["LIGHT_MODEL"]; ok {
		t.Error("a reviewer gets no light alias")
	}
}

func TestAJudgeOnTheLightAliasSharesItsToken(t *testing.T) {
	p := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = "internal", "tier-light" }))
	if env := harnessEnv(p); env["LIGHT_API_KEY_FILE"] != "/run/agent/model/judge-token" {
		t.Errorf("LIGHT_API_KEY_FILE = %q, want the judge's", env["LIGHT_API_KEY_FILE"])
	}
	srcs := tokenSources(p, "model-token")
	if _, ok := srcs["light-token"]; ok {
		t.Error("a judge on the light alias shares its token: no light-token source")
	}
	if s, ok := srcs["judge-token"]; !ok || s.Audience != "maku-gateway.internal.tier-light" {
		t.Errorf("judge-token %+v", s)
	}
}

func TestAJudgeElsewhereKeepsBothTokens(t *testing.T) {
	p := gatewayPod(t, gatewayRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass, r.Spec.Loop.JudgeModel = "internal", "tier-standard" }))
	if env := harnessEnv(p); env["LIGHT_API_KEY_FILE"] != "/run/agent/model/light-token" {
		t.Errorf("LIGHT_API_KEY_FILE = %q, want the light token's", env["LIGHT_API_KEY_FILE"])
	}
	srcs := tokenSources(p, "model-token")
	if s := srcs["judge-token"]; s.Audience != "maku-gateway.internal.tier-standard" {
		t.Errorf("judge-token %+v", s)
	}
	if s := srcs["light-token"]; s.Audience != "maku-gateway.internal.tier-light" {
		t.Errorf("light-token %+v", s)
	}
}
