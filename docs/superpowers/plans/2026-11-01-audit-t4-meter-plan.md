# Audit T4: money, one meter baseline per series Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close H-4 (Decision D10): the meter keeps one baseline per series of `meter.query`, so a
gateway replica whose scrape drops out for a tick, or whose counter restarts, is counted once, and
the ledger no longer overcharges a run toward a revocation it has not earned.

**Architecture:** the VM source reads the query reply series by series (`RunSeries`, each keyed
by its labels); the meter folds each series monotonically on its own (`Meter.fold`, state in a new
`Meter.series` map pruned with the run) and sums the folded series into the per-run `Usage` that
`total`, `spend` and `measure` already consume, unchanged. Any `Source` that is not a
`SeriesSource`, every test fake today included, keeps today's per-run path. The config warns at
startup when `meter.query` sums the replicas into one series, since per-series baselines are exact
only when a series is one replica's counter.

**Tech Stack:** Go 1.27.3, stdlib `testing`, `httptest` serving VictoriaMetrics `api/v1/query`
replies, the meter's `store` fake, golangci-lint v2 through `task lint`.

**Spec:** [`docs/superpowers/specs/2026-11-01-audit-fixes-design.md`](../specs/2026-11-01-audit-fixes-design.md),
theme T4, D0 and D10; the finding in [`2026-11-01-project-audit.md`](../specs/2026-11-01-project-audit.md)
(H-4). Executors read both.

**Provenance:** the code below was prototyped on `e14b7949` with `go test -overlay`, the repository
untouched: the new tests failed against the code before the fix with the output quoted in their
"run to fail" steps, then passed under `-race`; with the fix, `go build ./...`, `go vet`, `gofmt -l`
and `go test -race ./internal/app/ ./internal/factory/...` were clean. golangci-lint was not run on
the prototype: `task lint` before the commit is the check. Line numbers are `e14b7949`'s.

## Spec deviations

| # | Spec says | Plan does | Evidence |
|---|---|---|---|
| V1 | D10: one baseline per series, "labels as returned by the query" | The same, **plus** the query must keep the replica label, and the config warns when it does not | The audit's "one summed counter across gateway pods" is the query's doing, not only the meter's: the deployed `meter.query` (ai-native-ref `tooling/base/agent-factory/helm-values-configmap.yaml:357`) is `sum by (ar_agent, gen_ai_request_model, gen_ai_token_type) (gen_ai_client_token_usage_sum{…})`, and this repository's fixture (`config_test.go:68`) `sum by (ar_agent) (…)`. Both sum the replicas server-side: each returned series is already every replica's total, so a baseline per returned series is still one summed baseline, and H-4 stays. Worse, D10's "a fall is a counter reset and adds the new value" would read a stale replica's drop as a reset and add the other replicas' whole counters. The series key is therefore the full label set VictoriaMetrics returns, `__name__` aside, and the query must group by the replica too: `sum by (ar_agent, gen_ai_request_model, gen_ai_token_type, pod) (…)`, key = `(ar_agent, gen_ai_request_model, gen_ai_token_type, pod)` |
| V2 | "the meter's per-run state holds one baseline per series key" | The state is `Meter.series` (per run, per series key), reached through an optional `SeriesSource` interface that `VM` implements | `Source.RunUsage` returns per-run sums, where the series are already gone, and nine test files build a `Meter` on fakes that return per-run maps (`source`, `meter_test.go:55`; `typed`, `usd_test.go:17`; …). An optional interface keeps every one of them, and the "existing single-series cases unchanged" (spec test c) literally so |

**Deploy order (owner, ai-native-ref, outside this repository):** the query change and the image bump
go in **one** ai-native-ref pull request, or the query first. A release of this plan's binary with
today's summed query makes a stale replica worse than today (V1); this plan's startup warning names
it. An old binary with the new query sums the per-pod series per run itself: today's behaviour, no
worse. The replica label is `pod` on vm-operator's pod and service scrapes; confirm before the
change with a read-only query, `count by (pod) (gen_ai_client_token_usage_sum)`, which must return one
series per gateway pod. The meter's code keys on every returned label and does not depend on the name.

