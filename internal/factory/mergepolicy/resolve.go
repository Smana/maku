// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Resolved is a binding's class as the gate would read it, with what the gate does not read yet.
type Resolved struct {
	// Class is the policy, the binding's parameters and the guardrails (config.Resolve).
	Class    config.Class
	Approval []string // requiresApproval's paths
	// Approvals are requiresApproval's leaves as written: what a grant's hash reads, so the class
	// and its in-sync central one hash alike (B13).
	Approvals []config.ApprovalLeaf
	// PolicyGeneration is a binding's: the generation of the MergePolicy its class was resolved
	// from, the last resolved one's when that one serves (B9, B12).
	PolicyGeneration int64
}

// inShadow is r with a live class in shadow (stage 4's pause and MP8's demotion).
func (r Resolved) inShadow() Resolved {
	if r.Class.Live {
		r.Class.Live, r.Class.Shadow = false, true
	}
	return r
}

// Validate is the whole file against the central config: ValidateFile's error, without a
// repository's merge.
func Validate(f *File, c *config.Config) error {
	_, err := ValidateFile(f, nil, c)
	return err
}

// Report is what a file's validation found beside its error: the resolved classes, and the
// bindings that did not resolve, kept apart so one never invalidates the file (B9, B15).
type Report struct {
	Classes       map[string]Resolved
	BindingErrors map[string]config.BindingRefusal
}

// ValidateFile is the whole file against the central config and the repository it is read for:
// a file with no policy has none and passes. One that binds policies binds docs-links and revert
// (OD-8), and passes whatever its bindings resolve to: each goes through config.Resolve against
// the policy the factory serves, the guardrails and rc's merge (nil: none), and one that does not
// resolve is reported in BindingErrors. review is bound off with no parameter (Parse's shape): its
// merge authority stays implicit, in the repository's merge block.
func ValidateFile(f *File, rc *config.Repository, c *config.Config) (Report, error) {
	var rep Report
	if !f.Declares() {
		return rep, nil
	}
	var errs []error
	for _, name := range []string{"docs-links", "revert"} {
		if !declares(f, name) {
			errs = append(errs, fmt.Errorf("class %s is required in a file that declares classes (OD-8): add it, "+
				"%s keeps it unarmed", name, fmt.Sprintf(`"- {name: %s, mode: 'off'}"`, name)))
		}
	}
	rep.Classes, rep.BindingErrors = ResolveBindings(f, rc, c)
	return rep, errors.Join(errs...)
}

// declares reports whether the file binds name.
func declares(f *File, name string) bool {
	return slices.ContainsFunc(f.MergePolicies, func(b config.Binding) bool { return b.Name == name })
}

// ResolveBindings resolves a binding file's bindings in list order, as resources.Assemble resolves
// a Repository's (B4, B6, B14): each against the policy the factory serves now (cfg.Policies), under
// the guardrails and rc's merge. A binding naming no served policy is MissingRef, and the later of
// two ambiguous adopted bindings is AmbiguousAdoption. The refused are absent from the classes.
func ResolveBindings(f *File, rc *config.Repository, c *config.Config) (map[string]Resolved, map[string]config.BindingRefusal) {
	var merge config.Merge
	if rc != nil {
		merge = rc.Merge
	}
	ctx := policyContext(c)
	classes, refused := map[string]Resolved{}, map[string]config.BindingRefusal{}
	served := map[string]config.Class{}
	for _, b := range f.MergePolicies {
		p, ok := c.Policies[b.Name]
		if !ok {
			refused[b.Name] = config.BindingRefusal{Reason: v1alpha1.ReasonMissingRef, Message: fmt.Sprintf("the factory serves no MergePolicy %s", b.Name)}
			continue
		}
		cl, refusal := config.Resolve(b.Name, p.Spec, b, ctx, merge)
		if refusal != nil {
			refused[b.Name] = *refusal
			continue
		}
		if other := ambiguousWith(cl, f.MergePolicies, served); other != "" {
			refused[b.Name] = config.BindingRefusal{Reason: config.ReasonAmbiguousAdoption, Message: fmt.Sprintf(
				"class %s adopts the pull requests class %s adopts: the same diffShape %s, an overlapping login and branch prefix; bind one of them (B6)",
				b.Name, other, cl.DiffShape)}
			continue
		}
		served[b.Name] = cl
		classes[b.Name] = resolvedOf(cl)
		r := classes[b.Name]
		r.PolicyGeneration = p.Generation
		classes[b.Name] = r
	}
	return classes, refused
}

// ambiguousWith is the first binding, in list order, whose served class is ambiguous with cl.
func ambiguousWith(cl config.Class, order []config.Binding, served map[string]config.Class) string {
	for _, b := range order {
		if other, ok := served[b.Name]; ok && config.Ambiguous(other, cl) {
			return b.Name
		}
	}
	return ""
}

// resolvedOf is a resolved binding's class as the gate reads a file's: the class, and its
// approval subset's paths.
func resolvedOf(cl config.Class) Resolved {
	r := Resolved{Class: cl}
	for _, a := range cl.RequiresApproval {
		r.Approval = append(r.Approval, a.ChangedFiles...)
		r.Approvals = append(r.Approvals, config.ApprovalLeaf{ChangedFiles: slices.Clone(a.ChangedFiles)})
	}
	return r
}

// policyContext is what resolving a binding reads from the factory (B14).
func policyContext(c *config.Config) config.PolicyContext {
	return config.PolicyContext{HarnessProfiles: c.HarnessProfiles, Guardrails: c.MergeGuardrails, Templates: c.Templates, Loops: c.Loops}
}
