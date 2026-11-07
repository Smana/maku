// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/summary"
)

// family is "contract" producing consumer-v1-schema in acme/api, and "consumer" in acme/web
// consuming it; before filing when filed is false.
func family(filed bool) View {
	plan := v1alpha1.CoordinationPlan{
		ID: "parent-1", Title: "v1 schema", Version: 2,
		Primary: v1alpha1.CoordinationPrimary{Repository: "acme/api"},
		Children: []v1alpha1.CoordinationChild{
			{ID: "contract", Repository: "acme/api", Roles: []string{"implementer", "reviewer"},
				Produces: []v1alpha1.ArtifactSpec{{Name: "consumer-v1-schema", Kind: "file", Path: "schema/v1.json"}}},
			{ID: "consumer", Repository: "acme/web", Roles: []string{"implementer", "reviewer"}, DependsOn: []string{"contract"},
				Consumes: []v1alpha1.ArtifactConsumer{{From: "contract", Artifact: "consumer-v1-schema", Required: true}}},
		},
		MergeOrder:  []string{"contract", "consumer"},
		Integration: v1alpha1.IntegrationPlan{Role: "triager", RunMicroUSD: 500_000, MaxRuns: 2},
	}
	v := coordination.CoordinationView{ParentTask: "parent-1", Phase: "Planned", Plan: plan, PlanHash: "0123456789abcdef"}
	if !filed {
		return View{CoordinationView: v}
	}
	v.Phase = "Coordinating"
	v.ParentConfirmation = &coordination.ParentConfirmation{PlanHash: "0123456789abcdef"}
	v.ChildAuthorizations = []coordination.ChildAuthorization{
		{ChildID: "contract", ChildFingerprint: "fc", PlanVersion: 2},
		{ChildID: "consumer", ChildFingerprint: "fd", PlanVersion: 2},
	}
	v.Children = []coordination.CoordinationChildView{
		{ID: "contract", Repository: "acme/api", Task: "t-contract", Room: "t-contract", Fingerprint: "fc",
			IssueURL: "https://github.com/acme/api/issues/1", PRURL: "https://github.com/acme/api/pull/2",
			Phase: "Merged", Reason: "merged", HeadSHA: "h1", MergeCommitSHA: "m1"},
		{ID: "consumer", Repository: "acme/web", Task: "t-consumer", Room: "t-consumer", Fingerprint: "fd",
			IssueURL: "https://github.com/acme/web/issues/3", PRURL: "https://github.com/acme/web/pull/4",
			Phase: "AwaitingHuman", Reason: "integration_pending", HeadSHA: "h2", DependsOn: []string{"contract"},
			Consumes:           []string{"contract/consumer-v1-schema"},
			IntegrationRelease: &v1alpha1.IntegrationRelease{ChildID: "consumer", HeadSHA: "h2", IntegrationTask: "int-1"}},
	}
	v.Artifacts = []v1alpha1.LandedArtifact{{ChildID: "contract", Name: "consumer-v1-schema", Repository: "acme/api", MergeCommitSHA: "m1"}}
	v.IntegrationAttempts = []coordination.IntegrationAttemptView{
		{Task: "int-1", Purpose: "beforeDependentMerge", State: "passed", Subjects: []v1alpha1.IntegrationSubject{{ChildID: "contract", MergeCommitSHA: "m1"}, {ChildID: "consumer", HeadSHA: "h2"}}},
	}
	return View{CoordinationView: v}
}

func node(t *testing.T, f *summary.CoordinationFlow, id string) summary.CoordinationNode {
	t.Helper()
	for _, n := range f.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q in %+v", id, f.Nodes)
	return summary.CoordinationNode{}
}

func edge(f *summary.CoordinationFlow, from, to string) *summary.CoordinationEdge {
	for i := range f.Edges {
		if f.Edges[i].From == from && f.Edges[i].To == to {
			return &f.Edges[i]
		}
	}
	return nil
}