**Residual, documented (Task 1):** a new leader whose first reading of a run misses a stale replica
counts that replica's whole counter when it returns, as today: the meter cannot tell a returning
replica from a new one on a run it has never read.

## Global Constraints

- **Branch:** `fix/audit-t4-meter` from `origin/main`. T4 depends on no other theme (spec, Themes
  table); T9 (A-1, the ledger move) waits for it.
- **D0:** one commit for the finding, with its tests written first. The PR is merged with a rebase,
  not a squash.
- **Commits:** Conventional Commits in English, the repo's `type(scope): sentence (ID)` style, **no
  trailers**: no `Co-Authored-By`, no generated-with line. Same for the PR body.
- **Gates:** each test fails first for the reason given, then passes; then `go build ./... && go vet
  ./...`, `go test -race -count=1` on `internal/factory/meter/`, `internal/factory/config/` and
  `internal/app/`, and `task lint` (never a bare `golangci-lint`), before the commit. Task 2 runs
  `task check` (exit 0; `internal/store` needs Docker).
- **Tests:** stdlib `testing`, table-driven `t.Run`, the meter's hand-written `store` fake,
  `httptest` for VictoriaMetrics, `-race` always. No goldens: the replies are inputs built in the test.
- **SPDX:** every new `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- **Unchanged:** `Source`, `Usage`, `total`, `spend`, `measure`, the ledger and settlement, every
  existing meter test.

## File map

| File | Change | Task |
|---|---|---|
| `internal/factory/meter/series.go` | New: `Series`, `SeriesSource`, `seriesKey`, `seriesTotal`, `Meter.fold`, `Meter.usage`, `sumUsage`, `countedType` | 1 |
| `internal/factory/meter/vm.go:50-138` | `RunSeries`, `parseSeries`; `RunUsage` = `sumUsage(RunSeries)`; `parseUsage` goes | 1 |
| `internal/factory/meter/vm_test.go:149-153` | `TestCostFollowsTheGatewaySeries` reads through `parseSeries` and `sumUsage` | 1 |
| `internal/factory/meter/meter.go:330-331,612-616,687` | The `series` field, its pruning in `forget`, `Tick` reads `m.usage` | 1 |
| `internal/factory/meter/series_test.go` | New | 1 |
| `internal/factory/config/config.go:944,1107` | `Meter.ByReplica`, the startup warning | 1 |
| `internal/factory/config/replica_test.go` | New | 1 |
| `docs/factory.md:441,511-520` | The `meter.query` row; the residual | 1 |

---

### Task 1: H-4, one baseline per series

**Files:**
- Create: `internal/factory/meter/series.go`, `internal/factory/meter/series_test.go`
- Modify: `internal/factory/meter/vm.go:50-138`, `internal/factory/meter/vm_test.go:149-153`
- Modify: `internal/factory/meter/meter.go:330-331` (fields), `:612-616` (`forget`), `:687` (`Tick`)
- Create: `internal/factory/config/replica_test.go`
- Modify: `internal/factory/config/config.go:944` (after `ByModel`), `:1107` (`Parse`)
- Modify: `docs/factory.md:441`, `:511-520`

**Interfaces:**
- Consumes: `meter.store` (`meter_test.go:31`), `NewVM`, `httpx.New`, `runs.AnnUsage`; the
  config tests' `good` fixture (`config_test.go:19`).
- Produces:
  - `type Series struct { Run, Key string; Typed bool; Type, Model string; Value int64 }`
  - `type SeriesSource interface { RunSeries(ctx context.Context) ([]Series, error) }`
  - `func (v *VM) RunSeries(ctx context.Context) ([]Series, error)`; `(*VM).RunUsage` unchanged in signature
  - unexported `seriesKey(map[string]string) string`, `(*Meter).fold([]Series) []Series`,
    `(*Meter).usage(context.Context) (map[string]Usage, error)`, `sumUsage([]Series) map[string]Usage`,
    `parseSeries([]byte) ([]Series, error)`, `countedType(string) bool`
  - `func (m config.Meter) ByReplica() bool`

- [ ] **Step 1: Write the failing meter test**

Create `internal/factory/meter/series_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/httpx"
)

