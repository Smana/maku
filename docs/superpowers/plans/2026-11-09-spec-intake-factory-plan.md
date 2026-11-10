# Spec Intake Factory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep scoper Decisions settled and maintain exactly one durable, current-generation status comment per factory issue.

**Architecture:** The scoper prompt treats numbered D/A records as settled while acceptance remains in its operative section. A small `TaskForge` extension supports comment IDs, edits, and full pagination; narrate renders and synchronizes an issue-scoped comment whose ID/body are persisted in Task status, with marker recovery and owner checks across generations.

**Tech Stack:** Go, controller-runtime, go-github, Kubernetes CRD generation, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-11-09-spec-intake-design.md`

## Global Constraints

- The scoper rule is prompt-enforced; `askForAcceptance` and `statesAcceptance` remain unchanged.
- Existing event comments and `RecentComments` behavior remain unchanged.
- Only `TaskForge` and narrate’s consumer interface gain comment methods; do not widen the merger App’s powers.
- Status creation/edit is best-effort and never prevents a Task transition or event narration.
- No live check state or mid-run spend is added.
- Recovery paginates all comments; the recent-50 window is never the status identity store.
- A stale task generation must not overwrite the current generation’s comment.
- Run `crd:gen` after API edits; never hand-edit generated deepcopy/CRD output.

## Review Focus

- A status marker older than 50 comments must still be adopted without duplication: `TestSyncStatusRecoversBeyondRecentWindow` in Task 3.
- A crash after GitHub creation but before the Task status write must recover by marker: `TestSyncStatusRecoversCreatedComment` in Task 3.
- An escalated old generation must not publish over a new owner: `TestStatusCommentGenerationOwnership` in Task 4.
- An unchanged reconcile must make zero comment calls: `TestSyncStatusNoOp` in Tasks 3–4.
- A Decision-only acceptance record must not bypass the deterministic acceptance bounce: `TestAcceptanceStillRequiresOperativeSection` in Task 1.

---

### Task 1: Scoper respects settled Decisions

**Files:**
- Modify: `internal/factory/reconciler/text.go`
- Modify: `internal/factory/reconciler/scope_brief_test.go`
- Modify: `internal/factory/reconciler/scoper_acceptance_test.go`

**Interfaces:**
- No Go API change; `ScoperBrief` gains the exact settled-record rule in `scoperHow`.

- [ ] **Step 1: Write failing prompt and acceptance tests**

Add `TestScoperBriefKeepsDecisionsSettled`: the brief says every `D<n>`/`A<n>` line, with or without `[g<n>]`, is settled and MUST NOT be reopened; questions may cover only uncovered requirements. Add `TestAcceptanceStillRequiresOperativeSection`: an issue with `D1: run task check` under Decisions but an empty Acceptance check still follows the existing `askForAcceptance` path.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/factory/reconciler -run 'TestScoperBriefKeepsDecisionsSettled|TestAcceptanceStillRequiresOperativeSection' -count=1`
Expected: prompt test FAIL; the new acceptance case proves the deterministic guard is unchanged.

- [ ] **Step 3: Add the concise prompt rule**

Append to `scoperHow`: numbered Decision/Assumption lines are author-settled, never reopen them, and ask only about uncovered work. Do not add a text validator or alter acceptance parsing.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/factory/reconciler`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/reconciler/{text.go,scope_brief_test.go,scoper_acceptance_test.go}
git commit -m "feat(factory): keep intake decisions settled during scoping"
```

### Task 2: Comment identity, editing, and full recovery pagination

**Files:**
- Modify: `internal/factory/reconciler/reconciler.go` (`TaskForge` only)
- Modify: `internal/factory/narrate/narrate.go` (`commenter` only)
- Modify: `internal/factory/forge/github.go`, `internal/factory/forge/github_test.go`
- Modify: `internal/factory/forge/forgetest/fake.go`, `internal/factory/forge/forgetest/fake_test.go`

**Interfaces:**
- `TaskForge` and narrate’s `commenter` gain:
  - `CommentWithID(ctx context.Context, number int, body string) (int64, error)`
  - `EditComment(ctx context.Context, id int64, body string) error`
  - `AllComments(ctx context.Context, number int) ([]forge.Comment, error)`
- Existing `Comment(ctx, number, body) error` remains and delegates to `CommentWithID`, discarding the ID.

- [ ] **Step 1: Write failing forge tests**

Add `TestCommentIdentityAndEdit`: creation returns a stable nonzero ID, edit changes only that body and marks it edited. Add `TestAllCommentsPaginates`: more than 100 comments are all returned, newest first, without the 50-item cap. Add equivalent fake behavior tests.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/factory/forge ./internal/factory/forge/forgetest -run 'TestCommentIdentityAndEdit|TestAllCommentsPaginates' -count=1`
Expected: FAIL because the methods are undefined.

- [ ] **Step 3: Implement real and fake methods**

Use `Issues.CreateComment`’s returned comment ID and `Issues.EditComment` for the real forge, preserving `mark`/`permanent` error behavior. Implement `AllComments` with the existing 100-per-page list options, following `NextPage` to zero and reusing one conversion helper. Give the fake monotonically increasing IDs, editable storage, and unbounded `AllComments`; leave `RecentComments` capped at 50.

- [ ] **Step 4: Run forge and narrate compile/tests**

Run: `go test -race -count=1 ./internal/factory/forge ./internal/factory/forge/forgetest ./internal/factory/narrate ./internal/factory/reconciler`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/forge internal/factory/narrate/narrate.go internal/factory/reconciler/reconciler.go
git commit -m "feat(factory): edit and recover issue comments by id"
```

