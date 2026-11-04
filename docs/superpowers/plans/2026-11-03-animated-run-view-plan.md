# Animated run view Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A sticky right rail on the room page that draws the task as one vertical path of runs,
judge marks, milestones and planned ghosts, animated live and replayable, with the conversation as
its timeline.

**Architecture:** The factory adds `persona`, `plan`, `split` and `parent` to the task facts it
already posts. `internal/summary` folds the log into a `flow` section of summary/v1. The web page
renders that section as an SVG and DOM rail, filters the conversation per run through a new
events read, and animates the changes between refetches.

**Tech Stack:** Go (stdlib `testing`, controller-runtime fake client), TypeScript with DOM and
SVG (esbuild, vitest on jsdom), CSS in `web/src/app.css`.

**Spec:** `docs/superpowers/specs/2026-11-03-animated-run-view-design.md`

## Global Constraints

- `persona` and every `plan` entry match `^[a-z][a-z0-9-]{0,31}$`; `plan` holds at most 8
  entries (`envelope.MaxPlan`).
- `split.of` is `envelope.MinChildren` (2) to `envelope.MaxChildren` (5); a part's `index` is 1 to
  `of`; `outcome` is `landed`, `dropped`, `reverted` or empty; `room` is a C2 id (`envelope.ValidID`).
- The rail is 300 px wide at 1100 px and up; below that it is a row of dots and a drawer.
- Replay: 1200 ms per node, 400 ms per milestone, 20000 ms at most in all (beats shrink in
  proportion past it), with Skip. Under `prefers-reduced-motion: reduce`, Replay steps node by node.
- Colour by role: `--flow-judge` (violet) for `triager` and `reviewer` and the judge mark,
  `--flow-writer` (cyan) for `implementer` and `tester`, `--flow-plan` (amber) for ghosts,
  `--muted-foreground` for people and GitHub.
- A run filter loads at most 2000 events (`maxRunEvents`), in pages of 500.
- The CSP stays `style-src 'self'`: no `style` attribute, no inline `<style>`; set custom properties
  through CSSOM (`el.style.setProperty`) only, as `strip.ts:179` does.
- No new npm or Go dependency. Go tests use stdlib `testing` only, table-driven, with `-race`.
- Every new `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- A commit that touches `web/src` also commits the output of `task ui:build` in
  `internal/humanapi/ui/dist/`.
- `task check` exits 0 before every commit.
- Commits use conventional messages (`feat(summary): …`) and carry **no** `Co-Authored-By`
  trailer: the owner's CLAUDE.md forbids it.
- Three PRs, each on its own branch off `main` after the previous one merges: PR 1 is Tasks 1–4,
  PR 2 is Tasks 5–9, PR 3 is Tasks 10–12.

## Review Focus

1. **A task past 20 runs.** `TaskFacts.Runs` keeps the last 20; the fold keeps the same last 20
   and draws no node for a run that has rotated out of the facts (Task 3).
2. **A run whose `left` never lands** (a lost run, a broker restart). The node ends when its fact
   has `endedAt`, and `toSeq` is the last seq seen for that run, never 0 (Task 4).
3. **A summary refetch that drops the run being filtered**, because it rotated out. The filter
   returns to All runs and clears `#run=` (Task 9).
4. **A run longer than 2000 events.** The run view stops at 2000 and says `Showing the first 2,000
   events of this run` (Task 9).
5. **A persona the page has never seen** (`security-reviewer`). The node takes its role's colour
   and glyph, its label reads `security reviewer`, and it's rendered through `textContent` (Tasks 6
   and 7).

---

## PR 1: the facts and the fold (no UI change)

### Task 1: persona, plan, split and parent in the task facts

**Files:**
- Modify: `internal/envelope/payloads.go` (`TaskFacts` :134, `RunFact` :182, `RunFact.validate`
  :254, `TaskFacts.Validate` :270)
- Create: `internal/envelope/persona.go`
- Test: `internal/envelope/payloads_test.go` (`TestTaskFactsValidate` :28), `internal/envelope/persona_test.go`
- Modify: `docs/event-envelope.md` (the `state_changed{kind:task}` section)

**Interfaces:**
- Produces:
  ```go
  const MaxPlan = 8
  func ValidPersona(s string) bool // ^[a-z][a-z0-9-]{0,31}$
  // RunFact gains:  Persona string `json:"persona,omitempty"`
  // TaskFacts gains:
  //   Plan   []string    `json:"plan,omitempty"`
  //   Split  *SplitFact  `json:"split,omitempty"`
  //   Parent *ParentFact `json:"parent,omitempty"`
  type SplitFact struct {
  	Of    int        `json:"of"`
  	Parts []PartFact `json:"parts,omitempty"`
  }
  type PartFact struct {
  	Index   int    `json:"index"`
  	Issue   int    `json:"issue"`
  	Room    string `json:"room"`
  	Outcome string `json:"outcome,omitempty"`
  }
  type ParentFact struct {
  	Room  string `json:"room"`
  	Issue int    `json:"issue"`
  	Index int    `json:"index"`
  	Of    int    `json:"of"`
  }
  ```

- [ ] **Step 1: Write the failing tests.** In `persona_test.go`, `TestValidPersona`: a table where `scoper`,
  `security-reviewer`, `a` and `a` + 31 × `b` are valid, and `""`, `Scoper`, `1st`, `-x`,
  `a` + 32 × `b`, `scoper‮` and `rev iewer` are not. In `TestTaskFactsValidate`, add these cases
  (want substring after the colon):
  - `"persona not a persona"`: a run with `Persona: "Scoper"` → `"persona"`
  - `"plan of 9"`: 9 entries → `"plan is at most 8"`
  - `"plan entry not a persona"`: `Plan: []string{"x y"}` → `"plan"`
  - `"split of 1"`: `Split: &SplitFact{Of: 1}` → `"split"`
  - `"split of 6"` → `"split"`
  - `"part past of"`: `Of: 2` with a part of `Index: 3` → `"part"`
  - `"part room not an id"`: `Room: "t-52"` → `"part"`
  - `"part outcome unknown"`: `Outcome: "merged"` → `"part"`
  - `"more parts than of"`: `Of: 2` with 3 parts → `"part"`
  - `"parent index past of"`: `Parent: &ParentFact{Room: "k2mqx7ba", Issue: 1, Index: 3, Of: 2}` → `"parent"`

  As a positive case, a fact carrying `Persona: "security-reviewer"`, a `Plan` of 8, a split of 3
  with 2 valid parts, and a valid parent passes.

