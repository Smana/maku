# The factory's demo mode: implementation plan (plan A of the factory on a laptop)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `maku-factory` runs the whole label → scoper → implementer → judge → reviewer → gate loop on a laptop cluster with one GitHub App, no OIDC issuer, no metrics backend, an `http` broker and the portable AgentRun, and the gate posts its own `maku/merge-gate` commit status.

**Architecture:** One top-level factory config key, `demo: true`, relaxes a fixed list of production requirements in `Validate` and swaps three startup dependencies for demo stand-ins (a plain HTTP broker client, an off meter, no human verifier). A demo-only runnable keeps the shared `agent-git` Secret filled with a token minted from the factory App. Independently of demo, a new `merge.gateStatus` option makes the gate post `maku/merge-gate`, and `merge.policyBot: false` (demo only) removes the policy-bot rows from `baseChecks`.

**Tech Stack:** Go (controller-runtime, go-github, githubv4), the repository's `task check`.

**Spec:** `docs/superpowers/specs/2026-11-06-factory-on-a-laptop-design.md` (decisions Q1–Q27). Plans B (the umbrella chart), C (`task demo`, kind and k3d, the e2e job) and D (the quickstart page) follow this one.

## Global Constraints

- `task check` exits 0 before every commit (AGENTS.md). Lint only through `task lint`. Store tests need Docker (it runs here).
- Conventional commits, English, no trailers, no co-author lines.
- Every new `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- Production behaviour is unchanged when `demo` is absent or false: every new rule is gated on `demo` or on a new key that defaults off. A test per task proves the non-demo path still refuses what it refused.
- Q14: in demo, the factory, every run and the merger share one GitHub App. Q15: demo refuses any repository that is not public, before a run starts. Q12: the gate posts `maku/merge-gate` with the class and reason. Q13: shadow by default.
- Names: the demo profile is "demo" everywhere (config key, metric labels, docs). Never "local" for the factory (that word is the broker's `human.mode: local`).

## File structure

| File | Responsibility |
|---|---|
| `internal/factory/config/config.go` | `Demo bool`, `Merge.GateStatus`, `Merge.PolicyBot` and the demo branches of `Validate` |
| `internal/factory/config/demo_test.go` (new) | Demo config: good variant, each relaxed rule, each rule that still holds, non-demo unchanged |
| `internal/app/factory.go` | Demo stand-ins at startup; registering the token writer and the visibility check |
| `internal/factory/meter/off.go` (new) | `meter.Off`: a usage source that reports zero and says so |
| `internal/factory/forge/app.go` | Exported `Mint(ctx, MintOptions) (Token, error)` built from the existing private helpers |
| `internal/factory/forge/visibility.go` (new) | `Public(ctx, repo) (bool, error)` on `TaskForge` |
| `internal/factory/demo/gittoken.go` (new) | The runnable that writes `agent-git` every 30 minutes |
| `internal/factory/intake/issues.go` | The `private_repository` refusal in `IssuePoller.one` |
| `internal/factory/narrate/narrate.go` | The refusal's text |
| `internal/factory/forge/forge.go`, `github.go`, `absent.go`, `forgetest/fake.go` | `Merger.SetStatus` |
| `internal/factory/reconciler/arm.go`, `merge.go` | Skipping policy-bot when `Merge.PolicyBot` is false; posting the status |
| `internal/factory/reconciler/gatestatus.go` (new) | `statusFor(ArmDecision, class) (state, description string)` |
| `docs/factory.md` | The `demo`, `merge.gateStatus` and `merge.policyBot` keys |

---

### Task 1: The `demo` config key and its validation

**Files:**
- Modify: `internal/factory/config/config.go` (`Config` struct `:103-162`, `Merge` struct `~:389`, `Validate` `:1226` onward, the loop and scope refusals `:1889-1915`)
- Create: `internal/factory/config/demo_test.go`
- Modify: `docs/factory.md` (the config reference)

**Interfaces:**
- Produces: `Config.Demo bool` (`json:"demo,omitempty"`); `Merge.GateStatus bool` (`json:"gateStatus,omitempty"`); `Merge.PolicyBot *bool` (`json:"policyBot,omitempty"`, nil means true) with method `(m Merge) UsesPolicyBot() bool`.

What `demo: true` changes in `Validate`, exactly:

| Rule today | Under `demo: true` |
|---|---|
| `broker.url` must be `https://` | `http://` also accepted |
| `broker.caFile` required | optional; with an `http` URL it must be empty |
| `meter.url`, `meter.query`, `meter.logsURL`, `meter.throttleQuery` required | optional. If any is set, the existing rules apply. If `meter.url` is empty, any USD cap (`meter.prices`, sponsored USD ceilings, `split.familyUSD`, tier `taskUSD`) is refused: `demo: %s needs a meter, and the demo has none` |
| `roomsURL` must be `https://` | `http://localhost[:port]` and `http://127.0.0.1[:port]` also accepted |
| `api.humanIssuer`, `api.humanJWKS`, `api.clientIDFiles`, `api.groups.*` required | optional, and must all be empty: `demo: the factory API takes no human tokens` |
| `api.access` optional | must be unset: `demo: api.access needs an identity provider` |
| `scope: scoper` refused off the default runs group | accepted |
| a loop policy refused off the default runs group | accepted **only if** the run-controller passes the loop to the harness (verify: `internal/runctl/pod.go` handles `Loop.Checks` and `Loop.Search`; check `JudgeModel`, `StallSteps` and `Continuations` reach the harness). If any field the row sets is not passed, keep the refusal for that field and name it in the message |
| `merge.policyBotLogin` required for a live or shadow class | not required when `merge.policyBot: false` |

