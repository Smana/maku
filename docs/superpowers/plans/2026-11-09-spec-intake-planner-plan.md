# Spec Intake Planner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ranked clarification gaps and deterministic, hash-safe plan revisions without invalidating existing `plan/v1` consumers.

**Architecture:** The model emits ranked gaps whose markers live in hashed issue bodies. A pure revision engine verifies a base plan and admits only marker replacements plus corresponding Decision records; broker tools use it before re-planning or final validation. Display-only compatibility fields keep Slack and older room clients working.

**Tech Stack:** Go, stdlib `testing`, existing MCP JSON schemas, `internal/intake`, `internal/humanapi`, `internal/factory/sanitize`.

**Spec:** `docs/superpowers/specs/2026-11-09-spec-intake-design.md`

## Global Constraints

- Keep `apiVersion: plan/v1`; do not add gaps, recommendations, `round`, `roundsLeft`, or `Question` to `Material`.
- Gap IDs are `g1`, `g2`…, unique within a plan; cross-round stability is never required.
- Decision records use `D1 [g1]: choice, because reason` and `A2 [g2]: assumed answer, because why`; scoper records may omit `[g<n>]`.
- Split issues repeat only needed records: per-body IDs ascend and are unique, but numeric gaps are allowed.
- `human.intake.plan.maxRounds` defaults to 10; `dailyPlans` remains the enforced model-cost bound.
- `plan_check`, accept-the-rest, and the cap exit make no model call.
- `intake.Check` remains the intermediate validator; only final filing doors reject clarification markers.
- Use stdlib tests with `-race`; no new dependencies.

## Review Focus

- A candidate that changes a title, role, budget, dependency, or non-marker body text must be refused: `TestReviseRejects` in Task 3.
- Duplicate/orphan markers and repeated split Decision subsets must not confuse correlation: `TestDecisionGrammar` and `TestReviseCorrelation` in Tasks 2–3.
- A model failure or the round cap must not silently invoke the degraded composer or an extra model call: `TestRevisionMakesOneOrNoModelCall` in Task 4.
- Existing Slack metadata must retain its old hash and first-question rendering: `TestLegacyPlanCompatibility` in Tasks 1 and 5.
- A plan-less draft or factory API request carrying a marker must be refused just like `plan_check`: `TestFinalDraftDoors` in Task 5.

---

### Task 1: Ranked gap contract and model composition

**Files:**
- Modify: `internal/intake/plan.go`
- Modify: `internal/intake/model.go`
- Modify: `internal/intake/prompt.go`
- Modify: `internal/intake/compose.go`
- Test: `internal/intake/plan_test.go`, `internal/intake/model_test.go`, `internal/intake/compose_test.go`

**Interfaces:**
- Produces: `type Gap struct { ID, Section, Text string; Recommended Answer }`, `Plan.Gaps []Gap`, `Plan.Round int`, `Plan.RoundsLeft int`.
- Produces: `func WithGaps(p Plan, gaps []Gap) Plan`, which clones `gaps`, sets `Plan.Gaps`, and maps the first gap to `Plan.Question` (nil for none).
- `Draft.Gaps []Gap` replaces `Draft.Question`; `ComposeIn` gains `Round, RoundsLeft int`.

- [ ] **Step 1: Write failing contract tests**

Add `TestPlanGapsStayDisplayOnly`: hash a legacy plan before/after setting `Gaps`, `Round`, and `RoundsLeft`; require identical hashes and first-gap `Question` mapping. Add `TestComposeRankedGaps`: two model gaps survive in order and produce `Question == &Question{Text: "first…", …}`. Add a prompt test requiring `"gaps"` and marker instructions, not the old single-question shape.

```go
before := Hash(p)
p = WithGaps(p, []Gap{{ID: "g1", Text: "first", Recommended: Answer{Answer: "use one"}}, {ID: "g2"}})
p.Round, p.RoundsLeft = 2, 8
if got := Hash(p); got != before { t.Fatalf("display fields changed hash: %s != %s", got, before) }
if p.Question == nil || p.Question.Text != "first" { t.Fatalf("first gap not mapped: %+v", p.Question) }
```

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/intake -run 'TestPlanGapsStayDisplayOnly|TestComposeRankedGaps|TestSystemPrompt' -count=1`
Expected: FAIL because `Gap`, `WithGaps`, and ranked prompt/composition do not exist.

- [ ] **Step 3: Implement the ranked contract**

Add the exact types/fields above with JSON names `gaps`, `round`, `roundsLeft`. Change the strict model reply and system prompt to ranked `gaps`; require each gap to name one marker already in an issue body. Compose with `WithGaps` and carry `Round`/`RoundsLeft` from `ComposeIn`. Leave `material` unchanged.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/intake`
Expected: PASS, including existing legacy hash fixtures.

- [ ] **Step 5: Commit**

```bash
git add internal/intake/{plan.go,model.go,prompt.go,compose.go,plan_test.go,model_test.go,compose_test.go}
git commit -m "feat(intake): compose ranked clarification gaps"
```