- [ ] **Step 2: Run the tests to check they fail.** Run `go test -race ./internal/envelope -run 'TestValidPersona|TestTaskFactsValidate'`.
  Expected: they fail to compile, since `ValidPersona` and `SplitFact` are undefined.

- [ ] **Step 3: Implement the fields and the checks.** Add `ValidPersona` to `persona.go`. Add the fields and types to
  `payloads.go`. Extend `RunFact.validate` and the `switch` in `TaskFacts.Validate` with one `case`
  per rule, in the existing `return fmt.Errorf("task facts: …")` style. Comment each new field with
  its contract, for example `// Persona is what the run is for, set by the factory alone; Role
  stays what it may do.`

- [ ] **Step 4: Run the tests to check they pass.** Run `go test -race ./internal/envelope`. Expected: PASS.

- [ ] **Step 5: Document the fields.** In `docs/event-envelope.md`, add `persona` to the RunFact
  fields, and `plan`, `split` and `parent` to the task facts, one table row each, with the bounds
  above.

- [ ] **Step 6: Commit.** Run `task check`, then:
  `git commit -m "feat(envelope): persona, plan, split and parent in the task facts"`

### Task 2: the factory writes them

**Files:**
- Modify: `internal/factory/reconciler/facts.go` (`factsOf` :25, the `fact` closure :31-51,
  `factsFor` :194)
- Modify: `internal/factory/reconciler/tester.go` (`testerDue` :26)
- Test: `internal/factory/reconciler/facts_test.go`

**Interfaces:**
- Consumes: Task 1's `RunFact.Persona`, `TaskFacts.Plan`, `SplitFact`, `PartFact`, `ParentFact`.
- Produces:
  ```go
  func personaOf(t *v1alpha1.Task, r v1alpha1.RunRecord) string
  func (r *Reconciler) planOf(t *v1alpha1.Task) []string
  func (r *Reconciler) testFirst(t *v1alpha1.Task) bool
  func splitFact(s *v1alpha1.SplitStatus) *envelope.SplitFact
  func parentFact(p *v1alpha1.Parent) *envelope.ParentFact
  ```

- [ ] **Step 1: Write the failing tests.**
  - `TestPersonaOf`: a table over `(template, role)`:
    - `pair`, `triager` → `scoper`
    - `investigate`, `triager` → `triager`
    - `pair`, `implementer` → `implementer`
    - `pair`, `reviewer` → `reviewer`
    - `pair`, `tester` → `tester`
  - `TestFactsOfCarriesPersonas`: a `pair` task whose `Status.Runs` are triager, implementer and
    reviewer gives `factsOf(task).Runs[i].Persona` = `scoper`, `implementer`, `reviewer`, and
    `Run.Persona` = `reviewer`.
  - `TestPlanOf`: a table run through `g.r.planOf(task)`, the rig's `g.r.Cfg` set per case:
    - template `investigate` → `[triager]`
    - `pair`, no scope, no test-first → `[implementer reviewer]`
    - `pair` with `Spec.ScopeMode = string(config.ScopeScoper)` → `[scoper implementer reviewer]`
    - `pair` with scope, and a test-first class on the repository → `[scoper tester implementer reviewer]`
    - `solo` → `[implementer]`
  - `TestFactsOfCarriesTheSplit`: `Status.Split{State: "approved", Of: 3, Children: [{Index: 1,
    Issue: 52, Task: "k2mqx7ba", Outcome: "landed"}, {Index: 2, Issue: 53, Task: "p4zt6wce"}]}`
    gives `Split{Of: 3, Parts: [{1, 52, "k2mqx7ba", "landed"}, {2, 53, "p4zt6wce", ""}]}`. A split
    in state `proposed`, `declined` or `ignored` gives `Split == nil`.
  - `TestFactsOfCarriesTheParent`: `Spec.Parent{Task: "m3pq6xzb", Issue: 50, Index: 2, Of: 3}`
    gives `Parent{Room: "m3pq6xzb", Issue: 50, Index: 2, Of: 3}`.
  - Every fact these tests build passes `envelope.TaskFacts.Validate()`.

- [ ] **Step 2: Run the tests to check they fail.** Run `go test -race ./internal/factory/reconciler -run 'TestPersonaOf|TestFactsOfCarries|TestPlanOf'`.
  Expected: they fail to compile.

