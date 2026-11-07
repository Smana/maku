// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Multi-repository coordination (v4 design MR1–MR17) is additive data on Task, never a new CRD
// (MR2). A parent Task has spec.kind coordination and source.kind coordination; each child is an
// ordinary issue Task with spec.coordinationParent; each integration attempt has spec.kind
// integration and runs only triager (MR10, MR17). None reuses the splitter's spec.parent,
// spec.split, status.split or agents.ogenki.io/parent: the two lineages never collide.

// The Task kinds coordination adds (MR2); an absent spec.kind is an ordinary work Task.
const (
	TaskKindCoordination = "coordination"
	TaskKindIntegration  = "integration"
)

// SourceKindCoordination is a parent's and an integration Task's source kind, never api, so the
// API-sponsored semantics are never borrowed by accident (MR2).
const SourceKindCoordination = "coordination"

// CoordinationPlan's own apiVersion and kind: plan data, not a registered Kind (MR2, MR12).
const (
	CoordinationPlanAPIVersion = "maku.ogenki.io/v1alpha1"
	CoordinationPlanKind       = "CoordinationPlan"
)

// The retention caps of a parent's bounded records: a writer appends with AppendNewest, so the
// oldest goes first and etcd's object stays bounded.
const (
	MaxCoordinationHistory             = 16
	MaxCoordinationConfirmations       = 16
	MaxCoordinationAuthorizations      = 16
	MaxCoordinationIntegrationAttempts = 16
	MaxCoordinationIncidents           = 4
	MaxPendingGenerations              = 2
)

// AppendNewest appends v and keeps the newest max records, dropping the oldest first.
func AppendNewest[T any](s []T, v T, maxLen int) []T {
	s = append(s, v)
	if maxLen <= 0 {
		return s[:0]
	}
	if over := len(s) - maxLen; over > 0 {
		s = append(s[:0:0], s[over:]...)
	}
	return s
}

// CoordinationPlan is the full, versioned plan snapshot every interface exchanges (MR4, MR12).
// Validation, canonical hashing and materiality live in internal/factory/coordination; the CRD
// holds only the bounds and the invariants every mode shares.
// +kubebuilder:validation:XValidation:rule="self.children.exists(c, c.repository == self.primary.repository)",message="primary.repository is one child repository (MR3)"
// +kubebuilder:validation:XValidation:rule="self.children.all(a, self.children.exists_one(b, b.id == a.id))",message="a child id is unique"
// +kubebuilder:validation:XValidation:rule="self.children.all(a, self.children.exists_one(b, b.repository == a.repository))",message="a repository holds one child (MR1)"
// +kubebuilder:validation:XValidation:rule="!has(self.mergeOrder) || self.mergeOrder.all(id, self.children.exists(c, c.id == id))",message="mergeOrder names only children"
type CoordinationPlan struct {
	// +kubebuilder:validation:Enum="maku.ogenki.io/v1alpha1"
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:Enum=CoordinationPlan
	Kind string `json:"kind"`
	// The stable draft and parent Task id, a C2 id.
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Title string `json:"title"`
	// Strictly +1 per accepted edit.
	// +kubebuilder:validation:Minimum=1
	Version int32               `json:"version"`
	Primary CoordinationPrimary `json:"primary"`
	// At most two children (MR1); a plan-mode draft may hold one.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	Children []CoordinationChild `json:"children"`
	// The child ids in merge order.
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:MaxLength=32
	// +optional
	MergeOrder  []string               `json:"mergeOrder,omitempty"`
	Integration IntegrationPlan        `json:"integration"`
	Budget      CoordinationPlanBudget `json:"budget"`
}

// CoordinationPrimary is the user-confirmed primary repository: a child's, where the parent issue
// and the integration check live (MR3). Changing it starts a new parent.
type CoordinationPrimary struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Rationale string `json:"rationale,omitempty"`
}

