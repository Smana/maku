// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestProbePodGolden(t *testing.T) {
	golden(t, "probe.golden.yaml", ProbePod(kubeConfig(t), "abcdefgh"))
}

func TestProbePodIsHeldLikeARun(t *testing.T) {
	c := kubeConfig(t)
	p := ProbePod(c, "abcdefgh")
	if p.Name != "run-preflight-abcdefgh" || p.Namespace != "agents" {
		t.Fatalf("name %s/%s", p.Namespace, p.Name)
	}
	if p.Labels[LabelRunID] != ProbeRunID || p.Labels[LabelManagedBy] != "run-controller" {
		t.Fatalf("labels %v", p.Labels)
	}
	if p.Annotations[EgressAnnotation] != "github.com:443" {
		t.Fatalf("egress %q", p.Annotations[EgressAnnotation])
	}
	if p.Spec.RuntimeClassName == nil || *p.Spec.RuntimeClassName != "gvisor" {
		t.Fatal("the probe runs under the sandbox class: a class the nodes lack fails it")
	}
	ct := p.Spec.Containers[0]
	if ct.Image != c.Images.Probe || ct.Args[0] != "netprobe" || ct.Resources.Limits.Cpu().IsZero() || ct.Resources.Limits.Memory().IsZero() {
		t.Fatalf("container %+v", ct)
	}
	args := strings.Join(ct.Args, " ")
	// Three negative controls: the broker's 9090, which its own ingress policy also denies; the
	// apiserver and github.com dialled directly, which only the run's egress policy denies, so a CNI
	// that enforces ingress alone fails the probe.
	for _, want := range []string{"--allow room-broker.agent-platform.svc.cluster.local:8443", "--deny room-broker.agent-platform.svc.cluster.local:9090",
		"--deny kubernetes.default.svc.cluster.local:443", "--deny github.com:443",
		"--connect github.com:443", "--proxy http://egress-proxy.agent-platform.svc.cluster.local:3128"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	p.UID = "pod-uid"
	np := ProbeNetworkPolicy(c, p)
	if len(np.OwnerReferences) != 1 || np.OwnerReferences[0].UID != "pod-uid" || np.OwnerReferences[0].Kind != "Pod" || np.OwnerReferences[0].BlockOwnerDeletion != nil {
		t.Fatalf("owner %v", np.OwnerReferences)
	}
	if np.Spec.PodSelector.MatchLabels[LabelRunID] != ProbeRunID || np.Name != p.Name {
		t.Fatalf("the probe's own policy: %v", np.Spec.PodSelector)
	}
}

// probeClient is a fake that ends every probe pod it creates in phase, with message as netprobe's
// termination message.
func probeClient(t *testing.T, phase corev1.PodPhase, message string, objs ...client.Object) client.WithWatch {
	t.Helper()
	inner := fakeReader(t, append([]client.Object{gvisor()}, objs...)...)
	return interceptor.NewClient(inner, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if p, ok := o.(*corev1.Pod); ok {
				p.Status.Phase = phase
				if message != "" {
					p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "netprobe",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: message}}}}
				}
			}
			return c.Create(ctx, o, opts...)
		},
	})
}

func netPreflight(t *testing.T, c client.WithWatch) *Preflight {
	t.Helper()
	return &Preflight{Client: c, Kube: c, Cfg: kubeConfig(t), Log: slog.New(slog.DiscardHandler),
		ProbeStart: 200 * time.Millisecond, ProbePoll: 20 * time.Millisecond, ProbeSuffix: func() string { return "abcdefgh" }}
}

