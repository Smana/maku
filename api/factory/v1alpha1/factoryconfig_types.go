// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/internal/factory/config"
)

// The condition every factory resource carries, and the reasons it is false (factory resources
// D5, D8, D15).
const (
	ConditionAccepted = "Accepted"

	ReasonNotThisFactory  = "NotThisFactory"  // another factory's object (D15)
	ReasonDefinedTwice    = "DefinedTwice"    // the repository is also in the file (D8)
	ReasonMissingRef      = "MissingRef"      // a reference names no object
	ReasonNoMergePolicy   = "NoMergePolicy"   // a Repository no MergePolicy names
	ReasonDuplicatePolicy = "DuplicatePolicy" // two MergePolicies name one Repository
	ReasonInvalid         = "Invalid"         // the factory's own validation refused it

	// A binding that does not resolve (reusable merge policies B6, B9): config's reasons.
	ReasonMissingParameter  = config.ReasonMissingParameter
	ReasonParameterTwice    = config.ReasonParameterTwice
	ReasonOverPolicy        = config.ReasonOverPolicy
	ReasonGuardrailMismatch = config.ReasonGuardrailMismatch
	ReasonAmbiguousAdoption = config.ReasonAmbiguousAdoption

	// ConditionBindingsValid is false on a Repository while any of its bindings is refused (B10).
	ConditionBindingsValid = "BindingsValid"
)

// FactoryTriage is the live part of triage (D12): the classifier's URL stays in the values.
type FactoryTriage struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ControlPercent int `json:"controlPercent,omitempty"`
}

// FactorySpec is the factory's catalogue (D3, D12), in today's config keys. prices is today's
// meter.prices: the meter's URL and queries stay in the values.
type FactorySpec struct {
	Control  config.Control  `json:"control,omitzero"`
	Defaults config.Defaults `json:"defaults,omitzero"`
	Triage   FactoryTriage   `json:"triage,omitzero"`
	// +kubebuilder:validation:MaxProperties=16
	Tiers map[string]config.Tier `json:"tiers,omitempty"`
	// +kubebuilder:validation:MaxProperties=16
	Templates map[string]config.Template `json:"templates,omitempty"`
	Caps      config.Caps                `json:"caps,omitzero"`
	Budgets   config.Budgets             `json:"budgets,omitzero"`
	Facts     config.Facts               `json:"facts,omitzero"`
	Brief     config.Brief               `json:"brief,omitzero"`
	Resume    config.Resume              `json:"resume,omitzero"`
	Scoper    config.Scoper              `json:"scoper,omitzero"`
	// +kubebuilder:validation:MaxProperties=32
	Loops map[string]config.Loop `json:"loops,omitempty"`
	// +kubebuilder:validation:MaxProperties=16
	HarnessProfiles map[string]config.HarnessProfile `json:"harnessProfiles,omitempty"`
	MergeGuardrails config.MergeGuardrails           `json:"mergeGuardrails,omitzero"`
	// Evaluation is the team evaluation's cohort share and mode, read per decision.
	Evaluation config.Evaluation `json:"evaluation,omitzero"`
	// MergeDefaults are the merge settings most repositories share (Q8): a Repository's merge
	// overrides them key by key, each key whole.
	MergeDefaults *config.Merge `json:"mergeDefaults,omitempty"`
	// +kubebuilder:validation:MaxProperties=32
	Prices map[string]config.Price `json:"prices,omitempty"`
}

// FactoryStatus is what the factory made of its Factory (D11, narrowed for v1).
type FactoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Always serialized, zero included: the printer columns read them.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Repositories int `json:"repositories"`
	// +optional
	Stopped bool `json:"stopped"`
}

// Factory is one factory's catalogue (D3). Its name is config.factory, the fence's (D15).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Ready,type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name=Repos,type=integer,JSONPath=`.status.repositories`
// +kubebuilder:printcolumn:name=Stopped,type=boolean,JSONPath=`.status.stopped`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type Factory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FactorySpec   `json:"spec,omitempty"`
	Status            FactoryStatus `json:"status,omitempty"`
}

// FactoryList is a list of Factory.
//
// +kubebuilder:object:root=true
type FactoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Factory `json:"items"`
}

