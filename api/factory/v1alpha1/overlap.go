// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Overlap classifications. no_match covers the checked sources only; unverified_overlap records
// missing or inconclusive evidence and never proves that no duplicate exists.
const (
	OverlapActiveDuplicate    = "active_duplicate"
	OverlapCompletedDuplicate = "completed_duplicate"
	OverlapPriorAttempt       = "prior_attempt"
	OverlapPartialOverlap     = "partial_overlap"
	OverlapUnverifiedOverlap  = "unverified_overlap"
	OverlapNoMatch            = "no_match"
)

// OverlapStatus is the bounded outcome-overlap evaluation persisted on a Task. While review is
// pending, Classification and CheckedAt are both absent. ScoperRun is the deterministic evaluation
// run's C2 id, not the generated AgentRun claim name.
// +kubebuilder:validation:XValidation:rule="has(self.classification) == has(self.checkedAt)",message="classification and checkedAt are both empty while review is pending"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.classification) || (has(self.classification) && self.classification == oldSelf.classification && has(self.matches) == has(oldSelf.matches) && (!has(self.matches) || self.matches == oldSelf.matches) && has(self.sources) == has(oldSelf.sources) && (!has(self.sources) || self.sources == oldSelf.sources) && has(self.note) == has(oldSelf.note) && (!has(self.note) || self.note == oldSelf.note))",message="a final overlap classification and its evidence never change"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.classification) || (has(self.scoperRun) == has(oldSelf.scoperRun) && (!has(self.scoperRun) || self.scoperRun == oldSelf.scoperRun))",message="scoperRun may advance only while overlap review is pending"
type OverlapStatus struct {
	// SHA-256 of the repository-scoped canonical goal, in lowercase hexadecimal.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=64
	Fingerprint string `json:"fingerprint"`
	// Empty while candidate review is pending.
	// +kubebuilder:validation:Enum=active_duplicate;completed_duplicate;prior_attempt;partial_overlap;unverified_overlap;no_match
	// +optional
	Classification string `json:"classification,omitempty"`
	// +kubebuilder:validation:MaxItems=20
	// +optional
	Matches []OverlapMatch `json:"matches,omitempty"`
	// The sources actually checked; absence outside them is not evidence of no match.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=tasks;runs;rooms;github
	// +optional
	Sources []string `json:"sources,omitempty"`
	// The deterministic evaluation run's C2 id.
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	ScoperRun string `json:"scoperRun,omitempty"`
	// Empty while candidate review is pending.
	// +optional
	CheckedAt *metav1.Time `json:"checkedAt,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Note string `json:"note,omitempty"`
}

// OverlapMatch cites one candidate without copying its untrusted text into Task status.
type OverlapMatch struct {
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	Task string `json:"task,omitempty"`
	// The candidate run's C2 id.
	// +kubebuilder:validation:Pattern=`^[a-z2-7]{8}$`
	// +kubebuilder:validation:MaxLength=8
	// +optional
	Run string `json:"run,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	Issue int `json:"issue,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	PR int `json:"pr,omitempty"`
	// +kubebuilder:validation:Enum=exact;lexical
	Relation string `json:"relation"`
	// The candidate's existing Task phase or outcome.
	// +kubebuilder:validation:MaxLength=64
	Phase string `json:"phase"`
}

// OverlapPhase maps an existing Task phase to overlap evidence. Active here means outcome work is
// still in progress, independently of the execution-cap-oriented ActivePhase and TerminalPhase sets.
func OverlapPhase(phase string) (classification string, active bool) {
	switch phase {
	case PhaseReceived, PhaseTriaged, PhaseQueued, PhaseImplementing, PhaseReviewing, PhaseAwaitingCI,
		PhaseAutoMerging, PhaseVerifying, PhaseCoordinating, PhaseAwaitingHuman, PhaseMerged:
		return OverlapActiveDuplicate, true
	case PhaseDone, PhaseNoOp:
		return OverlapCompletedDuplicate, false
	case PhaseRejected, PhaseReverted, PhaseEscalated, PhaseClosed, PhaseStopped:
		return OverlapPriorAttempt, false
	case PhaseSplit:
		return OverlapPartialOverlap, false
	default:
		return OverlapUnverifiedOverlap, false
	}
}
