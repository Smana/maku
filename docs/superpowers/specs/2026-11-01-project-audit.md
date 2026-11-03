# maku: project audit, 2026-11-01

Audit at `2816bced` (v0.32.3), five parallel reviewers (security, rooms correctness, factory
correctness, structure/docs/dead code, web/charts/images/scripts) plus the automated gate. Input for
the improvements spec; nothing here is decided.

**✓** = re-verified by hand after the reviewer reported it. Everything else was confirmed by the
reviewer reading the code unless marked *plausible*.

## Baseline

| Check | Result |
|---|---|
| `task lint` (golangci-lint v2) | 0 issues |
| `task vuln` (govulncheck 1.8.0) | 0 reachable; 3 in required modules, not called |
| `go test -race` (all but `internal/store`, no Docker) | pass |
| `spdx`, `tidy`, `docs` | pass |
| `hack/names.sh` | **fails locally**: descends into the untracked `.claude/worktrees/` (S-11) |
| `web`: `tsc --noEmit`, vitest | pass, 782 tests |
| `helm lint --strict`, both `render.sh` | pass |
| `deadcode -test ./...` | 2 symbols |

Prior audit (`2026-10-15-code-health-design.md`): 10 findings spot-checked (B1, B3, B9, B12, B13,
B16, F5, F6, F11, F15), all closed. Still open from it: plaintext on the factory API `:8082` (S-1).

## High