Rules that `demo` adds:
- `merge.policyBot: false` is refused without `demo: true`: `merge.policyBot: false is the demo's: production gates through policy-bot`.
- `demo: true` requires `merge.gateStatus: true`: `demo: merge.gateStatus must be true, or no gate verdict reaches the pull request`.
- `merge.gateStatus` is allowed without demo.

- [ ] **Step 1: Write the failing tests** in `demo_test.go`, following `TestBadConfigsFail` (`config_test.go:359`): a `demoGood` YAML const derived from `good` (`config_test.go:20-73`) with `demo: true`, `broker.url: http://room-broker.maku.svc:8080`, no `caFile`, no `meter`, no human API keys, `merge.gateStatus: true`, `merge.policyBot: false`, the portable runs group and one row with `scope: scoper`. Then:
  - `TestDemoGoodParses`: `Parse(demoGood)` succeeds.
  - `TestDemoStillRefuses`: a `from/to/err` table on `demoGood` for each "still holds" row: `api.access` set, a `humanIssuer` set, `meter.prices` without `meter.url`, `gateStatus: false`, an `http` broker with a `caFile` set (each with its exact message above).
  - `TestNonDemoUnchanged`: on `good`, `merge.policyBot: false` fails; `broker.url: http://...` still fails with the old message; `scope: scoper` on the portable group still fails.
- [ ] **Step 2: Run them, see them fail:** `go test ./internal/factory/config/ -run 'Demo|NonDemo' -count=1`. Expected: FAIL on unknown field `demo`.
- [ ] **Step 3: Implement** the fields and the branches. Keep each relaxation next to the rule it relaxes (`if !c.Demo { …today's rule… } else { …demo rule… }`), not in a separate function, so a reader sees both.
- [ ] **Step 4: Run** `go test ./internal/factory/config/ -count=1`. Expected: PASS, the existing tests included.
- [ ] **Step 5: Docs.** `docs/factory.md`: a `demo` section listing the table above, and `merge.gateStatus` and `merge.policyBot` in the merge keys.
- [ ] **Step 6: `task check`, then commit:** `feat(config): demo mode, for the factory on a laptop`.

---

### Task 2: Startup without a CA, a meter or a human verifier

**Files:**
- Modify: `internal/app/factory.go` (`runFactory` `:131`; `LoadCA` `:152-159`; `NewVM` `:160-163`; `NewVerifier` `:236`; the meter wiring `:339-344`; `access()` in `factory_repos.go:133`)
- Create: `internal/factory/meter/off.go`, `internal/factory/meter/off_test.go`
- Test: `internal/app/factory_test.go`

**Interfaces:**
- Consumes: `Config.Demo` (Task 1).
- Produces: `meter.Off` implementing the same interface `meter.NewVM` returns (read it in `internal/factory/meter/vm.go:40-54`), returning zero usage and no error; its `String()` or name is `"off (demo)"`.

