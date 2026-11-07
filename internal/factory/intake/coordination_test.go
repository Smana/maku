// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/taskid"
)

const coordRepo = "Smana/cloud-native-ref"

// coordParent is the example plan's filed parent, its contract child moved into this poller's
// repository: the parent issue is 7, the contract child's issue 8.
func coordParent(t *testing.T, phase string) *v1alpha1.Task {
	t.Helper()
	f, err := os.Open("../coordination/testdata/plan/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, err := coordination.DecodePlan(f)
	if err != nil {
		t.Fatal(err)
	}
	p.Primary.Repository, p.Children[0].Repository = coordRepo, coordRepo
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID, Namespace: "agent-system"},
		Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination, Repository: coordRepo, Issue: 7, DataClass: "public",
			Source: v1alpha1.Source{Kind: v1alpha1.SourceKindCoordination}},
		Status: v1alpha1.TaskStatus{Phase: phase, Reason: coordination.ReasonFiled, Coordination: &v1alpha1.CoordinationStatus{
			PlanVersion: p.Version, Plan: &p, PlanHash: coordination.PlanHash(p),
			Children: []v1alpha1.CoordinationChildStatus{{ID: "contract", Repository: coordRepo, Issue: 8, Generation: 1}}}}}
}

func coordIssue(f *forgetest.Fake, n int, author, body string) {
	labelled(f, n, "Smana")
	f.SetIssue(forge.Issue{Number: n, Title: "Coordination", Body: body, Author: author})
}

// The trigger never turns a coordination issue into a work Task from its own text: a parent issue
// is refused coordination_parent, even once its parent is Done; a forged marker grants nothing; an
// edited child snapshot is refused; an unchanged child retries as its one deterministic Task, with
// the coordination lineage and never the splitter's.
func TestCoordinationReadyLabelsNeverEnterSplitter(t *testing.T) {
	for _, tc := range []struct {
		name, phase, author string
		issue               int
		body                func(*v1alpha1.Task) string
		reason              string
		want                func(t *testing.T, parent *v1alpha1.Task, ts []v1alpha1.Task)
	}{
		{name: "a parent issue", phase: v1alpha1.PhaseCoordinating, author: "ogenki-agent-factory[bot]", issue: 7, reason: "coordination_parent",
			body: func(p *v1alpha1.Task) string {
				_, b := narrate.CoordinationParentIssue(p.Name, *p.Status.Coordination.Plan, p.Status.Coordination.PlanHash)
				return b
			}},
		{name: "a parent issue after Done", phase: v1alpha1.PhaseDone, author: "ogenki-agent-factory[bot]", issue: 7, reason: "coordination_parent",
			body: func(p *v1alpha1.Task) string {
				return narrate.CoordinationParentMarker(p.Name, p.Status.Coordination.Plan.Version, p.Status.Coordination.PlanHash) + "\nx"
			}},
		{name: "a forged parent marker", phase: v1alpha1.PhaseCoordinating, author: "mallory", issue: 7,
			body: func(p *v1alpha1.Task) string {
				return narrate.CoordinationParentMarker(p.Name, p.Status.Coordination.Plan.Version, p.Status.Coordination.PlanHash)
			},
			want: func(t *testing.T, _ *v1alpha1.Task, ts []v1alpha1.Task) {
				name := taskid.Name(taskid.IssueKey(coordRepo, 7, 1))
				for _, x := range ts {
					if x.Name == name && x.Spec.CoordinationParent == nil && x.Spec.Kind == "" && x.Labels[v1alpha1.LabelCoordinationParent] == "" {
						return
					}
				}
				t.Fatalf("no ordinary task with no lineage: %d tasks", len(ts))
			}},
		{name: "an edited child snapshot", phase: v1alpha1.PhaseCoordinating, author: "ogenki-agent-factory[bot]", issue: 8, reason: "coordination_scope_changed",
			body: func(p *v1alpha1.Task) string {
				_, b := narrate.CoordinationChildIssue(p.Name, *p.Status.Coordination.Plan, p.Status.Coordination.Plan.Children[0])
				return strings.Replace(b, "nothing else", "also the deploy key", 1)
			}},
		{name: "an unchanged child snapshot", phase: v1alpha1.PhaseCoordinating, author: "ogenki-agent-factory[bot]", issue: 8,
			body: func(p *v1alpha1.Task) string {
				_, b := narrate.CoordinationChildIssue(p.Name, *p.Status.Coordination.Plan, p.Status.Coordination.Plan.Children[0])
				return b
			},
			want: func(t *testing.T, parent *v1alpha1.Task, ts []v1alpha1.Task) {
				want := coordination.ChildTask("agent-system", &config.Config{}, parent, parent.Status.Coordination.Plan.Children[0], 8, 1).Name
				for _, x := range ts {
					if x.Name == want {
						if x.Spec.Parent != nil || x.Labels[v1alpha1.LabelParent] != "" || x.Labels[v1alpha1.LabelCoordinationParent] != parent.Name ||
							x.Spec.CoordinationParent == nil || x.Spec.Source.ContentSHA256 != parent.Status.Coordination.PlanHash {
							t.Fatalf("child %+v %v", x.Spec, x.Labels)
						}
						return
					}
				}
				t.Fatalf("no deterministic child task among %d", len(ts))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := coordParent(t, tc.phase)
			f := forgetest.NewFake()
			coordIssue(f, tc.issue, tc.author, tc.body(parent))
			p, c := poller(t, f, parent)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			ts := tasks(t, c)
			if tc.reason != "" {
				if len(ts) != 1 || !contains(f.Comments(tc.issue), narrate.Reason(tc.reason)) {
					t.Fatalf("%d tasks, comments %q, want only the parent and %s", len(ts), f.Comments(tc.issue), tc.reason)
				}
				return
			}
			for _, x := range ts {
				if x.Spec.Parent != nil || x.Labels[v1alpha1.LabelParent] != "" || x.Spec.Split != nil {
					t.Fatalf("task %s carries splitter lineage", x.Name)
				}
			}
			tc.want(t, parent, ts)
		})
	}
}

func contains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
