// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
)

// filedRig is a filing rig whose family is filed, with child Tasks recorded for both children.
func filedRig(t *testing.T) (*filingRig, v1alpha1.CoordinationPlan) {
	t.Helper()
	r := newFilingRig(t)
	p := draftPlan(t)
	r.confirmAll(t, p)
	if err := r.s.File(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.s.setStatus(context.Background(), p.ID, func(task *v1alpha1.Task) {
		for i := range task.Status.Coordination.Children {
			task.Status.Coordination.Children[i].Task = "c" + strings.Repeat("a", 6) + string(rune('a'+i))
		}
	}); err != nil {
		t.Fatal(err)
	}
	return r, p
}

// A material version of a filed parent keeps it filed (its children's lineage reads that), records
// one pending generation for the affected child alone, and reposts once with its changed sections
// and who reconfirms; replaying the draft reposts nothing more.
func TestDraftOfAFiledParentPausesOnlyTheAffectedChild(t *testing.T) {
	r, p := filedRig(t)
	ctx := context.Background()
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "consumer").Scope.Task = "Validate the consumer against the landed v1 schema, strictly."
	for range 2 {
		if _, err := r.s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
			t.Fatal(err)
		}
	}
	task := parentOf(t, r.s, p.ID)
	cs := task.Status.Coordination
	if task.Status.Reason != ReasonFiled || len(cs.PendingGenerations) != 1 || cs.PendingGenerations[0].ChildID != "consumer" ||
		cs.PendingGenerations[0].Generation != 2 || cs.PendingGenerations[0].PlanVersion != 2 {
		t.Fatalf("reason %s pending %+v", task.Status.Reason, cs.PendingGenerations)
	}
	if Complete(cs) || currentAuthorization(cs, "contract") == nil || currentAuthorization(cs, "consumer") != nil {
		t.Fatalf("authorizations %+v", cs.Authorizations)
	}
	comments := r.forges[repoA].Comments(task.Spec.Issue)
	if len(comments) != 2 || !strings.HasPrefix(comments[1], narrate.CoordinationRepostMarker(p.ID, 2, PlanHash(q))) ||
		!strings.Contains(comments[1], "children.consumer.scope.task") {
		t.Fatalf("reposts %d: %q", len(comments), comments)
	}
	// A confirmation under the new version leaves the reason filed too.
	if _, err := r.s.Confirm(ctx, smana, parentConfirm(q)); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(t, r.s, p.ID).Status.Reason; got != ReasonFiled {
		t.Fatalf("a confirmation rewrote the reason to %s", got)
	}
}

// Between a new version and its confirmations, the unaffected child stays ready on its standing
// authorization; the affected one and the whole family are refused.
func TestUnaffectedChildStaysReadyAcrossAnEdit(t *testing.T) {
	r, p := filedRig(t)
	ctx := context.Background()
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "consumer").Scope.Task = "Another scope."
	if _, err := r.s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
		t.Fatal(err)
	}
	cs := parentOf(t, r.s, p.ID).Status.Coordination
	if err := r.s.ExecuteReady(ctx, cs, []string{"contract"}); err != nil {
		t.Fatalf("the unaffected child is held: %v", err)
	}
	for _, ids := range [][]string{{"consumer"}, nil} {
		if err := r.s.ExecuteReady(ctx, cs, ids); !errors.Is(err, ErrInvalidConfirmation) {
			t.Fatalf("%v: err = %v", ids, err)
		}
	}
}

// Two writers race one parent: a confirmation of v1 read before a v2 draft lands loses on the
// resourceVersion, re-reads, and is refused stale_plan with nothing recorded and no child.
func TestConcurrentConfirmationLosesAsStalePlan(t *testing.T) {
	ctx := context.Background()
	p := draftPlan(t)
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "consumer").Scope.Task = "Another scope."
	var other *Service
	raced := false
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&v1alpha1.Task{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if task, ok := obj.(*v1alpha1.Task); ok && !raced && len(task.Status.Coordination.Confirmations) > 0 {
					raced = true // the draft's write lands between this confirmation's read and write
					if _, err := other.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s := &Service{Client: c, Namespace: ns, Cfg: testConfig(config.CoordinationPlan), Checks: newFakeChecks(), Now: func() time.Time { return at }}
	other = &Service{Client: c, Namespace: ns, Cfg: s.Cfg, Checks: s.Checks, Now: s.Now}
	mustDraft(t, s, p)
	_, err := s.Confirm(ctx, smana, parentConfirm(p))
	if !errors.Is(err, ErrStalePlan) || !raced {
		t.Fatalf("err = %v (raced %v), want stale_plan", err, raced)
	}
	task := parentOf(t, s, p.ID)
	if task.Status.Coordination.PlanVersion != 2 || len(task.Status.Coordination.Confirmations) != 0 {
		t.Fatalf("version %d, confirmations %+v", task.Status.Coordination.PlanVersion, task.Status.Coordination.Confirmations)
	}
	var l v1alpha1.TaskList
	if err := c.List(ctx, &l); err != nil || len(l.Items) != 1 {
		t.Fatalf("%d tasks", len(l.Items))
	}
}

// AcceptVersion never rewrites an entry already pending: a second edit keeps the stopping Task
// and the generation, under the newer version.
func TestAcceptVersionKeepsOnePendingGeneration(t *testing.T) {
	p := draftPlan(t)
	cs := &v1alpha1.CoordinationStatus{Plan: p.DeepCopy(), Children: []v1alpha1.CoordinationChildStatus{
		{ID: "consumer", Task: "c0nsumer", Generation: 1, Fingerprint: ChildFingerprint(p, "consumer")}}}
	for v := int32(2); v <= 3; v++ {
		q := *p.DeepCopy()
		q.Version = v
		child(t, &q, "consumer").Scope.Task = fmt.Sprintf("scope %d", v)
		AcceptVersion(cs, q, Materiality{})
	}
	if len(cs.PendingGenerations) != 1 || cs.PendingGenerations[0] != (v1alpha1.PendingGeneration{ChildID: "consumer", Generation: 2, PlanVersion: 3, StoppingTask: "c0nsumer"}) {
		t.Fatalf("pending %+v", cs.PendingGenerations)
	}
}
