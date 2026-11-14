// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
)

type typed map[string]Usage

func (s typed) RunUsage(context.Context) (map[string]Usage, error) { return s, nil }

// GLM-5.3's list prices, the v2 cohort's (cloud-native-ref scripts/ops/github/cohort-row.py).
var glm = map[string]config.Price{"agent-default": {Input: 1.40, CacheRead: 0.26, Output: 4.40}}

// The v2 cohort's four arm-A implementer runs that the 1.5 M token cap revoked (#2279, #2277,
// #2278, #2269): 91–96 % of their input was cache reads.
var cohort = map[string]Usage{
	"#2279": {Input: 1_631_496, CacheRead: 1_552_384, Output: 30_304},
	"#2277": {Input: 1_541_445, CacheRead: 1_469_248, Output: 48_755},
	"#2278": {Input: 1_690_907, CacheRead: 1_619_776, Output: 27_722},
	"#2269": {Input: 1_594_321, CacheRead: 1_507_328, Output: 15_932},
}

func typedUsage(u Usage) Usage {
	u.Tokens, u.Typed = u.Input+u.Output, true
	return u
}

func standardRun(id string) runs.Run {
	return runs.Run{ID: id, Phase: "Running", Tier: "standard", Model: "agent-default", MaxTokens: 1_500_000}
}

type revocation struct{ reason, limit string }

func usdMeter(st *store, src Source, revoked *[]revocation, fell *[]string) *Meter {
	return &Meter{Runs: st, Source: src, Cfg: priced(glm, map[string]float64{"standard": config.RefUSD(1_500_000)}, config.Budgets{}),
		OnRevoke: func(_ context.Context, r, l string) {
			*revoked = append(*revoked, revocation{r, l})
		},
		OnFallback: func(_ context.Context, why string) { *fell = append(*fell, why) }}
}

// J-AC1 under OD-6 (b): the standard tier's default cap is $2.55, and none of the four cohort runs
// reaches it, though each is past 1.5 M tokens. #465 made the token cap a ceiling beside the
// dollar one, so each is still revoked at 1.5 M, though its cost stays far under $2.55.
func TestCohortRunsAreRevokedOnTheTokenCapUnderTheUSDCap(t *testing.T) {
	if c := config.RefUSD(1_500_000); c != 2.55 {
		t.Fatalf("the standard tier's default cap is $%v, want $2.55", c)
	}
	for issue, u := range cohort {
		t.Run(issue, func(t *testing.T) {
			usd := float64(cost(glm["agent-default"], typedUsage(u))) / 1e6
			if usd < 0.58 || usd > 0.70 { // the cohort's $0.58–0.70, or 0.42 M reference tokens at most
				t.Errorf("cost $%.4f, want $0.58–0.70", usd)
			}
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
			var revoked []revocation
			var fell []string
			if err := usdMeter(st, typed{"aaaaaaaa": typedUsage(u)}, &revoked, &fell).Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			p := st.patches["aaaaaaaa"]
			if p[runs.AnnRevoked] != ReasonBudgetRun || len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunTokens}) || len(fell) != 0 {
				t.Errorf("at $%.4f of $2.55: %v %v %v, want one budget-run on the token cap", usd, p, revoked, fell)
			}
			if p[runs.AnnUsage] == "" || u.Input+u.Output <= 1_500_000 {
				t.Errorf("the token count is written, and past the cap: %v", p)
			}
		})
	}
}

// The same volume with no cache hit costs over three times as much ($2.30–2.49), and the cap still bounds
// it: an uncached run is revoked once its cost reaches $2.55, about 1.8 M tokens.
func TestAnUncachedRunIsStillBounded(t *testing.T) {
	for issue, u := range cohort {
		cached := cost(glm["agent-default"], typedUsage(u))
		u.CacheRead = 0
		if fresh := cost(glm["agent-default"], typedUsage(u)); float64(fresh) < 3*float64(cached) || fresh >= 2_550_000 {
			t.Errorf("%s uncached: $%.4f, want over 3 × $%.4f and under the cap", issue, float64(fresh)/1e6, float64(cached)/1e6)
		}
	}
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
	src := typed{}
	var revoked []revocation
	var fell []string
	m := usdMeter(st, src, &revoked, &fell)
	for _, in := range []int64{900_000, 1_800_000} { // $2.52 + output's $0.13: over
		src["aaaaaaaa"] = typedUsage(Usage{Input: in, Output: 30_000})
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunUSD}) {
		t.Fatalf("revocations %v, want one budget-run on the USD cap", revoked)
	}
}

