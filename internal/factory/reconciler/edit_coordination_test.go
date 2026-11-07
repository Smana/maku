// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/taskid"
)

// acceptVersion is the service's acceptance of q on parent, as Draft records it: the new plan,
// its hash and history, the authorizations materiality keeps, then AcceptVersion.
func acceptVersion(t *testing.T, parent *v1alpha1.Task, q v1alpha1.CoordinationPlan) coordination.Materiality {
	t.Helper()
	cs := parent.Status.Coordination
	m, err := coordination.ComparePlans(*cs.Plan, q)
	if err != nil {
		t.Fatal(err)
	}
	h := coordination.PlanHash(q)
	cs.Plan, cs.PlanHash, cs.PlanVersion = q.DeepCopy(), h, q.Version
	cs.History = append(cs.History, v1alpha1.CoordinationPlanVersion{Version: q.Version, PlanHash: h, AcceptedAt: metav1.NewTime(now)})
	cs.Authorizations = coordination.AuthorizationsFor(q, cs.Authorizations, m)
	coordination.AcceptVersion(cs, q, m)
	return m
}

// confirmAll adds the parent's confirmation of the current plan and every missing authorization.
func confirmAll(parent *v1alpha1.Task) {
	cs := parent.Status.Coordination
	cs.Confirmations = append(cs.Confirmations, v1alpha1.ParentConfirmation{PlanHash: cs.PlanHash, Actor: "zitadel:s", LinkedGitHubLogin: "Smana", ConfirmedAt: metav1.NewTime(now)})
	for _, c := range cs.Plan.Children {
		fp := coordination.ChildFingerprint(*cs.Plan, c.ID)
		cs.Authorizations = append(cs.Authorizations, v1alpha1.ChildAuthorization{ChildID: c.ID, ChildFingerprint: fp, PlanVersion: cs.Plan.Version,
			Actor: "zitadel:" + c.ID, Repository: c.Repository, Permission: "write", FenceSHA: strings.Repeat("0", 40), ConfirmedAt: metav1.NewTime(now)})
	}
}

// filedFamily is p's filed parent at version 1 of its history, with each child's generation-1
// Task created and recorded.
func filedFamily(p v1alpha1.CoordinationPlan) (*v1alpha1.Task, []*v1alpha1.Task) {
	parent := filedParent(p, coordination.ReasonFiled)
	parent.Status.RoomRef = p.ID
	cs := parent.Status.Coordination
	cs.History = []v1alpha1.CoordinationPlanVersion{{Version: p.Version, PlanHash: cs.PlanHash, AcceptedAt: metav1.NewTime(now)}}
	var kids []*v1alpha1.Task
	for i, c := range p.Children {
		k := coordination.ChildTask("agent-system", cfg(), parent, c, 21+i, 1)
		k.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
		cs.Children[i].Task = k.Name
		kids = append(kids, k)
	}
	return parent, kids
}

// reservedLedger is a day ledger holding a reservation for run id.
func reservedLedger(day time.Time, id string, maxMicroUSD int64) *corev1.ConfigMap {
	b, _ := json.Marshal(ledger.Reservation{Principal: runs.PrincipalFactory, MaxMicroUSD: maxMicroUSD})
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ledger.Prefix + day.UTC().Format(ledger.DayLayout), Namespace: "agent-system"},
		Data: map[string]string{ledger.Reserved + id: string(b)}}
}