// replicaReply is an api/v1/query reply in the shape a query grouped by (ar_agent, pod) returns:
// one series per gateway replica of run aaaaaaaa, untyped, with its raw counter. A replica left
// out is one whose scrape is stale this tick.
func replicaReply(pods map[string]int64) string {
	var rs []string
	for pod, n := range pods {
		rs = append(rs, fmt.Sprintf(`{"metric":{"ar_agent":"system:serviceaccount:agents:xplane-run-aaaaaaaa","pod":%q},"value":[1727430000,"%d"]}`, pod, n))
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rs, ",") + `]}}`
}

// vmReplies is a VM whose i-th query answers replies[i]; past the last, the last again.
func vmReplies(t *testing.T, replies ...string) *VM {
	t.Helper()
	var mu sync.Mutex
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := replies[min(i, len(replies)-1)]
		i++
		mu.Unlock()
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	v, err := NewVM(srv.URL, "Q", httpx.New(5*time.Second, nil))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// H-4, D10: the meter keeps one baseline per series, so a gateway replica's counter is counted
// once, whatever its scrape does; each case's usage is the run's total after each tick.
func TestEachReplicaIsCountedOnce(t *testing.T) {
	for name, c := range map[string]struct {
		ticks []map[string]int64
		usage []int64
	}{
		// B is stale for one tick: A's growth counts, B's total is kept, and B's return adds only
		// what it grew (a summed baseline re-baselined on the drop and added B's 450 again).
		"a replica drops out and returns": {
			ticks: []map[string]int64{{"a": 600, "b": 400}, {"a": 700}, {"a": 700, "b": 450}},
			usage: []int64{1000, 1100, 1150},
		},
		// A restarts: its counter falls to 50, all of it new; B's growth counts beside it.
		"a replica's counter restarts": {
			ticks: []map[string]int64{{"a": 600, "b": 400}, {"a": 50, "b": 420}, {"a": 80, "b": 430}},
			usage: []int64{1000, 1070, 1110},
		},
		// One series: the per-run behaviour, unchanged while the counter only grows.
		"one series": {
			ticks: []map[string]int64{{"a": 600}, {"a": 650}, {"a": 900}},
			usage: []int64{600, 650, 900},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var replies []string
			for _, pods := range c.ticks {
				replies = append(replies, replicaReply(pods))
			}
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{{ID: "aaaaaaaa", Phase: "Running"}}}
			m := &Meter{Runs: st, Source: vmReplies(t, replies...)}
			for i, want := range c.usage {
				if err := m.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				if u := st.patches["aaaaaaaa"][runs.AnnUsage]; u != "" { // the composition projects it (SP1)
					st.runs[0].Tokens, _ = strconv.ParseInt(u, 10, 64)
				}
				if st.runs[0].Tokens != want {
					t.Fatalf("tick %d: usage %d, want %d", i+1, st.runs[0].Tokens, want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/meter/ -run TestEachReplicaIsCountedOnce`

Expected: FAIL on both replica cases; "one series" passes. Today's `RunUsage` sums the replicas per
run and `total` re-baselines on the fall, adding nothing for A's growth (and, on tick 3 of the first
case, B's whole 450 again: 1450):

```
--- FAIL: TestEachReplicaIsCountedOnce/a_replica_drops_out_and_returns   tick 2: usage 1000, want 1100
--- FAIL: TestEachReplicaIsCountedOnce/a_replica's_counter_restarts      tick 2: usage 1000, want 1070
```

- [ ] **Step 3: Read series by series and fold each one**

Create `internal/factory/meter/series.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Series is one series of the meter's query that is a run's: its run, its labels as one key, its
// token type and served model when labelled, and its raw value.
type Series struct {
	Run, Key    string
	Typed       bool   // the series carries gen_ai_token_type
	Type, Model string // gen_ai_token_type, gen_ai_request_model ("" when unlabelled)
	Value       int64
}

// SeriesSource is a Source that also reads the query series by series, so the meter keeps one
// baseline per series (D10) rather than one per run's sum. VM implements it.
type SeriesSource interface {
	RunSeries(ctx context.Context) ([]Series, error)
}

// seriesKey is a series' labels as one string, sorted by name, __name__ left out: the identity
// D10 keeps a baseline by. A query that keeps the gateway replica (pod) apart gives each
// replica's counter its own key.
func seriesKey(metric map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(metric)) {
		if k == "__name__" {
			continue
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(metric[k]))
		b.WriteByte(',')
	}
	return b.String()
}

// seriesTotal is one series' last reading and the monotonic total the meter gave it.
type seriesTotal struct {
	last  Series
	total int64
}

// fold keeps each run's series monotonic one by one (D10) and returns them with their totals as
// values. A series' total grows by its value's increase, or by its whole value when the value
// fell, since its counter restarted from zero; a series first seen counts its whole value, since a
// new replica's counter starts at zero too. A series missing from this reply keeps its total and
// adds nothing, so a replica whose scrape drops out for a tick is not counted again when it
// returns. Only runs with a series in this reply are returned: a run with none is unread, as
// before.
func (m *Meter) fold(series []Series) []Series {
	if m.series == nil {
		m.series = map[string]map[string]seriesTotal{}
	}
	read := map[string]bool{}
	for _, s := range series {
		run := m.series[s.Run]
		if run == nil {
			run = map[string]seriesTotal{}
			m.series[s.Run] = run
		}
		p, known := run[s.Key]
		switch {
		case !known:
			p.total = s.Value
		case s.Value >= p.last.Value:
			p.total += s.Value - p.last.Value
		default: // a restarted counter: all it counts is new
			p.total += s.Value
		}
		p.last = s
		run[s.Key] = p
		read[s.Run] = true
	}
	var out []Series
	for _, id := range slices.Sorted(maps.Keys(read)) {
		run := m.series[id]
		for _, key := range slices.Sorted(maps.Keys(run)) {
			s := run[key].last
			s.Value = run[key].total
			out = append(out, s)
		}
	}
	return out
}

// usage is this tick's reading of each run. A SeriesSource is read series by series and each kept
// monotonic on its own first (fold); any other Source's per-run sums are taken as they come.
func (m *Meter) usage(ctx context.Context) (map[string]Usage, error) {
	src, ok := m.Source.(SeriesSource)
	if !ok {
		return m.Source.RunUsage(ctx)
	}
	series, err := src.RunSeries(ctx)
	if err != nil {
		return nil, err
	}
	return sumUsage(m.fold(series)), nil
}

// sumUsage sums series into each run's counters: a typed series counts as its type, and also
// under its served model; an untyped one is the token count itself.
func sumUsage(series []Series) map[string]Usage {
	out := map[string]Usage{}
	for _, s := range series {
		u, n := out[s.Run], s.Value
		if !s.Typed {
			u.Tokens += n
			out[s.Run] = u
			continue
		}
		mu := u.Models[s.Model]
		switch s.Type {
		case "input":
			u.Input, mu.Input = u.Input+n, mu.Input+n
			u.Tokens += n
			u.Typed = true
		case "output":
			u.Output, mu.Output = u.Output+n, mu.Output+n
			u.Tokens += n
			u.Typed = true
		case "cached_input":
			u.CacheRead, mu.CacheRead = u.CacheRead+n, mu.CacheRead+n
		case "cache_creation_input":
			u.CacheWrite, mu.CacheWrite = u.CacheWrite+n, mu.CacheWrite+n
		default:
			continue
		}
		if u.Models == nil {
			u.Models = map[string]ModelUsage{}
		}
		u.Models[s.Model] = mu
		out[s.Run] = u
	}
	return out
}

// countedType is a gen_ai_token_type the meter counts.
func countedType(typ string) bool {
	switch typ {
	case "input", "output", "cached_input", "cache_creation_input":
		return true
	}
	return false
}
```

`sumUsage` is `parseUsage`'s old loop body, moved as is, so every typed and per-model rule stays.
The folded per-run sums only ever grow while a run is known, so `total`'s drop branch and `spend`'s
per-model `mono` see no fall from a replica; they keep handling the non-series `Source` path.

In `internal/factory/meter/vm.go`, replace everything from the `RunUsage` doc comment
(`// RunUsage is each run's raw counters, by run id. A series that is not a run's, …`) to the end
of the file with:

```go
// RunUsage is each run's raw counters, by run id: RunSeries summed by run, stateless. A series
// labelled gen_ai_token_type counts as its type, and also under its gen_ai_request_model, the
// model that served it; an unlabelled one is the token count itself, so an untyped query meters as
// before.
func (v *VM) RunUsage(ctx context.Context) (map[string]Usage, error) {
	series, err := v.RunSeries(ctx)
	if err != nil {
		return nil, err
	}
	return sumUsage(series), nil
}

// RunSeries is the query's reply series by series (D10), each keyed by its labels. A series that is
// not a run's, whose value is not a finite, non-negative count, or whose gen_ai_token_type the
// meter does not price (reasoning, a subset of output, and unknown types), is no reading.
func (v *VM) RunSeries(ctx context.Context) ([]Series, error) {
	target := v.base.JoinPath("api/v1/query")
	target.RawQuery = url.Values{"query": {v.query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("meter: request: %w", err)
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meter: query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxVMReply)
	if err != nil {
		return nil, fmt.Errorf("meter: query: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meter: query: status %d", resp.StatusCode)
	}
	return parseSeries(raw)
}

// parseSeries reads the body of an api/v1/query reply into the runs' series.
func parseSeries(raw []byte) ([]Series, error) {
	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("meter: query: the reply is not JSON")
	}
	if body.Status != "success" || body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("meter: query: status %q, result %q: want a success and a vector", body.Status, body.Data.ResultType)
	}
	var out []Series
	for _, s := range body.Data.Result {
		m := runwatch.RunSubject().FindStringSubmatch(s.Metric["ar_agent"])
		if m == nil {
			continue
		}
		str, _ := s.Value[1].(string)
		f, err := strconv.ParseFloat(str, 64)
		if err != nil || math.IsNaN(f) || f < 0 || f > maxReading {
			continue
		}
		typ, typed := s.Metric["gen_ai_token_type"]
		if typed && !countedType(typ) {
			continue // reasoning, a subset of output, and unknown types
		}
		out = append(out, Series{Run: m[1], Key: seriesKey(s.Metric), Typed: typed, Type: typ,
			Model: s.Metric["gen_ai_request_model"], Value: int64(f)})
	}
	return out, nil
}
```

`vm.go`'s imports are unchanged. `parseUsage` is gone: its one reader, a test, reads through the
two halves. In `internal/factory/meter/vm_test.go` (`TestCostFollowsTheGatewaySeries`), replace:

```go
	usage, err := parseUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	u := usage["cf4ato2x"]
```

with:

```go
	series, err := parseSeries(raw)
	if err != nil {
		t.Fatal(err)
	}
	u := sumUsage(series)["cf4ato2x"]
```

In `internal/factory/meter/meter.go`, the `Meter` struct's private fields end:

```go
	named    map[string]bool             // run and model pairs whose missing price, or input without cache, was reported
}
```

Replace with (a blank line first, so gofmt leaves the aligned block above alone):

```go
	named    map[string]bool             // run and model pairs whose missing price, or input without cache, was reported

	// series is per run: each series' last reading and the total fold gave it (D10).
	series map[string]map[string]seriesTotal
}
```

In `Tick`, replace:

```go
	used, err := m.Source.RunUsage(ctx)
```

with:

```go
	used, err := m.usage(ctx)
```

In `forget`, replace:

```go
	for id := range m.last {
		if !live[id] {
			delete(m.last, id)
		}
	}
```

with:

```go
	for id := range m.last {
		if !live[id] {
			delete(m.last, id)
		}
	}
	for id := range m.series {
		if !live[id] {
			delete(m.series, id)
		}
	}
```

`internal/app/factory.go:162,337` needs no change: it already hands the `*meter.VM` to
`Meter.Source`, and `usage` finds `RunSeries` on it.

- [ ] **Step 4: Run the meter tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/meter/ -run 'TestEachReplicaIsCountedOnce|TestVM|TestCostFollowsTheGatewaySeries|TestACounterResetNeverResetsTheCap|TestTheMetersMemoryIsTheFloorWhileTheAnnotationLags'`
Expected: PASS. The VM tests pin `RunUsage`'s reading unchanged; the two reset tests pin the
per-run path of a plain `Source`.

Then: `go test -race -count=1 ./internal/factory/meter/`
Expected: PASS, every existing test untouched.

- [ ] **Step 5: Write the failing config test**

Create `internal/factory/config/replica_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// H-4, D10: the meter keeps one baseline per series of meter.query, so the query must keep each
// gateway replica's counter apart. A query that sums them loads, with a warning that names the fix.
func TestAMeterQuerySummingTheReplicasWarns(t *testing.T) {
	warns := func(raw string) bool {
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "meter.query sums the gateway replicas") })
	}
	if !warns(good) { // sum by (ar_agent): every replica in one series
		t.Fatal("a query summing the replicas loads without a warning")
	}
	for name, by := range map[string]string{"by pod": "sum by (ar_agent, pod)", "by instance": "sum by (ar_agent, instance)"} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(good, "sum by (ar_agent)", by, 1)
			if raw == good {
				t.Fatal("the edit did not apply")
			}
			if warns(raw) {
				t.Fatal("a query keeping the replicas apart warns")
			}
		})
	}
}
```

- [ ] **Step 6: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/config/ -run TestAMeterQuerySummingTheReplicasWarns`

