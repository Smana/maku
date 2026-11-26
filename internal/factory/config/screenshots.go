// SPDX-License-Identifier: Apache-2.0

package config

import "slices"

// The screenshot caps per run (v5 Q16, spec §5.3): the default when a Repository names none, and
// the ceiling no Repository may raise.
const (
	DefaultMaxScreenshots int32 = 10
	MaxScreenshotsCeiling int32 = 25
)

// ScreenshotRoles are the roles a browser may serve (VS5): a reviewer, triager or scoper never
// gets one.
var ScreenshotRoles = []string{"implementer", "tester"}

// Screenshots is a Repository's opt-in to the browser sidecar (v5 Q16). It is set by a maintainer on
// the Repository only, never by an issue, a label or a run request, and off by default.
// +kubebuilder:object:generate=true
type Screenshots struct {
	// Enabled gives the repository's implementer and tester runs a loopback-only browser.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Roles narrows the roles that get the browser; empty: implementer and tester.
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=implementer;tester
	// +optional
	Roles []string `json:"roles,omitempty"`
	// MaxScreenshots caps a run's screenshots; empty: 10.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=25
	// +optional
	MaxScreenshots int32 `json:"maxScreenshots,omitempty"`
}

// For is the run's screenshot cap and whether a run of role gets the browser. A nil Screenshots,
// one not enabled, or a role outside ScreenshotRoles or Roles gets none.
func (s *Screenshots) For(role string) (int32, bool) {
	if s == nil || !s.Enabled || !slices.Contains(ScreenshotRoles, role) {
		return 0, false
	}
	if len(s.Roles) > 0 && !slices.Contains(s.Roles, role) {
		return 0, false
	}
	n := s.MaxScreenshots
	if n <= 0 {
		n = DefaultMaxScreenshots
	}
	return min(n, MaxScreenshotsCeiling), true
}

// validate holds a config file's screenshots to the bounds the Repository CRD admits.
func (s *Screenshots) validate(bad func(string, ...any)) {
	for _, r := range s.Roles {
		if !slices.Contains(ScreenshotRoles, r) {
			bad("screenshots.roles: %q is not one of %v (VS5)", r, ScreenshotRoles)
		}
	}
	if s.MaxScreenshots < 0 || s.MaxScreenshots > MaxScreenshotsCeiling {
		bad("screenshots.maxScreenshots: %d is not between 1 and %d", s.MaxScreenshots, MaxScreenshotsCeiling)
	}
}
