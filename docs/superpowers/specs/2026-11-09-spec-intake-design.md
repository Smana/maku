# Spec intake: design

**Status:** design, decided by the owner in a grilling session and refined in an architectural review
(2026-11-09). **Scope:** `internal/intake` (`plan/v1`), `internal/humanapi` (`intake_plan`, `plan_check`,
MCP prompts, `room_brief`), `internal/maku` (`need_answer`, the `factory-handoff` skill),
`internal/slackapp`, `internal/roomview`, `internal/factory/sanitize`, the factory scoper and reconciler,
`internal/factory/narrate`, `internal/factory/forge`, the `Task` status and its generated CRDs, the issue
template, and `human.intake.plan.maxRounds`. **Not here:** factory metrics, the self-improvement loop,
factory-as-code edits by agents, agent identities, or moving a run between the cloud and a local session
(see [Not in this spec](#not-in-this-spec)).

## Conclusion

A vague request about a complex change becomes a factory-ready issue through **a Q&A loop the
developer's own harness runs** (Claude Code, Cursor, Codex, any MCP client). The factory does not ask
the initial questions; it **decides when the spec is ready**. `intake_plan` returns a draft's open gaps,
ranked, each with a recommended answer. The harness asks the developer the top gap, applies the answer to
the draft as a numbered Decision, and calls `intake_plan` again until no gap remains or the developer
accepts the rest as assumptions.

Every answer creates a new plan revision. The broker verifies the previous plan and hash, admits only the
deterministic marker/Decision edits the harness was allowed to make, and returns the edited plan with a
**new hash**. `plan_check` is the deterministic final check, and `issue_file` stays the only door that
files from the developer's harness.

The same interface works in reverse. When the scoper bounces an issue, `need_answer` lets the developer
append an answer from the harness instead of editing the issue on GitHub. A maintainer still re-applies
`factory/ready`: answering a question does not grant permission to start factory work.

On GitHub, the **body** is the spec and only the developer changes it. **Labels** carry the machine state.
**One status comment**, which Maku edits in place, shows where the work is now.

```mermaid
sequenceDiagram
  actor Dev as Developer
  participant H as Harness (any MCP client)
  participant B as Broker (intake_plan / plan_check)
  participant GH as GitHub issue
  participant F as Factory (scoper, run)
  Dev->>H: vague request
  H->>B: intake_plan(text, context)
  B-->>H: plan + hash + ranked gaps
  loop until no gap, "accept the rest", or round cap
    H->>Dev: top gap + recommended answer (one question)
    Dev-->>H: answer or accept the rest
    H->>H: resolve markers; append D/A records in candidate
    alt another planner round
      H->>B: intake_plan(text, base plan + hash, candidate)
      B->>B: verify base and edits; hash candidate; re-plan
      B-->>H: new plan + hash + remaining gaps
    else deterministic final revision
      H->>B: plan_check(base plan + hash, candidate)
      B->>B: verify base and edits; refuse unresolved markers
      B-->>H: final plan + new hash + canStart
    end
  end
  H->>Dev: issue_file form (file + start when authorized)
  Dev-->>H: confirm
  H->>GH: issue with Decisions; optional factory/ready
  F->>GH: one issue-scoped status comment, edited in place
  F->>F: scoper: Decisions are settled
  alt scoper has a question
    F->>GH: factory/needs-author + event comment
    H->>Dev: need_answer form
    Dev-->>H: answer
    H->>GH: edit body only (new issue-local D<n>)
    Dev->>GH: a maintainer re-applies factory/ready
  end
```

## Why: what the comparison showed

Context for D1. A colleague's thesis: loops bolt more easily onto a good interactive harness than
interactive UX bolts onto a factory, and the open spot is "bring your own interactive harness" onto a
factory, *if the interface between the two is cracked*. The hardest part of that interface is the spec: a
complex change needs several rounds of questions with a human before it is worth handing to agents.

| Product | Documented planning and handoff workflow |
|---|---|
| Warp | Triage identifies scope and open questions; the foreman relays clarification Q&A. Spec produces `PRODUCT.md` and `TECH.md` in a draft PR, normally gated by human approval. [Factory MCP](https://docs.warp.dev/factories/factory-mcp/) lets external coding agents create tasks and exchange questions and answers with the foreman, but documents no dedicated ranked-gap/readiness protocol. |
| Devin | Ask Devin and Interactive Planner support planning and refinement. [Devin Local Plan mode](https://docs.devin.ai/desktop/devin-local/modes) asks clarifying questions, offers choices and writes a Markdown plan; [`/handoff`](https://docs.devin.ai/cli/handoff) can transfer the local session and its context to an asynchronous cloud Devin. |
| Factory.ai / Droid | [Spec Mode](https://docs.factory.com/autonomy-and-safety/specification-mode) performs read-only research, may ask clarifying questions and supports iterative plan refinement before approval. Approved plans can be saved as Markdown. |
| GitHub spec-kit | `specify` writes `spec.md` with `[NEEDS CLARIFICATION: …]` markers. [`clarify`](https://github.com/github/spec-kit/blob/main/templates/commands/clarify.md) runs a structured ambiguity scan and asks prioritized questions one at a time with recommendations, writing answers back into the local spec. Its core workflow does not file that spec into an external asynchronous factory. |
| Maku today | `intake_plan` asks **at most one** question. The scoper bounces an issue with `factory/needs-author`, and the author answers by editing the body. |

These products already cover important parts of the workflow: spec-kit runs structured clarification inside
the developer's agent, Devin can hand a locally planned session off to the cloud, and Warp connects external
harnesses to a factory with clarification and spec approval. The distinction here is narrower: Maku defines
a **factory-owned readiness protocol** — ranked gaps and recommended answers returned over MCP, iterative
Decisions persisted in the GitHub issue, a deterministic final `plan_check`, and one explicit filing path
into the asynchronous factory.

## Decisions

| # | Decision | Rejected |
|---|---|---|
| D1 | **Output: a comparison with Warp, then this design.** The comparison is the section above; the rest of the Warp gaps are the next grilling | A positioning note with no build; the unsupported claim that local Q&A plus asynchronous handoff is unprecedented |
| D2 | **The harness asks, the factory decides when it's ready.** The developer's own tool runs the Q&A, so any MCP client can run it. The factory defines readiness and stays the only judge of it | (a) the factory asks, through more scoper questions over issue comments or Slack: works without a harness, but it is slow and the experience is the weakest. (b) a spec agent in a room: the developer has to leave their harness |
| D3 | **`intake_plan` returns ranked gaps, and revisions use a verified base plan plus an edited candidate.** The broker validates the deterministic edits and returns a new hash. `plan_check` stays deterministic and free, and is the final check | (a) a new `intake_revise` tool: cleaner separation, but another public tool and round-trip. (b) a mandatory final model call: extra cost and no deterministic guarantee that accepted text survives |
| D4 | **An optional `## Decisions` section in the issue body**, with planner records `D1 [g1]: choice, because reason` and `A2 [g2]: assumed answer, because why`. IDs are allocated in one interleaved sequence across the plan. Split issues repeat only the records they need; per-body IDs are ascending and unique, with numeric gaps allowed. A post-filing scoper answer uses the next issue-local `D<n>:` without a gap reference | (a) a spec file merged through its own PR first: a merge before any work starts. (b) a parent issue: the agent has to fetch it. (c) contiguous per-body numbering: refuses the split-issue subset rule |
| D5 | **Stop rule: "accept the rest" plus a round cap.** Accepting the rest is a deterministic `plan_check` revision, with every recommendation written as an `A<n>` record. At `human.intake.plan.maxRounds` (default 10), `intake_plan` converts the remaining recommendations to recorded assumptions and returns a final plan without another model call. `dailyPlans` remains the enforced cost bound | (a) only gaps marked critical block: the model sorts gaps into blocking and not, and the developer cannot see why. (b) no cap: the interaction has no product bound. (c) broker-side intake state: unnecessary and ambiguous across replicas |
| D6 | **The loop ships as the broker's `factory_intake` MCP prompt, beside `handoff`,** with the short loop contract also in `intake_plan`'s tool description and the server instructions. `handoff` remains as a compatibility alias for the same loop, and `factory-handoff` becomes a thin wrapper | (a) rewriting only the Claude Code skill: prompts-less clients get nothing. (b) elicitation forms inside `issue_file`: forms instead of a conversation. (c) removing `handoff`: breaks already-installed, version-pinned skills |
| D7 | **The scoper treats Decisions and assumptions as settled.** Its prompt gets one rule: do not reopen a numbered `D<n>` or `A<n>`; ask only about what the issue does not cover. An acceptance answer replaces its marker inside `## Acceptance check`, so the deterministic `askForAcceptance` path stays unchanged | (a) skipping the scoper on intake issues: no second look. (b) teaching the scoper to infer acceptance from Decisions: unnecessary when the operative text stays in the acceptance section |
| D8 | **`need_answer`, a local MCP tool built like `issue_file`, edits the body only.** It reads the structured scope question through `room_brief` (public GitHub comment as fallback), requires confirmation, and appends the next issue-local Decision. A maintainer re-applies `factory/ready`; the tool never labels | (a) labeling as the author: non-maintainer labels are refused and removed by intake. (b) an author-label exception: a separate trust-path change. (c) free-form mid-run questions: new run state and event kinds |
| D9 | **The other Warp gaps are out of this spec**, success metric included; they are the next grilling | A metric or a self-improvement loop added here |
| D10 | **Issue and Maku sync: the body is the spec, labels are the state, one live status comment.** Its ID is persisted and its issue-scoped marker is recoverable through all comment pages. Only the current task generation edits it, and only when the rendered content changes. Event comments stay as the history | (a) a status block in the body: conflicts with R5 and developer edits. (b) searching only recent comments: duplicates the status comment after enough events. (c) live checks and spend in v1: extra permissions and misleading mid-run figures |
| D11 | **The spec lives in Maku** (this file), proposed as a draft PR. ANR gets only the `human.intake.plan.maxRounds` setting | Writing it in ai-native-ref: the code is here |
| D12 | **Format: the three sections plus Decisions, with three conventions borrowed from elsewhere.** (1) A gap in a draft is spec-kit's marker `[NEEDS CLARIFICATION: …]`. (2) "What it should be" uses RFC 2119 keywords (MUST, SHOULD, MAY). (3) Decisions are one-line ADRs. No EARS or Gherkin requirement | (a) spec-kit's full `spec.md`: too long for a fix. (b) EARS for every requirement: awkward to write. (c) a GitHub issue form: possible later, not needed by the loop |

## The changes

### `plan/v1`: ranked gaps and display-only rounds (D3, D5, D12)

`Plan` gains `Gaps []Gap`, ranked so that the first gap blocks the most:

| Field | Type | Meaning |
|---|---|---|
| `id` | string | `g1`, `g2`…; unique within one plan and names its one marker. Cross-round stability is best-effort; no protocol step depends on it |
| `section` | string | `what-is-wrong`, `what-it-should-be`, `acceptance-check`, or `scope` |
| `text` | string | the question, untrusted text |
| `recommended` | `Answer` | the existing `{answer, why}` |

`Plan` also gains display-only `round int` and `roundsLeft int`. `round` is 1 for an initial plan and the
verified base plan's round plus one for a revision; a legacy plan with no round is treated as round 1. It
is separate from `Rev`, which remains the consumer's message/session revision. `roundsLeft` is
`maxRounds - round`, floored at zero.

`Question` and the top-level `question` answer field remain populated from the first gap for Slack,
`roomview`, and older clients. The `asked` metric outcome becomes `len(Gaps) > 0`; the outcome vocabulary
does not change. Slack keeps its existing thread re-plan loop, with one mapped question per revision; it
does not adopt the harness's revision envelope or per-intake round cap in this spec.

`Gaps`, recommendations, `Question`, `round`, and `roundsLeft` stay **outside `Material`**, as `Question` is
today. Each open gap has exactly one `[NEEDS CLARIFICATION: g<n> <text>]` marker in one hashed issue body,
so the substance that must be confirmed is already hash-bound. This keeps `plan/v1` hashes compatible and
does not invalidate in-flight Slack proposals. An acceptance marker lives in `## Acceptance check`; a scope
marker lives before the first required heading; the other markers live in their named sections.

A newer client sending revision fields or a plan with `gaps` to an older broker fails closed through strict
decoding. A newer broker keeps old plans' hashes and the `Question` mapping, so old Slack and room clients
continue to render and confirm them.

### Revision envelope: `intake_plan` and `plan_check` (D3, D5)

Both tools accept an optional revision envelope:

| Field | Meaning |
|---|---|
| `plan` | the base `plan/v1` object the broker previously answered with |
| `hash` | the base plan's hash |
| `candidate` | the harness-edited candidate plan |

`plan_check` without `candidate` keeps its current unmodified-plan call shape. `intake_plan` revisions also
carry the original `text` and the usual repository/context arguments; repository, budget, issue order,
classes, roles, and dependencies cannot change between base and candidate.

For a revision, the broker:

1. Recomputes the base hash and refuses `plan_changed` on drift.
2. Derives the round from the base and enforces `maxRounds`. Every `intake_plan` attempt still spends one
   `dailyPlans` slot; `plan_check` spends none.
3. Validates the candidate deterministically. The only permitted material changes are replacing one or more
   existing markers with non-empty text and appending their corresponding Decision/Assumption records. Each
   resolved marker gains exactly one new record naming its gap ID; all other material is unchanged.
4. Recomputes the candidate's display gaps from its remaining markers, runs the normal repository, class,
   draft and redaction checks, computes the **candidate's new hash**, and returns that plan and hash.
5. For `intake_plan` below the cap, makes the bounded model call for the remaining gaps. Settled replacement
   text and all existing D/A records must survive verbatim. For `plan_check`, makes no model call.

To **accept the rest**, the harness replaces every remaining marker with its recommendation and appends the
corresponding `A<n> [g<n>]` records, then calls `plan_check` with the revision envelope. On the cap-reaching
`intake_plan` revision, the broker performs that conversion itself after validating the developer's latest
answer and returns a final plan without another model call. In both cases the confirmation form shows every
assumption being accepted.

A final plan has no gaps or unresolved markers. `canStart` retains its existing write-based
`issue_file` authorization meaning and is true only when final validation and that check pass. This spec
does not change the pre-existing gap between that offer and the factory's maintainer-only label intake;
`need_answer` therefore never labels.

### Final-draft validation (D4, D12)

A final-draft validator, separate from the intermediate template check the composer uses, adds the closed
refusal reasons `unresolved_marker` and `invalid_decisions`. It is used by `plan_check`, `issue_file`,
`run_create`, plan-less `issue_draft`, and every other external filing door, so no path can file a live
clarification marker. Intermediate planner output may still contain markers.

The Decisions grammar is `^[DA]\d+( \[g\d+\])?: .+$`. During intake, every new record needs its gap
reference. Within one issue body, record IDs are ascending and unique but need not be contiguous, because a
split issue repeats only the records it depends on. Marker/gap/record correspondence is checked across the
whole plan. Post-filing scoper answers have no planner gap reference and use the next issue-local number.

### Issue template (D4, D12)

`references/issue-template.md` gains, after **Acceptance check**:

```markdown
## Decisions

D1 [g1]: <choice>, because <reason>
A2 [g2]: assumed <answer>, because <why>
```

The section is optional: a fix with no gaps has none. The redactor covers it like the other sections.

### `factory_intake` prompt and clients without prompts (D6)

The broker's `mcpPrompts` gains `factory_intake(request)`; `maku mcp` already relays `prompts/list` and
`prompts/get`. `handoff` remains and invokes the same loop rather than documenting a second filing flow.
The installed `factory-handoff` skill (still pinned to v0.32.0 and unaware of `intake_plan`) is replaced in
new installations by a wrapper that invokes the prompt; existing installations keep their version pin.

The prompt tells the harness to call `intake_plan`, show its text as it is, ask only the first gap with its
recommendation, apply the answer through the revision envelope, and offer "accept the rest" at every
question. When no gaps remain, it calls `plan_check`, shows the final draft and assumptions, then calls
`issue_file`. The short version of that contract also lives in `intake_plan`'s tool description and the
server instructions, so a client that does not surface MCP prompts still receives the rules through
`tools/list`.

### Scoper (D7)

The scoper prompt (`internal/factory/reconciler/scoper.go`) gets one rule: the issue's `D<n>` and `A<n>`
lines are settled; a `question` envelope MUST NOT reopen one. This is a prompt-enforced contract, not a new
text validator. Issues filed directly on GitHub are scoped as today. Because an acceptance answer replaces
the marker inside `## Acceptance check`, `askForAcceptance` is unchanged.

### `need_answer` (D8)

A local tool in `internal/maku`, beside `issuefile.go`, takes the repository and issue. It acts only while
`factory/needs-author` is present and `factory/ready` is absent. It resolves the issue's current room and
reads the scope question and first assumption through `room_brief`, whose structured result exposes them
for a scope in `question` state. For a public task, the GitHub bounce comment is the fallback. For an
internal task whose room the developer cannot read, the tool changes nothing and names the maintainer/room
reader path; it does not invent or parse a hidden question.

The elicitation shows the question, the scoper's recommendation, the exact proposed body append, and a
send toggle defaulting to false. A client that cannot show forms gets `needs_confirmation`, and nothing is
written. The tool fetches the body and labels before showing the form and re-reads them immediately before
writing; any change refuses the write rather than clobbering a concurrent edit or racing a maintainer's
label. It creates `## Decisions` after Acceptance check when the section is absent and appends the next
issue-local `D<n>:` with the developer's answer.

On confirmation, the only write is `gh issue edit --body-file=-`, as the developer. The result says that a
maintainer must re-apply `factory/ready`. Body-before-label remains the required order, but this tool does
not perform the label step or imply that the author may start work. The existing freeze-at-label rule (R5,
`edited_after_label`) and maintainer authorization rule are unchanged.

### Status comment (D10)

The reconciler, not the intake poller, creates the status comment on the first narration-bearing reconcile.
A refused label gets none. Existing event comments, including `Started`, remain additional history.

The comment has an issue-scoped marker and the factory's login as its owner. Comment creation returns its
ID, which is persisted in `Task.Status` beside the last successfully rendered body. `EditComment(ctx, id,
body)` patches that ID. After a crash between creation and the status write, or when a new task generation
adopts the issue, recovery paginates all issue comments to find the factory-owned marker; the forge's
recent-50 window is not the identity store. Only the generation that currently owns the issue may update
it, so a stale generation cannot overwrite a newer one's state and recovery cannot create a duplicate.

`narrate.Status(t)` renders only data the factory already has:

```text
Factory status · implementing · updated 2026-11-09 14:02 UTC
Room: <link> · waiting on: nobody
Spec: sha256:… · 4 decisions · 1 assumption
PR: #1234
```

The waiting vocabulary comes from the room summary. Live PR checks and spend are deferred: checks belong
to the merger connection, and an ended-run ledger total is not a truthful live spend figure. The timestamp
is the last material transition, not every reconcile.

Creation and edits are best-effort and never block factory work. An edit happens only when the rendered
body differs from the last successful one; failures leave that record untouched and retry on a later
reconcile. The real GitHub forge and its fake expose the new ID/edit contract through the reconciler's
consumer-owned `TaskForge` interface.

## Verification

The implementation plan must include table-driven tests for:

- legacy `plan/v1` hashes, first-gap compatibility, strict mixed-version failures, and Slack/room rendering;
- every allowed and refused base-to-candidate edit, including marker/gap/record correlation and split-issue
  Decision subsets;
- accept-the-rest and the round cap, proving the deterministic paths make no model call and preserve every
  accepted answer;
- final-marker refusal through plan and plan-less filing doors, plus redaction of Decisions;
- prompt and prompt-less client instructions, `need_answer` confirmation, authorization, concurrent body or
  label changes, missing rooms, and internal questions;
- acceptance remaining in its operative section and the scoper's settled-Decision prompt contract;
- status-comment ID persistence, crash recovery past 50 comments, generation ownership, no-op reconciles,
  failed creation/edit retries, and the real/fake `TaskForge` contracts.

The repository gate is `task check`.

## Not in this spec

Also excluded is aligning `issue_file`'s pre-existing write-only `canStart` start offer with the factory's
maintainer-only label rule. That is an existing authorization mismatch, not a permission this design
changes; the new `need_answer` path avoids it by editing the body only.

To grill next (the rest of the comparison with Warp):

- Factory metrics: human touches per PR, adoption (share of merged PRs that went through the factory), cost
  per PR, LLM-as-judge scores. This includes this design's own measure: the scoper bounce rate on issues
  that went through the Q&A compared with those filed directly.
- A self-improvement loop: score runs against rubrics, cluster the low scorers, and have an agent open PRs
  on skills and AGENTS.md.
- Factory-as-code edits by agents, with canary and rollback.
- Agent identities as service accounts.
- Moving a run between the cloud and a local session (`room_fork` and `room_checkout` do not attach to a
  live run).