// RepositorySpec is one repository served (D3): today's repositories[] entry. Its merge settings
// and the MergePolicies it binds are here (reusable merge policies B2, B3), or, in the
// per-repository shape, in the MergePolicy that names it.
//
// +kubebuilder:validation:XValidation:rule="!has(self.mergePolicies) || !self.mergePolicies.exists(b, b.name == 'review' && (b.mode != 'off' || has(b.allowedFiles) || has(b.companionFiles) || has(b.requiredLabel) || has(b.oracleWorkflow) || has(b.holdoutChecks) || has(b.protectedFiles) || has(b.requiresApproval) || has(b.maxPatchFiles) || has(b.maxModifiedLines) || has(b.maxPerDay)))",message="review is bound with mode off and no parameter"
// +kubebuilder:validation:XValidation:rule="!has(self.mergePolicies) || size(self.mergePolicies) == 0 || (self.mergePolicies.exists(b, b.name == 'docs-links') && self.mergePolicies.exists(b, b.name == 'revert'))",message="bind docs-links and revert when any policy is bound (OD-8)"
// +kubebuilder:validation:XValidation:rule="!has(self.classAuthority) || self.classAuthority != 'repository' || !has(self.mergePolicies)",message="classAuthority repository: the repository's file holds the bindings (MP15)"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.factoryRef) || self.factoryRef == oldSelf.factoryRef",message="spec.factoryRef is fixed once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repository) || self.repository == oldSelf.repository",message="spec.repository is fixed once set"
type RepositorySpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	FactoryRef string `json:"factoryRef"`
	// Repository is owner/name on GitHub.
	// +kubebuilder:validation:MaxLength=140
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	Repository string `json:"repository"`
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=39
	Maintainers []string `json:"maintainers,omitempty"`
	// +kubebuilder:validation:MaxLength=50
	TriggerLabel string        `json:"triggerLabel,omitempty"`
	Checks       config.Checks `json:"checks,omitzero"`
	// +kubebuilder:validation:MaxItems=16
	Schedules []config.Schedule `json:"schedules,omitempty"`
	// +kubebuilder:validation:Enum=central;repository
	ClassAuthority string `json:"classAuthority,omitempty"`
	// Merge is the repository's merge settings (B2): config.Merge, plus fluxSource.
	Merge *config.Merge `json:"merge,omitempty"`
	// MergePolicies binds the class-shape MergePolicies this repository uses (B3), one per class:
	// the policy's name, the mode, and this repository's parameters.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	MergePolicies []config.Binding `json:"mergePolicies,omitempty"`
}

// RepositoryStatus is what the factory made of its Repository (D11, narrowed for v1).
type RepositoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// MergePolicy names the MergePolicy that applies.
	// +kubebuilder:validation:MaxLength=253
	MergePolicy string `json:"mergePolicy,omitempty"`
}