Expected: FAIL, no such warning exists yet:

```
--- FAIL: TestAMeterQuerySummingTheReplicasWarns
    a query summing the replicas loads without a warning
```

- [ ] **Step 7: Warn on a summed query, and document both**

In `internal/factory/config/config.go`, after `func (m Meter) ByModel() bool { … }` add:

```go

// ByReplica is true when the meter's query names the label that tells the gateway's replicas
// apart, pod or instance: the meter keeps one baseline per series (D10), which is exact only when
// a series is one replica's counter.
func (m Meter) ByReplica() bool {
	return strings.Contains(m.Query, "pod") || strings.Contains(m.Query, "instance")
}
```

In `Parse`, replace:

```go
	sum := sha256.Sum256(j)
	c.Hash = hex.EncodeToString(sum[:])
	return &c, nil
}
```

with:

```go
	if !c.Meter.ByReplica() {
		c.Warnings = append(c.Warnings, "meter.query sums the gateway replicas into one series: keep the replica label in its grouping, "+
			"e.g. sum by (ar_agent, gen_ai_request_model, gen_ai_token_type, pod), or a replica whose scrape drops out for a tick "+
			"is charged again when it returns (D10)")
	}
	sum := sha256.Sum256(j)
	c.Hash = hex.EncodeToString(sum[:])
	return &c, nil
}
```

