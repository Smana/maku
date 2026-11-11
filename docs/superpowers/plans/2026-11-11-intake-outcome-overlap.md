# Intake Outcome Overlap Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect and refuse confirmed duplicate outcomes across issue, sponsored API, and scheduled intake without duplicating execution or adding another orchestration service.

**Architecture:** Canonicalize sanitized goals and retrieve at most 20 candidates, using exact fingerprints for deterministic matches and the existing scoper for ambiguous candidates. Persist one bounded Task-status evaluation, replay it idempotently, and roll out through off, shadow, exact-active enforcement, and an explicit scoper-enforcement selector.

**Tech Stack:** Go, controller-runtime/envtest, Kubernetes CRDs/CEL, PostgreSQL room history, stdlib testing, Helm, k3d.

**Spec:** `docs/superpowers/specs/2026-11-11-intake-outcome-overlap-design.md`

## Global Constraints

- Maximum 20 candidates and 512 note characters. IDs/enums only in status, logs, traces, and metrics; no goal or candidate payloads.
- Only confirmed active duplicates refuse work. Completed, prior, partial, unverified, and no-match results never auto-block.
- A refusal starts no **implementation** AgentRun or implementation budget reservation; evaluation spend remains recorded. Exact-active refusal has zero runs.
- Persisted results replay idempotently. Pre-persistence reads may repeat; no scoper run is duplicated. Pending means empty `classification` and `checkedAt`.
- Default mode is `off`. `shadow` records without refusing. `enforce` starts exact-active only; `scoperEnforcement: true` is separately required for scoper-confirmed refusal.
- Use the existing scoper, deterministic AgentRun identity, recovery, test-first, fresh-run, and maintainer override paths. No new role, service, overlap database, initial index, or semantic preflight on every intake.
- TDD with table-driven stdlib tests and `-race`. Do not change `ActivePhase` or `TerminalPhase` semantics; use the spec's independent phase mapping.
- Preserve prior handoffs/review verdicts as fenced evidence. Candidate text is untrusted data, never an instruction.
- Run `task crd:gen` for generated artifacts; never hand-edit generated deepcopy/CRD files. Run `task check` and `task chart:install:k3d` before final integration.
- Conventional Commit subjects, no co-author trailers, no push without user authorization.

## Review Focus

1. **Reworded cross-source duplicate:** one scoper evaluation, no duplicate scoper run on restart; Task 5's replay and lost-write tests.
2. **Claim created but status write lost:** deterministic claim adoption, not a second scoper; Task 5's recovery test.
3. **Candidate prompt injection or fabricated citations:** untrusted text cannot force active refusal; Task 5's brief/output tests.
4. **Pending evaluation during mode rollback:** finish/adopt the existing evaluation instead of stranding it; Tasks 4–5's mode and recovery tests.
5. **Source errors and payload leakage:** failed sources become unverified with bounded evidence and no payload metrics; Tasks 3–4's outage and metrics tests.

---

### Task 1: Overlap status and CRD contract

**Files:**
- Create: `api/factory/v1alpha1/overlap.go`
- Create: `api/factory/v1alpha1/overlap_crd_test.go`
- Modify: `api/factory/v1alpha1/task_types.go` (TaskStatus)
- Modify: `api/factory/v1alpha1/task_crd_test.go` (TaskStatus CEL rule count)
- Generated: `api/factory/v1alpha1/zz_generated.deepcopy.go`, `config/crd/maku.ogenki.io_tasks.yaml`, `charts/maku-factory/crds/maku.ogenki.io_tasks.yaml`

**Interfaces:**
- Produces: `OverlapStatus`, `OverlapMatch`, six classifications, `OverlapPhase(phase string) (classification string, active bool)`, and `TaskStatus.Overlap *OverlapStatus`. Later tasks consume these exact types.

- [ ] **Step 1: Write failing schema, deepcopy, and phase tests**

