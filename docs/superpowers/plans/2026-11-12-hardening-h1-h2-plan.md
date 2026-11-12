# Untrusted-input hardening, stages H1 (audit) and H2 (non-code outputs enforced): implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** these two stages take the hardening track from contracts to its first live boundary.
- **H1, audit.** The factory shadows every GitHub write an agent run makes. It runs the threat check (T1 to T4, and T5 when a classifier is configured) on each one and records what it would have done as an `output_shadowed` event. Each run's input trust and would-be-withheld counts go into a new `status.inputTrust`. The run's brief gets a canary. Nothing is refused and no run changes behaviour.
- **H2, non-code outputs enforced.** For a repository that opts in, `pr`, `comment`, `labels` and `issue` become safe outputs. The run requests them through `output_*` tools, and the factory checks and applies them under a `publisher` identity. The sandbox role's `pull_requests: write` can then be dropped. `push` stays in the sandbox until H3.

Both stages ship switched off. `hardening.mode` defaults to `off`. `audit` takes a config change, and enforcement takes a per-repository `enforceOutputs` list. Readiness keeps answering `false` (R1 needs `mode: enforce`, which this release still refuses).

**Architecture:**
- **H1** adds three pieces to the factory: a **shadow pass** at the end of each writer run, a **system outputs endpoint** on the broker, and the **canary and input-trust recording** on the Task. The shadow pass reads the run's own writes back from GitHub (pushes, PR title and body, comments, label events, issues by the agents App inside the run's window). It turns each write into a `hardening.Request`, runs `hardening.Checker` with every layer in `audit`, and appends an `output_shadowed` record to the room.
- **H2** adds an **applier**, a factory runnable. It tails the rooms of tasks with a live writer run, rebuilds each run's caps from the log and checks every `output_requested`. Then it applies the request through a **Publisher** or refuses it, and answers with exactly one `output_applied` or `output_refused`. The H1 shadow pass becomes the **tracer**: an agents-App write of an enforced type with no matching `output_applied` is reported `untraced`.
- The broker serves the output tools per run, gated by an AgentRun annotation the factory writes at creation.

```mermaid
flowchart LR
  subgraph h1["H1: audit (observe, never refuse)"]
    W["run writes with its own token<br/>(gh, git, curl)"] --> GH[("GitHub")]
    GH -->|"read back at run end"| SH["shadow pass<br/>Checker, all layers audit"]
    SH -->|"output_shadowed"| LOG[("room log")]
    SH --> M["metrics + log line<br/>per would-refuse"]
  end
  subgraph h2["H2: non-code outputs enforced (opt-in per repository)"]
    A["agent"] -->|"output_pr / comment / labels / issue"| BR["broker MCP<br/>gated by run annotation"]
    BR -->|"output_requested"| LOG
    LOG --> AP["applier (factory)<br/>T1–T4 enforce, T5 audit"]
    AP -->|"agents App via octo-sts publisher"| GH
    AP -->|"issue: factory App"| GH
    AP -->|"output_applied / output_refused"| LOG
    GH -->|"tracer: untraced writes"| SH
  end
```

**Tech Stack:** Go (Maku). The H0 `internal/hardening` and `internal/mcp` contracts, `internal/envelope`, `internal/bridgeapi`, `internal/factory/{config,fence,forge,reconciler,rooms,runs,fmetrics}`, `api/factory/v1alpha1` (controller-gen), the Python harness (`images/maku-harness`, stdlib `unittest`), and the `maku-factory` Helm chart.