- [ ] **Step 3: Implement the five functions.**
  - `personaOf` has no config. It returns `"scoper"` for a `triager` run when
    `t.Spec.Template != "investigate"` (the name is the Task CRD's template enum), and otherwise
    `r.Role`.
  - Set `rf.Persona` in the `fact` closure.
  - Move the class-level half of `testerDue` into `testFirst`: the repository's
    `TestFirstFor(t.Spec.PredictedClass)` and the template holding `implementer`. `testerDue` keeps
    its behaviour by calling it.
  - `planOf` returns `[triager]` when `r.Cfg.Investigates(t.Spec.Template)`. Otherwise it builds
    `scoper` (if `ScopeMode` is scoper), then `tester` (if `testFirst`), then the template's roles
    in order without duplicates, capped at `envelope.MaxPlan`.
  - `factsFor` sets `f.Plan`. `factsOf` sets `f.Split` and `f.Parent`.
  - Use the existing split-state constant if `split.go` declares one, else the literal `"approved"`.

- [ ] **Step 4: Run the package tests.** Run `go test -race ./internal/factory/reconciler`. Expected: PASS. Existing tests that compare
  whole `RunFact` values now see `Persona`; add the persona to their expected values, and change
  nothing else.

- [ ] **Step 5: Commit.** Run `task check`, then:
  `git commit -m "feat(factory): the task facts name each run's persona, the plan and the split"`

### Task 3: the flow fold

**Files:**
- Create: `internal/summary/flow.go`
- Modify: `internal/summary/summary.go` (`State` :59, `foldState` :264: one call at its end)
- Test: `internal/summary/flow_fold_test.go` (package `summary`, so it reads `st.flow` directly)

**Interfaces:**
- Consumes: `envelope.Event.RunID`, `ParticipantPayload`, `HandoffPayload.Scope` and `.Split`,
  `tool_result{status: ok|error|rejected}`, `message{kind: judge_verdict, verdict}`,
  `run_requested{role, runId}`, `state_changed{kind: run_phase, phase, model}`,
  `state_changed{kind: task}`.
- Produces (unexported, inside `summary`):
  ```go
  // State gains:  flow flowState
  type flowState struct {
  	runs   []flowRun   // first-seen order, the last envelope.MaxRuns
  	phases []Milestone // each phase once, at its first appearance
  }
  type flowRun struct {
  	id, role, model, startedBy string
  	reason                     string // run_phase's terminal reason, as the log holds it
  	handoff                    string // its handoff summary's first line, at most 120 runes
  	from, to, last             int64 // seq of joined, left, and the last event of the run
  	at                         time.Time // ts of its first event
  	tools                      ToolCount
  	judge                      JudgeMark
  	scoped, split              bool // its handoff carried Scope, Split
  }
  func foldFlow(st *State, ev envelope.Event)
  ```
  Exported, used by Task 4: `type ToolCount struct{ OK, Failed int }` (`json:"ok"`, `json:"failed"`),
  `type JudgeMark struct{ Count int; Last string }` (`json:"count"`, `json:"last"`),
  `type Milestone struct{ Phase string; At time.Time; Seq int64 }` (`json:"phase"`, `json:"at"`, `json:"seq"`).

- [ ] **Step 1: Write the failing tests.** The cases, in `TestFoldFlow`, fold events with `Fold` and
  read `st.flow`:
  - `"a run's seq range"`: joined at 5, run events at 6–9, left at 10 → from 5, to 10, last 10
  - `"a live run"`: joined at 5, events to 9 → to 0, last 9
  - `"model"`: `run_phase{Running, model: glm-5.3}` → model `glm-5.3`
  - `"tool results"`: statuses ok, ok, error, rejected → `{OK: 2, Failed: 2}`
  - `"judge verdicts"`: not_yet, not_yet, met → `{Count: 3, Last: "met"}`
  - `"scoper's handoff"`: a triager's handoff with `Scope` → scoped true
  - `"handoff line"`: summary `"First line\nsecond"` → handoff `First line`; a 200-rune line is cut
    to 120 runes, whole runes
  - `"end reason"`: `run_phase{Failed, reason: stalled}` → reason `stalled`
  - `"started by a person"`: `run_requested` from human `github:ana` → startedBy `github:ana`
  - `"milestones once"`: task phases Implementing, AwaitingCI, AwaitingHuman, AwaitingCI, Merged →
    `[AwaitingCI, AwaitingHuman, Merged]`, each at its first seq
  - `"21 runs"`: 21 runs joined → 20 kept, the first one dropped
  - `"an event of no run"`: a human chat line changes no run

- [ ] **Step 2: Run the tests to check they fail.** Run `go test -race ./internal/summary -run TestFoldFlow`. Expected: FAIL (undefined).

- [ ] **Step 3: Implement `foldFlow`.** `foldState` calls it last, so `st.Facts` already holds the new
  task facts. Milestone phases are `AwaitingCI`, `AutoMerging`, `AwaitingHuman`, `Merged`,
  `Verifying`, `Done`, `Reverted`, `Escalated`, `Closed`, `Stopped`, `NoOp` and `Rejected`, as
  strings, since `internal/summary` doesn't import the factory API. A run is found by `ev.RunID`;
  a `participant{joined}` from an agent principal starts one.

- [ ] **Step 4: Run the tests to check they pass.** Run `go test -race ./internal/summary`. Expected: PASS, the goldens unchanged (nothing
  reads `flow` yet).

- [ ] **Step 5: Commit.** Run `task check`, then:
  `git commit -m "feat(summary): fold each run's range, model, tools, judge and milestones"`

### Task 4: the flow view in summary/v1

**Files:**
- Modify: `internal/summary/flow.go`, `internal/summary/summary.go` (`Summary` :149, `View` :376)
- Test: `internal/summary/flow_test.go`, `internal/summary/testdata/*.json` (rewritten)
- Modify: `docs/api.md` (the summary fields table under `GET /api/rooms/{id}/summary`)

**Interfaces:**
- Consumes: Task 3's `flowState`; Task 1's facts.
- Produces:
  ```go
  // Summary gains:  Flow *Flow `json:"flow,omitempty"`
  type Flow struct {
  	Plan       []string             `json:"plan,omitempty"`
  	Nodes      []FlowNode           `json:"nodes"`
  	Ghosts     []string             `json:"ghosts,omitempty"`
  	Milestones []Milestone          `json:"milestones,omitempty"`
  	Split      *envelope.SplitFact  `json:"split,omitempty"`
  	Parent     *envelope.ParentFact `json:"parent,omitempty"`
  }
  type FlowNode struct {
  	Run       string     `json:"run"`
  	Persona   string     `json:"persona"`
  	Role      string     `json:"role"`
  	Round     int        `json:"round"`
  	StartedAt time.Time  `json:"startedAt,omitzero"`
  	EndedAt   time.Time  `json:"endedAt,omitzero"`
  	FromSeq   int64      `json:"fromSeq"`
  	ToSeq     int64      `json:"toSeq"`
  	Outcome   string     `json:"outcome,omitempty"`
  	Verdict   string     `json:"verdict,omitempty"`
  	MicroUSD  int64      `json:"microUSD,omitempty"`
  	Model     string     `json:"model,omitempty"`
  	Judge     *JudgeMark `json:"judge,omitempty"`
  	Tools     ToolCount  `json:"tools"`
  	Edge      string     `json:"edge,omitempty"`
  	StartedBy string     `json:"startedBy,omitempty"`
  	Reason    string     `json:"reason,omitempty"`
  	// Handoff is the agent's own words, redacted at append: the page labels it as their claim.
  	Handoff string `json:"handoff,omitempty"`
  }
  func flowView(st State) *Flow
  ```

- [ ] **Step 1: Write the failing tests.** In `TestFlowView`, a table where each case folds events with
  `summary.Fold`, then reads `summary.View(…).Flow`:
  - `"no task, one run"` → `Flow == nil`
  - `"no task, two runs"` → 2 nodes, persona = role, no plan, no ghosts
  - `"a task with no run yet"` → 0 nodes, `Ghosts == Plan`
  - `"persona from the facts"`: a fact with `Persona: "security-reviewer"` → that persona
  - `"an old scoper"`: no persona, role `triager`, its handoff with `Scope` → persona `scoper`
  - `"ghosts"`: plan `[scoper tester implementer reviewer]`, runs scoper and tester → ghosts
    `[implementer reviewer]`
  - `"a skipped step"`: plan `[scoper tester implementer reviewer]`, runs scoper and implementer →
    ghosts `[reviewer]`
  - `"no ghosts once terminal"`: phase `Done` → `Ghosts` empty
  - `"rounds"`: implementer, reviewer (verdict `changes`), implementer, reviewer → rounds 1, 1, 2, 2
  - `"edges"`: scoper (scoped), tester, implementer, reviewer with `PR{Number: 41}`, then implementer
    with trigger `ci` → edges `""`, `plan`, `failing test`, `PR #41`, `CI red`
  - `"edges by trigger"`: an implementer with trigger `human` → `changes requested`; after a
    reviewer's `changes` → `changes`; trigger `retry` → `retry`; `resume` → `resumed`
  - `"a lost run"`: the fact has `endedAt`, the log has no `left` → `ToSeq` = the run's last seq
  - `"a run rotated out"`: 21 runs; the facts keep 20 → 20 nodes, all from the facts
  - `"split and parent"` are copied from the facts as they are
  - `"cost and outcome"` come from the fact; `Model` from the log, else the fact's first served model
  - `"reason and handoff"` come from the log's `flowRun`

- [ ] **Step 2: Run the tests to check they fail.** Run `go test -race ./internal/summary -run TestFlowView`. Expected: FAIL.

- [ ] **Step 3: Implement `flowView`.** `View` sets `s.Flow = flowView(st)`.
  - Nodes are the facts' runs joined to `flowState` runs by id, plus runs found only in the log,
    ordered by `StartedAt` (the fact's, else the run's first event), ties by `FromSeq`.
  - The rail is shown when `st.Facts != nil || len(nodes) >= 2`, else `nil`.
  - The edge into node k takes the first rule that matches:

    | Rule | Edge |
    |---|---|
    | the previous node was scoped | `plan` |
    | the previous node split | `split` |
    | trigger `ci`, `human`, `retry`, `resume` | `CI red`, `changes requested`, `retry`, `resumed` |
    | an implementer after a reviewer's `changes` | `changes` |
    | after a tester | `failing test` |
    | a reviewer, with a PR in the facts | `PR #<n>` |
    | otherwise | `""` |

  - Ghost matching keeps a cursor into the plan. Each node advances it past the first entry at or
    after the cursor that equals its persona. `Ghosts` is the plan from the cursor on, and empty
    once the facts' phase is terminal.
  - The scoper inference carries the comment
    `// Remove once no room from before persona remains within retention (animated run view, decision 3).`

- [ ] **Step 4: Rewrite the goldens, then run the tests.** Run `go test ./internal/summary -update-golden`, then
  `go test -race ./internal/summary`. Expected: PASS. Read the diff of `testdata/*.json`: only
  `flow` members are added, and `no_facts.json` has none.

- [ ] **Step 5: Document `flow`.** In `docs/api.md`, add a `flow` row to the summary/v1 fields table: what
  it holds, and when it's absent (the spec's decision 11).

