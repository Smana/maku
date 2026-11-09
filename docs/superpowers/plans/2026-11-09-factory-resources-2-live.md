# Factory resources, plan 2 of 4: the factory reads its resources, live

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The factory builds its config from the file and its `Factory`, `Repository` and
`MergePolicy` objects, and republishes it when they change, with no restart. Every reader takes
the current config once per decision. What startup derived from live fields is re-derived on each
publish. A task records the generations it started under.

**Architecture:** A `config.Live` holds an `atomic.Pointer[config.Config]`. Readers hold a
`config.Source`, a one-method interface, `Current() *Config`, which both `*Config` (fixed: tests,
CLI) and `*Live` satisfy. A `configsource.Source` runs on **every** replica. It lists the three
kinds through the uncached API reader, substitutes each refused object's last accepted spec,
calls `resources.Assemble`, and publishes. Watch events and a periodic resync trigger it.
Subscribers re-derive repository connections, the fence list, pollers, meter inputs and metrics.

**Tech Stack:** Go, controller-runtime (manager cache informers, `manager.Runnable`), envtest.

**Spec:** `docs/superpowers/specs/2026-11-09-factory-resources-design.md`: D5 (live, last good,
record not pin), D12 (live vs startup fields), D13 (every replica builds, the leader writes
status), D14 (invalid at startup is not served), D15. **Builds on plan 1**
(`internal/factory/resources`: `Object{Kind, Name, Namespace, Generation, Spec}`,
`Assemble(file, objs) (Result, error)`, where the error means "no servable config" and
`Result.Refused` stays valid alongside it). **Inventory:** the plan-2 inventory (holders, copy
sites, runnables with file:line) is at
`.superpowers/sdd/plan2-inventory.md` in the implementation worktree. Read it before tasks 2 and 4.

## Global Constraints

- Startup fields (D12) are read once and never re-derived: `policyManagement`, `coordination`, `hardening`, `api`, `runs`, `sandbox`, `demo`, `broker`, `github`, `meter.url/query/logsURL/throttleQuery`, `poll`, `tracing`, `factoryLogin`, `agentsLogin`, `roomsURL`, `factory`, `runlore.listen`, `triage.classifierURL`.
- Live fields: the catalogue (`control`, `defaults`, `tiers`, `templates`, `caps`, `budgets`, `facts`, `brief`, `resume`, `scoper`, `loops`, `harnessProfiles`, `mergeGuardrails`, `meter.prices`, `triage.controlPercent`) and `repositories` (set, settings, classes, merge).
- A reader loads the config **once per decision** (`cfg := x.Cfg.Current()` at the top of the method or tick) and never stores it past that decision.
- The config source runs with `NeedLeaderElection() == false`. Leader-only runnables stay leader-only.
- A publish never crashes the process. A failure to wire something new (for example a GitHub connection for a new repository) is logged and counted, and that repository is not served until the next publish succeeds.
- With no CRDs installed (`no matches for kind`), the factory runs from the file alone and logs it once.
- Every `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`. `task check` exits 0 before every commit. Conventional commits in English, no trailers, no co-author.

## Plans 3–4 (not here)

