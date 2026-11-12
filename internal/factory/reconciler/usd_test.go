// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"math"
	"slices"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/meter"
)

type fakeCosts map[string]meter.Cost

func (f fakeCosts) Get(id string) (meter.Cost, bool) { c, ok := f[id]; return c, ok }

// OD-6 (b) for the task, as #465 amended it: a pair task's runs, 95 % cache reads, pass 4.5 M
// tokens and stay far under the standard tier's $5.10, yet the 3 M token cap holds beside the
// dollar one. The same volume uncached is held by the dollars first. A run the meter cannot price
// holds the task to its 3 M tokens, said once.
func TestTaskUSDCapPricesCacheReads(t *testing.T) {
	glm := func(in, cached, out int64) int64 { // GLM-5.3 list, micro-USD
		return int64(math.Round(float64(in-cached)*1.40 + float64(cached)*0.26 + float64(out)*4.40))
	}
	type run struct {
		role, trigger   string
		in, cached, out int64
	}
	pair := []run{
		{"implementer", "initial", 1_631_496, 1_552_384, 30_304}, // the cohort's #2279
		{"reviewer", "review", 1_400_000, 1_316_000, 20_000},     // 94 % cached
		{"implementer", "human", 1_594_321, 1_507_328, 15_932},   // a revision the size of #2269
	}
	for name, c := range map[string]struct {
		uncached bool
		why      string
		started  bool
		want     []string
	}{
		"95 % cached: $1.78 of $5.10":    {false, "", false, []string{"revoked budget-task", "limit task-tokens"}},
		"the same volume uncached":       {true, "", false, []string{"revoked budget-task", "limit task-tokens"}},
		"an unpriced run: the token cap": {false, meter.FallbackUnpriced, false, []string{"fallback task unpriced", "revoked budget-task", "limit task-tokens"}},
	} {
		t.Run(name, func(t *testing.T) {
			tk := awaiting()
			tk.Spec.Template = "pair"
			tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000,
				RunMinutes: 45, RunMicroUSD: 2_550_000, TaskMicroUSD: 5_100_000}
			tk.Status.Phase, tk.Status.NextTrigger = v1alpha1.PhaseQueued, "human"
			costs := fakeCosts{}
			tk.Status.Runs = nil
			var tokens int64
			for i, r := range pair {
				cached := r.cached
				if c.uncached {
					cached = 0
				}
				tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(i), Role: r.role, Trigger: r.trigger,
					Phase: "Succeeded", Tokens: r.in + r.out, Started: awaiting().Status.Runs[0].Started})
				costs[rid(i)] = meter.Cost{MicroUSD: glm(r.in, cached, r.out)}
				tokens += r.in + r.out
			}
			if c.why != "" {
				costs[rid(1)] = meter.Cost{Why: c.why}
			}
			if tokens < 4_500_000 {
				t.Fatalf("the pair spends %d tokens, want 4.5 M or more", tokens)
			}
			g := newRig(t, tk, roomOf("3buqdlot"))
			g.f.SetPR(pr12())
			g.r.Cfg.Current().Budgets.EnforceTask = true
			g.r.Costs = costs
			got := g.reconcile(t, "3buqdlot", 1)
			if _, started := g.runs.specs[rid(3)]; started != c.started {
				t.Fatalf("the revision started %v, want %v: %s %s, cost %d µ$", started, c.started,
					got.Status.Phase, got.Status.Reason, got.Status.Usage.CostMicroUSD)
			}
			if !c.started && (got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task") {
				t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
			}
			for _, w := range c.want {
				if !slices.Contains(g.metrics.recorded, w) {
					t.Errorf("missing %q in %q", w, g.metrics.recorded)
				}
			}
			if n := countOf(g.metrics.recorded, "fallback task unpriced"); c.why != "" && n != 1 {
				t.Errorf("the fallback is said %d times, want once", n)
			}
		})
	}
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

