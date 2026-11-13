// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runctl"
)

const (
	controllerUser = "system:serviceaccount:" + release + ":run-controller"
	egressKey      = "maku.ogenki.io/egress"
)

// podEnv is an API server enforcing the chart's run-pod policy, with an admin and the run-controller.
type podEnv struct {
	admin, ctl client.Client
}

func startPodEnv(t *testing.T, sets ...string) podEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	policy := render(t, "templates/run-pod-policy.yaml", 2, sets...)
	ctrl.SetLogger(logr.Discard())
	e := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	admin, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	// system:masters passes RBAC, never admission: the policy alone decides.
	u, err := e.AddUser(envtest.User{Name: controllerUser, Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := client.New(u.Config(), client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, o := range []client.Object{
		// Pod Security comes before the policy in the admission chain: TestRunPodPolicyUnderPodSecurity
		// labels the namespace once the policy's own refusals are proven.
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runsNS}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "runc"}, Handler: "runc"},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: runsNS}},
	} {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range policy {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	pe := podEnv{admin: admin, ctl: ctl}
	pe.waitEnforced(t)
	return pe
}

// waitEnforced returns once the API server enforces the policy: an admin's pod is refused.
func (pe podEnv) waitEnforced(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := pe.admin.Create(t.Context(), runPod(t, true, false), client.DryRunAll)
		if deniedByPolicy(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run-pod policy was not enforced within 30 s (last: %v)", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runPod is the run-controller's own pod for a run, as runctl builds it, under the chart's images.
func runPod(t *testing.T, room, sandboxed bool) *corev1.Pod {
	t.Helper()
	c := runctl.Config{Namespace: runsNS, NamePrefix: "run-", ModelSecret: "agent-model", GitSecret: "agent-git",
		Broker: runctl.Broker{URL: "https://room-broker." + release + ".svc.cluster.local:8443", Namespace: release,
			Labels: map[string]string{"app.kubernetes.io/name": "room-broker"}, CASecret: "room-broker-ca"},
		Images: runctl.Images{Harness: "ghcr.io/smana/maku-harness:" + digest, Bridge: "ghcr.io/smana/maku-bridge:" + digest}}
	if sandboxed {
		c.RuntimeClassName = "gvisor"
	} else {
		c.AllowUnsandboxed = true
	}
	r := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: runsNS, UID: "uid-1"},
		Spec: v1alpha1.AgentRunSpec{Role: "implementer", Repository: "o/r", BaseRef: "main", Principal: "human:a",
			DataClass: "public", Task: v1alpha1.AgentRunTask{Text: "fix it"}, Budget: v1alpha1.AgentRunBudget{MaxMinutes: 30}, Size: "small"}}
	if room {
		r.Spec.RoomRef = "roomroom"
		c.RoomToolsURL = "http://room-broker." + release + ".svc.cluster.local:8090/mcp"
	}
	m := runctl.Model{BaseURL: "https://api.anthropic.com/v1", Name: "claude", Host: "api.anthropic.com", Port: 443}
	return runctl.Pod(r, c, m, "abcdefgh")
}

// probePod is the network preflight's pod in kubernetes mode, from the run-controller's image.
func probePod(t *testing.T, sandboxed bool) *corev1.Pod {
	t.Helper()
	c := runctl.Config{Namespace: runsNS, NamePrefix: "run-", NetworkPolicy: runctl.NetworkPolicyKubernetes,
		Broker:      runctl.Broker{URL: "https://room-broker." + release + ".svc.cluster.local:8443", Namespace: release},
		EgressProxy: runctl.EgressProxy{URL: "http://egress-proxy." + release + ".svc.cluster.local:3128"},
		Images:      runctl.Images{Probe: "ghcr.io/smana/maku-run-controller:" + digest}}
	if sandboxed {
		c.RuntimeClassName = "gvisor"
	}
	return runctl.ProbePod(c, "abcdefgh")
}

func TestRunPodPolicy(t *testing.T) {
	pe := startPodEnv(t)
	ctx := t.Context()
	yes, no := true, false
	sc := func(p *corev1.Pod) *corev1.SecurityContext { return p.Spec.Containers[0].SecurityContext }

	for _, room := range []bool{true, false} {
		if err := pe.ctl.Create(ctx, runPod(t, room, true), client.DryRunAll); err != nil {
			t.Fatalf("the run-controller's own pod (room %v) is refused: %v", room, err)
		}
	}
	// The network preflight's probe pod (kubernetes mode) is a run-controller pod like any other.
	if err := pe.ctl.Create(ctx, probePod(t, true), client.DryRunAll); err != nil {
		t.Fatalf("the preflight's probe pod is refused: %v", err)
	}

	for _, tc := range []struct {
		name, want string
		as         client.Client
		mut        func(*corev1.Pod)
	}{
		{"no runtimeClassName", `RuntimeClass "gvisor"`, nil, func(p *corev1.Pod) { p.Spec.RuntimeClassName = nil }},
		{"another runtimeClassName", `RuntimeClass "gvisor"`, nil, func(p *corev1.Pod) { p.Spec.RuntimeClassName = new("runc") }},
		{"the token automounted", "automountServiceAccountToken", nil, func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }},
		{"hostNetwork", "no host namespace", nil, func(p *corev1.Pod) { p.Spec.HostNetwork = true }},
		{"hostPID", "no host namespace", nil, func(p *corev1.Pod) { p.Spec.HostPID = true }},
		{"hostIPC", "no host namespace", nil, func(p *corev1.Pod) { p.Spec.HostIPC = true }},
		{"a hostPath volume", "no hostPath", nil, func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		}},
		{"a persistentVolumeClaim volume", "no persistentVolumeClaim", nil, func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "pvc", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "x"}}})
		}},
		{"a privileged container", "never privileged", nil, func(p *corev1.Pod) {
			sc(p).Privileged, sc(p).AllowPrivilegeEscalation = &yes, &yes
		}},
		{"allowPrivilegeEscalation unset", "allowPrivilegeEscalation: false", nil, func(p *corev1.Pod) { sc(p).AllowPrivilegeEscalation = nil }},
		{"capabilities not dropped", "drops ALL capabilities", nil, func(p *corev1.Pod) { sc(p).Capabilities = &corev1.Capabilities{} }},
		{"a capability added", "drops ALL capabilities", nil, func(p *corev1.Pod) { sc(p).Capabilities.Add = []corev1.Capability{"NET_RAW"} }},
		{"readOnlyRootFilesystem unset", "readOnlyRootFilesystem: true", nil, func(p *corev1.Pod) { sc(p).ReadOnlyRootFilesystem = nil }},
		{"no runAsNonRoot", "runAsNonRoot: true", nil, func(p *corev1.Pod) {
			p.Spec.SecurityContext.RunAsNonRoot = nil
			for i := range p.Spec.Containers {
				p.Spec.Containers[i].SecurityContext.RunAsNonRoot = nil
			}
			for i := range p.Spec.InitContainers {
				p.Spec.InitContainers[i].SecurityContext.RunAsNonRoot = nil
			}
		}},
		{"runAsNonRoot false on a container", "runAsNonRoot: true", nil, func(p *corev1.Pod) { sc(p).RunAsNonRoot = &no }},
		{"no seccomp profile", "seccomp profile", nil, func(p *corev1.Pod) {
			p.Spec.SecurityContext.SeccompProfile = nil
			sc(p).SeccompProfile = nil
		}},
		{"an Unconfined seccomp profile", "seccomp profile", nil, func(p *corev1.Pod) {
			sc(p).SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
		}},
		{"an init container without limits.cpu", "limits.cpu", nil, func(p *corev1.Pod) {
			delete(p.Spec.InitContainers[0].Resources.Limits, corev1.ResourceCPU)
			delete(p.Spec.InitContainers[0].Resources.Requests, corev1.ResourceCPU)
		}},
		{"a container without limits.memory", "limits.memory", nil, func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}
			delete(p.Spec.Containers[0].Resources.Requests, corev1.ResourceMemory)
		}},
		{"an image outside the allow-list", "images come from", nil, func(p *corev1.Pod) {
			p.Spec.Containers[0].Image = "docker.io/library/busybox:1.37@sha256:0000"
		}},
		{"a look-alike repository", "images come from", nil, func(p *corev1.Pod) {
			p.Spec.Containers[0].Image = "ghcr.io/smana/maku-harness-evil:" + digest
		}},
		{"an allow-listed repository without a digest", "pinned by digest", nil, func(p *corev1.Pod) {
			p.Spec.Containers[0].Image = "ghcr.io/smana/maku-harness:v0.0.0"
		}},
		{"a pod created by another user", "only the run-controller", pe.admin, func(*corev1.Pod) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := runPod(t, true, true)
			tc.mut(p)
			as := tc.as
			if as == nil {
				as = pe.ctl
			}
			err := as.Create(ctx, p, client.DryRunAll)
			if !deniedByPolicy(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want the policy's refusal naming %q, got %v", tc.want, err)
			}
		})
	}

	// A real pod: its egress annotation is fixed at create, and no ephemeral container is added.
	p := runPod(t, true, true)
	p.Annotations[egressKey] = "github.com:443"
	if err := pe.ctl.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	mergePatch := func(as client.Client, body map[string]any) error {
		b, _ := json.Marshal(body)
		return as.Patch(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: runsNS}}, client.RawPatch(types.MergePatchType, b))
	}
	t.Run("an unrelated label change", func(t *testing.T) {
		if err := mergePatch(pe.admin, map[string]any{"metadata": map[string]any{"labels": map[string]any{"example.com/x": "y"}}}); err != nil {
			t.Fatalf("refused: %v", err)
		}
	})
	t.Run("an UPDATE that changes the egress annotation", func(t *testing.T) {
		err := mergePatch(pe.ctl, map[string]any{"metadata": map[string]any{"annotations": map[string]any{egressKey: "example.com:443"}}})
		if !deniedByPolicy(err) || !strings.Contains(err.Error(), "never changes") {
			t.Fatalf("want the policy's refusal, got %v", err)
		}
	})
	t.Run("an UPDATE that drops the egress annotation", func(t *testing.T) {
		err := mergePatch(pe.admin, map[string]any{"metadata": map[string]any{"annotations": map[string]any{egressKey: nil}}})
		if !deniedByPolicy(err) || !strings.Contains(err.Error(), "never changes") {
			t.Fatalf("want the policy's refusal, got %v", err)
		}
	})
	t.Run("a pods/ephemeralcontainers update", func(t *testing.T) {
		got := &corev1.Pod{}
		if err := pe.admin.Get(ctx, client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		got.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: "debug", Image: "ghcr.io/smana/maku-harness:" + digest}}}
		err := pe.admin.SubResource("ephemeralcontainers").Update(ctx, got)
		if !deniedByPolicy(err) || !strings.Contains(err.Error(), "no kubectl debug") {
			t.Fatalf("want the policy's refusal, got %v", err)
		}
	})
}