### Task 3: Status rendering, Task ledger, and narrator synchronization

**Files:**
- Modify: `api/factory/v1alpha1/task_types.go`
- Create: `internal/factory/narrate/status.go`
- Create: `internal/factory/narrate/status_test.go`
- Generated: `api/factory/v1alpha1/zz_generated.deepcopy.go`, `config/crd/`, `charts/`

**Interfaces:**
- `TaskStatus` gains `StatusComment *StatusComment `json:"statusComment,omitempty"``; `StatusComment` has `ID int64 `json:"id,omitempty"`` and `Body string `json:"body,omitempty"`` (body max 4096).
- Produces: `func Status(t *v1alpha1.Task, roomsURL string) string`.
- Produces: `func (n Narrator) SyncStatus(ctx context.Context, t *v1alpha1.Task, roomsURL string) error`, mutating only `t.Status.StatusComment` after successful reads/writes.

- [ ] **Step 1: Write failing rendering and synchronization tests**

Add `TestStatus`: phase/PhaseSince, room link, `summary.Waiting` wording, spec SHA, separate D/A counts, and plain PR link; no checks or spend. Add `TestSyncStatusNoOp`, `TestSyncStatusRecoversCreatedComment`, `TestSyncStatusRecoversBeyondRecentWindow`, and failed-create/edit retry tests. Use 60+ fake comments to prove recovery does not depend on `RecentComments`.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/factory/narrate -run 'TestStatus|TestSyncStatus' -count=1`
Expected: FAIL because the renderer, ledger, and sync method are absent.

- [ ] **Step 3: Implement the ledger and pure sync algorithm**

Render a constant issue-scoped marker plus an owner marker containing `t.Name` and the generation parsed from `Task.Spec.Source.Key`’s `:gen<n>` suffix. If no ID is recorded, search `AllComments` by factory login and issue marker; create with `CommentWithID` only when none exists. Record ID/body after successful creation/adoption. Edit only when the newly rendered body differs; a failed write leaves the previous successful body recorded. For a terminal Task, read the current owner before editing: a newer generation may adopt even after ending, an equal generation must own the marker, and an older generation returns without writes. Use `summary.Waiting` with the Task’s phase/reason facts and `sanitize.DecisionRecords` for counts. Run `task crd:gen` for the new status type.

- [ ] **Step 4: Run API, narrate, and CRD checks**

Run: `go test -race -count=1 ./internal/factory/narrate && task crd:check`
Expected: PASS and no generated diff.

- [ ] **Step 5: Commit**

```bash
git add api/factory/v1alpha1 config/crd charts internal/factory/narrate/status{,_test}.go
git commit -m "feat(factory): synchronize one issue status comment"
```

### Task 4: Reconciler lifecycle and generation ownership

**Files:**
- Create: `internal/factory/reconciler/status_comment.go`
- Create: `internal/factory/reconciler/status_comment_test.go`
- Modify: `internal/factory/reconciler/reconciler.go`
- Modify: `docs/factory.md`

**Interfaces:**
- Consumes: `Narrator.SyncStatus` and `Status(t, roomsURL)` from Task 3.
- Produces: `func (r *Reconciler) statusCommentDue(t *v1alpha1.Task) bool` and `func (r *Reconciler) syncStatusComment(ctx context.Context, t *v1alpha1.Task) error`.

- [ ] **Step 1: Write failing reconciler lifecycle tests**

Add `TestStatusCommentLifecycle`: first narration-bearing reconcile creates one comment; later material transitions edit it; identical reconciles make no calls; terminal outcome is rendered. Add `TestStatusCommentGenerationOwnership`: generation 2 adopts the same ID while the escalated generation 1 performs no edit. Add `TestStatusCommentFailuresDoNotBlockTransition`: GitHub failure is returned for retry after the Task transition/event behavior has run. Assert refused issue intake creates no Task/status comment through existing intake tests.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/factory/reconciler -run 'TestStatusComment' -count=1`
Expected: FAIL because no reconciler calls SyncStatus.

- [ ] **Step 3: Wire synchronization after written transitions**

Compute `statusCommentDue` for issue Tasks with a narration outbox/record or an existing status comment whose rendered body differs. Include it in the terminal early-return condition and the post-transition processing condition. Call `syncStatusComment` beside outbox/facts processing so the existing second status update persists its ID/body; never create it in the intake poller. Keep event drain ordering and return sync errors for a later reconcile without rolling back work. Document the single status comment and its reduced fields.

- [ ] **Step 4: Run the complete repository gate**

Run: `go test -race -count=1 ./internal/factory/reconciler ./internal/factory/narrate && task check`
Expected: PASS and exit 0, including generated CRD/chart checks.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/reconciler docs/factory.md
git commit -m "feat(factory): keep issue status current across generations"
```
