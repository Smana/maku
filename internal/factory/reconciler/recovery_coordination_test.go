// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/runs"
)

// MR11: the parent persists exactly one proposal from its children's control state and keeps it
// while it stands; a reverted producer before final acceptance stops the final integration and
// proposes revise_plan; nothing reverts or merges the landed sibling.
func TestCoordinationRecoveryIsPersistedOnce(t *testing.T) {
	p := coordinationPlan(t, true)
	parent, kids := filedFamily(p)
	contract, consumer := kids[0], kids[1]
	merged := metav1.NewTime(now.Add(-time.Hour))
	contract.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, PullRequest: &v1alpha1.PullRequestRef{Number: 12,
		MergeCommitSHA: strings.Repeat("a", 40), MergedAt: &merged}}
	consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, Reason: "pr_closed", PullRequest: &v1alpha1.PullRequestRef{Number: 13}}
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
	cs := g.reconcile(t, p.ID, 1).Status.Coordination
	rec := cs.Recovery
	if rec == nil || rec.Code != coordination.RecoveryRetryChild || rec.ChildID != "consumer" || rec.PlanVersion != p.Version || !rec.CreatedAt.Equal(&metav1.Time{Time: now}) {
		t.Fatalf("recovery %+v", rec)
	}
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	if again := g.reconcile(t, p.ID, 1).Status.Coordination.Recovery; !again.CreatedAt.Equal(&rec.CreatedAt) || again.Code != rec.Code {
		t.Fatalf("a standing proposal changed: %+v", again)
	}

	// The producer is reverted while the family is incomplete: no final attempt, revise_plan.
	c, _ := g.coordTask(t, contract.Name)
	c.Status.Phase, c.Status.Reason = v1alpha1.PhaseReverted, "revert_requested"
	if err := g.c.Status().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	b, _ := g.coordTask(t, consumer.Name)
	b.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, PullRequest: &v1alpha1.PullRequestRef{Number: 13, MergeCommitSHA: strings.Repeat("b", 40), MergedAt: &merged}}
	if err := g.c.Status().Update(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	pt := g.reconcile(t, p.ID, 2)
	cs = pt.Status.Coordination
	if len(cs.IntegrationAttempts) != 0 || pt.Status.Phase != v1alpha1.PhaseCoordinating {
		t.Fatalf("a final integration over a reverted producer: %+v, %s", cs.IntegrationAttempts, pt.Status.Phase)
	}
	if cs.Recovery == nil || cs.Recovery.Code != coordination.RecoveryRevisePlan || cs.Recovery.ChildID != "contract" {
		t.Fatalf("recovery %+v", cs.Recovery)
	}
	if len(g.f.Reverts()) != 0 || len(g.f.Armed()) != 0 || len(g.runs.specs) != 0 {
		t.Fatalf("the parent acted: %d reverts, %d arms, %d runs", len(g.f.Reverts()), len(g.f.Armed()), len(g.runs.specs))
	}
}

