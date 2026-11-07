// SPDX-License-Identifier: Apache-2.0

package summary

// A coordination parent's projection (v4 design, "Roles and the parent view"): plain data, which
// coordinationview derives from the viewer's filtered view only, never from TaskFacts. It shares
// nothing with the splitter's Split/Parent. summary imports nothing of the factory, so the factory's
// own packages (narrate) may keep importing summary.

// The node kinds of a coordination flow.
const (
	NodeChild       = "child"
	NodeIntegration = "integration"
)

// The edge kinds: an artifact a dependent consumes, or an integration's read of a child.
const (
	EdgeArtifact    = "artifact"
	EdgeIntegration = "integration"
)

// The integration nodes' ids: the read before a dependent merges, and the family's final one.
// Neither can collide with a child id, a C2 id without a colon.
const (
	IntegrationBeforeNode = "integration:beforeDependentMerge"
	IntegrationFinalNode  = "integration:final"
)

// The confirmation a node still waits for.
const (
	ConfirmParent = "parent"
	ConfirmChild  = "child"
)

// CoordinationFlow is the parent's plan and live progress as the room page draws it: the children
// and integrations, the artifacts between them, and how many children this viewer may not see.
type CoordinationFlow struct {
	PlanVersion        int32              `json:"planVersion"`
	PlanHash           string             `json:"planHash"`
	Nodes              []CoordinationNode `json:"nodes"`
	Edges              []CoordinationEdge `json:"edges"`
	RestrictedChildren int                `json:"restrictedChildren"`
}

// CoordinationNode is a child or an integration. Disabled is a node that cannot start yet: a
// confirmation it waits for (RequiredConfirmation), or a dependency's artifact. Room, IssueURL and
// PRURL are set once the child is filed.
type CoordinationNode struct {
	ID                   string   `json:"id"`
	Kind                 string   `json:"kind"`
	ChildID              string   `json:"childID,omitempty"`
	Repository           string   `json:"repository,omitempty"`
	Phase                string   `json:"phase,omitempty"`
	Reason               string   `json:"reason,omitempty"`
	Roles                []string `json:"roles"`
	Room                 string   `json:"room,omitempty"`
	IssueURL             string   `json:"issueURL,omitempty"`
	PRURL                string   `json:"prURL,omitempty"`
	Disabled             bool     `json:"disabled"`
	RequiredConfirmation string   `json:"requiredConfirmation,omitempty"`
}

// CoordinationEdge is From feeding To: an artifact (Name <from>/<artifact>, State pending or
// landed), or an integration's read (State the attempt's, or released/stale for the exact head
// a passing integration released to a dependent).
type CoordinationEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	State string `json:"state"`
}

// CoordinationOverview is a parent's compact row in the room list.
type CoordinationOverview struct {
	PlanVersion        int32                    `json:"planVersion"`
	RestrictedChildren int                      `json:"restrictedChildren"`
	Children           []CoordinationChildState `json:"children"`
	RecoveryCode       string                   `json:"recoveryCode,omitempty"`
}

// CoordinationChildState is one visible child in a row.
type CoordinationChildState struct {
	ID        string   `json:"id"`
	Phase     string   `json:"phase,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	IssueURL  string   `json:"issueURL,omitempty"`
	PRURL     string   `json:"prURL,omitempty"`
	DependsOn []string `json:"dependsOn"`
}
