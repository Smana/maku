# Team Evaluation Room Profile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the resolved harness profile and evaluation cohort in the room UI and document a repeatable local apply/observe walkthrough.

**Architecture:** Add an optional `profile` value to `envelope.TaskFacts`, populate it from `Task.Status.Profile`, and pass it through `summary/v1.status` without any new I/O. Render it in the existing room summary area, then update the API, event, UI, and quickstart documentation.

**Tech Stack:** Go, Kubernetes Task CRD status, room event envelopes, `summary/v1`, TypeScript, Vitest, Helm, Markdown.

**Spec:** `docs/superpowers/specs/2026-11-10-team-evaluation-room-profile-design.md`

## Global Constraints

- All new contract fields are optional; older clients and profile-free rooms keep their current behavior.
- `Task.Status.Profile` is the only source of truth; never re-resolve a profile or parse narration.
- Render model-written `why` with `textContent`, never HTML.
- Preserve profile facts after terminal outcomes and add no GitHub, factory, or Kubernetes read to summary rendering.
- Do not change evaluation decisions, merge policy, or the Settings workflow.
- Rebuild and commit `internal/humanapi/ui/dist` whenever TypeScript changes.
- Run the complete `task check` before opening the implementation PR.

## Review Focus

- A room without profile facts must render no empty block: Tasks 1 and 2 test absent profile values.
- Invalid enums or overlong fields must fail validation before the broker stores facts: Task 1 tests each bound.
- Terminal tasks must retain their profile: Task 1 tests `factsKey` and `factsOf` after `Done`.
- Observe mode must distinguish the active floor from a variant recommendation: Task 2 renders that exact state.
- Model-written `why` must remain inert and refreshes must preserve the open fold: Task 2 tests both.
- Sealed logs created before this change must remain valid and render no profile block: Task 1 tests omission and Task 4 checks an older live room.

---

### Task 1: Profile Contract and Read Path

**Files:**
- Modify: `internal/envelope/policy.go`
- Modify: `internal/envelope/policy_test.go`
- Modify: `internal/envelope/payloads.go`
- Modify: `internal/factory/reconciler/facts.go`
- Modify: `internal/factory/reconciler/facts_test.go`
- Modify: `internal/summary/summary.go`
- Modify: `internal/summary/summary_test.go`

**Interfaces:**
- Produces: `envelope.ProfileFact{Name, Source, Cohort, Recommended, Reason, Why string}` and `TaskFacts.Profile *ProfileFact`.
- Produces: `summary/v1.status.profile`, the same optional JSON object.

- [ ] **Step 1: Write failing validation and propagation tests**

Add table-driven tests for valid profiles and for an empty/overlong name, invalid source, invalid cohort, overlong `recommended`, `reason`, and `why`. Add a reconciler test asserting that `factsOf` copies all six fields from a `ProfileStatus`, retains them in terminal facts, and leaves `Profile` nil without `ProfileStatus`. Add a summary test asserting exact profile propagation and omission.

- [ ] **Step 2: Run the Go tests and verify the expected failures**

Run `go test -race -count=1 ./internal/envelope ./internal/factory/reconciler ./internal/summary`.
Expected: compilation or assertion failures because `ProfileFact`, `TaskFacts.Profile`, and `status.profile` do not exist.

- [ ] **Step 3: Implement the additive contract and propagation**

Define `ProfileFact` with JSON fields `name`, `source`, `cohort`, `recommended`, `reason`, and `why`, all optional except the first three. Validate source against `label|floor|control|scoper|escalation`, cohort against `labeled|control|treatment`, and the CRD bounds 63/63/32/200 characters for name/recommended/reason/why. Call its validator from `TaskFacts.Validate` with a `task facts: profile:` error prefix. Copy `Task.Status.Profile` in `factsOf`, and assign `f.Profile` to `Summary.Status.Profile` in `View`.

- [ ] **Step 4: Run the Go tests and verify they pass**

Run the command from Step 2. Expected: all three packages pass with race detection and no golden rewrites.

- [ ] **Step 5: Commit the contract change**

```bash
git add internal/envelope internal/factory/reconciler/facts.go internal/factory/reconciler/facts_test.go internal/summary
git commit -m "feat(rooms): carry harness profiles in task summaries"
```