### Task 2: Shared marker, Decision, and final-draft validation

**Files:**
- Create: `internal/factory/sanitize/clarification.go`
- Create: `internal/factory/sanitize/clarification_test.go`
- Modify: `internal/intake/refusal.go`
- Test: `internal/intake/plan_test.go`

**Interfaces:**
- Produces: `type ClarificationMarker struct { ID, Text string; Start, End int }` with byte offsets, and `func ClarificationMarkers(body string) ([]ClarificationMarker, error)`.
- Produces: `type DecisionRecord struct { Kind string; Number int; Gap, Text string }`, and `func DecisionRecords(body string) ([]DecisionRecord, error)`.
- Produces: `func CheckFinalDraft(title, body string) (Draft, error)`; errors begin `unresolved_marker:` or `invalid_decisions:` as applicable.
- Produces: `func CheckFinal(p Plan, candidates []string, classes map[string]Class) *Refusal` in `internal/intake`, which calls `Check` then `CheckFinalDraft` for every issue and requires no gaps.

- [ ] **Step 1: Write failing parser and final-validation tests**

Add table-driven `TestDecisionGrammar` and `TestFinalDraft`: accept interleaved `D1 [g1]`, `A2 [g2]`, a subset `D1, D3`, and scoper `D4:`; reject duplicate/descending IDs, malformed gap references, a Decisions section before Acceptance, and any remaining marker. Add `TestCheckFinalRequiresNoGaps` in intake.

```go
for _, records := range []string{"D1: first\nD1: duplicate", "D2: descending\nD1: later", "D1 [g0]: bad gap"} {
    body := "## Acceptance check\ntask check\n\n## Decisions\n\n" + records
    if _, err := DecisionRecords(body); err == nil || !strings.HasPrefix(err.Error(), "invalid_decisions:") {
        t.Fatalf("DecisionRecords(%q) error = %v", body, err)
    }
}
```

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/factory/sanitize ./internal/intake -run 'TestDecisionGrammar|TestFinalDraft|TestCheckFinalRequiresNoGaps' -count=1`
Expected: FAIL because the parsers and final validator are absent.

- [ ] **Step 3: Implement deterministic validation**

Parse markers with one compiled expression for `[NEEDS CLARIFICATION: g<n> <text>]`, rejecting malformed IDs and duplicate IDs per body. Parse only single-line records under the optional `## Decisions` after Acceptance check. `CheckFinalDraft` calls `CheckDraft`, validates records, then rejects markers. Add `ReasonUnresolvedMarker` and `ReasonInvalidDecisions` with fixed sentences to intake’s closed refusal list and map final-draft errors in `CheckFinal`.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/factory/sanitize ./internal/intake`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/sanitize/clarification{,_test}.go internal/intake/{refusal.go,plan_test.go}
git commit -m "feat(intake): validate final drafts and decisions"
```

### Task 3: Pure revision engine

**Files:**
- Create: `internal/intake/revise.go`
- Create: `internal/intake/revise_test.go`

**Interfaces:**
- Consumes: Task 1’s `WithGaps`; Task 2’s marker/Decision parsers.
- Produces: `func Revise(base Plan, baseHash string, candidate Plan, maxRounds int) (Plan, error)`.
- Produces: `func CheckCarryForward(base, candidate, next Plan) *Refusal`, which rejects a model re-plan that drops or rewrites settled replacement text or existing D/A records.

- [ ] **Step 1: Write failing revision tests**

Add `TestRevise`, `TestReviseRejects`, `TestReviseCorrelation`, and `TestReviseCapAcceptsRemaining`. Pin: base hash verification; legacy base round 0 treated as 1; candidate new hash; one D/A record per resolved marker; unchanged non-marker material; per-body ascending non-contiguous IDs; cap conversion of every remaining recommendation to `A<n>` with no remaining gaps/markers.

```go
got, err := Revise(base, Hash(base), candidate, 10)
if err != nil { t.Fatal(err) }
if got.Round != 2 || got.RoundsLeft != 8 { t.Fatalf("rounds = %d/%d", got.Round, got.RoundsLeft) }
if got.Hash == Hash(base) || got.Hash != Hash(got) { t.Fatalf("candidate hash not renewed: %+v", got) }
```

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/intake -run 'TestRevise|TestCheckCarryForward' -count=1`
Expected: FAIL because `Revise` and `CheckCarryForward` are undefined.

- [ ] **Step 3: Implement the pure engine**

Verify `Hash(base) == baseHash` before reading candidate content. Compare repository, budget, issue order/title/class/roles/dependencies exactly. Validate body cores as unchanged segments with only existing marker spans replaced by non-empty text; require candidate Decisions sections to preserve old records and add exactly one same-issue record per resolved marker. Allocate new record IDs in one global interleaved sequence. Recompute display gaps from remaining markers using `WithGaps`; set round/roundsLeft and rehash. At `round == maxRounds`, apply remaining recommendations and append Assumptions; above the cap return `planner_invalid`. `CheckCarryForward` compares the model’s next plan against the verified base/candidate edits.

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/intake`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/intake/revise{,_test}.go
git commit -m "feat(intake): revise plans through deterministic edits"
```