`internal/app/factory.go:114` already logs each of `cfg.Warnings` at startup. `ByReplica`, like
`ByModel`, reads the query's text: a query naming `pod` only in a matcher passes it, which the
warning accepts as the operator's call.

In `docs/factory.md`, the `meter.query` row (line 441) becomes:

```markdown
| `meter.query` | Must group by `gen_ai_token_type` (`input`, `cached_input`, `cache_creation_input`, `output`), or no run can be priced and every run is capped in tokens. Grouped by `gen_ai_request_model` too, each model is priced at its own rate. Must keep the gateway replica apart, grouped by `pod` (or `instance`): the meter keeps one baseline per returned series, so a replica whose scrape drops out for a tick adds nothing and, on its return, only what it grew, and a replica's counter that falls restarted and adds its new value. A query summing the replicas loads with a startup warning, and a stale replica is charged again when it returns |
```

In the same file, replace:

```markdown
failover, a ledger that cannot be read, a failed write, landed or not, and midnight, with three
bounded exceptions:
```

with:

```markdown
failover, a ledger that cannot be read, a failed write, landed or not, and midnight, with four
bounded exceptions:
```

and after the bullet that ends `an under-count bounded by the reading lag at a run's\n  end.` add:

```markdown
- A new leader whose first reading of a run misses a stale gateway replica counts that replica's
  whole counter when it returns: it cannot tell a returning replica from a new one on a run it
  has never read. An over-count bounded by that replica's share of the run.
```