// Review Focus 5: a material edit to B while unaffected A runs and B has a live run whose
// reservation is outstanding. B's releases and future scheduling stop at acceptance, its old Task
// stops as superseded through the ordinary stop, and no replacement exists until that Task ended,
// its run is gone, its reservation released, its usage settled and the new version confirmed; the
// replacement is generation 2 of the new version holding only what generation 1 left. A keeps its
// Task and fingerprint and starts its next run throughout.
func TestMaterialEditSerializesOnlyAffectedGenerations(t *testing.T) {
	p := coordinationPlan(t, true)
	parent, kids := filedFamily(p)
	contract, consumer := kids[0], kids[1]
	contract.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseTriaged, Classification: &v1alpha1.Classification{Tier: "standard", Classifier: "coordination", Fallback: "static"}}
	started := metav1.NewTime(now.Add(-time.Hour))
	bRun := runID(consumer.Name, 0)
	consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseImplementing, RoomRef: consumer.Name,
		Classification: &v1alpha1.Classification{Tier: "standard", Classifier: "coordination", Fallback: "static"},
		Runs:           []v1alpha1.RunRecord{{ID: bRun, Role: "implementer", Trigger: "initial", Phase: "Running", CostMicroUSD: 700_000, Tokens: 1, Started: &started}}}
	for i := range parent.Status.Coordination.Children {
		st := &parent.Status.Coordination.Children[i]
		st.IntegrationRelease = &v1alpha1.IntegrationRelease{ChildID: st.ID, HeadSHA: strings.Repeat("e", 40), IntegrationTask: "aaaaaaaa", ManifestHash: strings.Repeat("f", 64)}
	}
	yesterday := now.Add(-24 * time.Hour)
	roomB := roomOf(consumer.Name)
	roomB.Spec.Repository = coordB
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer, roomB, reservedLedger(yesterday, bRun, runReserve))
	g.runs.runs[bRun] = runs.Run{ID: bRun, TaskID: consumer.Name, Role: "implementer", Phase: "Running", Created: started.Time}

	// v2 edits B's scope only: A's fingerprint is preserved.
	q := *p.DeepCopy()
	q.Version++
	q.Children[1].Scope.Task = "Validate the consumer against the landed v1 schema, strictly."
	pt, _ := g.coordTask(t, p.ID)
	acceptVersion(t, pt, q)
	if err := g.c.Status().Update(t.Context(), pt); err != nil {
		t.Fatal(err)
	}
	cs := pt.Status.Coordination
	pg := coordination.Pending(cs, "consumer")
	switch {
	case pg == nil || pg.Generation != 2 || pg.StoppingTask != consumer.Name || pg.PlanVersion != q.Version:
		t.Fatalf("pending %+v", cs.PendingGenerations)
	case cs.Children[1].IntegrationRelease != nil || cs.Children[1].Reason != coordination.ReasonGenerationPending:
		t.Fatalf("B's release survived the edit: %+v", cs.Children[1])
	case cs.Children[0].Task != contract.Name || cs.Children[0].Fingerprint != coordination.ChildFingerprint(q, "contract") || coordination.Pending(cs, "contract") != nil:
		t.Fatalf("A was touched: %+v", cs.Children[0])
	}

	// The parent stops B through the ordinary machinery; nothing replaces it yet.
	g.reconcile(t, p.ID, 1)
	if b, _ := g.coordTask(t, consumer.Name); b.Annotations[v1alpha1.AnnotationStop] != "superseded" {
		t.Fatalf("B not superseded: %v", b.Annotations)
	}
	b := g.reconcile(t, consumer.Name, 1)
	if b.Status.Phase != v1alpha1.PhaseStopped || b.Status.Reason != "superseded" || g.runs.patches[bRun][runs.AnnRevoked] != "manual" {
		t.Fatalf("B %s/%s, run patches %v", b.Status.Phase, b.Status.Reason, g.runs.patches[bRun])
	}
	// A, unaffected, keeps its Task and starts its next run before v2 is even confirmed.
	if a := g.reconcile(t, contract.Name, 2); a.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("A held at %s/%s", a.Status.Phase, a.Status.Reason)
	}
	gen2 := taskid.Name(taskid.CoordinationChildKey(p.ID, "consumer", int(q.Version), 2))
	noReplacement := func(why string) {
		t.Helper()
		if _, ok := g.coordTask(t, gen2); ok {
			t.Fatalf("a replacement started %s", why)
		}
	}
	g.reconcile(t, p.ID, 2)
	noReplacement("before the new version was confirmed")

	pt, _ = g.coordTask(t, p.ID)
	confirmAll(pt)
	if err := g.c.Status().Update(t.Context(), pt); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, p.ID, 2)
	noReplacement("while the old generation's reservation is outstanding and its usage unsettled")
	if pt, _ = g.coordTask(t, p.ID); pt.Status.Coordination.Children[1].Reason != coordination.ReasonGenerationPending {
		t.Fatalf("B's entry %+v", pt.Status.Coordination.Children[1])
	}

	// The meter's sweep releases yesterday's reservation; the usage still has to settle.
	if err := g.c.Delete(t.Context(), reservedLedger(yesterday, bRun, 0)); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, p.ID, 1)
	noReplacement("before the old generation's usage settled")
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	if b = g.reconcile(t, consumer.Name, 1); !b.Status.UsageSettled {
		t.Fatal("B never settled")
	}
	g.reconcile(t, p.ID, 2)
	rep, ok := g.coordTask(t, gen2)
	if !ok {
		t.Fatal("no replacement after the old generation drained")
	}
	left := p.Children[1].BudgetMicroUSD - coordination.AllocationSpent(b)
	pt, _ = g.coordTask(t, p.ID)
	st := pt.Status.Coordination.Children[1]
	switch {
	case rep.Spec.CoordinationParent.PlanVersion != q.Version || rep.Spec.Scope.Task != q.Children[1].Scope.Task || rep.Spec.Issue != 22:
		t.Fatalf("replacement spec %+v", rep.Spec)
	case rep.Spec.Budget.TaskMicroUSD != left || st.AllocationMicroUSD != left:
		t.Fatalf("replacement allocation %d (entry %d), want the %d generation 1 left", rep.Spec.Budget.TaskMicroUSD, st.AllocationMicroUSD, left)
	case st.Task != gen2 || st.Generation != 2 || st.Fingerprint != coordination.ChildFingerprint(q, "consumer") || len(pt.Status.Coordination.PendingGenerations) != 0:
		t.Fatalf("B's entry %+v, pending %+v", st, pt.Status.Coordination.PendingGenerations)
	case pt.Status.Coordination.Children[0].Task != contract.Name:
		t.Fatalf("A replaced: %+v", pt.Status.Coordination.Children[0])
	}
	// The replacement is the current generation: it is admitted; the old one never runs again.
	if r := g.reconcile(t, gen2, 1); r.Status.Phase != v1alpha1.PhaseTriaged {
		t.Fatalf("replacement %s/%s", r.Status.Phase, r.Status.Reason)
	}
}