// The chart labels the runs namespace Pod Security restricted (templates/agents-namespace.yaml): the
// run-controller's pods pass it, and it refuses what the policy does not hold, such as a hostPort.
func TestRunPodPolicyUnderPodSecurity(t *testing.T) {
	pe := startPodEnv(t)
	ctx := t.Context()
	ns := &corev1.Namespace{}
	if err := pe.admin.Get(ctx, client.ObjectKey{Name: runsNS}, ns); err != nil {
		t.Fatal(err)
	}
	ns.Labels["pod-security.kubernetes.io/enforce"] = "restricted"
	if err := pe.admin.Update(ctx, ns); err != nil {
		t.Fatal(err)
	}
	for _, room := range []bool{true, false} {
		for _, sandboxed := range []bool{true, false} {
			if err := pe.ctl.Create(ctx, runPod(t, room, sandboxed), client.DryRunAll); err != nil && !strings.Contains(err.Error(), "RuntimeClass") {
				t.Fatalf("the run-controller's pod (room %v, sandboxed %v) fails Pod Security restricted: %v", room, sandboxed, err)
			}
		}
	}
	if err := pe.ctl.Create(ctx, probePod(t, true), client.DryRunAll); err != nil {
		t.Fatalf("the preflight's probe pod fails Pod Security restricted: %v", err)
	}
	p := runPod(t, true, true)
	p.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 8000, HostPort: 8000}}
	if err := pe.ctl.Create(ctx, p, client.DryRunAll); err == nil || !strings.Contains(err.Error(), "violates PodSecurity") || !strings.Contains(err.Error(), "hostPort") {
		t.Fatalf("a hostPort must be refused by Pod Security restricted, got %v", err)
	}
}

