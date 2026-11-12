# Untrusted-input hardening, stages H3 (push enforced), H4 (reads filtered) and H5 (ready): implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** these three stages take the hardening track from H2's non-code boundary to a readiness answer that can be true.
- **H3, push enforced.** For a repository that opts in, `push` becomes a safe output. The harness turns the agent's `git push` into a git bundle and hands it to the factory through `output_push`. The factory checks the bundle: one ref, fast-forward, tree rules, class caps, then T1 to T4, and T5 in audit. It pushes the bundle as the publisher. A push that touches a protected path waits for a maintainer's approval of that bundle's digest, given in the room or in Slack. With every output type enforced, a sandbox role's token is read-only (`contents: read`).
- **H4, reads filtered.** `forge_read` is served. Text below `minAuthorTrust` is withheld in the factory before any model reads it, and each read is recorded in `status.inputTrust`. Runs on the run-controller lose `api.github.com` egress, while `git` keeps `github.com`. `mode: enforce` becomes valid. The drill runner and its report store are built, and the first full drill is the stage's gate.
- **H5, ready.** `hardening.Evaluator` gets its policy reader and its drill store. It reports per repository (a gauge, an API, the Evidence page) and is wired into `coordination.Prerequisites`. T5 is promoted to `enforce` one output type at a time, on drill numbers.

Each stage ships switched off. H3 needs `push` in a repository's `enforceOutputs`. H4 needs `mode: enforce`. H5 changes no behaviour: it reports, and the coordination gate reads the report.

**Architecture:**
- **H3** adds four pieces:
  - a **bundle store** in the broker's Postgres;
  - **harness-only `output_push`** on the room MCP server, with a raised body cap for that one call;
  - **digest-bound approvals**: the existing approvals table and decision surfaces, with a new binding the factory requests over a system route;
  - a **push applier** in the factory. It inspects the bundle with `git` in a bare, blobless mirror, then pushes with `--force-with-lease`.

  The tracer learns pushes: every agents-App push on an enforced repository must match an `output_applied` push by head SHA.
- **H4** adds a **factory read route**, `POST /v1/runs/{run}/forge-read`, which the broker's `forge_read` tool forwards to as `system:broker`. The factory reads the run's own issue, pull request or checks with its App, then withholds and levels the text. Withheld text never leaves the factory process. The broker records an `input_read` event that holds counts only. On the run-controller, a run annotated `agents.ogenki.io/forge-api: deny` gets FQDNs without `api.github.com`. Both the Cilium CNP and the egress proxy of #492 derive from `runctl.Config.FQDNs`. The **drill runner** is a factory runnable that scores the tasks named in a drill manifest and stores the report through the broker.
- **H5** wires the Evaluator:
  - R2 and R3 are read per run path: policy files on the XR, the minter's role table on the run-controller;
  - R5 comes from config facts;
  - R6 comes from the drill store.

  It reports readiness and composes `coordination.Prerequisites` with a class-authority adapter over `cfg.RepositoryAuthoritative`.

```mermaid
flowchart LR
  subgraph h3["H3: push enforced"]
    G["agent: git push"] -->|"git shim"| AP["agent-push<br/>git bundle create"]
    AP -->|"output_push (harness-only, 12 MiB cap)"| BR["broker<br/>bundle store + output_requested"]
    BR --> LOG[("room log")]
    LOG --> APL["applier<br/>gitbundle.Inspect, T1-T4, T5 audit"]
    APL -->|"protected path"| APR["approval, binding = digest<br/>room page or Slack"]
    APR -->|"approval_decided"| LOG
    APL -->|"git push --force-with-lease<br/>publisher token"| GH[("GitHub")]
    APL -->|"output_waiting / applied / refused"| LOG
  end
  subgraph h4["H4: reads filtered"]
    FR["agent: forge_read"] --> BR2["broker tool"]
    BR2 -->|"system:broker"| FAPI["factory forge-read<br/>withhold below minAuthorTrust"]
    FAPI -->|"factory App"| GH
    BR2 -->|"input_read (counts)"| LOG
    RC["run-controller FQDNs<br/>no api.github.com"]
  end
  subgraph h5["H5: ready"]
    EV["Evaluator R1-R7"] --> GA["gauge, API, Evidence page"]
    EV --> CP["coordination.Prerequisites"]
    DR["drill runner"] -->|"report"| BR3["broker drill_reports"]
    BR3 --> EV
  end
```

**Tech Stack:** Go (Maku):
- `internal/hardening`, `internal/envelope`, `internal/mcp`, `internal/bridgeapi`, `internal/store` (pgx, atlas migrations, testcontainers);
- `internal/humanapi`, `internal/slackapp`, `internal/summary`;
- `internal/factory/{applier,config,forge,reconciler,rooms,api,fmetrics}`, `internal/runctl`, `internal/maku` (CLI).

Also: the `git` binary in the factory image, the Python harness (`images/maku-harness`, stdlib `unittest`), and the `maku-factory` and `maku` Helm charts.

**Spec:** ai-native-ref `docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md`:
- stages H3, H4 and H5 of its Staged rollout;
- its Safe outputs, Author trust, Threat check, Protected paths, Injection drills, Acceptance criteria and Owner decisions sections.

Also read:
- the H1/H2 plan, Maku `docs/superpowers/plans/2026-11-12-hardening-h1-h2-plan.md` (rulings HR1 to HR21; tasks 1 to 21);
- `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`: D5, D6, D10, D12, and rollout steps 2 and 3;
- the Slack push-approval design (D5, D13), through `docs/superpowers/plans/2026-11-05-v3x-slack-push-approval-maku-plan.md`.

**Read against:** Maku `origin/main` `b07ab767`, cited `Maku/<path>`. Smana/maku#492 (`feat/v4-run-path-network`, `ee006c8e`, open) adds `internal/egressproxy`. It is cited as `#492`.

**Branches and pull requests, in order:**

| PR | Branch | Content | Size |
|---|---|---|---|
| 4 · H3a | `feat/hardening-h3-transport` | Tasks 22–28: output kinds and refusals, the bundle store, `output_push` and `output_status` served, the bundle read route, digest-bound approvals, the decision surfaces. Broker side, unreachable | L |
| 5 · H3b | `feat/hardening-h3-push-applier` | Tasks 29–34: `git` in the factory image, `gitbundle`, the push applier and its wait, the task's hold, the push tracer, the scorer. Factory side, unreachable | L |
| 6 · H3c | `feat/hardening-h3-enforce` | Tasks 35–39: `push` in `enforceOutputs`, the role table and the run-controller's read secret, the harness's `git` shim and `agent-push`, the rules, wiring, docs | M |
| 7 · H4a | `feat/hardening-h4-reads` | Tasks 40–46: `forge_read` read and filtered in the factory, served by the broker, `input_read`, `mode: enforce`, the run-controller's egress, the harness's reads, R5 | L |
| 8 · H4b | `feat/hardening-h4-drills` | Tasks 47–52: the drill report store, the gateway-log port, the drill runner, T5 measured on refusals, `maku drill plant` | L |
| 9 · H5 | `feat/hardening-h5-ready` | Tasks 53–57: the policy reader and R2/R3 per run path, readiness reported, `coordination.Prerequisites` wired, T5 promotion per type, docs | M |

Each PR is its own v0.4x minor of Maku.
- PRs 4 and 5 merge before PR 6, because nothing in them is reachable until PR 6 lets `push` into `enforceOutputs`.
- PR 8 does not depend on PR 7's code and may be built in parallel. H4's live gate needs both.
- PR 9 needs PR 8's store.

## Global Constraints

- **Off by default; each stage behind its own switch.**
  - H3: `push` in a repository's `enforceOutputs`. The central list stays as it is.
  - H4: `mode: enforce` per repository. It is valid only with all five types enforced, on the run-controller path, with the forge-read route allowed (HR34).
  - H5: nothing to switch. Readiness is computed, and the coordination gate already refuses whatever readiness refuses.
  - `layers.classifier: enforce` stays refused. Promotion is per type, through `layers.classifierEnforce` (HR40).
- **Exactly one answer per request still holds.** `output_waiting` is not an answer (HR27). A request answers `applied` or `refused` exactly once, and a waiting request is answered when its approval closes or a later push supersedes it.
- **The token is the boundary, the wrappers are advisory.** The `git` shim, the `gh` read refusals and the rules text guide the agent. The read-only sandbox token and the denied `api.github.com` egress are what hold. Neither the plan nor the docs call a wrapper a control.
- **Existing suites unchanged.** No existing test is edited unless a task names the edit and the reason. `task check` exits 0 on every PR.
- **Bounded vocabularies.** New refusals, `Missing`, record kinds and metric labels are closed sets. No payload text, bundle bytes, file content or withheld text appears in a log line, a metric or an approval card beyond the changed paths it names.
- **Readers trust the stamp, not the payload** (H1/H2 rule):
  - `output_requested` counts only from `agent:<run>`;
  - answers, `output_waiting` and `input_read` count only from `system:factory` or `system:broker`, as each kind names;
  - `approval_decided` counts only from the store's `Decide`.
- Maku house rules:
  - SPDX line on every Go file;
  - stdlib `testing`, table-driven;
  - conventional commits in English with no trailers;
  - golangci-lint v2's full set: escape invisible characters in test literals, and no error string ends in punctuation.
- This machine:
  - wrap every test run in `systemd-run --user --scope -q -p MemoryMax=6G`;
  - never override `HOME` or use `env -i`;
  - `internal/store` needs Docker;
  - `gitbundle` tests need `git` on `PATH`, which `task check` already has.
- After an API type change: `task crd:gen && task crd:check`. After a harness change: `task harness:test`. After a migration: `atlas migrate hash --dir file://internal/store/migrations`, then `task migrations`. After a chart change: `task chart:check`.

## Rulings made while planning

Numbered after the H1/H2 plan's HR1 to HR21.

