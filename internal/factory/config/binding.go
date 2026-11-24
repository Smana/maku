// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strings"
)

// The modes a binding puts its class in (B4, MR9): live merges, shadow is decided and never armed,
// disabled is neither (Q7, MR9).
const (
	ModeLive     = "live"
	ModeShadow   = "shadow"
	ModeDisabled = "disabled"
)

// Disabled reports whether mode evaluates nothing.
func Disabled(mode string) bool { return mode == ModeDisabled }

// LimitMode keeps cl's mode at most mode: a refused live edit can keep a previous live or shadow
// class, while a refused shadow edit can serve only shadow. ok is false for a mode that cannot
// safely restore anything.
func LimitMode(cl Class, mode string) (out Class, ok bool) {
	switch mode {
	case ModeLive:
		return cl, true
	case ModeShadow:
		cl.Live, cl.Shadow = false, true
		return cl, true
	default:
		return Class{}, false
	}
}

// The reasons a binding does not resolve (B9). MissingRef, the reason for a binding to an absent
// policy, is the resources layer's: Resolve is handed a policy.
const (
	ReasonMissingParameter  = "MissingParameter"
	ReasonParameterTwice    = "ParameterTwice"
	ReasonOverPolicy        = "OverPolicy"
	ReasonGuardrailMismatch = "GuardrailMismatch"
	ReasonAmbiguousAdoption = "AmbiguousAdoption"
	// ReasonUnbounded is an evaluated class with neither allowedFiles nor a diff shape (MR6).
	ReasonUnbounded = "Unbounded"
	ReasonInvalid   = "Invalid"
)

// Binding is one repository's use of a MergePolicy: the policy's name, mode, and parameters. A binding tightens or fills; it never widens what the policy says.
// +kubebuilder:object:generate=true
type Binding struct {
	// Name is the MergePolicy's, so the class's (B1).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=live;shadow;disabled
	Mode string `json:"mode"`
	// CompanionFiles, RequiredLabel and OracleWorkflow are fill-once: set here or on the policy,
	// never both. AllowedFiles fills the policy's when it has none, and narrows it when it has (MR3).
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	// +kubebuilder:validation:items:Pattern=`^\^.*\$$`
	AllowedFiles []string `json:"allowedFiles,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	CompanionFiles []string `json:"companionFiles,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	RequiredLabel string `json:"requiredLabel,omitempty"`
	// OracleWorkflow is the workflow on main whose runs report the policy's oracle findings.
	// +kubebuilder:validation:MaxLength=1024
	OracleWorkflow string `json:"oracleWorkflow,omitempty"`
	// HoldoutChecks, ProtectedFiles and RequiresApproval are added to the policy's.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	HoldoutChecks []string `json:"holdoutChecks,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	ProtectedFiles []string `json:"protectedFiles,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	RequiresApproval []ApprovalLeaf `json:"requiresApproval,omitempty"`
	// MaxPatchFiles, MaxModifiedLines and MaxPerDay lower the policy's caps, never raise them.
	// +kubebuilder:validation:Minimum=0
	MaxPatchFiles int `json:"maxPatchFiles,omitempty"`
	// +kubebuilder:validation:Minimum=0
	MaxModifiedLines int `json:"maxModifiedLines,omitempty"`
	// +kubebuilder:validation:Minimum=0
	MaxPerDay int `json:"maxPerDay,omitempty"`
}

// PolicySpec is a MergePolicy's class definition: MergeClass, without mode, workflow fields or requires.
// +kubebuilder:object:generate=true
type PolicySpec struct {
	MergeClass `json:",inline"`
}

// ServedPolicy is an accepted class-shape MergePolicy and the generation the factory serves.
type ServedPolicy struct {
	Spec       PolicySpec
	Generation int64
}

// PolicyContext is what resolving a binding reads from the factory: the harness profiles a class
// names, and the guardrails that cap every binding (B14).
type PolicyContext struct {
	HarnessProfiles map[string]HarnessProfile
	Guardrails      MergeGuardrails
	// Templates and Loops are what comparing a variant to its floor reads (TE2, TE6): without
	// them only the checks that read the profiles alone run.
	Templates map[string]Template
	Loops     map[string]Loop
}

