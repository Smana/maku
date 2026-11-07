// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
)

const (
	coordA = "Smana/public-contracts"
	coordB = "Smana/public-consumer"
)

// fakeCoordination is the coordination service as the reconciler sees it: File and ExecuteReady
// answer per child from ready, and every call is recorded.
type fakeCoordination struct {
	ready map[string]error
	files []string
	asked []string
}

func (f *fakeCoordination) File(_ context.Context, name string) error {
	f.files = append(f.files, name)
	return nil
}

func (f *fakeCoordination) ExecuteReady(_ context.Context, _ *v1alpha1.CoordinationStatus, ids []string) error {
	for _, id := range ids {
		f.asked = append(f.asked, id)
		if err := f.ready[id]; err != nil {
			return err
		}
	}
	return nil
}

func coordinationPlan(t *testing.T, independent bool) v1alpha1.CoordinationPlan {
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
	if independent {
		p.Children[1].DependsOn, p.Children[1].Consumes = nil, nil
	}
	return p
}

// filedParent is p's parent with every confirmation and its two issues filed: 21 in A, 22 in B.
func filedParent(p v1alpha1.CoordinationPlan, reason string) *v1alpha1.Task {
	h := coordination.PlanHash(p)
	cs := &v1alpha1.CoordinationStatus{PlanVersion: p.Version, Plan: p.DeepCopy(), PlanHash: h,
		Confirmations: []v1alpha1.ParentConfirmation{{PlanHash: h, Actor: "zitadel:s", LinkedGitHubLogin: "Smana", ConfirmedAt: metav1.NewTime(now)}}}
	for i, c := range p.Children {
		cs.Authorizations = append(cs.Authorizations, v1alpha1.ChildAuthorization{ChildID: c.ID, ChildFingerprint: coordination.ChildFingerprint(p, c.ID),
			PlanVersion: p.Version, Actor: "zitadel:" + c.ID, Repository: c.Repository, Permission: "write", FenceSHA: fmt.Sprintf("%040d", i), ConfirmedAt: metav1.NewTime(now)})
		cs.Children = append(cs.Children, v1alpha1.CoordinationChildStatus{ID: c.ID, Repository: c.Repository, Issue: 21 + i, Generation: 1,
			Fingerprint: coordination.ChildFingerprint(p, c.ID), AllocationMicroUSD: c.BudgetMicroUSD})
	}
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID, Namespace: "agent-system", CreationTimestamp: metav1.NewTime(now)},
		Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination, Repository: p.Primary.Repository, Issue: 20, Text: p.Title, DataClass: "public",
			Source: v1alpha1.Source{Kind: v1alpha1.SourceKindCoordination, Ref: "coordination:" + p.ID, Key: "coordination:" + p.ID,
				RequestedBy: "github:smana", Trust: "untrusted", ContentSHA256: h},
			Coordination: &v1alpha1.CoordinationSpec{MaxChildren: 2, PublicOnly: true}},
		Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseCoordinating, Reason: reason, Coordination: cs}}
}

func coordinationRig(t *testing.T, mode string, objs ...client.Object) (*rig, *fakeCoordination) {
	t.Helper()
	g := newRig(t, objs...)
	g.r.Cfg.Coordination.Mode = mode
	fc := &fakeCoordination{ready: map[string]error{}}
	g.r.Coordination = fc
	return g, fc
}

func (g *rig) coordTask(t *testing.T, name string) (*v1alpha1.Task, bool) {
	t.Helper()
	var tk v1alpha1.Task
	err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: name}, &tk)
	return &tk, err == nil
}

// MR2: a parent is Coordinating with a bounded reason and a planning Room on the primary, and never
// triages, scopes, splits, runs, files under plan mode, or holds an active slot.
func TestCoordinationParentNeverRuns(t *testing.T) {
	p := coordinationPlan(t, false)
	for _, mode := range []string{config.CoordinationPlan, config.CoordinationExecute} {
		t.Run(mode, func(t *testing.T) {
			parent := filedParent(p, coordination.ReasonAwaitingConfirmation)
			parent.Status.Phase = ""
			parent.Status.Coordination.Confirmations = nil // not yet confirmed: nothing files
			g, fc := coordinationRig(t, mode, parent)
			tk := g.reconcile(t, p.ID, 4)
			if tk.Status.Phase != v1alpha1.PhaseCoordinating || tk.Status.Reason != coordination.ReasonAwaitingConfirmation || v1alpha1.ActivePhase(tk.Status.Phase) {
				t.Fatalf("phase %s/%s", tk.Status.Phase, tk.Status.Reason)
			}
			var room roomv1.Room
			if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: p.ID}, &room); err != nil ||
				room.Spec.Repository != coordA || tk.Status.RoomRef != p.ID {
				t.Fatalf("room %+v %v", room.Spec, err)
			}
			if len(g.runs.specs) != 0 || tk.Spec.Template != "" || tk.Status.Classification != nil || tk.Spec.Split != nil ||
				len(fc.files) != 0 || len(g.f.Created()) != 0 || len(g.f.Armed()) != 0 || len(g.f.Reverts()) != 0 {
				t.Fatalf("the parent ran, triaged or filed: runs %d template %q files %v", len(g.runs.specs), tk.Spec.Template, fc.files)
			}
		})
	}
}

