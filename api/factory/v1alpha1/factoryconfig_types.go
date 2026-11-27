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
	ReasonOverRoutine       = config.ReasonOverRoutine

	// ConditionBindingsValid is false on a Repository while any of its bindings is refused (B10).
	ConditionBindingsValid = "BindingsValid"
	// ConditionRoutinesValid is false on a Repository while any of its routine bindings is refused.
	ConditionRoutinesValid = "RoutinesValid"

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

// GitOps is one GitHub repository and manifest file where policy-change pull requests are opened.
type GitOps struct {
	// Repository is owner/name on GitHub.
	// +kubebuilder:validation:MaxLength=140
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	Repository string `json:"repository"`
	// Path is the manifest's slash-separated path in that repository.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[^/].*[^/]$|^[^/]$`
	Path string `json:"path"`
	// Branch is the base branch for policy changes, main when omitted.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._/-]+$`
	// +optional
	Branch string `json:"branch,omitempty"`
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
	// Workflows are the factory's workflows (WF1, WF2, WR11): quick, standard, careful and none are
	// shipped, and an entry here replaces the shipped one of its name whole. harnessProfiles, its
	// old name, was removed (WF9, WF13).
	// +kubebuilder:validation:MaxProperties=16
	Workflows map[string]config.HarnessProfile `json:"workflows,omitempty"`
	// Roles are custom roles declared by the platform (WR11), each bound to a closed capability
	// profile and a brief file under the factory's configuration directory.
	// +kubebuilder:validation:MaxProperties=16
	Roles map[string]config.RoleDef `json:"roles,omitempty"`
	// Outcomes are the task outcomes this factory offers (phase F): change by default.
	// An outcome not listed here falls back to change.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=change;plan-first;review;report
	// +kubebuilder:validation:items:MaxLength=16
	// +optional
	Outcomes        []string               `json:"outcomes,omitempty"`
	MergeGuardrails config.MergeGuardrails `json:"mergeGuardrails,omitzero"`
	// Evaluation is the team evaluation's cohort share and mode, read per decision.
	Evaluation config.Evaluation `json:"evaluation,omitzero"`
	// MergeDefaults are the merge settings most repositories share (Q8): a Repository's merge
	// overrides them key by key, each key whole.
	MergeDefaults *config.Merge `json:"mergeDefaults,omitempty"`
	// GitOps is the repository and manifest path where the factory opens policy-change pull requests.
	// The factory never merges those pull requests.
	// +optional
	GitOps *GitOps `json:"gitops,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="!has(self.mergePolicies) || !self.mergePolicies.exists(b, b.name == 'review')",message="review is not a policy"
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
	// Routines binds reusable Routine objects this repository runs on a schedule.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Routines []config.RoutineBinding `json:"routines,omitempty"`
	// Workflow is the workflow a task of the repository runs when no label and no source names one,
	// standard by default, and whether its careful tasks start with the tester (WF3, WF7).
	Workflow config.RepositoryWorkflow `json:"workflow,omitzero"`
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
	// Screenshots gives the repository's implementer and tester runs a loopback-only browser (v5
	// Q16); off by default. Only a maintainer sets it, here: never an issue, a label or a run request.
	// +optional
	Screenshots *config.Screenshots `json:"screenshots,omitempty"`
	// Access is who may start a task: maintainers (the default), or write, which lets anyone with
	// write access start one where the factory enforces per-person daily caps.
	// +optional
	Access config.RepositoryAccess `json:"access,omitzero"`
	// Runtime is the image and size of the repository's implementer, tester and reviewer runs. The
	// image is built from the harness image, pinned by digest, and allowed by the run-controller's
	// runtime.allowedImages; the network stays the named profiles of checks.egress.
	// +optional
	Runtime config.Runtime `json:"runtime,omitzero"`
}

// RepositoryStatus is what the factory made of its Repository (D11, narrowed for v1).
type RepositoryStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=10
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
	// Routines is one entry per routine binding.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Routines []RoutineBindingStatus `json:"routines,omitempty"`
	// Merge is the repository's merge settings as resolved, the Factory's defaults applied (Q8).
	Merge *config.Merge `json:"merge,omitempty"`
	// MergeDefaulted names the merge keys taken from the Factory's mergeDefaults.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=64
	MergeDefaulted []string `json:"mergeDefaulted,omitempty"`
}

// BindingStatus is one binding: its written mode, the mode actually served, the policy generation,
// and whether its current generation resolves.
type BindingStatus struct {
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=live;shadow;disabled
	Mode string `json:"mode,omitempty"`
	// ServedMode is the resolved class's actual mode: live, shadow, or disabled when nothing serves.
	// +kubebuilder:validation:Enum=live;shadow;disabled
	ServedMode string `json:"servedMode,omitempty"`
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
// +kubebuilder:printcolumn:name=Policies,type=integer,JSONPath=`.status.classCount`
// +kubebuilder:printcolumn:name=Live,type=integer,JSONPath=`.status.liveCount`
// +kubebuilder:printcolumn:name=BindingsValid,type=string,JSONPath=`.status.conditions[?(@.type=="BindingsValid")].status`
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

// MergePolicySpec is one reusable policy definition, named by the object, that Repositories bind.
//
// +kubebuilder:validation:XValidation:rule="!has(self.oracle) || !has(self.oracle.workflow)",message="oracle.workflow is the repository's: set oracleWorkflow on the binding"
// +kubebuilder:validation:XValidation:rule="!has(self.monitor) || !has(self.monitor.flux) || !has(self.monitor.flux.source)",message="the Flux source is the repository's: Repository.spec.merge.fluxSource"
// +kubebuilder:validation:XValidation:rule="!has(self.narrowedFiles)",message="narrowedFiles is set by a binding's allowedFiles: set allowedFiles on the policy"
// +kubebuilder:validation:XValidation:rule="self.evidence != 'low' || has(self.diffShape) || has(self.oracle)",message="evidence: low needs diffShape or oracle: set one, or use evidence: standard"
type MergePolicySpec struct {
	// PolicySpec is the class definition: config.MergeClass.
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
	// +kubebuilder:validation:Enum=live;shadow;disabled
	Mode             string `json:"mode,omitempty"`
	ServedGeneration int64  `json:"servedGeneration,omitempty"`
	// Accepted is always serialized, false included.
	// +optional
	Accepted bool `json:"accepted"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
}

// LabelCatalog marks a MergePolicy of the agent-factory chart's catalog (merge policies review MR2):
// every Repository the factory serves evaluates it in shadow unless it binds it itself.
const LabelCatalog = "maku.ogenki.io/catalog"

// MergePolicy is one reusable class definition, its name the class name (reusable merge policies
// B1).
//
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$')",message="a MergePolicy's name is its class name (B1)"
// +kubebuilder:validation:XValidation:rule="self.metadata.name != 'review'",message="review is not a policy"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Consumers,type=integer,JSONPath=`.status.consumerCount`
// +kubebuilder:printcolumn:name=Current,type=integer,JSONPath=`.status.currentCount`
// +kubebuilder:printcolumn:name=Live,type=integer,JSONPath=`.status.liveCount`
// +kubebuilder:printcolumn:name=Description,type=string,JSONPath=`.spec.description`
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