// One release of compatibility: a tier without runUSD keeps its token cap, prices or not.
func TestATierWithoutAUSDCapKeepsItsTokenCap(t *testing.T) {
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
	var revoked []revocation
	var fell []string
	m := usdMeter(st, typed{"aaaaaaaa": typedUsage(cohort["#2279"])}, &revoked, &fell)
	m.Cfg = priced(glm, nil, config.Budgets{})
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunTokens}) || len(fell) != 0 {
		t.Fatalf("revocations %v, fallbacks %v", revoked, fell)
	}
}

// A run its USD cap cannot price is capped in tokens, and says so once: never metered at $0.
func TestAnUnpricableRunFallsBackToItsTokenCap(t *testing.T) {
	for name, c := range map[string]struct {
		model string
		u     Usage
		why   string
	}{
		"an unpriced model": {"tier-standard", typedUsage(cohort["#2279"]), FallbackUnpriced},
		"an untyped query":  {"agent-default", Usage{Tokens: 1_661_800}, FallbackUntyped},
	} {
		t.Run(name, func(t *testing.T) {
			r := standardRun("aaaaaaaa")
			r.Model = c.model
			r.MaxTokens = 2_000_000
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
			src := typed{"aaaaaaaa": c.u}
			var revoked []revocation
			var fell []string
			m := usdMeter(st, src, &revoked, &fell)
			for range 2 {
				if err := m.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if len(fell) != 1 || fell[0] != c.why || len(revoked) != 0 {
				t.Fatalf("fallbacks %v (want one %s), revocations %v", fell, c.why, revoked)
			}
			u := c.u
			u.Tokens, u.Input = 2_000_000, 2_000_000
			src["aaaaaaaa"] = u
			if err := m.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunTokens}) {
				t.Fatalf("revocations %v, want one on the token cap", revoked)
			}
		})
	}
}

// R12 for dollars: a gateway restart zeroes the typed counters, and the run's cost keeps growing
// from its high-water mark; the end reports it once.
func TestACounterResetNeverResetsTheUSDCost(t *testing.T) {
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
	src := typed{}
	var revoked []revocation
	var fell []string
	var costs []float64
	m := usdMeter(st, src, &revoked, &fell)
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return clock }
	m.OnCost = func(_ context.Context, k CostKey, usd float64, _ map[string]float64) {
		tier := k.Tier
		if tier == "standard" {
			costs = append(costs, usd)
		}
	}
	for _, in := range []int64{1_000_000, 100_000, 1_000_000} { // $1.40, a reset, then +$1.26: $2.66
		src["aaaaaaaa"] = typedUsage(Usage{Input: in})
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(revoked) != 1 || revoked[0].limit != LimitRunUSD {
		t.Fatalf("revocations %v, want the USD cap past the reset", revoked)
	}
	st.runs[0].Phase = "BudgetExhausted"
	for range 6 {
		clock = clock.Add(30 * time.Second)
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(costs) != 1 || costs[0] != 2.66 {
		t.Fatalf("costs %v, want one $2.66", costs)
	}
}

// The list prices of the models the gateway pins (ai-native-ref's meter.prices overlay), keyed by
// the model that served the request: agent-default is GLM-5.3 on public and Opus on internal.
var served = map[string]config.Price{
	"glm-5.3":         {Input: 1.40, CacheRead: 0.26, Output: 4.40},
	"claude-opus-5-5": {Input: 4, CacheRead: 0.20, CacheWrite: 5, Output: 20},
}

func byModel(models map[string]ModelUsage) Usage {
	u := Usage{Typed: true, Models: models}
	for _, m := range models {
		u.Input += m.Input
		u.CacheRead += m.CacheRead
		u.CacheWrite += m.CacheWrite
		u.Output += m.Output
		u.Tokens += m.Input + m.Output
	}
	return u
}

// The same claim (agent-default) costs what the model that served it charges: 700 k fresh input
// tokens and 10 k output are $1.02 on GLM-5.3, under the $2.55 cap, and $3.00 on Opus, over it.
func TestARunIsPricedByTheModelThatServedIt(t *testing.T) {
	for model, want := range map[string]struct {
		micro   int64
		revoked bool
	}{"glm-5.3": {1_024_000, false}, "claude-opus-5-5": {3_000_000, true}} {
		t.Run(model, func(t *testing.T) {
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
			var revoked []revocation
			var fell []string
			m := usdMeter(st, typed{"aaaaaaaa": byModel(map[string]ModelUsage{model: {Input: 700_000, Output: 10_000}})}, &revoked, &fell)
			with(m, func(c *config.Config) { c.Meter.Prices = served })
			m.Costs = &Costs{}
			if err := m.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if c, _ := m.Costs.Get("aaaaaaaa"); c.MicroUSD != want.micro || c.Why != "" || !slices.Equal(c.Served, []string{model}) {
				t.Errorf("cost %+v, want %d µUSD", c, want.micro)
			}
			if got := len(revoked) == 1 && revoked[0] == (revocation{ReasonBudgetRun, LimitRunUSD}); got != want.revoked || len(fell) != 0 {
				t.Errorf("revocations %v, fallbacks %v; want revoked: %v", revoked, fell, want.revoked)
			}
		})
	}
}

// A run served by two models costs the sum of each model's tokens at its own prices, a cache
// write at the model's write rate.
func TestARunServedByTwoModelsCostsTheSum(t *testing.T) {
	u := byModel(map[string]ModelUsage{
		"glm-5.3":         {Input: 1_000_000, CacheRead: 900_000, Output: 10_000},                  // 0.14 + 0.234 + 0.044
		"claude-opus-5-5": {Input: 200_000, CacheRead: 100_000, CacheWrite: 50_000, Output: 5_000}, // 0.2 + 0.02 + 0.25 + 0.1
	})
	got, unpriced := runCost(served, "agent-default", u)
	if got != 988_000 || unpriced != "" {
		t.Fatalf("cost %d µUSD, unpriced %q; want 988000", got, unpriced)
	}
}

// J-AC4: a served model the overlay does not price is never free. The run is held to its token cap,
// its cost is unpriced, and the model is named once, for AgentModelUnpriced.
func TestAServedModelWithoutAPriceIsNamedAndCappedInTokens(t *testing.T) {
	r := standardRun("aaaaaaaa")
	r.MaxTokens = 2_000_000
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
	src := typed{"aaaaaaaa": byModel(map[string]ModelUsage{
		"glm-5.3":          {Input: 100_000, Output: 1_000},
		"claude-fable-5-1": {Input: 100_000, Output: 1_000},
	})}
	var revoked []revocation
	var fell, named []string
	m := usdMeter(st, src, &revoked, &fell)
	with(m, func(c *config.Config) { c.Meter.Prices = served })
	m.Costs = &Costs{}
	m.OnUnpriced = func(_ context.Context, model string) { named = append(named, model) }
	for range 2 {
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := m.Costs.Get("aaaaaaaa"); c.Why != FallbackUnpriced {
		t.Errorf("cost %+v, want unpriced", c)
	}
	if len(fell) != 1 || fell[0] != FallbackUnpriced || len(named) != 1 || named[0] != "claude-fable-5-1" || len(revoked) != 0 {
		t.Fatalf("fallbacks %v, named %v, revocations %v", fell, named, revoked)
	}
	src["aaaaaaaa"] = byModel(map[string]ModelUsage{"claude-fable-5-1": {Input: 2_000_000}})
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunTokens}) {
		t.Fatalf("revocations %v, want one on the token cap", revoked)
	}
}

