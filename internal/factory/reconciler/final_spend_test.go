// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/meter"
)

// TaskSpendFinal is CD23's one number: how long the dollars took to settle, from the last run's end
// to the pass that posted them.
func (m *fakeMetrics) TaskSpendFinal(_ context.Context, d time.Duration) {
	m.add(fmt.Sprintf("spend_final %.0f", d.Seconds()))
}

// aSettled is a Done task whose last run ended at end and whose usage has already settled: the
// state the final spend starts from, with a USD cap so its dollars have a limit to read against.
func aSettled(end time.Time, tokens int64) *v1alpha1.Task {
	tk := issueTask("3buqdlot", 7, "x")
	started := metav1.NewTime(end.Add(-2 * time.Hour))
	finished := metav1.NewTime(end)
	tk.Spec.Template, tk.Spec.PredictedClass = "solo", "review"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000,
		RunMinutes: 45, RunMicroUSD: 2_550_000, TaskMicroUSD: 5_100_000}
	tk.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, PhaseSince: &finished, RoomRef: "3buqdlot",
		RoomSeq: 1, UsageSettled: true,
		Runs: []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Trigger: "initial", Phase: "Succeeded",
			Tokens: tokens, Started: &started, Finished: &finished}}}
	return tk
}

// factsPosted records the facts the room already carries, as a pass before the final one left them.
func factsPosted(t *testing.T, g *rig) *v1alpha1.Task {
	t.Helper()
	tk := stored(t, g.c)
	tk.Status.Facts = &v1alpha1.FactsLedger{Seq: tk.Status.RoomSeq, Hash: factsKey(g.r.factsFor(g.r.Cfg.Current(), tk)), Posted: true}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

// finalFacts are the posted facts that claim the dollars are final.
func finalFacts(g *rig) []envelope.TaskFacts {
	var out []envelope.TaskFacts
	for _, f := range g.log.facts {
		if f.SpendFinal {
			out = append(out, f)
		}
	}
	return out
}

// CD23: a terminal task requeues exactly once, at its last run's end plus the settle window plus a
// margin, prices again, and posts one fact whose dollars it will not change.
func TestAFinalSpendLandsOnce(t *testing.T) {
	end := now
	g := newRig(t, aSettled(end, 0))
	g.r.Cfg.Current().Facts.FinalSpend = true
	factsPosted(t, g)
	// One timed requeue, at the instant itself: not the poll interval, not a shorter one.
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil {
		t.Fatal(err)
	}
	at := g.r.settleWindow(g.r.Cfg.Current()) + spendFinalMargin
	if res.RequeueAfter != at || at > 3*time.Minute {
		t.Fatalf("requeue %s, want one wake-up at %s", res.RequeueAfter, at)
	}
	// The meter priced the run after the last tick that held it (meter.Costs keeps it for 15
	// minutes): the final re-price reads that figure.
	g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}}
	g.r.Now = func() time.Time { return end.Add(3 * time.Minute) }
	res, err = g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("a second requeue: %s", res.RequeueAfter)
	}
	if tk := stored(t, g.c); tk.Status.Facts == nil || tk.Status.Usage.CostMicroUSD != 420_000 {
		t.Fatalf("status %+v", tk.Status)
	}
	got := finalFacts(g)
	if len(got) != 1 {
		t.Fatalf("final facts %+v, all %+v", got, g.log.facts)
	}
	if sp := got[0].Spend; sp == nil || sp.UsedMicroUSD != 420_000 || sp.LimitMicroUSD != 5_100_000 {
		t.Fatalf("%+v", got[0])
	}
	if want := "spend_final 180"; fmt.Sprint(g.metrics.recorded) != "["+want+"]" {
		t.Fatalf("%q, want one %q", g.metrics.recorded, want)
	}
	// At rest: the fact it posted is the one the room has, so nothing posts and nothing requeues.
	g.r.Now = func() time.Time { return end.Add(10 * time.Minute) }
	if res, err = g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	if posted := finalFacts(g); len(posted) != 1 || len(g.log.factsSeqs) != 1 {
		t.Fatalf("a second final fact: %d posted, calls %v", len(posted), g.log.factsSeqs)
	}
}

