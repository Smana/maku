// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
)

// landedFamily is integrationFamily with the consumer merged at consumerMerge too.
func landedFamily(t *testing.T) (parent, contract, consumer *v1alpha1.Task) {
	t.Helper()
	parent, contract, consumer = integrationFamily(t)
	at := metav1.NewTime(now.Add(-time.Minute))
	consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: "merged",
		PullRequest: &v1alpha1.PullRequestRef{Number: 12, HeadSHA: consumerHead, MergeCommitSHA: consumerMerge, MergedAt: &at}}
	return parent, contract, consumer
}

// integrated writes integration Task name's typed result, as its run would have.
func (g *rig) integrated(t *testing.T, name, state, hash string) {
	t.Helper()
	it, ok := g.coordTask(t, name)
	if !ok {
		t.Fatalf("no integration task %s", name)
	}
	it.Status.Phase, it.Status.Reason = v1alpha1.PhaseDone, reasonIntegrationPassed
	it.Status.Result = &v1alpha1.IntegrationResult{State: state, ManifestHash: hash}
	if err := g.c.Status().Update(t.Context(), it); err != nil {
		t.Fatal(err)
	}
}

// notDone asserts the parent is still Coordinating, filed, with no acceptance, closed issue,
// merge or revert.
func notDone(t *testing.T, g *rig, p *v1alpha1.Task) {
	t.Helper()
	if p.Status.Phase != v1alpha1.PhaseCoordinating || p.Status.Reason != coordination.ReasonFiled || p.Status.Coordination.Acceptance != nil {
		t.Fatalf("parent %s/%s acceptance %+v", p.Status.Phase, p.Status.Reason, p.Status.Coordination.Acceptance)
	}
	if slices.Contains(g.f.Calls(), "close-issue 20") || len(g.f.Armed()) != 0 || len(g.f.Reverts()) != 0 {
		t.Fatalf("calls %v armed %v reverts %v", g.f.Calls(), g.f.Armed(), g.f.Reverts())
	}
}

// MR10, MR11: the parent is Done only after every child landed and the final integration passed
// for exactly their merge commits under the current plan. A merged producer beside a failed
// dependent is neither completed nor reverted; landed children without a final pass, a final pass
// over another SHA or an older plan, leave it Coordinating. The exact pass closes the parent
// issue, with no merge call of the parent's own.
func TestParentDoneOnlyAfterFinalIntegration(t *testing.T) {
	t.Run("A merged, B failed", func(t *testing.T) {
		parent, contract, consumer := integrationFamily(t)
		consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, Reason: "pr_closed"}
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
		p := g.reconcile(t, parent.Name, 4)
		notDone(t, g, p)
		if len(p.Status.Coordination.IntegrationAttempts) != 0 || !slices.Equal(p.Status.Coordination.MergeOrder, []string{"contract"}) {
			t.Fatalf("attempts %+v order %v", p.Status.Coordination.IntegrationAttempts, p.Status.Coordination.MergeOrder)
		}
		if c, _ := g.coordTask(t, contract.Name); c.Status.Phase != v1alpha1.PhaseDone || c.Status.PullRequest.MergeCommitSHA != producerMerg {
			t.Fatalf("the merged producer moved: %s", c.Status.Phase)
		}
	})

	t.Run("all landed, no final pass", func(t *testing.T) {
		parent, contract, consumer := landedFamily(t)
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
		p := g.reconcile(t, parent.Name, 3)
		notDone(t, g, p)
		as := p.Status.Coordination.IntegrationAttempts
		if len(as) != 1 || as[0].Purpose != coordination.PurposeFinal || as[0].State != coordination.ResultPending {
			t.Fatalf("attempts %+v", as)
		}
		for _, s := range as[0].Subjects {
			if s.HeadSHA != "" || (s.ChildID == "contract") != (s.MergeCommitSHA == producerMerg) {
				t.Fatalf("a final subject that is not a merge commit: %+v", s)
			}
		}
		g.integrated(t, as[0].Task, coordination.ResultFailed, as[0].ManifestHash)
		notDone(t, g, g.reconcile(t, parent.Name, 2))
	})

	t.Run("a final pass for another SHA or plan", func(t *testing.T) {
		for _, edit := range []func(m *coordination.IntegrationManifest){
			func(m *coordination.IntegrationManifest) { m.Subjects[0].MergeCommitSHA = strings.Repeat("f", 40) },
			func(m *coordination.IntegrationManifest) { m.PlanHash = strings.Repeat("e", 64) },
		} {
			parent, contract, consumer := landedFamily(t)
			cs := parent.Status.Coordination
			m, err := coordination.FinalManifest(parent.Name, cs, map[string]string{"contract": producerMerg, "consumer": consumerMerge})
			if err != nil {
				t.Fatal(err)
			}
			edit(&m)
			it := coordination.IntegrationTask("agent-system", cfg(), parent, m, 1)
			it.Spec.Integration.PlanHash = m.PlanHash
			hash := coordination.ManifestHash(m)
			it.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: reasonIntegrationPassed,
				Result: &v1alpha1.IntegrationResult{State: coordination.ResultPassed, ManifestHash: hash}}
			cs.IntegrationAttempts = []v1alpha1.IntegrationAttempt{{Task: it.Name, Purpose: coordination.PurposeFinal, PlanHash: m.PlanHash,
				ManifestHash: hash, State: coordination.ResultPending, Subjects: m.Subjects}}
			g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
			g.put(t, it)
			p := g.reconcile(t, parent.Name, 3)
			notDone(t, g, p)
			if p.Status.Coordination.IntegrationAttempts[0].State != coordination.ResultPassed {
				t.Fatalf("the pass is recorded as what it is: %+v", p.Status.Coordination.IntegrationAttempts[0])
			}
		}
	})

	t.Run("the exact final pass", func(t *testing.T) {
		parent, contract, consumer := landedFamily(t)
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
		a := g.reconcile(t, parent.Name, 2).Status.Coordination.IntegrationAttempts[0]
		g.integrated(t, a.Task, coordination.ResultPassed, a.ManifestHash)
		p := g.reconcile(t, parent.Name, 1)
		acc := p.Status.Coordination.Acceptance
		if p.Status.Phase != v1alpha1.PhaseDone || p.Status.Reason != reasonIntegrationPassed || acc == nil ||
			acc.IntegrationTask != a.Task || acc.ManifestHash != a.ManifestHash {
			t.Fatalf("parent %s/%s acceptance %+v", p.Status.Phase, p.Status.Reason, acc)
		}
		if !slices.Contains(g.f.Calls(), "close-issue 20") || len(g.f.Armed()) != 0 || len(g.f.Reverts()) != 0 {
			t.Fatalf("calls %v armed %v", g.f.Calls(), g.f.Armed())
		}
		if !slices.Equal(p.Status.Coordination.MergeOrder, []string{"contract", "consumer"}) {
			t.Fatalf("order %v", p.Status.Coordination.MergeOrder)
		}
	})
}