// BindingRefusal is why a binding does not resolve: one of the reasons above, and the message
// status shows.
type BindingRefusal struct {
	Reason, Message string
}

// Resolve turns a policy and one repository's binding of it into the class the gate decides on
// (B4, B14): the policy's meaning, the binding's mode, each fill-once parameter from where it is
// set, the additive lists unioned, the lower cap, the repository's Flux source, then the
// guardrails. The resolved class must pass the class rules. It never aliases the policy.
func Resolve(name string, p PolicySpec, b Binding, ctx PolicyContext, m Merge) (Class, *BindingRefusal) {
	cl, reason, problems := resolve(name, p, b, ctx, m)
	if reason != "" {
		return Class{}, &BindingRefusal{Reason: reason, Message: strings.Join(problems, "; ")}
	}
	return cl, nil
}

// ValidatePolicy is every reason a policy is refused alone (B9): its own shape, then a resolve
// with placeholders for parameters a binding may supply, in mode disabled.
func ValidatePolicy(name string, p PolicySpec, ctx PolicyContext) []string {
	if problems := policyShapeProblems(name, p); len(problems) > 0 {
		return problems
	}
	b := Binding{Name: name, Mode: ModeDisabled}
	if len(p.AllowedFiles) == 0 {
		b.AllowedFiles = []string{"^placeholder$"}
	}
	if p.Oracle != nil {
		b.OracleWorkflow = "placeholder.yaml"
	}
	if len(p.HoldoutChecks) == 0 {
		b.HoldoutChecks = []string{"placeholder"}
	}
	if p.DiffShape == "added-entry" && p.RequiredLabel == "" {
		b.RequiredLabel = "placeholder"
	}
	ctx.Guardrails = MergeGuardrails{} // valid alone: a binding may lower a cap under a guardrail
	_, _, problems := resolve(name, p, b, ctx, Merge{FluxSource: "ns/name", ContractCheck: "placeholder"})
	return problems
}

// policyShapeProblems is what a policy may not say whatever binds it: review (which is not a
// policy) or repository-specific parameters.
func policyShapeProblems(name string, p PolicySpec) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf("class %s: ", name)+fmt.Sprintf(f, a...)) }
	if name == "review" {
		bad("review is not a policy")
	}
	if len(p.NarrowedFiles) > 0 {
		bad("narrowedFiles is set by a binding's allowedFiles: set allowedFiles on the policy")
	}
	if p.Oracle != nil && p.Oracle.Workflow != "" {
		bad("oracle.workflow is the repository's: set oracleWorkflow on the binding")
	}
	if p.Monitor != nil && p.Monitor.Flux != nil && p.Monitor.Flux.Source != "" {
		bad("the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)")
	}
	return out
}