// CD23: postFacts persists the fact's hash before the room takes it, so a post the room refused
// leaves the final figure in the ledger with Posted false, and a hash test alone would lose the
// settle it measures. The retry that lands the fact counts it, once.
func TestAFinalSpendTheRoomRefusedIsCountedOnItsRetry(t *testing.T) {
	end := now
	g := newRig(t, aSettled(end, 0))
	g.r.Cfg.Current().Facts.FinalSpend = true
	factsPosted(t, g)
	g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}}
	g.r.Now = func() time.Time { return end.Add(3 * time.Minute) }
	g.log.failFacts = errors.New("the room is unreachable")
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil {
		t.Fatal("a post the room refused is returned for a retry")
	}
	if tk := stored(t, g.c); tk.Status.Facts == nil || tk.Status.Facts.Posted || tk.Status.Facts.Hash == "" {
		t.Fatalf("the ledger records the final fact before the room took it: %+v", tk.Status.Facts)
	}
	if len(g.metrics.recorded) != 0 {
		t.Fatalf("a post that never landed was counted: %q", g.metrics.recorded)
	}
	g.log.failFacts = nil
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	want := []string{"spend_final 180"}
	if !slices.Equal(g.metrics.recorded, want) || len(finalFacts(g)) != 1 {
		t.Fatalf("metrics %q, facts %+v, want %q and one fact", g.metrics.recorded, finalFacts(g), want)
	}
	// The reconcile after the retry posts nothing, and counts nothing.
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	if !slices.Equal(g.metrics.recorded, want) {
		t.Fatalf("counted twice: %q", g.metrics.recorded)
	}
}

// The same fact, lost on the write that records Posted: the room has it, the ledger does not, and
// the replay counts the settle once.
func TestAFinalSpendsConflictedWriteCountsItOnce(t *testing.T) {
	end := now
	armed := false
	c := fake.NewClientBuilder().WithScheme(scheme()).WithStatusSubresource(&v1alpha1.Task{}, &roomv1.Room{}).
		WithObjects(aSettled(end, 0)).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if tk, ok := o.(*v1alpha1.Task); ok && armed && tk.Status.Facts != nil && tk.Status.Facts.Seq == 2 && tk.Status.Facts.Posted {
				armed = false
				return apierrors.NewConflict(schema.GroupResource{Resource: "tasks"}, o.GetName(), errors.New("stale"))
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}}).Build()
	g := newRig(t)
	g.c, g.r.Client = c, c
	seeingMeter(t, c)
	g.r.Cfg.Current().Facts.FinalSpend = true
	factsPosted(t, g) // the room's ledger before the final fact: seq 1, posted
	g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}}
	g.r.Now = func() time.Time { return end.Add(3 * time.Minute) }
	armed = true
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter == 0 {
		t.Fatal("the lost write is returned")
	}
	if tk := stored(t, g.c); tk.Status.Facts.Posted || tk.Status.Facts.Seq != 2 {
		t.Fatalf("the room has the fact, the ledger records it unposted: %+v", tk.Status.Facts)
	}
	if len(g.metrics.recorded) != 0 || len(g.log.factsSeqs) != 1 {
		t.Fatalf("metrics %q, posts %v: one post, nothing counted yet", g.metrics.recorded, g.log.factsSeqs)
	}
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	if want := []string{"spend_final 180"}; !slices.Equal(g.metrics.recorded, want) {
		t.Fatalf("metrics %q, want %q", g.metrics.recorded, want)
	}
	if tk := stored(t, g.c); !tk.Status.Facts.Posted || len(finalFacts(g)) != 1 {
		t.Fatalf("ledger %+v, facts %+v", tk.Status.Facts, finalFacts(g))
	}
}

// The switch is off by default: a terminal task's dollars are not re-priced and nothing is claimed.
func TestWithoutTheSwitchAFinalSpendNeverLands(t *testing.T) {
	end := now
	g := newRig(t, aSettled(end, 0))
	factsPosted(t, g)
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}}
	g.r.Now = func() time.Time { return end.Add(10 * time.Minute) }
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	if posted := finalFacts(g); len(posted) != 0 || len(g.metrics.recorded) != 0 {
		t.Fatalf("facts %+v, metrics %q", posted, g.metrics.recorded)
	}
}

// Review Focus 6: a task that ended without a run has nothing to settle.
func TestATaskWithoutRunsNeverRequeuesForSpend(t *testing.T) {
	tk := aSettled(now, 0)
	tk.Status.Runs = nil
	g := newRig(t, tk)
	g.r.Cfg.Current().Facts.FinalSpend = true
	g.r.Costs = fakeCosts{}
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	g.r.Now = func() time.Time { return now.Add(10 * time.Minute) }
	if res, err = g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	if posted := finalFacts(g); len(posted) != 0 || len(g.metrics.recorded) != 0 {
		t.Fatalf("facts %+v, metrics %q", posted, g.metrics.recorded)
	}
}