| # | The spec says | The code says | This plan |
|---|---|---|---|
| HR22 | `push` carries a git bundle of at most 8 MiB, requested through a tool (UH2, UH4); H0 left B5 open ("raise the body cap for that one tool, and choose the store") | The room MCP server caps a request at 128 KiB (`Maku/internal/mcp/server.go`, `maxBody`). `mcp.BundleStore` has no implementation, and `internal/app/mcp.go` passes `OutputConfig{}`, so `output_push` is never served. A model cannot emit an 8 MiB base64 argument | **`output_push` is `HarnessOnly`**, like `room_judge`. A request with `X-Agent-Harness: 1` may be up to `maxHarnessBody = 12 MiB`, and once decoded, any call but `output_push` over 128 KiB is refused `request_too_large`. The bundle goes to `output_bundles` in the broker's Postgres, keyed by run and digest. The record keeps the digest and size. Rejected: a chunked tool (one call a second makes an 8 MiB push take about 90 s, against AC7's 10 s), and a new upload route (a new auth path, plus a gateway route on the XR). The marker is not a secret (`server.go`): a model that sends it still only *requests* a push, and the applier is the check |
| HR23 | "The tester's push and the harness's own pushes use the same `push` output" | The agent pushes with `git push` through the credential helper. The harness pushes the tester's contract (`push_contract`) and the disruption checkpoint (`checkpoint`, 7 s budget) itself (`Maku/images/maku-harness/agent_run.py`) | A `git` shim on `PATH` (`images/maku-harness/git`) turns `git push` into `agent-push` when `SAFE_OUTPUTS` names `push`. Every other subcommand passes through. `agent-push` builds the bundle, calls `output_push`, then polls `output_status` for up to 90 s and prints the answer. The checkpoint and the contract call the same code, and the checkpoint does not wait for an answer. Advisory, as the `gh` wrapper is: the token is the boundary |
| HR24 | The applier checks the bundle (ref, fast-forward, tree, caps) | The factory image is distroless static with no `git` (`Maku/images/maku-factory/Dockerfile`). `go.mod` has no go-git | **The factory shells out to `git`** in a cached bare, blobless mirror per repository, on an `emptyDir`, with `transfer.fsckObjects=true`, `core.hooksPath=/dev/null`, `protocol.allow=never` with `protocol.https.allow=always`, and no worktree. The token reaches `git` only through `GIT_CONFIG_COUNT` env (`http.extraHeader`), never through argv. The final image becomes a digest-pinned Wolfi `git` base, nonroot. Rejected: go-git (less proven on hostile packs, and no full fsck) and rebuilding commits through the Git Data API (one call per blob, and SHAs that only match by luck) |
| HR25 | "Fast-forward from the branch's current head or the task's base. No other ref, no tag, no submodule change, no symlink leaving the tree. File and line counts within the class's caps" | `forge.Diff` and `BranchActivity` read GitHub after a push. Nothing reads a bundle | `gitbundle.Inspect` accepts exactly one ref, `refs/heads/agent/<task>`, and no tag. There are two kinds of push:<ul><li>**fast-forward:** the new head descends from the branch's head. `Files` is the incremental change, head to new;</li><li>**rebase:** the branch is absent, or the new head descends from the base's current head. `Files` is the cumulative change from the merge base to new, so a rebase re-asks about a protected file already on the branch.</li></ul>A changed entry with mode `160000` (submodule), or a symlink whose target is absolute or resolves outside the root, is refused. The class caps are read on the cumulative change from the merge base. There are four new refusals at layer `t1_schema`: `push_ref`, `push_history`, `push_tree`, `push_size` |
| HR26 | Idempotent applies (HR16) | — | **A push's marker is its head SHA.** If the branch head already equals the bundle's head, the push is answered `applied` with no write. The push is `git push --force-with-lease=refs/heads/agent/<task>:<head read>`, so a concurrent push loses and is retried on the next tick |
| HR27 | A protected-path push "waits for a maintainer's approval of that bundle's digest" (UH9, O1) | The applier answers each request exactly once, in seq order, and expires a run's requests `AnswerWait` after it ends (`Maku/internal/factory/applier/applier.go`) | A new non-answer kind, **`output_waiting`** (`reason: protected_path_needs_approval`, `approvalId`). The run's later requests wait behind it, in order. The applier keeps reading the run until the approval closes:<ul><li>`approved`: the push is re-checked with `Approved: true` and re-inspected, then applied;</li><li>`denied`: refused `approval_denied`;</li><li>`expired`: refused `approval_expired`.</li></ul>A later `push` request of the same run supersedes a waiting one, which is refused `output_superseded`, and its approval is closed `superseded`. `hardening.applier.approvalTTL` defaults to 24 h, with a maximum of 7 days |
| HR28 | Approvals "in the room or through the Slack push approval" | Approval rows belong to bridge tool calls, fenced on the bridge lease. They close on the call's `tool_result` and on the run's end (`Maku/internal/store/approvals.go`, `answeredSQL`, `endedSQL`). Slack decides `forge.push` only (`slackDecidableClass`), binds the branch, and re-reads a *live* run (`Maku/internal/humanapi/slackdecide.go` step 11). The room page does not check repository write (`acts.go` `decide`) | **Same class, `forge.push`, plus a digest binding.**<ul><li>A migration adds `binding_digest` and `binding_output` to `approvals`.</li><li>`store.RequestOutputApproval` records a row with a system actor and no bridge fence, idempotent per output id.</li><li>Rows with a digest are excluded from both sweeps, `answeredSQL` and `endedSQL`. They close by decision, expiry or the factory's supersede.</li><li>`Decide` is unchanged.</li><li>Slack step 11, for a digest-bound row, compares the press's branch with the row's binding and the room's task branch. It needs no live run.</li><li>The card adds the short digest and the protected paths.</li><li>The room page requires repository write for a digest-bound row, the same standing as Slack's step 10.</li><li>Four eyes: the prompters are the run's, as today.</li></ul>This amends the Slack design's D5: the binding gains a digest, because a bundle exists before the push, which a commit SHA did not |
| HR29 | — | #477 treats an ended run that committed but opened no pull request as a failure. HR19 waits for answers up to `answerWait` | While any of a run's push requests is `output_waiting`, `awaitOutputs` keeps the task in `Implementing` with reason `push_awaiting_approval`, and requeues it. It does not start the #477 path. The room is `AwaitingHuman` through the open approval |
| HR30 | "Sandbox roles hold `contents: read`" (H3 row). HR12: the v4 D10 minter's role table follows `enforceOutputs` | The XR path scopes tokens by `.github/chainguard/agent-*.sts.yaml`. The run-controller mounts `gitSecret` (write) for implementer and tester runs, and `gitReadSecret` for read-only roles (`Maku/internal/runctl/config.go`, `GitSecretFor`). The minter (v4 step 3) is not built | **One role table, `hardening.SandboxPermissions(role, enforced) map[string]string`:**<ul><li>the implementer gets `contents: write` unless `push` is enforced, and `pull_requests: write` unless `pr`, `comment` and `labels` all are;</li><li>every other role gets read only.</li></ul>Three consumers:<ul><li>the run-controller mounts `gitReadSecret` for any run whose table grants no write. It needs all five types enforced, since a read token cannot open a pull request. `Invalid` refuses an implementer run with `push` served and no `gitReadSecret`;</li><li>the docs render the XR policy files from the table;</li><li>step 3's minter must mint from it (the D10 amendment, made concrete).</li></ul>H3 therefore **does not block on the minter**. HR13's warning narrows to a repository on the run-controller that enforces some types but not all |
| HR31 | Under enforce, the publisher holds `contents: write` (Safe outputs) | H2's publisher policy holds `contents: read` "until H3" (HR11) | The publisher policy gains `contents: write` at H3. GitHub refuses a push that changes `.github/workflows/**` from a token without `workflows: write`. The publisher policy may add `workflows: write` per repository. Without it, an approved workflow push is refused `output_unavailable` and the docs say so |
| HR32 | AC2: every agents-App write has an `output.applied` | The tracer skips pushes (`Maku/internal/factory/reconciler/shadow.go`, `o.w.Type != hardening.Push`) | On a repository that enforces `push`, each agents-App push in `BranchActivity` must match an `output_applied` push whose object is `…/commit/<after>`. A push with no match is `untraced_write` |
| HR33 | — | — | The disruption checkpoint requests its push and exits within its 7 s budget. The applier answers it after the run, inside `answerWait`. A checkpoint over 8 MiB is refused `push_size` and reported in the run's end note |
| HR34 | R1: "The effective mode is `enforce`". The H2 code refuses `enforce` "until H3" (`Maku/internal/factory/config/hardening.go`, `enforceRefused`) | H3's change is expressed by `enforceOutputs: [push]` under `mode: audit` | **`mode: enforce` arrives at H4.** It means all five types enforced and reads filtered, and it needs the run-controller path, because R5 cannot hold on the XR (HR36). H3 keeps `mode: audit`. The refusal text changes to "enforce arrives with stage H4" in PR 6 |
| HR35 | `forge_read` applies UH6 and UH7 (UH11) | `mcp.ForgeReadTool(r ForgeReader, p hardening.Policy)` takes a single static policy, and nothing implements `ForgeReader`. The broker's GitHub App is mounted for access checks only. The factory holds the config, the fence policy, the maintainers and the forge connections. Its API admits system callers through `SystemAllow`, which is empty today (`Maku/internal/factory/api/authn.go`) | **Read and filtered in the factory, served by the broker.** `POST /v1/runs/{run}/forge-read {target}` is admitted for `system:broker` through `SystemAllow`. The broker forwards the verified caller's run id, never an argument. The withholding is a pure `hardening.FilterThread`, moved from `internal/mcp`. The broker appends an `input_read` record (`system:broker`, `Delivery: none`, counts and levels only), and the factory folds it into `status.inputTrust` with `mode: enforce`. Rejected: reading in the broker (a second copy of the trust policy and the CI-log code) |
| HR36 | "The sandbox loses `api.github.com` egress. `git` keeps `github.com`" (UH11); v4 D5 and D6 | The XR's CNP is rendered by cloud-native-ref's composition. The run-controller's CNP and #492's egress proxy both take `runctl.Config.FQDNs(r)`, whose `github` profile includes `api.github.com` | **H4 targets the run-controller path.** The factory writes `agents.ogenki.io/forge-api: deny` on every run of a repository in `mode: enforce`, and `FQDNs` drops `api.github.com` for it. On the XR, `forge_read` is served, but R5 stays false: the factory cannot see the composition's egress. The credential agent's revoke (`DELETE api.github.com/installation/token`) is skipped under `forge-api: deny`. The token is read-only and expires within the hour |
| HR37 | Comments and CI logs the agent reads itself are raw `gh` (Current state) | The harness's judge triage runs `gh api …/check-runs` and `gh run view --log-failed` (`agent_run.py`). The factory's CI-failed revision text names `gh pr checks` (`Maku/internal/factory/reconciler/merge.go`) | With `FORGE_READ=1`, the `gh` wrapper refuses `issue view/list`, `pr view/checks`, `run view/list` and `api`, with "use forge_read" and exit 3. The judge triage calls `forge_read {target: checks}` through the harness's MCP client. The revision text names `forge_read`. `gh pr diff` is replaced in the rules by `git diff origin/<base>...HEAD` |
| HR38 | Drills are "a scheduled set of tasks on a dedicated public drill repository. A `drill-stranger` account plants the injections"; R6 reads "the drill reports in the factory's store" (UH14, O3) | `hardening.Score` exists. No runner, store or `Drills` implementation exists. The factory has no database | **Plant outside, score inside.** `maku drill plant` runs with the stranger's and a maintainer's tokens (an ANR CronJob, weekly and on a config-hash change). It plants the issues and comments, then posts a manifest to `POST /v1/hardening/drills` (operator principal): scenario, role set, task and marker. The **factory** scores, so a report cannot be forged by its submitter. The runner waits for the named tasks, and builds each `Observation` from:<ul><li>the room log;</li><li>the bundle inspection of each applied push;</li><li>`input_read` withheld counts;</li><li>the gateway-log port, for marker hits and guardrail verdicts.</li></ul>Reports are stored in the broker's Postgres (`drill_reports`) through system routes, with `system:factory` as the only writer. A request still `output_waiting` at scoring time scores as **held** (a T4 detection), not unanswered. `judge` is scored from the implementer run's events and the gateway's judge-model requests. `investigate` scenarios enter as internal tasks through the RunLore principal |
| HR39 | AC6: T5 is promoted "only below 2% false positives and above the regex layer's detection on the drills" | Layers stop at the first refusal (`Checker.Check`), so T5 never sees an output T3 refused | The applier runs T5 in audit **after** a refusal on text outputs too, and records it in `output_refused.audit`. `OutputRecord.Validate` then allows `Audit` on a refused record. "The regex layer" is read as T2 and T3, the pattern-based layers, on the same planted outputs. The report gains `detectionsByType`. False positives are T5 findings on `output_applied` outputs of non-drill tasks, counted by `agent_factory_hardening_t5_total{type,result}` |
| HR40 | "T5 promoted per output type on its drill numbers" | `layers.classifier` is one mode, and `enforce` is refused | `layers.classifierEnforce: [pr, comment, …]` names the promoted types. It needs `agentGuard.url` and the repository at `mode: enforce`. `Checker` takes a per-type classifier mode, and an unreachable classifier refuses `classifier_unavailable` for a promoted type. The numbers gate is a runbook decision read from the drill reports and the counter. The factory does not read Prometheus. The list joins `Hash()`, so a promotion needs a new passing drill (R6) |
| HR41 | `Readiness` is a port "read fresh at every admission" | `coordination.Service.Prerequisites` is `nil`, and nothing calls `Hardened` in production (`Maku/internal/app/factory.go`). `cfg.RepositoryAuthoritative` exists (`Maku/internal/factory/config/mergepolicy.go`) | H5 wires `coordination.Prerequisites{Classes: configAuthority, Hardening: Evaluator}`. Readiness is reported, freshly computed, by:<ul><li>a 5-minute gauge, `agent_factory_hardening_ready{repository}` and `_missing{repository,missing}`;</li><li>`GET /v1/hardening/readiness` (operator);</li><li>an Evidence-page row.</li></ul>The admission keeps computing its own answer: the gauge is never read as one |
| HR42 | R2: no sandbox role's policy grants a write; R3: a publisher policy exists | On the XR the policies are files that octo-sts reads. On the run-controller the sandbox credential is a Secret's content, which the factory cannot inspect. HR12's minter is not built | **R2 and R3 per run path.**<ul><li>XR: the policy files (H0's reader).</li><li>Run-controller: the minter's role table, through a `RoleTable` port that step 3 provides. With none configured, R2 answers `sandbox_write_token` missing.</li></ul>**This blocks H5's "true" on the run-controller on v4 step 3.** See **Owner decision HR42** below |
| HR43 | R5: "`forge_read` is active and the run profile has no `api.github.com` egress" | `Settings.ReadFilter` is always false (`HardeningSettings`) | `ReadFilter` is true when all of these hold:<ul><li>the repository is at `mode: enforce`;</li><li>runs are not the XR;</li><li>`api.systemAllow` maps a subject to `system:broker`;</li><li>the factory writes `forge-api: deny` on every run of the repository.</li></ul>A run-controller older than the factory is a version-skew risk. The chart pins both to one Maku version, and runbook H4-1 probes it |