// Under execute, a fully confirmed parent files through the service; once filed it creates each
// independent child once, in its own repository, with the plan's frozen scope, class, template,
// allocation, authorizer and hash; a dependent child waits.
func TestCoordinationParentAdmitsIndependentChildren(t *testing.T) {
	p := coordinationPlan(t, false)
	g, fc := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonConfirmed))
	g.reconcile(t, p.ID, 2)
	if len(fc.files) == 0 {
		t.Fatal("a confirmed parent did not file")
	}
	parent, _ := g.coordTask(t, p.ID)
	parent.Status.Reason = coordination.ReasonFiled
	if err := g.c.Status().Update(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	tk := g.reconcile(t, p.ID, 2)
	cs := tk.Status.Coordination
	if cs.Children[0].Task == "" || cs.Children[1].Task != "" || cs.Children[1].Reason != reasonWaitingDependency {
		t.Fatalf("children %+v", cs.Children)
	}
	child, ok := g.coordTask(t, cs.Children[0].Task)
	c := p.Children[0]
	switch {
	case !ok:
		t.Fatal("no child task")
	case child.Spec.Repository != c.Repository || child.Spec.Source.Kind != "issue" || child.Spec.Source.RequestedBy != "github:smana" ||
		child.Spec.Source.ContentSHA256 != coordination.PlanHash(p) || child.Spec.Issue != 21:
		t.Fatalf("child spec %+v", child.Spec)
	case child.Labels[v1alpha1.LabelCoordinationParent] != p.ID || child.Labels[v1alpha1.LabelParent] != "" || child.Spec.Parent != nil || child.Spec.Split != nil:
		t.Fatalf("child lineage %v %+v", child.Labels, child.Spec.Parent)
	case child.Spec.Scope == nil || child.Spec.Scope.Task != c.Scope.Task || child.Spec.Template != "pair" || child.Spec.PredictedClass != "scripts" ||
		child.Spec.Budget.TaskMicroUSD != c.BudgetMicroUSD:
		t.Fatalf("child frozen fields %+v", child.Spec)
	}

	// The child follows the ordinary path from its own admission: no C7, scoper or split.
	ct := g.reconcile(t, child.Name, 3)
	if ct.Status.Classification == nil || ct.Status.Classification.Classifier != "coordination" || ct.Status.Coordination == nil ||
		ct.Status.Coordination.ChildID != c.ID || ct.Status.Coordination.AllocationMicroUSD != c.BudgetMicroUSD {
		t.Fatalf("child status %+v %+v", ct.Status.Classification, ct.Status.Coordination)
	}
	var room roomv1.Room
	if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: child.Name}, &room); err != nil || room.Spec.Repository != c.Repository {
		t.Fatalf("child room %+v %v", room.Spec, err)
	}
	if len(g.runs.specs) != 1 {
		t.Fatalf("%d runs, want the child's first implementer", len(g.runs.specs))
	}
	for _, s := range g.runs.specs {
		if s.TaskID != child.Name || s.Branch != "agent/"+child.Name || s.Repository != c.Repository || s.Role != "implementer" {
			t.Fatalf("run %+v", s)
		}
	}
}