In `overlap_crd_test.go`, assert:
- Classifications: `active_duplicate`, `completed_duplicate`, `prior_attempt`, `partial_overlap`, `unverified_overlap`, `no_match`.
- `relation`: `exact`, `lexical`; sources: `tasks`, `runs`, `rooms`, `github` (max 4).
- Matches max 20, note max 512, fingerprint exactly 64 lowercase hex, C2 name bounds for Task/Run/ScoperRun, issue/PR minimum 1.
- `has(classification) == has(checkedAt)` and immutable final classification/matches/sources/note; `scoperRun` may advance only while pending.
- TaskStatus CEL retains `overlap` when previously set.
- Deepcopy of a populated overlap does not alias matches, sources, or `checkedAt`.
- Envtest proves the CEL transition rules; `scoperRun` stores the evaluation run's C2 id.
- `OverlapPhase`: active = `Received`, `Triaged`, `Queued`, `Implementing`, `Reviewing`, `AwaitingCI`, `AutoMerging`, `Verifying`, `Coordinating`, `AwaitingHuman`, `Merged`; completed = `Done`, `NoOp`; prior = `Rejected`, `Reverted`, `Escalated`, `Closed`, `Stopped`; partial = `Split`; empty/unknown = unverified.

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test -race -count=1 ./api/factory/v1alpha1`
Expected: FAIL because `OverlapStatus`, `OverlapMatch`, and `OverlapPhase` are undefined.

- [ ] **Step 3: Implement the bounded model**

Define the types and constants from the spec in `overlap.go`, including JSON tags and kubebuilder validation markers. Add `Overlap *OverlapStatus` to `TaskStatus`. Use empty classification/`checkedAt` for pending review and `scoperRun` for the deterministic evaluation claim. Implement `OverlapPhase` without reusing cap-oriented helpers. Every CEL transition rule that reads an optional `oldSelf` field must guard access with `has()` so updates where it was unset are not rejected.

- [ ] **Step 4: Regenerate and verify**

Run `task crd:gen`, then `go test -race -count=1 ./api/factory/v1alpha1` and `task crd:check`.
Expected: PASS with no generated diff after regeneration.

- [ ] **Step 5: Commit**

Commit the new types, tests, TaskStatus change, and generated artifacts with `feat(api): add bounded overlap status`.

### Task 2: Canonical goals and candidate retrieval

**Files:**
- Create: `internal/factory/intake/overlap.go`
- Create: `internal/factory/intake/overlap_test.go`

**Interfaces:**
- Consumes: Task 1's `OverlapMatch` and `OverlapPhase`.
- Produces:
  - `const MaxOverlapCandidates = 20`
  - `CanonicalGoal(text string) string`
  - `GoalFingerprint(repository, text string) string`
  - `GoalTerms(text string) []string` — sorted, deduplicated, capped at 32 terms
  - `OverlapCandidate { Match v1alpha1.OverlapMatch; Repository, Text string; Evidence []string; Active bool; Recent time.Time }`
  - `OverlapResult { Fingerprint, Classification, Note string; Matches []v1alpha1.OverlapMatch; Sources []string; Pending bool }`
  - `RankOverlapCandidates(goal, acceptance string, candidates []OverlapCandidate) []OverlapCandidate`

- [ ] **Step 1: Write failing canonicalization and ranking tests**

Cover NFKC/fullwidth forms, case/whitespace/punctuation variants, repository scoping, and use of sanitized stored text rather than raw `Source.ContentSHA256`. Seed >20 candidates and assert active-first, distinct shared-term descending, recency descending, Task name ascending, stable repeat order, global cap after sorting, and exact-active detection before truncation. Cover empty goal/acceptance text and `Split`, `Merged`, `Escalated`, and empty phases.

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test -race -count=1 ./internal/factory/intake`
Expected: FAIL because the overlap functions/types are absent.

- [ ] **Step 3: Implement canonicalization and pure retrieval**

Use NFKC, lowercase, punctuation-to-space, and `strings.Fields` for canonical text. Hash `sha256(strings.ToLower(repository) + "\x00" + canonicalGoal)`. Have the reconciler extract acceptance text in-package and pass it to `RankOverlapCandidates`; `internal/factory/intake` must not import reconciler. Rank deterministically and exclude the current Task and its runs. Keep exact-active detection before the 20-candidate lexical cap and keep result evidence bounded. Candidate `Text` is the sanitized goal only—never an AgentRun's rendered task brief.

- [ ] **Step 4: Run the tests and verify GREEN**

Run: `go test -race -count=1 ./internal/factory/intake`
Expected: PASS.

- [ ] **Step 5: Commit**

Commit both files with `feat(intake): add outcome fingerprint and candidate ranking`.

### Task 3: Rollout configuration and bounded metrics