// CoordinationChild is one repository-local change of the plan, with its own maintainer,
// resolved class, roles and lifetime allocation (MR5, MR8).
// +kubebuilder:validation:XValidation:rule="!has(self.produces) || self.produces.all(a, self.produces.exists_one(b, b.name == a.name))",message="an artifact name is unique within its child"
type CoordinationChild struct {
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,31}$`
	// +kubebuilder:validation:MaxLength=32
	ID string `json:"id"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// The linked maintainer who authorizes this child, github:<login> (MR5).
	// +kubebuilder:validation:Pattern=`^github:[A-Za-z0-9][A-Za-z0-9-]{0,38}$`
	// +kubebuilder:validation:MaxLength=46
	Maintainer string `json:"maintainer"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Rationale string `json:"rationale,omitempty"`
	// Resolved from the repository's own allowance, never declared by the plan.
	// +kubebuilder:validation:MaxLength=64
	Class string `json:"class"`
	// +kubebuilder:validation:Enum=light;standard;frontier
	Tier string `json:"tier"`
	// Derived from the class's harness, in its order.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=implementer;reviewer;tester;triager
	Roles []string `json:"roles"`
	// The lifetime allocation across every generation and retry of the child (MR8).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	BudgetMicroUSD int64     `json:"budgetMicroUSD"`
	Scope          TaskScope `json:"scope"`
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Produces []ArtifactSpec `json:"produces,omitempty"`
	// The child ids whose landing orders this child's admission (MR7).
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=32
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Consumes []ArtifactConsumer `json:"consumes,omitempty"`
}

// ArtifactSpec is a file a child produces, consumable only at its landed merge commit (MR7).
type ArtifactSpec struct {
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,63}$`
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`
	// The only initial artifact kind.
	// +kubebuilder:validation:Enum=file
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	MediaType string `json:"mediaType,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	MaxBytes int64 `json:"maxBytes"`
}

// ArtifactConsumer names a producer child's artifact this child requires (MR7).
type ArtifactConsumer struct {
	// +kubebuilder:validation:MaxLength=32
	From string `json:"from"`
	// +kubebuilder:validation:MaxLength=64
	Artifact string `json:"artifact"`
	Required bool   `json:"required"`
}

// IntegrationPlan is the family's read-only acceptance, run by triager in the primary
// repository (MR10, MR17).
// +kubebuilder:validation:XValidation:rule="!has(self.beforeDependents) || self.beforeDependents || (has(self.beforeDependentsRationale) && size(self.beforeDependentsRationale) > 0)",message="beforeDependents: false needs a rationale"
type IntegrationPlan struct {
	// +kubebuilder:validation:Enum=triager
	Role string `json:"role"`
	// Nil is true: a dependent merge waits for a passing integration (MR9). Read it through
	// EffectiveBeforeDependents.
	// +optional
	BeforeDependents *bool `json:"beforeDependents,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	BeforeDependentsRationale string `json:"beforeDependentsRationale,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	RunMicroUSD int64 `json:"runMicroUSD"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	MaxRuns int32 `json:"maxRuns"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	Acceptance []TaskAcceptance `json:"acceptance"`
}

// EffectiveBeforeDependents is BeforeDependents with its default, true.
func (p IntegrationPlan) EffectiveBeforeDependents() bool {
	return p.BeforeDependents == nil || *p.BeforeDependents
}

// CoordinationPlanBudget is the family's planning allocation and lifetime ceiling (MR8).
type CoordinationPlanBudget struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	PlanningMicroUSD int64 `json:"planningMicroUSD"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	TotalMicroUSD int64 `json:"totalMicroUSD"`
}

// CoordinationSpec is a parent's switch values, fixed at creation (MR1, MR16).
type CoordinationSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == 2",message="maxChildren is 2 in this release (MR1)"
	MaxChildren int32 `json:"maxChildren"`
	// +kubebuilder:validation:XValidation:rule="self",message="publicOnly is true in this release (MR16)"
	PublicOnly bool `json:"publicOnly"`
}

// CoordinationParent is a child's or an integration Task's lineage, fixed at creation: a child
// names its childID, an integration Task none (MR2).
type CoordinationParent struct {
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	Task string `json:"task"`
	// +kubebuilder:validation:Minimum=1
	PlanVersion int32 `json:"planVersion"`
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,31}$`
	// +kubebuilder:validation:MaxLength=32
	// +optional
	ChildID string `json:"childID,omitempty"`
}

// IntegrationSpec is an integration Task's immutable manifest inputs (MR10).
type IntegrationSpec struct {
	// +kubebuilder:validation:Enum=beforeDependentMerge;final
	Purpose string `json:"purpose"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	PlanHash string `json:"planHash"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	Subjects []IntegrationSubject `json:"subjects"`
}

// IntegrationSubject is one child at an exact commit: a PR head before its merge, or its landed
// merge commit, never both.
// +kubebuilder:validation:XValidation:rule="has(self.headSHA) != has(self.mergeCommitSHA)",message="an integration subject names exactly one of headSHA or mergeCommitSHA"
type IntegrationSubject struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	// +optional
	HeadSHA string `json:"headSHA,omitempty"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	// +optional
	MergeCommitSHA string `json:"mergeCommitSHA,omitempty"`
}

// LandedArtifact is a produced file as read at the producer's landed merge commit: the only
// thing a dependency consumes (MR7).
type LandedArtifact struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	MergeCommitSHA string `json:"mergeCommitSHA"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	BlobSHA string `json:"blobSHA"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	ContentSHA256 string `json:"contentSHA256"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65536
	SizeBytes int64 `json:"sizeBytes"`
}

