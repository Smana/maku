// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"encoding/json"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/summary"
)

// Attach puts v on s, as JSON, and its flow on s.Flow, creating an empty run flow when s has none.
// The run flow's own fields are left as they were.
func Attach(s *summary.Summary, v View) {
	raw, _ := json.Marshal(v) // strings, numbers and times only: it cannot fail
	s.Coordination = raw
	if s.Flow == nil {
		s.Flow = &summary.Flow{Nodes: []summary.FlowNode{}}
	}
	s.Flow.Coordination = FlowOf(v)
}

// Of is the view a summary carries, when it carries one.
func Of(s summary.Summary) (View, bool) {
	var v View
	if len(s.Coordination) == 0 || json.Unmarshal(s.Coordination, &v) != nil {
		return View{}, false
	}
	return v, true
}

// reasonArtifactPending is a dependent's reason while a required artifact has not landed.
const reasonArtifactPending = "artifact_pending"

// FlowOf is v's flow.coordination.
func FlowOf(v View) *summary.CoordinationFlow {
	f := &summary.CoordinationFlow{PlanVersion: v.Plan.Version, PlanHash: v.PlanHash, Nodes: []summary.CoordinationNode{}, Edges: []summary.CoordinationEdge{}, RestrictedChildren: v.RestrictedChildren}
	parentOK := v.ParentConfirmation != nil && v.ParentConfirmation.PlanHash == v.PlanHash
	allOK := parentOK
	dependents := false
	for _, c := range v.Plan.Children {
		live := liveChild(v, c.ID)
		n := summary.CoordinationNode{ID: c.ID, Kind: summary.NodeChild, ChildID: c.ID, Repository: c.Repository, Roles: slices.Clone(c.Roles)}
		switch {
		case !parentOK:
			n.RequiredConfirmation = summary.ConfirmParent
		case !authorized(v, c.ID, live):
			n.RequiredConfirmation = summary.ConfirmChild
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
			f.Edges = append(f.Edges, summary.CoordinationEdge{From: in.From, To: c.ID, Kind: summary.EdgeArtifact, Name: in.From + "/" + in.Artifact, State: state})
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
		f.Nodes = append(f.Nodes, integrationNode(v, summary.IntegrationBeforeNode, "beforeDependentMerge", roles, allOK))
		for _, c := range v.Plan.Children {
			if len(c.DependsOn) == 0 {
				f.Edges = append(f.Edges, summary.CoordinationEdge{From: c.ID, To: summary.IntegrationBeforeNode, Kind: summary.EdgeIntegration, State: attemptState(v, "beforeDependentMerge")})
				continue
			}
			f.Edges = append(f.Edges, summary.CoordinationEdge{From: summary.IntegrationBeforeNode, To: c.ID, Kind: summary.EdgeIntegration, State: releaseState(liveChild(v, c.ID))})
		}
	}
	f.Nodes = append(f.Nodes, integrationNode(v, summary.IntegrationFinalNode, "final", roles, allOK))
	for _, c := range v.Plan.Children {
		f.Edges = append(f.Edges, summary.CoordinationEdge{From: c.ID, To: summary.IntegrationFinalNode, Kind: summary.EdgeIntegration, State: attemptState(v, "final")})
	}
	return f
}

// OverviewOf is v's room-list row.
func OverviewOf(v View) summary.CoordinationOverview {
	o := summary.CoordinationOverview{PlanVersion: v.Plan.Version, RestrictedChildren: v.RestrictedChildren, Children: []summary.CoordinationChildState{}}
	if v.Recovery != nil {
		o.RecoveryCode = v.Recovery.Code
	}
	for _, c := range v.Plan.Children {
		s := summary.CoordinationChildState{ID: c.ID, DependsOn: append([]string{}, c.DependsOn...)}
		if live := liveChild(v, c.ID); live != nil {
			s.Phase, s.Reason, s.IssueURL, s.PRURL = live.Phase, live.Reason, live.IssueURL, live.PRURL
		}
		o.Children = append(o.Children, s)
	}
	return o
}

func liveChild(v View, id string) *coordination.CoordinationChildView {
	for i := range v.Children {
		if v.Children[i].ID == id {
			return &v.Children[i]
		}
	}
	return nil
}

// authorized is whether id holds an authorization at this plan version and, once the child has a
// fingerprint, for that fingerprint.
func authorized(v View, id string, live *coordination.CoordinationChildView) bool {
	return slices.ContainsFunc(v.ChildAuthorizations, func(a coordination.ChildAuthorization) bool {
		return a.ChildID == id && a.PlanVersion == v.Plan.Version && (live == nil || live.Fingerprint == "" || a.ChildFingerprint == live.Fingerprint)
	})
}

func landed(v View, from, name string) bool {
	return slices.ContainsFunc(v.Artifacts, func(a v1alpha1.LandedArtifact) bool { return a.ChildID == from && a.Name == name })
}

// latestAttempt is the newest attempt for purpose: the core appends them oldest first.
func latestAttempt(v View, purpose string) *coordination.IntegrationAttemptView {
	for i := len(v.IntegrationAttempts) - 1; i >= 0; i-- {
		if v.IntegrationAttempts[i].Purpose == purpose {
			return &v.IntegrationAttempts[i]
		}
	}
	return nil
}

func attemptState(v View, purpose string) string {
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

func integrationNode(v View, id, purpose string, roles []string, confirmed bool) summary.CoordinationNode {
	n := summary.CoordinationNode{ID: id, Kind: summary.NodeIntegration, Roles: roles, Disabled: !confirmed}
	if a := latestAttempt(v, purpose); a != nil {
		n.Phase, n.Reason = a.State, a.Reason
		n.Disabled = false
	}
	return n
}