**Files:**
- Create: `internal/factory/config/overlap.go`
- Create: `internal/factory/config/overlap_test.go`
- Modify: `internal/factory/config/config.go` (Config.Intake, defaults, validation)
- Modify: `internal/factory/fmetrics/fmetrics.go`, `internal/factory/fmetrics/fmetrics_test.go`
- Modify: `charts/maku-factory/tests/render.sh`, `charts/agent-factory/tests/render.sh`

**Interfaces:**
- Produces:
  - `OverlapMode` values `off`, `shadow`, `enforce`
  - `Config.Intake.Overlap.{Mode,ScoperEnforcement}` with omitted/zero-value mode defaulting to `off`
  - `OverlapEvaluation(ctx, classification, source string)`, `OverlapScoperRun(ctx, outcome string)`, and `OverlapEvaluationDuration(ctx, duration)` metrics methods

- [ ] **Step 1: Write failing config, chart, and metrics tests**

Cover omitted block/mode → off, quoted/unquoted `off`, `shadow`, `enforce`, `scoperEnforcement`, invalid strings/true/unknown keys/duplicate mode, direct zero-value validation, and hash changes. Assert `config.intake.overlap.mode=shadow|enforce` renders into ConfigMaps. Assert exact metric names/labels, histogram exposure, unknown values → `other`, no payload strings, and nil-meter no-ops.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test -race -count=1 ./internal/factory/config ./internal/factory/fmetrics` and the two chart render scripts.
Expected: FAIL on missing config field, render output, and metric methods.

- [ ] **Step 3: Implement configuration and metrics**

Mirror `ScopeMode.UnmarshalJSON` for YAML boolean `off`. Add the `intake:` block with `overlap.mode` and `scoperEnforcement`, default off, strict validation, and duplicate-key protection. Keep chart `config` passthrough; do not add a parallel top-level value. Add bounded counters/histogram with only the six classifications and four sources.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run the commands from Step 2.
Expected: PASS.

- [ ] **Step 5: Commit**

Commit configuration, chart tests, and metrics with `feat(factory): add overlap rollout and metrics`.

### Task 4: Exact refusal and persisted replay

**Files:**
- Create: `internal/factory/reconciler/overlap.go`
- Create: `internal/factory/reconciler/overlap_test.go`
- Modify: `internal/factory/reconciler/reconciler.go` (consumer interface/wiring)
- Modify: `internal/factory/reconciler/text.go` (evidence block)
- Modify: `internal/factory/api/tasks_test.go` (rejected replay)
- Modify: `internal/factory/narrate/narrate.go`, `internal/factory/narrate/narrate_test.go`

**Interfaces:**
- Consumes: Tasks 1–3's status, pure retrieval, mode, and metrics.
- Produces: consumer-side `overlapEvaluator` interface and `retrieveOverlap`/`overlapDue` orchestration. Exact-active produces `Rejected` with reason `overlap_active_duplicate`; pending candidates have empty classification/`checkedAt` and a deterministic evaluation identity.

- [ ] **Step 1: Write failing lifecycle and replay tests**

Table-drive issue/API/schedule pairs with different names but the same canonical goal. Assert exact-active refusal, no implementation runs/reservations, exact citation, and stable replay. Cover completed/prior/partial/unverified/no-match nonblocking, `Merged` active until `Done`, other-repository isolation, off/shadow/enforce, source outages, status-write failure/retry, rejected API idempotent replay, and re-label/supersede after refusal producing new evidence without duplicate evaluation. Assert no source reads in off mode and no success effects before persistence.

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test -race -count=1 ./internal/factory/reconciler ./internal/factory/api ./internal/factory/narrate`
Expected: FAIL because overlap evaluation/refusal is absent.

- [ ] **Step 3: Implement exact evaluation and persistence**

Declare `overlapEvaluator` in the consumer and evaluate ordinary issue/API/schedule Tasks in `received` after daily-cap refusal and before trace/triage/room/run/budget effects. Retrieve Tasks → Runs → Rooms, enriching only retained candidates with GitHub evidence. Persist `no_match` or `unverified_overlap` where appropriate, exact-active refusal, and pending candidates for Task 5. Replay persisted results without re-retrieval; pre-persistence reads may repeat. Keep split/coordination/adopt/RunLore tasks out of this path.

- [ ] **Step 4: Run the tests and verify GREEN**

Run the commands from Step 2.
Expected: PASS.

- [ ] **Step 5: Commit**

Commit the evaluator, lifecycle integration, refusal narration, and tests with `feat(factory): reject exact duplicate outcomes`.