// A produced artifact's change reaches its consumer through the fingerprint: both generations are
// replaced. An integration change keeps every Task but drops every release. A safe total decrease
// touches no child.
func TestMaterialEditAffectsByFingerprint(t *testing.T) {
	p := coordinationPlan(t, false)
	for _, tc := range []struct {
		name    string
		edit    func(*v1alpha1.CoordinationPlan)
		pending []string
		kept    bool // every release survives
	}{
		{name: "a produced artifact", edit: func(q *v1alpha1.CoordinationPlan) { q.Children[0].Produces[0].MaxBytes = 4096 },
			pending: []string{"contract", "consumer"}},
		{name: "integration acceptance", edit: func(q *v1alpha1.CoordinationPlan) {
			q.Integration.Acceptance[0].Proves = "The exact heads, then the merges, still work"
		}},
		{name: "a safe total decrease", edit: func(q *v1alpha1.CoordinationPlan) {
			q.Budget.PlanningMicroUSD -= 100_000
			q.Budget.TotalMicroUSD -= 100_000
		}, kept: true},
		{name: "a title", edit: func(q *v1alpha1.CoordinationPlan) { q.Title = "Consume the v1 schema" }, kept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, kids := filedFamily(p)
			for i := range parent.Status.Coordination.Children {
				parent.Status.Coordination.Children[i].IntegrationRelease = &v1alpha1.IntegrationRelease{ChildID: kids[i].Name}
			}
			q := *p.DeepCopy()
			q.Version++
			tc.edit(&q)
			acceptVersion(t, parent, q)
			cs := parent.Status.Coordination
			var pending []string
			for _, pg := range cs.PendingGenerations {
				pending = append(pending, pg.ChildID)
			}
			if strings.Join(pending, ",") != strings.Join(tc.pending, ",") {
				t.Fatalf("pending %v, want %v", pending, tc.pending)
			}
			for i, st := range cs.Children {
				if (st.IntegrationRelease != nil) != tc.kept {
					t.Fatalf("%s release %+v", st.ID, st.IntegrationRelease)
				}
				if st.Task != kids[i].Name {
					t.Fatalf("%s's Task replaced in place", st.ID)
				}
			}
		})
	}
}

// The old generation's lineage holds no more once a pending generation names it, and a Task of a
// fingerprint the plan no longer has is never the current generation.
func TestSupersededGenerationNeverRunsAgain(t *testing.T) {
	p := coordinationPlan(t, true)
	parent, kids := filedFamily(p)
	q := *p.DeepCopy()
	q.Version++
	q.Children[1].Scope.Task = "Another scope."
	acceptVersion(t, parent, q)
	old := kids[1]
	old.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseQueued, RoomRef: old.Name,
		Classification: &v1alpha1.Classification{Tier: "standard", Classifier: "coordination", Fallback: "static"}}
	g, _ := coordinationRig(t, config.CoordinationExecute, []client.Object{parent, old}...)
	if tk := g.reconcile(t, old.Name, 1); tk.Status.Phase != v1alpha1.PhaseStopped || tk.Status.Reason != "superseded" || len(g.runs.specs) != 0 {
		t.Fatalf("old generation %s/%s, %d runs", tk.Status.Phase, tk.Status.Reason, len(g.runs.specs))
	}
}