// Repository is one repository a Factory serves (D3).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Repo,type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name=Factory,type=string,JSONPath=`.spec.factoryRef`
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Connected,type=string,JSONPath=`.status.conditions[?(@.type=="Connected")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type Repository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RepositorySpec   `json:"spec"`
	Status            RepositoryStatus `json:"status,omitempty"`
}

// RepositoryList is a list of Repository.
//
// +kubebuilder:object:root=true
type RepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Repository `json:"items"`
}

// MergePolicySpec is a MergePolicy in one of two shapes (reusable merge policies B1, B8). With
// repositoryRef, the per-repository shape: one repository's classes and merge rules (D3, D4), today's
// repositories[].classes and merge, removed in the next minor. Without it, the class shape: one
// reusable class definition, named by the object, that Repositories bind.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repositoryRef) || self.repositoryRef == oldSelf.repositoryRef",message="spec.repositoryRef is fixed once set"
// +kubebuilder:validation:XValidation:rule="has(self.repositoryRef) == has(oldSelf.repositoryRef)",message="a MergePolicy keeps its shape: delete it and create a policy"
// +kubebuilder:validation:XValidation:rule="!has(self.repositoryRef) || !(has(self.description) || has(self.live) || has(self.shadow) || has(self.checks) || has(self.testFirst) || has(self.allowedFiles) || has(self.protectedFiles) || has(self.requiresApproval) || has(self.modifyOnly) || has(self.maxPatchFiles) || has(self.maxModifiedLines) || has(self.diffShape) || has(self.holdoutChecks) || has(self.maxPerDay) || has(self.oracle) || has(self.monitor) || has(self.author) || has(self.releaseAgeCheck) || has(self.companionFiles) || has(self.requiredLabel) || has(self.harness) || has(self.harnessAllowed) || has(self.escalation) || has(self.requires))",message="the per-repository fields (repositoryRef, classes, merge) and the class fields never together (B8)"
// +kubebuilder:validation:XValidation:rule="has(self.repositoryRef) || (!has(self.classes) && !has(self.merge))",message="classes and merge are the per-repository shape: they need repositoryRef (B8)"
// +kubebuilder:validation:XValidation:rule="has(self.repositoryRef) || (!has(self.live) && !has(self.shadow))",message="mode is the binding's: live and shadow are refused on a MergePolicy (B4)"
// +kubebuilder:validation:XValidation:rule="!has(self.oracle) || !has(self.oracle.workflow)",message="oracle.workflow is the repository's: set oracleWorkflow on the binding"
// +kubebuilder:validation:XValidation:rule="!has(self.monitor) || !has(self.monitor.flux) || !has(self.monitor.flux.source)",message="the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)"
type MergePolicySpec struct {
	// RepositoryRef names the Repository of a per-repository MergePolicy; absent, the object is
	// a class definition.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	RepositoryRef string `json:"repositoryRef,omitempty"`
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, !((has(self[k].live) && self[k].live) && (has(self[k].shadow) && self[k].shadow)))",message="a class is live or shadow, never both"
	Classes map[string]config.Class `json:"classes,omitempty"`
	Merge   *config.Merge           `json:"merge,omitempty"`
	// PolicySpec is the class shape's definition: config.Class less the mode, plus requires.
	config.PolicySpec `json:",inline"`
}

// ClassStatus is one class's mode and last decision (D10, narrowed for v1).
type ClassStatus struct {
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=live;shadow;unarmed
	Mode string `json:"mode"`
	// +kubebuilder:validation:MaxLength=63
	LastDecision   string       `json:"lastDecision,omitempty"`
	LastDecisionAt *metav1.Time `json:"lastDecisionAt,omitempty"`
}

// MergePolicyStatus is what the factory made of its MergePolicy and of the repository's file (D10).
type MergePolicyStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Enum=central;repository
	Authority string `json:"authority,omitempty"`
	// ClassCount and LiveCount are always serialized, zero included: the printer columns read them.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ClassCount int `json:"classCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	LiveCount int `json:"liveCount"`
	// +kubebuilder:validation:MaxItems=32
	Classes []ClassStatus `json:"classes,omitempty"`
}

// MergePolicy is one reusable class definition, its name the class name (reusable merge policies
// B1), or, in the per-repository shape until the next minor, one repository's merge policy (D3, D4).
//
// +kubebuilder:validation:XValidation:rule="has(self.spec.repositoryRef) || self.metadata.name.matches('^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$')",message="a MergePolicy's name is its class name (B1)"
// +kubebuilder:validation:XValidation:rule="has(self.spec.repositoryRef) || self.metadata.name != 'review' || !(has(self.spec.live) || has(self.spec.shadow) || (has(self.spec.checks) && self.spec.checks) || (has(self.spec.testFirst) && self.spec.testFirst) || has(self.spec.allowedFiles) || has(self.spec.protectedFiles) || has(self.spec.requiresApproval) || (has(self.spec.modifyOnly) && self.spec.modifyOnly) || has(self.spec.maxPatchFiles) || has(self.spec.maxModifiedLines) || has(self.spec.diffShape) || has(self.spec.holdoutChecks) || has(self.spec.maxPerDay) || has(self.spec.oracle) || has(self.spec.monitor) || has(self.spec.author) || has(self.spec.releaseAgeCheck) || has(self.spec.companionFiles) || has(self.spec.requiredLabel) || has(self.spec.requires))",message="review takes only description, harness, harnessAllowed and escalation"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Repo,type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name=Authority,type=string,JSONPath=`.status.authority`
// +kubebuilder:printcolumn:name=Classes,type=integer,JSONPath=`.status.classCount`
// +kubebuilder:printcolumn:name=Live,type=integer,JSONPath=`.status.liveCount`
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type MergePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MergePolicySpec   `json:"spec"`
	Status            MergePolicyStatus `json:"status,omitempty"`
}

// MergePolicyList is a list of MergePolicy.
//
// +kubebuilder:object:root=true
type MergePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MergePolicy `json:"items"`
}