// MR11: a parent's factory/stop pauses future family scheduling and integration releases; it never
// revokes a child already running in its own repository.
func TestParentStopPausesTheFamilyOnly(t *testing.T) {
	p := coordinationPlan(t, true)
	parent, kids := filedFamily(p)
	parent.Annotations = map[string]string{v1alpha1.AnnotationStop: "label"}
	parent.Status.Coordination.Children[0].Task = "" // not admitted yet
	consumer := kids[1]
	started := metav1.NewTime(now.Add(-time.Minute))
	run := runID(consumer.Name, 0)
	consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseImplementing, RoomRef: consumer.Name,
		Classification: &v1alpha1.Classification{Tier: "standard", Classifier: "coordination", Fallback: "static"},
		Runs:           []v1alpha1.RunRecord{{ID: run, Role: "implementer", Trigger: "initial", Phase: "Running", Started: &started}}}
	room := roomOf(consumer.Name)
	room.Spec.Repository = coordB
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, consumer, room)
	g.runs.runs[run] = runs.Run{ID: run, TaskID: consumer.Name, Role: "implementer", Phase: "Running", Created: started.Time}

	pt := g.reconcile(t, p.ID, 2)
	if pt.Status.Coordination.Children[0].Task != "" || pt.Status.Phase != v1alpha1.PhaseCoordinating || pt.Status.Reason != coordination.ReasonFiled {
		t.Fatalf("a stopped parent admitted or ended: %+v %s/%s", pt.Status.Coordination.Children[0], pt.Status.Phase, pt.Status.Reason)
	}
	b := g.reconcile(t, consumer.Name, 2)
	if b.Status.Phase != v1alpha1.PhaseImplementing || g.runs.patches[run][runs.AnnRevoked] != "" || b.Annotations[v1alpha1.AnnotationStop] != "" {
		t.Fatalf("the running child was revoked: %s/%s %v", b.Status.Phase, b.Status.Reason, g.runs.patches[run])
	}

	// A dependent's release is withheld while its parent is stopped.
	dp := coordinationPlan(t, false)
	dparent := filedParent(dp, coordination.ReasonFiled)
	dparent.Annotations = map[string]string{v1alpha1.AnnotationStop: "label"}
	dep := coordination.ChildTask("agent-system", cfg(), dparent, dp.Children[1], 22, 1)
	head := strings.Repeat("b", 40)
	dparent.Status.Coordination.Children[1].IntegrationRelease = &v1alpha1.IntegrationRelease{ChildID: "consumer", HeadSHA: head,
		IntegrationTask: "aaaaaaaa", ManifestHash: strings.Repeat("f", 64)}
	g2, _ := coordinationRig(t, config.CoordinationExecute, dparent, dep)
	gate, err := g2.r.integrationGate(t.Context(), dep, head)
	if err != nil || !gate.Required || gate.Accepted || gate.Reason != reasonCoordinationStopped {
		t.Fatalf("gate %+v %v", gate, err)
	}
}

// MR11: a child reverted after its parent is Done appends one bounded incident and its narration
// on the parent issue, through a conflict-retried status write; Done stays Done and no sibling is
// reverted.
func TestPostCompletionRevertIsOneIncident(t *testing.T) {
	p := coordinationPlan(t, true)
	parent, kids := filedFamily(p)
	parent.Status.Phase, parent.Status.Reason = v1alpha1.PhaseDone, reasonIntegrationPassed
	sha := strings.Repeat("a", 40)
	merged := metav1.NewTime(now.Add(-time.Hour))
	child := kids[0]
	child.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, PullRequest: &v1alpha1.PullRequestRef{Number: 12, MergeCommitSHA: sha, MergedAt: &merged}}
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, child, kids[1])
	for range 2 {
		g.r.tellCoordinationParent(t.Context(), child, 99)
	}
	pt, _ := g.coordTask(t, p.ID)
	cs := pt.Status.Coordination
	if pt.Status.Phase != v1alpha1.PhaseDone || pt.Status.Reason != reasonIntegrationPassed {
		t.Fatalf("the parent left Done: %s/%s", pt.Status.Phase, pt.Status.Reason)
	}
	if len(cs.Incidents) != 1 || cs.Incidents[0].ChildID != "contract" || cs.Incidents[0].MergeCommitSHA != sha || cs.Incidents[0].Reason != "reverted" {
		t.Fatalf("incidents %+v", cs.Incidents)
	}
	if len(pt.Status.Outbox) != 1 || pt.Status.Outbox[0].Number != 20 || !strings.Contains(pt.Status.Outbox[0].Body, "#99") {
		t.Fatalf("outbox %+v", pt.Status.Outbox)
	}
	if len(g.f.Reverts()) != 0 {
		t.Fatalf("%d sibling reverts", len(g.f.Reverts()))
	}
	// Bounded: the newest four.
	for i := range 6 {
		child.Status.PullRequest.MergeCommitSHA = strings.Repeat(string(rune('b'+i)), 40)
		g.r.tellCoordinationParent(t.Context(), child, 100+i)
	}
	if pt, _ = g.coordTask(t, p.ID); len(pt.Status.Coordination.Incidents) != v1alpha1.MaxCoordinationIncidents {
		t.Fatalf("%d incidents", len(pt.Status.Coordination.Incidents))
	}
	// Before Done, a revert records no incident: the parent proposes revise_plan instead.
	parent2, kids2 := filedFamily(p)
	g2, _ := coordinationRig(t, config.CoordinationExecute, parent2, kids2[0])
	g2.r.tellCoordinationParent(t.Context(), kids2[0], 99)
	if pt, _ = g2.coordTask(t, p.ID); len(pt.Status.Coordination.Incidents) != 0 {
		t.Fatalf("incident before Done: %+v", pt.Status.Coordination.Incidents)
	}
}