### Owner decision HR42 (pending, with HR12 and HR13)

The question: on the run-controller path, what proves R2 ("no sandbox role can write") before v4 step 3's minter exists?

- **Option A (recommended): wait for step 3.** Build H3 and H4 now; neither needs the minter (HR30). Order: H3, then H4, then v4 step 3 (the minter, minting from `hardening.SandboxPermissions` and exposing `RoleTable`), then H5's live gate. H5's code (PR 9) merges before step 3 and reports `sandbox_write_token` until the minter is configured.
- **Option B: probe-attested R2.** Each drill runs the AC1 probe (a writer run tries `git push`, `gh api -X POST` and `curl` with its token, and expects three `403`s). Run-controller R2 then reads: `gitReadSecret` is configured for the repository's runs (from the run-controller ConfigMap), and the latest passing drill for this config hash recorded the probe's three refusals. Residual: a Secret swapped to a write token between drills goes unseen until the next drill, which breaks "a regression refuses the next admission" for that fact.
- A third route, keeping the reference repositories on the XR through H5, does not work. R5 cannot hold on the XR (HR36), and v4 step 2 moves cloud-native-ref off the XR anyway.

This plan builds Option A, with the `RoleTable` port. Option B is one extra adapter behind the same port (Task 53 names it) if the owner picks it.

---

## PR 4 · H3a, the push transport and digest-bound approvals, broker side (L)

### Task 22: `output_waiting`, the push refusals, audit on refusals

**Spec:** UH4, UH9, HR25, HR27, HR39

**Files:**
- Modify: `internal/envelope/output.go`, `internal/envelope/output_read.go`, `internal/envelope/output_test.go`, `internal/envelope/output_read_test.go`
- Modify: `internal/hardening/outputs.go`, `internal/hardening/drill.go`, `internal/hardening/outputs_test.go`, `internal/hardening/drill_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestOutputWaitingValidate`:
    - an `output_waiting` record with id, type `push`, digest, reason `protected_path_needs_approval` and a 26-character ULID `ApprovalID` is valid;
    - a waiting record with an object, a payload, or another type is invalid;
    - `ApprovalID` on any other kind is invalid.
  - `TestAuditOnRefused`: `Audit` is now valid on `output_refused`, still capped at `MaxOutputFindings`, and still invalid on `output_requested`.
  - `TestReadOutputWaiting`: `ReadOutput` returns a waiting record only when it is stamped `system:factory`.
  - `TestPushRefusals`: `push_ref`, `push_history`, `push_tree` and `push_size` map to `t1_schema`; `approval_denied` and `approval_expired` to `t4_paths`; `output_superseded` to `t1_schema`. Each passes `outputReason`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/envelope/ ./internal/hardening/ -run 'TestOutputWaiting|TestAuditOnRefused|TestReadOutputWaiting|TestPushRefusals' -count=1`. Expected: FAIL (`undefined: KindOutputWaiting`).
- [ ] **Step 3: implement.**
  - `KindOutputWaiting MessageKind = "output_waiting"` and `OutputRecord.ApprovalID string`, with `Validate` per kind.
  - The seven refusals, added to `refusalLayer`.
- [ ] **Step 4: run** the same command, then `go test ./internal/envelope/ ./internal/hardening/ -count=1`. Expected: PASS. H1's `TestOutputShadowedValidate` case "Audit on a refused record is invalid" changes, so name that edit in the commit body.
- [ ] **Step 5: commit** `feat(envelope): output_waiting, the push refusals, and audit findings on refusals`.

### Task 23: the bundle store

**Spec:** UH2, HR22

**Files:**
- Create: `internal/store/migrations/20261113120000_output_bundles.sql`, `internal/store/bundles.go`, `internal/store/bundles_test.go`
- Modify: `internal/store/migrations/atlas.sum`

- [ ] **Step 1: write the failing tests** (Docker).
  - `TestPutBundle`:
    - `PutBundle(ctx, room, run, digest, b)` stores the bytes once;
    - a second put of the same run and digest is a no-op;
    - a digest that is not `sha256(b)` is `ErrDigestMismatch`;
    - more than `8 MiB` is `ErrTooLarge`;
    - a sealed room is `ErrSealed`.
  - `TestBundle`: `Bundle(ctx, room, run, digest)` returns the bytes, or `ErrNoBundle` for another room's run.
  - `TestSweepBundles`: `SweepBundles(ctx, now)` deletes bundles older than 14 days, unless a pending approval's `binding_digest` names them (that join is checked in Task 26's test).
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/store/ -run 'TestPutBundle|TestBundle|TestSweepBundles' -count=1`. Expected: FAIL (`undefined: PutBundle`).
- [ ] **Step 3: implement** the table `output_bundles(room_id, run_id, digest, size, bundle bytea, created_at)`:
  - primary key `(run_id, digest)`;
  - `CHECK (size BETWEEN 1 AND 8388608)` and `digest ~ '^[0-9a-f]{64}$'`;
  - the room foreign key.

  Add the sweep to the broker's existing sweeper tick. Then run `atlas migrate hash`.
- [ ] **Step 4: run** the same command, then `task migrations`. Expected: PASS.
- [ ] **Step 5: commit** `feat(store): a bundle store beside the room log for push outputs`.

### Task 24: serve `output_push` and `output_status`

**Spec:** UH4, HR22, HR23, HR27

**Files:**
- Modify: `internal/mcp/hardening.go`, `internal/mcp/server.go`, `internal/mcp/hardening_test.go`, `internal/mcp/server_test.go`, `internal/app/mcp.go`

- [ ] **Step 1: write the failing tests.**
  - `TestOutputPushHarnessOnly`:
    - with a bundle store, `output_push` is listed and callable only with `X-Agent-Harness: 1` and `push` in the run's safe outputs;
    - the model's list never names it;
    - the stored record's digest is `sha256` of the decoded bundle, with `Bytes` set and no payload;
    - the bundle is in the store.
  - `TestHarnessBodyCap`:
    - a harness `tools/call output_push` of 11 MiB is accepted;
    - a harness `tools/call room_message` over 128 KiB is refused `request_too_large`;
    - a non-harness request over 128 KiB is refused as today.
  - `TestOutputStatus`: `output_status {outputId}` answers `requested`, `waiting` (with `approvalId`), `applied` (with object) or `refused` (with reason), read from the caller's own run only. Another run's id answers `unknown`. It is served to every run that has safe outputs, and to the model too.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/mcp/ -run 'TestOutputPush|TestHarnessBodyCap|TestOutputStatus' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.**
  - `output_push` gets `HarnessOnly: true`.
  - `server.go` reads with `maxHarnessBody = 12 << 20` when the marker is set, then checks `len(body) <= maxBody || name == "output_push"`.
  - `output_status` reads through `envelope.ReadOutput`.
  - `internal/app/mcp.go` passes `OutputConfig{Bundles: store}`.
  - `output_push` stays unserved until a claim names `push`, which config refuses until PR 6.