// #465: task ozdjcnhn (standard tier, 3 M tokens, $5.10) reached 4,008,132 tokens at $1.47 and its
// reviewer still started: the dollar cap ruled alone. The token cap holds as well: no run starts
// without a whole run's tokens left, whatever the dollars say.
func TestTaskTokenCapHoldsWhileUnderTheUSDCap(t *testing.T) {
	tk := awaiting()
	tk.Spec.Template = "pair"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000,
		RunMinutes: 45, RunMicroUSD: 2_550_000, TaskMicroUSD: 5_100_000}
	tk.Status.Phase, tk.Status.NextTrigger = v1alpha1.PhaseQueued, "review"
	costs := fakeCosts{}
	tk.Status.Runs = nil
	for i, tokens := range []int64{2_004_066, 2_004_066} { // 4,008,132 together
		tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(i), Role: "implementer", Trigger: "initial",
			Phase: "Succeeded", Tokens: tokens, Started: awaiting().Status.Runs[0].Started})
		costs[rid(i)] = meter.Cost{MicroUSD: 735_000} // $1.47 in all
	}
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.f.SetPR(pr12())
	g.r.Cfg.Current().Budgets.EnforceTask = true
	g.r.Costs = costs
	got := g.reconcile(t, "3buqdlot", 1)
	if _, started := g.runs.specs[rid(2)]; started {
		t.Fatalf("a run started past the task's token cap: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task" {
		t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
	}
	if !slices.Contains(g.metrics.recorded, "limit task-tokens") {
		t.Errorf("the limit named is not the token cap: %q", g.metrics.recorded)
	}
}

// reviewBudgetTask is #475's task l5ufw4fz: standard tier (run 1.5 M, task 3.0 M tokens), queued
// for a reviewer after an implementer that took `used` tokens.
func reviewBudgetTask(used int64) *v1alpha1.Task {
	tk := awaiting()
	tk.Spec.Template = "pair"
	tk.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}
	tk.Status.Phase, tk.Status.NextTrigger, tk.Status.NextRole = v1alpha1.PhaseQueued, "review", "reviewer"
	tk.Status.Runs = []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Trigger: "initial", Phase: "Succeeded",
		Tokens: used, Started: awaiting().Status.Runs[0].Started}}
	tk.Status.Usage.Tokens = used
	return tk
}

// #475: the reviewer never ran because runFits wanted a whole run (1.5 M) for it. With 1.14 M left
// it starts, capped at what is left so the task cannot pass taskTokens.
func TestReviewerStartsWhenTheRemainderFitsAReview(t *testing.T) {
	g := newRig(t, reviewBudgetTask(1_858_756+1), roomOf("3buqdlot"))
	g.f.SetPR(pr12())
	g.r.Cfg.Current().Budgets.EnforceTask = true
	got := g.reconcile(t, "3buqdlot", 1)
	s, started := g.runs.specs[rid(1)]
	if !started {
		t.Fatalf("the reviewer did not start: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if s.Role != "reviewer" || s.MaxTokens != 1_141_243 {
		t.Errorf("role %s cap %d, want reviewer capped at the 1,141,243 left", s.Role, s.MaxTokens)
	}
}

// #475: with 100 k left no review fits (a quarter of a run is 375 k): the task escalates as before.
func TestReviewerRefusedWhenLessThanAReviewIsLeft(t *testing.T) {
	g := newRig(t, reviewBudgetTask(2_900_000), roomOf("3buqdlot"))
	g.f.SetPR(pr12())
	g.r.Cfg.Current().Budgets.EnforceTask = true
	got := g.reconcile(t, "3buqdlot", 1)
	if _, started := g.runs.specs[rid(1)]; started {
		t.Fatal("a reviewer started with 100 k left")
	}
	if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-task" {
		t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
	}
}

// An implementer still needs a whole run left.
func TestImplementerStillNeedsAWholeRun(t *testing.T) {
	tk := reviewBudgetTask(1_858_756)
	if runFitsFor(tk, "implementer") || !runFitsFor(tk, "reviewer") {
		t.Fatal("implementer must need a whole run, a reviewer a quarter of one")
	}
}

// #475: the overlap evaluation's scoper run is recorded but not charged to the task's caps.
func TestOverlapScoperIsNotChargedToTheTask(t *testing.T) {
	tk := reviewBudgetTask(1_000_000)
	tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(1), Role: "triager", Phase: "Succeeded", Tokens: 400_000, CostMicroUSD: 1})
	tk.Status.Usage.Tokens = 1_400_000
	tk.Status.Overlap = &v1alpha1.OverlapStatus{ScoperRun: rid(1)}
	if got := chargedTokens(tk); got != 1_000_000 {
		t.Fatalf("charged %d, want the implementer's 1,000,000 only", got)
	}
	if tk.Status.Usage.Tokens != 1_400_000 {
		t.Fatal("the evaluation's spend must stay in status.usage")
	}
	tk.Status.Overlap = nil // an ordinary scoper stays charged
	if got := chargedTokens(tk); got != 1_400_000 {
		t.Fatalf("an ordinary run was exempted: %d", got)
	}
}
