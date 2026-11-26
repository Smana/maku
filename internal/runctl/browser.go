// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"fmt"
	"slices"

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