- [ ] **Step 6: Commit.** Run `task check`, then:
  `git commit -m "feat(summary): summary/v1 carries the task's flow"`

**Open PR 1.**

---

## PR 2: the live rail

### Task 5: a page of the log for the run view

**Files:**
- Create: `internal/humanapi/events.go`
- Modify: `internal/humanapi/server.go` (routes :191-206)
- Test: `internal/humanapi/events_test.go`
- Modify: `docs/api.md` (the human API routes table, and a `GET /api/rooms/{id}/events` section)

**Interfaces:**
- Produces: `GET /api/rooms/{id}/events?after=N&limit=M` → `200 {"events": [envelope.Event…]}`.
  ```go
  func (s *Server) roomEvents(w http.ResponseWriter, r *http.Request)
  ```

- [ ] **Step 1: Write the failing tests.** In `TestEventsEndpoint`, built on `setup(t, …)` and `e.log.evs` as
  `TestSummaryEndpoint` is:
  - `"a page after N"`: 30 events, `after=10&limit=5` → seqs 11 to 15
  - `"the default limit"`: 700 events, no limit → 500 events
  - `"limit capped"`: `limit=501` → 400
  - `"after not a number"`: `after=x` → 400
  - `"limit zero"` → 400
  - `"unreadable room"`: what the summary gives an unreadable room (the same `roomStatus`)
  - `"log unreadable"`: `failingLog{e.log}` → the summary's status for `errLogUnreadable`
  - `"no principal"` → the summary's refusal

