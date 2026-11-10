# Spec Intake Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the cross-client intake loop and a confirmation-safe `need_answer` tool that appends scoper answers without changing label authority.

**Architecture:** The broker serves one canonical `factory_intake` prompt, with `handoff` as an alias and tool descriptions as the prompts-less fallback. `room_brief` exposes an authorized structured question; the local relay combines that with a GitHub issue snapshot, confirms an exact append, re-checks for conflicts, and edits only the body.

**Tech Stack:** Go, MCP prompts/elicitation, `gh issue view/edit`, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-11-09-spec-intake-design.md`

**Depends on:** `docs/superpowers/plans/2026-11-09-spec-intake-planner-plan.md` (revision envelope and Decision grammar).

## Global Constraints

- `factory_intake` is served by `internal/humanapi/mcpprompts.go`; `maku mcp` only relays it.
- `handoff` remains and runs the same canonical loop; existing installed skills remain version-pinned.
- `need_answer` never adds or removes a label. A maintainer re-applies `factory/ready`.
- It acts only with `factory/needs-author` present and `factory/ready` absent.
- No elicitation, declined confirmation, changed body, or changed labels means no write.
- Public GitHub comments are only a fallback; an internal question is never invented or exposed without room-read authorization.
- Body content travels on stdin (`--body-file=-`), never as a secret-bearing argv value.

## Review Focus

- An internal task with no readable room must fail closed without parsing its redacted public comment: `TestNeedAnswerInternalWithoutRoom` in Task 4.
- A maintainer labeling while the form is open must invalidate the write: `TestNeedAnswerRejectsConflict` in Task 4.
- A prompts-less client still receives the revision-loop rules: `TestIntakeInstructionsWithoutPrompt` in Task 2.
- An ambiguous room match must produce no body write: `TestNeedAnswerRoomResolution` in Task 4.
- Multi-line or empty answers must not corrupt the one-line Decision grammar: `TestAppendDecision` in Task 3.

---

### Task 1: Structured scoper question in `room_brief`

**Files:**
- Modify: `internal/humanapi/brief.go`
- Modify: `internal/humanapi/brief_test.go`
- Modify: `internal/humanapi/mcp.go` (the `room_brief` output schema)

**Interfaces:**
- Produces: `type briefQuestion struct { Text, Recommended string }` with JSON fields `text`, `recommended`.
- `briefEnvelope` gains `Question *briefQuestion `json:"question,omitempty"``; populated only when the last scope has `State == envelope.ScopeQuestion`.
- `Recommended` is `scope.Assumptions[0]`, or empty when no assumption exists.

- [ ] **Step 1: Write the failing brief test**

Add `TestBriefQuestion`: a scope question with one assumption appears in structured MCP and HTTP answers; a ready scope and a question with no assumptions produce nil/empty recommendation respectively. Assert the existing rendered brief is unchanged.

```go
if got.Question == nil || got.Question.Text != "Which command proves this?" || got.Question.Recommended != "task check" {
    t.Fatalf("question = %+v", got.Question)
}
```

- [ ] **Step 2: Run the test to verify failure**

Run: `go test ./internal/humanapi -run TestBriefQuestion -count=1`
Expected: FAIL because `briefEnvelope.Question` does not exist.

- [ ] **Step 3: Populate the authorized structured field**

Set `Question` in `briefOf` after `lastScope` returns, using the same scope already rendered by the authorized room read. Add the optional object to the MCP output schema; do not add question text to logs or metrics.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/humanapi`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/humanapi/{brief.go,brief_test.go,mcp.go}
git commit -m "feat(humanapi): expose scoper questions in room briefs"
```

### Task 2: Canonical intake prompt and prompts-less instructions

**Files:**
- Modify: `internal/humanapi/mcpprompts.go`, `internal/humanapi/mcpprompts_test.go`
- Modify: `internal/humanapi/mcp.go`, `internal/humanapi/intake_test.go`
- Modify: `internal/maku/skill/factory-handoff/SKILL.md`
- Modify: `docs/cli.md`, `docs/api.md`

**Interfaces:**
- Produces: `func factoryIntakeText(what string) string`, used by both `factory_intake` and `handoff` entries.
- Consumes: planner plan’s revision envelope (`plan` + `hash` + `candidate`) and first-gap `Question` compatibility.

- [ ] **Step 1: Write failing prompt tests**

Add `TestFactoryIntakePrompt` and `TestIntakeInstructionsWithoutPrompt`. Require `prompts/list` to contain `factory_intake` and `handoff`; both texts must say: ask only the first gap, offer accept-the-rest, resolve markers with D/A records, pass base plan+hash+candidate, and finish with `plan_check` then `issue_file`. Require `intake_plan`’s tool description and server instructions to carry the short loop even when no prompt capability is declared.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/humanapi -run 'TestFactoryIntakePrompt|TestIntakeInstructionsWithoutPrompt' -count=1`
Expected: FAIL because the canonical prompt and fallback wording are absent.

- [ ] **Step 3: Implement the shared prompt and update the skill/docs**

