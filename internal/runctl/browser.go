// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// BrowserConfig is the browser sidecar an implementer or tester run with spec.screenshots gets (v5
// Q16). Its resources are the chart's only, never a Repository's (C10).
type BrowserConfig struct {
	// Resources are the sidecar's requests and limits; each key left out takes its default
	// (requests cpu 250m, memory 512Mi; limits cpu 2, memory 2Gi: spec §4.1).
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`
}

// browserDefaults are spec §4.1's sidecar resources: Chromium's RSS is about 600 MiB at its first
// screenshot (ledger BQ3), so the limit leaves room for a heavy page.
var browserDefaults = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")},
}

// applyDefaults fills each resource key the config leaves out.
func (b *BrowserConfig) applyDefaults() {
	fill := func(list *corev1.ResourceList, defaults corev1.ResourceList) {
		if *list == nil {
			*list = corev1.ResourceList{}
		}
		for k, v := range defaults {
			if _, ok := (*list)[k]; !ok {
				(*list)[k] = v.DeepCopy()
			}
		}
	}
	fill(&b.Resources.Requests, browserDefaults.Requests)
	fill(&b.Resources.Limits, browserDefaults.Limits)
}

// validate refuses a request above its limit, which the API server would refuse on every browser
// pod instead.
func (b BrowserConfig) validate() []error {
	var errs []error
	for k, req := range b.Resources.Requests {
		if lim, ok := b.Resources.Limits[k]; ok && req.Cmp(lim) > 0 {
			errs = append(errs, fmt.Errorf("browser.resources: requests.%s %s is above limits.%s %s", k, req.String(), k, lim.String()))
		}
	}
	return errs
}

// screenshotsOn reports whether the run asks for the browser sidecar.
func screenshotsOn(r *v1alpha1.AgentRun) bool {
	return r.Spec.Screenshots != nil && r.Spec.Screenshots.Enabled
}

// VisionFor reports whether alias, on the run's data class's listener, accepts images (C4). It
// decides only whether a screenshot reaches the model; the humans get it either way (C15). In
// model.mode secret no alias is known to, so it is false.
func (c Config) VisionFor(r *v1alpha1.AgentRun, alias string) bool {
	if !c.Gateway() {
		return false
	}
	vision := c.Model.Gateway.Vision.Public
	if class(r) == "internal" {
		vision = c.Model.Gateway.Vision.Internal
	}
	return slices.Contains(vision, alias)
}

// BrowserGatePort is the browser gate's loopback port: the harness's MCP server named browser, and
// the room-bridge's screenshot source.
const BrowserGatePort = 9223

// browserGateURL is the gate's base URL on the pod's shared loopback.
var browserGateURL = fmt.Sprintf("http://127.0.0.1:%d", BrowserGatePort)

// browserProbe is the sidecar's probe: the gate listens on loopback only, which a kubelet httpGet
// cannot reach (it dials the pod IP, and a Host of 127.0.0.1 is the node's own loopback), so the gate
// binary checks its own /healthz from inside the container.
var browserProbe = []string{"/usr/local/bin/maku-browser-gate", "-probe", browserGateURL + "/healthz"}

// browserSidecar is the opt-in browser (v5 Q16, VS2): a native sidecar (C6) holding no workspace, no
// secret, no token and no proxy, so the kubelet ends it with the harness and restarts it after an
// OOM. Its env is the run's cap and whether the model sees images; nothing else.
func browserSidecar(r *v1alpha1.AgentRun, c Config, vision bool) (corev1.Container, []corev1.Volume) {
	always := corev1.ContainerRestartPolicyAlways
	health := corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: browserProbe}}
	ctr := corev1.Container{
		Name: "browser", Image: c.Images.Browser, RestartPolicy: &always,
		Env: []corev1.EnvVar{
			{Name: "MAX_SCREENSHOTS", Value: strconv.Itoa(int(r.Spec.Screenshots.MaxScreenshots))},
			{Name: "VISION", Value: zeroOne(vision)},
		},
		StartupProbe:    &corev1.Probe{ProbeHandler: health, PeriodSeconds: 1, FailureThreshold: 60, TimeoutSeconds: 3},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: health, PeriodSeconds: 10, TimeoutSeconds: 3},
		LivenessProbe:   &corev1.Probe{ProbeHandler: health, PeriodSeconds: 20, FailureThreshold: 3, TimeoutSeconds: 3},
		Resources:       *c.Browser.Resources.DeepCopy(),
		SecurityContext: containerSecurity(),
		VolumeMounts:    []corev1.VolumeMount{{Name: "browser-tmp", MountPath: "/tmp"}, {Name: "browser-cache", MountPath: "/home/browser/.cache"}},
	}
	return ctr, []corev1.Volume{emptyDir("browser-tmp", "512Mi", false), emptyDir("browser-cache", "128Mi", false)}
}

// browserEnv is the harness's side: the gate's MCP URL, and whether its model sees images.
func browserEnv(vision bool) []corev1.EnvVar {
	return []corev1.EnvVar{{Name: "BROWSER_MCP_URL", Value: browserGateURL + "/mcp"}, {Name: "SCREENSHOTS_VISION", Value: zeroOne(vision)}}
}

func zeroOne(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
