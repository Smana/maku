// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// The modes a binding puts its class in (B4): live merges, shadow is decided and never armed, off
// is neither (Q7).
const (
	ModeLive   = "live"
	ModeShadow = "shadow"
	ModeOff    = "off"
)

// The reasons a binding does not resolve (B9). MissingRef, the reason for a binding to an absent
// policy, is the resources layer's: Resolve is handed a policy.
const (
	ReasonMissingParameter  = "MissingParameter"
	ReasonParameterTwice    = "ParameterTwice"
	ReasonOverPolicy        = "OverPolicy"
	ReasonGuardrailMismatch = "GuardrailMismatch"
	ReasonAmbiguousAdoption = "AmbiguousAdoption"
	ReasonInvalid           = "Invalid"
)

// Requirable are the fields a policy's requires may name (B4, plan PR3): the four fill-once
// parameters, and holdoutChecks, which a binding then adds to.
func Requirable() []string {
	return []string{"allowedFiles", "companionFiles", "requiredLabel", "oracleWorkflow", "holdoutChecks"}
}

// Binding is one repository's use of a MergePolicy (B3, B4): the policy's name, which is the class
// name (B1), the mode, and the parameters this repository supplies. A binding tightens or fills;
// it never widens what the policy says.
// +kubebuilder:object:generate=true
type Binding struct {
	// Name is the MergePolicy's, so the class's (B1).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=live;shadow;off
	Mode string `json:"mode"`
	// AllowedFiles, CompanionFiles, RequiredLabel and OracleWorkflow are fill-once: set here or on
	// the policy, never both.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
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

// PolicySpec is a MergePolicy's class definition (B7, D16): a Class, less the mode, which is the
// binding's, plus the parameters every binding must supply.
// +kubebuilder:object:generate=true
type PolicySpec struct {
	Class `json:",inline"`
	// Requires names the parameters each binding must set: of Requirable.
	// +listType=set
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:Enum=allowedFiles;companionFiles;requiredLabel;oracleWorkflow;holdoutChecks
	Requires []string `json:"requires,omitempty"`
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
// with a placeholder in each field it requires, in mode off.
func ValidatePolicy(name string, p PolicySpec, ctx PolicyContext) []string {
	if problems := policyShapeProblems(name, p); len(problems) > 0 {
		return problems
	}
	b := Binding{Name: name, Mode: ModeOff}
	for _, f := range p.Requires {
		switch f {
		case "allowedFiles":
			b.AllowedFiles = []string{"^placeholder$"}
		case "companionFiles":
			b.CompanionFiles = []string{"^placeholder$"}
		case "requiredLabel":
			b.RequiredLabel = "placeholder"
		case "oracleWorkflow":
			b.OracleWorkflow = "placeholder.yaml"
		case "holdoutChecks":
			b.HoldoutChecks = []string{"placeholder"}
		}
	}
	ctx.Guardrails = MergeGuardrails{} // valid alone: a binding may lower a cap under a guardrail
	_, _, problems := resolve(name, p, b, ctx, Merge{FluxSource: "ns/name", ContractCheck: "placeholder"})
	return problems
}

// policyShapeProblems is what a policy may not say whatever binds it: the mode, the repository's
// parameters, a requires outside Requirable, or one naming a field the policy sets.
func policyShapeProblems(name string, p PolicySpec) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf("class %s: ", name)+fmt.Sprintf(f, a...)) }
	if p.Live || p.Shadow {
		bad("mode is the binding's: live and shadow are refused on a MergePolicy (B4)")
	}
	if p.Oracle != nil && p.Oracle.Workflow != "" {
		bad("oracle.workflow is the repository's: set oracleWorkflow on the binding")
	}
	if p.Monitor != nil && p.Monitor.Flux != nil && p.Monitor.Flux.Source != "" {
		bad("the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)")
	}
	// review's CEL rule, mirrored so offline validation and admission agree (R17): review merges
	// nothing, so it takes description, harness, harnessAllowed, escalation, checks and testFirst
	// only. validateClass refuses the rest of its fields; these three it does not read.
	if name == "review" && (len(p.CompanionFiles) > 0 || p.RequiredLabel != "" || len(p.Requires) > 0) {
		bad("review takes only description, harness, harnessAllowed, escalation, checks and testFirst")
	}
	set := map[string]bool{"allowedFiles": len(p.AllowedFiles) > 0, "companionFiles": len(p.CompanionFiles) > 0, "requiredLabel": p.RequiredLabel != ""}
	for i, f := range p.Requires {
		switch {
		case !slices.Contains(Requirable(), f):
			bad("requires %q is not one of %v", f, Requirable())
		case slices.Index(p.Requires, f) != i:
			bad("requires names %q twice", f)
		case set[f]:
			bad("requires %s, which it sets: a parameter is set in one place (B4)", f)
		}
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
	case b.Mode != ModeLive && b.Mode != ModeShadow && b.Mode != ModeOff:
		return refuse(ReasonInvalid, "class %s: mode %q is live, shadow or off", name, b.Mode)
	case name == "review" && b.Mode != ModeOff:
		return refuse(ReasonInvalid, "class review: review takes only mode off")
	case name == "review" && !reflect.DeepEqual(b, Binding{Name: b.Name, Mode: b.Mode}):
		return refuse(ReasonInvalid, "class review: review is bound with mode off and no parameter")
	}
	if problems := policyShapeProblems(name, p); len(problems) > 0 {
		return Class{}, ReasonInvalid, problems
	}
	cl := *p.Class.DeepCopy()
	cl.Live, cl.Shadow = b.Mode == ModeLive, b.Mode == ModeShadow

	// Fill once: in exactly one place, and in the binding when the policy requires it.
	for _, f := range []struct {
		key          string
		policy, bind bool
		fill         func()
	}{
		{"allowedFiles", len(p.AllowedFiles) > 0, len(b.AllowedFiles) > 0, func() { cl.AllowedFiles = slices.Clone(b.AllowedFiles) }},
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
		case !f.policy && slices.Contains(p.Requires, f.key):
			return refuse(ReasonMissingParameter, "MergePolicy %s requires %s", name, f.key)
		}
	}
	if b.OracleWorkflow != "" && p.Oracle == nil {
		return refuse(ReasonInvalid, "class %s: oracleWorkflow needs an oracle on MergePolicy %s", name, name)
	}

	// Additive: the policy's, then the binding's new entries, as written. A required holdoutChecks
	// is met only by a check the policy does not already name (PR3, A-L2).
	cl.HoldoutChecks = union(cl.HoldoutChecks, b.HoldoutChecks)
	if slices.Contains(p.Requires, "holdoutChecks") && len(cl.HoldoutChecks) == len(p.HoldoutChecks) {
		return refuse(ReasonMissingParameter, "MergePolicy %s requires holdoutChecks: the binding adds none of its own", name)
	}
	cl.ProtectedFiles = union(cl.ProtectedFiles, b.ProtectedFiles)
	for _, a := range b.RequiresApproval {
		if !slices.ContainsFunc(cl.RequiresApproval, func(x ApprovalLeaf) bool { return slices.Equal(x.ChangedFiles, a.ChangedFiles) }) {
			cl.RequiresApproval = append(cl.RequiresApproval, ApprovalLeaf{ChangedFiles: slices.Clone(a.ChangedFiles)})
		}
	}

	// Caps: the lower; a binding above the policy is refused.
	for _, f := range []struct {
		key    string
		value  *int
		policy int
		bind   int
	}{
		{"maxPatchFiles", &cl.MaxPatchFiles, p.MaxPatchFiles, b.MaxPatchFiles},
		{"maxModifiedLines", &cl.MaxModifiedLines, p.MaxModifiedLines, b.MaxModifiedLines},
		{"maxPerDay", &cl.MaxPerDay, p.MaxPerDay, b.MaxPerDay},
	} {
		switch {
		case f.bind == 0:
		case f.policy > 0 && f.bind > f.policy:
			return refuse(ReasonOverPolicy, "%s %d is over MergePolicy %s's %d", f.key, f.bind, name, f.policy)
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

	var problems []string
	bad := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }
	validateClass(name, cl, bad)
	floor := &Config{HarnessProfiles: ctx.HarnessProfiles, Templates: ctx.Templates, Loops: ctx.Loops}
	for _, v := range floor.VariantProblems(name, cl) {
		bad("class %s: %s", name, v)
	}
	// validateRepository's rule on a live class, here so it drops the binding, not the repository.
	if !m.UsesPolicyBot() && cl.Live && len(cl.AllowedFiles) == 0 {
		bad("class %s: merge.policyBot: false needs allowedFiles on every live class: without policy-bot, nothing else keeps a gate path from merging", name)
	}
	if m.ContractCheck == "" {
		if cl.TestFirst {
			bad("class %s: testFirst needs merge.contractCheck", name)
		}
		for _, pn := range slices.Compact(slices.Sorted(slices.Values(append([]string{cl.Harness}, cl.HarnessAllowed...)))) {
			if ctx.HarnessProfiles[pn].TestFirst {
				bad("class %s: profile %q: testFirst needs merge.contractCheck", name, pn)
			}
		}
	}
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
// paths and mandatory checks are added, an unset cap inherits theirs, and a cap above theirs is
// reported, as "maxPerDay 3 is over the guardrail's 2". Empty when the class is within them.
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
		key   string
		value *int
		limit int
	}{{"maxPatchFiles", &cl.MaxPatchFiles, g.MaxPatchFiles}, {"maxModifiedLines", &cl.MaxModifiedLines, g.MaxModifiedLines}, {"maxPerDay", &cl.MaxPerDay, g.MaxPerDay}} {
		switch {
		case f.limit == 0:
		case *f.value == 0:
			*f.value = f.limit
		case *f.value > f.limit:
			return fmt.Sprintf("%s %d is over the guardrail's %d", f.key, *f.value, f.limit)
		}
	}
	return ""
}