// ParentConfirmation is a primary maintainer's confirmation of one plan hash. It authorizes no
// child (MR5).
type ParentConfirmation struct {
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	PlanHash string `json:"planHash"`
	// The authenticated principal that confirmed.
	// +kubebuilder:validation:MaxLength=128
	Actor string `json:"actor"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`
	// +kubebuilder:validation:MaxLength=39
	LinkedGitHubLogin string      `json:"linkedGitHubLogin"`
	ConfirmedAt       metav1.Time `json:"confirmedAt"`
}

// ChildAuthorization is a child repository's linked maintainer authorizing one child fingerprint,
// with the write permission and fence read at that time (MR5, MR6).
type ChildAuthorization struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	ChildFingerprint string `json:"childFingerprint"`
	// +kubebuilder:validation:Minimum=1
	PlanVersion int32 `json:"planVersion"`
	// +kubebuilder:validation:MaxLength=128
	Actor string `json:"actor"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// The maintainer's GitHub permission on Repository when they authorized.
	// +kubebuilder:validation:Enum=write;maintain;admin
	Permission string `json:"permission"`
	// The repository's fence file blob on its default branch when they authorized.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	FenceSHA    string      `json:"fenceSHA"`
	ConfirmedAt metav1.Time `json:"confirmedAt"`
}

// IntegrationRelease is a passing integration's release of one exact dependent head to that
// repository's own gate. A head change makes it stale; it is never a merge approval (MR9).
type IntegrationRelease struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	HeadSHA string `json:"headSHA"`
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	IntegrationTask string `json:"integrationTask"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	ManifestHash string `json:"manifestHash"`
}

// RecoveryProposal is the one deterministic next action a failure proposes, never a playbook
// and never executed by the parent (MR11).
type RecoveryProposal struct {
	// +kubebuilder:validation:Enum=retry_child;resume_child;revise_plan;increase_budget;retry_producer;retry_dependent;retry_integration;rerun_integration;authorize_child
	Code string `json:"code"`
	// +kubebuilder:validation:MaxLength=32
	// +optional
	ChildID string `json:"childID,omitempty"`
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	IntegrationTask string `json:"integrationTask,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
	// +kubebuilder:validation:Minimum=1
	PlanVersion          int32       `json:"planVersion"`
	RequiresConfirmation bool        `json:"requiresConfirmation"`
	CreatedAt            metav1.Time `json:"createdAt"`
}

// CoordinationIncident is a child's revert after its parent is Done: recorded, never reopening
// the parent (MR11).
type CoordinationIncident struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	// +optional
	MergeCommitSHA string      `json:"mergeCommitSHA,omitempty"`
	At             metav1.Time `json:"at"`
}

// IntegrationResult is an integration Task's outcome, valid only for its immutable subjects (MR10).
type IntegrationResult struct {
	// +kubebuilder:validation:Enum=pending;passed;failed
	State string `json:"state"`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ManifestHash string `json:"manifestHash,omitempty"`
}

// CoordinationStatus is a bounded union. A parent keeps the plan, its hash and version, bounded
// confirmation history, budget, child lineage, artifacts, integration attempts, landing order,
// final acceptance, the one recovery proposal, incidents and at most two generation barriers. A
// child keeps only {planVersion, childID, allocationMicroUSD, integrationAcceptedHead}: lineage,
// never a second parent or split record (MR2).
// +kubebuilder:validation:XValidation:rule="!has(self.childID) || (!has(self.plan) && !has(self.planHash) && !has(self.history) && !has(self.confirmations) && !has(self.authorizations) && !has(self.budget) && !has(self.children) && !has(self.artifacts) && !has(self.integrationAttempts) && !has(self.mergeOrder) && !has(self.acceptance) && !has(self.recovery) && !has(self.incidents) && !has(self.pendingGenerations))",message="a child's status.coordination is lineage only"
// +kubebuilder:validation:XValidation:rule="has(self.childID) || (!has(self.allocationMicroUSD) && !has(self.integrationAcceptedHead))",message="an allocation and an accepted head are a child's"
type CoordinationStatus struct {
	// +kubebuilder:validation:Minimum=1
	// +optional
	PlanVersion int32 `json:"planVersion,omitempty"`

	// A child's fields.

	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,31}$`
	// +kubebuilder:validation:MaxLength=32
	// +optional
	ChildID string `json:"childID,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	AllocationMicroUSD int64 `json:"allocationMicroUSD,omitempty"`
	// The head integration last accepted for this child: stale once the PR head moves (MR9).
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}$`
	// +kubebuilder:validation:MaxLength=40
	// +optional
	IntegrationAcceptedHead string `json:"integrationAcceptedHead,omitempty"`

	// A parent's fields.

	// The current full plan snapshot.
	// +optional
	Plan *CoordinationPlan `json:"plan,omitempty"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	PlanHash string `json:"planHash,omitempty"`
	// The accepted versions, newest last; full snapshots are reposted on the parent issue.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	History []CoordinationPlanVersion `json:"history,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Confirmations []ParentConfirmation `json:"confirmations,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Authorizations []ChildAuthorization `json:"authorizations,omitempty"`
	// +optional
	Budget *CoordinationBudgetStatus `json:"budget,omitempty"`
	// +kubebuilder:validation:MaxItems=2
	// +optional
	Children []CoordinationChildStatus `json:"children,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Artifacts []LandedArtifact `json:"artifacts,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	IntegrationAttempts []IntegrationAttempt `json:"integrationAttempts,omitempty"`
	// The child ids in the order their pull requests landed.
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:MaxLength=32
	// +optional
	MergeOrder []string `json:"mergeOrder,omitempty"`
	// The final integration that let the parent be Done (MR10).
	// +optional
	Acceptance *CoordinationAcceptance `json:"acceptance,omitempty"`
	// +optional
	Recovery *RecoveryProposal `json:"recovery,omitempty"`
	// +kubebuilder:validation:MaxItems=4
	// +optional
	Incidents []CoordinationIncident `json:"incidents,omitempty"`
	// Affected generations waiting for their old generation to stop and its reservation to be
	// accounted before a replacement starts.
	// +kubebuilder:validation:MaxItems=2
	// +optional
	PendingGenerations []PendingGeneration `json:"pendingGenerations,omitempty"`
}