// Before its first reading a run has no model to price: no fallback is reported for a claim whose
// logical name the overlay does not carry.
func TestAnUnseenRunIsNotReportedUnpriced(t *testing.T) {
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
	var revoked []revocation
	var fell []string
	m := usdMeter(st, typed{}, &revoked, &fell)
	with(m, func(c *config.Config) { c.Meter.Prices = served })
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fell) != 0 || len(revoked) != 0 {
		t.Fatalf("fallbacks %v, revocations %v", fell, revoked)
	}
}

// agentgateway 1.6 reports an Anthropic call's input with its cache tokens on the non-streaming
// Chat Completions translation, and without them on a streamed one or native Messages. Both shapes
// of run-wfeiraim's 8 calls on aws-0 (fresh input 18 tokens) cost what it cost.
func TestCostReadsEitherInputShape(t *testing.T) {
	opus := served["claude-opus-5-5"]
	for name, u := range map[string]Usage{
		"input includes cache": {Input: 303_477, CacheRead: 258_965, CacheWrite: 44_494, Output: 10_147},
		"input excludes cache": {Input: 18, CacheRead: 258_965, CacheWrite: 44_494, Output: 10_147},
	} {
		// 18 x 4 + 258,965 x 0.20 + 44,494 x 5 + 10,147 x 20 = 477,275 micro-USD
		if got := cost(opus, u); got != 477_275 {
			t.Errorf("%s: cost = %d micro-USD, want 477275", name, got)
		}
	}
}