- [ ] **Step 2: Run the tests to check they fail.** Run `go test -race ./internal/humanapi -run TestEventsEndpoint`. Expected: FAIL (404 from the mux).

- [ ] **Step 3: Implement `roomEvents`.** It follows `roomSummary`: `principal`, then `readRoom`, then
  `context.WithTimeout(…, routeTimeout)`, then `s.Log.Range(ctx, id, after, limit)`, then
  `writeJSON`. `limit` defaults to and is capped at `pageSize`. Register
  `mux.HandleFunc("GET /api/rooms/{id}/events", s.roomEvents)` beside the summary route.

- [ ] **Step 4: Run the tests to check they pass.** Run `go test -race ./internal/humanapi`. Expected: PASS.

- [ ] **Step 5: Document the route.** In `docs/api.md`, add the route row and its section: query, bounds,
  statuses.

- [ ] **Step 6: Commit.** Run `task check`, then:
  `git commit -m "feat(humanapi): a page of a room's log for the run view"`

### Task 6: the flow model, glyphs and tokens in the web page

**Files:**
- Modify: `web/src/summary.ts` (`Summary` :15-47, `changesSummary` :64-72)
- Create: `web/src/flow.ts`, `web/src/glyphs.ts`
- Modify: `web/src/api.ts` (add `fetchEvents`)
- Modify: `web/src/app.css` (`:root` :23-82, `.dark` :84-130)
- Test: `web/test/flow.test.ts`, `web/test/glyphs.test.ts`, `web/test/summary.test.ts`, `web/test/contrast.test.ts`

**Interfaces:**
- Consumes: Task 4's `flow` JSON; Task 5's route.
- Produces:
  ```ts
  // summary.ts
  export interface FlowNode { run: string; persona: string; role: string; round: number;
    startedAt?: string; endedAt?: string; fromSeq: number; toSeq: number; outcome?: string;
    verdict?: string; microUSD?: number; model?: string; judge?: { count: number; last: string };
    tools: { ok: number; failed: number }; edge?: string; startedBy?: string; reason?: string; handoff?: string }
  export interface Milestone { phase: string; at: string; seq: number }
  export interface SplitPart { index: number; issue: number; room: string; outcome?: string }
  export interface Flow { plan?: string[]; nodes: FlowNode[]; ghosts?: string[]; milestones?: Milestone[];
    split?: { of: number; parts?: SplitPart[] }; parent?: { room: string; issue: number; index: number; of: number } }
  // Summary gains:  flow?: Flow
  // flow.ts
  export type Tone = "judge" | "writer";
  export function tone(role: string): Tone;
  export function personaLabel(persona: string): string;
  export type ChipKind = "ok" | "changes" | "fail" | "stop" | "live" | "";
  export function chip(n: FlowNode): { text: string; kind: ChipKind };
  export function detail(n: FlowNode, now: number): string;
  export type Row = { node: FlowNode } | { round: number; nodes: FlowNode[] };
  export function rows(nodes: FlowNode[], max: number): Row[];
  export function ghostMerge(f: Flow, phase: string): boolean;
  export function ghostsAfter(plan: string[], nodes: FlowNode[]): string[]; // Task 4's cursor rule, for Replay's frames
  // glyphs.ts
  export type GlyphName = "scope" | "search" | "checklist" | "tools" | "eye" | "branch" | "person" | "github" | "shield";
  export function glyph(name: GlyphName, label?: string): SVGSVGElement;
  export function glyphFor(persona: string, role: string): GlyphName;
  // api.ts
  export async function fetchEvents(id: string, after: number, limit: number, get?: typeof fetch): Promise<RoomEvent[]>;
  ```

- [ ] **Step 1: Write the failing tests.**
  - `flow.test.ts`:
    - `tone`: triager and reviewer → `judge`; implementer, tester and `unknown` → `writer`.
    - `personaLabel("security-reviewer")` → `security reviewer`.
    - `chip`, a table:
      - verdict `approve` → `✓ approve`/`ok`
      - verdict `changes` → `✗ changes`/`changes`
      - outcome `succeeded` → `✓`/`ok`
      - judge last `impossible` and outcome `failed` → `impossible`/`fail`
      - outcome `failed` or `lost` → `failed`/`fail`
      - outcome `revoked` or `budget_exhausted` → `stopped`/`stop`
      - no `endedAt` → `running`/`live`
    - `detail` of a node 14 minutes long, 820000 µ$ on `glm-5.3` → `14 m · $0.82 · glm-5.3`; a
      missing part is left out with its dot.
    - `rows(nodes, 8)` with 10 nodes over 3 rounds folds round 1 into one `{round: 1, nodes}` and
      leaves rounds 2 and 3 open; with 8 nodes, nothing folds; the round holding a live node never
      folds.
    - `ghostMerge` is true while the phase isn't terminal and no milestone is `Merged`.
    - `ghostsAfter` gives the same answers as Task 4's `"ghosts"` and `"a skipped step"` cases.
  - `glyphs.test.ts`:
    - `glyphFor("scoper", "triager")` → `scope`
    - `glyphFor("security-reviewer", "reviewer")` → `eye` (the role fallback)
    - `glyphFor("x", "x")` → `tools`
    - every glyph has `viewBox` `-12 -12 24 24` and no `style` or `on*` attribute, as
      `icons.test.ts` checks
  - `summary.test.ts`: `changesSummary` is true for a `handoff`, a `participant`, a
    `state_changed{kind: run_phase}` and a `message{kind: judge_verdict}`.
  - `contrast.test.ts`: pin `--flow-judge`, `--flow-writer` and `--flow-plan` in both themes, each
    at least 3:1 against `--card`.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/flow.test.ts test/glyphs.test.ts test/summary.test.ts test/contrast.test.ts`.
  Expected: FAIL.

- [ ] **Step 3: Implement the model, glyphs and tokens.**
  - Types and `changesSummary` cases go in `summary.ts`; the pure helpers in `flow.ts`; `fetchEvents`
    next to `fetchSummary`'s `api(...)` call.
  - `glyphs.ts` holds each glyph's path `d` strings, copied from
    `ai-native-ref/motion/ai-platform/assets/icons/glyphs/<name>.svg` without their `<metadata>`.
    Convert a `circle`, `line` or `rect` to its equivalent path. Build with `createElementNS` and
    the same attributes as `icons.ts:22`, except `viewBox` `-12 -12 24 24` and `stroke-width` 2.
  - Persona table: scoper → scope, triager → search, tester → checklist, implementer → tools,
    reviewer → eye. Role table: triager → search, tester → checklist, implementer → tools, reviewer
    → eye.
  - Tokens: `--flow-writer` `#0e7490` light and `#22d3ee` dark; `--flow-judge` `#6d28d9` light and
    `#a78bfa` dark; `--flow-plan` `#b45309` light and `#f5a524` dark.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): the flow model, the clip's glyphs and the rail's colours"`