// CoordinationPlanVersion is one accepted plan version.
type CoordinationPlanVersion struct {
	// +kubebuilder:validation:Minimum=1
	Version int32 `json:"version"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	PlanHash   string      `json:"planHash"`
	AcceptedAt metav1.Time `json:"acceptedAt"`
}

// CoordinationBudgetStatus is the family's confirmed lifetime ceiling and its static allocations
// (MR8); per-run spend stays on the ledger and the runs.
type CoordinationBudgetStatus struct {
	// +kubebuilder:validation:Minimum=0
	PlanningMicroUSD int64 `json:"planningMicroUSD"`
	// +kubebuilder:validation:Minimum=0
	TotalMicroUSD int64 `json:"totalMicroUSD"`
	// +kubebuilder:validation:Minimum=0
	AllocatedMicroUSD int64 `json:"allocatedMicroUSD"`
}

// CoordinationChildStatus is a parent's lineage record of one child: its newest generation.
type CoordinationChildStatus struct {
	// +kubebuilder:validation:MaxLength=32
	ID string `json:"id"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=140
	Repository string `json:"repository"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Fingerprint string `json:"fingerprint,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Issue int `json:"issue,omitempty"`
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	Task string `json:"task,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Generation int32 `json:"generation,omitempty"`
	// +kubebuilder:validation:MaxLength=32
	// +optional
	Phase string `json:"phase,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	AllocationMicroUSD int64 `json:"allocationMicroUSD,omitempty"`
	// +optional
	IntegrationRelease *IntegrationRelease `json:"integrationRelease,omitempty"`
}

// IntegrationAttempt is a parent's record of one integration Task.
type IntegrationAttempt struct {
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	Task string `json:"task"`
	// +kubebuilder:validation:Enum=beforeDependentMerge;final
	Purpose string `json:"purpose"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	PlanHash string `json:"planHash"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ManifestHash string `json:"manifestHash,omitempty"`
	// +kubebuilder:validation:Enum=pending;passed;failed
	State string `json:"state"`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:MaxItems=2
	Subjects []IntegrationSubject `json:"subjects"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	RunMicroUSD int64 `json:"runMicroUSD,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`
}

// CoordinationAcceptance is the passing final integration over every child's merge commit.
type CoordinationAcceptance struct {
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	IntegrationTask string `json:"integrationTask"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=64
	ManifestHash string      `json:"manifestHash"`
	AcceptedAt   metav1.Time `json:"acceptedAt"`
}

// PendingGeneration is one affected child's replacement barrier after a material edit.
type PendingGeneration struct {
	// +kubebuilder:validation:MaxLength=32
	ChildID string `json:"childID"`
	// The generation waiting to start.
	// +kubebuilder:validation:Minimum=1
	Generation int32 `json:"generation"`
	// +kubebuilder:validation:Minimum=1
	PlanVersion int32 `json:"planVersion"`
	// The old generation's Task, stopping.
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	StoppingTask string `json:"stoppingTask,omitempty"`
}
