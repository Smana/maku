// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
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
	tk.Status.Facts = &v1alpha1.FactsLedger{Seq: tk.Status.RoomSeq, Hash: factsKey(g.r.factsFor(tk)), Posted: true}
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
	g.r.Cfg.Facts.FinalSpend = true
	factsPosted(t, g)
	// One timed requeue, at the instant itself: not the poll interval, not a shorter one.
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil {
		t.Fatal(err)
	}
	at := g.r.settleWindow() + spendFinalMargin
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
	g.r.Cfg.Facts.FinalSpend = true
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
	g.r.Cfg.Facts.FinalSpend = true
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