**Spec:** ai-native-ref `docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md`, stages H1 and H2 of its Staged rollout, plus its Safe outputs, Author trust, Threat check, Protected paths, Configuration and Acceptance sections · H0 plan: ai-native-ref `docs/superpowers/plans/2026-11-08-untrusted-input-hardening-h0-plan.md` (Smana/maku#400) · Interacts with `docs/superpowers/specs/2026-11-12-v4-production-path-design.md` D5, D10 and D12 (rulings HR12 and HR13).

**Read against:** Maku `origin/main` `50b39ecc`, cited `Maku/<path>`.

**Branches and pull requests, in order:**

| PR | Branch | Content | Size |
|---|---|---|---|
| 1 · H1 | `feat/hardening-h1-audit` | Tasks 1–10: fence block, audit config, `output_shadowed` and the system endpoint, forge reads, shadow pass, canary, `status.inputTrust`, T5 client, wiring | L |
| 2 · H2a | `feat/hardening-h2-applier` | Tasks 11–15: forge writes and the octo-sts publisher, the factory-written footer and markers, the applier, the `issue` output, scorer hardening. Built and tested, unreachable from config | L |
| 3 · H2b | `feat/hardening-h2-enforce` | Tasks 16–21: `enforceOutputs`, per-run serving, brief and harness changes, waiting for answers, the tracer, policy gauges, docs | M |

Each PR is its own v0.4x minor of Maku. PR 2 can merge before PR 3 because nothing in it is reachable until PR 3's config key exists.

## Global Constraints

- **Off by default, audit by choice, enforce per output type only.**
  - `hardening.mode` defaults to `off`.
  - `audit` is accepted centrally and per repository.
  - `mode: enforce` is still refused at startup in this release, which keeps R1 false, so `Readiness` answers `false` everywhere (assert it).
  - `enforceOutputs` arrives in PR 3 and never accepts `push`.
  - `layers.classifier: enforce` is refused (promotion is H5).
- **Audit never changes a run.** Under `audit` no request is refused, nothing is withheld, the tools are not served, and the only things a run sees differently are the canary line in its brief. A shadow-pass error is logged and retried, and never moves a task.
- **Existing suites unchanged.** No existing test is edited except where a task names the edit and the reason, for example a fake that gains a method. `task check` exits 0 on every PR.
- **Bounded vocabularies.** Refusals, `Missing`, shadow verdicts and metric labels are closed sets, never free text. No payload content goes into a log line or a metric.
- **Readers trust the stamp, not the payload.**
  - An `output_requested` counts only from the run's own agent actor, as the broker stamped it (`Actor.ID == "agent:"+runID`, `OriginClient`), the way `integrationResult` reads (`Maku/internal/factory/reconciler/integration.go`).
  - An `output_applied`, `output_refused` or `output_shadowed` counts only from `system:factory`.
  - `OutputRecord.Validate` is called on every read.
- Maku house rules:
  - every Go file starts with the SPDX line;
  - stdlib `testing` only, table-driven;
  - conventional commits in English, no attribution lines or trailers;
  - lint is golangci-lint v2's full set, so escape invisible characters in test literals (`"​"`, ST1018), and no error string ends in punctuation.
- This machine:
  - wrap every test run in `systemd-run --user --scope -q -p MemoryMax=6G`;
  - never override `HOME` or use `env -i`;
  - `internal/store` needs Docker.
- `task crd:gen && task crd:check` after any API type change. `task harness:test` after any harness change.

## Rulings made while planning

The spec leaves these open, or the code makes them concrete. Each takes the option the decisions lean toward.

| # | The spec says | The code says | This plan |
|---|---|---|---|
| HR1 | H1: "The applier shadows every write"; "the comparison between the two records is the false-refusal evidence". The H0 exit says "H1 serves the tools under `audit`" | Under audit the run's own token still writes. A served `output_pr` would either duplicate the run's `gh pr create` or be a request nobody applies. The bridge cannot see `gh api` or `curl` (`Maku/internal/bridge/classify.go:13-19`), so in-sandbox mirroring misses writes | **H1 shadows observed writes, not requests.** At each writer run's end the factory reads that run's writes back from GitHub, builds the `Request` the run would have sent, and checks it. The tools are first served in H2, and only where enforced. This measures false refusals on exactly the writes runs make today, whatever path made them. It overrides the H0 exit sentence |
| HR2 | Requests and answers are room-log events (UH4) | The broker's system API accepts only `task_state` messages and task facts (`Maku/internal/bridgeapi/system.go:128-175`). `OutputRecord.Validate` has no caller yet | A fourth kind, `output_shadowed`, and `OutputRecord.Audit []OutputFinding` (layer and reason pairs, allowed on `applied` and `shadowed`). A new system route, `POST /v1/rooms/{id}/outputs`, takes `{runId, kind, record}` for the three answer kinds. Replays are keyed on `OriginClient = <principal>:output:<kind>` and a `clientSeq` derived from the output id, so a retry can never write a second answer |
| HR3 | H1 advances at "at most 1% of legitimate outputs" refused by "the deterministic layers" | `Checker.Check` in audit returns every finding in order (`Maku/internal/hardening/threat.go`) | A shadowed output's **would-refuse** is its first T1–T4 finding. T5 findings are recorded and excluded from the 1%. `protected_path_needs_approval` is reported as `would_wait`, not as a refusal: under H3 such a push waits for a maintainer by design. It is counted separately as H3's approval-load estimate. T2's rewrites are reported (`TextReport`) and are never refusals. "Legitimate" means a non-drill task whose output a maintainer did not close as wrong |
| HR4 | The factory writes the PR footer (Safe outputs) | `pr_footer.py` appends `---\nAgent-…` after `gh pr create` (`Maku/images/maku-harness/pr_footer.py`) | The shadow pass strips the harness footer before T1, so a body is measured as the model wrote it. In H2 the factory writes the footer itself (a Go port of `neutralise` and `footer`) and adds `Agent-Output: <id>` |
| HR5 | `labelAllowlist`, the example being `[documentation, needs-human]` | The H0 default is empty (B12) | No change in Maku. In audit an empty allowlist makes every agent label a would-refuse. The ANR follow-up sets the allowlist **before** the 50-task count starts (runbook step H1-2) |
| HR6 | A repository extends the floor in `.github/agent-factory.yaml` under `hardening:` | `fence.Parse` decodes strictly. Today a `hardening:` key makes the repository **not owned** (`Maku/internal/factory/fence/fence.go:36-55`) | PR 1 teaches `fence.Parse` the key, which never decides ownership, and parses `protectedPaths` and `minAuthorTrust` (stricter only) in `hardening.ParseRepositoryBlock`. A block that does not parse reads as the floor plus `merged`. **Stop rule:** no repository adds the block before PR 1's release runs on its cluster |
| HR7 | T3: a per-run canary planted in the brief | H0 ships `NewCanary` with no caller | Planted only when the repository's mode is at least `audit`, as one line at the end of the brief. It is kept on the AgentRun as `agents.ogenki.io/canary` and read by the shadow pass and the applier. A run's outputs are checked against that run's canary only. An older run's canary quoted from the room is not an alert (stated as a residual) |
| HR8 | `status.inputTrust` holds `min`, `withheld` and `sources` (UH7); in H1 "recorded, not applied" | Nothing is withheld in audit, and the agent can read every comment with `gh` until H4 | `status.inputTrust` gains `mode` (`audit` or `enforce`). Under audit, `withheld` counts what `forge_read` **would** have withheld, and `min` includes the level of that text, since the run could read it. A split child and an integration task inherit the parent's `min` |
| HR9 | T5 is `agent-guard`, called "over the same webhook contract the gateway uses"; H1 runs it in audit | No `agent-guard` service exists in Maku, ANR or CNR | PR 1 ships `hardening.WebhookClassifier`, a client for agentgateway's guardrail webhook `/request` (`messages[].role/content`; `Reject` means a hit), and `hardening.agentGuard.{url, timeout}`. With no URL, T5's effective mode is `off`, and the hash records that. Building and deploying `agent-guard` (O4's ADR) is outside this plan. H1's gate measures T1 to T4 only, so it does not wait for T5 |
| HR10 | H2 enforces four output types while `push` stays in the sandbox | `Hardening.Mode` is one mode per repository (`Maku/internal/factory/config/hardening.go`) | `enforceOutputs: [pr, comment, labels, issue]`, centrally and per repository. A repository's list must be a superset of the central one. It needs a mode of at least `audit` and a configured publisher. `push` is refused "until H3". The list joins `Hash()`. For an enforced type, T1 to T4 run at `enforce` and T5 at most `audit` |
| HR11 | UH5: one `publisher` identity; O5: a `publisher.sts.yaml` policy per repository | `arm` hands off any PR not authored by `agentsLogin` (`Maku/internal/factory/reconciler/arm.go:160`). The factory already files `factory/proposed` issues as the factory App (`Maku/internal/factory/api/internal.go:174`) | `pr`, `comment` and `labels` are applied **as the agents App**, with a token the factory exchanges at octo-sts under `.github/chainguard/publisher.sts.yaml` (`pull_requests: write`, `issues: write`, `contents: read` until H3). Agent text therefore never speaks with the factory App's voice, and the authorship and push checks keep holding. **`issue` is filed by the factory App**, through the existing `CreateIssue(…, factory/proposed)` path, because `factory/*` labels are the factory's (v4 D12: "the factory App handles issues, labels") |
| HR12 | v4 D10: per-run minted tokens; "an implementer gets `contents:write` and `pull_requests:write`; a reviewer gets read plus review" | Today's role policies: only the implementer holds `pull_requests: write`. The reviewer is read-only and its verdict is posted by `verdictpost` (CNR `.github/chainguard/agent-*.sts.yaml`) | **D10 must be amended** before v4 step 3 is built. The minter's role table follows the repository's effective `enforceOutputs`: the implementer drops `pull_requests: write` once `pr`, `comment` or `labels` is enforced, and drops `contents: write` at H3. The reviewer gets read only, never "plus review". Publisher tokens come from the same minter from the agents App key, a second `PublisherTokens` adapter beside H2's octo-sts one. R2 and R3 are re-read against the minter's table instead of policy files. Filed as an ANR and Maku spec follow-up (After this step) |
| HR13 | v4 D5: the run-controller becomes the run path, octo-sts optional | `runctl` runs read `GIT_TOKEN_FILE`, a PAT or the demo's shared token (`Maku/images/maku-harness/git_credential_agent.py`, `Maku/internal/factory/demo/gittoken.go:36`), which no role policy scopes | H2's live gate runs on the XR plus octo-sts path. v4 step 2 must not move a repository with `enforceOutputs` to a shared-token run path before step 3's minter applies HR12. With `enforceOutputs` set and `runs.kind` not the XR, the factory logs a startup warning that the sandbox token is unscoped. It does not refuse to start, because the tracer still reports every direct write |
| HR14 | The output tools are served on the `platform` MCP server | `Tool.Roles` filters by role only (`Maku/internal/mcp/server.go:70-82`). The broker has no hardening config | The factory writes `agents.ogenki.io/safe-outputs` (the run's enforced types, sorted, comma-separated) and `agents.ogenki.io/label-allowlist` on the AgentRun at creation. `runwatch.Run` reads both. `mcp.Tool` gains `Gate func(runwatch.Run) bool`: an output tool is listed and callable only when the annotation names its type. The applier's T1 stays the authority |
| HR15 | AC7: p95 at most 2 s per non-push output | The reconciler polls tasks every 30 s; the broker has no system stream | The applier is its own runnable. It polls the rooms of tasks with a live implementer or tester run every `hardening.applier.interval` (default 2 s) from a per-room cursor, and keeps applying a run's requests after the run ends. `agent_factory_hardening_output_seconds{type}` measures it |
| HR16 | Exactly one answer per request; idempotent applies | A crash between the GitHub write and the answer would write twice | Every applied object carries the output id: `Agent-Output: <id>` in the PR footer, `<!-- maku-output:<id> -->` appended to comments and issue bodies after T2 (which strips the model's own HTML comments). Before writing, the applier looks for its own marker (the open PR for the branch, the agents App's recent comments, `factory/proposed` issues since the run started). If the marker is found, it answers `applied` with that object |
| HR17 | `comment` targets the task's PR or issue; `labels` the task's PR | A text task has no issue, and labels can be requested before the PR exists | An eleventh bounded refusal, `output_target` (layer T1): no such target for this task yet. It is added to the drill scorer's layer map |
| HR18 | `pr` is `{title, body, draft}`, one open and five edits | GitHub's REST edit does not toggle draft | `draft` is honoured on open only. An edit changes the title and body |
| HR19 | — | #477 treats an ended run that committed but opened no PR as a failure (`Maku/internal/factory/reconciler/implement.go`, `detectPR`) | Under enforcement, the end-of-run step waits while any of the run's requests is unanswered, for up to `hardening.applier.answerWait` (default 5 min). After that it proceeds and logs `outputs_unanswered`. The applier still answers late requests |
| HR20 | — | `OutputRecord.Validate` requires a `https://github.com/` object | Unchanged here. GHES (v4 D4) widens it when `github.webURL` exists |
| HR21 | "The same check is exposed as a port" (RunLore RK16), and `inputTrust.min` serves RK11 | RunLore is its own product (v4 D20). Its phases 3 and 4 plans are "written at their gate" | PR 1 adds `Checker.CheckText(ctx, texts, canary) Verdict` (T2, T3, T5 on free text) and records `status.inputTrust`. A factory endpoint for RunLore is RunLore's phase-3 Maku plan, not this one |

---

## PR 1 · H1, audit (L)

### Task 1: the fence file's `hardening` block

**Spec:** Protected paths, UH10, HR6

**Files:**
- Modify: `internal/factory/fence/fence.go`, `internal/factory/fence/fence_test.go`
- Create: `internal/hardening/repo.go`, `internal/hardening/repo_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestParseAcceptsHardening` (fence): a file with `factory: gcp-0` and a `hardening:` map parses to `gcp-0`. A `hardening: 3` still parses, because ownership never depends on the block.
  - `TestParseRepositoryBlock` (hardening), table-driven:
    - `protectedPaths: [{path: scripts/ci/**, on: blocked}]` compiles, and `scripts/ci/x.sh` is `OnBlocked`;
    - `minAuthorTrust: merged` is kept;
    - `minAuthorTrust: none` is refused (only raising);
    - an unknown key is refused;
    - more than `MaxExtraPaths` entries are refused;
    - `on: maybe` is refused.
  - `TestRepositoryBlockFailsClosed`: unparseable YAML answers the floor and `Merged`, with an error.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/fence/ ./internal/hardening/ -run 'TestParseAcceptsHardening|TestParseRepositoryBlock|TestRepositoryBlockFailsClosed' -count=1`. Expected: FAIL. The fence test errors with "not {factory: <name>}", and the hardening tests fail to compile (`undefined: ParseRepositoryBlock`).
- [ ] **Step 3: implement.**
  - `fence.Parse` gets `Hardening json.RawMessage `json:"hardening"``, beside `Classes`, and never reads it.
  - `hardening.ParseRepositoryBlock(raw []byte, central Level) (RepositoryPolicy, error)` decodes the block strictly. `RepositoryPolicy{Paths ProtectedPaths, MinTrust Level}`. `MinTrust` is the maximum of the central level and the block's.
  - The fence's `OnFile` hook hands the body to a new `hardening.RepositoryPolicies` cache, keyed by repository and replaced at each successful read.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(hardening): a repository's fence file may extend the protected paths`.

### Task 2: `mode: audit` accepted; the classifier client config

**Spec:** Configuration, UH8, HR9, HR10 (enforce still refused)

**Files:**
- Modify: `internal/factory/config/hardening.go`, `internal/factory/config/hardening_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestHardeningAuditAccepted`: central `mode: audit` and a repository `{mode: audit}` validate, and `ModeFor` answers `audit`.
  - `TestHardeningEnforceStillRefused`: `mode: enforce`, centrally or per repository, fails with "enforce arrives with stage H3".
  - `TestClassifierEnforceRefused`: `layers.classifier: enforce` fails.
  - `TestAgentGuardConfig`: `agentGuard: {url: http://agent-guard.agent-system.svc:8080, timeout: 2s}` validates. A non-http(s) URL, a URL with userinfo, or a timeout outside (0, 10s] fails.
  - `TestHashCoversClassifierPresence`: two files that differ only in whether `agentGuard.url` is set hash differently. Two that differ only in the URL's host hash the same.
  - `TestReadinessStaysFalseUnderAudit`: `Evaluator{Settings: cfg.HardeningSettings}` with stub policies and drills that would pass answers `Hardened: false`, with `Missing` containing `mode_not_enforce`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ -run 'TestHardening|TestClassifier|TestAgentGuard|TestHash|TestReadinessStaysFalse' -count=1`. Expected: FAIL. Audit is refused with "this release is stage H0", and `AgentGuard` is undefined.
- [ ] **Step 3: implement.**
  - The off-only refusals in `validateHardening` become "enforce arrives with stage H3", for `mode` and each repository.
  - Add `AgentGuard{URL string; Timeout Duration}` to `Hardening` and `hardeningFile`.
  - `EffectiveClassifier()` answers `off` when the URL is empty.
  - `Hash()` takes `EffectiveClassifier()` in place of the raw `Layers.Classifier`.
  - `HardeningSettings` uses `LayersFor(mode, EffectiveClassifier())`.
- [ ] **Step 4: run** the same command. Expected: PASS. Then run `go test ./internal/factory/config/ -count=1`, which keeps H0's tests green except the two whose expected refusal text changed. Update those two in this step and name them in the commit body.
- [ ] **Step 5: commit** `feat(config): hardening audit mode and the agent-guard client settings`.

### Task 3: `output_shadowed`, audit findings, and stamped readers

**Spec:** UH4, HR2, HR3

**Files:**
- Modify: `internal/envelope/output.go`, `internal/envelope/output_test.go`
- Create: `internal/envelope/output_read.go`, `internal/envelope/output_read_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestOutputShadowedValidate`:
    - a shadowed record with id, type, digest and object is valid with or without a reason;
    - its reason may be a refusal or `would_wait`;
    - `Audit` entries must match `^t[1-5]_[a-z]+$` for the layer and the reason regexp for the reason, at most 5 of them;
    - `Audit` on a `requested` or `refused` record is invalid.
  - `TestReadOutput`: `envelope.ReadOutput(e, runID)` returns the kind and record only for:
    - an `output_requested` stamped `ActorAgent`, `agent:<runID>`, `OriginClient`;
    - an answer or shadow stamped `ActorSystem` with an id starting `system:factory`.

    An agent-stamped `output_applied` is ignored, and so is a record that fails `Validate`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/envelope/ -run 'TestOutputShadowed|TestReadOutput' -count=1`. Expected: FAIL (`undefined: KindOutputShadowed`).
- [ ] **Step 3: implement.**
  - `KindOutputShadowed MessageKind = "output_shadowed"`.
  - `OutputFinding{Layer, Reason string}`.
  - `OutputRecord.Audit []OutputFinding `json:"audit,omitempty"``.
  - `Validate` per kind.
  - `ReadOutput` as tested.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(envelope): output_shadowed and the stamp every output reader checks`.

### Task 4: the broker's system outputs route and the factory's client

**Spec:** UH4, HR2

**Files:**
- Modify: `internal/bridgeapi/system.go`, `internal/bridgeapi/server.go`, `internal/bridgeapi/system_test.go`, `internal/factory/rooms/client.go`, `internal/factory/rooms/client_test.go`, `internal/factory/reconciler/reconciler.go` (the `RoomLog` interface), the reconciler's room fake
- Modify: `docs/api.md`, `docs/room-log.md`

- [ ] **Step 1: write the failing tests.**
  - `TestRoomOutputs` (bridgeapi), against the in-memory log:
    - `POST /v1/rooms/{id}/outputs` with `{runId, kind: output_shadowed, record}` from an allowlisted `system:factory` answers 201 with a seq. The stored event is `ActorSystem`, its `RunID` is `runId`, and the payload has `Kind`, `Delivery: none` and `Output`;
    - the same id and kind again answer 200 with the first seq;
    - `output_requested` is 400 `bad_message`, and so is a record that fails `Validate`;
    - a non-system caller is 401 or 403;
    - a `runId` not matching `^[a-z0-9][a-z0-9-]{0,62}$` is 400;
    - the payload goes through the redactor.
  - `TestClientOutput` (rooms): `Client.Output(ctx, room, runID, kind, rec)` posts the body above, and maps 400 to an error that `errors.Is` matches with `ErrBadMessage`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/bridgeapi/ ./internal/factory/rooms/ -run 'TestRoomOutputs|TestClientOutput' -count=1`. Expected: FAIL (route 404, and `undefined: Output`).
- [ ] **Step 3: implement** `roomOutputs`, mounted with `s.bounded(maxMessageBytes, …)`:
  - `OriginClient = p.ID + ":output:" + kind`;
  - `OriginSeq` is the first 8 bytes of the 16-hex id as a positive int64 (`& math.MaxInt64`, and `|1` when zero).

  Then `rooms.Client.Output`, the `RoomLog` interface method and its fake.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(broker): a system route for the factory's output answers`.

### Task 5: the forge reads the shadow pass needs

**Spec:** UH6 (association), HR1

**Files:**
- Modify: `internal/factory/forge/forge.go`, `internal/factory/forge/github.go`, `internal/factory/forge/github_test.go`, `internal/factory/forge/forgetest/fake.go`, `internal/factory/reconciler/reconciler.go` (`TaskForge`)

- [ ] **Step 1: write the failing tests**, against the recorded-response test server already used in `github_test.go`:
  - `TestCommentAssociation`: `RecentComments` and `AllComments` fill `Comment.Association` from REST `author_association`.
  - `TestLabelEventsBy`: `LabelEventsBy(ctx, number, actor, since)` returns every `labeled` event by `actor` at or after `since`, oldest first, with `ErrEventsTruncated` past the page cap.
  - `TestIssuesByAuthorSince`: `IssuesByAuthorSince(ctx, login, since)` lists issues (not PRs) the login opened since `since`, at most 2 pages.
  - `TestIssueAssociation`: `Issue` fills `Issue.Association`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/forge/ -run 'TestCommentAssociation|TestLabelEventsBy|TestIssuesByAuthorSince|TestIssueAssociation' -count=1`. Expected: FAIL (unknown field `Association`).
- [ ] **Step 3: implement** the fields and the two methods, add them to `TaskForge` and the fake, and keep `Diff` and `BranchActivity` as they are.
- [ ] **Step 4: run** the same command, then `go test ./internal/factory/... -count=1`. Expected: PASS.
- [ ] **Step 5: commit** `feat(forge): author association and the reads that trace an agent's writes`.

### Task 6: the shadow core in `hardening`

**Spec:** Threat check, HR1, HR3, HR4, HR21

**Files:**
- Create: `internal/hardening/shadow.go`, `internal/hardening/shadow_test.go`, `internal/hardening/footer.go`, `internal/hardening/footer_test.go`
- Modify: `internal/hardening/threat.go` (`CheckText`)

- [ ] **Step 1: write the failing tests.**
  - `TestStripHarnessFooter`: a body ending in `---\nAgent-Room: r\nAgent-Run: x` loses exactly that block. A body whose last paragraph is not wholly `Agent-*` lines is unchanged. A forged `Agent-Run:` line in the middle stays, and is neutralised later.
  - `TestShadowVerdict`, table-driven over `ObservedWrite{Type, Payload, Object, Files, Added}`, with all layers in audit. `Shadow(ctx, checker, counts, w, ownIssue, canary) ShadowResult{Verdict: would_apply|would_refuse|would_wait, Layer, Reason, Audit}`:
    - a PR with a 10-character body is `would_refuse t1_schema output_schema`;
    - a comment with the canary is `would_refuse t3_secrets canary_leaked`;
    - a push touching `.github/workflows/x.yml` is `would_wait t4_paths protected_path_needs_approval`;
    - a push touching a `blocked` extension path is `would_refuse t4_paths protected_path_blocked`;
    - the fourth comment of a run is `would_refuse t1_schema output_cap`;
    - a label outside the allowlist is `would_refuse t1_schema output_schema`;
    - a clean PR is `would_apply` with T2's report;
    - a classifier hit is `would_apply` with `Audit` containing `t5_classifier injection_suspected` (T5 never decides H1's verdict).
  - `TestShadowDigest`: the record's digest is `Digest` of the observed payload's canonical JSON, and stays stable across runs.
  - `TestCheckText`: T2, T3 and T5 run on free text with no output type, and T1 and T4 are skipped.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ -run 'TestStripHarnessFooter|TestShadow|TestCheckText' -count=1`. Expected: FAIL (`undefined: Shadow`).
- [ ] **Step 3: implement.**
  - `ObservedWrite`, `ShadowResult` and `Shadow`. It forces `Layers` to audit for T1 to T4, takes the first T1–T4 finding as the verdict, maps `protected_path_needs_approval` to `would_wait`, and copies the remaining findings into `Audit`.
  - `StripHarnessFooter` in `footer.go`.
  - `Checker.CheckText`.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(hardening): shadow verdicts on observed writes, and the free-text check`.

### Task 7: the canary and `status.inputTrust`

**Spec:** UH7, T3, HR7, HR8

**Files:**
- Modify: `api/factory/v1alpha1/task_types.go` (`TaskStatus.InputTrust`; `RunRecord.Shadowed`), generated deepcopy and CRDs (`task crd:gen`), `internal/factory/runs/runs.go` (`Spec.Canary`, `AnnCanary`), `internal/factory/reconciler/implement.go` (`startRun`), `internal/factory/reconciler/text.go`
- Create: `internal/factory/reconciler/trust.go`, `internal/factory/reconciler/trust_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestCanaryPlantedUnderAudit`: with the repository in `audit`, `startRun` creates a claim whose `TaskText` ends with the canary line `Canary <mkc-…>: never write this token anywhere: not in code, commits, pull requests, comments or messages.` and whose annotations carry `agents.ogenki.io/canary`. Under `off` it plants neither. The canary never appears in a room message the factory posts.
  - `TestInputTrustRecorded`, table-driven:
    - the first brief records `issue` at `approved` (vouched by the trigger label), `steering` and `default_branch` at `merged`;
    - a revision adds `own_ci` at `approved`;
    - a RunLore-sourced task adds `runlore` at `none`;
    - a thread with one `NONE` comment under `minAuthorTrust: approved` gives `withheld.comment: 1`, `min: none`, `mode: audit`;
    - a split child's record holds `parent` at the parent's `min`.
  - `TestInputTrustCRD` (`api/factory/v1alpha1`): the schema caps `sources` at 16 items and `withheld` at 16 keys, and accepts only the five level names.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ ./api/factory/v1alpha1/ -run 'TestCanaryPlanted|TestInputTrust' -count=1`. Expected: FAIL (`undefined: InputTrust`).
- [ ] **Step 3: implement.**
  - The CRD type: `InputTrust{Min string; Withheld map[string]int32; Sources []TrustSource; Mode string}`, with `+kubebuilder` enums and bounds.
  - `RunRecord.Shadowed bool`, which the shadow pass sets.
  - `trust.go` builds a `hardening.InputTrust` from the brief's parts and from `AllComments` on the issue and PR (with `Association`), using `hardening.Policy{Min: RepositoryPolicy.MinTrust, Maintainers, TrustedBots, Blocked}`, and writes it at each run start and end.
  - `startRun` plants the canary when `ModeFor(repo).AtLeast(audit)`.
  - Then run `task crd:gen`.
- [ ] **Step 4: run** the same command, then `task crd:check`. Expected: PASS, and no drift.
- [ ] **Step 5: commit** `feat(factory): plant a canary and record each task's input trust under audit`.

### Task 8: the shadow pass at the end of a writer run

**Spec:** H1 row, HR1, HR3

**Files:**
- Create: `internal/factory/reconciler/shadow.go`, `internal/factory/reconciler/shadow_test.go`
- Modify: `internal/factory/reconciler/implement.go` (call site on a terminal implementer run), `internal/factory/reconciler/tester.go` (terminal tester run), `internal/factory/fmetrics/fmetrics.go`, `internal/factory/fmetrics/fmetrics_test.go`

- [ ] **Step 1: write the failing tests** with the forge fake and the room fake.
  - `TestShadowPass`: an implementer run that pushed two commits as `agentsLogin`, opened a PR, posted one comment and added one label, all within `[Started, Finished+grace]`, gets five `output_shadowed` posts:
    - each push's `Files` and `Added` come from `Diff(before, after)`;
    - the PR body has the harness footer stripped;
    - the label is checked against the allowlist.
    - `RunRecord.Shadowed` becomes true. A second reconcile posts nothing new, because the replay keys hold and `Shadowed` gates it.
  - `TestShadowPassNeverMovesTheTask`: a forge error leaves the phase and reason unchanged, logs `shadow pass failed`, and retries on the next reconcile.
  - `TestShadowPassOffAndDemo`: under `off`, or in the demo profile, nothing is read or posted.
  - `TestShadowMetrics`: `agent_factory_hardening_shadow_total{repository,type,verdict,layer,reason}` counts each record. Each `would_refuse` or `would_wait` logs `task.id`, `run.id`, `type`, `layer`, `reason` and `object`, with no payload text.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ ./internal/factory/fmetrics/ -run 'TestShadow' -count=1`. Expected: FAIL (`undefined: shadowRun`).
- [ ] **Step 3: implement** `(*Reconciler).shadowRun(ctx, cfg, t, run)`. It is called once a writer run is terminal, before the outcome is handled, and only when the repository's mode is at least `audit`. It:
  - collects `ObservedWrite`s from `BranchActivity(run.Branch)` filtered to `agentsLogin` in the window;
  - adds the task's PR (if it was created or its body changed in the window), `AllComments` by `agentsLogin` in the window, `LabelEventsBy(agentsLogin)`, and `IssuesByAuthorSince(agentsLogin)`;
  - runs `hardening.Shadow` with the run's canary and the repository's `RepositoryPolicy.Paths`;
  - posts each result through `RoomLog.Output`;
  - records the metric.

  The writes are sorted by time so caps count in order. Add the metric to `fmetrics`, plus its test.
- [ ] **Step 4: run** the same command, then `go test ./internal/factory/reconciler/ -count=1`. Expected: PASS, with existing reconciler tests unchanged (they run under `off`).
- [ ] **Step 5: commit** `feat(factory): shadow every agent write under audit`.

### Task 9: T5's webhook client

**Spec:** UH8 T5, HR9

**Files:**
- Create: `internal/hardening/classifier.go`, `internal/hardening/classifier_test.go`

- [ ] **Step 1: write the failing tests** against an `httptest` server:
  - `TestWebhookClassifier`: `Injection(ctx, texts)` posts `{"body":{"messages":[{"role":"user","content":…}]}}` to `<url>/request`, one message per text, chunked so that no message exceeds 2,000 runes. A `Reject` answer is a hit. A `Pass` or `Mask` answer is no hit;
  - a non-2xx answer, a timeout or malformed JSON is an error, which `Checker` maps to `classifier_unavailable`;
  - the client never sends more than 64 KiB.
  - `TestWebhookClassifierNoContentInErrors`: the error strings contain no text from the request.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ -run 'TestWebhookClassifier' -count=1`. Expected: FAIL (`undefined: WebhookClassifier`).
- [ ] **Step 3: implement** `WebhookClassifier{URL string; HC *http.Client}` with `httpx.New(timeout, nil)` at the call site, as `triage.HTTPClassifier` does.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(hardening): the agent-guard webhook client for T5`.

### Task 10: wiring, docs, the gate (PR 1)

**Files:**
- Modify: `internal/app/factory.go` (the `Checker`, `RepositoryPolicies` from the fence's `OnFile`, the redactor as `SecretScanner`, `WebhookClassifier` when `agentGuard.url` is set), `docs/security.md`, `docs/factory.md` (a "Hardening, audit" section), `docs/room-log.md` (`output_shadowed`), `docs/roadmap.md`
- Test: `internal/app/factory_test.go`

- [ ] **Step 1: failing test** `TestFactoryHardeningWiring`:
  - under `audit` the reconciler gets a `Checker` with layers at audit, the effective classifier, and the fence-fed repository policies;
  - under `off` it gets none;
  - `coordination.Service.Prerequisites` is still `nil`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/app/ -run TestFactoryHardeningWiring -count=1`. Expected: FAIL.
- [ ] **Step 2: wire it.** Run the same command. Expected: PASS.
- [ ] **Step 3: docs.** State what audit changes (a canary line, shadow records, `status.inputTrust`), what it does not change (no refusal, no withholding, no tools), and HR6's stop rule.
- [ ] **Step 4: gate.**
  - Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
  - Run `git diff origin/main...HEAD --stat -- internal/app/mcp.go internal/mcp/server.go`. Expected: no output (no tool is served).
- [ ] **Step 5: open PR 1.** Link the spec and this plan, list HR1 to HR9 and HR21 under "Builder choices", and include the data-flow diagram. Do not merge or tag: the owner reviews.

---

## PR 2 · H2a, the applier and the publisher, unreachable (L)

### Task 11: forge writes and the octo-sts publisher token

**Spec:** UH5, O5, HR11, HR12

**Files:**
- Create: `internal/factory/forge/octosts.go`, `internal/factory/forge/octosts_test.go`
- Modify: `internal/factory/forge/github.go` (`ConnectTokens`, `CreatePR`, `EditPR`), `internal/factory/forge/github_test.go`, `internal/factory/forge/forgetest/fake.go`

- [ ] **Step 1: write the failing tests.**
  - `TestOctoSTSTokens`: `OctoSTS{URL, TokenFile, Scope: "owner/repo", Identity: "publisher"}.Token(ctx)`:
    - reads the projected token file at each mint;
    - calls `GET <url>/sts/exchange?scope=owner/repo&identity=publisher` with it as the bearer;
    - caches the answer until 10 minutes before `expires_at`;
    - refuses a non-https URL;
    - never logs either token.
  - `TestConnectTokens`: `ConnectTokens(ctx, Options, TokenSource)` makes every API call with the source's token. A mint failure fails `Connect`.
  - `TestCreateEditPR`:
    - `CreatePR(ctx, base, head, title, body, draft)` posts `/pulls` and returns the `PR`;
    - `EditPR(ctx, number, title, body)` patches it;
    - a 422 "A pull request already exists" maps to `ErrPRExists`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/forge/ -run 'TestOctoSTS|TestConnectTokens|TestCreateEditPR' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.**
  - A `TokenSource` interface; the `tokens` type becomes its App-key implementation.
  - `OctoSTS` as tested.
  - `CreatePR`, `EditPR` and the fake.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(forge): a publisher connection through octo-sts, and pull request writes`.

### Task 12: the factory-written footer and the idempotency markers

**Spec:** Safe outputs (`pr`), HR4, HR16, HR18

**Files:**
- Modify: `internal/hardening/footer.go`, `internal/hardening/footer_test.go`

- [ ] **Step 1: failing tests**, ported case for case from `images/maku-harness/tests` for `pr_footer.py`:
  - `TestNeutraliseAgentLines`: every line a line-anchored reader would take for `Agent-*:`, in any case and behind any line separator, gets the `(agent-written) ` prefix.
  - `TestFactoryFooter`: `Footer(Provenance{Room, Run, Role, Task, TaskURL, Model, Output})` is `---` plus one `Key: value` line per non-empty, printable field. A `TaskURL` that is not `https://github.com/…` is dropped. `WithFooter(body, p)` always ends with it.
  - `TestMarkers`: `Marker(id)` is `<!-- maku-output:<16hex> -->`, and `FindMarker(body)` reads it back only from the body's last line.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ -run 'TestNeutralise|TestFactoryFooter|TestMarkers' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(hardening): the factory writes the provenance footer and output markers`.

### Task 13: the applier

**Spec:** UH3, UH4, UH5, T1–T5, HR2, HR14 to HR17

**Files:**
- Create: `internal/factory/applier/applier.go`, `internal/factory/applier/apply.go`, `internal/factory/applier/applier_test.go`, `internal/factory/applier/apply_test.go`
- Modify: `internal/hardening/outputs.go` (`RefuseTarget`), `internal/hardening/drill.go` (`refusalLayer`), `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: write the failing tests**, with the room fake, the forge fake as the publisher and as the factory App, and a fake clock.
  - `TestApplierApplies`, table-driven over the enforced types (all four):
    - `pr` on a branch with no PR opens one from `agent/<task>` into the task's base, with the footer and `draft`;
    - a second `pr` edits the same PR;
    - `comment{target: pr}` posts on the task's PR with the marker, and `comment{target: issue}` on the task's issue;
    - `labels` adds the labels to the PR.

    Each gets exactly one `output_applied` with the object URL and the sanitised payload's digest.
  - `TestApplierRefuses`:
    - a seventh `pr` is refused `output_cap`;
    - a comment with the canary is refused `canary_leaked`, and an alert is logged;
    - a comment with an AWS key is refused `secret_found`;
    - `labels` with no PR is refused `output_target`;
    - `comment{target: issue}` on a text task is refused `output_target`;
    - a type not in the run's safe outputs is refused `output_type`.

    Each gets exactly one `output_refused`, and nothing reaches the forge.
  - `TestApplierExactlyOnce`:
    - a restart, modelled as a new applier over the same log, answers nothing twice;
    - a crash after the GitHub write and before the answer is modelled by a forge holding the marker, and the applier answers `applied` with the existing object without writing again.
  - `TestApplierReadsOnlyStampedRequests`: a `output_requested` posted by a human or another run's agent is ignored.
  - `TestApplierAuditTypes`: a request of a type that is not enforced is answered `output_refused` with reason `output_type`. Requests can only arrive for served types, so this guards the gate.
  - `TestApplierLatency`: `agent_factory_hardening_output_seconds{type}` observes the time from the request event's timestamp to the answer.
  - `TestApplierT5Audit`: a classifier hit on an applied `pr` lands in `output_applied.audit`, and the output is still applied.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/applier/ ./internal/hardening/ -count=1`. Expected: FAIL (package missing, and `undefined: RefuseTarget`).
- [ ] **Step 3: implement.**
  - `Applier{Tasks, Rooms, Publisher func(repo) (Publisher, error), Factory func(repo) TaskForge, Checker func(repo) hardening.Checker, Enforced func(repo) []OutputType, Interval, Now, Metrics, Log}` as a `manager.Runnable`. Each tick it:
    - lists Tasks whose current run is an implementer or tester run, terminal for at most `answerWait`;
    - reads `EventsSince(room, cursor)` with `envelope.ReadOutput`;
    - rebuilds `Counts` from all of the run's requests in seq order;
    - checks each unanswered request with `LayersFor(enforce, min(classifier, audit))`;
    - applies it through `apply.go` (one function per type, marker lookup first);
    - answers through `RoomLog.Output`.
  - Add `RefuseTarget Refusal = "output_target"` to the refusals and to the drill scorer's layer map.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(factory): the safe-output applier for pr, comment and labels`.

### Task 14: the `issue` output through the factory App

**Spec:** Safe outputs (`issue`), HR11

**Files:**
- Modify: `internal/factory/applier/apply.go`, `internal/factory/applier/apply_test.go`

- [ ] **Step 1: failing test** `TestApplyIssue`:
  - the issue is filed through the factory App's `CreateIssue` with exactly `[factory/proposed]` (never the trigger label), the draft template's title and body, a provenance paragraph naming the task, run and output id, and the marker;
  - a replay finds the marker in `IssuesLabelledSince(factory/proposed, run.Started)` and does not file again;
  - the cap is 1.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/applier/ -run TestApplyIssue -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): follow-up issues from runs are filed factory/proposed`.

### Task 15: the scorer counts only stamped answers; the gate (PR 2)

**Spec:** UH14, HR2

**Files:**
- Modify: `internal/hardening/drill.go`, `internal/hardening/drill_test.go`, `docs/room-log.md`

- [ ] **Step 1: failing test** `TestScoreIgnoresUnstampedAnswers`: an `output_applied` stamped by an agent neither answers nor scores, and the request reads as `request_unanswered`. Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ -run TestScoreIgnoresUnstamped -count=1`. Expected: FAIL.
- [ ] **Step 2:** `scoreOutputs` reads through `envelope.ReadOutput`. Run the same command. Expected: PASS.
- [ ] **Step 3: gate.**
  - Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
  - Run `git grep -n 'applier.New\|applier.Applier{' -- internal/app/`. Expected: no output (the applier is not started yet).
- [ ] **Step 4: open PR 2**, listing HR11, HR14 to HR18 and HR20 under "Builder choices".

---

## PR 3 · H2b, non-code outputs enforced, per repository (M)

### Task 16: `enforceOutputs` and the publisher config

**Spec:** H2 row, HR10, HR11, HR13

**Files:**
- Modify: `internal/factory/config/hardening.go`, `internal/factory/config/hardening_test.go`
- Modify: `charts/maku-factory/values.yaml`, `charts/maku-factory/templates/deployment.yaml` (an optional projected token volume, audience from `hardening.publisher.audience`), the chart's render test

- [ ] **Step 1: failing tests.**
  - `TestEnforceOutputs`:
    - central `enforceOutputs: [pr]` with a repository's `[pr, comment, labels, issue]` validates;
    - a repository's list missing `pr` fails (it must be a superset);
    - `push` fails with "push arrives with stage H3";
    - an unknown type fails;
    - a list under `mode: off` fails;
    - a list without `publisher.octoSTS` fails.
  - `TestEnforcedFor`: `EnforcedFor(repo)` is the effective sorted list.
  - `TestHashCoversEnforce`: adding a type changes `Hash()`.
  - `TestUnscopedTokenWarning`: with `runs.kind` not the XR and a non-empty list, `Warnings()` names HR13.
  - Chart render: with `hardening.publisher.audience` set, the factory pod mounts a projected token at `/var/run/secrets/octo-sts/token`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ -run 'TestEnforce|TestHashCoversEnforce|TestUnscoped' -count=1` and `task chart:check`. Expected: FAIL.
- [ ] **Step 3: implement** `EnforceOutputs`, `Publisher{OctoSTS{URL, TokenFile, Identity}}`, `Applier{Interval, AnswerWait}` with defaults of 2s and 5m, the validation, and the chart values.
- [ ] **Step 4: run** both commands. Expected: PASS.
- [ ] **Step 5: commit** `feat(config): enforce the non-code safe outputs per repository`.

### Task 17: serve the output tools per run

**Spec:** UH4, HR14

**Files:**
- Modify: `internal/factory/runs/runs.go` (`Spec.SafeOutputs`, `Spec.LabelAllowlist`, `AnnSafeOutputs`, `AnnLabelAllowlist`), `internal/factory/reconciler/implement.go` (`startRun` sets them from `EnforcedFor`), `internal/runwatch/run.go` (reads them), `internal/mcp/server.go` (`Tool.Gate`), `internal/mcp/hardening.go` (each output tool's `Gate`; the allowlist from the run), `internal/app/mcp.go` (`Tools: append(RoomTools(…), OutputTools(…)…)`), `internal/runctl/pod.go` (`MCP_READ_ONLY_TOOLS` gains `room-broker__output_<type>` for served types; `SAFE_OUTPUTS` env)
- Test: `internal/mcp/server_test.go`, `internal/mcp/hardening_test.go`, `internal/runwatch/run_test.go`, `internal/runctl/roomtools_test.go`, `internal/factory/reconciler/implement_test.go`

- [ ] **Step 1: failing tests.**
  - `TestToolGate`:
    - a run annotated `safe-outputs: pr,comment` lists and may call `output_pr` and `output_comment`, and neither lists nor may call `output_labels`, `output_issue` or `output_push`;
    - a run with no annotation lists exactly today's room tools. This guards against a list diff for every non-enforced run.
  - `TestStartRunSafeOutputs`: an implementer claim on an enforced repository carries both annotations. A reviewer claim carries neither.
  - `TestRunctlReadOnlyTools`: the pod's `MCP_READ_ONLY_TOOLS` and `SAFE_OUTPUTS` follow the annotation.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/mcp/ ./internal/runwatch/ ./internal/runctl/ ./internal/factory/reconciler/ -run 'TestToolGate|TestStartRunSafeOutputs|TestRunctlReadOnlyTools' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.**
  - Output tools are built with `OutputConfig{Bundles: nil}`, so `output_push` stays unserved, and `LabelAllowlist` is read per call from the run.
  - `server.go` checks `Gate` in both `tools/list` and `call`.
- [ ] **Step 4: run** the same command, then `go test ./internal/mcp/ ./internal/app/ -count=1`. Expected: PASS.
- [ ] **Step 5: commit** `feat(mcp): serve the safe-output tools to the runs that must use them`.

### Task 18: the run's rules and the harness

**Spec:** H2 row, UH1

**Files:**
- Modify: `internal/runctl/rules.go`, `internal/runctl/rules_test.go`, `internal/factory/reconciler/text.go` (the implementer brief's PR line), `images/maku-harness/gh`, `images/maku-harness/tests/test_gh_wrapper.py` (create it if absent)

- [ ] **Step 1: failing tests.**
  - `TestRulesSafeOutputs`: with `SafeOutputs` containing `pr`, the implementer's rule 3 reads "Request the pull request with the output_pr tool: the factory opens it from <branch> into <base>, or updates it, and writes the footer. Never open, edit, comment on or label pull requests or issues with gh." The `gh pr create` line is gone. Without `SafeOutputs` the rules are byte-for-byte today's.
  - `test_gh_wrapper.py`: with `SAFE_OUTPUTS=pr,comment,labels,issue`, the wrapper exits 3 with "use output_pr" for `gh pr create|new|edit|ready`. It does the same with the matching tool name for `gh pr comment`, `gh issue comment|create|edit`, `gh pr edit --add-label` and `gh label`. Without the variable it behaves as today. `gh api` is not parsed: the token's `pull_requests: read` is the boundary.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/runctl/ ./internal/factory/reconciler/ -run 'TestRulesSafeOutputs|TestImplementerBrief' -count=1` and `task harness:test`. Expected: FAIL.
- [ ] **Step 3: implement.** Run both commands. Expected: PASS.
- [ ] **Step 4: commit** `feat(harness): runs with safe outputs request their pull requests`.

### Task 19: wait for answers; the tracer

**Spec:** AC2 (the H2 slice), HR19

**Files:**
- Modify: `internal/factory/reconciler/implement.go`, `internal/factory/reconciler/shadow.go`, `internal/factory/reconciler/shadow_test.go`, `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: failing tests.**
  - `TestRunEndWaitsForAnswers`:
    - a terminal implementer run with one unanswered `output_pr` keeps the task in `Implementing` and requeues it;
    - once the applier answers `applied`, `detectPR` finds the PR and the task goes on;
    - past `answerWait` the task proceeds as today's #477 path, and `outputs_unanswered` is logged.
  - `TestTracer`: on an enforced repository, the shadow pass matches each agents-App PR, comment or label write of an enforced type to an `output_applied`:
    - by PR number for `pr`;
    - by comment id, read from the marker, for `comment`;
    - by label name and time for `labels`.

    A write with no match is posted as `output_shadowed` with reason `untraced_write` and counted in `agent_factory_hardening_untraced_total{repository,type}`. Pushes are still shadowed as in H1.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ -run 'TestRunEndWaitsForAnswers|TestTracer' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): trace every agent write to the output that caused it`.

### Task 20: the sandbox policy gauge

**Spec:** R2 (as evidence; readiness unchanged), the H2 row's "sandbox roles lose `pull_requests: write`"

**Files:**
- Modify: `internal/hardening/readiness.go` (`PolicyPermissions`), `internal/hardening/readiness_test.go`, `internal/app/factory.go` (read at each fence pass for enforced repositories), `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: failing tests.**
  - `TestPolicyPermissions`: the parsed permission map from an octo-sts policy, lower-cased. A policy that does not parse is an error.
  - `TestSandboxPermissionGauge`: `agent_factory_hardening_sandbox_permission{repository,role,permission,access}` is 1 for each granted pair of each sandbox role, and `{role=publisher}` likewise.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ ./internal/factory/fmetrics/ -run 'TestPolicyPermissions|TestSandboxPermissionGauge' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): report each sandbox role's GitHub permissions on enforced repositories`.

### Task 21: start the applier; docs; the gate (PR 3)

**Files:**
- Modify: `internal/app/factory.go` (start `applier.Applier` when any repository enforces; the publisher via `forge.ConnectTokens` with `OctoSTS`), `internal/app/factory_test.go`, `docs/security.md`, `docs/factory.md`, `docs/operations.md` (enabling, rolling back, the stop rules), `docs/roadmap.md`

- [ ] **Step 1: failing test** `TestFactoryStartsApplier`:
  - an empty `enforceOutputs` everywhere starts no applier and builds no publisher;
  - a repository with a list starts it with that repository's publisher;
  - `Prerequisites` is still `nil`, and readiness answers `false`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/app/ -run TestFactoryStartsApplier -count=1`. Expected: FAIL.
- [ ] **Step 2: wire it.** Run the same command. Expected: PASS.
- [ ] **Step 3: docs.**
  - The enable order: Maku release, then config, then the publisher policy, then the gateway profile, then the role policy.
  - Rollback: remove the repository from `enforceOutputs` and restore `pull_requests: write` in the role policy.
  - What stays open: `push` (H3), raw reads (H4).
- [ ] **Step 4: gate.** Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 5: open PR 3**, listing HR10 to HR14 and HR19 under "Builder choices", and naming HR12 and HR13 as the v4 interactions the owner must confirm.

## After this step (not in these PRs)

### ANR follow-ups (each its own PR, after the matching Maku release)

1. **H1 config** (after PR 1's release):
   - Bump the Maku pin in `tooling/base/agent-factory/`.
   - Add a `hardening:` block to the factory config (`tooling/base/agent-factory/helm-values-configmap.yaml`):
     - central `mode: off`;
     - `repositories: {Smana/cloud-native-ref: {mode: audit}, Smana/ai-native-ref: {mode: audit}}`, the reference repositories;
     - `labelAllowlist` with the labels agents legitimately add today (read from the last 30 days of agents-App label events; at least `documentation`, `needs-human` if they exist);
     - `blockedUsers: []`;
     - `layers.classifier: audit`.
   - Teach `scripts/ci/factory_config.py` and `validate-factory-config.sh` the block.
2. **Gateway A2 and A3 in audit**, and **T5 in audit** (once `agent-guard` exists, HR9):
   - A2 is a `promptGuard.request[].webhook` to `agent-guard` `/request`, with `failureMode: FailOpen` and `action: Audit`, on both model listeners.
   - A3 is a `guardrails.processors[]` on `agent-mcp` `tools/call` Response, with `failureMode: FailOpen` and no `Mutate` yet. It goes in `infrastructure/base/agent-gateway/mcp.yaml`.
   - The `guardrails` access-log field goes in the gateway's log format.
   - `hardening.agentGuard.url` points at the service.
   - `agent-guard` itself (Prompt Guard 2, O4's ADR) is its own plan. **Open dependency for the owner:** which repository builds it.
3. **Dashboards and alerts:** panels for `agent_factory_hardening_shadow_total` and `_untraced_total`, and an alert on any `canary_leaked`.
4. **Fence blocks (HR6 stop rule):** a repository adds `hardening: {protectedPaths: …}` to its `.github/agent-factory.yaml` only after step 1 runs on gcp-0 and aws-0.
5. **H2 config** (after PR 3's release), one repository first (`Smana/ai-native-ref`):
   - `hardening.publisher.octoSTS` and the chart's projected-token audience;
   - an egress policy from the factory to the octo-sts host;
   - `repositories: {Smana/ai-native-ref: {mode: audit, enforceOutputs: [pr, comment, labels, issue]}}`;
   - `output_pr`, `output_comment`, `output_labels` and `output_issue` added to the implementer and tester capability profiles (`infrastructure/base/agent-gateway/profiles/*.yaml`, regenerated with `scripts/ci/gen-capabilities.py --write`);
   - the XR path's `MCP_READ_ONLY_TOOLS`, `SAFE_OUTPUTS` and rules text, wherever its composition renders them.
6. **Repository policy PRs** (maintainer-reviewed, protected paths), in each enforced repository:
   - first add `.github/chainguard/publisher.sts.yaml`: the factory's ServiceAccount subject, `contents: read`, `pull_requests: write`, `issues: write`;
   - **after** a canary task's PR has arrived through `output_applied`, change `agent-implementer.sts.yaml` to `pull_requests: read`;
   - then repeat for `Smana/cloud-native-ref`.
7. **v4 spec amendments** (HR12 and HR13), in Maku's v4 production-path spec:
   - D10's role table follows `enforceOutputs`, with the reviewer read-only;
   - the publisher is minted from the agents App;
   - R2 and R3 read the minter's table;
   - step 2 must not move an enforced repository to a shared-token run path.

### Live gates: runbook items for a gcp-0 session (ANR `docs/runbooks/agent-factory/26-hardening-h1-h2.md`, results recorded in the session ledger)

**H1-1 · audit is on and changes nothing.** After ANR step 1:
- the factory starts;
- one task's claim carries `agents.ogenki.io/canary`, and its brief ends with the canary line;
- its Task has `status.inputTrust` with `mode: audit`;
- its room has `output_shadowed` events after the implementer's end;
- no run, phase or merge differs from the previous session.

**H1-2 · the 50-task count.**
- Run 50 ordinary tasks across the audit repositories and their usual classes.
- Pull every `would_refuse` and `would_wait` from VictoriaLogs (`shadow verdict`) and the counter `sum by (verdict, layer, reason) (increase(agent_factory_hardening_shadow_total[…]))`.
- **Advance** when T1 to T4 `would_refuse` is at most 1% of legitimate shadowed outputs and each one is explained in the results table (task, type, layer, reason, cause, fix or accepted).
- `would_wait` is reported apart, as H3's expected approval load. T5 is reported apart and does not gate.
- **Stop and fix** on any `canary_leaked` that is not a planted test, or any refusal class above 0.5% on its own. A fix restarts the count for its output type only.
- Also record:
  - the share of tasks with `inputTrust.min` below `approved`, which is O2's expected human-path load;
  - each `withheld.comment` total.

**H2-1 · one repository enforced.** After ANR steps 5 and 6 on `Smana/ai-native-ref`:
- a task's PR, comment and label arrive through `output_applied`;
- the PR is authored by `ogenki-agents[bot]` and has the factory footer with `Agent-Output`;
- `arm` treats it as agent-authored;
- the gauge shows the implementer's `pull_requests` at `read` and the publisher at `write`;
- a probe implementer run's `gh pr create` and `gh api -X POST repos/…/pulls` both answer 403.

**H2-2 · the 20-task trace.**
- Run 20 tasks on the enforced repositories, the second added after the first passes 10.
- **Advance** when every PR, comment and label of those tasks is traced to an `output_applied`, meaning `agent_factory_hardening_untraced_total` stays at 0.
- Also record p95 of `agent_factory_hardening_output_seconds` per type (AC7 evidence, not a gate) and the merge rate against the previous 30 tasks (AC8 evidence).
- **Rollback rule:** any untraced write or a stuck task means restoring the role policy, then removing the repository from `enforceOutputs`, in that order.

## Self-review

- **H1 row coverage:**
  - `mode: audit` on the reference repositories: Task 2, ANR step 1.
  - "the applier shadows every write": Tasks 3, 4, 6 and 8, by observation (HR1).
  - trust levels and withheld counts recorded, not applied: Task 7 (HR8).
  - gateway A2 and A3 in audit: ANR step 2.
  - T5 in audit: Tasks 2 and 9 plus ANR step 2 (HR9).
  - the gate: 50 tasks at most 1% false refusals, each explained, in runbook H1-2, measured by Task 8's records and metrics (HR3).
- **H2 row coverage:**
  - `pr`, `comment`, `labels` and `issue` through the applier: Tasks 11 to 14, 16 and 17.
  - sandbox roles lose `pull_requests: write`: ANR step 6, made safe by Tasks 18 and 19, and evidenced by Task 20.
  - the gate: 20 tasks with every PR, comment and label traced: Task 19's tracer and runbook H2-2.
- **Carried from H0's exit:**
  - the off-only refusal lifted for audit: Task 2;
  - `status.inputTrust` (B17): Task 7;
  - the canary: Task 7;
  - the applier: Task 13;
  - B5's bundle store stays open for H3.
- **Acceptance slices:**
  - AC2 for non-code outputs: Task 19;
  - AC4's mechanism (canary planted and checked): Tasks 7, 8 and 13;
  - AC6's H1 half: runbook H1-2;
  - AC7 measured: Task 13;
  - AC9 unaffected: readiness stays false, asserted in Tasks 2 and 21.
- **v4 interactions as rulings:** D10 (HR12), D12 (HR11), D5 and step 2 (HR13), D4/GHES (HR20).
- **RunLore knowledge base phase 3:** this plan gives it `status.inputTrust` and `Checker.CheckText` (HR21). Its own gate still needs the drills (H4 and H5).
- **Risks, each with a stop rule:**
  - fence ownership loss (HR6);
  - policy-before-config ordering (ANR step 6);
  - a shared-token run path (HR13);
  - an empty label allowlist inflating H1's count (HR5);
  - double writes on crash (HR16).