- [ ] **Step 8: Run everything to make sure it passes**

Run: `go test -race -count=1 ./internal/factory/config/ -run TestAMeterQuerySummingTheReplicasWarns`
Expected: PASS.

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/meter/ ./internal/factory/config/ ./internal/app/ && task lint`
Expected: PASS, 0 lint issues.

- [ ] **Step 9: Commit**

```bash
git add internal/factory/meter/series.go internal/factory/meter/series_test.go internal/factory/meter/vm.go \
  internal/factory/meter/vm_test.go internal/factory/meter/meter.go \
  internal/factory/config/config.go internal/factory/config/replica_test.go docs/factory.md
git commit -m "fix(meter): one baseline per series, so a gateway replica's counter is counted once (H-4)"
```

---

### Task 2: the gate and the pull request

**Files:** none changed.

- [ ] **Step 1: Run the whole gate**

Run: `task check`
Expected: exit 0, Docker running for `internal/store`'s testcontainers. Note the exit code for the PR.

- [ ] **Step 2: One commit, no trailer**

Run: `git log --oneline origin/main..HEAD && git log origin/main..HEAD --format=%B | grep -ci 'co-authored-by\|generated with'`
Expected: one commit ending `(H-4)`, then `0`.

- [ ] **Step 3: Push and open the pull request**

```bash
git fetch origin && git rebase origin/main
git push -u origin fix/audit-t4-meter
gh pr create --base main --title "fix(meter): one baseline per series, so a gateway replica's counter is counted once (H-4)" --body-file - <<'EOF'
## What