// resolve is Resolve, with the problems kept apart for ValidatePolicy.
func resolve(name string, p PolicySpec, b Binding, ctx PolicyContext, m Merge) (Class, string, []string) {
	refuse := func(reason, f string, a ...any) (Class, string, []string) {
		return Class{}, reason, []string{fmt.Sprintf(f, a...)}
	}
	switch {
	case b.Name != name:
		return refuse(ReasonInvalid, "class %s: the binding names MergePolicy %q", name, b.Name)
	case b.Mode != ModeLive && b.Mode != ModeShadow && !Disabled(b.Mode):
		return refuse(ReasonInvalid, "class %s: mode %q is live, shadow or disabled", name, b.Mode)
	case name == "review":
		return refuse(ReasonInvalid, "class review: review is not a policy")
	}
	if problems := policyShapeProblems(name, p); len(problems) > 0 {
		return Class{}, ReasonInvalid, problems
	}
	cl := p.Class()
	cl.Live, cl.Shadow = b.Mode == ModeLive, b.Mode == ModeShadow

	// MR3: a binding's allowedFiles over the policy's narrows it; both must admit a path.
	if len(p.AllowedFiles) > 0 && len(b.AllowedFiles) > 0 {
		cl.NarrowedFiles = slices.Clone(b.AllowedFiles)
	}

	// Fill once: in one place; allowedFiles is filled from the binding only where the policy has none.
	for _, f := range []struct {
		key          string
		policy, bind bool
		fill         func()
	}{
		{"allowedFiles", len(p.AllowedFiles) > 0, len(b.AllowedFiles) > 0 && len(p.AllowedFiles) == 0, func() { cl.AllowedFiles = slices.Clone(b.AllowedFiles) }},
		{"companionFiles", len(p.CompanionFiles) > 0, len(b.CompanionFiles) > 0, func() { cl.CompanionFiles = slices.Clone(b.CompanionFiles) }},
		{"requiredLabel", p.RequiredLabel != "", b.RequiredLabel != "", func() { cl.RequiredLabel = b.RequiredLabel }},
		{"oracleWorkflow", false, b.OracleWorkflow != "", func() {
			if cl.Oracle != nil {
				cl.Oracle.Workflow = b.OracleWorkflow
			}
		}},
	} {
		switch {
		case f.policy && f.bind:
			return refuse(ReasonParameterTwice, "%s is set by MergePolicy %s and by the binding", f.key, name)
		case f.bind:
			f.fill()
		}
	}
	if b.OracleWorkflow != "" && p.Oracle == nil {
		return refuse(ReasonInvalid, "class %s: oracleWorkflow needs an oracle on MergePolicy %s", name, name)
	}

	// Additive: the policy's, then the binding's new entries, as written.
	cl.HoldoutChecks = union(cl.HoldoutChecks, b.HoldoutChecks)
	cl.ProtectedFiles = union(cl.ProtectedFiles, b.ProtectedFiles)
	for _, a := range b.RequiresApproval {
		if !slices.ContainsFunc(cl.RequiresApproval, func(x ApprovalLeaf) bool { return slices.Equal(x.ChangedFiles, a.ChangedFiles) }) {
			cl.RequiresApproval = append(cl.RequiresApproval, ApprovalLeaf{ChangedFiles: slices.Clone(a.ChangedFiles)})
		}
	}

	// Caps: the lower; a binding above the policy or the guardrail is refused. A policy's own cap
	// above the guardrail is clamped later by ApplyGuardrails.
	for _, f := range []struct {
		key          string
		value        *int
		policy, bind int
		guard        int
	}{
		{"maxPatchFiles", &cl.MaxPatchFiles, p.MaxPatchFiles, b.MaxPatchFiles, ctx.Guardrails.MaxPatchFiles},
		{"maxModifiedLines", &cl.MaxModifiedLines, p.MaxModifiedLines, b.MaxModifiedLines, ctx.Guardrails.MaxModifiedLines},
		{"maxPerDay", &cl.MaxPerDay, p.MaxPerDay, b.MaxPerDay, ctx.Guardrails.MaxPerDay},
	} {
		switch {
		case f.bind == 0:
		case f.policy > 0 && f.bind > f.policy:
			return refuse(ReasonOverPolicy, "%s %d is over MergePolicy %s's %d", f.key, f.bind, name, f.policy)
		case f.guard > 0 && f.bind > f.guard:
			return refuse(ReasonGuardrailMismatch, "%s %d is over the guardrail's %d", f.key, f.bind, f.guard)
		default:
			*f.value = f.bind
		}
	}

	return finish(name, cl, ctx, m)
}

// Recheck runs a class resolved earlier through the checks that read the factory and the
// repository rather than the policy (B9, B14): the Flux source, the guardrails, the class rules,
// the harness profiles, policyBot and testFirst, under the current context and merge. A last
// resolved class put back must pass them, or it would refuse its whole repository at Parse. It
// never aliases cl.
func Recheck(name string, cl Class, ctx PolicyContext, m Merge) (Class, *BindingRefusal) {
	out, reason, problems := finish(name, *cl.DeepCopy(), ctx, m)
	if reason != "" {
		return Class{}, &BindingRefusal{Reason: reason, Message: strings.Join(problems, "; ")}
	}
	return out, nil
}