func TestCoordinationFlowBeforeAndAfterFiling(t *testing.T) {
	t.Run("before filing every node is planned and disabled", func(t *testing.T) {
		f := FlowOf(family(false))
		if f.PlanVersion != 2 || f.PlanHash != "0123456789abcdef" {
			t.Fatalf("plan identity = %d %q", f.PlanVersion, f.PlanHash)
		}
		ids := []string{}
		for _, n := range f.Nodes {
			ids = append(ids, n.ID)
			if !n.Disabled {
				t.Errorf("%s enabled before confirmation", n.ID)
			}
		}
		want := []string{"contract", "consumer", summary.IntegrationBeforeNode, summary.IntegrationFinalNode}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("nodes = %v, want %v (no parent run or merge node)", ids, want)
		}
		c := node(t, f, "contract")
		if c.Kind != summary.NodeChild || c.RequiredConfirmation != "parent" || !reflect.DeepEqual(c.Roles, []string{"implementer", "reviewer"}) || c.Room != "" {
			t.Fatalf("contract = %+v", c)
		}
		if i := node(t, f, summary.IntegrationFinalNode); i.Kind != summary.NodeIntegration || !reflect.DeepEqual(i.Roles, []string{"triager"}) {
			t.Fatalf("final = %+v", i)
		}
		e := edge(f, "contract", "consumer")
		if e == nil || e.Kind != summary.EdgeArtifact || e.Name != "contract/consumer-v1-schema" || e.State != "pending" {
			t.Fatalf("artifact edge = %+v", e)
		}
	})

	t.Run("a confirmed parent waits on each child's authorization", func(t *testing.T) {
		v := family(false)
		v.ParentConfirmation = &coordination.ParentConfirmation{PlanHash: v.PlanHash}
		v.ChildAuthorizations = []coordination.ChildAuthorization{{ChildID: "contract", PlanVersion: 2}}
		f := FlowOf(v)
		if n := node(t, f, "contract"); n.RequiredConfirmation != "" {
			t.Fatalf("authorized contract still asks %q", n.RequiredConfirmation)
		}
		if n := node(t, f, "consumer"); n.RequiredConfirmation != "child" || !n.Disabled {
			t.Fatalf("consumer = %+v", n)
		}
		if n := node(t, f, summary.IntegrationFinalNode); !n.Disabled {
			t.Fatal("integration enabled before every authorization")
		}
	})

	t.Run("a confirmation of an older hash is not a confirmation", func(t *testing.T) {
		v := family(true)
		v.ParentConfirmation.PlanHash = "stale"
		if n := node(t, FlowOf(v), "contract"); n.RequiredConfirmation != "parent" {
			t.Fatalf("contract = %+v", n)
		}
	})

	t.Run("after filing nodes carry the live child", func(t *testing.T) {
		f := FlowOf(family(true))
		c := node(t, f, "consumer")
		if c.Disabled || c.Phase != "AwaitingHuman" || c.Reason != "integration_pending" || c.Room != "t-consumer" ||
			c.IssueURL != "https://github.com/acme/web/issues/3" || c.PRURL != "https://github.com/acme/web/pull/4" {
			t.Fatalf("consumer = %+v", c)
		}
		if e := edge(f, "contract", "consumer"); e == nil || e.State != "landed" {
			t.Fatalf("artifact edge = %+v", e)
		}
		if e := edge(f, summary.IntegrationBeforeNode, "consumer"); e == nil || e.Kind != summary.EdgeIntegration || e.State != "released" {
			t.Fatalf("release edge = %+v", e)
		}
		if n := node(t, f, summary.IntegrationBeforeNode); n.Phase != "passed" {
			t.Fatalf("pre integration = %+v", n)
		}
	})

	t.Run("a release for another head is stale", func(t *testing.T) {
		v := family(true)
		v.Children[1].HeadSHA = "h3"
		if e := edge(FlowOf(v), summary.IntegrationBeforeNode, "consumer"); e == nil || e.State != "stale" {
			t.Fatalf("release edge = %+v", e)
		}
	})

	t.Run("a dependent without its landed artifact stays disabled", func(t *testing.T) {
		v := family(true)
		v.Artifacts = nil
		v.Children[1].Phase, v.Children[1].Reason = "", ""
		n := node(t, FlowOf(v), "consumer")
		if !n.Disabled || n.Reason != "artifact_pending" {
			t.Fatalf("consumer = %+v", n)
		}
	})

	t.Run("no pre integration when dependents need none", func(t *testing.T) {
		v := family(true)
		no := false
		v.Plan.Integration.BeforeDependents = &no
		for _, n := range FlowOf(v).Nodes {
			if n.ID == summary.IntegrationBeforeNode {
				t.Fatal("pre integration drawn when beforeDependents is false")
			}
		}
	})
}

