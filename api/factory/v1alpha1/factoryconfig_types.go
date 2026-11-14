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

	ReasonNotThisFactory = "NotThisFactory" // another factory's object (D15)
	ReasonDefinedTwice   = "DefinedTwice"   // the repository is also in the file (D8)
	ReasonMissingRef     = "MissingRef"     // a reference names no object
	ReasonInvalid        = "Invalid"        // the factory's own validation refused it

	// A binding that does not resolve (reusable merge policies B6, B9): config's reasons.
	ReasonMissingParameter  = config.ReasonMissingParameter
	ReasonParameterTwice    = config.ReasonParameterTwice
	ReasonOverPolicy        = config.ReasonOverPolicy
	ReasonGuardrailMismatch = config.ReasonGuardrailMismatch
	ReasonAmbiguousAdoption = config.ReasonAmbiguousAdoption

	// ConditionBindingsValid is false on a Repository while any of its bindings is refused (B10).
	ConditionBindingsValid = "BindingsValid"

	// ConditionPrivateCode is false on a served Repository whose code is private, internal or of
	// unread visibility while spec.privateCode.acknowledged is unset (v4 D3): a warning in this
	// release, a refusal from the next (step 7 R1).
	ConditionPrivateCode = "PrivateCode"
	ReasonUnacknowledged = "Unacknowledged"
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

// RepositorySpec is one repository served (D3): today's repositories[] entry, with its merge
// settings and the MergePolicies it binds (reusable merge policies B2, B3).
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
	// PrivateCode states that a private or internal repository's code is sent to the configured
	// models, which are usually SaaS (v4 D3). A private repository without it is warned in this
	// release (the PrivateCode condition) and refused from the next.
	PrivateCode *config.PrivateCode `json:"privateCode,omitempty"`
}

// RepositoryStatus is what the factory made of its Repository (D11, narrowed for v1).
type RepositoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Authority is who declares the classes: central, or the repository's file (MP15).
	// +kubebuilder:validation:Enum=central;repository
	Authority string `json:"authority,omitempty"`
	// ClassCount and LiveCount are always serialized, zero included: the printer columns read them.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ClassCount int `json:"classCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	LiveCount int `json:"liveCount"`
	// Classes is one entry per binding (B10).
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	Classes []BindingStatus `json:"classes,omitempty"`
	// Merge is the repository's merge settings as resolved, the Factory's defaults applied (Q8).
	Merge *config.Merge `json:"merge,omitempty"`
	// MergeDefaulted names the merge keys taken from the Factory's mergeDefaults.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=64
	MergeDefaulted []string `json:"mergeDefaulted,omitempty"`
}

// BindingStatus is one class of a Repository (B10): the binding's mode, the policy generation it
// serves, and whether its current generation resolves.
type BindingStatus struct {
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=live;shadow;off
	Mode string `json:"mode,omitempty"`
	// PolicyGeneration is the generation of the MergePolicy the class serves.
	PolicyGeneration int64 `json:"policyGeneration,omitempty"`
	// Accepted is always serialized, false included.
	// +optional
	Accepted bool `json:"accepted"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	LastDecision   string       `json:"lastDecision,omitempty"`
	LastDecisionAt *metav1.Time `json:"lastDecisionAt,omitempty"`
}

// Repository is one repository a Factory serves (D3).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Repo,type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name=Factory,type=string,JSONPath=`.spec.factoryRef`
// +kubebuilder:printcolumn:name=Classes,type=integer,JSONPath=`.status.classCount`
// +kubebuilder:printcolumn:name=Live,type=integer,JSONPath=`.status.liveCount`
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

// MergePolicySpec is one reusable class definition, named by the object, that Repositories bind
// (reusable merge policies B1). The per-repository shape (repositoryRef, classes, merge) was
// removed in the release after N (B8): the API server prunes those fields on read.
//
// +kubebuilder:validation:XValidation:rule="!has(self.live) && !has(self.shadow)",message="mode is the binding's: live and shadow are refused on a MergePolicy (B4)"
// +kubebuilder:validation:XValidation:rule="!has(self.oracle) || !has(self.oracle.workflow)",message="oracle.workflow is the repository's: set oracleWorkflow on the binding"
// +kubebuilder:validation:XValidation:rule="!has(self.monitor) || !has(self.monitor.flux) || !has(self.monitor.flux.source)",message="the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)"
type MergePolicySpec struct {
	// PolicySpec is the class definition: config.Class less the mode, plus requires.
	config.PolicySpec `json:",inline"`
}

// MergePolicyStatus is what the factory made of its MergePolicy (B10): Accepted, and its consumers.
type MergePolicyStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ConsumerCount, CurrentCount and LiveCount are always serialized, zero included: the printer
	// columns read them. CurrentCount counts the accepted consumers serving the current generation,
	// LiveCount the served consumers that bind the policy live.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ConsumerCount int `json:"consumerCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	CurrentCount int `json:"currentCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	LiveCount int `json:"liveCount"`
	// Consumers are the Repositories that bind the MergePolicy (B10).
	// +listType=map
	// +listMapKey=repository
	// +kubebuilder:validation:MaxItems=64
	Consumers []ConsumerStatus `json:"consumers,omitempty"`
}

// ConsumerStatus is one Repository's binding of a MergePolicy (B10).
type ConsumerStatus struct {
	// Repository is the Repository object's name.
	// +kubebuilder:validation:MaxLength=253
	Repository string `json:"repository"`
	// +kubebuilder:validation:Enum=live;shadow;off
	Mode             string `json:"mode,omitempty"`
	ServedGeneration int64  `json:"servedGeneration,omitempty"`
	// Accepted is always serialized, false included.
	// +optional
	Accepted bool `json:"accepted"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
}

// MergePolicy is one reusable class definition, its name the class name (reusable merge policies
// B1).
//
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$')",message="a MergePolicy's name is its class name (B1)"
// +kubebuilder:validation:XValidation:rule="self.metadata.name != 'review' || !(has(self.spec.live) || has(self.spec.shadow) || has(self.spec.allowedFiles) || has(self.spec.protectedFiles) || has(self.spec.requiresApproval) || (has(self.spec.modifyOnly) && self.spec.modifyOnly) || has(self.spec.maxPatchFiles) || has(self.spec.maxModifiedLines) || has(self.spec.diffShape) || has(self.spec.holdoutChecks) || has(self.spec.maxPerDay) || has(self.spec.oracle) || has(self.spec.monitor) || has(self.spec.author) || has(self.spec.releaseAgeCheck) || has(self.spec.companionFiles) || has(self.spec.requiredLabel) || has(self.spec.requires))",message="review takes only description, harness, harnessAllowed, escalation, checks and testFirst"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Consumers,type=integer,JSONPath=`.status.consumerCount`
// +kubebuilder:printcolumn:name=Current,type=integer,JSONPath=`.status.currentCount`
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