### Task 7: the rail

**Files:**
- Create: `web/src/rail.ts`
- Modify: `web/src/app.css` (rail rules)
- Test: `web/test/rail.test.ts`

**Interfaces:**
- Consumes: Task 6's types and helpers.
- Produces:
  ```ts
  export interface RailHandlers { select(run: string | undefined): void }
  export interface Rail {
    el: HTMLElement;                                   // aside.rail
    set(f: Flow | undefined, phase: string, now: number): void;
    tick(now: number): void;                           // the live node's age
    live(e: RoomEvent): void;                          // +1 tool ok/failed on the live node
    mark(run: string | undefined): void;               // the node on screen: [aria-current=true]
    chosen(run: string | undefined): void;             // the filtered node: [aria-pressed=true]
  }
  export function mountRail(h: RailHandlers): Rail;
  ```
  The DOM, which later tasks style and animate:
  - `aside.rail > nav[aria-label="Task path"] > ol.rail-path`
  - each node is `li.rail-node[data-run][data-tone][data-state=done|live|ghost] > button.rail-button`
    (glyph, `span.rail-persona`, `span.rail-chip[data-kind]`, `span.rail-detail`)
  - between nodes, `li.rail-edge > svg > path.rail-stroke` with `span.rail-edge-label`
  - a folded round is `li.rail-round > button`
  - milestones are `ol.rail-milestones > li.rail-milestone[data-phase]`
  - the hover card is `div.rail-card[role=tooltip]`, and the live announcer
    `p.rail-live[aria-live=polite]`

- [ ] **Step 1: Write the failing tests.** In `rail.test.ts`, on jsdom:
  - `"draws nodes, edges, ghosts and milestones in order"`: a flow of scoper (done), implementer
    (live), ghost reviewer and milestone AwaitingCI gives 3 `li.rail-node`, with an edge label
    `plan` between the first two, and a ghost merge milestone.
  - `"renders a persona as text"`: persona `security-reviewer` gives `.rail-persona.textContent`
    `security reviewer`, and the node's `data-tone` is `judge` for role `reviewer`.
  - `"the judge mark"`: `judge {count: 2}` shows `◇×2` on its node.
  - `"tick ages the live node"`: `tick(now + 60_000)` changes the live node's `.rail-detail`.
  - `"live counts a tool result"`: `live(tool_result{status: error})` on the live run's id raises
    its card's failed count by 1; an event of another run changes nothing.
  - `"announces a run that starts"`: a second `set` with a new live node writes
    `implementer started` to `.rail-live`.
  - `"select on click"`: clicking a node's button calls `h.select(run)`.
  - `"no style attribute"`: no element in the rail has a `style` attribute.
  - `"a started-by mark"`: `startedBy: "github:ana"` gives `started by ana` in the card.
  - `"the card"`: focus on a node's button opens the same `.rail-card` as hover. It shows the start
    and end, the tool counts, the reason, and the handoff line under the label `the agent's claim`
    (as Notes label theirs). Blur closes it.
  - `"absent flow hides the rail"`: `set(undefined, …)` sets `el.hidden`.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/rail.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement `mountRail` and its CSS.** `set` re-renders the path on each call, keyed by
  `data-run`, so Task 10 can tell new nodes from old. Colours come from `data-tone` and
  `data-state` rules in `app.css`, through the `--flow-*` tokens; past nodes get dimmer with
  `opacity`.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): the rail draws the task's path"`

### Task 8: the rail on the room page

**Files:**
- Modify: `web/src/room.ts` (`app.replaceChildren` :93, `loadSummary` :162-195, the 1 s ticker
  :158-161, `onEvent` :244-259)
- Modify: `web/src/app.css` (`.room-body` grid, the sticky rail, `.rail-dots`, the drawer, the
  1100 px breakpoint)
- Test: `web/test/room.test.ts`

**Interfaces:**
- Consumes: Task 7's `mountRail`.
- Produces: `div.room-body` holding `needsPanel, lead, skeleton, main, forkPanel` and `rail.el`,
  with class `has-rail` while a flow is shown; `div.rail-dots` (one dot per node and ghost)
  that opens the drawer (`rail.el.classList.add("open")`). The custom properties `--header-h` and
  `--dock-h` are set on `app` through CSSOM.