func termMsg(t *testing.T, kind, msg string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"ok": false, "kind": kind, "message": msg})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPreflightNetworkPasses(t *testing.T) {
	c := probeClient(t, corev1.PodSucceeded, "")
	p := netPreflight(t, c)
	var gauge []string
	p.Passed = func(check string, ok bool) {
		if ok {
			gauge = append(gauge, check)
		}
	}
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("%+v", got)
	}
	if strings.Join(gauge, ",") != "runtimeClass,gitSecrets,network" {
		t.Fatalf("checks passed: %v", gauge)
	}
	var pods corev1.PodList
	if err := c.List(t.Context(), &pods); err != nil || len(pods.Items) != 0 {
		t.Fatalf("the probe pod is deleted after its verdict: %v %v", pods.Items, err)
	}
}

func TestPreflightNetworkUnenforcedRefusesRuns(t *testing.T) {
	msg := "deny room-broker.agent-platform.svc.cluster.local:9090 connected: the CNI does not enforce NetworkPolicy"
	p := netPreflight(t, probeClient(t, corev1.PodFailed, termMsg(t, "unenforced", msg)))
	var failed bool
	p.Passed = func(check string, ok bool) { failed = failed || (check == "network" && !ok) }
	if soon := p.Tick(t.Context()); soon {
		t.Fatal("an unenforcing CNI is checked again at the normal interval")
	}
	if got := p.Result(); got.State != PreflightFailed || got.Check != "network" || got.Message != msg {
		t.Fatalf("%+v", got)
	}
	if !failed {
		t.Fatal("the gauge says network 0")
	}
}

func TestPreflightNetworkUnreachableHoldsRuns(t *testing.T) {
	msg := "the egress proxy refused github.com:443: 403 Forbidden"
	p := netPreflight(t, probeClient(t, corev1.PodFailed, termMsg(t, "unreachable", msg)))
	if soon := p.Tick(t.Context()); !soon {
		t.Fatal("a component still starting is checked again soon")
	}
	if got := p.Result(); got.State != PreflightWaiting || got.Message != msg {
		t.Fatalf("%+v", got)
	}
}

func TestPreflightNetworkProbeNotStarted(t *testing.T) {
	ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "ev", Namespace: "agents"}, Reason: "FailedCreatePodSandBox",
		Message: "no runtime for \"runsc\" is configured", LastTimestamp: metav1.Now(),
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "run-preflight-abcdefgh", Namespace: "agents"}}
	p := netPreflight(t, probeClient(t, corev1.PodPending, "", ev))
	p.Tick(t.Context())
	want := `the probe pod did not start under RuntimeClass "gvisor": FailedCreatePodSandBox: no runtime for "runsc" is configured`
	if got := p.Result(); got.State != PreflightFailed || got.Message != want {
		t.Fatalf("%+v\nwant %s", got, want)
	}
}

func TestPreflightNetworkDeletesLeftoverProbes(t *testing.T) {
	old := ProbePod(kubeConfig(t), "zzzzzzzz")
	c := probeClient(t, corev1.PodSucceeded, "", old)
	p := netPreflight(t, c)
	p.Tick(t.Context())
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(old), &corev1.Pod{}); err == nil {
		t.Fatal("a probe pod from an earlier process is deleted")
	}
}

func TestPreflightNetworkCreatesTheProbesPolicy(t *testing.T) {
	inner := probeClient(t, corev1.PodSucceeded, "")
	var created []string
	c := interceptor.NewClient(inner, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch o.(type) {
		case *corev1.Pod:
			created = append(created, "pod")
		case *networkingv1.NetworkPolicy:
			created = append(created, "networkpolicy")
		}
		return c.Create(ctx, o, opts...)
	}})
	p := netPreflight(t, c)
	p.Tick(t.Context())
	if strings.Join(created, ",") != "pod,networkpolicy" {
		t.Fatalf("created %v: the policy is owned by the pod, so it comes second", created)
	}
}

func TestPreflightNetworkOnlyInKubernetesMode(t *testing.T) {
	c := probeClient(t, corev1.PodFailed, termMsg(t, "unenforced", "x"))
	p := netPreflight(t, c)
	p.Cfg = testConfig(t) // cilium
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("cilium mode runs no network probe: %+v", got)
	}
}
