// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// ErrGuardrail marks a class refused by the central config rather than by its own shape, for the
// load-error metric's reason.
var ErrGuardrail = errors.New("guardrail")

// Resolved is a class as the gate would read it, with what the gate does not read yet.
type Resolved struct {
	// Class is all's leaves, the deny and the guardrails: the class itself when it has no any.
	Class config.Class
	// Branches are Class with each branch of any added, one variant per branch; nil without any.
	Branches []config.Class
	Approval []string // requiresApproval's paths
	// Approvals are requiresApproval's leaves as written: what a grant's hash reads, so the class
	// and its in-sync central one hash alike (B13).
	Approvals []config.ApprovalLeaf
	Profile   config.HarnessProfile
	// PolicyGeneration is a binding's: the generation of the MergePolicy its class was resolved
	// from, the last resolved one's when that one serves (B9, B12); 0 for a class the file defines
	// itself.
	PolicyGeneration int64
}

// Variants are the classes the evaluator decides on, one at a time: the class holds when one
// variant does.
func (r Resolved) Variants() []config.Class {
	if r.Branches != nil {
		return r.Branches
	}
	return []config.Class{r.Class}
}

// inShadow is r with every live variant in shadow (stage 4's pause and MP8's demotion).
func (r Resolved) inShadow() Resolved {
	shadow := func(cl config.Class) config.Class {
		if cl.Live {
			cl.Live, cl.Shadow = false, true
		}
		return cl
	}
	r.Class = shadow(r.Class)
	if r.Branches != nil {
		bs := make([]config.Class, len(r.Branches))
		for i, b := range r.Branches {
			bs[i] = shadow(b)
		}
		r.Branches = bs
	}
	return r
}

// Resolve turns a parsed class into the config.Class the gate decides on, under the guardrails:
// they add protected paths and mandatory checks, an undeclared cap inherits theirs, and a declared
// cap above theirs is refused (MP6). Each branch of any is resolved the same way.
func Resolve(name string, p ClassPolicy, c *config.Config) (Resolved, error) {
	profile, ok := c.HarnessProfiles[p.Harness]
	if !ok {
		return Resolved{}, fmt.Errorf("class %s: %w: harness %q is not in harnessProfiles", name, ErrGuardrail, p.Harness)
	}
	adopted := p.Author == AuthorAdopt
	if none := profile.Template == config.TemplateNone; none != adopted {
		return Resolved{}, fmt.Errorf("class %s: %w: harness %q: template none runs nothing, and only an adopted class takes it", name, ErrGuardrail, p.Harness)
	}
	cl := config.Class{Description: p.Description, Live: p.Mode == ModeLive, Shadow: p.Mode == ModeShadow,
		Checks: profile.Checks, TestFirst: profile.TestFirst, MaxPerDay: p.Limits.MaxPerDay, Monitor: p.Monitor}
	// Only a class that declares variants carries its floor into the gate's class: without them,
	// harness is MP2's checks and testFirst, read above, and the class runs as before. On review
	// harness has no other role (R30): it is the evaluation's floor whenever declared.
	if name == "review" || len(p.HarnessAllowed) > 0 || p.Escalation != nil {
		cl.Harness, cl.HarnessAllowed, cl.Escalation = p.Harness, p.HarnessAllowed, p.Escalation
	}
	var errs []error
	for _, m := range c.VariantProblems(name, cl) {
		errs = append(errs, fmt.Errorf("class %s: %w: %s", name, ErrGuardrail, m))
	}
	if adopted {
		cl.Author = &config.Author{Adopt: p.Adopt}
	}
	for _, l := range p.Conditions.All {
		apply(&cl, l)
	}
	for _, d := range p.Conditions.Deny {
		cl.ProtectedFiles = append(cl.ProtectedFiles, d.ProtectedFiles...)
	}
	r := Resolved{Profile: profile}
	for _, a := range p.RequiresApproval {
		r.Approval = append(r.Approval, a.ChangedFiles...)
		r.Approvals = append(r.Approvals, config.ApprovalLeaf{ChangedFiles: slices.Clone(a.ChangedFiles)})
	}
	for _, branch := range p.Conditions.Any {
		v := clone(cl)
		for _, l := range branch {
			apply(&v, l)
		}
		r.Branches = append(r.Branches, v)
	}
	for i := range r.Branches {
		errs = append(errs, guard(name, &r.Branches[i], c.MergeGuardrails))
	}
	errs = append(errs, guard(name, &cl, c.MergeGuardrails))
	if err := errors.Join(errs...); err != nil {
		return Resolved{}, err
	}
	r.Class = cl
	return r, nil
}

// apply adds one leaf to a class.
func apply(cl *config.Class, l Leaf) {
	cl.AllowedFiles = append(cl.AllowedFiles, l.AllowedFiles...)
	cl.HoldoutChecks = append(cl.HoldoutChecks, l.HoldoutChecks...)
	cl.CompanionFiles = append(cl.CompanionFiles, l.CompanionFiles...)
	switch {
	case l.ModifyOnly != nil:
		cl.ModifyOnly = *l.ModifyOnly
	case l.MaxPatchFiles != nil:
		cl.MaxPatchFiles = *l.MaxPatchFiles
	case l.MaxModifiedLines != nil:
		cl.MaxModifiedLines = *l.MaxModifiedLines
	case l.DiffShape != nil:
		cl.DiffShape = *l.DiffShape
	case l.ReleaseAgeCheck != nil:
		cl.ReleaseAgeCheck = *l.ReleaseAgeCheck
	case l.Oracle != nil:
		cl.Oracle = l.Oracle
	case l.RequiredLabel != nil:
		cl.RequiredLabel = *l.RequiredLabel
	}
}

