// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
)

// A coordination parent's projection (v4 design, "Roles and the parent view"). It is drawn from
// the viewer's filtered coordinationview.View only, never from TaskFacts, and shares nothing with
// the splitter's Split/Parent: a family is not a split, and no part semantics leak into it.

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

// reasonArtifactPending is a dependent's reason while a required artifact has not landed.
const reasonArtifactPending = "artifact_pending"

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

// AttachCoordination puts v on s and its flow on s.Flow, creating an empty run flow when s has
// none. The run flow's own fields are left as they were.
func AttachCoordination(s *Summary, v coordinationview.View) {
	s.Coordination = &v
	if s.Flow == nil {
		s.Flow = &Flow{Nodes: []FlowNode{}}
	}
	s.Flow.Coordination = CoordinationFlowOf(v)
}

// CoordinationFlowOf is v's flow.
func CoordinationFlowOf(v coordinationview.View) *CoordinationFlow {
	f := &CoordinationFlow{PlanVersion: v.Plan.Version, PlanHash: v.PlanHash, Nodes: []CoordinationNode{}, Edges: []CoordinationEdge{}, RestrictedChildren: v.RestrictedChildren}
	parentOK := v.ParentConfirmation != nil && v.ParentConfirmation.PlanHash == v.PlanHash
	allOK := parentOK
	dependents := false
	for _, c := range v.Plan.Children {
		live := liveChild(v, c.ID)
		n := CoordinationNode{ID: c.ID, Kind: NodeChild, ChildID: c.ID, Repository: c.Repository, Roles: slices.Clone(c.Roles)}
		switch {
		case !parentOK:
			n.RequiredConfirmation = ConfirmParent
		case !authorized(v, c.ID, live):
			n.RequiredConfirmation = ConfirmChild
		}
		if n.RequiredConfirmation != "" {
			allOK = false
		}
		pending := false
		for _, in := range c.Consumes {
			state := "pending"
			if landed(v, in.From, in.Artifact) {
				state = "landed"
			} else if in.Required {
				pending = true
			}
			f.Edges = append(f.Edges, CoordinationEdge{From: in.From, To: c.ID, Kind: EdgeArtifact, Name: in.From + "/" + in.Artifact, State: state})
		}
		if len(c.DependsOn) > 0 {
			dependents = true
		}
		if live != nil {
			n.Phase, n.Reason, n.Room, n.IssueURL, n.PRURL = live.Phase, live.Reason, live.Room, live.IssueURL, live.PRURL
		}
		if pending && n.Reason == "" {
			n.Reason = reasonArtifactPending
		}
		n.Disabled = n.RequiredConfirmation != "" || pending || live == nil || live.Task == ""
		f.Nodes = append(f.Nodes, n)
	}
	// An integration reads every child at once: with one withheld, it would show the viewer a
	// state and subjects resting on what they may not read.
	if v.RestrictedChildren > 0 || len(v.Plan.Children) == 0 {
		return f
	}
	roles := []string{v.Plan.Integration.Role}
	if dependents && v.Plan.Integration.EffectiveBeforeDependents() {
		f.Nodes = append(f.Nodes, integrationNode(v, IntegrationBeforeNode, "beforeDependentMerge", roles, allOK))
		for _, c := range v.Plan.Children {
			if len(c.DependsOn) == 0 {
				f.Edges = append(f.Edges, CoordinationEdge{From: c.ID, To: IntegrationBeforeNode, Kind: EdgeIntegration, State: attemptState(v, "beforeDependentMerge")})
				continue
			}
			f.Edges = append(f.Edges, CoordinationEdge{From: IntegrationBeforeNode, To: c.ID, Kind: EdgeIntegration, State: releaseState(liveChild(v, c.ID))})
		}
	}
	f.Nodes = append(f.Nodes, integrationNode(v, IntegrationFinalNode, "final", roles, allOK))
	for _, c := range v.Plan.Children {
		f.Edges = append(f.Edges, CoordinationEdge{From: c.ID, To: IntegrationFinalNode, Kind: EdgeIntegration, State: attemptState(v, "final")})
	}
	return f
}

// CoordinationOverviewOf is v's room-list row.
func CoordinationOverviewOf(v coordinationview.View) CoordinationOverview {
	o := CoordinationOverview{PlanVersion: v.Plan.Version, RestrictedChildren: v.RestrictedChildren, Children: []CoordinationChildState{}}
	if v.Recovery != nil {
		o.RecoveryCode = v.Recovery.Code
	}
	for _, c := range v.Plan.Children {
		s := CoordinationChildState{ID: c.ID, DependsOn: append([]string{}, c.DependsOn...)}
		if live := liveChild(v, c.ID); live != nil {
			s.Phase, s.Reason, s.IssueURL, s.PRURL = live.Phase, live.Reason, live.IssueURL, live.PRURL
		}
		o.Children = append(o.Children, s)
	}
	return o
}

func liveChild(v coordinationview.View, id string) *coordination.CoordinationChildView {
	for i := range v.Children {
		if v.Children[i].ID == id {
			return &v.Children[i]
		}
	}
	return nil
}

// authorized is whether id holds an authorization at this plan version and, once the child has a
// fingerprint, for that fingerprint.
func authorized(v coordinationview.View, id string, live *coordination.CoordinationChildView) bool {
	return slices.ContainsFunc(v.ChildAuthorizations, func(a coordination.ChildAuthorization) bool {
		return a.ChildID == id && a.PlanVersion == v.Plan.Version && (live == nil || live.Fingerprint == "" || a.ChildFingerprint == live.Fingerprint)
	})
}

func landed(v coordinationview.View, from, name string) bool {
	return slices.ContainsFunc(v.Artifacts, func(a v1alpha1.LandedArtifact) bool { return a.ChildID == from && a.Name == name })
}

// latestAttempt is the newest attempt for purpose: the core appends them oldest first.
func latestAttempt(v coordinationview.View, purpose string) *coordination.IntegrationAttemptView {
	for i := len(v.IntegrationAttempts) - 1; i >= 0; i-- {
		if v.IntegrationAttempts[i].Purpose == purpose {
			return &v.IntegrationAttempts[i]
		}
	}
	return nil
}

func attemptState(v coordinationview.View, purpose string) string {
	if a := latestAttempt(v, purpose); a != nil && a.State != "" {
		return a.State
	}
	return "pending"
}

// releaseState is whether a passing integration released this dependent's exact current head.
func releaseState(live *coordination.CoordinationChildView) string {
	switch {
	case live == nil || live.IntegrationRelease == nil:
		return "pending"
	case live.HeadSHA != "" && live.IntegrationRelease.HeadSHA == live.HeadSHA:
		return "released"
	default:
		return "stale"
	}
}

func integrationNode(v coordinationview.View, id, purpose string, roles []string, confirmed bool) CoordinationNode {
	n := CoordinationNode{ID: id, Kind: NodeIntegration, Roles: roles, Disabled: !confirmed}
	if a := latestAttempt(v, purpose); a != nil {
		n.Phase, n.Reason = a.State, a.Reason
		n.Disabled = false
	}
	return n
}
