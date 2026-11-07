// SPDX-License-Identifier: Apache-2.0

package coordination

import v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"

// The experience-neutral contracts (v4 plan, "Experience-neutral contracts"): versioned data, not
// HTTP surfaces. Consumers import these types or generate their wire types from them, never fork
// the fields. JSON names are lower camel case; hashes are lowercase hex without a prefix.
//
// CoordinationView is unfiltered and internal only: a consumer filters every child by the viewer's
// GitHub read before exposing anything (MR13). No human route serves it.

// ParentConfirmation is the CRD record itself: one representation only.
type ParentConfirmation = v1alpha1.ParentConfirmation

// ChildAuthorization is the CRD record itself: one representation only.
type ChildAuthorization = v1alpha1.ChildAuthorization

// CoordinationIncident is the CRD record itself: a post-completion revert that never reopens Done.
type CoordinationIncident = v1alpha1.CoordinationIncident //nolint:revive // the plan's contract name, imported as is

// CoordinationConfirmation is one confirmation request: the plan and hash it confirms, and either
// the parent's confirmation or one child's authorization (MR5).
type CoordinationConfirmation struct { //nolint:revive // the plan's contract name, imported as is
	Plan     v1alpha1.CoordinationPlan `json:"plan"`
	PlanHash string                    `json:"planHash"`
	Parent   *ParentConfirmation       `json:"parent,omitempty"`
	Child    *ChildAuthorization       `json:"child,omitempty"`
	Planning PlanningReconciliation    `json:"planning"`
}

// PlanningReconciliation is the planning attempt's cost as the shared intake route accounts it:
// its identifiers when it supplies them, never an invented AgentRun id.
type PlanningReconciliation struct {
	AttemptID          string `json:"attemptID"`
	EffectiveRequester string `json:"effectiveRequester"`
	ReservationID      string `json:"reservationID,omitempty"`
	SettlementID       string `json:"settlementID,omitempty"`
	ReservedMicroUSD   int64  `json:"reservedMicroUSD"`
	SettledMicroUSD    int64  `json:"settledMicroUSD"`
}

// CoordinationView is a parent's whole state for a consumer to filter and render.
type CoordinationView struct { //nolint:revive // the plan's contract name, imported as is
	ParentTask              string                     `json:"parentTask"`
	Phase                   string                     `json:"phase"`
	Reason                  string                     `json:"reason"`
	Plan                    v1alpha1.CoordinationPlan  `json:"plan"`
	PlanHash                string                     `json:"planHash"`
	ParentConfirmation      *ParentConfirmation        `json:"parentConfirmation,omitempty"`
	ChildAuthorizations     []ChildAuthorization       `json:"childAuthorizations"`
	Children                []CoordinationChildView    `json:"children"`
	Artifacts               []v1alpha1.LandedArtifact  `json:"artifacts"`
	IntegrationAttempts     []IntegrationAttemptView   `json:"integrationAttempts"`
	MergeOrder              []string                   `json:"mergeOrder"`
	Budget                  CoordinationBudgetView     `json:"budget"`
	Recovery                *v1alpha1.RecoveryProposal `json:"recovery,omitempty"`
	PostCompletionIncidents []CoordinationIncident     `json:"postCompletionIncidents,omitempty"`
}

// CoordinationChildView is one child as a view shows it. DependsOn holds child ids; Consumes holds
// <from>/<artifact> ids.
type CoordinationChildView struct { //nolint:revive // the plan's contract name, imported as is
	ID                      string                       `json:"id"`
	Repository              string                       `json:"repository"`
	Task                    string                       `json:"task"`
	Room                    string                       `json:"room"`
	IssueURL                string                       `json:"issueURL"`
	PRURL                   string                       `json:"prURL,omitempty"`
	Phase                   string                       `json:"phase"`
	Reason                  string                       `json:"reason"`
	Fingerprint             string                       `json:"fingerprint"`
	DependsOn               []string                     `json:"dependsOn"`
	Consumes                []string                     `json:"consumes"`
	HeadSHA                 string                       `json:"headSHA,omitempty"`
	MergeCommitSHA          string                       `json:"mergeCommitSHA,omitempty"`
	AllocationMicroUSD      int64                        `json:"allocationMicroUSD"`
	SettledMicroUSD         int64                        `json:"settledMicroUSD"`
	ReservedMicroUSD        int64                        `json:"reservedMicroUSD"`
	IntegrationAcceptedHead string                       `json:"integrationAcceptedHead,omitempty"`
	IntegrationRelease      *v1alpha1.IntegrationRelease `json:"integrationRelease,omitempty"`
}

// IntegrationAttemptView is one integration Task as a view shows it.
type IntegrationAttemptView struct {
	Task         string                        `json:"task"`
	Purpose      string                        `json:"purpose"`
	PlanHash     string                        `json:"planHash"`
	ManifestHash string                        `json:"manifestHash"`
	State        string                        `json:"state"`
	Reason       string                        `json:"reason"`
	Subjects     []v1alpha1.IntegrationSubject `json:"subjects"`
	RunMicroUSD  int64                         `json:"runMicroUSD"`
	CostMicroUSD int64                         `json:"costMicroUSD"`
}

// CoordinationBudgetView is the family's allocations beside their reserved and settled spend.
type CoordinationBudgetView struct { //nolint:revive // the plan's contract name, imported as is
	PlanningMicroUSD            int64 `json:"planningMicroUSD"`
	PlanningReservedMicroUSD    int64 `json:"planningReservedMicroUSD"`
	PlanningSettledMicroUSD     int64 `json:"planningSettledMicroUSD"`
	TotalMicroUSD               int64 `json:"totalMicroUSD"`
	AllocatedMicroUSD           int64 `json:"allocatedMicroUSD"`
	ChildrenReservedMicroUSD    int64 `json:"childrenReservedMicroUSD"`
	ChildrenSettledMicroUSD     int64 `json:"childrenSettledMicroUSD"`
	IntegrationReservedMicroUSD int64 `json:"integrationReservedMicroUSD"`
	IntegrationSettledMicroUSD  int64 `json:"integrationSettledMicroUSD"`
	IntegrationMaxRuns          int   `json:"integrationMaxRuns"`
	IntegrationRuns             int   `json:"integrationRuns"`
}

// CheckoutManifest is the parent checkout's contract (MR14): a declaration only, with no checkout
// behavior in this plan.
type CheckoutManifest struct {
	ParentTask  string          `json:"parentTask"`
	PlanVersion int32           `json:"planVersion"`
	PlanHash    string          `json:"planHash"`
	Children    []CheckoutChild `json:"children"`
}

// CheckoutChild is one child's verified worktree in a checkout. Issue and PR are validated URLs,
// not numbers.
type CheckoutChild struct {
	ID             string                    `json:"id"`
	Repository     string                    `json:"repository"`
	Worktree       string                    `json:"worktree"`
	Room           string                    `json:"room"`
	Issue          string                    `json:"issue"`
	PR             string                    `json:"pr"`
	DependsOn      []string                  `json:"dependsOn"`
	Artifacts      []v1alpha1.LandedArtifact `json:"artifacts"`
	HeadSHA        string                    `json:"headSHA,omitempty"`
	MergeCommitSHA string                    `json:"mergeCommitSHA,omitempty"`
}