// A cost entry the meter could only say Why about is unpriced, not $0: the final re-price leaves
// the run marked unpriced and claims no final figure for the task (CD23).
func TestAWhyOnlyCostIsNeverAFinalFigure(t *testing.T) {
	end := now
	g := newRig(t, aSettled(end, 0))
	g.r.Cfg.Current().Facts.FinalSpend = true
	factsPosted(t, g)
	g.r.Costs = fakeCosts{rid(0): {Why: meter.FallbackUnpriced}}
	g.r.Now = func() time.Time { return end.Add(3 * time.Minute) }
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
	tk := stored(t, g.c)
	if tk.Status.Runs[0].Unpriced != meter.FallbackUnpriced || tk.Status.Runs[0].CostMicroUSD != 0 {
		t.Fatalf("the run reads as settled dollars: %+v", tk.Status.Runs[0])
	}
	if usdCapped(tk) {
		t.Fatal("a task with an unpriced run is held to its tokens, never to a $0 cost")
	}
	if posted := finalFacts(g); len(posted) != 0 || len(g.log.factsSeqs) != 0 {
		t.Fatalf("final facts %+v, calls %v", posted, g.log.factsSeqs)
	}
	// The task falls back to its token cap, said once — and no final spend is timed.
	if want := []string{"fallback task " + meter.FallbackUnpriced}; !slices.Equal(g.metrics.recorded, want) {
		t.Fatalf("metrics %q, want %q", g.metrics.recorded, want)
	}
	// It stays at rest: an unclaimable figure is not a wake-up every second.
	g.r.Now = func() time.Time { return end.Add(20 * time.Minute) }
	if res, err = g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
		t.Fatalf("requeue %s, %v", res.RequeueAfter, err)
	}
}

// The facts key step (10 % of the cap) keeps a live figure quiet, and never a final one: the final
// figure is a claim about money, and a claim is exact.
func TestAFinalFiguresKeyIsNotBucketed(t *testing.T) {
	key := func(micro int64, final bool) string {
		return factsKey(envelope.TaskFacts{Phase: v1alpha1.PhaseDone, SpendFinal: final,
			Spend: &envelope.SpendFact{UsedMicroUSD: micro, LimitMicroUSD: 5_100_000}})
	}
	if key(420_000, false) != key(460_000, false) {
		t.Fatal("a live figure's key still steps with 10 % of the cap")
	}
	if key(420_000, true) == key(460_000, true) {
		t.Fatal("a final figure's key carries the exact dollars (CD23)")
	}
}

// aEnded is aSettled in the phase a task that never merged ends in: no pull request, so no revert
// window keeps its log open — the close is decided by the settle and the final spend alone.
func aEnded(phase string, end time.Time) *v1alpha1.Task {
	tk := aSettled(end, 0)
	tk.Status.Phase = phase
	return tk
}

// CD23: the final spend is a fact the room must still take, so the close waits for its instant — a
// failed or stopped task has no revert window to keep the log open, and closing at the settle window
// would seal it one minute before the fact is due (the post is then refused for good). Past the instant
// the room closes as before, so a task whose runs the meter could not price — whose final fact is
// withheld — is not held open for a figure that can never come.
func TestTheRoomStaysOpenForTheFinalSpend(t *testing.T) {
	for _, phase := range []string{v1alpha1.PhaseRejected, v1alpha1.PhaseStopped} {
		t.Run(phase, func(t *testing.T) {
			end := now
			g := newRig(t, aEnded(phase, end))
			g.r.Cfg.Current().Facts.FinalSpend = true
			factsPosted(t, g)
			withRoom(t, g)
			// The pass that settles the usage would close the room; the instant is still ahead of it.
			g.r.Now = func() time.Time { return end.Add(g.r.settleWindow(g.r.Cfg.Current())) }
			res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
			if err != nil {
				t.Fatal(err)
			}
			if roomGone(t, g) {
				t.Fatal("the room closed before the final spend was due: its log would refuse the fact")
			}
			if res.RequeueAfter <= 0 || res.RequeueAfter > spendFinalMargin {
				t.Fatalf("requeue %s, want the instant, %s away", res.RequeueAfter, spendFinalMargin)
			}
			// The instant: the run is priced again, the final fact lands, and the room closes after it.
			g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}}
			g.r.Now = func() time.Time { return end.Add(g.r.settleWindow(g.r.Cfg.Current()) + spendFinalMargin) }
			g.reconcile(t, "3buqdlot", 2)
			got := finalFacts(g)
			if len(got) != 1 || got[0].Spend == nil || got[0].Spend.UsedMicroUSD != 420_000 {
				t.Fatalf("final facts %+v, all %+v", got, g.log.facts)
			}
			if !roomGone(t, g) {
				t.Fatal("the room stays open past the instant")
			}
		})
	}
	// An unpriced run: no final fact is coming, and the wait is the instant alone — the room closes at
	// it rather than being retained for a figure the meter cannot claim.
	t.Run("unpriced", func(t *testing.T) {
		end := now
		g := newRig(t, aSettled(end, 1_500)) // tokens, no dollars: the meter priced none of them
		g.r.Cfg.Current().Facts.FinalSpend = true
		factsPosted(t, g)
		withRoom(t, g)
		g.r.Now = func() time.Time { return end.Add(g.r.settleWindow(g.r.Cfg.Current()) + spendFinalMargin) }
		g.reconcile(t, "3buqdlot", 1)
		if posted := finalFacts(g); len(posted) != 0 {
			t.Fatalf("an unpriced task claimed a final figure: %+v", posted)
		}
		if !roomGone(t, g) {
			t.Fatal("the room is held for a final figure that cannot come")
		}
	})
}