### Task 5: Scoper classification and recovery

**Files:**
- Create: `internal/factory/reconciler/overlap_brief.go`
- Create: `internal/factory/reconciler/overlap_brief_test.go`
- Modify: `internal/factory/reconciler/overlap.go`
- Modify: `internal/factory/reconciler/text.go`
- Modify: `internal/envelope/payloads.go`, `internal/mcp/tools.go` (typed `OverlapAssessment` and schema)
- Modify: `internal/factory/reconciler/implement.go` and `internal/factory/reconciler/scoper.go` (dispatch precedence/resume)

**Interfaces:**
- Consumes: Task 4's pending evaluation and Task 1–3's contracts.
- Produces: typed `OverlapAssessment { Classification, Citations, Sources, BlindSpots, Note }`, one deterministic scoper run for ambiguous candidates, and final persisted classification with `checkedAt`.

- [ ] **Step 1: Write failing scoper and recovery tests**

Cover one reworded candidate reaching scoper once, no repeated retrieval/create on restart, lost status after claim creation adopting the deterministic claim, active refusal without implementer, nonblocking continuation at the next ordinal run, malformed/inconclusive output → unverified, `no_match` accepted only when the scoper rules out every supplied candidate, fabricated citations rejected, and rollback finishing pending work. Include `scoperEnforcement=false` keeping scoper-active classifications advisory and `true` enabling refusal. Add prompt-injection cases for forged fences/instructions/oversize evidence and payload-canary absence.

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test -race -count=1 -run Overlap ./internal/factory/reconciler ./internal/envelope ./internal/mcp`
Expected: FAIL because typed assessment, brief, and overlap dispatch are absent.

- [ ] **Step 3: Implement typed assessment, brief, and lifecycle**

Persist retrieval before scoper creation. Begin the brief with the existing `ScoperMarkerPrefix`, then start overlap before ordinary scoper handling and before `scoperDue`, using the existing deterministic ordinal/reservation. Require active_duplicate to cite an active supplied candidate; all citations must be supplied IDs. Accept scoper `no_match` only after every candidate is ruled out. Apply `scoperEnforcement` only after a valid scoper-confirmed active result; limited enforcement refuses exact matches only. Keep pending classification empty until final, restore `scoperRun` from the expected claim after lost writes, and make failure/missing output/missing claim unverified without auto-blocking. Preserve prior handoffs/review verdicts as fenced evidence and cap the complete brief at the existing text bound.

- [ ] **Step 4: Run the tests and verify GREEN**

Run the commands from Step 2 plus `go test -race -count=1 ./internal/factory/reconciler`.
Expected: PASS.

- [ ] **Step 5: Commit**

Commit typed assessment, scoper lifecycle, recovery, and security tests with `feat(factory): classify overlap with existing scoper`.

### Task 6: Documentation and full acceptance verification

**Files:**
- Modify: `docs/factory.md`, `docs/architecture.md`, `docs/operations.md`, `charts/maku-factory/README.md`

**Interfaces:**
- Consumes: Tasks 1–5's complete behavior and tests.
- Produces: operator documentation for evidence, limitations, modes, enforcement selector, metrics, rollback, and shadow acceptance evidence.

- [ ] **Step 1: Document the feature and rollout**

Document canonical goal/fingerprint, 20-candidate bound, classifications, no-match versus unverified, exact/scoper refusal budget semantics, pending/replay, `off`/`shadow`/`enforce`/`scoperEnforcement`, rollback, metrics, and shadow evidence collection. Keep existing recovery, test-first, and fresh-run documentation consistent.

- [ ] **Step 2: Run focused and generated checks**

Run `task crd:check`, `task chart:check`, and `go test -race -count=1 ./api/factory/v1alpha1 ./internal/factory/intake ./internal/factory/reconciler ./internal/factory/api ./internal/factory/config ./internal/factory/fmetrics ./internal/factory/narrate`.
Expected: PASS.

- [ ] **Step 3: Run full verification**

Run `task check` and `task chart:install:k3d`.
Expected: every quality-gate step exits 0 and chart install prints `install OK on k3d`; temporary cluster is deleted.

- [ ] **Step 4: Commit and report evidence**

Commit documentation with `docs: describe outcome overlap rollout`. Report tests, shadow fixture classifications, refusal/replay evidence, and any untested production scenarios separately; do not claim production precision without shadow data.