| # | Where | Defect | Failure scenario | Fix direction |
|---|---|---|---|---|
| H-1 ✓ | `internal/store/bridges.go:61` (`claimFree`), `internal/bridgeapi/server.go:310` (`hello`) | hello never answers 410 on a sealed room: `claimFree` reads `bridge_run`, never `sealed` | A room closed by a human; a queued or resumed run starts, hello gives 200, the gate releases the harness, the first batch gets 410, the bridge stops mirroring and the harness runs to budget unrecorded. `bridge/admission_test.go` fakes the 410 the server never sends | `SELECT sealed … FOR UPDATE`, return `ErrSealed`; a bridgeapi test; consider revoking live runs on `close` |
| H-2 ✓ | `internal/factory/reconciler/merge.go:886-927` | `MainState` maps CANCELLED to PENDING; `verifying` has no deadline. `merge.awaitingCIFor` defaults to unbounded. `forge.CommitChecks` (`github.go:931`) reads one page of 100 with no truncation flag | `cancel-in-progress` on main cancels the merge commit's run: the Task sits in Verifying forever, counted active by `slotFree`; enough of them and no task starts. A failing check on page 2 reads SUCCESS | Bound Verifying (e.g. 2× `verifyFor`, then end inconclusive / hand off); default a bound for AwaitingCI; paginate or flag truncation |
| H-3 ✓ | `internal/factory/forge/github.go:1193` | `Diff` refuses only `len(Files) == 100`; GitHub lists up to 300 on page one (`ChangedFiles` already uses `>= 300`) | A ≥300-file diff on a class with `maxPatchFiles: 0`: links-only / semver / shape checks judge a partial diff and the PR can arm | `>= maxChangedFiles`; a test at 300 |
| H-4 *plausible* | `internal/factory/meter/meter.go:405,426-431` | One summed counter across gateway pods; a pod's series dropping out re-baselines, its return adds its whole counter again | Replica B's scrape goes stale for a tick: B's tokens are counted twice, the ledger overcharges, a run can be revoked under budget | Monotonicity per series, or `last_over_time` in the query |
| H-5 ✓ | `charts/maku-factory/templates/rbac.yaml:47-52` | AgentRun Role hardcodes `namespace: agents`, `apiGroups: [cloud.ogenki.io]`; the binary takes both from `config.runs` | `config.runs.group=maku.ogenki.io` (the umbrella's group): every AgentRun call is forbidden. `render.sh:70` asserts only `has("verbs")` | Template from `.Values.config.runs`; assert in `render.sh` |
| H-6 ✓ | `hack/quickstart/up.sh:65-78`, `fetch-runsc.sh:34` | `up.sh` installs `.quickstart/bin/maku`, then `fetch-runsc.sh` does `rm -rf .quickstart/bin` | First quickstart run (and every `GVISOR_RELEASE` bump) deletes the maku it told the user to put on PATH. CI misses it: e2e pre-fetches runsc | Remove only runsc files and the stamp, or install maku after |

## Medium

### Security and deployment

| # | Where | Defect | Fix direction |
|---|---|---|---|
| S-1 | `internal/factory/api/server.go:204`, `internal/runrequest/runrequest.go:110` | Human ZITADEL bearer tokens (12 h) cross pod-to-pod to `:8082` in plaintext; `docs/security.md:122` says the CNI has no WireGuard — the reason `:8443` has TLS. Open since 2026-10-15 | TLS on `:8082` with the reloading `GetCertificate`; broker refuses a non-`https` `factoryURL` |
| S-2 | `charts/maku/templates/broker-rbac.yaml:52` | Broker holds `patch` on AgentRuns (only `suspend` needs annotations); `docs/security.md:100` says it has get/list/watch/delete. RBAC can't scope a patch to annotations: a compromised broker can rewrite a run's spec | Admission policy (VAP/Kyverno) limiting the broker SA to the suspend annotations; doc fix |
| S-3 | `charts/maku/templates/run-controller-rbac.yaml:180-190` | Run controller can `get` the broker's TLS Secret, `tls.key` included; it needs `ca.crt` only | CA-only Secret/ConfigMap (trust-manager Bundle) |
| S-4 | `charts/maku/templates/broker-cnp.yaml:11-54` | No ingress `:8443` from the factory, no egress to `factoryURL:8082` | Opt-in rules keyed on `factoryURL` |
| S-5 | `charts/maku/templates/run-controller-rbac.yaml:139-149`, `tests/render.sh:82-100` | Role follows `runs.group`; the controller is fixed at `maku.ogenki.io`. The test asserts the broken combination | `fail` when `runController.enabled` and group ≠ `maku.ogenki.io` |
| S-6 | `internal/factory/sanitize/public.go:116-125` | A backslash before the opening backtick escapes it: `Public("\\@smana")` renders a live mention, `\#12` a live backlink | Double a preceding backslash; test |
| S-7 | `internal/factory/config/config.go:1271-1295` | `Validate` neither checks the templates `Matrix` picks nor pins `investigate` to `[triager]` | `investigate: {roles: [implementer]}` runs an implementer on internal data (R38) — validate both |
| S-8 | `.github/dependabot.yml`, `taskfile.yaml:142` | No npm ecosystem; `npm ci --no-audit`. markdown-it (the untrusted-text parser) is never bumped or scanned | npm ecosystem for `/web` |
| S-9 | `internal/humanapi/summary.go:41-60`, `rooms.go:422` | Summary folds the whole log (≤100k events, ~200 queries) per request; summary and room list have no per-principal limit. `maku mcp` polls `runTaskStatus` every 5 s per task | Per-principal limiter; fold cache keyed by `LastSeq` |

### Factory correctness

| # | Where | Defect | Failure scenario |
|---|---|---|---|
| F-1 *plausible* | `forge/github.go:987-995` | `ErrHeadMoved` / `ErrNotMergeable` key on HTTP 409/405; GraphQL `mergePullRequest` answers 200 with `errors[]` | A not-mergeable PR retries forever, holding a slot; tests fake the 409 |
| F-2 | `reconciler.go:569-577`, `monitor.go:709-723` | Outbox unbounded in code, CRD `maxItems=16`; `followUpFound` queues 2 per follow-up × 16 | Over 16, every `Status().Update` is Invalid and `drain` never runs: the Task wedges |
| F-3 | `reconciler.go:580-596`, `roomclose.go:27` | `drain` stops at the first failure; `forge.Comment` has no permanent class | A deleted/locked issue (404/410/403) blocks all later narration and the room never closes |
| F-4 | `narrate/narrate.go:801`, `watch.go:338-381` | Escalation key `end-escalated-<len(Runs)>`: a retry that escalates before a new run posts nothing | Maintainer sees "retrying…", then silence; budget-task retries can never succeed |
| F-5 | `intake/issues.go:246-259,311,360` | `Generation` counts label events by *current* maintainers; removing one lowers the count, `IssueKey` collides, AlreadyExists read as dedup | The label is removed, no Task, no comment |
| F-6 | `intake/issues.go:503-595` | One failing stop/revert item aborts the poll | A locked issue stalls the repository's intake every minute |
| F-7 | `api/server.go:203-216` | `Start` returns on `ErrServerClosed` without awaiting the drain | SIGTERM between `Create(Task)` and `CreateIssue`: Task with no issue, replays answer `duplicate` |
| F-8 | `intake/runlore.go:231-241` | `CreateIssue` on `r.Context()` | Caller hang-up leaves an issue-less Task forever |
| F-9 | `reconciler.go:298-300` | span-retry early return sits before the terminal block | Collector down (≤24 h): ended tasks don't close rooms; revert watch and settle slow to 15 min |
| F-10 | `slackapp/app.go:174-180,251` | `go f()` per event, unbounded, unowned | SIGTERM after `RunCreate`: `Threads.Put` and the reply are lost; Slack won't retry |
| F-11 *plausible* | `forge/github.go:1032`, `merge.go:965` | `RevertPR` not idempotent | Timeout after GitHub commits: a second revert, counted by the breaker |

### Rooms correctness

| # | Where | Defect | Failure scenario |
|---|---|---|---|
| R-1 *plausible* | `internal/humanapi/fork.go:111-119` | Cleanup `CloseRoom` on the same expired act context, error ignored | A large fork times out on Create: the copy is never sealed, never purged, and sits in every `NOT sealed` sweep |
| R-2 | `internal/roomctrl/reconciler.go:251` | `room.Status == want` compares `Task *TaskStatus` by pointer | Every room with a task PATCHes status on every 15 s resync — use `equality.Semantic.DeepEqual` as `runctl` does |

### UI

| # | Where | Defect |
|---|---|---|
| U-1 ✓ | `web/src/controls.ts:219`, `fork.ts:21`; `docs/api.md:452`, `cli.md:51,60` | `tester` offered; server's `runRoles` refuses it (`startrun.go:23`); `test/fork.test.ts:66` uses it |
| U-2 | `web/src/summary.ts:40,117-127` | `answer` needs (scoper bounce) never render in Needs-you or the pill count |
| U-3 | `web/src/summary.ts:121-124` | Approve-to-merge needs still labelled "Merge:" — the wording #308/#310 removed elsewhere |

### Structure

| # | Where | Defect | Fix direction |
|---|---|---|---|
| A-1 | `internal/factory/api/{ledger,server,settled}.go` | The HTTP server package is the budget-ledger library; intake, meter and reconciler import it for that | Extract `internal/factory/ledger` |
| A-2 | `internal/runwatch/kind.go:36-39,85` | Package-global `atomic.Pointer` + `init()` + process-wide `Configure` — against the conventions; tests share one value | Inject a `Kind` through constructors |
| A-3 | `CLAUDE.md`/`AGENTS.md` package map, `docs/architecture.md` | 13 packages missing (`factory/{decision,fence,filefence,finding,fluxwatch,mdlinks,semverdiff,steering}`, `{migrategroup,roomview,runctl,sponsored,webhook}`) + `cmd/maku-run-controller`; runwatch seam row stale (now configurable); runctl/migrategroup lack seam rows | Doc fix |

### Ops and tooling

| # | Where | Defect |
|---|---|---|
| S-10 | repo root | No `.dockerignore`; Dockerfiles `COPY . .` — local builds ship `.git`, `node_modules`, `.claude/` (3 GB) and `.quickstart/` (kind admin kubeconfig) into the build stage |
| S-11 ✓ | `.gitignore`, `hack/names.sh`, `spdx.sh` | `.claude/` not ignored: 42 orphaned worktrees (3 GB) make `names` fail locally (25k false hits) |
| S-12 | `scripts/shadow-merge-tally.sh:89` | A failed `gh` search inside a process substitution halves the tally silently; reproduced `widen=yes`, rc 0 |

## Low (selected)

- **Rooms:** fanout `Subscribe` race recreates a room at `last: 0` and floods subscribers (`fanout/hub.go`); `maku mcp` relay blocking send on a cap-1 channel (`maku/mcp.go:~395`); `suspend.Room` stops at the first patch error; bridgeapi registry has no `closing` flag; humanapi limiter maps never evicted (`acts.go:162`, `mcp.go:354`); per-item transactions in a 500-item batch (`bridgeapi/events.go:88`).
- **Factory:** `merge.go:1026,1055` append to `narrated` bypassing the 512 trim; CEL `maxMicroUSD` transition rule lacks `has()` (`task_types.go:107`); `forge/app.go:81` never evicts on 401; `deadlinks.go:100` unanchored `allowedFiles`; `schedule.go:178` drops a new pair on AlreadyExists; `killswitch/sweep.go:52` loses metrics on error; `issues.go:562` swallows revert errors; `classify.go:113` oracle-class label; `runlore.go:206` ignores `Withheld`; `api/server.go:378,751` reservation drop on committed claim; `api/internal.go:348` generation collision; same Idempotency-Key + different body both 201; nil-unsafe `Metrics`/`a.Now()` calls; global `editMu` held 30 s; `runrequest` 15 s < factory's 30 s; `forgetest` `RecentComments` order reversed vs the real client; `githubLedger` re-reads on every pass when truncated.
- **Security:** `baseRef` unvalidated into git argv and rules.md (`api/server.go:442`); webhook dial guard misses `::/96`, `2001::/32`, `fec0::/10` (`webhook/dial.go:16`); `ROOMS_MCP_KEY` read once from env; Slack server lacks `IdleTimeout` (`app/slackapp.go:207`); `agents` namespace has no PSA `enforce` label; factory chart has no NetworkPolicy; images use `USER nonroot:nonroot` (kubelet needs a numeric uid for `runAsNonRoot`); digest regex accepts truncated digests; local mode gives any local process the web UI's standing (doc it).
- **Charts:** factory PDB `minAvailable: 1` at 1 replica blocks drains; factory container ports hardcoded vs `config.api.listen`; run-controller `:9090` unscrapeable; no broker PDB; render tests assert less than they claim.
- **UI:** markdown allows `http:` (docs say https; three link helpers disagree); `localStorage` read outside the try (`theme.ts:64`, `autorefresh.ts:52`); `ChatView.pending` never pruned; frames unchecked (`seq` NaN wedges `SeqTracker`); unquoted values in the copy-paste shell command (`view.ts:333`); repo filter built once.
- **Scripts:** `--limit 500` truncates silently; gVisor/maku checksums fetched from the same bucket as the artifact; `bash -n` in `check.sh:11` checks only the first file.
- **Convention:** direct `time.Now()` in CLI deadline code (`maku/roomwait.go:195`, `run.go:145`); `var elicitWait` mutable (`maku/mcp.go:38`); `context.Background()` in metric callbacks (`app/factory.go:123`, `rooms.go:90`, `approvals.go:69`); factory verifiers built without `WithLogger`.

## Complexity

| Function | Lines | Cyclo |
|---|---|---|
| `factory/config.(*Config).Validate` `config.go:1163` | 272 | 137 |
| `reconciler.awaitingCI` `merge.go:31` | 248 | 69 |
| `app.runFactory` `factory.go:129` | 237 | 39 |
| `meter.(*Meter).Tick` `meter.go:678` | 169 | 46 |
| `api.createTask` `tasks.go:79` | 158 | 41 |
| `mcp.RoomTools` `tools.go:65` | 157 | 47 |
| `app.serveBroker` `broker.go:142` | 154 | 30 |
| `reconciler.queued` `reconciler.go:900` | 148 | 48 |
| `maku.Checkout.Run` `checkout.go:89` | 148 | 49 |
| `roomview.Milestones` `changes.go:118` | 147 | 46 |

Also: `Reconcile` cyclo 53, `validateClass` 77, `validateRepository` 58, `HumanConfig.validate` 48.
Large files: `factory/config/config.go` 1849, `meter/meter.go` 1558, `reconciler/merge.go` 1203;
`internal/app/maku*.go` ~1.5k LOC of CLI logic in the wiring package.

**Duplication:** three rate limiters (bridgeapi, humanapi acts, humanapi mcp); the `now()` fallback in
11 packages; the nil-logger fallback 22×; the tier list 4× (`config.go:1003`, `fmetrics.go:59`,
`outcome.go:81`, `runs.go:155`); three golden-update switches (`-update-golden`, `-update`,
`UPDATE_GOLDEN`); `el()`/`cls()` and three URL-safety helpers in `web/`; five Go Dockerfiles differing
by the binary name; the six-image matrix 4× and the sign job 2× in workflows.

**Coupling:** `fmetrics` imports narrate/webhook/runs/killswitch for vocabularies and does a live
`killswitch.Engaged` read; `humanapi` imports `bridge` for one function (`Stricter`) and
`factory/sanitize`.

## Dead code

| Symbol | Where | Verdict |
|---|---|---|
| `WithMaxStale` | `authn/jwt.go:135` | delete (unused even by tests) |
| `webhook.Limits` | `webhook/events.go:27` | delete |
| `wire.FrameTransient` | `wire/browser.go:21` | delete (never sent, never handled) |
| `egress.new` rule | `web/src/approval-help.ts:43` | delete (class never approvable) |
| `NewVerifierWithKeyfunc`, `With{Clock,HTTPClient,CacheTTL,MinRefreshInterval}` | `authn/jwt.go:115-208` | test-only: keep as option API or move |
| `pageItemBytes`, `nextPeakBytes`, `batchBody` | `bridge/batch.go:20,37,122` | move to `_test.go` |
| `Harness.{Ready,Next,Skip,Send}` | `bridge/harness.go` | test-only wrappers |
| `SettledByRun`, `deadLinks`, `Snapshot`, `runCost`, `FirstBrief`/`ReviseBrief`, `triage.Static`, `summary.TaskPhases`/`Fold` | factory, summary | test-only |
| `IgnoredFactoryDaily`, `IgnoredHumanDaily`, `PerPerson.IgnoredDailyTokens`, no-prices branch | `factory/config/config.go:492-508,1056` | delete: "one release" compat from v0.16.0 |
| `internal/migrategroup`, `migrate-group`, `migrate-e2e.sh`, `docs/maku-group-migration.md` | — | schedule removal once every cluster migrated |
| `web/preview/list.js` | tracked build output | untrack, gitignore |
| 29 plans + 8 specs in `docs/superpowers/` | shipped work | archive (keep the two reader pages link) |

## Docs drift

README "Status v0.15.2" (latest v0.32.3); architecture.md "Four binaries" (six, `maku-slack`
absent from binaries and ports tables; bridge health `:8085` missing); gate table lists 12 steps,
CONTRIBUTING says eleven, taskfile runs 14 (`names`, `quickstart:check` missing); `chart:check`
covers both charts; `maku-broker` also has `migrate`, `migrate-group`; `ci.yaml:163` says the
broker chart lives elsewhere; `metrics.go:136` stale comment; `SLACK_APP_CONFIG` undocumented;
chart READMEs omit ~15 `broker.*`/`runController.*` keys and two factory secrets; the factory and
broker must share a namespace, documented nowhere.

## Checked and clean

JWT verification (alg pinning, iss/aud/exp/nbf, key sizes, JWKS caps); human authn and the policy
matrix; the D7 room access gate; WebSocket Origin/CSRF; `:8090` key compare; bridgeapi body caps,
strict decode, bounded limiter; Slack HMAC and replay window; `httpx` SSRF guards; `exec` argv-only;
SQL parameterised, append-only grants, RLS everywhere, no `SECURITY DEFINER`; redaction on every
append path; brief fencing and sanitize fixpoint; XSS (no HTML sinks in `web/`); `web/` ↔ `wire`
frame types match; reconnect/resume and bounded UI state; store transactions and row closing;
LISTEN/NOTIFY reconnect; every server's bounds (Slack aside); workflows (SHA pins, `permissions: {}`,
no `pull_request_target`); digest-pinned distroless images; chart securityContext; no secrets on argv.