| Plan | Delivers |
|---|---|
| 3 | The leader writes status from `configsource.Source.Outcomes()` (conditions, served generation, MergePolicy authority and per-class mode and last decision, Repository `Connected`, Factory `Ready`); the settings pages output a MergePolicy patch |
| 4 | ai-native-ref: generated manifests, `maku config validate` in CI, a shrunken values file (after a Maku release, which is the owner's) |

## File structure

| File | Responsibility |
|---|---|
| `internal/factory/config/live.go` (create), `live_test.go` | `Source`, `Live`, `(*Config).Current`, `Generations` |
| `internal/factory/config/config.go` (modify) | `Generations` field (json:"-") and `GenerationsFor` |
| 14 holder types (modify; list in task 2) | `Cfg config.Source`, one `Current()` per decision |
| `internal/factory/mergepolicy/watch.go` (modify) | evaluation cache keyed by file hash **and** config hash |
| `internal/factory/configsource/source.go` (create), `source_test.go`, `source_envtest_test.go` | `Source`: list, last-good substitution, assemble, publish, outcomes, runnable |
| `internal/app/factory.go`, `factory_repos.go` (modify) | initial sync before the manager, live-aware `repoConns`, fence, pollers, meter, metrics, Flux, RunLore |
| `internal/factory/fence/fence.go`, `internal/factory/meter/meter.go`, `internal/factory/intake/pollers.go` (modify, create) | live inputs; the poller supervisor |
| `api/factory/v1alpha1/task_types.go` (modify) and 5 writer sites | `status.configGenerations` |
| `docs/architecture.md`, `docs/merge-policies.md` (modify) | how the factory reads its resources |

---

### Task 1: `config.Source` and `config.Live`

**Files:**
- Create: `internal/factory/config/live.go`
- Modify: `internal/factory/config/config.go` (add the `Generations` field to `Config`)
- Test: `internal/factory/config/live_test.go`

**Interfaces:**
- Produces:

```go
// Source is where a reader takes the config for one decision (factory resources D5).
type Source interface{ Current() *Config }

// Current makes a fixed *Config its own Source: tests and the CLI pass one.
func (c *Config) Current() *Config { return c }

// Generations are the object generations a published config was built from (D5: tasks record
// them). Zero means the file, not an object.
type Generations struct {
	Factory int64
	Repos   map[string]RepoGenerations // keyed by lowercased owner/name
}
type RepoGenerations struct{ Repository, MergePolicy int64 }

// GenerationsFor returns the generations repository was served under; zeros for a file entry.
func (c *Config) GenerationsFor(repository string) (factory, repo, policy int64)

// Live is the factory's published config. Load-free reads, serialized publishes.
type Live struct { /* atomic.Pointer[Config], mu sync.Mutex, prepare []func(*Config), subs []func(prev, next *Config) */ }
func NewLive(c *Config) *Live
func (l *Live) Current() *Config
// Prepare registers f to run on every config before it is published, and runs it on the
// current one now: what Parse cannot set, such as SetClassSource (stage 4).
func (l *Live) Prepare(f func(*Config))
// Subscribe registers f to run after each publish, in registration order, with the previous
// and the new config. Subscribers re-derive what startup derived (D12).
func (l *Live) Subscribe(f func(prev, next *Config))
// Publish runs the prepare hooks on next, stores it, then calls the subscribers. A next whose
// Hash and Generations equal the current one's is not published (no subscriber call).
func (l *Live) Publish(next *Config)
```

`Config` gains `Generations Generations `json:"-"``. Parse leaves it zero.

- [ ] **Step 1: Write the failing tests** (`live_test.go`):

```go
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"sync"
	"testing"
)

func TestLivePublishRunsPrepareThenSubscribers(t *testing.T) {
	a, b := &Config{Hash: "a"}, &Config{Hash: "b"}
	l := NewLive(a)
	var prepared []string
	l.Prepare(func(c *Config) { prepared = append(prepared, c.Hash) })
	var seen [][2]string
	l.Subscribe(func(prev, next *Config) { seen = append(seen, [2]string{prev.Hash, next.Hash}) })
	l.Publish(b)
	if l.Current() != b {
		t.Fatal("Current is the published config")
	}
	if len(prepared) != 2 || prepared[0] != "a" || prepared[1] != "b" {
		t.Fatalf("Prepare runs on the current config at registration, then on each publish: %v", prepared)
	}
	if len(seen) != 1 || seen[0] != [2]string{"a", "b"} {
		t.Fatalf("subscribers see prev and next: %v", seen)
	}
}

func TestLivePublishSkipsAnUnchangedConfig(t *testing.T) {
	l := NewLive(&Config{Hash: "a"})
	calls := 0
	l.Subscribe(func(_, _ *Config) { calls++ })
	l.Publish(&Config{Hash: "a"})
	if calls != 0 {
		t.Fatal("same hash and generations: no publish")
	}
	l.Publish(&Config{Hash: "a", Generations: Generations{Factory: 2}})
	if calls != 1 {
		t.Fatal("a new generation with the same content is still a publish (tasks record it)")
	}
}

func TestLiveIsRaceFree(t *testing.T) {
	l := NewLive(&Config{Hash: "0"})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); l.Publish(&Config{Hash: string(rune('a' + i))}) }()
		go func() { defer wg.Done(); _ = l.Current().Hash }()
	}
	wg.Wait()
}

func TestConfigIsItsOwnSource(t *testing.T) {
	c := &Config{}
	var s Source = c
	if s.Current() != c {
		t.Fatal("a *Config is a fixed Source")
	}
}

func TestGenerationsFor(t *testing.T) {
	c := &Config{Generations: Generations{Factory: 3, Repos: map[string]RepoGenerations{"smana/maku": {Repository: 2, MergePolicy: 5}}}}
	if f, r, p := c.GenerationsFor("Smana/maku"); f != 3 || r != 2 || p != 5 {
		t.Fatalf("got %d %d %d", f, r, p)
	}
	if f, r, p := c.GenerationsFor("Smana/other"); f != 3 || r != 0 || p != 0 {
		t.Fatalf("a file repository has zero object generations: %d %d %d", f, r, p)
	}
}
```

- [ ] **Step 2:** Run `go test -race ./internal/factory/config/ -run 'Live|OwnSource|GenerationsFor'`. Expect a compile failure.
- [ ] **Step 3:** Implement `live.go`. `Publish` takes `mu` for its whole body (publishes are serialized; `Current` never takes it). It compares `Hash` and `reflect.DeepEqual(Generations)` against the current config. If they are equal it returns. Otherwise it runs the prepare hooks on `next`, then `p.Store(next)`, then calls the subscribers in order, still under `mu`, so they see publishes in order. A panicking subscriber must not wedge `mu`: use `defer l.mu.Unlock()`. Document in the doc comment that subscribers must not call `Publish`.
- [ ] **Step 4:** The tests pass with `-race`.
- [ ] **Step 5:** `task check` passes. Commit `feat(config): a live config source that publishes and notifies`.

---

### Task 2: Readers take a `config.Source` and load once per decision

**Files (modify):** every holder in the inventory's "Pointer holders" list:
`internal/factory/api/server.go` (+ every `s.Cfg.` use in that package),
`internal/factory/policyview/service.go`, `internal/factory/evidence/service.go`,
`internal/factory/reconciler/*.go` (`Reconciler.Cfg`, `Arm.Cfg`),
`internal/factory/triage/classify.go`, `triage.go`, `internal/factory/coordination/checks.go`,
`confirmation.go`, `internal/factory/intake/issues.go`, `schedule.go`, `adopt.go`, `runlore.go`,
`internal/factory/mergepolicy/watch.go`.

**Interfaces:**
- Consumes: `config.Source` (task 1).
- Produces: each of those structs' field `Cfg` becomes `Cfg config.Source`. The field name is unchanged, so tests that pass a `*config.Config` keep compiling.

**The rule.** Change the field's type. The compiler then lists every `x.Cfg.Field` use. At each,
introduce `cfg := x.Cfg.Current()` **once at the top of the enclosing decision**, and replace
`x.Cfg.` with `cfg.` below it:

| Kind of method | Where `Current()` goes |
|---|---|
| HTTP handler | first line of the handler |
| controller `Reconcile` | first line of `Reconcile` (one task, one config) |
| poller or ticker | first line of each tick's body, not of `Start` |
| helper taking no config, called from one of the above | add a `cfg *config.Config` parameter instead of calling `Current()` again |

Never call `Current()` twice in one decision, and never store the result in a struct.

Startup reads in `Start` (`API.Listen`, `API.TLS`, `Poll.Issues`, `RunLore.Listen`) call
`Current()` once in `Start`. They are startup fields (D12).

**`mergepolicy.Watch`:** its evaluation cache (`watch.go` ~95–98) is keyed by the file's hash only.
Key it by the file hash **and** `cfg.Hash`, so a config change re-evaluates an unchanged file. Add
a test: same file, a new config (different `HarnessProfiles`), and `Observe` re-evaluates. Leave
the demoted state ("until restart") as it is, and record it in the report.

- [ ] **Step 1:** Write the `mergepolicy.Watch` re-evaluation test first and see it fail.
- [ ] **Step 2:** Change the field types, then fix compile errors package by package with the rule above. Run `go vet ./...` after each package.
- [ ] **Step 3:** Run `go test -race -p 1` on each touched package. Existing tests must pass unchanged, apart from tests that construct a holder with a non-`*Config` value (there should be none).
- [ ] **Step 4:** `grep -rn "\.Cfg\.[A-Z]" --include=*.go internal | grep -v _test` prints nothing.
- [ ] **Step 5:** `task check` passes. Commit `refactor(factory): readers take a config source and load it once per decision`.

---

### Task 3: `configsource.Source`

**Files:**
- Create: `internal/factory/configsource/source.go`, `source_test.go`, `source_envtest_test.go`

**Interfaces:**
- Consumes: `resources.Object`, `resources.Assemble`, `resources.Result`, `resources.Kind*` (plan 1); `config.Live`, `config.Generations` (task 1); the `v1alpha1` kinds.
- Produces:

```go
// Outcome is what the factory made of one object, for the status writer (plan 3).
type Outcome struct {
	Kind               resources.Kind
	Name               string
	ObservedGeneration int64  // the object's current generation
	ServedGeneration   int64  // the generation in the published config; 0 when not served
	Accepted           bool   // the current generation is served
	Reason, Message    string // why not, when !Accepted (a v1alpha1.Reason*)
}

// Source builds the factory's config from its file and its resources (D5, D13, D14).
type Source struct {
	Reader    client.Reader // uncached (mgr.GetAPIReader() or a plain client): works before the cache starts
	Namespace string
	File      []byte
	Live      *config.Live // nil until the first successful Sync, which creates it
	Log       *slog.Logger
	Resync    time.Duration // 5m when zero
	// unexported: mu, lastGood map[key]resources.Object, outcomes []Outcome, kick chan struct{}, crdsAbsent bool
}
// Sync lists, assembles and publishes once. It returns an error only when no config can be
// served and none is published yet (startup, D14).
func (s *Source) Sync(ctx context.Context) error
// Outcomes returns a copy of the last Sync's outcomes, sorted by Kind then Name.
func (s *Source) Outcomes() []Outcome
// Kick asks for a Sync soon (non-blocking; coalesced).
func (s *Source) Kick()
// Start runs Sync on every Kick and every Resync until ctx ends (manager.Runnable).
func (s *Source) Start(ctx context.Context) error
// NeedLeaderElection is false: every replica serves from its own config (D13).
func (s *Source) NeedLeaderElection() bool { return false }
// Watch registers informer handlers on the three kinds that Kick; with no CRDs it does nothing.
func (s *Source) Watch(ctx context.Context, c cache.Cache) error
```

**Sync algorithm:**
1. List `FactoryList`, `RepositoryList` and `MergePolicyList` in `Namespace` through `Reader`. Convert each item to a `resources.Object`, taking `Spec` from `runtime.DefaultUnstructuredConverter.ToUnstructured(&item)["spec"]` so omitted fields stay omitted. If a list fails with `meta.IsNoMatchError`, treat it as zero objects and log once (`crdsAbsent`). Any other error is returned (no publish).
2. `r, err := resources.Assemble(s.File, objs)`.
3. **Last good (D5).** For each refused object whose key (kind and name) has a `lastGood` entry, replace it in `objs` by that entry: same name, the old spec and generation. If any was replaced, `r, err = Assemble(s.File, objs)` again. Each replaced object's outcome is `Accepted=false` with the first pass's reason and message, `ServedGeneration` = the last good generation when the second pass accepted it, else 0.
4. If `err != nil`: record the outcomes (all refusals; nothing served). If `Live == nil`, return the error (startup cannot serve, D14). Otherwise keep the current config and return nil.
5. Otherwise build `config.Generations` from the accepted objects (Factory's generation; per repository, keyed by lowercased `spec.repository`, the Repository's and MergePolicy's generations) and set `r.Config.Generations`. Create `Live` with `config.NewLive` on the first success, or `Publish` the config.
6. Update `lastGood` to the accepted objects whose **current** spec was used. Drop entries for objects that no longer exist: a deleted object is meant to go.
7. Record the outcomes for every listed object, sorted.

- [ ] **Step 1: Unit tests** (`source_test.go`) with a fake reader: `sigs.k8s.io/controller-runtime/pkg/client/fake` with the v1alpha1 scheme. Use plan 1's fixture `internal/factory/resources/testdata/file.yaml` as `File`. Write these tests:
  - `TestSyncServesTheFileAloneWithNoObjects`
  - `TestSyncAddsARepository`: a Repository and its MergePolicy are published; `GenerationsFor` returns their generations.
  - `TestSyncKeepsTheLastGoodVersion`: sync with a valid MergePolicy at generation 1. Update it to an invalid spec (live and shadow on one class) at generation 2 and sync again. The repository is still served with generation 1's classes. The outcome is `Accepted=false, ServedGeneration=1, Reason=Invalid`.
  - `TestSyncDropsADeletedObject`.
  - `TestSyncAtStartupWithNothingServableFails`: a migrated file (the residual of plan 1's `Migrate`) plus an invalid Factory makes `Sync` return an error, and `Live` stays nil.
  - `TestSyncWithoutCRDsRunsFromTheFile`: a reader whose `List` returns a `meta.NoKindMatchError`.

  Set `Generation` explicitly on fake objects: the fake client does not bump it.
- [ ] **Step 2: envtest** (`source_envtest_test.go`, using the existing envtest pattern from `internal/factory/reconciler/factoryresources_envtest_test.go`). Create a Repository and a MergePolicy for real. Start `Source` with a manager cache and `Watch`. Assert within 10s (`Eventually`-style polling) that `Live.Current().RepositoryFor(...)` is non-nil. Update the MergePolicy (add a class) and assert it appears. Delete it and assert the repository is gone.
- [ ] **Step 3:** Implement. `kick` is a buffered channel of size 1, and `Kick` sends with `select { case ch <- struct{}{}: default: }`. `Start` loops on `kick` and a `time.Ticker(Resync)`; a Sync error is logged, not returned. `Watch` calls `c.GetInformer(ctx, &v1alpha1.Factory{})` (and the other two), with `AddEventHandler` on add, update and delete → `Kick`. A `NoMatch` error is skipped.
- [ ] **Step 4:** Tests pass with `-race -p 1`. `task check` passes. Commit `feat(configsource): build and publish the factory's config from its resources`.

---

### Task 4: The factory runs on the live config

**Files:**
- Modify: `internal/app/factory.go`, `internal/app/factory_repos.go`
- Modify: `internal/factory/fence/fence.go`, `internal/factory/meter/meter.go`
- Create: `internal/factory/intake/pollers.go` (the poller supervisor)
- Tests: beside each package's existing tests

**Interfaces:**
- Consumes: `config.Live` (task 1), the `Source` holders (task 2), `configsource.Source` (task 3).
- Produces:
  - `fence.Fence.Repositories` becomes `func() []string`.
  - `meter.Meter` gains `Cfg config.Source`. `Prices`, `RunUSD` and `Budgets` are read from `Cfg.Current()` per tick, and the copied fields are removed. `RunUSD` is derived per tick from `cfg.Tiers` (the same rule as `factory.go`'s `runUSD` loop).
  - `intake.Pollers{New func(repo string) (*IssuePoller, bool)}` with `Start(ctx) error` (a leader-only runnable that keeps the parent context) and `Sync(repos []string)`, which starts each new repository's poller in its own goroutine with a child context, cancels removed ones, and is safe to call before `Start` (it records the wanted set and starts them on `Start`).
  - `repoConns` gains a `sync.RWMutex` and `sync(ctx, cfg *config.Config) error`, which connects new repositories, drops removed ones, and recomputes the merger permission bits.

**Changes, by inventory item:**
1. **Startup order.**
   1. Read the file's raw bytes.
   2. Build a plain `client.Client` (`ctrl.GetConfig()` plus `factoryScheme`) and `configsource.Source{Reader: that client, Namespace: ns, File: raw}`.
   3. `src.Sync(ctx)`. On error, return it (D14).
   4. `live := src.Live`, then `cfg := live.Current()` for the startup fields. Everything that today reads `cfg` at startup reads this `cfg`.
   5. `newFactoryManager(log, ns, cfg.Runs)` as today.

   Add `ByObject` cache entries for the three kinds in `ns` (`factoryManagerOptions`) and register them in `factoryScheme`. After the manager is built: `src.Reader = mgr.GetAPIReader()`, `src.Watch(ctx, mgr.GetCache())`, `mgr.Add(src)`.
2. **Holders:** every `Cfg: cfg` in `runFactory` becomes `Cfg: live`.
3. **`SetClassSource`** (stage 4): `live.Prepare(func(c *config.Config) { c.SetClassSource(w.Classes) })` in place of the direct call.
4. **`repoConns`:** `connectRepositories` becomes `newRepoConns` plus `sync`. On publish (a `live.Subscribe` callback), `sync` connects new repositories and drops removed ones. A failure (a missing merger key for a repository whose class is now live or shadow, an App not installed) logs at error, increments a metric (add `factory_config_wire_errors_total{repository}`), and leaves that repository out of `names()`, so it is not served. At startup the same failure still fails the start, as today.
5. **Fence:** `Repositories: conns.names` (a func). `Refresh` calls it per pass and deletes state for names no longer listed.
6. **Pollers:** replace the startup loop over `cfg.Repos()` with `intake.Pollers`. Its `Sync(conns.names())` runs at start and in a subscriber.
7. **Meter:** `Cfg: live`, and remove the copies.
8. **Metrics:** in a subscriber, call `m.ListRepositories(conns.names()...)`, `m.ChargePeople(next.Budgets.PerPerson.Enabled)`, `m.RunBudgetUSD` for each tier (and 0 for tiers removed since `prev`), and `warnConfig(log, next, m)`.
9. **Flux:** always set `rec.Flux` (an uncached `APIReader`; the monitor already handles no proof).
10. **RunLore:** look up `conns.get(cfg.RunLoreRepository())` per request (give `intake.RunLore` a `Forges func(string) (…, bool)` in place of the captured forge).
11. **demo.GitToken:** `names` becomes `conns.names` (a func).

- [ ] **Step 1: Tests first**, one per change that has behaviour:
  - fence: removed names are dropped and new names are read per pass;
  - meter: a tier added by a publish is priced on the next tick;
  - pollers: `Sync` starts and stops pollers (a fake runnable that counts);
  - `repoConns.sync`: adds and drops; a failing connect leaves the repository out and returns an error;
  - `internal/app`: a test that starts the factory's wiring is likely too heavy. Instead, cover the subscriber function by extracting it as `func onPublish(...)` and testing it with fakes.
- [ ] **Step 2:** Implement in the order 1 → 11, compiling after each.
- [ ] **Step 3:** `go test -race -p 1` on the touched packages, then `task check`.
- [ ] **Step 4:** Commit in two steps: `refactor(factory): live inputs for the fence, the meter and the pollers`, then `feat(factory): serve the config built from its resources and republish it live`.

---

### Task 5: Tasks record the generations they started under

**Files:**
- Modify: `api/factory/v1alpha1/task_types.go` (add the field), the 5 `ConfigHash` writer sites (`reconciler.go:997`, `coordination_child.go:84`, `integration.go:85`, `adopt.go:42`, `split.go:402`)
- Generated: the Task CRD (`task crd:gen`)
- Test: `internal/factory/reconciler/` beside the triage tests

**Interfaces:**
- Produces:

```go
// ConfigGenerations are the generations of the factory's resources a task was triaged under
// (factory resources D5): recorded, not pinned. Zero means the file.
type ConfigGenerations struct {
	Factory     int64 `json:"factory,omitempty"`
	Repository  int64 `json:"repository,omitempty"`
	MergePolicy int64 `json:"mergePolicy,omitempty"`
}
// in TaskStatus, beside ConfigHash:
ConfigGenerations *ConfigGenerations `json:"configGenerations,omitempty"`
```

- [ ] **Step 1:** Write a failing test. A reconciler with a `*config.Config` whose `Generations` has the task's repository at Repository 2 and MergePolicy 5, and Factory 3. Triage a task. Its status has `configGenerations {3, 2, 5}` and `configHash` as today.
- [ ] **Step 2:** Add the type, run `task crd:gen`, and at each of the 5 sites set `ConfigGenerations` from `cfg.GenerationsFor(task's repository)`, next to `ConfigHash`. Leave it nil when all three are zero.
- [ ] **Step 3:** The tests pass and `task check` passes. Commit `feat(api): tasks record the resource generations they were triaged under`.

---

### Task 6: Documentation

**Files:** `docs/architecture.md`, `docs/merge-policies.md` (the "Factory resources (preview)" section from plan 1), `charts/maku-factory/README.md`.

- [ ] Replace "the factory does not read them yet" with how it reads them:
  - every replica builds its config from the file and the resources;
  - a change applies live;
  - a refused change keeps the last good version while the process runs, and an invalid object at startup is not served;
  - which fields are live and which need a rollout (D12, as a table);
  - tasks record `configGenerations`.

  Add a mermaid diagram of file + resources → Source → Live → readers and subscribers. `task docs` passes. Commit `docs: the factory reads its resources live`.

---

## Self-review

- **Spec coverage, plan 2's share:** D5 → tasks 1, 3, 5. D12 → global constraints and task 4. D13 → task 3 (`NeedLeaderElection` false; status is plan 3). D14 → task 3, step 4, and task 4, item 1. D15 → enforced by `Assemble` (plan 1). The no-CRD fallback → task 3. Live wiring of every inventory item → task 4, items 1–11.
- **Risks carried:** the demoted state in `mergepolicy.Watch` stays "until restart" (task 2 records it). A repository removed while a task runs keeps its task. The reconciler's `RepositoryFor` then falls back as today: noted for the final review.
- **Types:** `Source`, `Live`, `Generations`, `RepoGenerations`, `GenerationsFor`, `Outcome`, `configsource.Source`, `ConfigGenerations`: consistent across tasks.