Build `factory_intake` and `handoff` from `factoryIntakeText`; leave `plan`, `status`, `resume`, and `inbox` behavior unchanged. Update server/tool instructions without inventing a second planner. Replace the skill’s single-question filing instructions with prompt invocation and revision-envelope handling, retaining its room-following sections and version pin. Document `need_answer` as the scoper-bounce counterpart without implementing it here.

- [ ] **Step 4: Run docs and affected tests**

Run: `go test -race -count=1 ./internal/humanapi && task docs`
Expected: PASS and exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/humanapi internal/maku/skill/factory-handoff/SKILL.md docs/{cli.md,api.md}
git commit -m "feat(humanapi): serve the factory intake loop"
```

### Task 3: Pure Decision append and GitHub issue boundary

**Files:**
- Create: `internal/maku/needanswer.go`
- Create: `internal/maku/needanswer_test.go`
- Modify: `internal/maku/mcp.go` (injected issue boundary)
- Modify: `internal/maku/issuefile_test.go` if shared GH helpers move

**Interfaces:**
- Produces: `func AppendDecision(body, answer string) (string, error)`; trims and requires one non-empty line, appends `D<max+1>: <answer>`, creating `## Decisions` after Acceptance check when absent.
- Produces: `type IssueComment struct { Author, Body string; CreatedAt time.Time }` and `type IssueSnapshot struct { Body string; Labels []string; UpdatedAt time.Time; Comments []IssueComment }`.
- Produces: `type IssueEditor interface { View(context.Context, string, int) (IssueSnapshot, error); EditBody(context.Context, string, int, string) error }`.
- `Relay` gains `Issues IssueEditor`; production uses `type GHIssues struct{}` implementing that interface.

- [ ] **Step 1: Write failing pure and GH-shape tests**

Add `TestAppendDecision`: no section, existing interleaved/non-contiguous records, blank answer, multiline answer, and idempotent preservation of prior text. Add GH JSON fixture tests for labels/comments/updatedAt and verify `EditBody` receives the body on stdin.

```go
got, err := AppendDecision("## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\ntask check", "Use task check")
if err != nil || !strings.Contains(got, "## Decisions\n\nD1: Use task check") { t.Fatalf("append = %q, %v", got, err) }
```

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/maku -run 'TestAppendDecision|TestGHIssue' -count=1`
Expected: FAIL because the append helper and issue boundary are absent.

- [ ] **Step 3: Implement the pure append and `GHIssues`**

Use planner Task 2’s `sanitize.DecisionRecords` to find the maximum existing number; do not require contiguous numbering. Reject `\r` or `\n` in an answer rather than folding it. Implement `View` with `gh issue view <n> --repo=<repo> --json body,labels,updatedAt,comments` and `EditBody` with `gh issue edit <n> --repo=<repo> --body-file=-`, using `exec.CommandContext`, bounded output/time, and stdin.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/maku`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/maku
git commit -m "feat(maku): add confirmed issue decision edits"
```

### Task 4: `need_answer` relay integration

**Files:**
- Modify: `internal/maku/needanswer.go`, `internal/maku/needanswer_test.go`
- Modify: `internal/maku/localtools.go`, `internal/maku/localtools_test.go`
- Modify: `internal/app/maku.go` (production `Issues: maku.GHIssues{}` wiring)
- Modify: `docs/cli.md`, `docs/api.md`

**Interfaces:**
- Produces: local tool `need_answer` with required `{repo, issue}` and optional `{room}`; room is resolved through `rooms_list` when omitted.
- Consumes: Task 1’s `briefEnvelope.Question`, Task 3’s `IssueEditor` and `AppendDecision`.
- Result: `{answered: true, issue: <url>, started: false, next: "a maintainer must re-apply factory/ready"}`; no label call exists in this path.

- [ ] **Step 1: Write failing integration tests**

Add table-driven tests for success via room brief, public-comment fallback, `TestNeedAnswerInternalWithoutRoom`, `TestNeedAnswerRoomResolution` (none/ambiguous), `TestNeedAnswerRejectsConflict` (body, labels, ready already present), wrong labels, no elicitation, decline, and missing `Issues`. Assert `EditBody` is called exactly once only on confirmed unchanged success and `Label` is never called.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/maku -run 'TestNeedAnswer' -count=1`
Expected: FAIL with `unknown tool: need_answer` or missing behavior.

- [ ] **Step 3: Implement the guarded flow**

Decode strict arguments; load the issue and require needs-author/no-ready. Resolve one room, call `room_brief`, and use its structured question. If unavailable, parse the newest public bounce comment carrying the factory’s `event=needs-author-…` marker and question/assumption; if it contains no question, return `needs_room_access` without writing. Build the exact append, show question/recommendation/append in a form with required `answer` and default-false `send`, then re-read and require identical body, labels, and updatedAt before `EditBody`. Wire `Issues: maku.GHIssues{}` in `internal/app/maku.go`. Return the explicit maintainer-next result and document partial/failure outcomes.

- [ ] **Step 4: Run the complete repository gate**

Run: `go test -race -count=1 ./internal/maku ./internal/humanapi && task check`
Expected: PASS and exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/maku internal/app/maku.go docs/{cli.md,api.md}
git commit -m "feat(maku): answer scoper bounces without relabeling"
```