- [ ] **Step 1: Failing tests.** `off_test.go`: `Off` reports zero for any run and a non-error. In `factory_test.go`, following the existing tests that build the manager options (`:56`): with a demo config (no CA, no meter, no human issuer), the start-up helpers build a broker client with `http.DefaultTransport`-style roots (no CA load), `meter.Off`, and no verifier, and none of them performs a network call (use an unroutable issuer URL in the non-demo test to show today's path still fails fast).
- [ ] **Step 2: Run, see fail.**
- [ ] **Step 3: Implement.**
  - `LoadCA` only when `cfg.Broker.CAFile != ""`.
  - `meter.Off{}` when `cfg.Meter.URL == ""`. Skip `meter.VL` when `LogsURL` is empty.
  - When `cfg.Demo`, skip `authn.NewVerifier` and give `api.Authenticator` a verifier that refuses every human token with `demo_no_human_tokens`. System tokens (`SystemIssuer`) keep today's path when set.
  - Factor each choice into a small function (`brokerClient(cfg)`, `usageSource(cfg)`, `humanVerifier(ctx, cfg)`) so the test calls them directly.
- [ ] **Step 4: Run** `go test -race ./internal/app/ ./internal/factory/meter/ -count=1`. PASS.
- [ ] **Step 5: `task check`, commit:** `feat(factory): start without a CA, a meter or a human verifier in demo mode`.

---

### Task 3: Mint a token from the App, and keep `agent-git` filled

**Files:**
- Modify: `internal/factory/forge/app.go` (`tokens` `:64-77`, `get` `:81`, `sign` `:105`, `find` `:134`, `mint` `:152`, `call` `:174`)
- Create: `internal/factory/demo/gittoken.go`, `internal/factory/demo/gittoken_test.go`
- Modify: `internal/app/factory.go` (register the runnable when `cfg.Demo`)
- Modify: `internal/factory/config/config.go` (`Demo` block: `demo.gitSecret {namespace, name}`, default `runs` namespace from `Runs.Namespace` and name `agent-git`)

**Interfaces:**
- Produces in `forge`:

```go
// MintOptions names the App, the repositories and the permissions of one installation token.
type MintOptions struct {
	AppIDFile, KeyFile string
	Owner              string            // the installation's account
	Repositories       []string          // names only, no owner
	Permissions        map[string]string // e.g. {"contents": "write"}
}

// Token is an installation token and when GitHub says it expires.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Mint signs a JWT from the App's files, finds the installation and mints one token, uncached.
func Mint(ctx context.Context, c *http.Client, apiURL string, o MintOptions) (Token, error)
```

- Produces in `demo`: `type GitToken struct{ Client client.Client; Mint func(context.Context) (forge.Token, error); Namespace, Name string; Every time.Duration; Clock func() time.Time }` implementing `manager.Runnable` and `NeedLeaderElection() bool { return true }`. It writes Secret `Name` in `Namespace` with key `token`, labels `app.kubernetes.io/managed-by: maku-factory`, and annotation `maku.ogenki.io/expires-at` (RFC 3339); it rewrites every `Every` (default 30m) and immediately when the annotation is within 10 minutes of expiry. A mint failure keeps the old Secret, logs, and retries after 1 minute.

The demo's token permissions, in one place (`demo.Permissions`): `contents: write`, `pull_requests: write`, `issues: write`, `metadata: read`, `statuses: write`. Repositories: every name in `api.repositories` (or the configured repositories list the factory already serves; read `connectRepositories`, `factory_repos.go:40`).

- [ ] **Step 1: Failing tests.**
  - `forge`: refactor-safety. The existing app.go tests still pass through `tokens.get`, now built on `Mint`. A new `TestMintSendsRepositoriesAndPermissions` against an `httptest` server asserts the access-token request body `{"repositories":[...],"permissions":{...}}` and parses `expires_at`.
  - `demo`: with a fake client and a fake mint: the first `Start` tick creates the Secret; a later tick updates it; a mint error leaves the old value; the annotation drives an early refresh.
- [ ] **Step 2: Run, see fail.**
- [ ] **Step 3: Implement** `Mint` by moving `sign`, `find` and `call` behind it; `tokens.mint` calls `Mint`. Implement `GitToken`. Register it in `runFactory` when `cfg.Demo`.
- [ ] **Step 4: Run** `go test -race ./internal/factory/forge/... ./internal/factory/demo/ -count=1`. PASS.
- [ ] **Step 5: `task check`, commit:** `feat(factory): demo mode keeps the runs' git token fresh from the factory App`.

---

### Task 4: Public repositories only

**Files:**
- Create: `internal/factory/forge/visibility.go` (and add the method to the `TaskForge` interface, `reconciler.go:67`, the GitHub implementation and `forgetest.Fake`)
- Modify: `internal/factory/intake/issues.go` (`IssuePoller.one` `:296`, after the writer check)
- Modify: `internal/factory/narrate/narrate.go` (reason map `:236-250`)
- Modify: `internal/app/factory.go` (a start-up check over every served repository when `cfg.Demo`)
- Tests: `internal/factory/intake/issues_test.go`, `internal/app/factory_test.go`

**Interfaces:**
- Produces: `TaskForge.Public(ctx context.Context) (bool, error)`: GET `repos/{owner}/{repo}`, true when `private` is false. The fake returns a settable value (`SetPublic(bool)`).

- [ ] **Step 1: Failing tests.**
  - Poller, demo on: a private repository's labelled issue is refused once with reason `private_repository`, the label is removed and no Task is created. Demo off: the same issue proceeds, `Public` is never called.
  - Start-up: demo with one private repository exits with `demo: <owner/repo> is private; the demo serves public repositories only`.
  - A `Public` error refuses (fail closed) and retries on the next poll; it does not remove the label.
- [ ] **Step 2: Run, see fail.**
- [ ] **Step 3: Implement.** Reason text in `narrate.go`: "This factory runs as a demo, which serves public repositories only: in the demo, an agent can read the model key and the GitHub token."
- [ ] **Step 4: Run** `go test -race ./internal/factory/intake/ ./internal/app/ -count=1`. PASS.
- [ ] **Step 5: `task check`, commit:** `feat(factory): the demo serves public repositories only`.

---

### Task 5: The gate posts `maku/merge-gate`, and can gate without policy-bot

**Files:**
- Modify: `internal/factory/forge/forge.go` (`Merger` interface `:495-535`), `github.go` (a `Repositories.CreateStatus` call), `absent.go`, `forgetest/fake.go` (record statuses; `Statuses(sha) []forge.Status`)
- Modify: `internal/factory/forge/app.go` (`MergerPermissions` `:51`: add `statuses: write` when asked)
- Create: `internal/factory/reconciler/gatestatus.go`, `gatestatus_test.go`
- Modify: `internal/factory/reconciler/arm.go` (`baseChecks` `:361`, policy-bot rows `:402-415`), `merge.go` (`awaitingCI` `:31`, after the decision is final `~:164`)
- Tests: `arm_test.go` (`TestDecideArm` `:99`), `merge_test.go` (`mergeRig` `~:34`)

**Interfaces:**
- Consumes: `Merge.GateStatus`, `Merge.UsesPolicyBot()` (Task 1).
- Produces: `Merger.SetStatus(ctx context.Context, sha, state, context, description string) error`; `statusFor(d ArmDecision, class string) (state, description string)`; the constant `GateStatusContext = "maku/merge-gate"`.

`statusFor`, exactly (description at most 140 characters, GitHub's limit):

| `d.Verdict` | state | description |
|---|---|---|
| `arm` | `success` | `<class>: merging` |
| `shadow` | `success` | `<class>, shadow: would merge` |
| `wait` | `pending` | `<class>: waiting, <reason>` |
| `human` | `pending` | `<class>: a human decides, <reason>` |
| `ci_red`, `ci_cancelled` | `failure` | `<class>: <reason>` |
| `escalate` | `failure` | `<class>: escalated, <reason>` |

An empty class reads `no class`.

- [ ] **Step 1: Failing tests.**
  - `gatestatus_test.go`: one row per verdict above, and a long reason truncated to 140.
  - `arm_test.go`: with `PolicyBot` false, a green PR without `policy-bot: main` no longer gives `policy_absent`; with it true (default) the existing rows at `:134-136` are unchanged.
  - `merge_test.go`: with `GateStatus` true, each decision path in `awaitingCI` calls `SetStatus` once per head with `statusFor`'s values; the same head and the same state are not posted twice (read the fake's statuses); with `GateStatus` false, `SetStatus` is never called. A `SetStatus` error is logged and does not change the decision.
- [ ] **Step 2: Run, see fail.**
- [ ] **Step 3: Implement.** In `baseChecks`, wrap the policy-bot rows in `if rc.Merge.UsesPolicyBot()`. In `awaitingCI`, after `d` is final, `if cfg.Merge.GateStatus { postGateStatus(...) }`, deduplicated on (head, state, description) using the last posted value kept on the Task's status or, if there is no field for it, by reading the commit's statuses first (`PullRequestChecks` already returns them: `Checks.StatusOf(GateStatusContext)`). When `GateStatus` is on, connect the merger with `statuses: write`.
- [ ] **Step 4: Run** `go test -race ./internal/factory/reconciler/ ./internal/factory/forge/... -count=1`. PASS.
- [ ] **Step 5: `task check`, commit:** `feat(gate): post maku/merge-gate, and gate without policy-bot in demo mode`.

---

### Task 6: One App, end to end in the config

**Files:**
- Modify: `internal/factory/config/demo_test.go`, `internal/app/factory_test.go`
- Modify: `docs/factory.md`

The demo uses one App: `github.mergerAppIDFile` and `github.mergerKeyFile` name the factory App's own files. This task proves that works without code changes, or makes the smallest change that does.

- [ ] **Step 1: Failing test.** `demoGood` with the merger files equal to the App files and one class in shadow parses; `connectRepositories` with that config builds a working merger over the same key (use the existing httptest-backed GitHub fake if `factory_repos` tests have one; otherwise assert `connectMerger` returns a non-`Absent` merger).
- [ ] **Step 2: Run.** If it passes untouched, keep the test as the proof and go to Step 4.
- [ ] **Step 3: Implement** only what failed.
- [ ] **Step 4: Docs:** `docs/factory.md`'s demo section states that the merger files may name the factory App's files, and that the App then needs the union of both permission sets plus `statuses: write`.
- [ ] **Step 5: `task check`, commit:** `test(factory): the demo's one App is the factory and the merger`.

## Self-review

- Spec coverage: Q8 (no login: Task 2's no human tokens, the broker keeps `human:local`), Q12 (Task 5), Q13 (shadow default is config; Task 5 posts the shadow verdict), Q14 (Tasks 2, 3 and 6), Q15 (Task 4). Q4's roles: Task 1 lifts the scoper and loop refusals. The chart, `task demo`, the template repository, CI and docs are plans B to D.
- Production unchanged: each task carries a non-demo test.
- Open at execution: whether the run-controller passes the judge model (Task 1's loop row). The implementer verifies and the ruling goes in the ledger.

---

### Task 7: The portable AgentRun carries the loop policy (added at execution)

Added by the controller after Task 1 found that the portable AgentRun's `spec.loop` carries only `checks` and `search`. Q4 puts the completion judge in the demo's loop, and the judge only runs when the harness receives the loop policy.

**Files:**
- Modify: `api/v1alpha1/agentrun_types.go` (`Loop`: `StallSteps int32`, `Continuations int32`, `JudgeModel string`, each `omitempty`, with the same validation bounds as `api/factory/v1alpha1/task_types.go:199-207`), the generated CRD and deepcopy (`task crd:gen`)
- Modify: `internal/runctl/pod.go` (`AGENT_LOOP` as the harness README describes: `images/maku-harness/README.md:27-56`, a JSON object of the set fields; and whatever the judge needs to reach a model: read the README's judge proxy section and `images/maku-harness/agent_run.py` around `JUDGE_TIMEOUT_S`)
- Modify: `internal/factory/runs/runs.go` (`Build`: the loop fields reach the portable kind exactly as they reach `cloud.ogenki.io`)
- Modify: `internal/factory/config/config.go` (drop Task 1's demo refusals for the fields now passed)
- Tests: `internal/runctl` (envtest: a run with a loop gets `AGENT_LOOP` with exactly its fields; none without a loop), `internal/factory/runs` (Build carries the fields), `internal/factory/config/demo_test.go` (the refusals are gone)

- [ ] Step 1: read how the reference host's composition gives the harness its loop and its judge (the harness README is the contract). If the judge needs a component the portable runtime lacks (a proxy sidecar, a gateway route), report BLOCKED with what it needs rather than building it.
- [ ] Step 2: failing tests. Step 3: run, fail. Step 4: implement. Step 5: `go test -race ./internal/runctl/ ./internal/factory/runs/ ./internal/factory/config/ -count=1`. Step 6: `task check`; commit `feat(runctl): the portable AgentRun carries the loop policy`.
