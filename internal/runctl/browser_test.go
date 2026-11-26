// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

// browserPod renders a screenshots run in gateway mode behind the kubernetes egress proxy, on an
// alias that sees images: every switch the browser touches is on.
func browserPod(t *testing.T, mut ...func(*v1alpha1.AgentRun)) *corev1.Pod {
	t.Helper()
	c := gatewayTestConfig(t)
	c.Images.Browser = browserImage
	c.Model.Gateway.Vision = GatewayAliases{Public: []string{"tier-standard"}}
	c.NetworkPolicy, c.EgressProxy = NetworkPolicyKubernetes, EgressProxy{URL: "http://egress-proxy.maku.svc.cluster.local:3128",
		Namespace: "maku", Labels: map[string]string{"app": "egress-proxy"}}
	r := gatewayRun(append([]func(*v1alpha1.AgentRun){withScreenshots}, mut...)...)
	return Pod(r, c, c.GatewayModel(r), "abcdefgh")
}

func initContainer(p *corev1.Pod, name string) *corev1.Container {
	for i := range p.Spec.InitContainers {
		if p.Spec.InitContainers[i].Name == name {
			return &p.Spec.InitContainers[i]
		}
	}
	return nil
}

func envOf(c *corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestBrowserPodGolden(t *testing.T) {
	golden(t, "pod-browser.golden.yaml", browserPod(t))
}

// VS2, AC4: the browser is a native sidecar holding no workspace, no secret, no token and no proxy.
func TestBrowserSidecarHoldsNothing(t *testing.T) {
	p := browserPod(t)
	b := initContainer(p, "browser")
	if b == nil || b.RestartPolicy == nil || *b.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("browser is a native sidecar")
	}
	if b.Image != browserImage {
		t.Errorf("image %q", b.Image)
	}
	var mounts []string
	for _, m := range b.VolumeMounts {
		mounts = append(mounts, m.Name+":"+m.MountPath)
	}
	if want := []string{"browser-tmp:/tmp", "browser-cache:/home/browser/.cache"}; !slices.Equal(mounts, want) {
		t.Errorf("mounts %v, want %v", mounts, want)
	}
	var names []string
	for _, e := range b.Env {
		if e.ValueFrom != nil {
			t.Errorf("env %s reads from a source", e.Name)
		}
		names = append(names, e.Name+"="+e.Value)
	}
	if want := []string{"MAX_SCREENSHOTS=10", "VISION=1"}; !slices.Equal(names, want) {
		t.Errorf("env %v, want %v", names, want)
	}
	if len(b.EnvFrom) != 0 || len(b.Ports) != 0 {
		t.Errorf("envFrom %v ports %v", b.EnvFrom, b.Ports)
	}
	sc := b.SecurityContext
	if sc == nil || !*sc.ReadOnlyRootFilesystem || !*sc.RunAsNonRoot || *sc.AllowPrivilegeEscalation || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("security context %+v", sc)
	}
	if b.Resources.Limits.Memory().String() != "2Gi" || b.Resources.Requests.Cpu().String() != "250m" {
		t.Errorf("resources %+v", b.Resources)
	}
	// The gate listens on loopback only, which a kubelet httpGet (pod IP) cannot reach: exec probes.
	for name, pr := range map[string]*corev1.Probe{"startup": b.StartupProbe, "readiness": b.ReadinessProbe, "liveness": b.LivenessProbe} {
		if pr == nil || pr.Exec == nil || !slices.Equal(pr.Exec.Command, browserProbe) {
			t.Errorf("%s probe %+v", name, pr)
		}
	}
	sizes := map[string]string{}
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir != nil && strings.HasPrefix(v.Name, "browser-") {
			sizes[v.Name] = v.EmptyDir.SizeLimit.String()
		}
	}
	if sizes["browser-tmp"] != "512Mi" || sizes["browser-cache"] != "128Mi" {
		t.Errorf("emptyDir caps %v", sizes)
	}
}

