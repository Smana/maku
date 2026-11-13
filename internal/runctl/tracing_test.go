// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/runs"
)

const tracingConfig = `tracing:
  otlpEndpoint: http://collector.obs.svc.cluster.local:4318
  collector:
    namespace: obs
    labels: {app.kubernetes.io/name: collector}
`

const validTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func traced(r *v1alpha1.AgentRun) {
	r.Annotations = map[string]string{runs.AnnTraceparent: validTraceparent}
}

func TestLoadConfigTracing(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+tracingConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tracing.OTLPEndpoint != "http://collector.obs.svc.cluster.local:4318" || c.Tracing.Collector.Namespace != "obs" ||
		c.Tracing.Collector.Labels["app.kubernetes.io/name"] != "collector" || c.Tracing.port() != 4318 {
		t.Fatalf("tracing %+v", c.Tracing)
	}
	if !reflect.ValueOf(testConfig(t).Tracing).IsZero() {
		t.Fatal("no tracing block: tracing is off")
	}
	const collectorErr = "tracing.collector: name the collector's pods (namespace, labels) so the run's policy can reach it"
	for _, tc := range []struct{ name, body, want string }{
		{"no collector", "tracing:\n  otlpEndpoint: http://collector.obs.svc.cluster.local:4318\n", collectorErr},
		{"no collector labels", strings.Replace(tracingConfig, "    labels: {app.kubernetes.io/name: collector}\n", "", 1), collectorErr},
		{"no collector namespace", strings.Replace(tracingConfig, "    namespace: obs\n", "", 1), collectorErr},
		{"collector without endpoint", strings.Replace(tracingConfig, "  otlpEndpoint: http://collector.obs.svc.cluster.local:4318\n", "", 1), "tracing.collector is set without tracing.otlpEndpoint"},
		{"grpc scheme", strings.Replace(tracingConfig, "http://", "grpc://", 1), "tracing.otlpEndpoint"},
		{"host:port only", strings.Replace(tracingConfig, "http://collector", "collector", 1), "tracing.otlpEndpoint"},
		{"no port", strings.Replace(tracingConfig, ":4318", "", 1), "tracing.otlpEndpoint"},
		{"port 0", strings.Replace(tracingConfig, ":4318", ":0", 1), "tracing.otlpEndpoint"},
		{"port 70000", strings.Replace(tracingConfig, ":4318", ":70000", 1), "tracing.otlpEndpoint"},
		{"short service name", strings.Replace(tracingConfig, ".svc.cluster.local", ".svc", 1), "tracing.otlpEndpoint"},
		{"external host", strings.Replace(tracingConfig, "collector.obs.svc.cluster.local", "otel.example.com", 1), "tracing.otlpEndpoint"},
		{"userinfo", strings.Replace(tracingConfig, "http://", "http://u:p@", 1), "tracing.otlpEndpoint"},
		{"query", strings.Replace(tracingConfig, ":4318", ":4318/?x=1", 1), "tracing.otlpEndpoint"},
		{"other namespace", strings.Replace(tracingConfig, "namespace: obs", "namespace: other", 1), "tracing.collector.namespace"},
	} {
		_, err := cfgFrom(t, goodConfig+tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error containing %q, got %v", tc.name, tc.want, err)
		}
	}
	if _, err := cfgFrom(t, goodConfig+strings.Replace(tracingConfig, "http://", "https://", 1)); err != nil {
		t.Errorf("https endpoint: %v", err)
	}
}

func TestPodTracingEnv(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+tracingConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ann, want string
		set             bool
	}{
		{"valid", validTraceparent, validTraceparent, true},
		{"sampled flag 00", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00", true},
		{"no annotation", "", "", false},
		{"all-zero trace id", "00-00000000000000000000000000000000-b7ad6b7169203331-01", "", false},
		{"all-zero parent id", "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01", "", false},
		{"upper case", "00-0AF7651916CD43DD8448EB211C80319C-B7AD6B7169203331-01", "", false},
		{"version ff", "ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "", false},
		{"truncated", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b71", "", false},
		{"newline", validTraceparent + "\nX: y", "", false},
	} {
		r := goRun(func(r *v1alpha1.AgentRun) {
			if tc.ann != "" {
				r.Annotations = map[string]string{runs.AnnTraceparent: tc.ann}
			}
		})
		if why := c.Invalid(r); why != "" {
			t.Fatalf("%s: the run must still start: %s", tc.name, why)
		}
		p := Pod(r, c, testModel, "abcdefgh")
		env := harnessEnv(p)
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://collector.obs.svc.cluster.local:4318" {
			t.Errorf("%s: OTEL_EXPORTER_OTLP_ENDPOINT = %q", tc.name, env["OTEL_EXPORTER_OTLP_ENDPOINT"])
		}
		got, set := env["TRACEPARENT"]
		if set != tc.set || got != tc.want {
			t.Errorf("%s: TRACEPARENT = %q (set %v), want %q (set %v)", tc.name, got, set, tc.want, tc.set)
		}
		for _, ct := range p.Spec.InitContainers {
			for _, e := range ct.Env {
				if strings.HasPrefix(e.Name, "OTEL_") || e.Name == "TRACEPARENT" {
					t.Errorf("%s: %s has %s: the harness alone exports the run's span", tc.name, ct.Name, e.Name)
				}
			}
		}
	}
	// No tracing: neither variable, whatever the annotation.
	env := harnessEnv(Pod(goRun(traced), testConfig(t), testModel, "abcdefgh"))
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "TRACEPARENT"} {
		if _, ok := env[k]; ok {
			t.Errorf("tracing off: %s is set", k)
		}
	}
}