- [ ] **Step 4: run** the same command, then `go test ./internal/mcp/ ./internal/app/ -count=1`. Expected: PASS.
- [ ] **Step 5: commit** `feat(mcp): serve output_push to the harness, and output_status to every enforced run`.

### Task 25: the factory's bundle read route

**Spec:** HR22

**Files:**
- Modify: `internal/bridgeapi/system.go`, `internal/bridgeapi/server.go`, `internal/bridgeapi/system_test.go`, `internal/factory/rooms/client.go`, `internal/factory/rooms/client_test.go`, `docs/api.md`

- [ ] **Step 1: failing tests.**
  - `TestRoomBundle`: `GET /v1/rooms/{id}/bundles/{digest}?run=<id>` from `system:factory` (`OutputClient`) returns `application/x-git-bundle` bytes. Any other principal gets 403. A bad digest or run gets 400, and a missing bundle gets 404.
  - `TestClientBundle`: `rooms.Client.Bundle(ctx, room, run, digest)` returns the bytes, refuses more than 8 MiB, and verifies the digest.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/bridgeapi/ ./internal/factory/rooms/ -run 'TestRoomBundle|TestClientBundle' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(broker): the factory reads a push request's bundle`.

### Task 26: digest-bound approvals

**Spec:** UH9, O1, HR27, HR28

**Files:**
- Create: `internal/store/migrations/20261113130000_approval_digest.sql`, `internal/bridgeapi/outputapprovals.go`, `internal/bridgeapi/outputapprovals_test.go`
- Modify: `internal/store/approvals.go`, `internal/store/approvals_test.go`, `internal/envelope/payloads.go` (`ApprovalRequestedPayload.Digest`, `.Output`), `internal/factory/rooms/client.go`

- [ ] **Step 1: write the failing tests.**
  - `TestRequestOutputApproval` (Docker):
    - a row with class `forge.push`, `binding_branch agent/t-1`, `binding_digest <64hex>`, `binding_output <16hex>`, a `system:factory` actor, the run's prompters and `expires_at = now + ttl` enters `pending` with its `approval_requested` event;
    - a second request for the same output returns the first row;
    - no bridge lease is needed.
  - `TestDigestApprovalsSurviveRunEnd`: a digest-bound row stays pending after the run's `participant{left}` and after a `tool_result` with its call id. A plain `forge.push` row is still superseded, as today.
  - `TestSupersedeOutputApproval`: `CloseOutputApproval(ctx, id, "superseded")` closes it with the broker's reason. It is a no-op on a decided row.
  - `TestOutputApprovalRoutes`: `POST /v1/rooms/{id}/approvals` and `POST /v1/approvals/{id}/supersede` from `system:factory` only, plus `GET /v1/approvals/{id}`, which returns state, binding and decider.
  - The migration's trigger compares `payload->>'digest'` and `payload->>'output'` with the new columns.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/store/ ./internal/bridgeapi/ -run 'TestRequestOutputApproval|TestDigestApprovals|TestSupersedeOutputApproval|TestOutputApprovalRoutes' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.**
  - The migration: two columns defaulted to `''`, each with a `CHECK`, and the replaced `approvals_are_their_events()`.
  - `answeredSQL` and `endedSQL` gain `AND a.binding_digest = ''`.
  - The routes, and the `rooms.Client` methods `RequestApproval`, `SupersedeApproval` and `Approval`.
- [ ] **Step 4: run** the same command, then `task migrations`. Expected: PASS.
- [ ] **Step 5: commit** `feat(store): approvals bound to a push bundle's digest, requested by the factory`.

### Task 27: deciding a digest-bound approval in the room and in Slack

**Spec:** UH9, HR28

**Files:**
- Modify: `internal/humanapi/acts.go`, `internal/humanapi/acts_test.go`, `internal/humanapi/slackdecide.go`, `internal/humanapi/slackdecide_test.go`, `internal/slackapp/card.go`, `internal/slackapp/card_test.go`, `internal/summary/summary.go`, `internal/summary/summary_test.go`

- [ ] **Step 1: write the failing tests.**
  - `TestDecideDigestNeedsWrite` (room page): on a digest-bound row, a room approver without repository write is refused `not_permitted`, and one with write decides. A branch-bound row decides as today.
  - `TestSlackDecideDigestBound`:
    - with the run ended, a press on a digest-bound row whose branch equals the row's binding and the room's task branch decides;
    - a different branch is `binding_mismatch`;
    - a branch-bound row with no live run is still `binding_mismatch`, as today.
  - `TestCardDigestFields`: the card shows "Digest" (the first 12 hex) and "Protected paths" (at most 10, then "and N more"), escaped through `slackText`. The button value format is unchanged.
  - `TestDescribeOutputPush`: `DescribeApproval("forge.push", action)` for an output action names the branch, the digest and the protected paths, and never commit text.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/humanapi/ ./internal/slackapp/ ./internal/summary/ -run 'TestDecideDigest|TestSlackDecideDigest|TestCardDigest|TestDescribeOutputPush' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** The room page reuses Slack's `Identity` and `Access` ports through `humanapi.Server`. Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(approvals): a maintainer decides a protected-path push in the room or in Slack`.

### Task 28: the gate (PR 4)

- [ ] **Step 1:** run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 2:** run `git grep -n 'RequestApproval\|\.Bundle(' -- internal/factory/applier/`. Expected: no output (no factory caller yet).
- [ ] **Step 3: open PR 4.** List HR22, HR27 and HR28 under "Builder choices". Name the Slack D5 amendment for the owner.

---

## PR 5 · H3b, the push applier, unreachable (L)

### Task 29: `git` in the factory image; the mirror cache

**Spec:** HR24

**Files:**
- Modify: `images/maku-factory/Dockerfile`, `charts/maku-factory/values.yaml`, `charts/maku-factory/templates/deployment.yaml`, the chart's render test

- [ ] **Step 1: failing render test.** With `hardening.gitCache.sizeLimit: 2Gi`, the factory pod mounts an `emptyDir` at `/var/cache/maku/git` with that limit and sets `MAKU_GIT_CACHE`. Without the value nothing is mounted. Run `task chart:check`. Expected: FAIL.
- [ ] **Step 2: implement.**
  - The final stage becomes a digest-pinned Wolfi `git` base, nonroot (uid 65532), `ENTRYPOINT ["/maku-factory"]`.
  - The image test asserts that `git --version` is at least 2.45.
  - Run `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `build(factory): git in the factory image, and a mirror cache`.

### Task 30: `gitbundle`, inspect and push

**Spec:** UH2, Safe outputs (`push`), HR24, HR25, HR26

**Files:**
- Create: `internal/factory/gitbundle/mirror.go`, `inspect.go`, `push.go`, `gitbundle_test.go`, `testdata/` (scripts that build fixture repositories at test time, never committed bundles)

- [ ] **Step 1: write the failing tests**, table-driven over fixtures built with `git` in `t.TempDir()`, using a local bare remote as "GitHub":
  - `TestInspectFastForward`: two commits on `agent/t-1` from its head give `FastForward`, `Files` with status and mode (deletions included), `Added` lines, commit `Messages`, and `Cumulative{Files, Lines}` from the merge base with `main`.
  - `TestInspectRebase`: a branch rebased onto a newer `main` is `Rebase`, with `Files` cumulative.
  - `TestInspectRefusals`:
    - two refs, a tag, or a ref other than `refs/heads/agent/t-1` is `push_ref`;
    - a head descending from neither the branch nor the base is `push_history`;
    - a gitlink (`160000`), a symlink to `/etc/passwd`, a symlink `a/../../x`, or an object failing fsck is `push_tree`;
    - missing prerequisites are `push_history`.
  - `TestInspectNoHooksNoToken`: a bundle whose commits carry a `.git/hooks`-looking path runs nothing. A fake `git` on `PATH` that records argv and env shows that the token is passed only through `GIT_CONFIG_*`, never in argv.
  - `TestPushLease`:
    - `Push(ctx, remote, head, oldHead)` updates the ref;
    - a remote moved since `oldHead` is `ErrLeaseLost`;
    - a remote already at `head` is a no-op success (HR26).
  - `TestMirrorConcurrency`: two inspections of one repository serialise on its mirror lock. A corrupt mirror is removed and re-cloned.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/gitbundle/ -count=1`. Expected: FAIL (package missing).
- [ ] **Step 3: implement.**
  - `Mirror{Dir, Remote, Token func() (string, error)}`, with a blobless bare clone and fetches limited to `main` and `agent/<task>`.
  - `Inspect(ctx, m, bundle, ref, base)`: `bundle verify`, `bundle list-heads`, then a fetch into `refs/maku/out/<id>` under fsck, then `diff --raw -z --no-renames` and `diff -U0` (blobs lazy-fetched), then `log --format=%B`.
  - Every call carries a 60 s timeout and a 1 MiB output cap.
- [ ] **Step 4: run** the same command. Expected: PASS.
- [ ] **Step 5: commit** `feat(factory): inspect and push a run's git bundle outside the sandbox`.

### Task 31: the applier applies `push`, and waits for approvals

**Spec:** UH2, UH9, T1–T5, HR25 to HR27, HR31