### Task 4: Broker revision wiring and round configuration

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `internal/app/broker.go`, `internal/app/broker_test.go`
- Modify: `internal/humanapi/intake.go`, `internal/humanapi/intake_test.go`
- Modify: `internal/intake/model.go`, `internal/intake/prompt.go`, `internal/intake/model_test.go`

**Interfaces:**
- `PlanConfig.MaxRounds int `json:"maxRounds,omitempty"``, `DefaultMaxRounds = 10`, `humanapi.Intake.MaxRounds int`.
- `intakeArgs` and `planCheckArgs` gain `Candidate *intake.Plan `json:"candidate,omitempty"``; `intakeArgs` also gains `Plan *intake.Plan `json:"plan,omitempty"`` and `Hash string `json:"hash,omitempty"``.
- `DraftRequest.Candidate *Plan` serializes the verified candidate separately from the 4 KiB `Context`.

- [ ] **Step 1: Write failing config and tool tests**

Add config default/validation cases for 10, 1, and negative maxRounds. Add `TestRevisionMakesOneOrNoModelCall`: below cap exactly one model call, cap exactly zero, model failure on a revision no degraded composer. Add `TestPlanCheckRevision`: returns the revised plan/new hash; without candidate a marker-bearing plan returns `unresolved_marker`; malformed envelopes return `invalid_arguments` or `plan_changed` as appropriate.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/config ./internal/app ./internal/humanapi -run 'TestIntakePlanConfig|TestRevisionMakesOneOrNoModelCall|TestPlanCheckRevision' -count=1`
Expected: FAIL because revision fields/wiring are absent.

- [ ] **Step 3: Implement configuration and broker flow**

Default and validate `maxRounds >= 1`, pass it through `app` into `Intake`. For `intake_plan`, verify/revise the envelope before GitHub/model work, keep the daily-cap order, skip the model at the cap, otherwise pass the candidate to the model and Compose with the revised plan’s `Rev`, `Round`, and `RoundsLeft`, then run `CheckCarryForward`. Never use the degraded composer for a failed revision. For `plan_check` with candidate, call `Revise`, then the normal guard/redactor and `CheckFinal`; without candidate, preserve the existing call shape plus final-marker validation. Update both JSON schemas and tool descriptions with the exact envelope.

- [ ] **Step 4: Run affected tests**

Run: `go test -race -count=1 ./internal/config ./internal/app ./internal/humanapi ./internal/intake`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config internal/app internal/humanapi internal/intake
git commit -m "feat(humanapi): serve deterministic intake revisions"
```

### Task 5: Final filing doors, compatibility views, and docs

**Files:**
- Modify: `internal/humanapi/issuedraft.go`, `internal/humanapi/runcreate.go`, associated tests
- Modify: `internal/factory/api/tasks.go`, `internal/hardening/outputs.go`, `internal/factory/intake/publish.go`, associated tests
- Modify: `internal/roomview/plan.go`, `internal/slackapp/broker.go`, `internal/slackapp/proposal_test.go`
- Modify: `internal/maku/skill/factory-handoff/references/issue-template.md`
- Modify: `docs/api.md`, `docs/cli.md`, `docs/architecture.md`

**Interfaces:**
- Consumes: `CheckFinalDraft`, `CheckFinal`, `WithGaps`, and the broker’s first-gap `Question` mapping.
- No new Slack re-plan protocol: existing thread replies continue to call initial-shape `intake_plan`.

- [ ] **Step 1: Write failing door and compatibility tests**

Add `TestFinalDraftDoors`: a marker is refused by `issue_draft`, `plan_check`, `run_create`, factory task creation, hardening output, and issue publishing, while intermediate Compose still accepts it. Add `TestLegacyPlanCompatibility`: an old Slack plan keeps its hash/rendering; a new ranked plan displays only the first gap as `Question`. Add the Decisions template fixture.

- [ ] **Step 2: Run the tests to verify failure**

Run: `go test ./internal/humanapi ./internal/factory/api ./internal/hardening ./internal/factory/intake ./internal/roomview ./internal/slackapp -run 'TestFinalDraftDoors|TestLegacyPlanCompatibility' -count=1`
Expected: FAIL on at least one unguarded door or missing compatibility fixture.

- [ ] **Step 3: Apply final validation and update docs**

Use `CheckFinalDraft` at external draft boundaries and `CheckFinal` in `run_create`; keep Compose and intermediate `Check` unchanged. Make no Slack protocol changes beyond the compatibility mapping and tests. Document the revision envelope, Decision grammar, maxRounds/dailyPlans relationship, and mixed-version fail-closed behavior. Update the issue template with `D1 [g1]` / `A2 [g2]`.

- [ ] **Step 4: Run the complete repository gate**

Run: `task check`
Expected: exit 0, including CRD/chart/docs/UI gates.

- [ ] **Step 5: Commit**

```bash
git add internal docs
git commit -m "feat(intake): close final draft doors to open gaps"
```