// Review Focus 2: an authority lost only at a later admission or resume holds that child alone,
// with its bounded reason, before any claim; the independent sibling keeps going.
func TestAdmissionAndResumeRecheckRepositoryAuthority(t *testing.T) {
	for _, lost := range []error{coordination.ErrAccessUnverified, coordination.ErrRepositoryNotOwned,
		coordination.ErrRepositoryPermission, coordination.ErrPolicyChanged, coordination.ErrPrerequisites} {
		t.Run(lost.Error(), func(t *testing.T) {
			p := coordinationPlan(t, true)
			g, fc := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonFiled))
			cs := g.reconcile(t, p.ID, 2).Status.Coordination
			if cs.Children[0].Task == "" || cs.Children[1].Task == "" {
				t.Fatalf("independent children not both created: %+v", cs.Children)
			}
			fc.ready["consumer"] = fmt.Errorf("%w: lost since authorization", lost)
			contract := g.reconcile(t, cs.Children[0].Task, 3)
			consumer := g.reconcile(t, cs.Children[1].Task, 3)
			if contract.Status.Phase != v1alpha1.PhaseImplementing {
				t.Fatalf("the independent sibling stopped at %s/%s", contract.Status.Phase, contract.Status.Reason)
			}
			if consumer.Status.Phase != v1alpha1.PhaseReceived || consumer.Status.Reason != lost.Error() || consumer.Status.RoomRef != "" {
				t.Fatalf("consumer %s/%s room %q", consumer.Status.Phase, consumer.Status.Reason, consumer.Status.RoomRef)
			}
			// Admitted while it held, then lost before its next run: Queued re-reads it too.
			delete(fc.ready, "consumer")
			consumer = g.reconcile(t, cs.Children[1].Task, 2)
			if consumer.Status.Phase != v1alpha1.PhaseQueued {
				t.Fatalf("consumer reached %s", consumer.Status.Phase)
			}
			fc.ready["consumer"] = lost
			runs := len(g.runs.specs)
			consumer = g.reconcile(t, cs.Children[1].Task, 2)
			if consumer.Status.Phase != v1alpha1.PhaseQueued || consumer.Status.Reason != lost.Error() || len(g.runs.specs) != runs {
				t.Fatalf("resume past a lost authority: %s/%s, %d runs", consumer.Status.Phase, consumer.Status.Reason, len(g.runs.specs)-runs)
			}
		})
	}
}

// MR2: a child whose lineage is not one repository's, or not the coordination's alone, is refused
// before any claim; a Room of its name on another repository is never adopted.
func TestCoordinationChildLineage(t *testing.T) {
	p := coordinationPlan(t, true)
	for _, tc := range []struct {
		name   string
		edit   func(*v1alpha1.Task)
		objs   func(*v1alpha1.Task) []client.Object
		reason string
	}{
		{name: "a splitter label", edit: func(c *v1alpha1.Task) { c.Labels[v1alpha1.LabelParent] = "3buqdlot" }, reason: "coordination_lineage"},
		{name: "another parent's label", edit: func(c *v1alpha1.Task) { c.Labels[v1alpha1.LabelCoordinationParent] = "aaaaaaaa" }, reason: "coordination_lineage"},
		{name: "another repository", edit: func(c *v1alpha1.Task) { c.Spec.Repository = coordB }, reason: "cross_repository_lineage"},
		{name: "another issue", edit: func(c *v1alpha1.Task) { c.Spec.Source.Ref = coordA + "#99" }, reason: "cross_repository_lineage"},
		{name: "an edited snapshot", edit: func(c *v1alpha1.Task) { c.Spec.Scope.Task = "and the deploy key" }, reason: "coordination_scope_changed"},
		{name: "a Room on another repository", objs: func(c *v1alpha1.Task) []client.Object {
			return []client.Object{&roomv1.Room{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: "agent-system"},
				Spec: roomv1.RoomSpec{Owner: "system:factory", Driver: "system:factory", DataClass: "public", Repository: coordB}}}
		}, reason: "cross_repository_lineage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := filedParent(p, coordination.ReasonFiled)
			child := coordination.ChildTask("agent-system", cfg(), parent, p.Children[0], 21, 1)
			child.CreationTimestamp = metav1.NewTime(now)
			if tc.edit != nil {
				tc.edit(child)
			}
			objs := []client.Object{parent, child}
			if tc.objs != nil {
				objs = append(objs, tc.objs(child)...)
			}
			g, _ := coordinationRig(t, config.CoordinationExecute, objs...)
			ct := g.reconcile(t, child.Name, 3)
			if ct.Status.Phase != v1alpha1.PhaseRejected || ct.Status.Reason != tc.reason || len(g.runs.specs) != 0 {
				t.Fatalf("%s/%s, %d runs, want Rejected/%s", ct.Status.Phase, ct.Status.Reason, len(g.runs.specs), tc.reason)
			}
		})
	}
}