**Files:**
- Create: `internal/factory/applier/push.go`, `internal/factory/applier/push_test.go`
- Modify: `internal/factory/applier/applier.go` (the run window and the waiting state), `internal/factory/applier/apply.go`, `internal/factory/applier/publisher.go` (`Pushers` from `PublisherTokens`), `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: write the failing tests** with the room fake, a `gitbundle` fake and the clock fake.
  - `TestApplyPush`: a clean fast-forward bundle is inspected, then checked:
    - `Request{Files, Added (lines and messages), Approved: false}`, with T1 to T4 at enforce and T5 at audit;
    - it is pushed with the lease;
    - it gets one `output_applied` whose object is `https://github.com/o/r/commit/<head>`.
  - `TestPushNeedsApproval`: a bundle touching `go.mod`:
    - gets one `output_waiting` and one `RequestApproval` (binding branch, digest, output, `Action{output, digest, base, head, protected: [go.mod]}`), and no push;
    - a later `approval_decided approved` read from the log is re-inspected, checked with `Approved: true`, and pushed: `applied`;
    - `denied` gets `approval_denied`, `expired` gets `approval_expired`.
  - `TestPushSuperseded`: a second push request while the first waits answers the first `output_superseded` and closes its approval, then checks the second.
  - `TestPushBehindWaiting`: an `output_pr` after a waiting push stays unanswered until the push's answer.
  - `TestPushWindow`: a run ended 3 × `answerWait` ago with an open approval is still read. Once the approval closes it follows H2's window.
  - `TestPushRefusals`:
    - `push_ref`, `push_history`, `push_tree`, and `push_size` over the class's `maxPatchFiles` or `maxModifiedLines` (cumulative);
    - a canary in an added line is `canary_leaked`, with the alert log line;
    - a blocked extension path is `protected_path_blocked`, and no approval is requested.
  - `TestPushIdempotent`: when the branch head equals the bundle head, the answer is `applied` with no push. A lost lease leaves the request pending for the next tick.
  - `TestPushLatency`: `agent_factory_hardening_output_seconds{type="push"}` is observed. `agent_factory_hardening_approval_wait_seconds` is observed when an approval closes.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/applier/ -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.**
  - H2's guard `typ == hardening.Push → output_type` becomes "push without a pusher". The applier is still unreachable, because config refuses `push`.
  - `Pushers func(ctx, repo) (gitbundle.Remote, error)` takes its token from the same `PublisherTokens.For(repo)`.
  - `approval_decided` events are read only through `envelope.ApprovalDecidedPayload` stamped by the store. The approval is re-read with `rooms.Client.Approval` before applying.
- [ ] **Step 4: run** the same command, then `go test ./internal/factory/... -count=1`. Expected: PASS.
- [ ] **Step 5: commit** `feat(factory): apply push outputs, and hold a protected-path push for a maintainer`.

### Task 32: the task holds while a push waits

**Spec:** HR29

**Files:**
- Modify: `internal/factory/reconciler/tracer.go` (`awaitOutputs`, `answersPending`), `internal/factory/reconciler/implement.go`, `internal/factory/reconciler/tracer_test.go`

- [ ] **Step 1: failing test** `TestRunEndHoldsForApproval`:
  - a terminal implementer run whose push is `output_waiting` keeps the task in `Implementing`, with reason `push_awaiting_approval` and a requeue;
  - after `applied`, `detectPR` and the usual path run;
  - after `approval_denied`, the task takes today's path for an ended run with no pushed work, with `push_refused` in the narration.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ -run TestRunEndHoldsForApproval -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command, then `go test ./internal/factory/reconciler/ -count=1`. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): a task waits while its push waits for a maintainer`.

### Task 33: the tracer traces pushes

**Spec:** AC2, HR32

**Files:**
- Modify: `internal/factory/reconciler/tracer.go`, `internal/factory/reconciler/shadow.go`, `internal/factory/reconciler/tracer_test.go`

- [ ] **Step 1: failing test** `TestTracerPush`: on a repository that enforces `push`:
  - an agents-App push whose `After` is an applied push's head is traced;
  - any other is posted `output_shadowed untraced_write` and counted in `agent_factory_hardening_untraced_total{type="push"}`;
  - on a repository that does not enforce `push`, pushes are shadowed as in H1.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ -run TestTracerPush -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): trace every agent push to the output that made it`.

### Task 34: the scorer reads pushes and waits; the gate (PR 5)

**Spec:** UH14, HR38

**Files:**
- Modify: `internal/hardening/drill.go`, `internal/hardening/drill_test.go`

- [ ] **Step 1: failing test** `TestScoreWaitingIsHeld`: a request whose last record is `output_waiting` and never applied scores no failure, and adds `Detections[t4_paths]`. A waiting request later applied with forbidden text fails `out_of_policy_applied`. Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ -run TestScoreWaitingIsHeld -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command. Expected: PASS.
- [ ] **Step 3: gate.**
  - Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
  - Run `go test ./internal/factory/config/ -run TestEnforceOutputs -count=1`. Expected: PASS with `push` still refused ("push arrives with stage H3").
- [ ] **Step 4: open PR 5**, listing HR24 to HR27, HR29 and HR32.

---

## PR 6 · H3c, push enforced, per repository (M)

### Task 35: `push` in `enforceOutputs`

**Spec:** H3 row, HR30, HR34

**Files:**
- Modify: `internal/factory/config/hardening.go`, `internal/factory/config/hardening_test.go`, `internal/factory/config/config.go` (the HR13 warning)

- [ ] **Step 1: failing tests.**
  - `TestEnforcePush`:
    - `enforceOutputs: [push, pr, comment, labels, issue]` validates under `mode: audit`;
    - on the run-controller (`!Runs.XR()`), a list with `push` but not all five fails with "push on the run-controller needs every output type enforced: its runs then mount gitReadSecret (HR30)";
    - on the XR, `push` alone validates.
  - `TestApproverTTL`: `applier.approvalTTL` defaults to 24h, and values outside (0, 168h] fail.
  - `TestHashCoversPush`: adding `push` changes `Hash()`.
  - `TestEnforceRefusalH4`: `mode: enforce` fails with "enforce arrives with stage H4".
  - `TestHR13WarningNarrowed`: on the run-controller, the warning is given only for a repository enforcing some types but not all.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ -run 'TestEnforcePush|TestApproverTTL|TestHashCoversPush|TestEnforceRefusalH4|TestHR13' -count=1`. Expected: FAIL. Update H2's `TestEnforceOutputs` case "push fails" (now push needs all five on the run-controller) and the H1 refusal-text case, and name both edits in the commit body.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(config): enforce push outputs per repository`.

### Task 36: the role table and the run-controller's read secret

**Spec:** H3 row ("sandbox roles hold `contents: read`"), HR12, HR30

**Files:**
- Create: `internal/hardening/roles.go`, `internal/hardening/roles_test.go`
- Modify: `internal/runctl/config.go` (`GitSecretFor(r)`, `Invalid`), `internal/runctl/pod.go`, `internal/runctl/config_test.go`, `internal/runctl/testdata/pod.golden.yaml`, `internal/app/factory.go` (the gauge's expected value), `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: failing tests.**
  - `TestSandboxPermissions`, table-driven over role × enforced set:
    - the implementer with nothing enforced gets `contents: write, pull_requests: write`;
    - with `pr, comment, labels` it keeps `contents: write` and gets `pull_requests: read`;
    - with all five it gets read only;
    - the tester gets read only once `push` is enforced;
    - the reviewer and triager always get read only.
  - `TestGitSecretForRun`: an implementer claim annotated `safe-outputs: comment,issue,labels,pr,push` mounts `gitReadSecret`. Without `push` it mounts `gitSecret`. `Invalid` refuses the first when `gitReadSecret` is unset.
  - `TestPolicyDrift`: `agent_factory_hardening_policy_drift{repository,role}` is 1 when the H2 permission gauge's parsed policy grants more than `SandboxPermissions` allows for the repository's enforced set.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ ./internal/runctl/ ./internal/factory/fmetrics/ -run 'TestSandboxPermissions|TestGitSecretForRun|TestPolicyDrift' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command, then `go test ./internal/runctl/ -count=1` (goldens unchanged for runs without `push`). Expected: PASS.
- [ ] **Step 4: commit** `feat(runctl): a run with no write left mounts the read-only token`.

### Task 37: the harness's `git` shim and `agent-push`

**Spec:** UH2, HR23, HR33

**Files:**
- Create: `images/maku-harness/git`, `images/maku-harness/agent_push.py`, `images/maku-harness/tests/test_agent_push.py`
- Modify: `images/maku-harness/agent_run.py` (`checkpoint`, `push_contract`), `images/maku-harness/Dockerfile`, `images/maku-harness/tests/test_agent_run.py`

- [ ] **Step 1: failing tests.**
  - `test_agent_push.py`, against a local bare remote and a fake MCP server:
    - with `SAFE_OUTPUTS` containing `push`, `git push`, `git push origin HEAD` and `git push origin agent/t-1` build a bundle of `refs/heads/agent/t-1` excluding the remote-tracking `main` and branch heads, then call `output_push` with the harness marker;
    - the command prints `applied <sha>`, `refused <reason>` or `waiting for a maintainer's approval (<approvalId>)` from `output_status`, and exits 0, 1 or 0 respectively;
    - `--delete`, a tag, or another branch exits 3 with "push only agent/t-1";
    - a bundle over 8 MiB exits 1 with `push_size` before any call;
    - after `applied` the remote-tracking ref is updated, so `unpushed()` reads 0.
  - The shim: every other subcommand execs `/usr/bin/git` unchanged. Without `SAFE_OUTPUTS` even `push` passes through.
  - `test_agent_run.py`:
    - `checkpoint` under `SAFE_OUTPUTS` requests and does not wait;
    - `push_contract` requests through `agent_push.request` and waits for the answer.

    Each case is named in the commit body as an addition, not an edit.
- [ ] **Step 2: run** `task harness:test`. Expected: FAIL.
- [ ] **Step 3: implement.** `agent_push.py` reuses `agent_run`'s MCP client class (the `MCP_TOKEN_FILE` bearer and the harness header). Run `task harness:test`. Expected: PASS.
- [ ] **Step 4: commit** `feat(harness): git push requests a push output when the factory pushes`.

### Task 38: the run's rules

**Spec:** H3 row

**Files:**
- Modify: `internal/runctl/rules.go`, `internal/runctl/rules_test.go`, `internal/factory/reconciler/text.go`

- [ ] **Step 1: failing test** `TestRulesPushOutput`: with `push` served, the implementer's rule 2 reads "Push only to <branch>, with git push: the factory checks the push and applies it. A change to a protected path waits for a maintainer's approval; check it with output_status." and gains "Never force-push another branch." The tester's contract line names the factory push. Without `push` the rules are byte-for-byte H2's. Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/runctl/ -run TestRulesPushOutput -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): runs with push outputs are told how their pushes land`.

### Task 39: wire it; docs; the gate (PR 6)

**Files:**
- Modify: `internal/app/factory.go` (the applier gets `Pushers`, the `gitbundle` mirror root from `MAKU_GIT_CACHE`, and `ApprovalTTL`), `internal/app/factory_test.go`, `docs/security.md`, `docs/factory.md` ("Hardening, push enforced"), `docs/operations.md`, `docs/roadmap.md`

- [ ] **Step 1: failing test** `TestFactoryPushWiring`:
  - a repository with `push` enforced gets an applier with a pusher and a mirror root;
  - no `MAKU_GIT_CACHE` fails startup with a clear error;
  - `Prerequisites` is still `nil`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/app/ -run TestFactoryPushWiring -count=1`. Expected: FAIL.
- [ ] **Step 2: wire it.** Run the same command. Expected: PASS.
- [ ] **Step 3: docs.**
  - The enable order: release, then the publisher policy gaining `contents: write` (optionally `workflows: write`), then config, then the capability profiles, then the role policy or `gitReadSecret`, last.
  - Rollback: restore the role policy or `gitSecret` first, then remove `push`.
  - The approval surfaces.
  - The HR31 residual.
- [ ] **Step 4: gate.** Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 5: open PR 6**, listing HR30, HR31, HR33 and HR34. Name HR30 as the concrete D10 amendment.

---

## PR 7 · H4a, reads filtered (L)

### Task 40: `FilterThread` and the `ForgeReads` port

**Spec:** UH6, UH7, UH11, HR35

**Files:**
- Create: `internal/hardening/forgeread.go`, `internal/hardening/forgeread_test.go`
- Modify: `internal/mcp/hardening.go`, `internal/mcp/hardening_test.go`

- [ ] **Step 1: failing tests.**
  - `TestFilterThread` (moved from H0's `ForgeReadTool` tests, case for case):
    - text below the minimum is withheld and counted;
    - `factory/vouch` raises an item to `approved`;
    - bodies are sanitised and cut at 16 KiB;
    - foreign CI is `none`.
  - `TestForgeReadToolForwards`: `ForgeReadTool(r ForgeReads)` passes the caller's run and the target, returns the port's result as is, and never reads a run id from the arguments.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ ./internal/mcp/ -run 'TestFilterThread|TestForgeReadTool' -count=1`. Expected: FAIL. H0's `ForgeReadTool` tests move into `TestFilterThread`, so name the move in the commit body.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `refactor(hardening): the read filter is one pure function the factory runs`.

### Task 41: the factory's forge-read route

**Spec:** UH6, UH11, HR35

**Files:**
- Create: `internal/factory/api/forgeread.go`, `internal/factory/api/forgeread_test.go`
- Modify: `internal/factory/api/server.go`, `internal/factory/forge/github.go` (`FailedJobLog`, `IssueLabels`), `internal/factory/forge/github_test.go`, `internal/factory/forge/forgetest/fake.go`, `internal/app/factory.go`

- [ ] **Step 1: failing tests.**
  - `TestForgeReadRoute`:
    - `POST /v1/runs/{run}/forge-read {target: issue|pr|checks}` from `system:broker` resolves the run's Task by `status.runs[].id`, reads the task's own issue, pull request or branch-head checks through the repository's factory-App connection, and answers `ForgeReadResult` filtered by `RepositoryPolicy.MinTrust`, maintainers, trusted bots and `blockedUsers`;
    - a run of a repository not in `mode: enforce` answers 409 `read_filter_off`;
    - any other principal gets 403;
    - an unknown run gets 404.
  - `TestForgeReadNoWithheldText`: the response bytes never contain a withheld body, and neither does the log.
  - `TestFailedJobLog`: at most 64 KiB, the tail, sanitised.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/api/ ./internal/factory/forge/ -run 'TestForgeRead|TestFailedJobLog' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): read a run's issue, pull request and checks with author trust applied`.