Theme T4 of the 2026-11-01 audit fixes (H-4, decision D10). The meter read one summed counter per run: a gateway replica whose scrape went stale for a tick dropped the sum, the meter re-baselined, and the replica's return added its whole counter again, overcharging the ledger toward a revocation.

- `meter.VM` reads the query reply series by series (`RunSeries`), each keyed by its labels.
- The meter folds each series on its own: a missing series keeps its total and adds nothing; a returning one adds what it grew; a fall is a restarted counter and adds its new value. The folded series feed the unchanged per-run accounting.
- The config warns at startup when `meter.query` sums the replicas into one series.

## Deploy step (ai-native-ref, same PR as the image bump, or before)

`meter.query` must keep the replica: `sum by (ar_agent, gen_ai_request_model, gen_ai_token_type, pod) (…)`. Check first that `count by (pod) (gen_ai_client_token_usage_sum)` returns one series per gateway pod. With today's summed query this release is worse on a stale replica than the last one; the startup warning names it.

## Verification

`task check`: exit 0. `TestEachReplicaIsCountedOnce` (a replica dropping out and returning, a replica's counter restarting, one series) failed before the fix at tick 2; every existing meter test passes untouched.
EOF
```

Merge with a rebase, not a squash (D0).

## Self-review

- **Spec coverage:** D10 per-series baselines (`fold`); spec test (a) "a replica drops out and returns",
  (b) "a replica's counter restarts", (c) "one series" plus every existing meter test unchanged;
  recorded `api/v1/query` replies are built in the test in the reply's exact shape (`replicaReply`),
  since no live reply can be recorded offline. The query prerequisite (V1) is covered by the warning,
  the docs and the PR's deploy step.
- **Placeholders:** none; every code step is the prototyped code.
- **Names:** `Series`, `SeriesSource`, `RunSeries`, `parseSeries`, `sumUsage`, `seriesKey`,
  `seriesTotal`, `fold`, `usage`, `countedType`, `Meter.series`, `ByReplica` are used as defined.