// With sandbox.unsafeAllowRunc the policy renders no RuntimeClass rule: the demo's pod passes.
func TestRunPodPolicyUnsafeAllowRunc(t *testing.T) {
	pe := startPodEnv(t, "sandbox.unsafeAllowRunc=true", "runController.createRuntimeClass=false")
	if err := pe.ctl.Create(t.Context(), runPod(t, true, false), client.DryRunAll); err != nil {
		t.Fatalf("an unsandboxed run pod under unsafeAllowRunc is refused: %v", err)
	}
}

// gatewayRunPod is the run-controller's pod in model.mode gateway: the run's own tokens for the
// gateway's aliases, and no model Secret.
func gatewayRunPod(t *testing.T) *corev1.Pod {
	t.Helper()
	c := runctl.Config{Namespace: runsNS, NamePrefix: "run-", GitSecret: "agent-git", RuntimeClassName: "gvisor",
		Model: runctl.ModelConfig{Mode: runctl.ModelModeGateway, Gateway: runctl.Gateway{URL: "http://model-gateway." + release + ".svc.cluster.local",
			Ports: runctl.GatewayPorts{Public: 8080, Internal: 8081}, AudiencePrefix: "maku-gateway"}},
		Broker: runctl.Broker{URL: "https://room-broker." + release + ".svc.cluster.local:8443", Namespace: release,
			Labels: map[string]string{"app.kubernetes.io/name": "room-broker"}, CASecret: "room-broker-ca"},
		RoomToolsURL: "http://room-broker." + release + ".svc.cluster.local:8090/mcp",
		Images:       runctl.Images{Harness: "ghcr.io/smana/maku-harness:" + digest, Bridge: "ghcr.io/smana/maku-bridge:" + digest}}
	r := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: runsNS, UID: "uid-1"},
		Spec: v1alpha1.AgentRunSpec{Role: "implementer", Repository: "o/r", BaseRef: "main", Principal: "human:a", Model: "tier-light",
			DataClass: "public", RoomRef: "roomroom", Task: v1alpha1.AgentRunTask{Text: "fix it"}, Budget: v1alpha1.AgentRunBudget{MaxMinutes: 30}, Size: "small",
			Loop: v1alpha1.AgentRunLoop{JudgeModel: "tier-light"}}}
	return runctl.Pod(r, c, c.GatewayModel(r), "abcdefgh")
}

