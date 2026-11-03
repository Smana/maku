# Audit fixes: design

**Status:** design, approved by the owner on 2026-11-01 in the brainstorming session that followed
the [project audit](2026-11-01-project-audit.md).
**Scope:** the audit's 6 High and the Medium findings; the Low ones stay parked in the audit.
Behaviour a user sees changes only where a finding is a bug, and in the two places a decision says
so (D2's new default, D6's refused `http://` factory URL). **Plans:** one per theme, under
`docs/superpowers/plans/2026-11-01-audit-*-plan.md`.

## Conclusion

The audit at v0.32.3 found a clean gate, a sound security model and the 2026-10-15 findings closed.
What remains are **six High defects**, each a contract the code states but does not keep: a closed
room that still hands out its bridge lease, a merged task that can sit in Verifying forever, a
diff gate blind past 300 files, a meter that can double-count, a factory chart whose RBAC ignores
its own config, and a quickstart that deletes the binary it just installed. Around them sit about
thirty Medium edges: liveness and idempotency in the factory, plaintext tokens on the run-request
API, RBAC broader than the docs, three UI renderings that lag the server, two structural knots, and
repository hygiene. Each theme below becomes one pull request; each finding is pinned by a failing
test before its fix.

## Decisions

Each rejects a named alternative.

| # | Decision | Rejected | Why |
|---|---|---|---|
| D0 | **One PR per theme**, each finding one commit with the test that pins it first; `task check` exits 0 on every PR | One sweeping PR | A money fix and a chart fix never share a revert (precedent: 2026-10-15 D1) |
| D1 | **Verifying ends `Done` with reason `merged_unverified`** once `2 × merge.verifyFor` has passed since the merge with no FAILURE and no qualifying SUCCESS, and narrates the merge SHA and the revert window. A CANCELLED check keeps meaning "keep watching" until then | Escalate to a maintainer; re-run the cancelled check | The PR is already merged: escalating holds a slot for nothing, and `factory/revert` already covers the human path. A re-run needs a new App permission |
| D2 | **`merge.awaitingCIFor` defaults to `24h`**; an explicit `0` keeps "no bound" | Keep the unbounded default | An unbounded default is H-2's stuck slot by another door |
| D3 | **`forge.CommitChecks` paginates** up to 10 pages of 100; a listing that reaches the cap reads as PENDING, never SUCCESS | A truncation flag only | Callers already treat PENDING as "wait"; no caller has to learn a new field |
| D4 | **The maku chart ships a `ValidatingAdmissionPolicy`** that lets the broker's ServiceAccount change, on an AgentRun, only `metadata.annotations` keys under the suspend prefix; `docs/security.md` lists `patch` and its bound | Kyverno; drop `patch` and move suspend elsewhere | Kyverno is the platform's, not this repository's; the VAP is in-tree Kubernetes (GA in 1.30) and testable with `helm template` |
| D5 | **A hello on a sealed room answers 410 `sealed`.** Revoking a closed room's live runs is out of scope | Fold the revoke in | It changes what `close` means to a human; it gets its own decision |
| D6 | **TLS on the run-request API is optional, the operator brings the certificate**, and the broker refuses an `http://` `factoryURL` unless `factoryInsecure: true` | Accept `http` with a warning; require TLS always | The owner's call: an explicit opt-out matches strict config and the bridge's `BROKER_URL` rule, while a cluster without cert-manager can still run |
| D7 | **The reloading certificate loader moves to `internal/tlsreload`** and both servers import it | The factory imports `bridgeapi` | A factory package importing the broker's API package inverts the stages |
| D8 | **The budget ledger moves to `internal/factory/ledger`**, a move with no behaviour change | Leave it in `api` | Three domain packages import the HTTP server to reach it |
| D9 | **`runwatch.Kind` is injected**; the package global, its `init()` and `Configure` go | Keep the global, document it | `AGENTS.md` allows no package state beyond `version.Version`; tests share one process-wide value |
| D10 | **The meter keeps one baseline per series** (labels as returned by the query), not one summed baseline | Wrap the query in `last_over_time` | `last_over_time` hides a dropout only within its window and still re-adds past it; per-series state is exact |

## Themes

Order: a theme may start once every theme with a lower number in its "After" column has merged.
Themes without a dependency run in parallel worktrees.