// A reading whose input is below its cache tokens is named once per run and model, so a gateway
// path change shows on a counter instead of only in the price.
func TestAnInputBelowItsCacheTokensIsNamedOnce(t *testing.T) {
	r := standardRun("aaaaaaaa")
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
	src := typed{"aaaaaaaa": byModel(map[string]ModelUsage{
		"claude-opus-5-5": {Input: 18, CacheRead: 258_965, CacheWrite: 44_494, Output: 10_147},
		"glm-5.3":         {Input: 100_000, CacheRead: 90_000, Output: 1_000},
	})}
	var revoked []revocation
	var fell, named []string
	m := usdMeter(st, src, &revoked, &fell)
	with(m, func(c *config.Config) { c.Meter.Prices = served })
	m.Costs = &Costs{}
	m.OnInputExcludesCache = func(_ context.Context, model string) { named = append(named, model) }
	for range 2 {
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(named) != 1 || named[0] != "claude-opus-5-5" {
		t.Fatalf("named %v, want claude-opus-5-5 once", named)
	}
	// glm: 10,000 x 1.40 + 90,000 x 0.26 + 1,000 x 4.40 = 41,800; opus: 477,275
	if c, _ := m.Costs.Get("aaaaaaaa"); c.MicroUSD != 519_075 {
		t.Fatalf("cost %+v, want 519075 micro-USD", c)
	}
}

// TC1: a row's source and checkedAt are provenance, never a price.
func TestAPriceRowsProvenanceChangesNoCost(t *testing.T) {
	p := glm["agent-default"]
	q := p
	q.Source, q.CheckedAt = "https://docs.z.ai/guides/overview/pricing", "2026-10-16"
	u := typedUsage(cohort["#2279"])
	if a, b := cost(p, u), cost(q, u); a != b {
		t.Fatalf("cost %d with provenance, %d without", b, a)
	}
}

// priced is the config a test meter reads: prices, each tier's USD run cap, and the budgets.
func priced(prices map[string]config.Price, runUSD map[string]float64, b config.Budgets) *config.Config {
	c := &config.Config{Budgets: b, Tiers: map[string]config.Tier{}}
	c.Meter.Prices = prices
	for name, usd := range runUSD {
		c.Tiers[name] = config.Tier{RunUSD: usd}
	}
	return c
}

// Factory resources D12: the meter reads the live config each tick, so a tier's USD cap that a
// publish adds caps the next tick's runs, with no restart.
func TestATierCapPublishedLiveAppliesOnTheNextTick(t *testing.T) {
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("aaaaaaaa")}}
	live := config.NewLive(priced(glm, nil, config.Budgets{}))
	var revoked []revocation
	var fell []string
	m := usdMeter(st, typed{"aaaaaaaa": typedUsage(Usage{Input: 100_000, Output: 10_000})}, &revoked, &fell)
	m.Cfg = live
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 0 {
		t.Fatalf("no cap yet, revoked %v", revoked)
	}
	next := priced(glm, map[string]float64{"standard": 0.10}, config.Budgets{})
	next.Hash = "published"
	live.Publish(next)
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunUSD}) {
		t.Fatalf("revocations %v, want one on the published USD cap", revoked)
	}
}

// with changes a copy of the meter's config, for a test that alters one input.
func with(m *Meter, f func(*config.Config)) {
	c := &config.Config{}
	if m.Cfg != nil {
		cp := *m.Cfg.Current()
		c = &cp
	}
	f(c)
	m.Cfg = c
}

// #465: a flat-rate model's nominal prices leave the USD cap far out of reach (task ozdjcnhn's
// implementer made 88 calls, $1.47 of $5.10 across the task), so the run token cap is a ceiling
// beside the dollar one: the run is revoked at 1.5 M tokens though its cost is a fraction of $2.55.
func TestARunIsRevokedAtItsTokenCapWhateverItCosts(t *testing.T) {
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{standardRun("7lrzay4x")}}
	src := typed{}
	var revoked []revocation
	var fell []string
	m := usdMeter(st, src, &revoked, &fell)
	for _, u := range []Usage{
		{Input: 1_400_000, CacheRead: 1_350_000, Output: 20_000}, // under the cap
		{Input: 1_960_000, CacheRead: 1_900_000, Output: 40_000}, // 2.0 M tokens, well under $2.55
	} {
		src["7lrzay4x"] = typedUsage(u)
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if u.Input < 1_500_000 && len(revoked) != 0 {
			t.Fatalf("revoked under the cap: %v", revoked)
		}
	}
	if usd := m.last["7lrzay4x"].usd; usd >= 2_550_000 {
		t.Fatalf("cost $%.2f, the test needs it under the USD cap", float64(usd)/1e6)
	}
	if len(revoked) != 1 || revoked[0] != (revocation{ReasonBudgetRun, LimitRunTokens}) {
		t.Fatalf("revocations %v, want one budget-run on the token cap", revoked)
	}
}