// Without tracing, the pod, the NetworkPolicy and the CNP are the goldens; tracing adds to them,
// never changes what is there.
func TestTracingOffIsTheGoldens(t *testing.T) {
	golden(t, "pod.golden.yaml", Pod(goRun(traced), testConfig(t), testModel, "abcdefgh"))
	golden(t, "cnp.golden.yaml", NetworkPolicy(goRun(traced), testConfig(t), testModel, "abcdefgh").Object)
	c := kubeConfig(t, "roomToolsURL: http://room-broker.agent-platform.svc.cluster.local:8090/mcp\n")
	golden(t, "netpol.golden.yaml", KubeNetworkPolicy(goRun(traced), c, testModel, "abcdefgh"))
}

func TestKubeNetworkPolicyTracing(t *testing.T) {
	off := KubeNetworkPolicy(goRun(), kubeConfig(t), testModel, "abcdefgh")
	on := KubeNetworkPolicy(goRun(), kubeConfig(t, tracingConfig), testModel, "abcdefgh")
	if len(on.Spec.Egress) != len(off.Spec.Egress)+1 || !reflect.DeepEqual(on.Spec.Egress[:len(off.Spec.Egress)], off.Spec.Egress) {
		t.Fatalf("tracing adds one rule and changes none:\noff %v\non  %v", off.Spec.Egress, on.Spec.Egress)
	}
	rule := on.Spec.Egress[len(on.Spec.Egress)-1]
	assertCollectorRule(t, rule)
	if len(on.Spec.Ingress) != 0 {
		t.Fatalf("tracing opens no ingress: %v", on.Spec.Ingress)
	}
}

func assertCollectorRule(t *testing.T, rule networkingv1.NetworkPolicyEgressRule) {
	t.Helper()
	if len(rule.To) != 1 || rule.To[0].IPBlock != nil || rule.To[0].NamespaceSelector == nil || rule.To[0].PodSelector == nil ||
		!reflect.DeepEqual(rule.To[0].NamespaceSelector.MatchLabels, map[string]string{"kubernetes.io/metadata.name": "obs"}) ||
		!reflect.DeepEqual(rule.To[0].PodSelector.MatchLabels, map[string]string{"app.kubernetes.io/name": "collector"}) {
		t.Fatalf("the collector's pods and nothing more: %v", rule.To)
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 4318 || string(*rule.Ports[0].Protocol) != "TCP" {
		t.Fatalf("the collector's port only: %v", rule.Ports)
	}
}

func TestCNPTracing(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+tracingConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{"", "roomroom"} {
		r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = room })
		off := NetworkPolicy(r, testConfig(t), testModel, "abcdefgh").Object["spec"].(map[string]any)
		on := NetworkPolicy(r, c, testModel, "abcdefgh").Object["spec"].(map[string]any)
		offE, onE := off["egress"].([]any), on["egress"].([]any)
		if len(onE) != len(offE)+1 || !reflect.DeepEqual(onE[1:len(offE)], offE[1:]) {
			t.Fatalf("room %q: tracing adds one rule and changes none but DNS:\noff %v\non  %v", room, offE, onE)
		}
		want := map[string]any{
			"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "obs", "app.kubernetes.io/name": "collector"}}},
			"toPorts":     []any{map[string]any{"ports": []any{map[string]any{"port": "4318", "protocol": "TCP"}}}},
		}
		if got := onE[len(onE)-1]; !reflect.DeepEqual(got, want) {
			t.Fatalf("room %q: collector rule %v, want %v", room, got, want)
		}
		dnsOf := func(egress []any) []any {
			return egress[0].(map[string]any)["toPorts"].([]any)[0].(map[string]any)["rules"].(map[string]any)["dns"].([]any)
		}
		offDNS, onDNS := dnsOf(offE), dnsOf(onE)
		if len(onDNS) != len(offDNS)+1 || !slices.ContainsFunc(onDNS, func(d any) bool {
			return d.(map[string]any)["matchName"] == "collector.obs.svc.cluster.local"
		}) {
			t.Fatalf("room %q: DNS must add the collector's name and nothing else: %v", room, onDNS)
		}
		if !reflect.DeepEqual(on["ingress"], off["ingress"]) {
			t.Fatalf("room %q: tracing opens no ingress", room)
		}
	}
}