// v4 step 4 (GW3): in model.mode gateway a run holds tokens for the broker, its room tools and the
// gateway's aliases only, and never the model Secret.
func TestRunPodPolicyGatewayTokens(t *testing.T) {
	pe := startPodEnv(t, "runController.model.mode=gateway", "runController.model.gateway.aliases.public={tier-light}")
	ctx := t.Context()
	if err := pe.ctl.Create(ctx, gatewayRunPod(t), client.DryRunAll); err != nil {
		t.Fatalf("the run-controller's gateway-mode pod is refused: %v", err)
	}
	token := func(aud string) func(*corev1.Pod) {
		return func(p *corev1.Pod) {
			ttl := int64(3600)
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "extra", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: aud, ExpirationSeconds: &ttl, Path: "t"}}}}}})
		}
	}
	for _, tc := range []struct {
		name, want string
		mut        func(*corev1.Pod)
	}{
		{"the apiserver's audience", "projected ServiceAccount tokens", token("https://kubernetes.default.svc")},
		{"no audience (the apiserver's)", "projected ServiceAccount tokens", token("")},
		{"another prefix", "projected ServiceAccount tokens", token("maku-gatewayx.public.tier-light")},
		// The broker's client listener and the preflight's audience are not a run's.
		{"the planner's audience", "projected ServiceAccount tokens", token("maku-gateway.client.planner")},
		{"the preflight's audience", "projected ServiceAccount tokens", token("maku-gateway.preflight")},
		{"the model Secret", "never reads the model Secret agent-model", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "model", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "agent-model"}}})
		}},
		{"the model Secret, projected", "never reads the model Secret agent-model", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "model", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "agent-model"}}}}}}})
		}},
		{"the model Secret, in a harness env var", "never reads the model Secret agent-model", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "K", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "agent-model"}, Key: "api_key"}}})
		}},
		{"the model Secret, in an init container's envFrom", "never reads the model Secret agent-model", func(p *corev1.Pod) {
			p.Spec.InitContainers[0].EnvFrom = append(p.Spec.InitContainers[0].EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "agent-model"}}})
		}},
	} {
		p := gatewayRunPod(t)
		tc.mut(p)
		if err := pe.ctl.Create(ctx, p, client.DryRunAll); !deniedByPolicy(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a denial naming %q", tc.name, err, tc.want)
		}
	}
	allowed := gatewayRunPod(t)
	token("maku-gateway.public.tier-light")(allowed)
	if err := pe.ctl.Create(ctx, allowed, client.DryRunAll); err != nil {
		t.Errorf("a maku-gateway.public.tier-light token is refused: %v", err)
	}
	// secret mode (the default) keeps today's policy: the same pod with the model Secret passes there.
	if err := startPodEnv(t).ctl.Create(ctx, runPod(t, true, true), client.DryRunAll); err != nil {
		t.Fatalf("secret mode: %v", err)
	}
}