- [ ] **Step 1: Write the failing tests.** In `room.test.ts`, through `page()`:
  - `"a summary with a flow shows the rail"`: `.room-body.has-rail aside.rail` is present and not
    hidden.
  - `"a summary without a flow hides it"`: no `has-rail`.
  - `"the ticker ages the live node"`: the vitest fake timer moves 60 s and the live node's
    detail changes.
  - `"a tool result counts live"`: an `event(seq, "tool_result", {status: "ok"})` of the live run
    raises its count before any refetch.
  - `"a judge verdict refetches"`: an event `message{kind: judge_verdict}` schedules one summary
    fetch.
  - `"dots open the drawer"`: a click on `.rail-dots` adds `open` to the rail.
  - `"the conversation keeps its place"`: `main` still holds `.view-chat` and `details.raw-events`.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/room.test.ts`. Expected: FAIL.

- [ ] **Step 3: Wire the rail in.**
  - In `loadSummary`, call `rail.set(sum.flow, sum.status.phase, Date.now())` and toggle
    `has-rail`. The ticker calls `rail.tick`; `onEvent` calls `rail.live(e)`.
  - A `ResizeObserver` on the header and the dock sets `--header-h` and `--dock-h`. Guard it:
    jsdom has none.
  - The CSS: at 1100 px and up, `.room-body.has-rail` is `grid-template-columns: minmax(0, 1fr)
    300px`. The rail is `position: sticky; top: var(--header-h, 0px); max-height: calc(100vh -
    var(--header-h, 0px) - var(--dock-h, 0px)); overflow-y: auto; align-self: start`. Below
    1100 px, the rail hides unless `.open`, when it's a fixed drawer with a close button, and
    `.rail-dots` shows.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS. Then check by hand: `task ui:build`,
  `cd web && npm run preview`, and look at a factory room at 1440 px and at 800 px, in both
  themes.

- [ ] **Step 5: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): the room page holds the rail beside the conversation"`

### Task 9: the scroll index and the run view

**Files:**
- Create: `web/src/runview.ts`
- Modify: `web/src/room.ts`
- Test: `web/test/runview.test.ts`
- Modify: `docs/web-ui.md` (the room page table: a **Rail** row after Notes)

**Interfaces:**
- Consumes: Task 5's `fetchEvents`; `mountViews` (`roomview.ts:20`); Task 7's `Rail.mark` and
  `Rail.chosen`.
- Produces:
  ```ts
  export const maxRunEvents = 2000;
  export function nodeAt(nodes: FlowNode[], seq: number): string | undefined;
  export interface RunView { set(nodes: FlowNode[]): void; select(run: string | undefined): Promise<void>; apply(e: RoomEvent): void }
  export function mountRunView(roomMain: HTMLElement, o: {
    load: (after: number, limit: number) => Promise<RoomEvent[]>;
    views: (el: HTMLElement) => Views;             // mountViews with the page's fork and you
    chosen: (run: string | undefined) => void;
  }): RunView;
  export function mountScrollIndex(chat: HTMLElement, o: {
    nodes: () => FlowNode[]; mark: (run: string | undefined) => void;
    observe?: (cb: IntersectionObserverCallback) => { observe(el: Element): void; disconnect(): void };
  }): { refresh(): void };
  ```

- [ ] **Step 1: Write the failing tests.** In `runview.test.ts`:
  - `nodeAt`:
    - nodes `[5, 10]` and `[11, live]`: seq 7 → the first node, seq 40 → the second
    - seq 3, before any node → `undefined`
    - a seq between two nodes' ranges → the node that started before it
  - `"select loads the run in a view of its own"`: `select(run)` with `fromSeq` 100 and `toSeq` 130
    calls `load(99, 500)` and renders seqs 100 to 130 in `div.run-view`. It hides the room's
    `main` children, sets `location.hash` to `#run=<id>`, and calls `chosen(run)`.
  - `"All runs returns"`: `select(undefined)` removes `div.run-view`, shows the room's view,
    clears the hash and calls `chosen(undefined)`.
  - `"stops at 2000"`: a run of 2600 events loads 4 pages, renders 2000 and shows
    `Showing the first 2,000 events of this run`.
  - `"live events of the chosen run"`: `apply(e)` for the chosen live run adds it to the run view;
    another run's event doesn't.
  - `"a refetch drops the chosen run"`: `set(nodes)` without the chosen run returns to All runs.
  - `"the hash restores"`: with `#run=<id>` at mount and that run in the first `set`, it is
    selected.
  - `"the scroll index marks the topmost visible row's node"`: a fake `observe` reports a row with
    `data-seq=120` as intersecting, so `mark` is called with the node holding 120.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/runview.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement `mountRunView` and `mountScrollIndex`, and wire them into `room.ts`.**
  - `rail` `select` → `runView.select`; `loadSummary` → `runView.set(sum.flow?.nodes ?? [])`;
    `onEvent` → `runView.apply(e)` and `scrollIndex.refresh()`.
  - The run view pages `load` from `fromSeq - 1` until `toSeq`, a short page, or
    `maxRunEvents`. For a live run it loads up to its last seq, then takes live events.
  - `mountScrollIndex` uses `IntersectionObserver` by default; the tests pass a fake.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS.

- [ ] **Step 5: Document the rail.** In `docs/web-ui.md`, add a **Rail** row to the room page table: where it
  sits, what a node shows, the dots below 1100 px, the run view, `#run=`. Keep the page's style:
  one row, no per-row status tag (`hack/docs.sh`).

- [ ] **Step 6: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): the rail indexes the conversation and opens a run's own view"`

**Open PR 2.**

---

## PR 3: motion, Replay, split

### Task 10: live motion

**Files:**
- Modify: `web/src/rail.ts` (`set`)
- Modify: `web/src/app.css` (keyframes under `@media (prefers-reduced-motion: no-preference)`)
- Test: `web/test/rail.test.ts`

**Interfaces:**
- Produces, as classes on the rail's DOM:
  - `.enter` on a node new since the last `set`, and on a ghost that turned live
  - `.draw` on a `path.rail-stroke` whose edge is new; its `--len` custom property comes from
    `getTotalLength()`, set through CSSOM
  - `.tick` on a judge mark whose count rose
  - `.fill` on a milestone that's new

  Each class is removed on `animationend`. `[data-state=live]` breathes through CSS alone.