func TestCoordinationProjectionDropsRestrictedIntegration(t *testing.T) {
	v := family(true)
	// What the filter leaves of a family whose consumer this viewer cannot read.
	v.Plan.Children = v.Plan.Children[:1]
	v.Plan.MergeOrder = []string{"contract"}
	v.Children = v.Children[:1]
	v.ChildAuthorizations = v.ChildAuthorizations[:1]
	v.IntegrationAttempts = nil
	v.RestrictedChildren = 1
	f := FlowOf(v)
	if f.RestrictedChildren != 1 {
		t.Fatalf("restricted = %d", f.RestrictedChildren)
	}
	for _, n := range f.Nodes {
		if n.Kind == summary.NodeIntegration {
			t.Fatalf("integration node drawn over a restricted child: %+v", n)
		}
	}
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), "consumer") {
		t.Fatalf("the restricted child reached the flow: %s", b)
	}
}

func TestAttachCoordinationLeavesTheRunFlowAlone(t *testing.T) {
	s := summary.Summary{Flow: &summary.Flow{Plan: []string{"step"}, Nodes: []summary.FlowNode{{Run: "r1"}}}}
	before, _ := json.Marshal(s.Flow)
	v := family(true)
	Attach(&s, v)
	if got, ok := Of(s); !ok || got.PlanHash != v.PlanHash {
		t.Fatal("view not attached")
	}
	if s.Flow.Coordination == nil || s.Flow.Split != nil || s.Flow.Parent != nil {
		t.Fatalf("flow = %+v", s.Flow)
	}
	cp := *s.Flow
	cp.Coordination = nil
	after, _ := json.Marshal(&cp)
	if string(before) != string(after) {
		t.Fatalf("run flow changed: %s -> %s", before, after)
	}

	var empty summary.Summary
	Attach(&empty, v)
	if empty.Flow == nil || empty.Flow.Coordination == nil || empty.Flow.Nodes == nil {
		t.Fatal("a summary without a flow gets one holding only the coordination")
	}
}

func TestCoordinationOverviewIsCompact(t *testing.T) {
	v := family(true)
	v.Recovery = &v1alpha1.RecoveryProposal{Code: "retry_integration"}
	v.RestrictedChildren = 1
	o := OverviewOf(v)
	want := summary.CoordinationOverview{PlanVersion: 2, RestrictedChildren: 1, RecoveryCode: "retry_integration", Children: []summary.CoordinationChildState{
		{ID: "contract", Phase: "Merged", Reason: "merged", IssueURL: "https://github.com/acme/api/issues/1", PRURL: "https://github.com/acme/api/pull/2", DependsOn: []string{}},
		{ID: "consumer", Phase: "AwaitingHuman", Reason: "integration_pending", IssueURL: "https://github.com/acme/web/issues/3", PRURL: "https://github.com/acme/web/pull/4", DependsOn: []string{"contract"}},
	}}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("overview =\n%+v\nwant\n%+v", o, want)
	}
}
