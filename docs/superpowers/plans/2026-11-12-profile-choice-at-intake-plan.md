# Profile Choice at Intake Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A person who asks in plain words sees, before anything is filed, the class, the harness
profile and the merge outcome the factory will apply, with one line of why; a maintainer may pin
another profile the class allows; the factory re-checks every pin at triage.

**Architecture:** One source of truth, the factory's accepted config snapshot, served read-only as
`GET /v1/catalogue/{owner}/{name}` and cached 60 s by the broker. The planner suggests from it
(`plan/v2`, a display `suggestion` and a material `profile`), `classes_list` lists it, `issue_file`
and `run_create` carry the class (and the pin) to the factory, and triage honours or refuses the pin
with a closed list of reasons shared with intake.

**Tech Stack:** Go, stdlib `testing` with `-race`, controller-runtime/envtest, CRDs/CEL, OpenTelemetry
metrics, MCP JSON schemas, Slack Block Kit, TypeScript/vitest (`web/`).

**Spec:** `docs/superpowers/specs/2026-11-11-profile-choice-at-intake-design.md` (Maku #443), decisions
O1–O3 and P1–P15 with the owner's answers of 2026-11-11.

## Global Constraints

- **The word is "profile"** (*harness profile* on first use on a page), never "workflow" (P1), in
  code, tool descriptions, Slack text, UI and docs.
- **The factory is the authority** (P6). Every value the broker shows (class, profiles, roles, merge
  outcome, `maintainer`) comes from the catalogue; the merge outcome is computed, never the model's
  (P3). The model writes only `class`, `suggestion.profile` and `why` (≤ 200 characters, untrusted).
- **Accepting a suggestion pins nothing** (P4). A pin exists only when a maintainer picked one (P5).
- **Refusals are the closed list** in `internal/intake/refusal.go` (P12), one sentence each, never naming
  another repository. A pin refused at triage is never a task failure: the task runs unpinned.
- **The planner and the scoper stay independent** (P13): `spec.intake.suggested` is never read by any
  brief; only the agreement metric reads it.
- **No model call on `plan_check`, on the profile revision or on the `plan/v1` upgrade** (the spec-intake
  plan's constraint; see gap G2).
- **Typed schemas (#457):** every node of a new or changed MCP tool schema names its `type`;
  `internal/humanapi/mcpschema_test.go` must cover `classes_list` and the changed `intake_plan`,
  `plan_check` and `run_create` schemas.
- **Repository case (#461):** repository names compare case-insensitively at every edge and are written
  in the configured spelling; the plan hash keeps its lowercase form. Catalogue cache keys are lowercase.
- **Metrics:** instruments are created `intake_*` (broker) and `agent_factory_*` (factory) and served as
  `maku_*`; labels are config names or closed enums, `other` for a name no longer in config.
- **Generated artifacts** through `task crd:gen` and `task ui:build` (the `ui:check` bundle), never by
  hand. TDD: the failing test first; table-driven stdlib tests; hand-written fakes.
- **Each stage is one PR and one Maku release**; ai-native-ref bumps its pin per stage. Stages that change
  what the evaluation counts start with `evaluation.mode: observe` on the live factory.
- Conventional Commits in English, no trailers. `task check` exits 0 before every push.

## How this builds on today's main

| Shipped or in flight | What this plan reuses or must respect |
|---|---|
| #453 outcome overlap (`api/factory/v1alpha1/overlap.go`, `internal/factory/reconciler/overlap.go`) | Overlap runs in `received`, before triage. A pin is resolved at triage, so a duplicate refused by overlap never reaches pin resolution, and an overlap scoper run never reads `spec.intake`. `TaskStatus` CEL rule counts in `task_crd_test.go` changed with #453: Task 3.4 updates the counts from there, not from an older base |
| #454 reachable repositories, #455 refusal holds, failed scoper does not fall through | The catalogue serves only repositories the factory serves (the #454 set): an unserved repository is a 404 that names nothing. A failed scoper leaves a pinned task's pin intact (`resolveProfile`'s `case q.Source == "label"` comes before `case s == nil`); Task 3.5 pins that order with a test |
| #457 typed schemas | `classes_list`'s schema and the `suggestion`/`profile` additions name every node's type; the schema test enumerates tools, so a new tool fails it until listed |
| #460 planner gap floor (`internal/intake/floor.go`, `kind: model_claim`, `Planner.ServedModel`) | `ApplyFloor` runs after composition and never touches `suggestion` or `profile`; Task 2.2 adds a test. The suggestion's `why` is model text like a `model_claim`: the plan view labels it as the planner's reading. `ServedModel` stays display through `plan/v2`. Stage 2 starts after #460 merges (both edit `plan.go`, `compose.go`, `model.go`, `humanapi/intake.go`, `roomview/plan.go`) |
| #461 repository case at the edges | `canonicalRepo`/`servesRepo` in `slackapp` and the factory API's case-insensitive match are what the catalogue route and the broker cache use. Stage 1's factory route starts after #461 merges (both edit `internal/factory/api/server.go` and `tasks.go`) |

## Live evidence the plan answers (gcp-0, 2026-11-11)

1. **A class label alone puts a task in cohort `labeled`.** Confirmed in code: `triage.CohortOf(taskID,
   declared, percent)` returns `labeled` for any declared class, and `floorProfile` maps that cohort to
   `source=label`; `profileChoices` then returns nil, so the scoper is never offered a choice. Stage 2
   makes `issue_file` and `run_create` declare the class (P7), so **shipping stage 2 before P8 would move
   every maintainer intake task out of the evaluation.** The plan merges Stage 4 (P8) before Stage 2
   (gap G1).
2. **The planner's class list is broker config** (`human.intake.plan.classes`), and ai-native-ref's
   lists only `docs-links` (ANR #351), so every plan is filed `docs-links` with its $2 ceiling. Stage 2
   makes the catalogue the planner's class source per repository; the config table is only the fallback
   (no suggestion), then goes in Stage 5. ANR #351 closes with ANR's Stage 2 pin bump; that same PR
   adds `review` to ANR's fallback table so a catalogue outage never files everything `docs-links`.

## Spec gaps this plan resolves (accepted 2026-11-11, owner: use defaults)

| # | Gap | Plan's resolution | Status |
|---|---|---|---|
| G1 | Stage 2 (P7: the class reaches the factory) ships before Stage 4 (P8), so between the two releases every maintainer intake task is `labeled` (live evidence 1) | **Merge order 1 → 4 → 2 → 3 → 5.** Stage 4 depends on nothing in 2 or 3: before pins exist, nothing is `labeled`. Stage numbers keep the spec's names | Accepted 2026-11-11 (owner: use defaults) |
| G2 | P14 says a `plan/v1` confirmed after the upgrade is "re-planned"; the spec-intake contract says `plan_check` makes no model call, and a re-plan would spend the requester's `dailyPlans` | **Deterministic upgrade** `intake.Upgrade` (no model call): same issues, `profile: ""`, a suggestion computed from the catalogue with the factory's choice and an empty `why`, a new hash, shown again, never filed | Accepted 2026-11-11 (owner: use defaults) |
| G3 | "The roles shown come from the pinned or suggested profile's template", but roles are material and, under P4, an unpinned task runs the floor (or the scoper's pick), not the suggestion | **Material `roles`** = the pinned profile's template, else the floor's template, else `[]` ("chosen at triage", P15). The suggested profile's roles are display, inside `suggestion` | Accepted 2026-11-11 (owner: use defaults) |
| G4 | The broker's class table is global; the catalogue is per repository, and a plan may weigh several candidate repositories | The model is given each candidate's offered classes; `Check` validates `issues[].class` against **the plan repository's** catalogue; the ceiling is that class's `ceilingUSD` | Accepted 2026-11-11 (owner: use defaults) |
| G5 | `gh issue edit --add-label` fails when the label does not exist; `class:<c>` and `profile:<p>` labels are not created anywhere | `issue_file` applies `class:`/`profile:` through `gh api -X POST repos/{r}/issues/{n}/labels -f labels[]=…`, which creates a missing label; `factory/ready` keeps its current path. Task 2.6 proves it against a fake `gh` and Stage 3's live proof against GitHub | Accepted 2026-11-11 (owner: use defaults) |
| G6 | The catalogue is "with the caller's token (as `/v1/policy/evidence`)", whose reader is `human_only`; the Slack App is a sponsored principal acting `on_behalf_of`, and `maintainer` must be the Slack user's | The route accepts a human, or a sponsored principal with `onBehalfOf` resolved to a login exactly as `POST /v1/tasks` resolves it; `maintainer` is that login's. The route is mounted whenever the API is, not behind `policyManagement.promotion` | Accepted 2026-11-11 (owner: use defaults) |
| G7 | "The broker caches per repository for 60 s", but the answer carries the caller's `maintainer` flag | Cache key `(lowercase repository, caller login)`; 60 s TTL; bounded LRU of 256 entries | Accepted 2026-11-11 (owner: use defaults) |
| G8 | P8's table covers labels and intake classes; `predictClass` also declares a scheduled task's class | P8 applies to every source: only an honoured pin is `labeled` | Accepted 2026-11-11 (owner: use defaults) |
| G9 | The Metrics section's agreement row is split from its table by a blank line, so it renders as a headerless table | Task 1.6 fixes the spec's table in the same docs commit | Accepted 2026-11-11 (owner: use defaults) |

## Stage order and PRs

| Merge | Stage | PR title | Release | Behaviour change |
|---|---|---|---|---|
| 1st | 1 | `feat(factory): the catalogue, classes_list and the settings Profiles row` | minor | none: read-only |
| 2nd | 4 | `feat(triage): only a profile pin is the labeled cohort` | minor | class-labelled and scheduled tasks join the cohorts |
| 3rd | 2 | `feat(intake): plan/v2 suggests a class, a profile and the merge outcome` | minor | maintainers' intake tasks carry their class |
| 4th | 3 | `feat(factory): a maintainer pins a harness profile at intake` | minor | pinned tasks run their profile; same-profile escalation asks a human |
| 5th | 5 | `feat(intake)!: the catalogue is the planner's only class source` | minor, breaking config | `human.intake.plan.classes` is refused |

## Review Focus

1. **A pin never bypasses the floor rule:** a `live`/`shadow` class, a profile outside floor ∪ allowed,
   a non-maintainer applier and a test-first pin on internal data are refused at intake and at triage
   with the same code (shared fixture `internal/intake/testdata/pins.json`, Tasks 3.2 and 3.5).
2. **Hash safety:** the `profile` revision changes nothing but `issues[].profile` and the one D/A record
   (`TestReviseProfileOnly`, Task 3.3); `plan/v1` is never filed (`TestUpgradeIsShownNeverFiled`, Task 2.3).
3. **Evaluation integrity:** a class label no longer sets the cohort (Task 4.1); an honoured pin is
   `labeled` in `observe` and in `apply` (Task 3.5); the scoper never reads the suggestion (Task 3.7).
4. **Untrusted text:** `why` and descriptions are rendered inert in Slack, the room view and the skill's
   text (`roomview` is the one place, Tasks 1.4 and 2.7).
5. **Escalation:** no step to the active profile, pinned and scoper-applied alike (Task 3.6), and the
   release notes say so.

---

## Stage 1: the catalogue, `classes_list`, the settings Profiles row

**Prerequisite:** #461 merged (shared files in `internal/factory/api`). Branch from fresh `origin/main`.

### Task 1.1: The catalogue model (size M)

**Files:**
- Create: `internal/factory/catalogue/catalogue.go`, `internal/factory/catalogue/catalogue_test.go`
- Modify: `internal/factory/reconciler/policyfact.go` (use `catalogue.ModeOf` instead of its private `modeOf`)

**Interfaces (produced, consumed by every later task):**

```go
// Package catalogue is what a repository offers at intake, built from the accepted config snapshot:
// its classes, their profiles, the roles each runs and the merge outcome. It never reads GitHub.
package catalogue

type Catalogue struct {
	Repository     string             `json:"repository"`
	Generations    Generations        `json:"generations"`
	Evaluation     Evaluation         `json:"evaluation"`
	ApproveToMerge bool               `json:"approveToMerge"`
	Maintainer     bool               `json:"maintainer"`
	Classes        []Class            `json:"classes"`
	Profiles       map[string]Profile `json:"profiles"`
}
type Class struct {
	Name, Description, Mode string // Mode: live | shadow | off | unarmed
	CeilingUSD              float64
	Floor                   string
	Allowed                 []string
	Escalation              *Escalation // {OutOfBudget, NotConverged}
}
type Profile struct {
	Template   string; Roles []string; Tier string; Checks, TestFirst bool
	Loop, Description string; Offered bool; NotOffered string // "no_template"
}

func Build(cfg *config.Config, repository string, maintainer bool) (Catalogue, bool)
func ModeOf(c config.Class, gateOn bool) string
func Outcome(mode string, approveToMerge bool) string // auto | shadow | approval | maintainer
func (c Catalogue) Class(name string) (Class, bool)
func (c Catalogue) OfferedProfiles(class string) []string // floor then allowed, Offered only
```

JSON tags are the spec's camelCase (`ceilingUSD`, `floor`, `allowed`, `notOffered`).

- [ ] **Step 1: Write the failing tests**

`TestBuildOffersClasses`: a repository with `revert`, an adopted class, an oracle class, `docs-links`
(live) and `review` → only `docs-links` and `review`, in name order; a repository whose classes omit
`review` still lists it from the factory default. `TestProfileOffered`: a profile with `template` is
`offered: true` with the template's roles from `cfg.Templates[t].Roles`; one without is
`offered: false, notOffered: "no_template", roles: nil` (P15). `TestModeOf`: live, shadow, off, and
any class with the gate off → `unarmed`. `TestOutcome`: `live`→`auto`, `shadow`→`shadow`, `off` with
`approveToMerge`→`approval`, `off` without and `unarmed`→`maintainer`. `TestBuildUnservedRepository`:
`ok == false`. `TestBuildRepositoryAuthority`: `classAuthority: repository` classes come from the
accepted repository-file classes on `cfg.RepositoryFor(repo)`. `TestBuildGenerations`: from
`cfg.GenerationsFor`.

- [ ] **Step 2: RED**

Run: `go test -race -count=1 ./internal/factory/catalogue`
Expected: FAIL, package has no Go files / undefined `Build`.

- [ ] **Step 3: Implement**

Pure functions over `*config.Config`. `ApproveToMerge` is `gateOn && rc.Merge.ApproveToMerge` (offered
classes are agents classes by construction). Move `modeOf` from `policyfact.go` into `ModeOf` with the
`off` / `unarmed` split; `policyfact.go` keeps emitting `unarmed` for both, as `envelope.validMode`
requires.

- [ ] **Step 4: GREEN**

Run: `go test -race -count=1 ./internal/factory/catalogue ./internal/factory/reconciler`
Expected: PASS.

- [ ] **Step 5: Commit** `feat(factory): build a repository's catalogue from the accepted snapshot`

### Task 1.2: `GET /v1/catalogue/{owner}/{name}` (size M)

**Files:**
- Create: `internal/factory/api/catalogue.go`, `internal/factory/api/catalogue_test.go`
- Modify: `internal/factory/api/server.go` (route, mounted whenever the API is: gap G6)

**Interfaces:** `func (s *Server) catalogue(w http.ResponseWriter, r *http.Request)`. Caller: a human
principal that `CanRead` the repository, or a sponsored principal with `?onBehalfOf=` resolved to a
GitHub login the way `createTask` resolves it. `maintainer` = `cfg.RepositoryFor(repo).IsMaintainer(login)`.

- [ ] **Step 1: Failing tests** — `TestCatalogueRoute` table: reader → 200 and the Task 1.1 body; mixed
  case path → 200 with the configured spelling (#461); unserved repository → 404 `not_found` naming
  nothing; non-reader → 403 `not_permitted`; unauthenticated → 401; sponsored without `onBehalfOf` →
  403 `human_only`; sponsored with a linked maintainer → `maintainer: true`; human non-maintainer →
  `false`; `policyManagement.promotion: false` → still 200.
- [ ] **Step 2: RED** — `go test -race -count=1 -run Catalogue ./internal/factory/api` fails (404 from the mux).
- [ ] **Step 3: Implement** — reuse `policyReader`'s authentication minus its `human_only` refusal for
  the sponsored case; `catalogue.Build(cfg, repo, maintainer)`; JSON with `Cache-Control: no-store`.
- [ ] **Step 4: GREEN** — `go test -race -count=1 ./internal/factory/api`.
- [ ] **Step 5: Commit** `feat(api): serve a repository's catalogue`

### Task 1.3: The broker's catalogue reader and cache (size M)

**Files:**
- Create: `internal/runrequest/catalogue.go`, `internal/runrequest/catalogue_test.go`
- Create: `internal/humanapi/catalogue.go`, `internal/humanapi/catalogue_test.go`
- Modify: `internal/metrics/metrics.go`, `internal/metrics/metrics_test.go` (`intake_catalogue_reads_total{outcome}`)

**Interfaces:**

```go
// runrequest
func (f Factory) Catalogue(ctx context.Context, accessToken, repository, onBehalfOf string) (catalogue.Catalogue, error)
// humanapi: consumer-side interface and cache
type catalogueSource interface {
	Catalogue(ctx context.Context, accessToken, repository, onBehalfOf string) (catalogue.Catalogue, error)
}
type catalogueCache struct { /* key: lower(repo)+"\x00"+login; TTL 60 s; cap 256; now func() time.Time */ }
func (c *catalogueCache) Get(ctx context.Context, p authn.Principal, repository, onBehalfOf string) (catalogue.Catalogue, error)
var errCatalogueUnavailable = errors.New("catalogue unavailable")
```

- [ ] **Step 1: Failing tests** — `TestCatalogueClient` (httptest: path escaping, token header, 404/403
  mapped to typed errors); `TestCatalogueCacheTTL` (hit within 60 s, miss after, injected clock);
  `TestCatalogueCacheKeyedByCaller` (two logins, two entries: G7); `TestCatalogueCacheBounded` (257th
  entry evicts the oldest); `TestCatalogueReadsMetric` (`hit`, `miss`, `error`).
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/runrequest ./internal/humanapi ./internal/metrics -run 'Catalogue|IntakeCatalogue'`.
- [ ] **Step 3: Implement** — through the existing `internal/httpx` client; an error is never cached.
- [ ] **Step 4: GREEN** — same command.
- [ ] **Step 5: Commit** `feat(broker): read and cache the factory's catalogue`

### Task 1.4: `classes_list` (size M)

**Files:**
- Create: `internal/humanapi/classeslist.go`, `internal/humanapi/classeslist_test.go`
- Create: `internal/roomview/catalogue.go`, `internal/roomview/catalogue_test.go` (text rendering, inert)
- Modify: `internal/humanapi/mcp.go` (register), `internal/humanapi/mcpschema_test.go` (#457)

**Interfaces:** tool `classes_list {repository: string (required), class: string, on_behalf_of: string}`;
`roomview.CatalogueText(c catalogue.Catalogue, class string) string`.

- [ ] **Step 1: Failing tests** — `TestClassesListSummary`: one line per offered class,
  `docs-links · live · merges on its own · profile links-solo`; `TestClassesListDetail`: with `class`,
  each profile's roles, tier, tests-first, loop, description, the floor, the escalation targets, "you
  may pin" only when `maintainer`; a `no_template` floor reads "chosen at triage, not pinnable";
  `TestClassesListAccess`: a human who cannot read → refusal, the Slack App without `on_behalf_of` →
  refusal; `TestClassesListUnknownClass`; `TestCatalogueTextInert` (a description with Slack/Markdown
  control text renders inert); the schema test lists `classes_list`; `rooms_human_mcp_calls_total{tool="classes_list"}` counted.
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/humanapi ./internal/roomview -run 'ClassesList|CatalogueText|Schema'`.
- [ ] **Step 3: Implement** — text and structured content; the word "profile" only (P1).
- [ ] **Step 4: GREEN** — same command.
- [ ] **Step 5: Commit** `feat(mcp): classes_list shows a repository's classes and profiles`

### Task 1.5: The settings *Profiles* row (size M)

**Files:**
- Modify: `internal/humanapi/policy.go` (`policyRepository` adds `catalogue` from the Task 1.3 cache; index adds `evaluation`)
- Modify: `internal/humanapi/policy_test.go`
- Modify: `web/src/class-card.ts`, `web/src/settings.ts`, `web/src/settings-api.ts`; create `web/src/class-card-profiles.test.ts`
- Generated: `internal/humanapi/ui/dist/`

- [ ] **Step 1: Failing tests** — Go: `TestPolicyRepositoryCarriesCatalogue`, catalogue error → the page
  still answers with `catalogue: null`. vitest: a class card renders *Profiles* with floor, allowed,
  escalation targets and each profile's roles and tier; a `no_template` floor reads "roles chosen at
  triage"; the index shows `evaluation: observe · control 20 %`.
- [ ] **Step 2: RED** — `go test -race -count=1 -run PolicyRepository ./internal/humanapi` and `cd web && npx vitest run class-card-profiles`.
- [ ] **Step 3: Implement** — read-only; no new page (D9 of factory resources).
- [ ] **Step 4: GREEN** — same, then `task ui:check`.
- [ ] **Step 5: Commit** `feat(ui): a Profiles row on each class card`

### Task 1.6: Docs and the stage gate (size S)

**Files:** `docs/api.md` (the route), `docs/cli.md` (`classes_list`), `docs/web-ui.md`, `docs/naming.md`
(row: "workflow" is reserved for GitHub Actions, P1), the spec's metrics table (G9).

- [ ] **Step 1:** write the sections; `task docs` passes.
- [ ] **Step 2:** `task check` exits 0. Commit `docs: the catalogue, classes_list and the Profiles row`.

**Stage 1 checkpoint (review):** a `catalogue_test.go` fixture shaped like ai-native-ref's live config
(`docs-links` live, `review` off with `harnessAllowed`) lists the expected classes; `classes_list` output pasted in the PR.
ANR: pin bump only.

---

## Stage 4: only a profile pin is the `labeled` cohort (P8), merged second (G1)

### Task 4.1: The cohort rule (size M)

**Files:**
- Modify: `internal/factory/triage/triage.go` (`CohortOf`), `internal/factory/triage/classify.go`
- Modify: `internal/factory/triage/triage_test.go`, `internal/factory/triage/classify_test.go`
- Modify: `internal/factory/reconciler/reconciler.go` (`floorProfile`), `internal/factory/reconciler/profile_test.go`

**Interfaces:** `func CohortOf(taskID string, pinned bool, percent int) string`; `Decision` gains
`Pinned bool` (always false until Stage 3); `Declared` keeps feeding the policy fact's conditions only.

- [ ] **Step 1: Failing tests** — `TestClassLabelJoinsCohorts` (a maintainer's `class:` label → `control`
  or `treatment` by hash, never `labeled`); `TestScheduledClassJoinsCohorts` (G8);
  `TestPinnedIsLabeled` (`pinned: true` → `labeled`); reconciler: a class-labelled task's
  `profileChoices` is non-nil when the class has `harnessAllowed`, and its `source` is `floor`/`control`.
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/factory/triage ./internal/factory/reconciler -run 'Cohort|Labeled|ProfileChoices'`.
- [ ] **Step 3: Implement** — `cohort := CohortOf(t.Name, false, …)`; leave the `declared` return of
  `predictClass` for the conditions in `policyfact.go`.
- [ ] **Step 4: GREEN** — `go test -race -count=1 ./internal/factory/...`.
- [ ] **Step 5: Commit** `feat(triage): a class label no longer sets the cohort`

### Task 4.2: The annotation and release notes (size S)

**Files:** `docs/factory.md` (evaluation section: `source=label` before this release means "class label";
from it, "a person chose the profile"), and the GitHub release body for this release (the repository keeps no CHANGELOG).

- [ ] **Step 1:** document; `task docs`, then `task check`. Commit `docs(factory): source=label now means a pinned profile`.

**Stage 4 checkpoint:** before merge the live factory is in `evaluation.mode: observe`. ANR follow-up: pin
bump and the dashboard annotation on `agent_factory_task_outcomes_total{source="label"}` at the release date.

---

## Stage 2: `plan/v2`, the suggestion, the class reaches the factory (P7, P14)

**Prerequisites:** Stage 1 and Stage 4 released; #460 merged. Rebase on `origin/main`.

### Task 2.1: The `plan/v2` contract (size S)

**Files:** `internal/intake/plan.go`, `internal/intake/plan_test.go`, `internal/intake/testdata/plan/*.json` (fixtures)

**Interfaces:**

```go
const APIVersion = "plan/v2"
const APIVersionV1 = "plan/v1" // recognised only by Upgrade (Task 2.3)

type Issue struct { /* … */ Profile string `json:"profile"`; Suggestion *Suggestion `json:"suggestion,omitempty"` }
type Suggestion struct {
	Profile string     `json:"profile"`          // "" = the factory's choice
	Roles   []string   `json:"roles"`            // the suggested profile's template roles, display (G3)
	Merge   MergeShown `json:"merge"`            // {Outcome, ApproveToMerge}, the broker's (P3)
	Why     string     `json:"why"`              // ≤ 200 runes, untrusted
}
// materialIssue: Profile after Class; Suggestion is not material.
```

- [ ] **Step 1: Failing tests** — `TestPlanV2HashCoversProfile` (changing `profile` changes the hash);
  `TestSuggestionIsDisplay` (changing `suggestion` does not); `TestV1AndV2HashDiffer` (same issues);
  `TestCheckRefusesV1` (`Check` refuses `plan/v1` with `planner_invalid`); `TestWhyBound` (201 runes →
  `plan_too_large`).
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/intake -run 'PlanV2|Suggestion|V1|WhyBound'`.
- [ ] **Step 3: Implement**; add `v2-*.json` fixtures beside the `plan/v1` ones, which stay as `Upgrade`'s inputs (Task 2.3).
- [ ] **Step 4: GREEN** — `go test -race -count=1 ./internal/intake`.
- [ ] **Step 5: Commit** `feat(intake): plan/v2 carries a profile and a display suggestion`

### Task 2.2: Composition from the catalogue (size L)

**Files:**
- Modify: `internal/intake/compose.go`, `internal/intake/model.go`, `internal/intake/prompt.go`, `internal/intake/refusal.go` (`Check`, `CheckFinal`)
- Test: `internal/intake/compose_test.go`, `internal/intake/model_test.go`, `internal/intake/refusal_test.go`, `internal/intake/floor_test.go`

**Interfaces:**

```go
type ClassOption struct { Name, Description, Floor string; Offered []string; CeilingUSD float64; Mode string; Roles map[string][]string }
// ComposeIn gains:
Offers map[string][]ClassOption // per candidate repository, lowercase key (G4); nil = config fallback, no suggestion
ApproveToMerge map[string]bool
// Check / CheckFinal take the plan repository's []ClassOption instead of map[string]Class.
```

The model reply gains per issue `"suggestion": {"profile": "...", "why": "..."}`; the prompt lists each
candidate's classes with their descriptions and offered profiles (word "profile", P1).

- [ ] **Step 1: Failing tests** — `TestSuggestionOutsideClassIsFactorysChoice` (a model profile not in
  `Offered` → `profile: ""`, logged); `TestMergeOutcomeIsTheBrokers` (a model `merge` field is ignored;
  `Outcome` from `Mode`/`ApproveToMerge`); `TestRolesFollowTheFloorNotTheSuggestion` (G3: templated
  floor → its roles; `no_template` floor → `[]`); `TestPerRepositoryClasses` (a class offered on
  repository A but not B, plan on B → `invalid_class`); `TestCeilingFromCatalogue`;
  `TestFallbackHasNoSuggestion` (`Offers == nil` → config table, `Suggestion == nil`);
  `TestFloorLeavesSuggestionAlone` (#460's `ApplyFloor` after composition keeps `suggestion` and `profile`);
  `TestPromptListsProfiles` (no occurrence of "workflow").
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/intake -run 'Suggestion|MergeOutcome|Roles|PerRepository|Ceiling|Fallback|FloorLeaves|Prompt'`.
- [ ] **Step 3: Implement** — keep `Classes` as the fallback input until Stage 5.
- [ ] **Step 4: GREEN** — `go test -race -count=1 ./internal/intake`.
- [ ] **Step 5: Commit** `feat(intake): suggest a class and profile from the catalogue`

### Task 2.3: The `plan/v1` upgrade (P14, G2) (size M)

**Files:** create `internal/intake/upgrade.go`, `internal/intake/upgrade_test.go`; modify
`internal/humanapi/intake.go` (`plan_check`), `internal/humanapi/runcreate.go`, `internal/slackapp/proposalaction.go`,
their tests.

**Interfaces:** `func Upgrade(v1 Plan, offers []ClassOption, approveToMerge bool) (Plan, *Refusal)` —
deterministic, no model call; `plan_check`/`run_create` answer a `plan/v1` with `{"status": "replanned", "plan": <v2>}`;
Slack redraws the proposal with the v2 plan and its new hash.

- [ ] **Step 1: Failing tests** — `TestUpgradeKeepsIssues` (title, body, class, dependsOn unchanged;
  `profile: ""`; new hash); `TestUpgradeClassGone` (class no longer offered → `invalid_class`);
  `TestUpgradeIsShownNeverFiled` (table over `plan_check`, `run_create` and Slack Confirm: nothing filed,
  no model call counted, the v2 plan returned or redrawn).
- [ ] **Step 2: RED** — `go test -race -count=1 -run Upgrade ./internal/intake ./internal/humanapi ./internal/slackapp`.
- [ ] **Step 3–4: Implement, GREEN** — same command, then the three packages whole.
- [ ] **Step 5: Commit** `feat(intake): a plan/v1 confirmed after the upgrade is shown again as plan/v2`

### Task 2.4: The broker plans from the catalogue (size M)

**Files:** `internal/humanapi/intake.go`, `internal/humanapi/intake_test.go`, `internal/app/broker.go` (wire the Task 1.3 cache)

- [ ] **Step 1: Failing tests** — `TestIntakePlanReadsTheCatalogue` (offers per candidate from the
  cache; `Check` against the plan repository); `TestIntakePlanCatalogueDown` (fallback to
  `human.intake.plan.classes`, no suggestion, `intake_catalogue_reads_total{outcome="error"}`);
  `TestIntakePlanSchemaTyped` (#457: `suggestion` nodes typed).
- [ ] **Step 2: RED** — `go test -race -count=1 -run 'IntakePlan' ./internal/humanapi`.
- [ ] **Step 3–4: Implement, GREEN** — `go test -race -count=1 ./internal/humanapi`.
- [ ] **Step 5: Commit** `feat(broker): intake_plan reads the catalogue`

### Task 2.5: `run_create` carries the class to triage (P7) (size L)

**Files:**
- Modify: `api/factory/v1alpha1/task_types.go` (`TaskSpec.Intake *TaskIntake`), `api/factory/v1alpha1/task_crd_test.go`
- Generated: `zz_generated.deepcopy.go`, `config/crd/maku.ogenki.io_tasks.yaml`, `charts/maku-factory/crds/…`
- Modify: `internal/factory/api/tasks.go`, `internal/factory/api/tasks_test.go`
- Modify: `internal/runrequest/tasks.go`, `internal/humanapi/runcreate.go`, their tests
- Modify: `internal/factory/triage/classify.go`, `internal/factory/triage/classify_test.go`

**Interfaces:**

```go
type TaskIntake struct {
	Class     string `json:"class,omitempty"`     // ≤ 63, class-name pattern
	Suggested string `json:"suggested,omitempty"` // the planner's suggestion, for the agreement metric only (P13)
	Login     string `json:"login,omitempty"`     // the sponsor's resolved login, never a request value
}
// CEL: spec.intake immutable once set. TaskRequest gains Class, Suggested (Profile in Stage 3).
```

- [ ] **Step 1: Failing tests** — CRD: bounds, immutability, deepcopy; API: `class` copied, `login` is
  the resolved sponsor, a `login` in the body is refused `invalid_arguments`; humanapi:
  `TestRunCreateClassBoundByPlan` (class ≠ the plan's issue class → refusal; the plan's own class →
  passed); triage: `TestIntakeClassFromMaintainer` (login in `maintainers` → that class, declared),
  `TestIntakeClassFromNonMaintainerIgnored`, `TestLabelBeatsIntakeClass`, cohort unaffected (Stage 4).
- [ ] **Step 2: RED** — `go test -race -count=1 ./api/factory/v1alpha1 ./internal/factory/api ./internal/runrequest ./internal/humanapi ./internal/factory/triage`.
- [ ] **Step 3: Implement**, then `task crd:gen`.
- [ ] **Step 4: GREEN** — same command and `task crd:check`.
- [ ] **Step 5: Commit** `feat(factory): run_create carries the planned class to triage`

### Task 2.6: `issue_file` applies `class:` before `factory/ready` (size M)

**Files:** `internal/maku/issuefile.go`, `internal/maku/issuefile_test.go`

**Interfaces:** `GHAddLabel(ctx, repo string, issue int, label string) error` running
`gh api -X POST repos/{repo}/issues/{n}/labels -f labels[]=<label>` (creates a missing label, G5);
`readyLabel` keeps `GHLabel`. The form message gains `Class <c> · profile: the factory's choice · <merge words>`
and the suggestion line; for a non-maintainer (catalogue `maintainer: false`) it says the class will not count.

- [ ] **Step 1: Failing tests** — `TestStartLabelsInOrder` (fake `Labeler` records `class:docs-links`,
  then `factory/ready`); `TestStartNonMaintainerSkipsClass`; `TestLabelArgsNeverAShell` (argv shape);
  `TestFormShowsSuggestion` (inert `why`).
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/maku -run 'Start|LabelArgs|FormShows'`.
- [ ] **Step 3–4: Implement, GREEN** — `go test -race -count=1 ./internal/maku`.
- [ ] **Step 5: Commit** `feat(maku): issue_file applies the planned class before factory/ready`

### Task 2.7: The suggestion on the surfaces (size M)

**Files:**
- Modify: `internal/maku/skill/factory-handoff/SKILL.md`, `internal/maku/skill/skill_test.go`
- Modify: `internal/roomview/proposal.go`, `internal/roomview/plan.go`, their tests
- Modify: `internal/slackapp/proposal.go`, `internal/slackapp/proposal_test.go`

- [ ] **Step 1: Failing tests** — skill: contains the suggestion paragraph's first sentence and the
  `classes_list` table row, no "workflow"; roomview: `TestProposalSuggestionLines` renders
  `class docs-links · profile links-solo (one implementer, tier light) · merges on its own when the gate's conditions hold`
  then `why: …`, inert; a fallback plan shows no suggestion line; plan view labels `why` as the
  planner's reading (#460's unverified wording).
- [ ] **Step 2: RED** — `go test -race -count=1 ./internal/maku/skill ./internal/roomview ./internal/slackapp -run 'Skill|Suggestion|Proposal'`.
- [ ] **Step 3: Implement** — display only; the pin paragraph and the select come in Stage 3.
- [ ] **Step 4: GREEN** — the three packages whole.
- [ ] **Step 5: Commit** `feat(intake): show the suggested class, profile and merge outcome`

### Task 2.8: Docs and the stage gate (size S)

`docs/cli.md`, `docs/slack.md`, `docs/factory.md` (intake carries the class), release notes (hash change at
`plan/v2`). `task check` exits 0. Commit `docs: plan/v2 and the suggestion`.

**Stage 2 checkpoint:** live factory in `observe` before the release. ANR pin-bump PR: closes ANR #351,
adds `review` (with `docs`, `scripts` as ANR #351 lists) to the fallback `human.intake.plan.classes`.
Live check on the next session: one `intake_plan` per repository shows its catalogue's classes.

---

## Stage 3: pins (O2, P5, P6, P9, P10, P12, P13)

### Task 3.1: The profile refusals (size S)

**Files:** `internal/intake/refusal.go`, `internal/intake/refusal_test.go`

**Interfaces:** the ten codes of the spec as constants; `type ProfileRefusal struct { Code, Class,
Profile, Floor, Mode string; Allowed []string }`; `func (r ProfileRefusal) Sentence() string`. Kept apart
from `refuse()`'s single-`%s` rule because these sentences take several fields.

- [ ] **Step 1: Failing test** — `TestProfileRefusalSentences`: each code renders the spec's sentence;
  none contains a repository; an unknown code panics in tests only through a closed switch.
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/intake -run ProfileRefusal`.
- [ ] **Step 5: Commit** `feat(intake): the closed list of profile refusals`

### Task 3.2: One pin rule, two readers (size M)

**Files:** create `internal/intake/pin.go`, `internal/intake/pin_test.go`, `internal/intake/testdata/pins.json`;
create `internal/factory/triage/pin.go`, `internal/factory/triage/pin_test.go`

**Interfaces:**

```go
// intake: against the catalogue (no tests-paths read: that one is triage's)
func CheckPin(c catalogue.Catalogue, class, profile string, public bool) *ProfileRefusal
// triage: against config, the same order as the spec's Triage steps 2–4
type Pin struct { Name, Refused string } // Refused: a code from intake, "" when honoured
func ResolvePin(cfg *config.Config, repo, class string, labelPins []LabelPin, field, login string, public bool) Pin
```

- [ ] **Step 1: Failing tests** — both tests read `pins.json` (class, profile, maintainer, public →
  expected code) so the two readers cannot drift: `profile_unknown`, `class_no_profiles`,
  `class_profile_fixed` (live and shadow), `profile_not_allowed`, `profile_not_offered` (intake only),
  `not_maintainer`, `profile_internal`, honoured floor, honoured allowed. Triage adds
  `profile_conflict` (two labels; label ≠ field).
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/intake ./internal/factory/triage -run Pin`.
- [ ] **Step 5: Commit** `feat(intake): check a profile pin the way triage will`

### Task 3.3: The `profile` revision (size M)

**Files:** `internal/intake/revise.go`, `internal/intake/revise_test.go`, `internal/humanapi/intake.go`,
`internal/humanapi/intake_test.go`, `internal/metrics/metrics.go`, `internal/metrics/metrics_test.go`

**Interfaces:** `Revise` admits an edit kind `profile`: candidate equal to base except `issues[i].profile`
(and the matching record); it appends `D<n> profile: <p>, pinned by the developer` or, at finalisation
with `profile: ""` and a differing suggestion, `A<n> profile: factory's choice; suggested <s>`.
Metrics: `intake_profile_refusals_total{surface,reason}`, `intake_profile_choices_total{surface,class,choice}`
counted when a final plan is returned by `plan_check` (CLI surface) or filed by `run_create` (Slack).

- [ ] **Step 1: Failing tests** — `TestReviseProfileOnly`; `TestReviseProfileRejectsOtherEdits`
  (title, body, class changed alongside → refused); `TestProfileRecord` (D and A record grammar holds
  `CheckFinal`); `TestPlanCheckRefusesPin` (each Task 3.2 code through `plan_check`, plan not
  finalised); `TestPlanCheckNonMaintainerPin` → `not_maintainer`; `TestProfileChoiceMetric`
  (`factory`, `suggested`, `other`); no model call (fake planner call count 0).
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/intake ./internal/humanapi ./internal/metrics -run 'Revise|Profile|PlanCheck'`.
- [ ] **Step 5: Commit** `feat(intake): a maintainer pins a profile as a plan revision`

### Task 3.4: The pin's fields on the Task (size M)

**Files:** `api/factory/v1alpha1/task_types.go` (`TaskIntake.Profile`, `ProfileStatus.Requested` ≤ 63,
`ProfileStatus.Refused` ≤ 32), `api/factory/v1alpha1/task_crd_test.go` (rule counts as left by #453),
generated CRDs; `internal/factory/api/tasks.go` (`TaskRequest.Profile`); `internal/runrequest/tasks.go`;
`internal/humanapi/runcreate.go` (profile bound by the plan hash); `internal/envelope/policy.go`
(`ProfileFact.Requested`, `Refused`, validated); `internal/summary/summary.go`; tests of each.

- [ ] **Step 1: Failing tests** — CRD bounds and CEL; `TestRunCreateProfileBoundByPlan`;
  `TestProfileFactRequestedRefused` (bounds, refused is a known code); summary fold copies both.
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./api/factory/v1alpha1 ./internal/factory/api ./internal/runrequest ./internal/humanapi ./internal/envelope ./internal/summary`, then `task crd:gen` and `task crd:check`.
- [ ] **Step 5: Commit** `feat(api): a Task carries its requested and refused profile`

### Task 3.5: Triage honours or refuses the pin (size L)

**Files:** `internal/factory/triage/classify.go` (read `profile:` labels with `LabelEvents`, last applier),
`internal/factory/reconciler/reconciler.go` (`floorProfile` applies an honoured pin through `profileSpec`:
template, loop row, tier only upward within the task's ceiling), `internal/factory/reconciler/profile.go`,
`internal/factory/narrate/narrate.go` (`ProfileRefused`), `internal/factory/fmetrics/fmetrics.go`
(`agent_factory_profile_pins_total{class,profile,outcome,reason}`), tests of each.

- [ ] **Step 1: Failing tests** — `TestPinHonouredInObserveAndApply` (P9: `labeled`, `source=label`,
  `requested` set, no `profileChoices`; does not go through `variantFor`'s `observe` refusal);
  `TestPinRefusalsAtTriage` (table over the `pins.json` codes + `profile_conflict` and
  `profile_no_tests_paths`; task runs unpinned, cohort by hash, `refused` set, one narration line in the
  spec's wording); `TestPinLabelApplierMustBeMaintainer`; `TestPinFieldFromSponsorLogin`;
  `TestFailedScoperKeepsPin` (#455); `TestPinMetric` (`honoured`, `refused`, unknown name → `other`).
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/factory/triage ./internal/factory/reconciler ./internal/factory/narrate ./internal/factory/fmetrics`.
- [ ] **Step 5: Commit** `feat(factory): triage honours a maintainer's profile pin`

### Task 3.6: No step to the active profile (P10) (size S)

**Files:** `internal/factory/reconciler/stepup.go`, `internal/factory/reconciler/stepup_test.go`

```go
if target == "" || target == p.Name || p.Source == "escalation" || p.Cohort == "control" {
	return false, nil // the same profile again rarely converges: a maintainer decides (P10)
}
```

- [ ] **Step 1: Failing tests** — `TestStepUpSkipsActiveProfile` table: pinned `review-frontier` out of
  budget → escalated to a human; scoper-applied `review-frontier` → same; floor `review-floor` →
  steps to `review-frontier` as today.
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 -run StepUp ./internal/factory/reconciler`.
- [ ] **Step 5: Commit** `feat(factory): escalation never steps to the profile a task already runs`

### Task 3.7: The agreement metric (P13) (size S)

**Files:** `internal/factory/fmetrics/fmetrics.go`, `fmetrics_test.go`, `internal/factory/reconciler/profile.go`,
`internal/factory/reconciler/scoper_test.go`

- [ ] **Step 1: Failing tests** — `TestProfileAgreement` (`agree`, `differ`, `no_suggestion` for the
  issue path, `no_recommendation`), counted once per resolved task of a class that offers a choice;
  `TestScoperBriefNeverReadsSuggestion` (a canary in `spec.intake.suggested` is absent from every brief).
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/factory/fmetrics ./internal/factory/reconciler -run 'Agreement|Brief'`.
- [ ] **Step 5: Commit** `feat(factory): count how often the planner and the scoper agree`

### Task 3.8: The pin on the surfaces (size M ×3)

**a. CLI** — `internal/maku/skill/factory-handoff/SKILL.md` (the spec's full paragraph: three answers for a
maintainer, none for anyone else, a pin is a `plan_check` revision), `internal/maku/issuefile.go`
(order `class:`, `profile:`, `factory/ready`). Tests: `TestStartLabelsInOrder` gains the pin;
skill text test.
**b. Slack** — `internal/slackapp/proposal.go`, `proposalaction.go`, `internal/roomview/proposal.go`: a static
select "Profile: factory's choice | <offered floor> | <offered allowed…>" only for a maintainer and an
`off` class; choosing re-plans (new hash, `redrawProposal`); Confirm binds the hash shown; an "Options"
button posts `classes_list` ephemeral. Tests: select absent for non-maintainer, `live` class and
`shadow` class; choosing posts a revision; Confirm after a stale select → `plan_changed`.
**c. Room UI** — `web/src/` Harness profile block shows *Requested* and *Refused*; vitest; `task ui:check`.

Each: RED/GREEN on its packages, then commit `feat(maku): …`, `feat(slack): …`, `feat(ui): …`.

### Task 3.9: Docs and the stage gate (size S)

`docs/factory.md` (pins, refusals, evaluation table, **P10's behaviour change**), `docs/slack.md`,
`docs/cli.md`, release notes. `task check` exits 0.

**Stage 3 checkpoint:** live proof on the next aws-0 session: one pinned task (honoured, `labeled`),
one refused (`class_profile_fixed` on `docs-links`), one default; one label applied through `gh api` on
a repository where `profile:` did not exist yet (G5). ANR: pin bump; runbook line for the proof.

---

## Stage 5: the catalogue is the only class source

### Task 5.1: Drop `human.intake.plan.classes` (size M)

**Files:** `internal/config/config.go`, `internal/config/config_test.go` (the field is unknown, so a file
carrying it fails startup, by the strict-config rule), `internal/humanapi/intake.go`
(`catalogue_unavailable` instead of the fallback), `internal/intake/compose.go` (remove `Classes`),
`charts/maku/values.yaml`, `charts/maku/tests/render.sh`, `docs/`.

- [ ] **Step 1: Failing tests** — `TestPlanClassesRefused` (decode error names the field and says the
  catalogue replaced it); `TestIntakePlanCatalogueUnavailable` → `catalogue_unavailable`, no model call;
  chart render without the block.
- [ ] **Step 2–4:** RED/GREEN with `go test -race -count=1 ./internal/config ./internal/humanapi ./internal/intake` and `task chart:check`.
- [ ] **Step 5: Commit** `feat(intake)!: the catalogue is the planner's only class source`

**Stage 5 checkpoint:** `task check` exits 0. ANR's pin-bump PR removes `human.intake.plan.classes` from
`infrastructure/{base,aws-0,gcp-0}/room-broker/config.yaml` in the same commit (the broker refuses the
old key).

## Sizes at a glance

| Stage | Tasks (size) |
|---|---|
| 1 | 1.1 M · 1.2 M · 1.3 M · 1.4 M · 1.5 M · 1.6 S |
| 4 | 4.1 M · 4.2 S |
| 2 | 2.1 S · 2.2 L · 2.3 M · 2.4 M · 2.5 L · 2.6 M · 2.7 M · 2.8 S |
| 3 | 3.1 S · 3.2 M · 3.3 M · 3.4 M · 3.5 L · 3.6 S · 3.7 S · 3.8 M×3 · 3.9 S |
| 5 | 5.1 M |
