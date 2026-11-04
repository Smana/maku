# Audit T9 Structure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Untie the two structural knots the audit found: the budget ledger leaves the run-request API's package for its own (A-1, D8), and the AgentRun kind becomes a value each binary injects instead of a process-wide global (A-2, D9).

**Architecture:** A-1 is a pure move: the ledger's code and its own tests go to `internal/factory/ledger`, every importer switches to it, nothing behaves differently. A-2 adds methods to `runwatch.Kind`, then hands the kind to each reader through its constructor or a required field, package by package, each commit green, and only the last commit deletes `current`, `init()`, `Configure` and the package-level accessors.

**Tech Stack:** Go 1.27, controller-runtime (fake client, `informertest`), `gofmt`/`goimports` (`-local github.com/Smana/maku`), GNU `sed -E`.

**Spec:** [`docs/superpowers/specs/2026-11-01-audit-fixes-design.md`](../specs/2026-11-01-audit-fixes-design.md) (theme T9, decisions D0, D8, D9), findings A-1 and A-2 in [`2026-11-01-project-audit.md`](../specs/2026-11-01-project-audit.md). Read both first.

## Before you start: T9 runs after T2, T3, T4 and T5

The spec orders T9 after T2 (factory liveness), T3 (gate and intake), T4 (money) and T5 (shutdown and ownership): they rewrite the same files (the reconciler, intake, `meter.go` and `vm.go`, `api/server.go`'s `Start`, `internal/app/*`). This plan was written against `e14b7949`; every line number below is that commit's.

1. `git switch main && git pull --ff-only`, and check `git log --oneline` shows the T2, T3, T4 and T5 PRs merged. If one is missing, stop: T9 does not start.
2. Branch: `git switch -c refactor/audit-t9-structure`.
3. **Re-grep every call site before each task**, with the command in that task's first step, and compare with the tables here. Anything T2–T5 added (a new ledger symbol, a new `runwatch.GVK()` caller, a changed `parseUsage` signature) is migrated the same way as its neighbours. Code shown here is anchored on function names, not line numbers; where T2–T5 changed a function's body, keep their body and apply only this plan's substitution.
4. T6 (factory TLS) may have merged too and changed `runRequester` in `internal/app/rooms.go`: Task 7 then adds the kind parameter to whatever signature T6 left.

## Spec deviations

| # | Spec says | Reading shows | Plan does |
|---|---|---|---|
| A-1 (scope) | `Release`, `DayUsed`, `SettledColumn`, `SettledByRunDay`, `LedgerDayLayout`, `LedgerPrefix` and their tests move | Those six do not cut the import. `reconciler` also takes `api.Reserve`, `api.Reservation`, `api.ErrMeterBlind`, `api.ErrOverBudget`, `api.ErrPersonOverBudget` (`reconciler.go:971`, `implement.go`); `meter` takes `api.StampMeter`, `api.LedgerAttempts`, `api.AddSettled`, `api.LedgerSpentColumn`, `api.SetLedgerSpentColumn`, `api.LegacyLedgerPrefix`, `api.LedgerReserved`, `api.LedgerDay` (`meter.go`). They all live in `api/ledger.go`, `api/settled.go` and the ledger block of `api/server.go` (`:143-184`, `:642-845`) | The whole ledger surface moves (table in Task 1). Only `Server`'s methods `reserve`, `dropReservation` and `person` stay in `api`, calling `ledger.*` |
| A-1 (names) | the names as listed | In a package named `ledger`, `ledger.LedgerDay` and `ledger.LedgerSpentColumn` are funcs whose name repeats the package: revive's `exported` rule (on in `.golangci.yaml`, no exclusion preset) reports "stutters" | The `Ledger` prefix drops on every moved name (`LedgerPrefix` → `Prefix`, `LedgerDay` → `Day`, `ErrLedgerBusy` → `ErrBusy` …, table in Task 1). Still no behaviour change |
| A-1 (tests) | "their tests move", "tests unchanged" | Some ledger tests drive the HTTP server (`TestTheAPIRefusesWhileTheMeterIsBlind`, `TestTheAPIChargesThePersonOfTheToken`, `TestDailyBudget` …) | Tests that call only ledger functions move; tests that go through `api.Server` stay in `api`. Only identifiers change in either. The union of test names is checked equal before and after |
| A-2 (consumers) | the kind is passed to "the roomctrl, suspend, bridgeapi, runs and runwatch constructors" | `internal/bridgeapi` reads no kind: it uses `runwatch.Run`, `runwatch.Watcher` (through its `Liveness` interface) and `runwatch.BusyScope` only. The readers of the global are `runwatch` (`Watcher`, `Register`, `FromUnstructured`), `roomctrl.deleteRuns`, `suspend.(*Suspender).patch`, `runrequest.Manifest`, `factory/runs` (`Namespace`, `GVK`, `Name`, `Build`, `FromUnstructured`, `Client`), `factory/reconciler.SetupWithManager`, `factory/meter` (`parseUsage`, `VL`, through `RunSubject`), and `internal/app`'s two manager options | The kind reaches exactly those; `bridgeapi` is untouched |
| A-2 (startup test) | — | `internal/app/runs_kind_test.go`'s `TestEntrypointsConfigureTheRunsKind` drives `serveBroker`/`runFactory` until they fail, then reads `runwatch.Current()`. With no global there is nothing to read, and both entrypoints fail (TLS pair, CA file) before they build a component | Replaced by guards that need no global: the wiring functions take the kind as a parameter (a forgotten one does not compile); `roomctrl` and the factory reconciler's `SetupWithManager`, `runrequest.Manifest` and `meter.VL`/`NewVM` refuse a kind that does not validate; both manager-options tests run a native kind; and a final check that no non-test file in `internal/app` names `runwatch.DefaultKind` |

## Global Constraints

- **No behaviour change.** A-1 moves code; A-2 changes where the kind comes from, never what it is. Every existing test keeps its assertions; only identifiers and constructor arguments change.
- **D8:** `go list -deps ./internal/factory/intake ./internal/factory/meter ./internal/factory/reconciler` does not list `github.com/Smana/maku/internal/factory/api`.
- **D9:** `grep -rn 'func init' internal/runwatch` prints nothing; no package-level `current`, `Configure` or `Current`; tests that want a kind build one per instance.
- **AGENTS.md:** packages by domain; consumer-side interfaces; plain constructor injection; no package state beyond `version.Version` (the immutable regexps and `runwatch.DefaultKind` stay); no `init()` doing work; every new `.go` file opens with `// SPDX-License-Identifier: Apache-2.0`; a new package carries a `// Package …` contract comment; exported symbols carry doc comments.
- **Lint through `task lint`**, never a bare `golangci-lint run`. Imports: `goimports -local github.com/Smana/maku -w <files>`.
- **Each commit builds and passes** `go build ./... && go vet ./... && go test -race -count=1 <touched packages>`. The last task runs `task check` (needs Docker).
- **Commits:** Conventional Commits, English, **no** `Co-Authored-By` and no "Generated with" line. A-1 is one commit; A-2 is one commit per task (D0 allows a finding to span commits so each stays green).
- **Branch:** `refactor/audit-t9-structure`.
- **Out of scope:** `AGENTS.md`'s package map and runwatch seam row (T10's A-3 adds `internal/factory/ledger` and the configurable kind once this merges).
- **Test kind for "another runtime":** `runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}`, named `native` in every new test.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/factory/ledger/ledger.go` (new) | package contract; layout constants, `Reservation`, `Reserve` and its errors, `OverDaily`, `DayUsed`, the spent column, reservations | 1 |
| `internal/factory/ledger/release.go` (moved from `api/ledger.go`) | `Release`; the meter's heartbeat (`StampMeter`, `ErrMeterBlind`, `meterBlind`) | 1 |
| `internal/factory/ledger/settled.go` (moved from `api/settled.go`) | `Day`, the settled column, `SettledByRun[Day]` | 1 |
| `internal/factory/ledger/{helpers,release,reserve,person,settled}_test.go` | the ledger's own tests | 1 |
| `internal/factory/api/{server.go,tasks.go,*_test.go}` | admission over `ledger.*`; the runs client in its tests | 1, 7 |
| `internal/factory/{intake,meter,reconciler}/*`, `internal/app/factory.go` | import `ledger`, not `api`, for the ledger | 1 |
| `internal/runwatch/kind.go`, `run.go`, `watcher.go` | `Kind`'s methods; the `Watcher` of one kind; no global | 2, 3, 9 |
| `internal/app/runs.go`, `broker.go` | `wireRuns`, `managerOptions`, `newManager` take the kind; the broker wires `cfg.Runs` | 3–6, 9 |
| `internal/roomctrl/reconciler.go` | `Reconciler.Kind`, required | 4 |
| `internal/suspend/suspend.go` | `Suspender.Kind`, required | 5 |
| `internal/runrequest/runrequest.go`, `internal/app/rooms.go` | `Manifest.Kind`, required; `runRequester` takes the kind | 6 |
| `internal/factory/runs/runs.go`, `internal/factory/reconciler/reconciler.go`, `internal/app/factory.go` | `Build`/`FromUnstructured`/`Scheme` take the kind; `Client.Kind`; the reconciler's `Kind` | 7 |
| `internal/factory/meter/vm.go`, `vl.go` | `NewVM` and `VL` take the kind; `parseUsage` takes the subject | 8 |
| `internal/app/runs_kind_test.go` (deleted), `startup_warnings_test.go` | the global's test goes; `factoryCfg` moves to its last user | 9 |

---

### Task 1: A-1, the budget ledger moves to `internal/factory/ledger`

**Files:**
- Create: `internal/factory/ledger/ledger.go`, `internal/factory/ledger/helpers_test.go`, `internal/factory/ledger/reserve_test.go`, `internal/factory/ledger/person_test.go`
- Move: `internal/factory/api/ledger.go` → `internal/factory/ledger/release.go`; `api/settled.go` → `ledger/settled.go`; `api/settled_test.go` → `ledger/settled_test.go`; `api/ledger_test.go` → `ledger/release_test.go`
- Modify: `internal/factory/api/server.go` (`:143-184`, `:642-845` leave), `api/tasks.go:361`, `api/server_test.go`, `api/person_test.go`, every other `api/*_test.go` that names a ledger symbol
- Modify: every importer: `internal/factory/intake/{issues.go,person_test.go}`, `internal/factory/meter/{meter.go,meter_test.go,release_test.go,settlement_test.go,latecommit_test.go,latecommit_fix_test.go}`, `internal/factory/reconciler/{reconciler.go,implement.go,split.go,caps_test.go,person_test.go,sponsored_test.go,release_test.go,tester_envtest_test.go}`, `internal/app/factory.go`

**Interfaces:**
- Consumes: nothing new.
- Produces, package `github.com/Smana/maku/internal/factory/ledger` (old `api` name → new name):

| `api.` (old) | `ledger.` (new) | Kind |
|---|---|---|
| `LedgerPrefix` | `Prefix` | const `"agent-factory-ledger-usd-"` |
| `LegacyLedgerPrefix` | `LegacyPrefix` | const |
| `LedgerSpent` | `Spent` | const |
| `LedgerReserved` | `Reserved` | const |
| `LedgerSettled` | `Settled` | const |
| `LedgerDayLayout` | `DayLayout` | const `"20060102"` |
| `LedgerAttempts` | `Attempts` | const `5` |
| `LedgerDay(name, prefix string) (string, bool)` | `Day` | func |
| `LedgerSpentColumn(cm) (map[string]int64, error)` | `SpentColumn` | func |
| `SetLedgerSpentColumn(cm, spent) error` | `SetSpentColumn` | func |
| `ErrLedgerBusy` | `ErrBusy` | var |
| `Reservation`, `Reserve`, `Release`, `DayUsed`, `OverDaily`, `ErrOverBudget`, `ErrPersonOverBudget`, `ErrRunBusy`, `ErrRoomBusy`, `ErrMeterBlind`, `MeterHeartbeat`, `StampMeter`, `SettledColumn`, `AddSettled`, `SettledByRun`, `SettledByRunDay` | same name | unchanged signatures |
| unexported `dayUsed`, `ledgerReservations`, `meterBlind`, `meterLastSuccess`, `meterBlindAfter` | same name, in `ledger` | — |

Stays in `api`: `(*Server).reserve`, `(*Server).dropReservation`, `(*Server).person`, `reasonPersonOver`, `keyedID` (which reads `ledger.DayLayout`), `KeyLifetime`.

- [ ] **Step 1: Re-grep and record the baseline**

```bash
grep -rn 'factory/api"' --include=*.go internal cmd
grep -rhoE '\bapi\.[A-Z][A-Za-z0-9_]*' $(grep -rl 'factory/api"' --include=*.go internal cmd) | sort | uniq -c
base=$(mktemp -d)
go test -list '.*' ./internal/factory/api | grep '^Test' | sort > "$base/before.txt"
wc -l "$base/before.txt"
```

Every `api.` symbol printed is either in the table above (it moves) or one of `Server`, `IssueFiler`, `Authenticator`, `GitHubAccess`, `LoginResolver`, `PersonResolver`, `WriteChecker` (it stays). A new ledger symbol T2–T5 added joins the table: it moves, and loses a `Ledger` prefix if it has one.

- [ ] **Step 2: Move the whole files**

```bash
mkdir -p internal/factory/ledger
git mv internal/factory/api/ledger.go       internal/factory/ledger/release.go
git mv internal/factory/api/settled.go      internal/factory/ledger/settled.go
git mv internal/factory/api/settled_test.go internal/factory/ledger/settled_test.go
git mv internal/factory/api/ledger_test.go  internal/factory/ledger/release_test.go
```

- [ ] **Step 3: Cut the ledger out of `api/server.go` into `ledger/ledger.go`**

Create `internal/factory/ledger/ledger.go` with this header:

```go
// SPDX-License-Identifier: Apache-2.0

// Package ledger is the factory's daily budget ledger (R50): one ConfigMap per UTC day in the
// factory's namespace, whose resourceVersion is the transaction, so two replicas cannot take one
// slot. Admission reserves a run's ceiling (Reserve) and drops it when the run ends (Release); the
// meter owns the spent and settled columns and stamps its heartbeat. The run-request API, the
// reconciler's Queued, intake's daily read and the meter all read and write the one encoding
// here. Every amount is micro-USD (TC6). It imports no other factory package.
package ledger
```

Then cut from `internal/factory/api/server.go`, with their doc comments, and paste below that header in this order:

1. the `// Ledger layout (R50). …` comment and its `const ( LedgerPrefix … LedgerAttempts = 5 )` block;
2. `// Reservation is one ledger entry …` and `type Reservation struct`;
3. `// Reserve's refusals. …` and its `var ( ErrOverBudget … ErrLedgerBusy )` block;
4. `func Reserve` with its comment;
5. `func OverDaily`, `func dayUsed`, `func DayUsed`, `func LedgerSpentColumn`, `func SetLedgerSpentColumn`, `func ledgerReservations`, each with its comment.

Leave in `server.go`: `NeedLeaderElection` onward up to `reasonPersonOver`, `person`, `reserve`, and `dropReservation`.

- [ ] **Step 4: Cut the tests that drive only the ledger**

1. From `internal/factory/ledger/release_test.go`, cut `TestTheAPIRefusesWhileTheMeterIsBlind` with its `// #54: the API says why it refuses…` comment, and paste it into `internal/factory/api/server_test.go` right after `TestDailyBudget`.
2. From `internal/factory/api/server_test.go`, cut `TestConcurrentReservationsAdmitOnlyWhatFits` with its `// #78: …` comment into a new `internal/factory/ledger/reserve_test.go` that starts:

```go
// SPDX-License-Identifier: Apache-2.0

package ledger
```

3. From `internal/factory/api/person_test.go`, cut, with their comments, `TestOneEntryChargesThePersonAndTheFleet`, `rvNum`, `TestThePersonDayRefusesFirst`, `TestLabelAndAPIAdmissionsShareThePersonDay` and `TestAPersonWhoIsThePrincipalCountsOnce` into a new `internal/factory/ledger/person_test.go` that starts:

```go
// SPDX-License-Identifier: Apache-2.0

package ledger

const alice = "github:alice"
```

`api/person_test.go` keeps its own `const alice`, `personLinks`, `personServer`, `TestTheAPIChargesThePersonOfTheToken` and `entry`.

4. Create `internal/factory/ledger/helpers_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// now is the tests' clock: the day ledger it names is agent-factory-ledger-usd-20260927, as in
// the API's tests.
var now = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

// scheme knows ConfigMaps, the ledger's one kind.
func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}
```

- [ ] **Step 5: Rename inside the new package**

```bash
sed -i -E \
  -e 's/^package api$/package ledger/' \
  -e 's/\bLegacyLedgerPrefix\b/LegacyPrefix/g' \
  -e 's/\bLedgerPrefix\b/Prefix/g' \
  -e 's/\bSetLedgerSpentColumn\b/SetSpentColumn/g' \
  -e 's/\bLedgerSpentColumn\b/SpentColumn/g' \
  -e 's/\bLedgerSpent\b/Spent/g' \
  -e 's/\bLedgerReserved\b/Reserved/g' \
  -e 's/\bLedgerSettled\b/Settled/g' \
  -e 's/\bLedgerDayLayout\b/DayLayout/g' \
  -e 's/\bLedgerAttempts\b/Attempts/g' \
  -e 's/\bLedgerDay\b/Day/g' \
  -e 's/\bErrLedgerBusy\b/ErrBusy/g' \
  internal/factory/ledger/*.go
```

`\b` keeps `LegacyLedgerPrefix`, `SetLedgerSpentColumn` and `LedgerDayLayout` from matching a shorter rule. Doc comments follow (`// LedgerDay is …` becomes `// Day is …`), as revive wants.

- [ ] **Step 6: Free the name `ledger` where a local variable holds it**

A local named `ledger` would shadow the package in the statements that now say `ledger.Prefix`:

- `internal/factory/api/server_test.go`, `TestTwoAdmissionsOneSlot`: rename the local `ledger` to `day` in its three uses (`ledger := &corev1.ConfigMap{…}`, `objs = append(objs, ledger)`, `ledger.Name`).
- `internal/factory/reconciler/sponsored_test.go` (~:250): rename `ledger := func(o client.Object) bool {…}` to `isLedger := …`, and its call `if ledger(o) {` (~:264) to `if isLedger(o) {`.

Re-check with `grep -rnE '\bledger\s*:?=' --include=*.go internal/factory internal/app`: it prints nothing outside `internal/factory/ledger` (struct fields such as `m.ledger` and `Ledger:` are selectors and harmless).

- [ ] **Step 7: Qualify the moved names inside `api`**

```bash
sed -i -E \
  -e 's/\bLegacyLedgerPrefix\b/ledger.LegacyPrefix/g' \
  -e 's/\bLedgerPrefix\b/ledger.Prefix/g' \
  -e 's/\bSetLedgerSpentColumn\b/ledger.SetSpentColumn/g' \
  -e 's/\bLedgerSpentColumn\b/ledger.SpentColumn/g' \
  -e 's/\bLedgerSpent\b/ledger.Spent/g' \
  -e 's/\bLedgerReserved\b/ledger.Reserved/g' \
  -e 's/\bLedgerSettled\b/ledger.Settled/g' \
  -e 's/\bLedgerDayLayout\b/ledger.DayLayout/g' \
  -e 's/\bLedgerAttempts\b/ledger.Attempts/g' \
  -e 's/\bLedgerDay\b/ledger.Day/g' \
  -e 's/\bErrLedgerBusy\b/ledger.ErrBusy/g' \
  -e 's/\b(Reservation|Reserve|Release|DayUsed|OverDaily|ErrOverBudget|ErrPersonOverBudget|ErrRunBusy|ErrRoomBusy|ErrMeterBlind|MeterHeartbeat|StampMeter|SettledColumn|AddSettled|SettledByRunDay|SettledByRun)\b/ledger.\1/g' \
  internal/factory/api/*.go
```

Then `grep -nE 'ledger\.ledger\.|\.ledger\.' internal/factory/api/*.go` must print nothing: a hit is a selector the sed qualified twice (fix it by hand). `(*Server).reserve` is lower-case and untouched.

- [ ] **Step 8: Switch the importers**

```bash
files=$(grep -rl --include=*.go 'internal/factory/api"' internal cmd)
sed -i -E \
  -e 's/\bapi\.LegacyLedgerPrefix\b/ledger.LegacyPrefix/g' \
  -e 's/\bapi\.LedgerPrefix\b/ledger.Prefix/g' \
  -e 's/\bapi\.SetLedgerSpentColumn\b/ledger.SetSpentColumn/g' \
  -e 's/\bapi\.LedgerSpentColumn\b/ledger.SpentColumn/g' \
  -e 's/\bapi\.LedgerSpent\b/ledger.Spent/g' \
  -e 's/\bapi\.LedgerReserved\b/ledger.Reserved/g' \
  -e 's/\bapi\.LedgerSettled\b/ledger.Settled/g' \
  -e 's/\bapi\.LedgerDayLayout\b/ledger.DayLayout/g' \
  -e 's/\bapi\.LedgerAttempts\b/ledger.Attempts/g' \
  -e 's/\bapi\.LedgerDay\b/ledger.Day/g' \
  -e 's/\bapi\.ErrLedgerBusy\b/ledger.ErrBusy/g' \
  -e 's/\bapi\.(Reservation|Reserve|Release|DayUsed|OverDaily|ErrOverBudget|ErrPersonOverBudget|ErrRunBusy|ErrRoomBusy|ErrMeterBlind|MeterHeartbeat|StampMeter|SettledColumn|AddSettled|SettledByRunDay|SettledByRun)\b/ledger.\1/g' \
  $files
```

This also rewrites comments that name the symbols (`reconciler.go`'s `case errors.Is(err, api.ErrMeterBlind): // …`), which is wanted.

- [ ] **Step 9: Fix the imports**

```bash
goimports -local github.com/Smana/maku -w internal/factory/ledger/*.go internal/factory/api/*.go $files
```

`goimports` drops the now-unused `api` import from `meter`, `intake/issues.go`, `reconciler/*.go` and adds `github.com/Smana/maku/internal/factory/ledger`. Where it cannot resolve `ledger` (a cold module cache), add the import by hand in the Smana group.

- [ ] **Step 10: Build, test, and prove nothing was lost**

```bash
go build ./... && go vet ./internal/factory/... ./internal/app/...
go test -race -count=1 ./internal/factory/... ./internal/app/...
go test -list '.*' ./internal/factory/api ./internal/factory/ledger | grep '^Test' | sort > "$base/after.txt"
diff "$base/before.txt" "$base/after.txt" && echo "same tests"
go list -deps ./internal/factory/intake ./internal/factory/meter ./internal/factory/reconciler | grep -x 'github.com/Smana/maku/internal/factory/api' || echo "D8 holds"
go list -deps ./internal/factory/ledger | grep 'github.com/Smana/maku/' | grep -v 'internal/factory/ledger$' || echo "ledger imports no maku package"
```

Expected: build and tests PASS; `same tests`; `D8 holds`; `ledger imports no maku package` (its tests import `factory/runs` for `runs.PrincipalFactory`, which `-deps` without `-test` does not list).

- [ ] **Step 11: Lint**

Run: `task lint`
Expected: exit 0. A `stutters` report names a symbol Step 5 missed; rename it the same way.

- [ ] **Step 12: Commit**

```bash
git add -A internal/factory internal/app
git commit -m "refactor(factory): the budget ledger moves to internal/factory/ledger, so domain packages stop importing the API (A-1)"
```

---

### Task 2: A-2, a `runwatch.Kind` answers for itself

**Files:**
- Modify: `internal/runwatch/kind.go` (add methods after `ValidatePrefix`; `newState`; package `GVK`)
- Modify: `internal/runwatch/run.go:77-81` (`FromUnstructured`)
- Test: `internal/runwatch/kind_test.go`

**Interfaces:**
- Consumes: `Kind{Group, Namespace, NamePrefix}`, `kindClaim(apiVersion, ns, name)` (test helper in `kind_test.go`).
- Produces:
  - `func (k Kind) GVK() schema.GroupVersionKind` (Version `v1alpha1`, Kind `AgentRun`)
  - `func (k Kind) ClaimName(id string) string` = `k.NamePrefix + id`
  - `func (k Kind) RunSubject() *regexp.Regexp` (compiles on each call)
  - `func (k Kind) FromUnstructured(u *unstructured.Unstructured) (Run, bool)`
  - the package-level `GVK()`, `Namespace()`, `ClaimPrefix()`, `RunSubject()`, `FromUnstructured(u)` still exist and delegate to `Current()` until Task 9.

- [ ] **Step 1: Re-grep**

Run: `grep -rnE 'runwatch\.(GVK|Namespace|ClaimPrefix|Configure|Current|RunSubject|FromUnstructured|DefaultKind|New)\b' --include=*.go internal cmd | grep -v '^internal/runwatch/' | wc -l` and keep the number: Task 9 drives it to `DefaultKind` and `New` only.

- [ ] **Step 2: Write the failing test**

In `internal/runwatch/kind_test.go`, add `"k8s.io/apimachinery/pkg/runtime/schema"` to the imports, and append:

```go
// D9: a kind answers for itself, whatever kind the process was configured with.
func TestAKindAnswersForItself(t *testing.T) {
	native := Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	if g := native.GVK(); g != (schema.GroupVersionKind{Group: "maku.ogenki.io", Version: "v1alpha1", Kind: "AgentRun"}) {
		t.Fatalf("GVK %v", g)
	}
	if n := native.ClaimName("abcdefgh"); n != "run-abcdefgh" {
		t.Fatalf("claim name %q", n)
	}
	if m := native.RunSubject().FindStringSubmatch("system:serviceaccount:runs:run-abcdefgh"); len(m) != 2 || m[1] != "abcdefgh" {
		t.Fatalf("subject match %v", m)
	}
	if native.RunSubject().MatchString("system:serviceaccount:agents:xplane-run-abcdefgh") {
		t.Fatal("another kind's subject matched")
	}
	if r, ok := native.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); !ok || r.ID != "abcdefgh" {
		t.Fatalf("native claim: ok=%v id=%q", ok, r.ID)
	}
	for _, u := range []*unstructured.Unstructured{
		kindClaim("maku.ogenki.io/v1alpha1", "runs", "xplane-run-abcdefgh"), // another kind's prefix
		kindClaim("maku.ogenki.io/v1alpha1", "agents", "run-abcdefgh"),     // another namespace
	} {
		if _, ok := native.FromUnstructured(u); ok {
			t.Errorf("%s/%s read as a native run", u.GetNamespace(), u.GetName())
		}
	}
	if _, ok := DefaultKind.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); ok {
		t.Fatal("the default kind read a native run")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test -race -count=1 ./internal/runwatch -run TestAKindAnswersForItself`
Expected: FAIL to compile: `native.GVK undefined (type Kind has no field or method GVK)`.

- [ ] **Step 4: Add the methods**

In `internal/runwatch/kind.go`, after `ValidatePrefix`, add:

```go
// GVK is k's AgentRun kind (C3).
func (k Kind) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: k.Group, Version: "v1alpha1", Kind: "AgentRun"}
}

// ClaimName is the name of run id's claim under k: <prefix><C2 id>.
func (k Kind) ClaimName(id string) string { return k.NamePrefix + id }

// RunSubject matches a run's ServiceAccount token subject under k, capturing the run id: the
// ServiceAccount is named exactly like the run, on both runtimes. It compiles on each call, so a
// caller that matches many subjects keeps the result.
func (k Kind) RunSubject() *regexp.Regexp {
	return regexp.MustCompile("^system:serviceaccount:" + regexp.QuoteMeta(k.Namespace) + ":" +
		regexp.QuoteMeta(k.NamePrefix) + "([a-z2-7]{8})$")
}
```

Replace `newState`'s body with `return &state{kind: k, subject: k.RunSubject()}`, and the package-level `GVK` with:

```go
// GVK is the configured kind's AgentRun kind (C3).
func GVK() schema.GroupVersionKind { return Current().GVK() }
```

In `internal/runwatch/run.go`, replace the head of `FromUnstructured`:

```go
// FromUnstructured reads exactly the C3 fields SP2 relies on. ok is false for a
// claim that is not a run: not named <prefix><C2 id>, or outside the namespace.
func FromUnstructured(u *unstructured.Unstructured) (Run, bool) {
	id, ok := strings.CutPrefix(u.GetName(), ClaimPrefix())
	if !ok || !envelope.ValidID(id) || u.GetNamespace() != Namespace() {
		return Run{}, false
	}
```

with:

```go
// FromUnstructured reads u under the configured kind (Current).
func FromUnstructured(u *unstructured.Unstructured) (Run, bool) { return Current().FromUnstructured(u) }

// FromUnstructured reads exactly the C3 fields SP2 relies on. ok is false for a claim that is not
// one of k's runs: not named <prefix><C2 id>, or outside k's namespace.
func (k Kind) FromUnstructured(u *unstructured.Unstructured) (Run, bool) {
	id, ok := strings.CutPrefix(u.GetName(), k.NamePrefix)
	if !ok || !envelope.ValidID(id) || u.GetNamespace() != k.Namespace {
		return Run{}, false
	}
```

The rest of the body is unchanged.

- [ ] **Step 5: Run the package's tests**

Run: `go test -race -count=1 ./internal/runwatch && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/runwatch/kind.go internal/runwatch/run.go internal/runwatch/kind_test.go
git commit -m "refactor(runwatch): a Kind answers for itself: its GVK, claim names, run subjects and claims (A-2)"
```

---

### Task 3: A-2, the `Watcher` and the broker's manager take the kind

**Files:**
- Modify: `internal/runwatch/watcher.go` (`Watcher`, `New`, `Upsert`, `Remove`, `Register`)
- Modify: `internal/app/runs.go` (`wireRuns`), `internal/app/broker.go` (`serveBroker`, `managerOptions`, `newManager`)
- Modify: every `*_test.go` calling `runwatch.New()` / `New()`; `internal/app/runs_test.go` (`TestWireRuns`, `TestManagerOptions`)
- Test: `internal/runwatch/runwatch_test.go`

**Interfaces:**
- Consumes: `Kind.GVK()`, `Kind.FromUnstructured` (Task 2).
- Produces: `func New(k Kind) *Watcher`; `func wireRuns(ctx context.Context, kind runwatch.Kind, src informerSource, add func(manager.Runnable) error, log *slog.Logger, open runwatch.Unfinished, events *runwatch.Events, tick func(time.Duration) (<-chan time.Time, func())) (runWiring, error)`; `func managerOptions(ns string, kind runwatch.Kind) ctrl.Options`; `func newManager(log *slog.Logger, ns string, kind runwatch.Kind) (ctrl.Manager, error)`.

- [ ] **Step 1: Re-grep**

Run: `grep -rnE 'runwatch\.New\(|wireRuns\(|managerOptions\(|newManager\(' --include=*.go internal cmd`
Every hit is in the files above or a `*_test.go`; a production caller outside `internal/app` gets the kind it is handed, never `runwatch.DefaultKind`.

- [ ] **Step 2: Write the failing test**

In `internal/runwatch/runwatch_test.go`, after `TestRegisterFeedsTheWatcherFromTheInformer`, add:

```go
// D9: a watcher reads its own kind: it registers on that kind's informer and takes only that
// kind's claims.
func TestAWatcherReadsItsOwnKind(t *testing.T) {
	native := Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	inf := controllertest.NewFakeInformer()
	informers := &informertest.FakeInformers{InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{native.GVK(): inf}}
	w := New(native)
	if _, err := Register(t.Context(), informers, w); err != nil {
		t.Fatal(err)
	}
	inf.Add(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh"))
	inf.Add(kindClaim("cloud.ogenki.io/v1alpha1", "agents", "xplane-run-7f3cq2xz"))
	if _, ok := w.Get("abcdefgh"); !ok {
		t.Fatal("the native run is not watched: Register took another kind's informer")
	}
	if _, ok := w.Get("7f3cq2xz"); ok {
		t.Fatal("another kind's claim was taken")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test -race -count=1 ./internal/runwatch -run TestAWatcherReadsItsOwnKind`
Expected: FAIL to compile: `too many arguments in call to New`.

- [ ] **Step 4: The watcher holds its kind**

In `internal/runwatch/watcher.go`, add to `Watcher`, first field:

```go
	kind    Kind // the runs it reads: claims of any other kind are not runs to it
```

Replace `New`:

```go
// New returns an empty Watcher of k's runs.
func New(k Kind) *Watcher { return &Watcher{kind: k, runs: map[string]Run{}} }
```

In `Upsert` replace `cur, ok := FromUnstructured(u)` with `cur, ok := w.kind.FromUnstructured(u)`; in `Remove` replace `r, ok := FromUnstructured(u)` with `r, ok := w.kind.FromUnstructured(u)`; in `Register` replace `u.SetGroupVersionKind(GVK())` with `u.SetGroupVersionKind(w.kind.GVK())`, and say so in its comment: `// Register attaches the watcher to the cache's informer for its kind's AgentRuns. ctx is`.

- [ ] **Step 5: Every test that builds a watcher names its kind**

```bash
sed -i 's/runwatch\.New()/runwatch.New(runwatch.DefaultKind)/g' $(grep -rl 'runwatch\.New()' --include='*_test.go' internal cmd)
sed -i -E 's/(^|[^.[:alnum:]_])New\(\)/\1New(DefaultKind)/g' internal/runwatch/*_test.go
```

The second line leaves `redact.New()` and `controllertest.NewFakeInformer()` alone (a `.` or more letters precede).

- [ ] **Step 6: The broker hands the watch and the cache its config's kind**

`internal/app/runs.go`: `wireRuns` gains `kind runwatch.Kind` after `ctx`, documented in its comment (`… kind is the config's runs kind: the watch reads only its claims.`), and builds `watch := runwatch.New(kind)`.

`internal/app/broker.go`:

```go
// managerOptions watches Rooms in the broker's namespace and kind's AgentRuns in theirs
// only: the RBAC (Task 1.18) grants exactly that, so a cluster-wide informer
// would be refused. Unstructured reads stay uncached, so the finalizer's run
// list and deletes see the API server, not the watch.
func managerOptions(ns string, kind runwatch.Kind) ctrl.Options {
	runObj := &unstructured.Unstructured{}
	runObj.SetGroupVersionKind(kind.GVK())
```

and in its `Cache` the run entry becomes `runObj: {Namespaces: map[string]cache.Config{kind.Namespace: {}}},`. `newManager(log *slog.Logger, ns string, kind runwatch.Kind)` calls `opts := managerOptions(ns, kind)`. In `serveBroker`: `mgr, err := newManager(log, ns, cfg.Runs)` and `rw, err := wireRuns(ctx, cfg.Runs, mgr.GetCache(), mgr.Add, log, st, events, nil)`.

- [ ] **Step 7: The app's tests pass a kind, and one is native**

`internal/app/runs_test.go`, `TestWireRuns`: its three calls become `wireRuns(ctx, runwatch.DefaultKind, informers, …)`, `wireRuns(ctx, runwatch.DefaultKind, &informertest.FakeInformers{Error: …}, …)`, `wireRuns(ctx, runwatch.DefaultKind, &informertest.FakeInformers{}, …)`, and its map key `runwatch.GVK()` becomes `runwatch.DefaultKind.GVK()`.

`TestManagerOptions`: replace the loop header

```go
	for _, ns := range []string{"agent-system", "rooms-test"} {
		t.Run(ns, func(t *testing.T) {
			o := managerOptions(ns)
```

with

```go
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	for _, tc := range []struct {
		ns string
		rk runwatch.Kind // the runs kind: the loop below has its own kind
	}{{"agent-system", runwatch.DefaultKind}, {"rooms-test", native}} {
		ns, rk := tc.ns, tc.rk
		t.Run(ns, func(t *testing.T) {
			o := managerOptions(ns, rk)
```

and in its body `runwatch.GVK().Kind: {runwatch.Namespace()}` becomes `rk.GVK().Kind: {rk.Namespace}`, `u.GroupVersionKind() != runwatch.GVK()` becomes `u.GroupVersionKind() != rk.GVK()`.

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go test -race -count=1 ./internal/runwatch ./internal/app ./internal/roomctrl ./internal/humanapi ./internal/bridgeapi ./internal/mcp ./internal/bridge`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add -A internal
git commit -m "refactor(runwatch): the Watcher and the broker's manager read the runs kind they are given (A-2)"
```

---

### Task 4: A-2, `roomctrl.Reconciler` lists a deleted room's runs by its `Kind`

**Files:**
- Modify: `internal/roomctrl/reconciler.go` (`Reconciler`, `deleteRuns`, `SetupWithManager`)
- Modify: `internal/app/broker.go` (`serveBroker`'s `rc := &roomctrl.Reconciler{…}`)
- Test: `internal/roomctrl/reconciler_test.go`

**Interfaces:**
- Consumes: `Kind.GVK`, `Kind.FromUnstructured`, `Kind.Validate`, `runwatch.New(k)`.
- Produces: `roomctrl.Reconciler.Kind runwatch.Kind`, required; `SetupWithManager` returns an error naming `Kind` when it does not validate.

- [ ] **Step 1: Re-grep**

Run: `grep -rn 'Reconciler{' --include=*.go internal/roomctrl internal/app | grep -v factory`
The broker's `rc := &roomctrl.Reconciler{…}` and `broker_test.go`'s `TestWithSuspend` are the non-roomctrl ones.

- [ ] **Step 2: Every test reconciler gets the default kind**

```bash
sed -i -E 's/&Reconciler\{/\&Reconciler{Kind: runwatch.DefaultKind, /g' internal/roomctrl/*_test.go
```

- [ ] **Step 3: Write the failing tests**

In `TestSetupRefusesMissingDependencies`, add a case to the table:

```go
		{"Kind", func(r *Reconciler) { r.Kind = runwatch.Kind{} }},
```

After `TestDeleteRecordsRunEndsBeforeTheSeal`, add:

```go
// D9: a deletion lists and records the runs of the kind the reconciler was given, not a
// process-wide one.
func TestDeleteListsTheRunsOfItsKind(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(native.GVK(), meta.RESTScopeNamespace)
	m.Add(v1alpha1.GroupVersion.WithKind("Room"), meta.RESTScopeNamespace)
	run := &unstructured.Unstructured{}
	run.SetGroupVersionKind(native.GVK())
	run.SetName(native.ClaimName("7f3cq2xz"))
	run.SetNamespace(native.Namespace)
	_ = unstructured.SetNestedField(run.Object, roomID, "spec", "roomRef")
	_ = unstructured.SetNestedField(run.Object, "Failed", "status", "phase")
	run.SetFinalizers([]string{"test/hold"})
	c := fake.NewClientBuilder().WithScheme(scheme()).WithRESTMapper(m).
		WithObjects(deletingRoom(), run).WithStatusSubresource(&v1alpha1.Room{}).Build()
	var ops []string
	r := &Reconciler{Kind: native, Client: c, Store: newStore(&ops, store.RoomState{ID: roomID}), Runs: runwatch.New(native),
		Ends: fakeEnds{&ops}, Now: clock(deletedAt.Add(time.Second))}
	if _, err := r.Reconcile(t.Context(), request()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"end:7f3cq2xz:Failed", "close"}; !slices.Equal(ops, want) {
		t.Fatalf("ops = %v, want %v", ops, want)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test -race -count=1 ./internal/roomctrl`
Expected: FAIL to compile: `unknown field Kind in struct literal of type Reconciler`.

- [ ] **Step 5: The reconciler reads its kind**

In `Reconciler`, after `Runs runIndex`, add:

```go
	// Kind is the runs kind, required: a deletion lists and reads the room's runs as its claims.
	Kind runwatch.Kind
```

In `deleteRuns`:

```go
	list.SetGroupVersionKind(r.Kind.GVK().GroupVersion().WithKind(r.Kind.GVK().Kind + "List"))
```

```go
	if err := reader.List(ctx, list, client.InNamespace(r.Kind.Namespace)); err != nil {
```

```go
		run, ok := r.Kind.FromUnstructured(u)
```

In `SetupWithManager`, first:

```go
	if err := r.Kind.Validate(); err != nil {
		return fmt.Errorf("roomctrl: the reconciler needs Kind: %w", err)
	}
```

In `internal/app/broker.go`, the room controller gets the config's kind: `rc := &roomctrl.Reconciler{Kind: cfg.Runs, Client: mgr.GetClient(), …}`.

- [ ] **Step 6: Run the tests**

Run: `go build ./... && go test -race -count=1 ./internal/roomctrl ./internal/app`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/roomctrl internal/app/broker.go
git commit -m "refactor(roomctrl): the Reconciler lists a deleted room's runs by its injected kind (A-2)"
```

---

### Task 5: A-2, the `Suspender` patches claims of its `Kind`

**Files:**
- Modify: `internal/suspend/suspend.go` (`Suspender`, `patch`)
- Modify: `internal/app/broker.go` (`withSuspend`), `internal/app/broker_test.go` (`TestWithSuspend`)
- Test: `internal/suspend/suspend_test.go`

**Interfaces:**
- Consumes: `Kind.GVK`, `Kind.ClaimName`; `roomctrl.Reconciler.Kind` (Task 4).
- Produces: `suspend.Suspender.Kind runwatch.Kind`, required; `withSuspend` copies `rc.Kind`.

- [ ] **Step 1: Re-grep**

Run: `grep -rn 'Suspender{' --include=*.go internal`

- [ ] **Step 2: Every test suspender gets the default kind**

```bash
sed -i -E 's/&Suspender\{/\&Suspender{Kind: runwatch.DefaultKind, /g' internal/suspend/*_test.go
```

- [ ] **Step 3: Write the failing tests**

In `internal/suspend/suspend_test.go`, after `TestRoomSuspendsAndResumes`, add:

```go
// D9: the claim patched is one of the Suspender's kind, wherever the default kind points.
func TestRoomPatchesTheClaimOfItsKind(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(native.GVK(), meta.RESTScopeNamespace)
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(native.GVK())
	claim.SetNamespace(native.Namespace)
	claim.SetName(native.ClaimName("7f3cq2xz"))
	_ = unstructured.SetNestedField(claim.Object, true, "spec", "suspendable")
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithRESTMapper(m).WithObjects(claim).Build()
	key := types.NamespacedName{Namespace: native.Namespace, Name: native.ClaimName("7f3cq2xz")}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(native.GVK())
	if err := c.Get(t.Context(), key, u); err != nil {
		t.Fatal(err)
	}
	r := running()
	r.ResourceVersion = u.GetResourceVersion()
	s := &Suspender{Kind: native, Client: c, Approvals: waitingOn("7f3cq2xz"), After: 5 * time.Minute, Now: func() time.Time { return now }}
	if err := s.Room(t.Context(), room("attended"), now.Add(-10*time.Minute), []runwatch.Run{r}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), key, u); err != nil {
		t.Fatal(err)
	}
	if got := u.GetAnnotations()[runwatch.OperatingModeAnnotation]; got != ModeSuspended {
		t.Fatalf("mode %q on the native claim", got)
	}
}
```

In `internal/app/broker_test.go`, `TestWithSuspend`: make the reconciler native and check the copy. Replace `rc := &roomctrl.Reconciler{}` with

```go
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	rc := &roomctrl.Reconciler{Kind: native}
```

and the last check with

```go
	if s, ok := rc.Suspend.(*suspend.Suspender); !ok || s.After != 7*time.Minute || s.Kind != native {
		t.Fatalf("on: %#v", rc.Suspend)
	}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test -race -count=1 ./internal/suspend ./internal/app -run 'TestRoomPatchesTheClaimOfItsKind|TestWithSuspend'`
Expected: FAIL to compile: `unknown field Kind in struct literal of type Suspender`.

- [ ] **Step 5: The suspender reads its kind**

In `Suspender`, first field:

```go
	// Kind is the runs kind, required: the claims patched are its.
	Kind runwatch.Kind
```

In `patch`:

```go
	u.SetGroupVersionKind(s.Kind.GVK())
	u.SetNamespace(s.Kind.Namespace)
	u.SetName(s.Kind.ClaimName(run.ID))
```

In `internal/app/broker.go`'s `withSuspend`: `rc.Suspend = &suspend.Suspender{Kind: rc.Kind, Client: c, Approvals: a, After: cfg.After.Duration, Log: log}`, and its comment gains `The trigger patches claims of the room controller's kind.`

- [ ] **Step 6: Run the tests**

Run: `go build ./... && go test -race -count=1 ./internal/suspend ./internal/app`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/suspend internal/app/broker.go internal/app/broker_test.go
git commit -m "refactor(suspend): the Suspender patches claims of the room controller's kind (A-2)"
```

---

### Task 6: A-2, `runrequest.Manifest` renders a claim of its `Kind`

**Files:**
- Modify: `internal/runrequest/runrequest.go` (`Manifest`, `Request`)
- Modify: `internal/app/rooms.go` (`runRequester`, `humanSide`)
- Test: `internal/runrequest/runrequest_test.go`, `internal/app/rooms_test.go` (`TestRunRequester`); callers of `runrequest.Manifest{}` in `internal/humanapi/{startrun_test,fork_test}.go`, `internal/app/{approvals_test,rooms_test}.go`

**Interfaces:**
- Consumes: `Kind.GVK`, `Kind.ClaimName`, `Kind.Validate`, `Kind.FromUnstructured`.
- Produces: `runrequest.Manifest{Kind runwatch.Kind}` (value receiver `Request` refuses a kind that does not validate); `func runRequester(factoryURL string, kind runwatch.Kind) runrequest.Requester`.

- [ ] **Step 1: Re-grep**

Run: `grep -rnE 'Manifest\{\}|runRequester\(' --include=*.go internal`
If T6 changed `runRequester`'s signature, keep T6's parameters and add `kind runwatch.Kind` last.

- [ ] **Step 2: Callers name the default kind**

```bash
sed -i 's/runrequest\.Manifest{}/runrequest.Manifest{Kind: runwatch.DefaultKind}/g' $(grep -rl 'runrequest\.Manifest{}' --include='*_test.go' internal)
sed -i 's/\bManifest{}\.Request/Manifest{Kind: runwatch.DefaultKind}.Request/g' internal/runrequest/runrequest_test.go
goimports -local github.com/Smana/maku -w $(grep -rl 'Manifest{Kind: runwatch.DefaultKind}' --include='*_test.go' internal)
```

- [ ] **Step 3: Write the failing tests**

Replace `TestManifestReadsBackUnderBothKinds` in `internal/runrequest/runrequest_test.go` with:

```go
// One rendered manifest is a run to either runtime's reader (adoption 3a, R2), each kind given
// per instance: there is no process-wide kind to set (D9). Without a kind it refuses.
func TestManifestReadsBackUnderBothKinds(t *testing.T) {
	for _, k := range []runwatch.Kind{runwatch.DefaultKind, {Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"}} {
		res, err := Manifest{Kind: k}.Request(t.Context(), Request{Role: "implementer", Repository: "o/r",
			BaseRef: "4be1c9d", Branch: "agent/abcdefgh", TaskText: "t", DataClass: "public", RoomRef: "abcdefgh",
			Principal: "human:a"})
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(res.Manifest); err != nil {
			t.Fatal(err)
		}
		if u.GroupVersionKind() != k.GVK() {
			t.Errorf("%s: gvk %v", k.Group, u.GroupVersionKind())
		}
		if r, ok := k.FromUnstructured(u); !ok || r.ID != res.RunID || r.Room != "abcdefgh" {
			t.Errorf("%s: read back ok=%v %+v", k.Group, ok, r)
		}
	}
	if _, err := (Manifest{}).Request(t.Context(), Request{Role: "implementer"}); err == nil {
		t.Error("a manifest without its runs kind must refuse")
	}
}
```

In `internal/app/rooms_test.go`, replace `TestRunRequester`'s first check

```go
	if _, ok := runRequester("").(runrequest.Manifest); !ok {
		t.Fatal("before SP3 the claim is rendered")
	}
	f, ok := runRequester("https://factory.example").(runrequest.Factory)
```

with

```go
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	if m, ok := runRequester("", native).(runrequest.Manifest); !ok || m.Kind != native {
		t.Fatalf("before SP3 the claim is rendered, of the config's kind: %#v", m)
	}
	f, ok := runRequester("https://factory.example", native).(runrequest.Factory)
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test -race -count=1 ./internal/runrequest ./internal/app`
Expected: FAIL to compile: `unknown field Kind in struct literal of type Manifest`.

- [ ] **Step 5: The manifest renders its kind's claim**

`internal/runrequest/runrequest.go`, replace `Manifest` and the head of its `Request`:

```go
// Manifest renders the AgentRun claim for the owner to create (ruling P14), of Kind: its GVK and
// namespace, and the name prefix runwatch parses.
type Manifest struct {
	// Kind is the runs kind, required: the claim rendered is one of its runs.
	Kind runwatch.Kind
}

// Request renders r as a claim, its run id derived from r's idempotency key.
func (m Manifest) Request(_ context.Context, r Request) (Result, error) {
	if err := m.Kind.Validate(); err != nil {
		return Result{}, fmt.Errorf("runrequest: the manifest needs the runs kind: %w", err)
	}
```

and in its body:

```go
	gvk := m.Kind.GVK()
	b, err := json.Marshal(map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]string{"name": m.Kind.ClaimName(id), "namespace": m.Kind.Namespace}, "spec": spec})
```

`internal/app/rooms.go`:

```go
// runRequester is SP3's factory at factoryURL, or before SP3 (unset) the
// manifest renderer (ruling P14), rendering claims of kind.
func runRequester(factoryURL string, kind runwatch.Kind) runrequest.Requester {
	if factoryURL == "" {
		return runrequest.Manifest{Kind: kind}
	}
```

and in `humanSide`: `runRequester(cfg.FactoryURL, cfg.Runs)`.

- [ ] **Step 6: Run the tests**

Run: `go build ./... && go test -race -count=1 ./internal/runrequest ./internal/app ./internal/humanapi`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runrequest internal/app internal/humanapi
git commit -m "refactor(runrequest): the manifest renders the claim of the kind it is given (A-2)"
```

---

### Task 7: A-2, the factory's runs client, reconciler and manager take the kind

**Files:**
- Modify: `internal/factory/runs/runs.go` (delete `Namespace`, `GVK`, `Name`; `Scheme`, `Build`, `FromUnstructured`, `Client`, `empty`, `named`)
- Modify: `internal/factory/reconciler/reconciler.go` (`Reconciler`, `SetupWithManager`)
- Modify: `internal/app/factory.go` (`runFactory`, `factoryManagerOptions`, `newFactoryManager`), `internal/app/factory_test.go` (`TestFactoryManagerOptions`)
- Modify (tests): `internal/factory/runs/*_test.go`, `internal/factory/reconciler/{envtest_test,search_test,checks_test,team_test}.go`, `internal/runctl/envtest_test.go`, `internal/factory/api/server_test.go`
- Create: `internal/factory/reconciler/kind_test.go`

**Interfaces:**
- Consumes: `Kind.GVK`, `Kind.ClaimName`, `Kind.Validate`.
- Produces:
  - `func Scheme(s *runtime.Scheme, k runwatch.Kind)`
  - `func Build(k runwatch.Kind, s Spec) *unstructured.Unstructured`
  - `func FromUnstructured(k runwatch.Kind, u *unstructured.Unstructured) (Run, bool)`
  - `type Client struct { C client.Client; Kind runwatch.Kind }`
  - `reconciler.Reconciler.Kind runwatch.Kind`, required by `SetupWithManager`
  - `func factoryManagerOptions(ns string, kind runwatch.Kind) ctrl.Options`, `func newFactoryManager(log *slog.Logger, ns string, kind runwatch.Kind) (ctrl.Manager, error)`
  - `runs.Namespace()`, `runs.GVK()`, `runs.Name()` no longer exist.

- [ ] **Step 1: Re-grep**

Run: `grep -rnE 'runs\.(Namespace|GVK|Name|Scheme|Build|FromUnstructured)\(|runs\.Client\{' --include=*.go internal cmd`
Production hits must be only `internal/factory/reconciler/reconciler.go` and `internal/app/factory.go`; everything else is a test.

- [ ] **Step 2: Write the failing test**

Create `internal/factory/reconciler/kind_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
)

// D9: the reconciler watches the runs kind it is given; without one it refuses at startup rather
// than watch an AgentRun of no group.
func TestSetupRefusesAMissingRunsKind(t *testing.T) {
	if err := (&Reconciler{}).SetupWithManager(nil); err == nil || !strings.Contains(err.Error(), "runs kind") {
		t.Fatalf("err = %v, want one naming the runs kind", err)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test -race -count=1 ./internal/factory/reconciler -run TestSetupRefusesAMissingRunsKind`
Expected: FAIL: a nil-pointer panic inside `ctrl.NewControllerManagedBy(nil)`: nothing checks the kind before the manager is used.

- [ ] **Step 4: `runs` takes the kind**

In `internal/factory/runs/runs.go`, delete `Namespace()`, `GVK()` and `Name()` with their comments, and replace `Scheme`:

```go
// Scheme registers k's AgentRun as an unstructured kind (the XRD has no Go types).
func Scheme(s *runtime.Scheme, k runwatch.Kind) {
	s.AddKnownTypeWithName(k.GVK(), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(k.GVK().GroupVersion().WithKind("AgentRunList"), &unstructured.UnstructuredList{})
}
```

`Build`: signature `func Build(k runwatch.Kind, s Spec) *unstructured.Unstructured`, comment `// Build is the claim for s, of kind k. It does not validate: Create does, before the claim exists.`, and its three identity lines:

```go
	u.SetGroupVersionKind(k.GVK())
	u.SetNamespace(k.Namespace)
	u.SetName(k.ClaimName(s.RunID))
```

`FromUnstructured`: signature `func FromUnstructured(k runwatch.Kind, u *unstructured.Unstructured) (Run, bool)`, comment `… not named <prefix><C2 id> (runs.namePrefix), or outside runs.namespace, as runwatch's Kind decides.`, and its head:

```go
	id, ok := strings.CutPrefix(u.GetName(), k.NamePrefix)
	if !ok || !envelope.ValidID(id) || u.GetNamespace() != k.Namespace {
		return Run{}, false
	}
```

Replace `Client`, `empty` and `named`:

```go
// Client creates, reads, annotates and deletes claims of Kind, in its namespace.
type Client struct {
	C client.Client
	// Kind is the runs kind, required: every claim read or written is one of its runs.
	Kind runwatch.Kind
}

func (c Client) empty() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(c.Kind.GVK())
	return u
}

func (c Client) named(id string) *unstructured.Unstructured {
	u := c.empty()
	u.SetNamespace(c.Kind.Namespace)
	u.SetName(c.Kind.ClaimName(id))
	return u
}
```

and in the methods: `Create` calls `c.C.Create(ctx, Build(c.Kind, s), opts...)`; `replayOf` and `Get` use `u := c.empty()` and `types.NamespacedName{Namespace: c.Kind.Namespace, Name: c.Kind.ClaimName(<id>)}`; `Get` returns `FromUnstructured(c.Kind, u)`; `List` sets `c.Kind.GVK().GroupVersion().WithKind("AgentRunList")`, lists `client.InNamespace(c.Kind.Namespace)` and reads `FromUnstructured(c.Kind, &l.Items[i])`; `Annotate` and `Delete` use `c.named(id)`.

- [ ] **Step 5: The factory reconciler requires its kind**

In `internal/factory/reconciler/reconciler.go`, next to `Runs RunClient`:

```go
	// Kind is the runs kind, required: SetupWithManager watches its AgentRuns.
	Kind runwatch.Kind
```

and `SetupWithManager` begins:

```go
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Kind.Validate(); err != nil {
		return fmt.Errorf("reconciler: the runs kind: %w", err)
	}
	run := &unstructured.Unstructured{}
	run.SetGroupVersionKind(r.Kind.GVK())
```

- [ ] **Step 6: The factory wires its config's kind**

`internal/app/factory.go`:

- `func factoryManagerOptions(ns string, kind runwatch.Kind) ctrl.Options`: `runObj.SetGroupVersionKind(kind.GVK())` and `runObj: {Namespaces: map[string]cache.Config{kind.Namespace: {}}},`; its comment says `kind's AgentRuns in theirs only`.
- `func newFactoryManager(log *slog.Logger, ns string, kind runwatch.Kind)`: `runs.Scheme(scheme, kind)` and `opts := factoryManagerOptions(ns, kind)`.
- `runFactory`: `mgr, err := newFactoryManager(log, ns, cfg.Runs)`; `rc := runs.Client{C: mgr.GetClient(), Kind: cfg.Runs}`; the reconciler literal `rec := &reconciler.Reconciler{…}` gains `Kind: cfg.Runs,`.

`internal/app/factory_test.go`, `TestFactoryManagerOptions`: replace

```go
	for _, ns := range []string{"agent-system", "factory-test"} {
		t.Run(ns, func(t *testing.T) {
			o := factoryManagerOptions(ns)
```

with

```go
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	for _, tc := range []struct {
		ns string
		rk runwatch.Kind // the runs kind: the loop below has its own kind
	}{{"agent-system", runwatch.DefaultKind}, {"factory-test", native}} {
		ns, rk := tc.ns, tc.rk
		t.Run(ns, func(t *testing.T) {
			o := factoryManagerOptions(ns, rk)
```

and in its body `runs.GVK().Kind: {runs.Namespace()}` becomes `rk.GVK().Kind: {rk.Namespace}`, `x.GroupVersionKind() != runs.GVK()` becomes `x.GroupVersionKind() != rk.GVK()`.

- [ ] **Step 7: The tests name the default kind**

Outside the package:

```bash
ext=$(grep -rlE 'runs\.(Build|Scheme|Namespace|GVK|Name)\(|runs\.Client\{' --include='*_test.go' internal)
sed -i -E \
  -e 's/runs\.Build\(/runs.Build(runwatch.DefaultKind, /g' \
  -e 's/runs\.Scheme\(([a-z]+)\)/runs.Scheme(\1, runwatch.DefaultKind)/g' \
  -e 's/runs\.Namespace\(\)/runwatch.DefaultKind.Namespace/g' \
  -e 's/runs\.GVK\(\)/runwatch.DefaultKind.GVK()/g' \
  -e 's/runs\.Name\(/runwatch.DefaultKind.ClaimName(/g' \
  -e 's/runs\.Client\{C: ([a-z]+)\}/runs.Client{C: \1, Kind: runwatch.DefaultKind}/g' \
  $ext
```

Inside `internal/factory/runs`:

```bash
sed -i -E \
  -e 's/(^|[^.[:alnum:]_])Build\(/\1Build(runwatch.DefaultKind, /g' \
  -e 's/(^|[^.[:alnum:]_])FromUnstructured\(/\1FromUnstructured(runwatch.DefaultKind, /g' \
  -e 's/(^|[^.[:alnum:]_])Name\(/\1runwatch.DefaultKind.ClaimName(/g' \
  -e 's/(^|[^.[:alnum:]_])Scheme\(s\)/\1Scheme(s, runwatch.DefaultKind)/g' \
  -e 's/Client\{C: ([^}]*)\}/Client{C: \1, Kind: runwatch.DefaultKind}/g' \
  internal/factory/runs/*_test.go
```

In `internal/factory/reconciler/envtest_test.go`, the reconciler literal also gets `Kind: runwatch.DefaultKind,` next to `Runs: runs.Client{…}`. Then:

```bash
goimports -local github.com/Smana/maku -w $ext internal/factory/runs/*.go internal/factory/reconciler/*.go internal/app/*.go
```

(`internal/app/factory_test.go` now names `runwatch.Kind` and `internal/factory/reconciler/reconciler.go` `runwatch.Kind`: neither imported `runwatch` before.)

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go vet ./internal/factory/... ./internal/app/... ./internal/runctl/... && go test -race -count=1 ./internal/factory/... ./internal/app ./internal/runctl`
Expected: PASS (the envtest suites skip without `KUBEBUILDER_ASSETS`, as in CI's `task test`; when they run, they pass).

- [ ] **Step 9: Commit**

```bash
git add -A internal/factory internal/app internal/runctl
git commit -m "refactor(factory): the runs client, the reconciler and the factory's manager take the runs kind (A-2)"
```

---

### Task 8: A-2, the meter matches the run subjects of its kind

**Files:**
- Modify: `internal/factory/meter/vm.go` (`VM`, `NewVM`, `RunUsage`, `parseUsage`), `internal/factory/meter/vl.go` (`VL`, `RecentlyThrottled`)
- Modify: `internal/app/factory.go` (`meter.NewVM(…)`, `meter.VL{…}`)
- Test: `internal/factory/meter/vm_test.go`, `internal/factory/meter/meter_test.go`

**Interfaces:**
- Consumes: `Kind.RunSubject`, `Kind.Validate`.
- Produces: `func NewVM(rawURL, query string, kind runwatch.Kind, hc *http.Client) (*VM, error)`; `func parseUsage(raw []byte, subject *regexp.Regexp) (map[string]Usage, error)`; `VL.Kind runwatch.Kind`, required.

- [ ] **Step 1: Re-grep (T4 rewrote this file)**

Run: `grep -rnE 'NewVM\(|parseUsage\(|VL\{|RunSubject' --include=*.go internal`
T4 (H-4, D10) keeps a baseline per series, so `parseUsage` may return a different shape after it merged. Keep T4's return type and body; this task only adds the `subject` parameter and replaces `runwatch.RunSubject()` with it. Adapt the new test's assertion to T4's shape if it changed (the run id key `abcdefgh` and the reading 42 stay).

- [ ] **Step 2: Existing tests name the default kind**

```bash
sed -i -E \
  -e 's/NewVM\(([^,]+), ([^,]+), /NewVM(\1, \2, runwatch.DefaultKind, /' \
  -e 's/parseUsage\(raw\)/parseUsage(raw, runwatch.DefaultKind.RunSubject())/g' \
  -e 's/VL\{URL:/VL{Kind: runwatch.DefaultKind, URL:/g' \
  internal/factory/meter/*_test.go
```

- [ ] **Step 3: Write the failing tests**

Append to `internal/factory/meter/vm_test.go` (add `"maps"`, `"github.com/Smana/maku/internal/runwatch"` to its imports):

```go
// D9: the meter reads the run subjects of the kind it was given, not a process-wide one, and
// refuses to start without one.
func TestTheMeterReadsItsKindsSubjects(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	raw := []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"ar_agent":"system:serviceaccount:runs:run-abcdefgh"},"value":[0,"42"]},` +
		`{"metric":{"ar_agent":"system:serviceaccount:agents:xplane-run-7f3cq2xz"},"value":[0,"9"]}]}}`)
	usage, err := parseUsage(raw, native.RunSubject())
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage["abcdefgh"].Tokens != 42 {
		t.Fatalf("usage %+v: want the native run's 42 only", usage)
	}
	if _, err := NewVM("http://vm.example:8428", "Q", runwatch.Kind{}, httpx.New(5*time.Second, nil)); err == nil {
		t.Fatal("a meter without the runs kind must refuse")
	}
}

// D9: so does the throttle reader.
func TestVLReadsItsKindsSubjects(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"log.x_ar_agent":"system:serviceaccount:runs:run-abcdefgh","hits":"3"}`+"\n"+
			`{"log.x_ar_agent":"system:serviceaccount:agents:xplane-run-7f3cq2xz","hits":"1"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	got, err := VL{Kind: native, URL: srv.URL, Query: "q", HC: srv.Client()}.RecentlyThrottled(t.Context())
	if err != nil || !maps.Equal(got, map[string]bool{"abcdefgh": true}) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := (VL{URL: srv.URL, Query: "q", HC: srv.Client()}).RecentlyThrottled(t.Context()); err == nil {
		t.Fatal("a VL without the runs kind must refuse")
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test -race -count=1 ./internal/factory/meter -run 'KindsSubjects'`
Expected: FAIL to compile: `too many arguments in call to parseUsage`, `unknown field Kind in struct literal of type VL`.

- [ ] **Step 5: The meter keeps its kind's subject**

`internal/factory/meter/vm.go`: `VM` gains `subject *regexp.Regexp // the run subjects of the runs kind: a series of any other is no reading`. `NewVM`:

```go
// NewVM is the source at rawURL (meter.url): http or https with a host, since vmsingle serves
// plain http in the cluster. kind is the runs kind whose run subjects it reads; hc comes from
// httpx.New.
func NewVM(rawURL, query string, kind runwatch.Kind, hc *http.Client) (*VM, error) {
```

with, in its `switch`, a case before `hc == nil`:

```go
	case kind.Validate() != nil:
		return nil, fmt.Errorf("meter: the runs kind is required: %w", kind.Validate())
```

and `return &VM{base: u, query: query, subject: kind.RunSubject(), hc: hc}, nil`. `RunUsage` returns `parseUsage(raw, v.subject)`. `parseUsage(raw []byte, subject *regexp.Regexp)`: its comment adds `subject captures a run's id from its ar_agent label.`, and `runwatch.RunSubject().FindStringSubmatch(s.Metric["ar_agent"])` becomes `subject.FindStringSubmatch(s.Metric["ar_agent"])`.

`internal/factory/meter/vl.go`: `VL` gains, first field:

```go
	// Kind is the runs kind, required: a row is a run's only when its verified identity is one
	// of Kind's run subjects.
	Kind runwatch.Kind
```

In `RecentlyThrottled`, after the `HC == nil` check:

```go
	if err := v.Kind.Validate(); err != nil {
		return nil, fmt.Errorf("meter: the runs kind is required: %w", err)
	}
	subject := v.Kind.RunSubject()
```

and `runwatch.RunSubject().FindStringSubmatch(row["log.x_ar_agent"])` becomes `subject.FindStringSubmatch(row["log.x_ar_agent"])`.

`internal/app/factory.go`: `meter.NewVM(cfg.Meter.URL, cfg.Meter.Query, cfg.Runs, httpx.New(vmTimeout, nil))` and `Throttle: meter.VL{Kind: cfg.Runs, URL: cfg.Meter.LogsURL, Query: cfg.Meter.ThrottleQuery, HC: httpx.New(vmTimeout, nil)}`.

- [ ] **Step 6: Run the tests**

Run: `goimports -local github.com/Smana/maku -w internal/factory/meter/*.go && go build ./... && go test -race -count=1 ./internal/factory/meter ./internal/app`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/factory/meter internal/app/factory.go
git commit -m "refactor(meter): the meter matches the run subjects of the kind it is given (A-2)"
```

---

### Task 9: A-2, no process-wide runs kind

**Files:**
- Modify: `internal/runwatch/kind.go` (whole file below), `internal/runwatch/run.go` (delete the package `FromUnstructured`)
- Modify: `internal/runwatch/kind_test.go` (whole file below), `internal/runwatch/{runwatch_test,reason_test,sweep_test}.go`
- Modify: `internal/app/broker.go`, `internal/app/factory.go` (delete the `Configure` calls)
- Delete: `internal/app/runs_kind_test.go`; move `factoryCfg` into `internal/app/startup_warnings_test.go`
- Modify (tests): every `*_test.go` still naming `runwatch.ClaimPrefix()`, `runwatch.Namespace()`, `runwatch.GVK()`, `runwatch.FromUnstructured(`, `runwatch.RunSubject()`

**Interfaces:**
- Consumes: everything from Tasks 2–8.
- Produces: `runwatch` exports `Kind` (with `WithDefaults`, `Validate`, `GVK`, `ClaimName`, `RunSubject`, `FromUnstructured`), `DefaultKind`, `ValidatePrefix`; no `Configure`, `Current`, `Namespace`, `ClaimPrefix`, package `GVK`, package `RunSubject`, package `FromUnstructured`; no `init`.

- [ ] **Step 1: Re-grep the production callers**

Run: `grep -rnE 'runwatch\.(Configure|Current|ClaimPrefix|Namespace|GVK|RunSubject|FromUnstructured)\(' --include=*.go internal cmd | grep -v '_test\.go'`
Expected: only `internal/app/broker.go` and `internal/app/factory.go`'s `runwatch.Configure(cfg.Runs)`. Any other hit is a reader Tasks 3–8 missed (a T2–T5 addition): give it the kind the way its neighbours got it before going on.

- [ ] **Step 2: Rewrite `kind.go` without the global**

`internal/runwatch/kind.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package runwatch

import (
	"errors"
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is where AgentRuns live and how they are named: cloud-native-ref's Crossplane XR by
// default, agent-platform's own CRD (run-controller, adoption 3a) when configured. It is a value
// each binary builds from its config and hands to every reader of a claim (D9): there is no
// process-wide kind, so two kinds can share a process, as the tests do.
type Kind struct {
	Group      string `json:"group,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	NamePrefix string `json:"namePrefix,omitempty"`
}

// DefaultKind is cloud-native-ref's: SP1's XRD, which names a run xplane-run-<C2 id> (C2).
var DefaultKind = Kind{Group: "cloud.ogenki.io", Namespace: "agents", NamePrefix: "xplane-run-"}

var (
	groupRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	nsRE     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	prefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}-$`) // + 8 id characters stays a DNS label
)

// WithDefaults fills each empty field from DefaultKind.
func (k Kind) WithDefaults() Kind {
	if k.Group == "" {
		k.Group = DefaultKind.Group
	}
	if k.Namespace == "" {
		k.Namespace = DefaultKind.Namespace
	}
	if k.NamePrefix == "" {
		k.NamePrefix = DefaultKind.NamePrefix
	}
	return k
}

// Validate reports every malformed field at once.
func (k Kind) Validate() error {
	var errs []error
	if !groupRE.MatchString(k.Group) {
		errs = append(errs, fmt.Errorf("group %q is not an API group (a DNS subdomain with a dot)", k.Group))
	}
	if !nsRE.MatchString(k.Namespace) {
		errs = append(errs, fmt.Errorf("namespace %q is not a DNS label", k.Namespace))
	}
	if err := ValidatePrefix(k.NamePrefix); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ValidatePrefix refuses a run-name prefix that, with 8 id characters, is not a DNS label.
func ValidatePrefix(p string) error {
	if !prefixRE.MatchString(p) {
		return fmt.Errorf("namePrefix %q must match %s", p, prefixRE)
	}
	return nil
}

// GVK is k's AgentRun kind (C3).
func (k Kind) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: k.Group, Version: "v1alpha1", Kind: "AgentRun"}
}

// ClaimName is the name of run id's claim under k: <prefix><C2 id>.
func (k Kind) ClaimName(id string) string { return k.NamePrefix + id }

// RunSubject matches a run's ServiceAccount token subject under k, capturing the run id: the
// ServiceAccount is named exactly like the run, on both runtimes. It compiles on each call, so a
// caller that matches many subjects keeps the result.
func (k Kind) RunSubject() *regexp.Regexp {
	return regexp.MustCompile("^system:serviceaccount:" + regexp.QuoteMeta(k.Namespace) + ":" +
		regexp.QuoteMeta(k.NamePrefix) + "([a-z2-7]{8})$")
}
```

In `internal/runwatch/run.go`, delete the package-level `FromUnstructured` and its comment (the method stays).

- [ ] **Step 3: Rewrite `kind_test.go`**

`internal/runwatch/kind_test.go` (keep `TestAKindAnswersForItself` from Task 2 as it is, between the two first tests):

```go
// SPDX-License-Identifier: Apache-2.0

package runwatch

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func kindClaim(apiVersion, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind("AgentRun")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// cloud-native-ref configures nothing: its kind is the default.
func TestDefaultKindIsCloudNativeRefs(t *testing.T) {
	k := DefaultKind
	if g := k.GVK(); g.Group != "cloud.ogenki.io" || g.Version != "v1alpha1" || g.Kind != "AgentRun" {
		t.Fatalf("GVK %v", g)
	}
	if k.Namespace != "agents" || k.ClaimName("abcdefgh") != "xplane-run-abcdefgh" {
		t.Fatalf("namespace %q claim %q", k.Namespace, k.ClaimName("abcdefgh"))
	}
	if m := k.RunSubject().FindStringSubmatch("system:serviceaccount:agents:xplane-run-abcdefgh"); len(m) != 2 || m[1] != "abcdefgh" {
		t.Fatalf("subject match %v", m)
	}
}

// (TestAKindAnswersForItself, from Task 2, unchanged.)

// An empty field takes the default; a malformed one is refused. The config packages call both
// at load, which is where Configure's checks went.
func TestWithDefaultsAndValidate(t *testing.T) {
	if got := (Kind{Group: "maku.ogenki.io", NamePrefix: "run-"}).WithDefaults(); got != (Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"}) {
		t.Fatalf("an empty field takes the default: %+v", got)
	}
	for _, k := range []Kind{{Group: "ogenki"}, {Namespace: "Agents"}, {NamePrefix: "run"}, {NamePrefix: "-run-"}} {
		if err := k.WithDefaults().Validate(); err == nil {
			t.Errorf("%+v: want an error", k)
		}
	}
}
```

- [ ] **Step 4: The entrypoints stop configuring a global**

In `internal/app/broker.go` (`serveBroker`) and `internal/app/factory.go` (`runFactory`), delete the comment `// Before anything watches or creates a run: every reader takes the kind from here.` and the `if err := runwatch.Configure(cfg.Runs); err != nil { … }` block under it. `config.Load` already defaults and validates `cfg.Runs` (`internal/config/config.go:370,407`, `internal/factory/config/config.go:1032-1033`).

Delete `internal/app/runs_kind_test.go`, after moving its `factoryCfg` constant (and only it: `brokerCfg`, `writeCfg`, `runsBlock`, `wantKind` have no other user) to the end of `internal/app/startup_warnings_test.go`, its last reader.

- [ ] **Step 5: The remaining tests name the default kind**

```bash
ts=$(grep -rlE 'runwatch\.(ClaimPrefix|Namespace|GVK|FromUnstructured|RunSubject)\(' --include='*_test.go' internal cmd)
sed -i -E \
  -e 's/runwatch\.ClaimPrefix\(\)/runwatch.DefaultKind.NamePrefix/g' \
  -e 's/runwatch\.Namespace\(\)/runwatch.DefaultKind.Namespace/g' \
  -e 's/runwatch\.GVK\(\)/runwatch.DefaultKind.GVK()/g' \
  -e 's/runwatch\.FromUnstructured\(/runwatch.DefaultKind.FromUnstructured(/g' \
  -e 's/runwatch\.RunSubject\(\)/runwatch.DefaultKind.RunSubject()/g' \
  $ts
sed -i -E \
  -e 's/(^|[^.[:alnum:]_])FromUnstructured\(/\1DefaultKind.FromUnstructured(/g' \
  -e 's/(^|[^.[:alnum:]_])GVK\(\)/\1DefaultKind.GVK()/g' \
  internal/runwatch/runwatch_test.go internal/runwatch/reason_test.go internal/runwatch/sweep_test.go
```

The sed reads `_test.go` files only, on purpose: a production caller left over must fail to compile, not silently take the default.

- [ ] **Step 6: Check D9**

```bash
go build ./... && go vet ./...
grep -rn 'func init' internal/runwatch || echo "no init"
grep -rnE 'runwatch\.(Configure|Current|ClaimPrefix|Namespace|GVK|RunSubject|FromUnstructured)\(' --include=*.go internal cmd || echo "no global accessor"
grep -n 'runwatch.DefaultKind' internal/app/*.go | grep -v '_test\.go' || echo "the app wires only cfg.Runs"
grep -rn 'atomic' internal/runwatch/kind.go || echo "no package state"
```

Expected: the build passes and the four `echo`s print.

- [ ] **Step 7: Run the tests**

Run: `go test -race -count=1 ./...`
Expected: PASS (the store tests need Docker).

- [ ] **Step 8: Commit**

```bash
git add -A internal
git commit -m "refactor(runwatch): no process-wide runs kind: current, init and Configure are gone (A-2)"
```

---

### Task 10: The gate

**Files:** none changed, unless the gate finds something.

- [ ] **Step 1: Run the whole gate**

Run: `task check`
Expected: exit 0: `spdx` (the four new `.go` files open with the SPDX line), `tidy`, `lint` (no `stutters`, no unused `writeCfg`/`brokerCfg`), `vuln`, `test`, `crd:check`, `chart:check`, `migrations`, `docs`, `scripts`, `ui:test`, `ui:check`.

- [ ] **Step 2: The theme's checks**

```bash
go list -deps ./internal/factory/intake ./internal/factory/meter ./internal/factory/reconciler | grep -x 'github.com/Smana/maku/internal/factory/api' || echo "D8 holds"
grep -rn 'func init' internal/runwatch || echo "D9 holds"
git log --oneline main..
```

Expected: `D8 holds`, `D9 holds`, nine `refactor(…)` commits: one for A-1, eight for A-2.

- [ ] **Step 3: Hand off**

Hand the branch to superpowers:finishing-a-development-branch; push and open the PR only when the owner asks. The PR description (English, no "Generated with" line) lists the Spec deviations above and tells T10 that `internal/factory/ledger` and the injected kind are ready for `AGENTS.md`'s package map and seam row.

## Self-review

| Spec item | Task |
|---|---|
| A-1 / D8: the ledger moves to `internal/factory/ledger`; `api`, `intake`, `meter`, `reconciler` import it; tests move unchanged | 1 |
| D8 check: `go list -deps` of intake, meter, reconciler has no `api` | 1 (Step 10), 10 |
| A-2 / D9: `Kind` is a value built from config in `internal/app` and passed to each reader | 2–8 (`cfg.Runs` in `broker.go`, `rooms.go`, `factory.go`) |
| A-2: `current`, `init()`, `Configure` deleted | 9 |
| D9 check: `grep -n 'func init' internal/runwatch` empty; tests set a kind per instance | 9, 10 |
| D0: `task check` exits 0 on the PR; each commit green | every task's test step, 10 |
| "T9 runs after T2–T5": rebase and re-grep first | Before you start; Step 1 of each task |