| Theme | Findings | After | Plan |
|---|---|---|---|
| T1 Rooms lifecycle | H-1, R-1, R-2, S-9 | — | `2026-11-01-audit-t1-rooms-lifecycle-plan.md` |
| T2 Factory liveness | H-2, F-1, F-2, F-3, F-4, F-9, F-11 | — | `2026-11-01-audit-t2-factory-liveness-plan.md` |
| T3 Gate and intake | H-3, S-6, S-7, F-5, F-6 | — | `2026-11-01-audit-t3-gate-intake-plan.md` |
| T4 Money | H-4 | — | `2026-11-01-audit-t4-meter-plan.md` |
| T5 Shutdown and ownership | F-7, F-8, F-10 | — | `2026-11-01-audit-t5-shutdown-plan.md` |
| T6 Factory API TLS | S-1 | T5 (`api.Server`'s `listen`/`serve` split) | `2026-11-01-audit-t6-factory-tls-plan.md` |
| T7 Charts and RBAC | H-5, S-2, S-3, S-4, S-5 | T6 | `2026-11-01-audit-t7-charts-plan.md` |
| T8 UI | U-1, U-2, U-3 | — | `2026-11-01-audit-t8-ui-plan.md` |
| T9 Structure | A-1, A-2 | T2, T3, T4, T5 | `2026-11-01-audit-t9-structure-plan.md` |
| T10 Hygiene and docs | H-6, S-8, S-10, S-11, S-12, A-3 | T9 (A-3's package map only) | `2026-11-01-audit-t10-hygiene-plan.md` |

### T1 Rooms lifecycle

| # | Fix | Pinning test |
|---|---|---|
| H-1 | `store.claimFree` selects `bridge_run, sealed … FOR UPDATE` and returns `ErrSealed` on a sealed row; `hello`'s `logFailure` already maps `ErrSealed` to 410 | store: `ClaimBridge` on a sealed room returns `ErrSealed` and leaves `bridge_run` unchanged; bridgeapi: hello on a sealed room answers 410 with reason `sealed` |
| R-1 | `fork`'s cleanup calls `CloseRoom` on `context.WithoutCancel(ctx)` bounded by 10 s and logs a failure (room id only) | humanapi: `Rooms.Create` fails after the act context is spent; the fake log records `CloseRoom` for the forked id |
| R-2 | `roomctrl.patchStatus` compares with `equality.Semantic.DeepEqual`, as `runctl` does | roomctrl: a reconcile whose status, `Task` included, is unchanged sends no patch |
| S-9 | `GET /api/rooms` and `GET /api/rooms/{id}/summary` pass the per-principal limiter (`Actor.limited`); `summaryOf` keeps a fold cache keyed by room, holding the `State` and its `LastSeq`, and folds only events after it; the cache is an LRU of 256 rooms | humanapi: the n+1th request within the limit's window answers 429; a second summary of an unchanged room reads no event page; after one append it reads one page from the cached seq |

### T2 Factory liveness

| # | Fix | Pinning test |
|---|---|---|
| H-2 | D1 in `verifying`; D2 in `config.ApplyDefaults`; D3 in `forge.CommitChecks` | reconciler: CANCELLED past `2 × verifyFor` ends `Done`/`merged_unverified` with one narration; PENDING before it stays. config: an unset `awaitingCIFor` reads 24h, an explicit `0s` stays 0. forge (httptest): a FAILURE on page 2 reads FAILURE; 10 full pages read PENDING |
| F-1 | `forge.(*GitHub).Merge` classifies a GraphQL `200` whose `errors[]` say the head moved or the PR is not mergeable into `ErrHeadMoved` / `ErrNotMergeable`, alongside the existing HTTP checks. The plan records the exact error strings from a live call as a fixture before coding the match | forge (httptest): a 200 with each recorded `errors[]` body maps to its sentinel; an unrecognised one stays a plain error |
| F-2 | `narrateLater` merges entries with the same key and caps `status.outbox` at the CRD's 16, dropping the oldest and logging `narration_dropped` with the task id and key; `drain` runs before the pass's first status write | reconciler (fake client enforcing `maxItems`): a monitor pass finding 9 follow-ups ends with an accepted status write and a 16-entry outbox |
| F-3 | `forge.Comment` returns a `*forge.PermanentError` on 404, 410 and a 403 whose body says the issue is locked; `drain` drops such an entry, logs it, continues | reconciler: entry 1 fails permanently, entry 2 posts, the outbox empties and the room closes |
| F-4 | The escalation key becomes `end-escalated-<len(Runs)>-<Retries>`; a `/factory retry` on a `budget-task` escalation under `enforceTask` is answered "the task's cap is part of its spec: raise it on a new task" and does not count as a retry | reconciler: a retry that escalates again before a run posts a second escalation; the budget-task retry posts the answer and leaves `Retries` unchanged |
| F-9 | The span-retry requeue folds into the terminal block's `min()` instead of returning before it | reconciler: a terminal task with a span due and an open room closes the room in the same pass |
| F-11 | `RevertPR` (GraphQL `revertPullRequest`) first lists the merger's open PRs whose head branch starts with `revert-<n>-`, GitHub's naming for that mutation, and adopts one if found; the plan confirms the naming from a live revert before coding the match | forge (httptest): a create that times out after the PR exists, retried, opens no second PR and returns the first's number |

### T3 Gate and intake

| # | Fix | Pinning test |
|---|---|---|
| H-3 | `forge.Diff` refuses at `len(cmp.Files) >= maxChangedFiles` | forge (httptest): a compare listing 300 files returns `ErrDiffTooLarge`; 299 does not |
| S-6 | `sanitize.Public` doubles a backslash immediately preceding a code span it opens, so the span cannot be escaped | sanitize: `\@x`, `\#12` and `\\@x` render through goldmark with no live mention or reference |
| S-7 | `Config.Validate` refuses a `Matrix` cell naming an undefined template, and refuses `investigate.roles` other than `[triager]` when `api` has internal principals or `runlore` is set | config table: each refused shape names its key in the error |
| F-5 | `intake.Generation` counts every label event of the trigger label, any actor; on `AlreadyExists` for a Task in a terminal phase, intake retries with generation + 1, up to 3, before treating it as a duplicate | intake: a maintainer removed from config, then a fresh label, creates a Task and comments |
| F-6 | The stop and revert passes collect per-item errors with `errors.Join` and continue; the poll returns the join after every pass ran | intake: one item whose comment fails permanently; the other items, ready intake and the orphan scan still run |

### T4 Money

| # | Fix | Pinning test |
|---|---|---|
| H-4 | D10: the meter's per-run state holds one baseline per series key (the query's label set). A missing series keeps its last value and adds nothing; a returning series with a value at or above its baseline adds the difference; below it, a counter reset, adds the new value | meter, from recorded `api/v1/query` replies: (a) B drops out one tick and returns, B's tokens counted once; (b) A restarts (counter falls), A's post-restart tokens counted once; (c) the existing single-series cases unchanged |

### T5 Shutdown and ownership

| # | Fix | Pinning test |
|---|---|---|
| F-7 | `api.Server.Start` returns only after `Shutdown` returns (the broker's idiom, `internal/app/broker.go`) | api: a handler blocked mid-write when ctx is cancelled completes its write before `Start` returns |
| F-8 | `runlore` files the issue and patches the Task on `context.WithoutCancel(r.Context())` bounded by 30 s | intake: the client cancels after the Task's create; the issue is filed and the Task patched |
| F-10 | `slackapp` runs events on 8 workers fed by a 64-slot queue; a full queue answers 503 so Slack retries; `Wait()` joins the workers and the app's drain calls it | slackapp: 65 queued events, the 65th gets 503; cancel during an event, `Wait` returns after its `Threads.Put` |

### T6 Factory API TLS

| # | Fix | Pinning test |
|---|---|---|
| S-1 | D7: `bridgeapi.TLSConfig` moves to `internal/tlsreload.Config(certFile, keyFile, log)`; bridgeapi calls it. The factory's `api` config gains `tls: {certFile, keyFile}`, both or neither; set, `Start` serves `ListenAndServeTLS` with TLS 1.3 minimum. The broker config gains `factoryCAFile` (read at use into the run-request client's root pool) and `factoryInsecure`; `Validate` refuses an `http://` `factoryURL` without `factoryInsecure: true` | tlsreload: the moved tests. factory config: half-set `tls` refused. api: a TLS round trip with an `httptest`-issued cert. broker config table: `http` refused, `http` + `factoryInsecure` accepted, `https` accepted. runrequest: a server signed by the CA in `factoryCAFile` is reached; another CA is refused |

The factory chart gains `api.tls: {secretName, issuerRef: {kind, name}}` with `broker.tls`'s shape: a
cert-manager `Certificate` for the factory Service's names when `issuerRef.name` is set, the Secret
mounted read-only and `config.api.tls` pointed at it, the port named `https`. The maku chart gains
`broker.factoryCA.secretName` and `broker.factoryInsecure`. `docs/integration.md` and the release
notes state the upgrade step; the roadmap row "TLS on the factory's run-request API" closes.

### T7 Charts and RBAC

| # | Fix | Pinning assertion (`yq` in `render.sh`) |
|---|---|---|
| H-5 | The factory's AgentRun Role takes `namespace` and `apiGroups` from `.Values.config.runs`, defaulting to today's values | rendered with `config.runs.group=maku.ogenki.io,namespace=runs2`: the Role reads those |
| S-2 | D4: a `ValidatingAdmissionPolicy` + binding scoped to the broker's ServiceAccount on `agentruns` `UPDATE`: `object.spec == oldObject.spec`, and every changed annotation key starts with the suspend prefix | rendered policy exists with the expression; a `kubectl`-free CEL check is out of reach, so the plan adds an envtest case in Go if the API server in envtest supports VAP, else the render assertion only |
| S-3 | The run controller reads the broker CA from `broker.tls.caSecretName` (a Secret holding `ca.crt` only); its Role's `resourceNames` names that Secret, never the key pair's | rendered Role has no `get` on `broker.tls.secretName` |
| S-4 | `broker-cnp.yaml` adds, when `factoryURL` is set, ingress on `:8443` from `broker.factoryPodLabels` and egress to the factory's host and port | rendered CNP with `factoryURL` set carries both rules |
| S-5 | The chart `fail`s when `runController.enabled` and `runs.group != maku.ogenki.io` | render exits non-zero with that message; `render.sh:82-100`'s contrary assertion is corrected |

### T8 UI

| # | Fix | Pinning test (vitest) |
|---|---|---|
| U-1 | `tester` leaves both role selects, `docs/api.md` and `docs/cli.md`; `test/fork.test.ts` uses `reviewer` | the selects' options equal the server's `runRoles` |
| U-2 | The `Need` type gains `kind: "answer"` and `author?`; `needsItems` renders it linked to the issue through `safeLink`; the pill count includes it | a summary with an `answer` need renders one item and counts one |
| U-3 | A merge need with `approve` set renders "Approve:" | the approve-to-merge fixture renders "Approve:", not "Merge:" |

### T9 Structure

| # | Fix | Check |
|---|---|---|
| A-1 | D8: `Release`, `DayUsed`, `SettledColumn`, `SettledByRunDay`, `LedgerDayLayout`, `LedgerPrefix` and their tests move to `internal/factory/ledger`; `api`, `intake`, `meter`, `reconciler` import it | `go list -deps` of `intake`, `meter` and `reconciler` no longer includes `internal/factory/api`; tests unchanged and green |
| A-2 | D9: `runwatch.Kind` is a value built from config in `internal/app` and passed to the roomctrl, suspend, bridgeapi, runs and runwatch constructors; `current`, `init()` and `Configure` are deleted | `grep -n 'func init' internal/runwatch` prints nothing; tests that set a kind do so per instance |

### T10 Hygiene and docs

| # | Fix | Check |
|---|---|---|
| H-6 | `fetch-runsc.sh` removes only `runsc`, `containerd-shim-runsc-v1`, `gvisor-bin/` and the stamp | a shell test: a file `maku` in the dir survives a refetch |
| S-8 | `dependabot.yml` gains the `npm` ecosystem for `/web`, same cooldown and major-ignore as the others | file review |
| S-10 | A root `.dockerignore`: `.git`, `.claude`, `.quickstart`, `web/node_modules`, `**/__pycache__` | `docker build` context size, noted in the PR |
| S-11 | `.gitignore` gains `.claude/` | `hack/names.sh` passes with worktrees present |
| S-12 | `shadow-merge-tally.sh` captures each search into a variable under `set -e` before `jq -s` | `scripts/shadow-merge-tally_test.sh` gains a failing-search case expecting a non-zero exit |
| A-3 | `AGENTS.md`: the 13 missing packages and `cmd/maku-run-controller` in the map, the runwatch seam row (configurable kind), rows for runctl and migrategroup, the gate table's 14 steps and both charts under `chart:check`; `CONTRIBUTING.md`'s step count; `docs/architecture.md`'s binaries, images and ports; `README.md`'s status line | `hack/docs.sh` passes; review |

## Revisions from planning

Writing the plans against the code changed these fixes. Where a theme's table above disagrees with
this section, this section wins; each plan's "Spec deviations" carries the evidence.

| Finding | Revision | Why |
|---|---|---|
| H-1 | `ClaimBridge`'s takeover `UPDATE` also gains `AND NOT sealed`; the store test is the one that fails first | The takeover path ignored the seal too; hello already mapped `ErrSealed` to 410 |
| S-9 | A separate per-principal *read* budget on `Server` (10/s, burst 20), not `Actor.limited`; the fold cache stores a `summary.State.Clone` and skips the read when `LastSeq` has not moved | `Server.Actor` may be nil, and sharing the act budget would let page refreshes refuse a viewer's next act; `State.Add` mutates in place |
| H-2 / D2 | `awaitingCIFor` becomes `*Duration`, defaulted in `Parse` (there is no `ApplyDefaults`); `mainSignal` and the webhook's `room.merged` learn `merged_unverified` | An explicit `0` must stay distinguishable from unset |
| F-2 | The cap at 16 stays; "drain before the first status write" is dropped | Draining first breaks the invariant that nothing posts before its status write lands; the cap alone ends the wedge |
| F-3 | `RecentComments` also returns `PermanentError` | It is the first call to fail on a deleted issue |
| F-11 | `RevertPR` takes the reverted PR's number to find `revert-<n>-` | The adoption lookup needs it |
| F-5 | Counting every label event only; the "retry generation + 1 on AlreadyExists" is dropped; the test asserts the Task, not a comment | Counting all events makes generations unique; the retry could start a second Task for one label; intake never comments on a Task it creates |
| H-3 | The fixtures and `docs/factory.md`, which promised 100 files, move to 300 | They stated the wrong cap |
| S-7 | Validation requires the `solo`, `pair` and `trio` templates | Those are the templates `Matrix` picks |
| H-4 | The deployed `meter.query` (`sum by (ar_agent, gen_ai_request_model, gen_ai_token_type)`) sums across pods, so the query must keep `pod`; the series key is the full label set; the meter warns at startup on a query without `pod`. **The query change lands in ai-native-ref with, or before, the image bump** | Per-series state cannot separate pods the query has already summed |
| F-7 | The RunLore intake server's `Start` gets the same fix; both drains go from 5 s to 15 s, under the manager's 20 s | Same bug, found while planning |
| F-10 | The 503 starts at the 73rd event (8 working + 64 queued); a refused event's id is forgotten so Slack's retry is not dropped as a duplicate | Dedup would otherwise swallow the retry |
| S-1 | The factory serves through `tls.NewListener` over T5's listener; `api.tls.secretName` defaults to empty, plaintext stays the chart default; a test-only `tlsreloadtest` writes key pairs | Builds on T5's split; TLS is opt-in by D6 |
| S-2 | The policy allows exactly the three suspend annotation keys, not a prefix, and also freezes labels, finalizers and owner references; an envtest case proves it (envtest 1.36.2 enforces VAP) | The meter's and the revoke annotations share the `agents.ogenki.io/` prefix: a prefix rule would let the broker forge a revoke |
| S-3 | `hack/quickstart/up.sh` creates the CA-only Secret | The quickstart must keep working |
| U-1 | `maku run` drops `tester` too; one `runRoles` list feeds both selects and a test compares it with `startrun.go` | The CLI offered the same refused role |
| A-1 | The whole ledger surface moves, not the six symbols; names drop the `Ledger` prefix | The six alone leave the import; revive flags the stutter |
| A-2 | `bridgeapi` reads no kind; the readers are `roomctrl`, `suspend`, `runrequest`, factory `runs`, the reconciler, the meter and `runwatch`, migrated one green commit each | Found by grep |

## Out of scope

- The audit's Low findings, parked in the audit file.
- Revoking a closed room's live runs (D5).
- Re-running cancelled checks (D1).
- Splitting the large functions and files the audit lists (`Config.Validate`, `awaitingCI`, `meter.go`, `config.go`): a follow-up once T2 to T4 have changed them.
- Consolidating the Dockerfiles, CI matrices, rate limiters and `now()` helpers.

## Verification

| Check | Proves |
|---|---|
| A failing test precedes every fix, named in its plan | Each edge is pinned |
| `task check` exits 0 on every PR, store tests included (Docker) | The gate |
| `go list -deps ./internal/factory/{intake,meter,reconciler}` has no `internal/factory/api` | D8 |
| `grep -rn 'func init' internal/runwatch` prints nothing | D9 |
| Both `render.sh` scripts assert H-5, S-2 to S-5 and the T6 Certificate | The chart fixes |
| A broker started with `factoryURL: http://…` and no `factoryInsecure` exits with the key named | D6 |

## Open to the owner

| Question | Default taken here |
|---|---|
| The exact GraphQL error strings for F-1, and the head-branch naming of `revertPullRequest` for F-11 | Recorded from live calls against a disposable PR before either match is coded |
| The H-4 query change in ai-native-ref (`pod` kept in `meter.query`) | Merged with, or before, the image bump that carries T4 |
| `broker.factoryPodLabels` default (S-4) | `app.kubernetes.io/name: agent-factory`, the factory chart's selector label |
