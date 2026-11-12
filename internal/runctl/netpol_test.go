// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const kubeModeConfig = `networkPolicy: kubernetes
egressProxy:
  url: http://egress-proxy.agent-platform.svc.cluster.local:3128
  namespace: agent-platform
  labels: {app.kubernetes.io/name: egress-proxy}
`

func kubeConfig(t *testing.T, extra ...string) Config {
	t.Helper()
	c, err := cfgFrom(t, goodConfig+kubeModeConfig+strings.Join(extra, ""))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadConfigKubernetesMode(t *testing.T) {
	c := kubeConfig(t)
	if c.DNS.Namespace != "kube-system" || c.DNS.Labels["k8s-app"] != "kube-dns" {
		t.Fatalf("dns defaults: %+v", c.DNS)
	}
	if c.EgressProxy.Port() != 3128 {
		t.Fatalf("proxy port %d", c.EgressProxy.Port())
	}
	for name, body := range map[string]string{
		"no proxy url":    goodConfig + "networkPolicy: kubernetes\n",
		"https proxy url": goodConfig + strings.Replace(kubeModeConfig, "url: http://", "url: https://", 1),
		"no proxy port":   goodConfig + strings.Replace(kubeModeConfig, ":3128", "", 1),
		"no proxy labels": goodConfig + strings.Replace(kubeModeConfig, "  labels: {app.kubernetes.io/name: egress-proxy}\n", "", 1),
		"no proxy ns":     goodConfig + strings.Replace(kubeModeConfig, "  namespace: agent-platform\n", "", 1),
		"bad deny cidr":   goodConfig + kubeModeConfig + "  denyCIDRs: [10.0.0.0]\n",
	} {
		if _, err := cfgFrom(t, body); err == nil || !strings.Contains(err.Error(), "egressProxy") {
			t.Errorf("%s: want an egressProxy error, got %v", name, err)
		}
	}
}

func TestKubeNetworkPolicyGolden(t *testing.T) {
	c := kubeConfig(t, "roomToolsURL: http://room-broker.agent-platform.svc.cluster.local:8090/mcp\n")
	golden(t, "netpol.golden.yaml", KubeNetworkPolicy(goRun(), c, testModel, "abcdefgh"))
}

func TestKubeNetworkPolicyInClusterModelGolden(t *testing.T) {
	m := Model{BaseURL: "http://stub-model.e2e.svc.cluster.local:8080/v1", Name: "stub", Host: "stub-model.e2e.svc.cluster.local", Port: 8080}
	r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = "" })
	np := KubeNetworkPolicy(r, kubeConfig(t), m, "abcdefgh")
	golden(t, "netpol-incluster-model.golden.yaml", np)
	if len(np.Spec.Ingress) != 0 {
		t.Fatalf("no room: no ingress rule, got %v", np.Spec.Ingress)
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("both directions are enforced: %v", np.Spec.PolicyTypes)
	}
}

func TestEgressIsTheAllowList(t *testing.T) {
	got := strings.Join(Egress(goRun(), testConfig(t), testModel), ",")
	want := "github.com:443,api.github.com:443,codeload.github.com:443,objects.githubusercontent.com:443," +
		"raw.githubusercontent.com:443,release-assets.githubusercontent.com:443," +
		"proxy.golang.org:443,sum.golang.org:443,dl.google.com:443,api.anthropic.com:443"
	if got != want {
		t.Fatalf("Egress = %s\nwant      %s", got, want)
	}
	m := Model{BaseURL: "https://llm.example.com:8443/v1", Name: "m", Host: "llm.example.com", Port: 8443}
	if got := Egress(run("run-abcdefgh"), testConfig(t), m); !strings.HasSuffix(strings.Join(got, ","), ",llm.example.com:8443") {
		t.Fatalf("a model on another port: %v", got)
	}
	m = Model{BaseURL: "http://stub-model.e2e.svc.cluster.local:8080/v1", Name: "m", Host: "stub-model.e2e.svc.cluster.local", Port: 8080}
	if got := strings.Join(Egress(run("run-abcdefgh"), testConfig(t), m), ","); strings.Contains(got, "stub-model") {
		t.Fatalf("an in-cluster model is reached directly, never through the proxy: %s", got)
	}
}

func TestKubernetesModePod(t *testing.T) {
	c := kubeConfig(t)
	p := Pod(goRun(), c, testModel, "abcdefgh")
	if got, want := p.Annotations[EgressAnnotation], strings.Join(Egress(goRun(), c, testModel), ","); got != want {
		t.Fatalf("egress annotation %q, want %q", got, want)
	}
	env := harnessEnv(p)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy"} {
		if env[k] != "http://egress-proxy.agent-platform.svc.cluster.local:3128" {
			t.Errorf("%s = %q", k, env[k])
		}
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if env[k] != "127.0.0.1,localhost,.svc.cluster.local" {
			t.Errorf("%s = %q", k, env[k])
		}
	}
	for _, ct := range p.Spec.InitContainers {
		for _, e := range ct.Env {
			if strings.Contains(strings.ToLower(e.Name), "proxy") {
				t.Errorf("%s has %s: the bridge talks in-cluster only", ct.Name, e.Name)
			}
		}
	}
	// cilium mode: nothing of the above.
	cp := Pod(goRun(), testConfig(t), testModel, "abcdefgh")
	if _, ok := cp.Annotations[EgressAnnotation]; ok {
		t.Fatal("cilium mode: no egress annotation")
	}
	if _, ok := harnessEnv(cp)["HTTPS_PROXY"]; ok {
		t.Fatal("cilium mode: no proxy env")
	}
}

// kubernetes mode: the apiserver accepts the run's NetworkPolicy and the annotated pod, and no CNP
// is created.
func TestEnvtestKubernetesModeCreatesANetworkPolicy(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	e.r.Cfg = kubeConfig(t)
	create(t, e, run("run-kkkkkkkk"))
	e.reconcile(t, "run-kkkkkkkk")
	p := &corev1.Pod{}
	if err := e.c.Get(e.ctx, key("run-kkkkkkkk"), p); err != nil {
		t.Fatalf("pod: %v", err)
	}
	if p.Annotations[EgressAnnotation] == "" {
		t.Fatal("the pod carries its allow-list")
	}
	np := &networkingv1.NetworkPolicy{}
	if err := e.c.Get(e.ctx, key("run-kkkkkkkk"), np); err != nil {
		t.Fatalf("NetworkPolicy: %v", err)
	}
	if !metav1.IsControlledBy(np, e.get(t, "run-kkkkkkkk")) {
		t.Fatal("the run owns its NetworkPolicy")
	}
	cnp := &unstructured.Unstructured{}
	cnp.SetAPIVersion("cilium.io/v2")
	cnp.SetKind("CiliumNetworkPolicy")
	if err := e.c.Get(e.ctx, key("run-kkkkkkkk"), cnp); !apierrors.IsNotFound(err) {
		t.Fatalf("kubernetes mode creates no CNP, got %v", err)
	}
}

func TestKubeNetworkPolicySelectsTheRunAndOpensOnlyTheBridgeProbe(t *testing.T) {
	np := KubeNetworkPolicy(goRun(), kubeConfig(t), testModel, "abcdefgh")
	if np.Spec.PodSelector.MatchLabels[LabelRunID] != "abcdefgh" || np.Name != "run-abcdefgh" || np.Namespace != "agents" {
		t.Fatalf("selector %v name %s/%s", np.Spec.PodSelector, np.Namespace, np.Name)
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].Ports) != 1 || np.Spec.Ingress[0].Ports[0].Port.IntValue() != BridgeHealthPort {
		t.Fatalf("ingress %v", np.Spec.Ingress)
	}
	var proxy bool
	for _, e := range np.Spec.Egress {
		if len(e.To) == 1 && e.To[0].PodSelector != nil && e.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] == "egress-proxy" &&
			e.Ports[0].Port.IntValue() == 3128 && *e.Ports[0].Protocol == corev1.ProtocolTCP {
			proxy = true
		}
		for _, to := range e.To {
			if to.IPBlock != nil {
				t.Fatalf("a run reaches no address block, only the proxy: %v", e)
			}
		}
	}
	if !proxy {
		t.Fatalf("no proxy rule: %v", np.Spec.Egress)
	}
}