// finish is resolve's last part, from the repository's facts on: what a class must pass whatever
// policy and binding made it.
func finish(name string, cl Class, ctx PolicyContext, m Merge) (Class, string, []string) {
	refuse := func(reason, f string, a ...any) (Class, string, []string) {
		return Class{}, reason, []string{fmt.Sprintf(f, a...)}
	}
	// The repository's fact: the Flux source its monitors watch.
	if mo := cl.Monitor; mo != nil && mo.Flux != nil {
		if m.FluxSource == "" {
			return refuse(ReasonMissingParameter, "monitor.flux needs Repository.spec.merge.fluxSource")
		}
		mo.Flux.Source = m.FluxSource
	}

	if over := ApplyGuardrails(&cl, ctx.Guardrails); over != "" {
		return refuse(ReasonGuardrailMismatch, "class %s: %s", name, over)
	}

	// Missing binding parameters on oracle and added-entry policies.
	if cl.Oracle != nil {
		switch {
		case cl.Oracle.Workflow == "":
			return refuse(ReasonMissingParameter, "class %s: an oracle policy needs oracleWorkflow on the binding", name)
		case len(cl.AllowedFiles) == 0:
			return refuse(ReasonMissingParameter, "class %s: an oracle policy needs allowedFiles", name)
		case len(cl.HoldoutChecks) == 0:
			return refuse(ReasonMissingParameter, "class %s: an oracle policy needs holdoutChecks", name)
		}
	}
	if cl.DiffShape == "added-entry" {
		switch {
		case len(cl.AllowedFiles) == 0:
			return refuse(ReasonMissingParameter, "class %s: added-entry needs allowedFiles", name)
		case len(cl.HoldoutChecks) == 0:
			return refuse(ReasonMissingParameter, "class %s: added-entry needs holdoutChecks", name)
		case cl.RequiredLabel == "":
			return refuse(ReasonMissingParameter, "class %s: added-entry needs requiredLabel", name)
		}
	}

	// MR6: a class the gate evaluates, live or shadow, is bounded by paths or a shape, with or
	// without policy-bot: nothing else keeps a gate path from merging.
	if (cl.Live || cl.Shadow) && name != "revert" && !cl.Bounded(name) {
		return refuse(ReasonUnbounded, "class %s: an evaluated policy needs allowedFiles or a diffShape", name)
	}

	var problems []string
	bad := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }
	validateClass(name, cl, bad)
	if len(problems) > 0 {
		return Class{}, ReasonInvalid, problems
	}
	return cl, "", nil
}

// union is a followed by b's entries a lacks, in a fresh slice.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// ApplyGuardrails caps one class under the guardrails (merge policies MP6, B14): their protected
// paths and mandatory checks are added, an unset cap inherits theirs, and a higher policy cap is
// clamped. Resolve rejects an explicit binding raise before this clamp.
func ApplyGuardrails(cl *Class, g MergeGuardrails) string {
	for _, pf := range g.ProtectedFiles {
		if !slices.Contains(cl.ProtectedFiles, pf) {
			cl.ProtectedFiles = append(cl.ProtectedFiles, pf)
		}
	}
	for _, n := range g.MandatoryChecks {
		if !slices.Contains(cl.HoldoutChecks, n) {
			cl.HoldoutChecks = append(cl.HoldoutChecks, n)
		}
	}
	for _, f := range []struct {
		value *int
		limit int
	}{{&cl.MaxPatchFiles, g.MaxPatchFiles}, {&cl.MaxModifiedLines, g.MaxModifiedLines}, {&cl.MaxPerDay, g.MaxPerDay}} {
		switch {
		case f.limit == 0:
		case *f.value == 0 || *f.value > f.limit:
			*f.value = f.limit
		}
	}
	return ""
}
