// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
)

const (
	// AccessMaintainers lets only the repository's maintainers start a task: the default.
	AccessMaintainers = "maintainers"
	// AccessWrite also lets anyone GitHub lets write start one, where per-person caps are enforced.
	AccessWrite = "write"
)

// RepositoryAccess is who may start a task on a repository (developer review gap 1). Approving a plan,
// merging, a deeper retry and the settings stay with its maintainers.
// +kubebuilder:object:generate=true
type RepositoryAccess struct {
	// Start is maintainers (the default) or write. Write takes effect only where per-person daily
	// caps are enforced (budgets.perPerson.enabled and budgets.enforcePrincipal): every start is then
	// charged to the developer who made it, within their own day (owner decision D1).
	// +kubebuilder:validation:Enum=maintainers;write
	// +optional
	Start string `json:"start,omitempty"`
}

func (ra RepositoryAccess) validate(bad func(f string, a ...any)) {
	switch ra.Start {
	case "", AccessMaintainers, AccessWrite:
	default:
		bad("access.start: %q is neither maintainers nor write", ra.Start)
	}
}

// WriteStartsConfigured is whether the repository asks for starts by write access, in effect or not.
func (r *Repository) WriteStartsConfigured() bool { return r != nil && r.Access.Start == AccessWrite }

// WriteStarts is whether write access starts a task on the repository: asked for, and per-person
// caps enforced (D1).
func (r *Repository) WriteStarts(b Budgets) bool {
	return r.WriteStartsConfigured() && b.PerPerson.Enabled && b.EnforcePrincipal
}

// MayStart is whether login may start a task on the repository, provided GitHub lets them write,
// which the caller checks: a maintainer always; anyone else where WriteStarts. A bot never starts
// by write access.
func (r *Repository) MayStart(login string, b Budgets) bool {
	if r.IsMaintainer(login) {
		return true
	}
	return login != "" && !strings.HasSuffix(strings.ToLower(login), "[bot]") && r.WriteStarts(b)
}