// clone copies a class's lists, so a branch's additions stay its own.
func clone(cl config.Class) config.Class {
	cl.AllowedFiles = slices.Clone(cl.AllowedFiles)
	cl.ProtectedFiles = slices.Clone(cl.ProtectedFiles)
	cl.HoldoutChecks = slices.Clone(cl.HoldoutChecks)
	cl.CompanionFiles = slices.Clone(cl.CompanionFiles)
	return cl
}

// guard applies the guardrails to one variant (config.ApplyGuardrails, B14). Parse has refused a
// negative cap.
func guard(name string, cl *config.Class, g config.MergeGuardrails) error {
	if over := config.ApplyGuardrails(cl, g); over != "" {
		return fmt.Errorf("class %s: %w: %s", name, ErrGuardrail, over)
	}
	return nil
}

// unbounded is an agents class decided (live or shadow) with a variant that names no allowed path.
// docs-links and revert are exempt: their links-only shape is built in (R52). An adopted class's
// diff shape bounds it.
func unbounded(name string, p ClassPolicy, r Resolved) bool {
	if p.Mode == ModeOff || p.Author != AuthorAgents || config.BuiltInShape(name) != "" {
		return false
	}
	return slices.ContainsFunc(r.Variants(), func(v config.Class) bool { return len(v.AllowedFiles) == 0 })
}

// OnlyGuardrail is true when every fault err joins is ErrGuardrail's: a file that is also invalid
// is invalid, whatever errors.Is finds somewhere in the join.
func OnlyGuardrail(err error) bool {
	if err == nil {
		return false
	}
	switch e := err.(type) { //nolint:errorlint // walking the tree is the point: errors.Is answers "any", this is "all"
	case interface{ Unwrap() []error }:
		for _, c := range e.Unwrap() {
			if !OnlyGuardrail(c) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		if e.Unwrap() == ErrGuardrail { //nolint:errorlint // the sentinel itself, one level down
			return true
		}
		return OnlyGuardrail(e.Unwrap())
	}
	return false
}

// Validate is the whole file against the central config: ValidateFile's error, without a
// repository's merge.
func Validate(f *File, c *config.Config) error {
	_, err := ValidateFile(f, nil, c)
	return err
}

// Report is what a file's validation found beside its error: the resolved classes, the bindings
// that did not resolve, kept apart so one never invalidates the file (B9, B15), and the
// deprecations, sentences about what the next minor refuses (PR4).
type Report struct {
	Classes       map[string]Resolved
	BindingErrors map[string]config.BindingRefusal
	Deprecations  []string
}

// ValidateFile is the whole file against the central config and the repository it is read for:
// a file with no policy has none and passes. One that declares one declares docs-links and revert
// (OD-8). A class-shape file passes when each class passes Resolve and the gate's own class check,
// every variant of it; review may be declared for its evaluation keys only (R30, Parse's shape):
// its merge authority stays implicit, in the repository's merge block (approveToMerge,
// maintainers), which no class file holds. A binding file passes whatever its bindings resolve
// to: each goes through config.Resolve against the policy the factory serves, the guardrails and
// rc's merge (nil: none), and one that does not resolve is reported in BindingErrors.
func ValidateFile(f *File, rc *config.Repository, c *config.Config) (Report, error) {
	var rep Report
	if !f.Declares() {
		return rep, nil
	}
	var errs []error
	for _, name := range []string{"docs-links", "revert"} {
		if !declares(f, name) {
			errs = append(errs, fmt.Errorf("class %s is required in a file that declares classes (OD-8): add it, "+
				"%s keeps it unarmed", name, unarmed(f, name)))
		}
	}
	if f.Bound() {
		rep.Classes, rep.BindingErrors = ResolveBindings(f, rc, c)
		return rep, errors.Join(errs...)
	}
	rep.Classes = map[string]Resolved{}
	for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
		r, err := Resolve(name, f.Classes[name], c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, v := range r.Variants() {
			config.ValidateClass(name, v, func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) })
		}
		if unbounded(name, f.Classes[name], r) {
			errs = append(errs, fmt.Errorf("class %s: a live or shadow class needs allowedFiles, in all or in every branch of any: "+
				"without it every path of the repository is in the class, and only policy-bot fences it", name))
		}
		if f.Classes[name].Conditions.Any != nil {
			rep.Deprecations = append(rep.Deprecations, fmt.Sprintf("class %s: conditions.any is deprecated, and the next minor refuses it "+
				"with the classes grammar: bind one MergePolicy per branch in mergePolicies (B7)", name))
		}
		rep.Classes[name] = r
	}
	if err := errors.Join(errs...); err != nil {
		return Report{Deprecations: rep.Deprecations}, err
	}
	return rep, nil
}

// declares reports whether the file declares class name, in either shape.
func declares(f *File, name string) bool {
	if f.Bound() {
		return slices.ContainsFunc(f.MergePolicies, func(b config.Binding) bool { return b.Name == name })
	}
	_, ok := f.Classes[name]
	return ok
}

// unarmed is how the file declares name unarmed, in its shape.
func unarmed(f *File, name string) string {
	if f.Bound() {
		return fmt.Sprintf(`"- {name: %s, mode: 'off'}"`, name)
	}
	return fmt.Sprintf("\"%s: {mode: off, author: agents, harness: <a profile>}\"", name)
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
