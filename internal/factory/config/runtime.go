// SPDX-License-Identifier: Apache-2.0

package config

import (
	"regexp"
	"slices"
)

// RunSizes are the run sizes a claim takes (the AgentRun CRD's spec.size).
var RunSizes = []string{"small", "medium", "large"}

// pinnedImage is an image named by digest: the run-controller places nothing a tag could move.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

// Runtime is the run image and size a repository's implementer, tester and reviewer runs take
// (developer review gap 2, owner decision D2). The image must be built from the harness image and
// sit under the run-controller's runtime.allowedImages; its network stays the named profiles.
// +kubebuilder:object:generate=true
type Runtime struct {
	// Image replaces the harness image, pinned by digest: registry/name@sha256:<64 hex>.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`
	// +optional
	Image string `json:"image,omitempty"`
	// Size is small (the default), medium or large.
	// +kubebuilder:validation:Enum=small;medium;large
	// +optional
	Size string `json:"size,omitempty"`
}

func (rt Runtime) validate(bad func(f string, a ...any)) {
	if rt.Image != "" && !pinnedImage.MatchString(rt.Image) {
		bad("runtime.image %q is not pinned by digest (name@sha256:<64 hex>)", rt.Image)
	}
	if rt.Size != "" && !slices.Contains(RunSizes, rt.Size) {
		bad("runtime.size %q is not one of %v", rt.Size, RunSizes)
	}
}