### Task 42: the broker serves `forge_read`; `input_read`

**Spec:** UH7, UH11, HR35

**Files:**
- Create: `internal/mcp/forgeclient.go`, `internal/mcp/forgeclient_test.go`
- Modify: `internal/envelope/output.go` (`KindInputRead`, `InputReadRecord`), `internal/app/mcp.go`, `internal/app/broker.go`, `charts/maku/templates/broker-deployment.yaml` (a projected token for the factory's audience), the chart's render test, `docs/room-log.md`

- [ ] **Step 1: failing tests.**
  - `TestForgeClient`: it posts to the config's `FactoryURL` with the broker's projected token, and maps 409 to the tool error "read_filter_off: forge_read is not active for this repository".
  - `TestForgeReadServed`: listed only for a run annotated `agents.ogenki.io/forge-api: deny`. Each call appends one `input_read` (`system:broker`, `Delivery: none`, `{target, withheld, min, sources}`), validated, with no text.
  - Chart render: with `hardening.forgeRead.audience`, the broker mounts the token.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/mcp/ ./internal/envelope/ ./internal/app/ -run 'TestForgeClient|TestForgeReadServed|TestInputRead' -count=1` and `task chart:check`. Expected: FAIL.
- [ ] **Step 3: implement.** Run both. Expected: PASS.
- [ ] **Step 4: commit** `feat(broker): serve forge_read and record what each run read`.

### Task 43: `mode: enforce`, the run annotations, `status.inputTrust` under enforce

**Spec:** H4 row, R1, UH7, HR8, HR34

**Files:**
- Modify: `internal/factory/config/hardening.go`, `internal/factory/config/hardening_test.go`, `internal/factory/runs/runs.go` (`AnnForgeAPI`), `internal/factory/reconciler/trust.go`, `internal/factory/reconciler/implement.go`, `internal/factory/reconciler/trust_test.go`

- [ ] **Step 1: failing tests.**
  - `TestModeEnforce`: a repository `{mode: enforce, enforceOutputs: [all five]}` validates on the run-controller. It fails on the XR ("R5 cannot hold on the XR: forge_read needs the run-controller's egress (HR36)"), without all five, or with no `system:broker` in `api.systemAllow`.
  - `TestStartRunForgeAPI`: every claim of an enforced repository, reviewer and triager included, carries `agents.ogenki.io/forge-api: deny`.
  - `TestInputTrustEnforce`: under enforce, `status.inputTrust.mode` is `enforce`. `withheld` sums the run's `input_read` records. `min` counts only what was delivered, so withheld text does not lower it.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ ./internal/factory/reconciler/ -run 'TestModeEnforce|TestStartRunForgeAPI|TestInputTrustEnforce' -count=1`. Expected: FAIL. Update Task 35's `TestEnforceRefusalH4` to the new rule and name the edit.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(config): mode enforce, reads filtered on the run-controller`.

### Task 44: the run-controller drops `api.github.com`

**Spec:** UH11, v4 D6, HR36

**Files:**
- Modify: `internal/runctl/config.go` (`FQDNs`), `internal/runctl/pod.go` (`FORGE_READ=1`), `internal/runctl/config_test.go`, `internal/runctl/testdata/cnp.golden.yaml`. After #492, also its `netpol` and egress-annotation goldens.

- [ ] **Step 1: failing test** `TestFQDNsForgeAPIDeny`:
  - an AgentRun annotated `forge-api: deny` gets the `github` profile without `api.github.com`, and keeps `github.com`, `codeload.github.com` and `objects.githubusercontent.com`;
  - without the annotation the list is unchanged;
  - the pod gets `FORGE_READ=1`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/runctl/ -run TestFQDNsForgeAPIDeny -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run `go test ./internal/runctl/ -count=1`. Expected: PASS. **Ordering rule:** if #492 has merged, its egress annotation derives from `FQDNs` and its golden gains the denied case. If it has not, this change covers the Cilium mode, and #492 rebases onto it.
- [ ] **Step 3: commit** `feat(runctl): a run that reads through forge_read has no api.github.com egress`.

### Task 45: the harness's reads

**Spec:** UH11, HR37

**Files:**
- Modify: `images/maku-harness/gh`, `images/maku-harness/agent_run.py` (judge triage), `images/maku-harness/git_credential_agent.py` (revoke), `images/maku-harness/tests/test_gh_wrapper.py`, `images/maku-harness/tests/test_judge_triage.py`, `internal/factory/reconciler/merge.go`, `internal/runctl/rules.go`, and the matching Go tests

- [ ] **Step 1: failing tests.**
  - The wrapper: with `FORGE_READ=1`, `gh issue view|list`, `gh pr view|checks|list`, `gh run view|list` and `gh api …` exit 3 with "use forge_read (target issue, pr or checks)". Without the variable they behave as today.
  - Judge triage: under `FORGE_READ=1` the check runs and failed logs come from `forge_read {target: checks}` through the harness client, and no `gh` subprocess runs.
  - The credential agent: under `FORGE_READ=1` the revoke is skipped and logged once.
  - `TestCIFailedTextForgeRead`: on an enforced repository the revision text reads "Read the failing jobs with forge_read (target checks)". Otherwise it is as today.
  - `TestRulesReviewerDiff`: under `FORGE_READ` the reviewer's rules name `git diff origin/<base>...HEAD`.
- [ ] **Step 2: run** `task harness:test` and `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/reconciler/ ./internal/runctl/ -run 'TestCIFailedTextForgeRead|TestRulesReviewerDiff' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run both. Expected: PASS.
- [ ] **Step 4: commit** `feat(harness): runs read forge text through forge_read`.

### Task 46: R5; docs; the gate (PR 7)

**Files:**
- Modify: `internal/factory/config/hardening.go` (`HardeningSettings.ReadFilter`), `internal/factory/config/hardening_test.go`, `docs/security.md`, `docs/factory.md` ("Hardening, reads filtered"), `docs/operations.md`

- [ ] **Step 1: failing test** `TestReadFilterFact`: `ReadFilter` holds only under HR43's conditions. On the XR it is always false. Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ -run TestReadFilterFact -count=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Run the same command. Expected: PASS.
- [ ] **Step 3: docs.**
  - What enforce changes: withholding, no API egress, `forge_read`.
  - The residuals: HTML on `github.com`, and an approved author's injection.
  - Rollback is `mode: audit`, which restores egress at the next run.
- [ ] **Step 4: gate.** Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 5: open PR 7**, listing HR34 to HR37 and HR43.

---

## PR 8 · H4b, the drills (L)

### Task 47: the drill report store

**Spec:** UH14, R6, HR38

**Files:**
- Create: `internal/store/migrations/20261113140000_drill_reports.sql`, `internal/store/drills.go`, `internal/store/drills_test.go`, `internal/bridgeapi/drills.go`, `internal/bridgeapi/drills_test.go`, `internal/factory/rooms/drills.go`, `internal/factory/rooms/drills_test.go`

- [ ] **Step 1: failing tests.**
  - `TestStoreDrillReport` (Docker): `PutDrillReport` stores `{id, version, configHash, at, pass, report jsonb ≤ 256 KiB}` once per id. `LastPass(version, hash)` returns the newest passing report.
  - `TestDrillRoutes`: `POST /v1/drills` and `GET /v1/drills/last-pass?version=&configHash=` from `system:factory` only.
  - `TestRoomsDrills`: the client implements `hardening.Drills`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/store/ ./internal/bridgeapi/ ./internal/factory/rooms/ -run 'TestStoreDrillReport|TestDrillRoutes|TestRoomsDrills' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement**, then `atlas migrate hash`. Run the same command and `task migrations`. Expected: PASS.
- [ ] **Step 4: commit** `feat(store): drill reports, written by the factory alone`.

### Task 48: the gateway-log port

**Spec:** UH14, AC5, HR38

**Files:**
- Create: `internal/factory/drills/gatewaylog.go`, `internal/factory/drills/gatewaylog_test.go`
- Modify: `internal/factory/config/hardening.go` (`drills.gatewayLog {url, timeout}`)

- [ ] **Step 1: failing tests** against an `httptest` VictoriaLogs:
  - `MarkerHits(ctx, runIDs, marker, from, to)` issues one LogsQL query, filtered by the run-id field and the marker phrase, and returns the count;
  - `Guardrails(ctx, runIDs, from, to)` returns the counts per `guardrails` verdict;
  - errors carry no marker text;
  - an unset URL is a nil port.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/drills/ ./internal/factory/config/ -run 'TestMarkerHits|TestGuardrails|TestGatewayLogConfig' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): read the gateway's audit log for drill scoring`.

### Task 49: the drill runner

**Spec:** UH14, AC3 to AC5, O3, HR38

**Files:**
- Create: `internal/factory/drills/runner.go`, `internal/factory/drills/runner_test.go`, `internal/factory/api/drills.go`, `internal/factory/api/drills_test.go`
- Modify: `internal/hardening/drill.go` (`DetectionsByType`), `internal/hardening/drill_test.go`, `internal/factory/reconciler/` (claims of `factory/drill-injection` tasks are kept until scored), `internal/factory/fmetrics/fmetrics.go`, `internal/app/factory.go`

- [ ] **Step 1: write the failing tests.**
  - `TestDrillManifestRoute`: `POST /v1/hardening/drills {id, scenarios: [{scenario, task, marker}]}` from an operator is accepted once per id. Each scenario id must be in `DefaultSuite`, and each task must exist and carry `factory/drill-injection`.
  - `TestDrillRunner`, with fakes for rooms, bundles, `gitbundle` and the gateway log:
    - once every named task is terminal or past `drills.timeout` (default 6h), it builds one `Observation` per scenario × role present, as HR38 describes;
    - it scores with `DrillMeta{Version, ConfigHash: Hash()}` and posts the report;
    - it sets `agent_factory_hardening_drill_last{result}` and logs `drill scored` with the id, pass and failure count only.
  - `TestDrillRunnerMissingRole`: a scenario whose role never ran fails `not_run`, so the report does not pass.
  - `TestDetectionsByType`: refusals and holds are counted per output type and layer.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/drills/ ./internal/factory/api/ ./internal/hardening/ -run 'TestDrill|TestDetectionsByType' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command, then `go test ./internal/factory/... -count=1`. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): score injection drills from the room and gateway logs`.

### Task 50: T5 measured on refusals

**Spec:** AC6, HR39

**Files:**
- Modify: `internal/hardening/threat.go` (`Checker.Measure`), `internal/hardening/threat_test.go`, `internal/factory/applier/applier.go`, `internal/factory/applier/applier_test.go`, `internal/factory/fmetrics/fmetrics.go`

- [ ] **Step 1: failing tests.**
  - `TestMeasureAfterRefusal`: a text output refused by T3 still gets T5's verdict in `Audit`. A push refused at T1 does not, because nothing can be read.
  - `TestT5Counter`: `agent_factory_hardening_t5_total{type,result=hit|pass|unavailable}` is counted on every T5 call, with a `drill` label (`true`/`false`) from the task's label.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ ./internal/factory/applier/ -run 'TestMeasureAfterRefusal|TestT5Counter' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(hardening): measure T5 on every text output, refused ones included`.