- [ ] **Step 1: Write the failing tests.**
  - `"marks what changed"`: `set(a)`, then `set(b)` with one more node, one more edge, a judge count
    up by 1 and one more milestone. Exactly one `.enter`, one `.draw`, one `.tick` and one `.fill`
    appear.
  - `"first paint animates nothing"`: the first `set` adds none of them.
  - `"classes leave on animationend"`: dispatching `animationend` removes the class.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/rail.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement the change marks.** `set` compares the run ids, edge keys
  (`<from>→<to>`), judge counts and milestone phases with the previous call. Keyframes: `enter` is
  a 150 ms fade and scale from 0.96; `draw` is 400 ms of `stroke-dashoffset` from `var(--len)` to
  0; `tick` is a 200 ms pulse; `fill` is a 300 ms fill; the live node breathes at 2.4 s per cycle.
  All of them sit inside `(prefers-reduced-motion: no-preference)`; the global reduce rule
  (`app.css:684`) already zeroes the rest.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): the rail moves as the task moves"`

### Task 11: Replay

**Files:**
- Create: `web/src/replay.ts`
- Modify: `web/src/rail.ts` (a Replay button, shown when the phase is terminal)
- Test: `web/test/replay.test.ts`

**Interfaces:**
- Consumes: Task 7's `Rail.set`; Task 10's change marks, which make each step animate.
- Produces:
  ```ts
  export const nodeBeat = 1200, milestoneBeat = 400, maxReplay = 20000;
  export function frames(f: Flow): Flow[];           // the flow after each node, then after each milestone
  export function beats(f: Flow): number[];          // one delay per frame, scaled to fit maxReplay
  export function replay(rail: Rail, f: Flow, phase: string, o: {
    reduced: boolean; schedule?: (fn: () => void, ms: number) => unknown; cancel?: (h: unknown) => void;
  }): { skip(): void; next(): void; done: Promise<void> };
  ```

- [ ] **Step 1: Write the failing tests.**
  - `frames` of a flow with 3 nodes and 2 milestones gives 6 frames. The first has no nodes and the
    whole plan as ghosts; the last equals the input.
  - `beats` of 3 nodes and 2 milestones gives `[1200, 1200, 1200, 400, 400]`. With 20 nodes and 4
    milestones it sums to at most 20000, each beat scaled by the same factor.
  - On a fake schedule, `replay` calls `rail.set` once per frame in order and resolves `done` after
    the last one.
  - `skip()` sets the last frame at once and resolves.
  - With `reduced: true`, nothing is scheduled; each `next()` sets the next frame.
  - The Replay button shows only once the phase is terminal, and is replaced by Skip while a replay
    runs.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/replay.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement `frames`, `beats` and `replay`, and add the button to the rail.** A frame k
  is the flow cut to its first k nodes. Its ghosts are the plan from the cut's cursor (the same
  rule as Task 4, ported in `flow.ts` as `ghostsAfter(plan, nodes)`), and its milestones are those
  whose seq is at most the k-th node's `fromSeq`. Then come the milestones one by one. The rail
  holds the button. A frame's ghosts come from Task 6's `ghostsAfter`. `matchMedia("(prefers-reduced-motion: reduce)")` decides `reduced`.

- [ ] **Step 4: Run the tests to check they pass.** Run `task ui:test`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): a finished task replays on its rail"`

### Task 12: split chips, the parent node, and the docs

**Files:**
- Modify: `web/src/rail.ts`, `web/src/flow.ts`, `web/src/app.css`
- Test: `web/test/rail.test.ts`, `web/test/flow.test.ts`
- Modify: `docs/web-ui.md` (the Rail row: motion, Replay, split)

**Interfaces:**
- Consumes: `Flow.split` and `Flow.parent` (Task 4).
- Produces:
  ```ts
  export type PartState = "done" | "dropped" | "reverted" | "running" | "queued";
  export function parts(split: NonNullable<Flow["split"]>): { index: number; issue?: number; room?: string; state: PartState }[];
  ```
  The DOM: `ol.rail-parts > li.rail-part[data-state] > a[href="/r/<room>"]` under the scoper's
  node, and `li.rail-parent > a[href="/r/<room>"]` at the top of a part's path.

- [ ] **Step 1: Write the failing tests.**
  - `parts` of `{of: 3, parts: [{1, landed}, {2}]}` → done, running, queued. Outcome `dropped` →
    `dropped`; `reverted` → `reverted`.
  - `"split chips link each part"`: 3 `li.rail-part`; the first two link `/r/<room>`; the queued one
    has no link and shows `#?` until its issue is known.
  - `"the parent node"`: `parent {room, issue: 50, index: 2, of: 3}` gives a first item reading
    `part 2 of 3 of #50`, linking `/r/<room>`.

- [ ] **Step 2: Run the tests to check they fail.** Run `cd web && npx vitest run test/rail.test.ts test/flow.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement `parts` and their rendering.** The parent's milestones render under the
  chips, so the path reads fork, then wait, then done.

- [ ] **Step 4: Run the tests, then check by hand.** Run `task ui:test`. Expected: PASS. Then by hand, in
  `npm run preview`: replay a finished room in both themes, with reduced motion on and off.

- [ ] **Step 5: Document motion, Replay and split.** In `docs/web-ui.md`, extend the Rail row with them,
  one sentence each.

- [ ] **Step 6: Commit.** Run `task ui:build`, then `task check`, then:
  `git commit -m "feat(web): a split fans out on the rail, a part links back to its parent"`

**Open PR 3.** Once it merges, a separate ai-native-ref PR moves **The animated run view** row in
`website/content/docs/status.md` out of v4.0's "Nothing here is built yet" table, as its docs style
(rule 16, images carry no state) requires.