### Task 2: Harness Profile Room Block

**Files:**
- Create: `web/src/profile.ts`
- Create: `web/test/profile.test.ts`
- Modify: `web/src/summary.ts`
- Modify: `web/src/room.ts`
- Regenerate: `internal/humanapi/ui/dist/assets/app.js`

**Interfaces:**
- Consumes: `Summary.status.profile` from Task 1.
- Produces: `renderProfile(root: HTMLElement, s: Summary): void`, called beside `renderPolicy`.

- [ ] **Step 1: Write failing UI tests**

Cover: no block without a profile; headline `Harness profile · review-frontier · treatment`; all six fields; observe mode with active `review-floor`, `recommended=review-frontier`, `reason=observe`; `<img>` in `why` rendered as text; and an opened details element staying open after `renderProfile` runs again.

- [ ] **Step 2: Run the UI test and verify it fails**

Run `npm test -- test/profile.test.ts` with the pinned Node 24.21.0 toolchain.
Expected: the module or render function is missing.

- [ ] **Step 3: Implement the room block**

Add `profile?: {name: string; source: string; cohort: string; recommended?: string; reason?: string; why?: string}` to the TypeScript `Summary.status`. Implement `renderProfile` using the existing `el` helper and details/open-state pattern, then call it from `room.ts` after `renderPolicy` so both blocks share the existing summary area.

- [ ] **Step 4: Run the focused and full UI checks**

Run `npm test -- test/profile.test.ts`, then `task ui:test`. Expected: the new cases and the complete UI suite pass.

- [ ] **Step 5: Rebuild and commit the UI**

Run `task ui:build`, verify `git diff --check`, then commit the source, tests, and generated bundle with `feat(ui): show the task's harness profile`.

### Task 3: Contract Reference and Quickstart Guide

**Files:**
- Modify: `docs/event-envelope.md`
- Modify: `docs/api.md`
- Modify: `docs/web-ui.md`
- Modify: `docs/quickstart.md`

**Interfaces:**
- Documents the exact `status.profile` shape from Task 1 and the room behavior from Task 2.

- [ ] **Step 1: Document the contract and room block**

Describe optional profile facts in the TaskFacts and `summary/v1` references and the Harness profile block in the web UI guide, including omission for older/profile-free rooms.

- [ ] **Step 2: Add the local apply/observe walkthrough**

Document the secret-free `.demo/evaluation.yaml`, full repository-list preservation, `helm upgrade` with both values files, the explicit Task CRD `kubectl apply`, Task/AgentRun inspection, and a same-shell metrics port-forward. Include apply and observe expectations and the valid `reason=none` model-dependent negative case.

- [ ] **Step 3: Verify documentation and commit**

Run `bash hack/docs_test.sh && bash hack/docs.sh`, inspect the rendered relative links, and commit with `docs: explain team evaluation locally`.

### Task 4: Full Verification, Review, and Live Smoke

**Files:**
- Review the complete branch diff, including both spec commits.

- [ ] **Step 1: Run the full repository gate**

Run `task check` with Node 24.21.0 first on PATH. Expected: exit 0, including all Go race tests, UI tests, and committed bundle verification.

- [ ] **Step 2: Obtain an independent full-range review**

Provide the reviewer the spec, this plan, the merge base and head, and the Review Focus list. Address Critical or Important findings through a new red/green test cycle; record any deferred Minor findings.

- [ ] **Step 3: Push and open the implementation PR**

Rebase onto a freshly fetched `origin/main` if necessary, push `feat/team-evaluation-room-profile`, and open a conventional-commit PR containing the contract, UI, docs, verification evidence, and any deferred limitations.

- [ ] **Step 4: Deploy PR images and smoke-test the room**

Once PR prerelease images exist, inspect the tag with `docker buildx imagetools`, upgrade the existing demo, and create one bounded floor-profile task. Verify its `summary/v1` response carries `review-floor` with no applied recommendation and its room page returns 200; Vitest proves the corresponding block renders. Verify an older sealed room's summary still omits `status.profile`. Save results in the PR and leave the demo in `observe` mode.
