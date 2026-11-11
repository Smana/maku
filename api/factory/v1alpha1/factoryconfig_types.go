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

// RepositorySpec is one repository served (D3): today's repositories[] entry less its classes
// and merge block, which are its MergePolicy's.
//
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

// MergePolicySpec is one repository's classes and merge rules (D3, D4): today's
// repositories[].classes and merge, unchanged in shape.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repositoryRef) || self.repositoryRef == oldSelf.repositoryRef",message="spec.repositoryRef is fixed once set"
type MergePolicySpec struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	RepositoryRef string `json:"repositoryRef"`
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, !((has(self[k].live) && self[k].live) && (has(self[k].shadow) && self[k].shadow)))",message="a class is live or shadow, never both"
	Classes map[string]config.Class `json:"classes,omitempty"`
	Merge   config.Merge            `json:"merge,omitzero"`
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

// MergePolicy is one repository's merge policy (D3, D4).
//
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