// The harness gets the gate's MCP URL and whether its model sees images; the proxy still bypasses
// loopback, so the harness reaches the gate directly.
func TestBrowserHarnessEnv(t *testing.T) {
	h := harnessEnv(browserPod(t))
	if h["BROWSER_MCP_URL"] != "http://127.0.0.1:9223/mcp" || h["SCREENSHOTS_VISION"] != "1" {
		t.Errorf("harness env %v", h)
	}
	if !slices.Contains(strings.Split(h["NO_PROXY"], ","), "127.0.0.1") || !slices.Contains(strings.Split(h["no_proxy"], ","), "127.0.0.1") {
		t.Errorf("NO_PROXY %q does not bypass loopback", h["NO_PROXY"])
	}
	// A text-only alias: the gate keeps the screenshots for humans, the model gets text (C15).
	text := browserPod(t, func(r *v1alpha1.AgentRun) { r.Spec.Model = "tier-light" })
	if h := harnessEnv(text); h["SCREENSHOTS_VISION"] != "0" || envOf(initContainer(text, "browser"))["VISION"] != "0" {
		t.Errorf("text-only alias: harness %v", h)
	}
	if _, ok := text.Annotations[runwatch.VisionAnnotation]; ok {
		t.Error("a text-only run is annotated vision")
	}
}

// Task 15: the bridge reads the gate's loopback URL, and the pod carries the cap and vision.
func TestBrowserBridgeAndAnnotations(t *testing.T) {
	p := browserPod(t)
	if got := envOf(initContainer(p, "room-bridge"))["BROWSER_GATE_URL"]; got != "http://127.0.0.1:9223" {
		t.Errorf("bridge BROWSER_GATE_URL %q", got)
	}
	if p.Annotations[runwatch.ScreenshotsAnnotation] != "10" || p.Annotations[runwatch.VisionAnnotation] != "true" {
		t.Errorf("annotations %v", p.Annotations)
	}
	if got := initContainer(p, "browser"); got == nil || p.Spec.InitContainers[len(p.Spec.InitContainers)-1].Name != "browser" {
		t.Error("the browser comes after the room's sidecars")
	}
}

// A run with no room has no bridge to relay screenshots: the browser is its only init container.
func TestBrowserWithoutRoom(t *testing.T) {
	p := browserPod(t, func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = "" })
	if len(p.Spec.InitContainers) != 1 || p.Spec.InitContainers[0].Name != "browser" {
		t.Fatalf("init containers %v", p.Spec.InitContainers)
	}
	if harnessEnv(p)["BROWSER_MCP_URL"] == "" {
		t.Error("the harness still gets the browser")
	}
}

// AC1: no opt-in, no sidecar, no env, no annotation; a disabled opt-in is none either.
func TestNoBrowserWithoutOptIn(t *testing.T) {
	c := testConfig(t)
	c.Images.Browser = browserImage
	for name, r := range map[string]*v1alpha1.AgentRun{
		"no opt-in":       goRun(),
		"disabled opt-in": goRun(withScreenshots, func(r *v1alpha1.AgentRun) { r.Spec.Screenshots.Enabled = false }),
	} {
		t.Run(name, func(t *testing.T) {
			p := Pod(r, c, testModel, "abcdefgh")
			if initContainer(p, "browser") != nil {
				t.Fatal("AC1: no opt-in, no sidecar")
			}
			if _, ok := harnessEnv(p)["BROWSER_MCP_URL"]; ok {
				t.Error("harness BROWSER_MCP_URL")
			}
			if _, ok := envOf(initContainer(p, "room-bridge"))["BROWSER_GATE_URL"]; ok {
				t.Error("bridge BROWSER_GATE_URL")
			}
			if _, ok := p.Annotations[runwatch.ScreenshotsAnnotation]; ok {
				t.Error("screenshots annotation")
			}
		})
	}
}
