// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/rooms"
)

// retainedTask is a task as a factory from before the plan and personas left it. The pins below are
// what that factory's factsKey gave it; recomputing them from today's code would prove nothing.
func retainedTask(phase, hash string) *v1alpha1.Task {
	tk := issueTask("3buqdlot", 7, "# title")
	started := metav1.NewTime(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	ended := metav1.NewTime(started.Add(time.Hour))
	tk.Spec.Template, tk.Spec.PredictedClass = "pair", "scripts"
	tk.Spec.Parent = &v1alpha1.Parent{Task: "p4zt6wce", Issue: 40, Index: 1, Of: 2}
	tk.Status = v1alpha1.TaskStatus{Phase: phase, PhaseSince: &ended, RoomRef: "3buqdlot", RoomSeq: 5,
		Facts: &v1alpha1.FactsLedger{Seq: 5, Hash: hash, Posted: true},
		Split: &v1alpha1.SplitStatus{State: SplitApproved, Of: 2},
		Runs: []v1alpha1.RunRecord{
			{ID: "aaaaaaaa", Role: "triager", Trigger: "initial", Phase: "Succeeded", Started: &started, Finished: &ended,
				Provenance: v1alpha1.RunProvenance{ServedModels: []string{"glm-5.3"}}},
			{ID: "bbbbbbbb", Role: "implementer", Trigger: "initial", Phase: "Succeeded", Started: &started, Finished: &ended,
				Provenance: v1alpha1.RunProvenance{FactoryVersion: "v1", ServedModels: []string{"kimi-k3"}}},
		}}
	return tk
}

// An upgrade must not rewrite the rooms of the tasks it retains: the rail's new fields are left out
// of an ended task's hash, so its ledger still matches and nothing is posted.
func TestAnUpgradeLeavesAnEndedTasksFactsAlone(t *testing.T) {
	for _, tc := range []struct {
		phase, pin string
		split      bool
	}{
		{v1alpha1.PhaseDone, "bf32e4e7d52a1a70", true},
		{v1alpha1.PhaseEscalated, "ebbc81f6439abc8a", false},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			tk := retainedTask(tc.phase, tc.pin)
			if !tc.split {
				tk.Status.Split = nil
			}
			g := newRig(t, tk)
			g.r.Cfg.Templates["pair"] = config.Template{Roles: []string{"implementer", "reviewer"}}
			if f := g.r.factsFor(tk); len(f.Plan) == 0 || f.Runs[0].Persona == "" || (f.Split != nil) != tc.split || f.Parent == nil {
				t.Fatalf("the fixture must carry the rail's fields: %+v", f)
			}
			if got := factsKey(g.r.factsFor(tk)); got != tc.pin {
				t.Fatalf("key %s, the factory before the upgrade left %s", got, tc.pin)
			}
			if err := g.r.postFacts(t.Context(), tk); err != nil {
				t.Fatal(err)
			}
			if len(g.log.factsSeqs) != 0 || tk.Status.RoomSeq != 5 {
				t.Fatalf("posted %v, roomSeq %d", g.log.factsSeqs, tk.Status.RoomSeq)
			}
		})
	}
}

// An escalated parent still waits on budget-family, and tellParent can revert a part under it while
// the phase stays Escalated: that must reach the room, or the rail keeps drawing the part as landed.
func TestAnEscalatedParentsRevertedPartMovesItsKey(t *testing.T) {
	tk := retainedTask(v1alpha1.PhaseEscalated, "")
	tk.Status.Split.Children = []v1alpha1.SplitChild{{Index: 1, Issue: 52, Task: "k2mqx7ba", Outcome: ChildLanded}}
	before := factsKey(factsOf(tk))
	tk.Status.Split.Children[0].Outcome = ChildReverted
	if factsKey(factsOf(tk)) == before {
		t.Fatal("a reverted part did not move an escalated parent's key")
	}
}

// The rail's fields still move a live task's key, and a task that ends posts them.
func TestTheRailsFactsReachALiveAndAnEndingTask(t *testing.T) {
	live := factsOf(awaiting())
	before := factsKey(live)
	live.Plan = []string{"implementer"}
	if factsKey(live) == before {
		t.Fatal("a live task's plan is not in its key")
	}
	tk := retainedTask(v1alpha1.PhaseDone, "the last phase's")
	g := newRig(t, tk)
	g.r.Cfg.Templates["pair"] = config.Template{Roles: []string{"implementer", "reviewer"}}
	if err := g.r.postFacts(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if f, ok := g.log.facts[6]; !ok || len(f.Plan) == 0 || f.Runs[0].Persona != "scoper" {
		t.Fatalf("posted %+v", g.log.facts)
	}
}

// A sealed room's log takes no event again (rooms.ErrSealed): its facts are settled, not waiting.
// Otherwise factsDue stays true and every later reconcile posts and logs for facts that cannot land.
func TestASealedRoomSettlesItsFacts(t *testing.T) {
	g := newRig(t, awaiting())
	tk := stored(t, g.c)
	g.log.failFacts = &rooms.APIError{Status: 410, Reason: "sealed"}
	if err := g.r.postFacts(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if g.r.factsDue(tk) || !tk.Status.Facts.Posted {
		t.Fatalf("due %v, ledger %+v", g.r.factsDue(tk), tk.Status.Facts)
	}
	if err := g.r.postFacts(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if len(g.log.factsSeqs) != 1 {
		t.Fatalf("posted %v: a sealed room is asked once per fact", g.log.factsSeqs)
	}
	// New facts ask once more, and the answer settles them too.
	tk.Status.Phase = v1alpha1.PhaseDone
	if err := g.r.postFacts(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if len(g.log.factsSeqs) != 2 || g.r.factsDue(tk) {
		t.Fatalf("posted %v, due %v", g.log.factsSeqs, g.r.factsDue(tk))
	}
}