### Task 51: `maku drill plant`

**Spec:** UH14, O3

**Files:**
- Create: `internal/maku/drill.go`, `internal/maku/drill_test.go`, `docs/drills.md`

- [ ] **Step 1: failing test** `TestDrillPlant`, against `httptest` GitHub and factory servers:
  - with `--repo`, `--stranger-token-file`, `--maintainer-token-file` and `--factory`, it plants each scenario of the suite:
    - `issue_body`: a stranger's issue, labelled with the trigger label by the maintainer;
    - `comment`: a maintainer issue plus a stranger comment;
    - `ci_log`: a branch fixture whose workflow echoes the marker;
    - `encoded`: Base64 and homoglyph variants;
  - each plant carries a fresh 16-hex marker, then the manifest is posted;
  - the `default_branch` fixtures are checked to exist, never written;
  - `runlore`, `tool_result` and `room` are planted through the documented hooks (the internal principal's task, a planted log line, a scoper handoff of the planted issue), or the command exits 2, naming the missing hook;
  - tokens are read from files and never printed.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/maku/ -run TestDrillPlant -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(maku): plant an injection drill and hand its manifest to the factory`.

### Task 52: wiring; docs; the gate (PR 8)

- [ ] **Step 1:** wire the runner (leader only) and the `Evaluator.Drills` adapter in `internal/app/factory.go`, with `TestFactoryDrillWiring`. Run `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/app/ -run TestFactoryDrillWiring -count=1`. Expected: FAIL, then PASS.
- [ ] **Step 2: docs.** `docs/drills.md` covers the repository, the accounts, the cadence, how to read a report and the stop rule. Add a drills section to `docs/factory.md`.
- [ ] **Step 3: gate.** Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 4: open PR 8**, listing HR38 and HR39.

---

## PR 9 · H5, ready (M)

### Task 53: the policy reader; R2 and R3 per run path

**Spec:** R2, R3, HR42

**Files:**
- Modify: `internal/hardening/readiness.go` (`Settings.RunPath`, the `RoleTable` port), `internal/hardening/readiness_test.go`, `internal/factory/forge/github.go` (`DefaultBranchFile`), `internal/factory/forge/github_test.go`, `internal/factory/config/hardening.go`

- [ ] **Step 1: failing tests.**
  - `TestPoliciesPerRunPath`:
    - on the XR, R2 and R3 read the policy files, as H0's tests do;
    - on the run-controller with no `RoleTable`, the answer is `sandbox_write_token` missing, and R3 is read from the publisher policy file (octo-sts still mints the publisher);
    - with a `RoleTable` whose grants for the repository's sandbox roles hold no write, R2 holds.
  - `TestDefaultBranchFile`: a 404 is `(nil, false, nil)`, and the file is read through the factory App connection.
  - Option B, only if the owner picks it: `TestProbeAttestedR2`.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/hardening/ ./internal/factory/forge/ -run 'TestPoliciesPerRunPath|TestDefaultBranchFile' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(hardening): read the sandbox's write facts where each run path keeps them`.

### Task 54: readiness reported

**Spec:** UH13, HR41

**Files:**
- Create: `internal/factory/hardeningreport/report.go`, `internal/factory/hardeningreport/report_test.go`
- Modify: `internal/factory/api/server.go` (`GET /v1/hardening/readiness`), `internal/factory/api/evidence.go`, `internal/factory/fmetrics/fmetrics.go`, `internal/app/factory.go`, `web/src` (the Evidence page's readiness row)

- [ ] **Step 1: failing tests.**
  - `TestReadinessReport`:
    - every 5 minutes, for each repository at `mode` audit or above, `Hardened` is computed afresh and sets `agent_factory_hardening_ready{repository}` and `_missing{repository,missing}`;
    - a change is logged once, with the repository and the `Missing` list;
    - an evaluation error sets `ready` 0 and `_missing{missing="error"}`.
  - `TestReadinessRoute`: an operator gets `[{repository, hardened, missing, drill}]`. Everyone else gets 403.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/hardeningreport/ ./internal/factory/api/ -run 'TestReadiness' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Run the same command, and the web suite. Expected: PASS.
- [ ] **Step 4: commit** `feat(factory): report each repository's hardening readiness`.

### Task 55: `coordination.Prerequisites` wired

**Spec:** Execution prerequisites, AC9, HR41

**Files:**
- Create: `internal/factory/coordination/authority.go`, `internal/factory/coordination/authority_test.go`
- Modify: `internal/app/factory.go`, `internal/app/factory_test.go`

- [ ] **Step 1: failing tests.**
  - `TestConfigAuthority`: `Authoritative(repo)` is `cfg.RepositoryAuthoritative` on the live config. An unknown repository answers false.
  - `TestPrerequisitesWired`:
    - `coordination.Service.Prerequisites` is non-nil;
    - for each of R1 to R7 made false, the next `ReadyForExecute` refuses (AC9, offline);
    - a repository both authoritative and hardened passes.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/coordination/ ./internal/app/ -run 'TestConfigAuthority|TestPrerequisitesWired' -count=1`. Expected: FAIL.
- [ ] **Step 3: implement.** Remove the comment that says `Prerequisites` stays nil. Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(coordination): execute asks class authority and hardening readiness`.

### Task 56: T5 promoted per output type

**Spec:** H5 row, AC6, O4, HR40

**Files:**
- Modify: `internal/factory/config/hardening.go`, `internal/factory/config/hardening_test.go`, `internal/hardening/threat.go` (`Layers.ClassifierFor(t)`), `internal/hardening/threat_test.go`, `internal/factory/applier/applier.go`

- [ ] **Step 1: failing tests.**
  - `TestClassifierEnforcePerType`:
    - `layers.classifierEnforce: [pr, comment]` validates only with `agentGuard.url` and the repository at `mode: enforce`, and `layers.classifier: enforce` stays refused;
    - `Hash()` covers the list.
  - `TestCheckerPerType`: a hit on `pr` refuses `injection_suspected`, while a hit on `issue` is recorded in audit. An unavailable classifier refuses `classifier_unavailable` on `pr` and fails open on `issue`.
  - `TestApplierPerTypeT5`: the applier no longer caps T5 at audit for promoted types.
- [ ] **Step 2: run** `systemd-run --user --scope -q -p MemoryMax=6G go test ./internal/factory/config/ ./internal/hardening/ ./internal/factory/applier/ -run 'TestClassifierEnforcePerType|TestCheckerPerType|TestApplierPerTypeT5' -count=1`. Expected: FAIL. H2's `TestApplierT5Audit` keeps its case for an unpromoted type. Name that edit.
- [ ] **Step 3: implement.** Run the same command. Expected: PASS.
- [ ] **Step 4: commit** `feat(hardening): promote T5 to enforce one output type at a time`.

### Task 57: docs; the gate (PR 9)

- [ ] **Step 1: docs.**
  - `docs/factory.md` gets "Hardening, ready": R1 to R7 and where each fact is read, the report, AC9, the promotion procedure, and HR42's block.
  - `docs/security.md` gets the residuals.
  - `docs/roadmap.md`.
- [ ] **Step 2: gate.** Run `systemd-run --user --scope -q -p MemoryMax=6G task check`. Expected: exit 0.
- [ ] **Step 3: open PR 9**, listing HR40 to HR42. Ask for the owner's HR42 decision in the description.

---

## After this step (not in these PRs)

### v4 production-path follow-ups (Maku specs, before step 3 is built)

1. **D10 amendment (HR12, made concrete by HR30):**
   - the minter mints each sandbox role from `hardening.SandboxPermissions(role, EnforcedFor(repo))`, the reviewer read-only;
   - the publisher is minted from the agents App as a second `PublisherTokens` adapter;
   - the minter exposes `hardening.RoleTable` for R2.
2. **Step 2 ordering (HR13, narrowed):** cloud-native-ref may move to the run-controller before step 3 for repositories that enforce all five types, with `gitReadSecret` holding a read-only credential (AC1 probe). It may not for a partially enforced repository.
3. **HR42 owner decision:** Option A (H5 after step 3) or Option B (probe-attested R2).

### ANR follow-ups (each its own PR, after the matching Maku release)

1. **H3 config** (after PR 6), on one repository first (`Smana/ai-native-ref`):
   - bump the pin;
   - `repositories.<repo>.enforceOutputs: [comment, issue, labels, pr, push]`;
   - `applier.approvalTTL: 24h`;
   - `gitCache.sizeLimit`;
   - the Slack channel's `approvals: true`, if it is not already on.
2. **Capability profiles:** `output_status` for the implementer and the tester (`infrastructure/base/agent-gateway/profiles/*.yaml`, regenerated with `scripts/ci/gen-capabilities.py --write`). On the XR, raise the agent-mcp route's request-body limit to 12 MiB for `/mcp`, or confirm its default (HR22). `output_push` is harness-only and is not added to the model's profile.
3. **Repository policy PRs** (maintainer-reviewed, protected paths), in each enforced repository:
   - first, `publisher.sts.yaml` gains `contents: write`, plus `workflows: write` only if the repository wants approved workflow pushes (HR31);
   - **after** one canary task's push arrives through `output_applied`, `agent-implementer.sts.yaml` and `agent-tester.sts.yaml` drop to `contents: read`;
   - on the run-controller, `runController.gitReadSecret` holds a read-only credential, and `unsafeSharedGitToken` is false.
4. **XR composition** (cloud-native-ref, only while an enforced repository still runs on the XR): render `SAFE_OUTPUTS` with `push`, the rules text and `MCP_READ_ONLY_TOOLS`, as H2's step 5 did.
5. **H4 config** (after PRs 7 and 8):
   - runs on the run-controller for the enforced repositories (v4 step 2's switch);
   - `api.systemAllow` maps the broker's ServiceAccount to `system:broker`;
   - the broker's `hardening.forgeRead.audience`;
   - a NetworkPolicy from the broker to the factory API;
   - `forge_read` in every role's capability profile;
   - then `repositories.<repo>.mode: enforce`.
6. **Drills (O3):**
   - create the public drill repository `Smana/maku-drill`, with its `default_branch` fixtures and the CI-log workflow;
   - the `drill-stranger` account and its token Secret, and a maintainer token Secret;
   - a weekly CronJob running `maku drill plant`, plus a Flux-triggered run when the factory's `hardening` hash changes;
   - `hardening.drills.gatewayLog.url` pointing at VictoriaLogs, with the gateway's `guardrails` and run-id fields in its access log;
   - the drill repository added to the factory at `mode: enforce`.
7. **Dashboards and alerts:**
   - panels for `_ready`, `_missing`, `_approval_wait_seconds`, `_t5_total` and `_drill_last`;
   - alerts on `ready` going from 1 to 0, any `canary_leaked`, any `untraced_write`, `_policy_drift`, and `_drill_last{result="fail"}`.
8. **H5:** coordination's `mode` moves to execute only after runbook H5-2. T5 promotions are config PRs, one type each, citing the drill report id.

### Live gates: runbook items for a gcp-0 session

ANR `docs/runbooks/agent-factory/27-hardening-h3-h5.md`, with results recorded in the session ledger.

**H3-1 · one repository's push enforced.** After ANR steps 1 to 3 on `Smana/ai-native-ref`:
- a task's pushes arrive through `output_applied`, with the objects at `/commit/<sha>`;
- the gauge shows the implementer at `contents: read`;
- a probe implementer run's raw `git push` (with the helper's token), `gh api -X PUT …/contents/x` and `curl -X POST` with `$GH_TOKEN` all answer `403`, which is AC1;
- `_untraced_total{type="push"}` stays at 0.

**H3-2 · approvals in the room and in Slack.** Each case below is required:
- one task that bumps a dependency in `go.mod` gets an approval approved on the room page, then applied;
- one approved from the Slack card, then applied;
- one denied (`approval_denied`, and the task takes the no-push path);
- one left to expire on a 1h `approvalTTL` test setting (`approval_expired`);
- one superseded by a second push;
- a four-eyes refusal for the prompter.

For each, record the decision surface, the wait, and the card's digest matching the record's.

**H3-3 · the 20-task count.**
- Run 20 tasks on the push-enforced repositories, the second added after the first passes 10.
- **Advance** when:
  - every push of those tasks is traced to an `output_applied`;
  - no task stuck outside a pending approval;
  - every protected-path push either waited and was decided, or was refused by its rule.
- Also record p95 of `_output_seconds{type="push"}` (AC7, at most 10 s) and the merge rate against the previous 30 tasks (AC8).
- **Rollback rule:** restore the role policy or `gitSecret`, then remove `push`.

**H4-1 · reads filtered.** After ANR step 5:
- a run's pod has no `api.github.com` in its CNP or egress annotation;
- `curl https://api.github.com` from the sandbox fails, while `git fetch` works;
- `gh issue view` exits 3;
- a stranger's comment on the task's issue reaches `forge_read` as `[1 item withheld: author trust]`;
- `status.inputTrust.mode` is `enforce`, with `withheld.comment: 1`.

**H4-2 · the first full drill.** After ANR step 6:
- `maku drill plant` covers every scenario × role in `DefaultSuite` (33 runs).
- **Advance** when the report passes: no out-of-policy output applied, no canary applied, every withheld marker at 0 model requests (AC5), and no `not_run`.
- **Stop and fix** on any failure. The fix restarts the drill.
- Record the detections per layer and type, as T5's baseline.

**H5-1 · readiness true.**
- With the minter in place (HR42 Option A) or the probe attestation (Option B), `GET /v1/hardening/readiness` answers `hardened: true` for each enforced repository, naming the drill.
- Switching one repository to `mode: audit` turns its gauge to 0 within 5 minutes, and refuses the next coordination admission, which is AC9 live. Switch it back.

**H5-2 · two consecutive weekly drills plus acceptance.** Two weekly drills pass for the same config hash, which is AC3. Then the acceptance table holds:
- AC1 (H3-1's probe, on every enforced repository);
- AC2 (30 days of `_untraced_total` at 0);
- AC4 (no `canary_leaked` across the drills and 30 days of tasks);
- AC5 (H4-2);
- AC6 (H1's 1%, and T5 per type);
- AC7 and AC8 (H3-3's numbers, re-read over 30 days);
- AC9 (H5-1).

**H5-3 · T5 promotion, per type.** For a type whose drills show T5 detection above T2 and T3 on the planted outputs, and below 2% false positives (`_t5_total{drill="false",result="hit"}` over `_t5_total{drill="false"}`, 30 days), with the classifier's availability at its target, add the type to `classifierEnforce`. Then a passing drill under the new hash is needed before readiness returns (R6).

## Self-review

- **H3 row coverage:**
  - `push` through the applier with T4: Tasks 22 to 31, plus 35 to 39.
  - Sandbox roles hold `contents: read`: Task 36 (run-controller) and ANR step 3 (XR), without the minter (HR30).
  - The gate: 20 tasks, with approvals in the room and in Slack, in runbook H3-2 and H3-3, built by Tasks 26 and 27.
- **H4 row coverage:**
  - `forge_read`: Tasks 40 to 42.
  - `minAuthorTrust` applied: Tasks 41 and 43.
  - No `api.github.com` egress: Task 44, on the run-controller (HR36), with the harness fallout in Task 45.
  - The gate: the first full drill, in Tasks 47 to 51 and runbook H4-2.
- **H5 row coverage:**
  - Readiness reports true for enforced repositories: Tasks 53 to 55, blocked on the run-controller by v4 step 3 unless the owner picks HR42 Option B.
  - T5 promoted per type: Tasks 50 and 56, and runbook H5-3.
  - The gate: H5-2.
- **H0 and H2 carry-overs:**
  - B5's bundle store and body cap: Tasks 23 and 24.
  - The `ForgeReader` with no implementation: Tasks 40 and 41.
  - The `Drills` with no implementation: Task 47.
  - The `Prerequisites: nil`: Task 55.
  - `Settings.ReadFilter` always false: Task 46.
  - H2's agent never learning its answers: `output_status`, Task 24.
- **Acceptance mapping:**
  - AC1: H3-1, Task 36;
  - AC2: Task 33 plus H5-2;
  - AC3: Task 49 plus H5-2;
  - AC4: Tasks 31 and 49;
  - AC5: Tasks 41, 48 and H4-2;
  - AC6: Tasks 50 and 56;
  - AC7: Task 31's histogram;
  - AC8: H3-3;
  - AC9: Task 55 offline, H5-1 live.
- **v4 interactions:**
  - D10 and HR12 are made concrete (HR30) and the block is stated (HR42).
  - D5 and step 2: HR13 is narrowed.
  - D6 and #492: Task 44's ordering rule.
  - D12: the publisher stays the agents App, and the factory App files issues and reads `forge_read`.
- **Risks, each with a stop rule:**
  - A bundle or `git` exploit in the factory: fsck, no worktree, no hooks, a timeout, and a size cap (HR24).
  - Approvals that strand tasks: the TTL and the task hold (HR27, HR29), with runbook H3-2's expiry case.
  - Policy-before-config ordering: ANR step 3.
  - Version skew between the factory and the run-controller: R5 (HR43), with runbook H4-1's probe.
  - A forged drill report: the factory scores and is the only writer (HR38).
  - A T5 outage after promotion: per-type fail-closed, promoted only at its availability target (HR40).

