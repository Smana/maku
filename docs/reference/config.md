<!-- page-type: reference -->
# The config file

## Contents

- [Top-level and repository keys](#keys)
- [`demo`](#demo)
- [Run tokens: the role table](#run-tokens-the-role-table)
- [The ownership fence](#the-ownership-fence)
- [Who may start work on a repository](#who-may-start-work-on-a-repository)
- [Steering](#steering)
- [The scoper](#the-scoper)

## Keys

YAML, the chart's `config` value, rendered to `/etc/agent-factory/config/config.yaml`. Unknown keys
fail the start, every problem is reported at once, and the file is a gate path: only a
human-authored pull request changes it. Source: `internal/factory/config/config.go`.

| Key | Holds | Default |
|---|---|---|
| `factory` | This factory's name, a DNS label such as `prod`: a repository is acted on only while its `.github/agent-factory.yaml` gives it | Required |
| `repositories` | At most 8 entries, one per repository, names unique (case-insensitive); see the entry table below | Required |
| `control` | `{repository, issue}`: the issue whose `factory/stop` is the global stop, on a listed repository | `issue` 0: none |
| `intake.overlap` | `{mode, scoperEnforcement}`: [outcome-overlap detection](../factory.md#outcome-overlap-detection) for issue, sponsored API and scheduled Tasks. `mode` is `off`, `shadow` or `enforce` (a bare YAML `off` reads as the string); `scoperEnforcement` independently selects scoper-confirmed refusal in `enforce` only. Unknown or duplicated keys are refused | `mode: off`, `scoperEnforcement: false` |
| `factoryLogin`, `agentsLogin` | The factory App's login (narration) and the agents App's (pull requests) | Required |
| `roomsURL` | `https://<host>` of the rooms UI, linked in comments | Required |
| `broker` | `url` (https only), `caFile`, `tokenFile` | Required |
| `github` | `appIDFile`, `privateKeyFile`; `mergerAppIDFile`, `mergerKeyFile` (both or neither; required once a class is live or shadow; equal to `appIDFile`, the merger is folded into the factory App and every `merge.mergerLogin` must be `factoryLogin`); `agentsAppIDFile`, `agentsKeyFile` (both or neither: the agents App, over the `runTokens.appIDFile` and `privateKeyFile` aliases, which may not name another App). Each App's permissions: [GitHub Apps](../github-apps.md) | Required |
| `poll` | `issues`, `tasks`, `meter`: durations such as `"60s"` | Required, positive |
| `defaults` | `dataClass`, `predictedClass` (`template` and `tier` were removed: a workflow names them) | Required |
| `triage` | `controlPercent` (0–100); every task takes the static `standard` tier (`classifierURL` was removed) | Required |
| `tiers` | Exactly `light`, `standard`, `frontier`; see [Triage](../factory.md#triage-tiers-templates-classes) | `runUSD`, `taskUSD` from the token caps when `meter.prices` is set |
| `templates` | `<name>: {roles, maxReviewRounds, reviewerModel}`; every template but a lone `triager` has an `implementer`; `reviewerModel` (a gateway logical name) needs a `reviewer` role. `solo` and `pair` are chosen by triage by class and tier, and a workflow's template replaces triage's; `trio` is refused. Once `runlore` or `api.internalPrincipals` is set, `investigate` is required as `{roles: [triager]}` | The [shipped workflows](../factory.md#the-shipped-workflows)' `solo`, `pair` and `investigate` where the file omits them; `reviewerModel` unset: the other tier's model |
| `caps` | `activeTasks`, `concurrentRuns`, `tasksPerDay`, `maxTextBytes` (≤ 14336), `awaitingHumanWIP`, `maxPendingMinutes` (5–480), `escalatedHours`, `preferRevisions` (a revision runs before new intake, caps still binding) | `maxPendingMinutes` 30, `escalatedHours` 168, `preferRevisions` false |
| `budgets` | `enforceTask`, `enforcePrincipal`, `factoryDailyUSD`, `humanDailyUSD` ([daily budgets](../factory.md#daily-budgets)), `perPerson: {enabled, dailyUSD}` ([per-person caps](../factory.md#per-person-caps)), each day positive. The token days `factoryDaily`, `humanDaily` and `perPerson.dailyTokens` load in this release only, ignored, each named in a startup warning | `enforceTask` true, `factoryDailyUSD` 42.50, `humanDailyUSD` 8.50, `perPerson.enabled` false, `perPerson.dailyUSD` `humanDailyUSD` |
| `resume` | `maxPerTask` (1–5) | 2 |
| `scoper` | `nudgeAt`, `maxToolCalls` (`1 <= nudgeAt < maxToolCalls <= 200`): the [scoper](#the-scoper)'s tool-call bounds, written as its brief's first line, `<!-- agent-factory scoper nudgeAt=N maxToolCalls=M -->`, which stays first when the run is resumed | `nudgeAt` 25, `maxToolCalls` 40, each defaulted alone |
| `meter` | `url`, `query`, `prices`, and the gateway's 429s from one source: `throttle.promQuery` (PromQL on `url`), or `logsURL` with `throttleQuery` (VictoriaLogs) | Required but `prices`; one 429 source, never both (`meter.throttle.promQuery and meter.logsURL with meter.throttleQuery: choose one`) |
| `runs` | `group`, `namespace`, `namePrefix`: where AgentRuns live; `gatewayIdentity: true` states that each portable run calls the model gateway with its own projected token (the run-controller's `model.mode: gateway`, [the gateway](../gateway.md)), so the meter prices it and `loops` or `workflows` model overrides and `split` are accepted. `group` is `maku.ogenki.io`, the run-controller's: cloud-native-ref's `cloud.ogenki.io` was removed in v0.49.0 and is refused ([upgrading](../upgrading.md#the-xr-run-kind-and-octo-sts)) | the run-controller's AgentRun (`maku.ogenki.io`, `agents`, `run-`); `gatewayIdentity` false |
| `tracing` | `otlpEndpoint`, `host:port` | Empty: tracing off |
| `api` | `listen` (the chart's `api` port is `:8082`), `humanIssuer`, `humanJWKS`, `clientIDFiles`; `groups: {admin, member}`, the two groups the API admits; `systemIssuer`, `systemJWKS` (both or neither), `systemPrincipals`; `systemRepositories: {<principal>: [owner/name]}`, the listed repositories each system principal may start runs on; `access: {links: broker, ttl}`, the broker's GitHub account links that resolve a human caller to their GitHub login (`links` must be `broker`, `ttl` at most 5m); `tls: {certFile, keyFile}`, both or neither: the API serves TLS 1.3 from that pair, re-read when it is renewed, and the broker's `factoryURL` becomes `https://` (the chart's `api.tls` writes it); absent, plaintext; `projectIDFile` and `sponsored`, see [sponsored principals](sponsored-principals.md); `humanJWKS` is optional with an `https://` `humanIssuer`, whose discovery document names it; `preset` is `zitadel` (the default, today's token rules) or `generic`, any OIDC issuer, which needs `rolesClaim` (a claim name, or an RFC 6901 pointer such as `/realm_access/roles`) and takes `tokenUse` (`id`, the default: the broker forwards the human's ID token, whose `aud` holds one of `clientIDFiles`' ids; or `access`, with `audience` or `audienceFile`, which `sponsored` needs too, in place of `projectIDFile`) and `sponsoredClientClaim` (`generic` only: the claim naming a sponsored token's client, one of `cid` (Okta), `appid` (Entra), `azp` or `client_id`; `azp` or `client_id` naming another client still refuses) | Required but `groups`, `access` and the system keys; `groups` defaults to `agents-admin` and `agents-member`, the broker's values. Without `access` every human run is refused |
| `runlore` | `listen`, `tokenFile`, `minConfidence` (0–1], `dailyCap`; `repository`, the listed repository its issues and tasks go to | Required but `repository`, which defaults to the first entry |
| `sandbox` | `suspend: {enabled}`: a run `POST /v1/runs` starts in an attended room gets `spec.suspendable: true`, which lets the room broker suspend it while its approval waits on an idle room (the broker's `suspend` config). Fixed on the claim at create | `enabled` false |
| `loops` | `<workflow>: {stallSteps (1–500), continuations (0–5), judge (an alias or none)}`, keyed by a workflow or `default` (every workflow without a row); a row named after a class is refused. A task reads its workflow's `loop`, else the row named after its workflow, else `default`'s. A row inherits each key it leaves unset from `default`'s (WF10): `models` role by role and tier by tier, `split` whole, and a key the row sets, `scope: off`, `search: false` and `judge: none` included, is its own; the factory warns at load about each key a row inherits, and when that switches its loop on. A row switches the class's [loop policy](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-10-21-loops-and-teams-design.md) on when it sets any of the three, and an unset one is 80, 2 or `tier-light`; `scope` (`off`, `steering`, `scoper`; unset is `off`, and a bare `off` in YAML reads as `off`) picks the class's [steering](#steering) or [scoper](#the-scoper): never part of the loop, so `{scope: steering}` alone switches no loop on; `split` (`{maxChildren 2–5, familyUSD, childTier, anyTier}`; `familyUSD` defaults to four of the child tier's `taskUSD`, `childTier` to `standard`) is refused without `scope: scoper` on the same row, on `docs-links`, `revert` or an oracle class, and without `meter.prices`; it lets the scoper of a task C7 itself rated `frontier` propose [a split](../factory.md#splits); `anyTier: true` (default `false`) offers it on every task of the class, whatever tier C7 rated it (needed where no classifier is deployed, so every task is `standard`). `search: true` offers the class's implementer runs the [search tool](../../images/maku-harness/README.md) (`spec.loop.search`, which the harness reads as `AGENT_SEARCH=1`); like `scope`, it never switches the loop on, and a class row without `search` takes `default`'s. The room-bridge classes a bare `search` as the plain built-in only on a run given it, assuming MCP tools reach it prefixed (`server_tool`): an unprefixed MCP tool named `search` would also class plain (and the SDK refuses the duplicate tool name at start). `models` (`{<role>: {<tier>: <alias>}}`) puts a role on a provider alias for a **public** task of that tier, the run's tier (the reviewer's is the other tier, the scoper's `light`): `kimi-frontier` (Moonshot's `k3.1-pro`) on `frontier`, `mistral-standard` (Mistral Medium 3.5) on `standard`, `mistral-light` (Mistral Small 4) on `light`, and no alias on another tier, so a run keeps its tier's caps. A role or tier without an entry keeps its tier's `model`, or the template's `reviewerModel`, unless the workflow's own `models` field overrides it; this loops row never gives an internal task a provider alias, since Moonshot processes in China and Mistral in the EU, and the run's `provenance.modelSource` records `tier`, `reviewerModel` or `classModels`. `judge` may be `mistral-light` too, and an internal task of the class then gets `tier-light`. With `meter.prices` set, each alias needs a row: its served model's for a query by `gen_ai_request_model`, else its own. No tier's `model` and no `reviewerModel` may name a provider alias, `POST /v1/runs` refuses one on an internal run (`provider_alias_is_public_only`), every claim is refused one on an internal run, as its model or its judge's, and the Task and AgentRun CRDs hold the same rule; like `scope`, `models` never switches the loop on. The loop policy and the scoper run on either runtime. On the portable `maku.ogenki.io` AgentRun, `models` and `split` need `runs.gatewayIdentity` (`loops.<class>.models needs runs.gatewayIdentity (a model gateway, rollout step 4)`, and the same for `split`), and `split` with it also needs a meter (`meter.url` and `meter.query`: `loops.<class>.split needs a meter: a family's cap is in USD`). With `runs.gatewayIdentity`, each run calls the model gateway with its own token, on the alias its claim names (`spec.model`), and the meter prices it. Without it, USD budgets are not metered on that runtime: its runs carry no gateway identity, so `familyUSD`, the tiers' `runUSD` and `taskUSD`, and the principal caps spend nothing, and a configured meter says so at startup. That runtime then runs every role on modelSecret's model, and resolves no judge alias: its judge calls the run's model endpoint (`JUDGE_BASE_URL`) with the model Secret's optional `judge_model` key, or the run's own model without it (`JUDGE_MODEL`); the demo's `demo.model.judgeName` writes that key | None: every run as before |
| `workflows` | `<name>: {template (solo, pair, investigate or none), tier, checks, loop (off or a `loops` row), escalation: {outOfBudget, notConverged}, effort: {<role>: low, medium or high}, models: {<role>: <alias>}, description (≤ 120 bytes, the line the scoper's brief shows)}`: the workflows a task may run; `none` is an adopted pull request's (no run) and takes no other key. `escalation` names the workflow a failed run of this one steps to, by signal group, each a defined workflow. `off` is no loop, never `default`'s. The workflow a task [runs](../factory.md#the-shipped-workflows) shapes its runs: `template` when set, `tier` when above triage's, `checks` and the loop row (`spec.loop`, `spec.scopeMode`, `spec.search`). `effort` and `models` are per-role overrides for a reviewer, tester or triager, never the implementer: absent effort is the harness's `high`, while a set value reaches the claim as `spec.effort` and the harness as `LLM_REASONING_EFFORT`; an absent model keeps the tier's model, then the loops row's public provider override. An internal run's model override is `tier-light`, `tier-standard` or `tier-frontier`, never a provider alias; a public-default workflow's provider override reads as no override on an internal run. Model overrides need `runs.gatewayIdentity`. The map's old name and a workflow's `testFirst` are refused ([upgrading](../upgrading.md#workflows-name-what-runs)) | The [shipped workflows](../factory.md#the-shipped-workflows) where the file omits them |
| `evaluation` | `{controlPercent (0–100), mode (observe or apply), treatment}`: the team evaluation's control cohort of tasks with no pinned workflow (a class label is no pin), whether a recommended workflow is only recorded or used, and the workflow the treatment cohort runs on a task on the repository's default (a defined workflow other than `none`, else refused). Hashed apart from `triage.controlPercent`. `mode` governs the scoper's recommendation only: a workflow's `escalation` steps under `observe` too | `controlPercent` 0: no cohort; `mode` `observe` |
| `mergeGuardrails` | `{protectedFiles (regular expressions), mandatoryChecks, maxPatchFiles, maxModifiedLines, maxPerDay, maxGrantLifetime, minPromotionEvidence}`: the bounds a repository's class policy may not loosen; a cap of 0 is no guardrail. `minPromotionEvidence` is the clean shadow decisions a [promotion](../factory.md#promotion) needs, at least 20; a lower one is refused, and a promotion on fewer needs an operator's exception. Validated only: stage 1 decides nothing with the caps | None; `minPromotionEvidence` 20 |
| `policyManagement.stage` | How far the merge-policy rollout goes: `read` (stage 1), `simulate` (adds `simulation` and `dualEvaluation`), `manage` (adds `repositoryAuthority`, `approvalMemory` and `promotion`). Replaces the six switches below, which still load as the highest stage they cover and set the `Deprecated` condition; not set together with them | `manage` |
| `policyManagement.read` | `true` observes each owned repository's class policy and compares it with this config. Stage 1 decides nothing: merge authority stays central | `false` |
| `policyManagement.simulation` | `true` lets `maku policy simulate` (a policy file decided on decision fixtures by the gate's own evaluator) and `maku policy diff` (tightening or relaxation, class by class) run against this config. Offline; turned off when the simulator disagrees with the gate | `false` |
| `policyManagement.dualEvaluation` | `true` decides each merge decision again under the repository's policy as `read` last observed it, counts the agreement and writes it into the decision record. Needs `read`. Stage 3 decides nothing: merge authority stays central | `false` |
| `policyManagement.promotion` | `true` serves `GET /v1/policy/evidence` and `GET /v1/policy/repositories` on the API: each class's [promotion](../factory.md#promotion) evidence. The broker reads the same key to serve the settings pages and record promotions. Stage 6 changes no class: a maintainer's own pull request does | `false` |
| `policyManagement.operators` | GitHub logins who decide promotion exceptions; read by the broker | None: no exception can be approved |
| `policyManagement.approvalMemory` | `true` consults saved approval grants where a live class's `requiresApproval` subset would ask a human (stage 5): the class's grants are read from the broker immediately before the decision and again before the merge call, never cached, and an active grant bound to the policy in force, covering every file the subset caught, lets the decision arm; the record names it (`grant=`). A failed lookup authorizes nothing, a shadow or off class never consults one, and a revert of a merge a grant authorized disables it. Needs `read`, and the broker's `human.approvalMemory`. Until repository authority (stage 4) decides merges, it changes only the dual evaluation's repository decision | `false` |
| `policyManagement.repositoryAuthority` | `true` lets a repository whose entry sets `classAuthority: repository` be decided on its own `.github/agent-factory.yaml`: the gate decides on its bindings (the `requiresApproval` subset asking a maintainer), and every other reader (triage, intake, schedules, the monitor) reads them. The entry declares no central `classes` (one authoritative source). A file not yet read or older than the fence's window waits (`policy_unavailable`); an invalid one hands off (`policy_invalid`); both pause auto-merge while human merges continue. A relaxation of a live class runs it in shadow. Needs `read`. Rollback: restore the repository's exported central classes and drop `classAuthority` | `false` |

Each `repositories` entry:

| Key | Holds | Default |
|---|---|---|
| `name` | `owner/name` | Required |
| `maintainers` | GitHub logins that may label, retry and review on this repository; never a bot | Required |
| `triggerLabel` | The start label, `factory/ready` | Required |
| `classes` | `<name>: {live, shadow, author: {adopt: {logins, branches}}, releaseAgeCheck, allowedFiles, protectedFiles, modifyOnly, maxPatchFiles, maxModifiedLines, diffShape, holdoutChecks, oracle: {probe: dead-links, workflow, maxFindings ≤ 16}, maxPerDay, monitor: {oracleRuns, flux: {source, within}, followUpWindow}}`; `oracle` needs `holdoutChecks`, `diffShape: links-only`, `modifyOnly: true`, a non-empty `allowedFiles`, and, when `maxModifiedLines` is set, `maxModifiedLines >= 2 x oracle.maxFindings` (a retarget changes two lines); `live` and `shadow` never both; every `live` or `shadow` class needs `allowedFiles` or `diffShape`; `allowedFiles` patterns anchored `^…$`; `author.adopt` and the semver shapes: see [adopted classes](../factory.md#adopted-classes); `docs-links` and `revert` required and hold `diffShape: links-only` built in; `review` and workflow fields (`checks`, `testFirst`, `harness`, `harnessAllowed`, `escalation`) are refused ([upgrading](../upgrading.md#merge-policies-removals)); see [merge policies](../merge-policies.md#a-binding) | Required when not served from a `Repository` |
| `checks` | `{egress: [profiles]}`: what a check-enabled run requests beyond `github`, at most 4 of `pypi`, `npm`, `golang`, `crates`. The repository's `.github/agent-checks.yaml` never names egress | None: github only |
| `merge` | `requiredChecks`, `verifyChecks`, `leakScanCheck` (in both lists), `contractCheck` (the job that runs a task's test contract: not one of `requiredChecks`, and set whenever the repository sets `workflow.testFirst`), `policyBotLogin` (required with `policyBot: true`), `policyBot` (true routes the gate through policy-bot, an opt-in; unset is false, so `maku/merge-gate` is the status branch protection requires, which needs `gateStatus`), `gateStatus` (posts the gate's verdict as the commit status `maku/merge-gate` on the pull request; unset is true; `demo` requires it), `mergerLogin` (bots), `maxPerDay` (the repository's auto-merges a day; `autoMergesPerDay` is refused), `fixRuns`, `checkReruns`, `verifyFor`, `revertWindow`, `awaitingCIFor`, `breaker: {window, maxReverts, maxFollowUps, recoverAfter}`, `approveToMerge` (`awaitingCIFor` optional, 24h when omitted, `0s` for no bound; `maxFollowUps` and `recoverAfter` optional, 0 is off; `recoverAfter` below `window`; `approveToMerge` optional, false is off: on, a maintainer's `APPROVED` review of an agents pull request's current head arms the merger. It acts only when a class of any configured repository is live, shadow or monitored (factory-wide, as the merger key is): with none, the task hands off `merge_gate_off` before the approval is read) | Required |
| `workflow` | `{default, testFirst}`: `default` is the workflow a task runs when no label and no source names one (`standard` when unset; `none` is refused); `testFirst: true` starts every public `careful` task with the tester and needs `merge.contractCheck` | `standard`; no tester |
| `schedules` | `[{name, cron, class?, text, workflow?, dataClass?, probe?, probeAuthor?}]`; `workflow` is the workflow its tasks run, `quick` when unset; `class` is set only when its policy declares `oracle.probe` (refused otherwise); `probe` is `renovate-red` or `dead-links`, and `probeAuthor` the login whose open PRs it reads. `dead-links` needs its class to declare the oracle, finds nothing while the class is off or a task of the schedule is open, and reads the newest two runs of the oracle's workflow on `main`. Names are unique across every repository | None; `probeAuthor` defaults to `renovate[bot]` |

`api.repositories`, the run-request API's allowlist, is the entries' names.

## `demo`

`demo: true` labels a factory on a laptop cluster: no OIDC issuer, no metrics backend, one
GitHub App. The broker keeps TLS: the umbrella chart generates its CA. Absent, every rule is production's. Under `demo: true`, `Validate`
changes exactly this:

| Rule today | Under `demo: true` |
|---|---|
| `meter.url`, `meter.query`, and a 429 source (`meter.throttle.promQuery`, or `meter.logsURL` with `meter.throttleQuery`) required | optional. If any is set, the existing rules apply. If `meter.url` is empty, any USD cap (`meter.prices`, sponsored USD ceilings, `split.familyUSD`, tier `runUSD` or `taskUSD`) is refused: `demo: %s needs a meter, and the demo has none` |
| `roomsURL` must be `https://` | `http://localhost[:port]` and `http://127.0.0.1[:port]` also accepted |
| `api.humanIssuer`, `api.humanJWKS`, `api.clientIDFiles`, `api.groups.*` required | optional, and must all be empty (no group default is applied): `demo: the factory API takes no human tokens` |
| `api.access` optional | must be unset: `demo: api.access needs an identity provider` |

`demo: true` also requires `merge.gateStatus: true` on every repository.

Without policy-bot (`merge.policyBot` unset or false, the default since v0.49.0) a
repository needs `merge.gateStatus` left on, since `maku/merge-gate` is then the status branch
protection requires (see [Branch protection for the merge gate](../factory.md#branch-protection-for-the-merge-gate)),
and `merge.policyBotLogin` may be empty (a value that is set must still be a bot login). Every live
class of the repository then needs `allowedFiles`: policy-bot's gate-path human row is not there,
and nothing else keeps a gate path from merging. `merge.policyBot: true` opts back into policy-bot.

**A gate path is never the gate's to merge, approval or not** (policy-bot's S9, kept without it).
With `merge.policyBot: false`, the gate paths are the `protectedFiles` of every class of the
repository, where the `.policy.yml` gate list moves: the central config's classes and, for a
repository whose [merge policy](../merge-policies.md) is authoritative, every class and every `any`
branch of its policy. Under `approveToMerge`, a maintainer's approval arms the merge without the
class's own conditions, so the gate reads the approved head's diff for this one rule, and checks it
once over the final decision, so no `any` branch can arm an approval over a gate path. A change to
a gate path, or a rename from one, is handed off as `gate_path` and stays a human merge on GitHub. `allowedFiles` still bounds what a class arms on its own, not
what a maintainer approves.

**The demo runs on one GitHub App.** A production install runs two or more ([GitHub
Apps](../github-apps.md)). `github.mergerAppIDFile` and `github.mergerKeyFile` may name
the factory App's own files (the chart's `apps.merger.mode: factory` writes them) (`github.appIDFile`, `github.privateKeyFile`): the merger connection
then signs with the same key, and a shadow or live class still has its merger. That one App needs
the union of every permission asked on the demo's behalf, as one list: `actions: write` (optional,
for check re-runs), `checks: read`, `contents: write`, `issues: write`, `metadata: read`,
`pull_requests: write`, `statuses: write`. It covers the factory App's own set (contents read,
issues write, pull_requests write, metadata read), the merger's (checks read, statuses read,
contents write, pull_requests write, metadata read, plus the optional actions and issues writes
and `statuses: write` for the gate's verdict) and the run tokens' (the write token: contents write,
pull_requests write, metadata read; the read token: contents, pull_requests, issues, checks,
actions and metadata read). `statuses: write` is the merger's alone: the run tokens do not ask for
it, since one App backs every run and a run that could post a status could forge `maku/merge-gate`.
A token minted for a narrower profile asks for a subset, which an App holding the union grants.

**Per-run tokens (the only factory-written tokens since v0.49.0).** `runTokens.perRun`
(with `runTokens.enabled`, or the demo's factory App) gives every run its own token, which the
run-controller mounts whenever `runController.gitSecret` is empty. `runTokens.enabled` mints from the
agents App (`github.agentsAppIDFile` and `agentsKeyFile`, which the chart's
`apps.agents.existingSecret` writes), and is refused without `perRun` or without both files. The
shared `agent-git` and `agent-git-read` writer is removed; a laptop's one PAT
(`runController.gitSecret`, demo-only) is the only shared token left:

```mermaid
sequenceDiagram
  participant F as factory (minter)
  participant G as GitHub
  participant K as runs namespace
  participant C as run-controller
  participant H as harness
  F->>K: create the AgentRun (the leader mints at once, a follower leaves it to the leader)
  F->>G: installation of owner/repo; a token on repo alone, the role's permissions
  F->>K: apply <run>-git (token, expires-at, owned by the run)
  C->>K: get <run>-git: the factory's? 5 minutes left?
  C->>K: create the pod, mounting <run>-git
  loop every 30 s, under 10 minutes left
    F->>G: a fresh token
    F->>K: apply <run>-git (the kubelet updates the volume in place)
  end
  H->>G: revoke the token on exit
  F->>G: revoke at the run's end
  F->>K: delete <run>-git
```

- **Only the leader mints.** The replica that creates a run mints its first token at once when it
  is the leader; on a follower the leader mints at its next 30-second pass. So no two replicas
  ever hold a token for one run.
- **The token** is minted from the App the config names for the run's one repository (its
  installation is looked up per repository, so runs may span owners), with exactly the
  permissions the role table below gives the run's role under its `safe-outputs` annotation. A run
  created without that annotation mints under the repository's live enforced set, which can only
  narrow the token. A reply granting anything else is revoked and refused (`scope_mismatch`).
- **Only runs the factory accounts for get one:**
  - a run naming a live Task on the same repository (its role is checked against the table only:
    a Task drives several roles);
  - an API run holding a reservation in today's or yesterday's ledger, whose recorded repository
    and role the claim must match: the minter trusts what admission recorded, not the claim.

  Anything else is refused once (`maku_factory_run_token_refusals_total{reason}`: `unbound_task`,
  `unbound_api`, `repository_mismatch`, `role_unknown`, `role_mismatch`) and its run waits Pending.
  A reservation from before this release names no repository: its run is refused `unbound_api`.
- **The run-controller** starts a pod only on a Secret that is the factory's (its labels and an
  ownerReference to that very run), holds a token, and has over 5 minutes left. Otherwise the run
  waits Pending with reason `SecretMissing` or `SecretExpiring`. A running pod is never stopped for
  a later expiry.
- **Suspended runs** are not refreshed; they are re-minted when they leave `Suspended`.
- **At the run's end** the harness revokes its token after the checkpoint push
  (`GIT_TOKEN_REVOCABLE=1`), and the factory revokes the token it holds and deletes the Secret.
- **The factory never reads a Secret** in the runs namespace: see
  [security](security-controls.md#the-factory-writes-run-token-secrets-it-cannot-read).

## Run tokens: the role table

The per-run minter gives each run a token scoped to the run's one repository, with the
permissions its role gets from one table, `minter.Roles` (`internal/factory/minter/roles.go`). The
scoper and integration runs mint as the triager. "Once enforced" reads the run's own
`agents.ogenki.io/safe-outputs` annotation, written when the run is created: a type the factory
publishes for the run takes the matching write away from the sandbox role. The table below is
generated from the code (`go test ./internal/factory/minter -run TestDocsTable -update`). No role
holds `statuses`, `workflows`, `administration`, `checks: write` or `actions: write`, and only the
publisher holds `issues: write`. Runs mint from it under `runTokens.perRun` (above); the shared
run tokens ignore it.

<!-- roles:begin -->
| Role | Permissions | Once enforced |
|---|---|---|
| implementer | `actions: read`, `checks: read`, `contents: write`, `issues: read`, `metadata: read`, `pull_requests: write` | `pr`, `comment`, `labels`: `pull_requests: read`; `push`: `contents: read` |
| tester | `actions: read`, `checks: read`, `contents: write`, `issues: read`, `metadata: read`, `pull_requests: read` | `push`: `contents: read` |
| reviewer | `actions: read`, `checks: read`, `contents: read`, `issues: read`, `metadata: read`, `pull_requests: read` | none |
| triager | `actions: read`, `checks: read`, `contents: read`, `issues: read`, `metadata: read`, `pull_requests: read` | none |
| publisher | `contents: read`, `issues: write`, `metadata: read`, `pull_requests: write` | `push`: `contents: write` |
<!-- roles:end -->

The public-only check runs at start-up and on each labelled issue, so a repository made private
after start-up is caught only when an issue is labelled.

**The single-repository shape is removed** (v4 step 8): a file with a top-level `repository`,
`maintainers`, `triggerLabel`, `controlIssue`, `classes`, `merge` or `schedules` fails the start,
naming each key. Serve the repository from a `Repository` object; `maku config migrate` still reads
the old file and writes it ([upgrading](../upgrading.md#the-single-repository-config)).

**Each repository has its own GitHub connections**, the factory App's and the merger's, one pair
per entry; a repository either App is not installed on is refused alone: it is not served, its `Repository`
reports `Connected=False` (`WiringFailed`), the others are served, and the factory retries every
minute until the App is installed. Each entry has its own issue
poller, and a task only ever reads and writes its own repository through its own pair. The daily
auto-merge cap and the breaker count that repository's merges only.

## The ownership fence

A repository is this factory's only while `.github/agent-factory.yaml` on its default branch names
it, so two clusters never take the same issue:

```yaml
# .github/agent-factory.yaml: make it a policy-bot gate path
factory: aws-0
```

Every replica reads each listed repository's file at start and every 5 minutes, with the factory
App. Absent, unparsable (the file names the factory with `factory: <name>`, and may also hold
`mergePolicies` and a [`hardening`](../factory.md#the-repositorys-hardening-block) block, neither of which decides
ownership) or naming another factory, the repository
gets no trigger-label or revert intake, no orphan scan, no schedules, no RunLore issues
(`409 repository_not_owned`), no API runs (`409 repository_not_owned`) and no merges (gate step
`not_owned`, a human merges). Stop labels and the control issue still work. A read that fails keeps
the last answer for 15 minutes, then the repository counts as not owned until a read succeeds.
`maku_factory_repository_owned{repository}` shows the state.

## Who may start work on a repository

| Path | Bound to | GitHub check |
|---|---|---|
| Trigger label | The poller's repository; the labeller in that entry's `maintainers` | The labeller has `write` or higher on it; no answer leaves the label for the next poll |
| `factory/revert` label | The labelled pull request's task, on the poller's repository | Same as the trigger label |
| `/factory retry` | The escalated task's repository; the commenter in that entry's `maintainers` | Same as the trigger label |
| Schedule | The entry that lists it | None: the config is the requester |
| RunLore | `runlore.repository` | None: the config chooses, not the caller |
| `POST /v1/runs`, roomless | `repository`, in the list | The human has `write` or higher on it, admins included; a system principal needs it in `api.systemRepositories` |
| `POST /v1/runs` with `roomRef` (broker `start_run`, a fork's runs) | The room's `spec.repository`, else `400 repository_differs_from_the_room` | As roomless |
| `POST /v1/runs` with `resumeBranch` | The branch's newest run's repository and its room's, else `400 repository_differs_from_the_branch` | As roomless |

The `write` check resolves the token's subject to the caller's GitHub login through their
[GitHub account link](../github-link.md), read from the broker (`api.access.links: broker`), then asks
GitHub with the factory App; both answers are cached for
`api.access.ttl`. A caller with no GitHub link, a lookup that fails, or a factory without
`api.access` is refused `403 repository_permission`. Metrics carry a `repository` label whose
values are the listed names only, anything else `other`.

## Steering

A repository's maintainers may keep a note per class at `.github/agent-factory/steering/<class>.md`
(`review.md` for unlabelled issues): at most 2048 bytes, an optional `# ` title, at most three `## `
modules, one of them `## Verify` listing the checks that prove a change of the class, each with the
CI check that runs it. `## Leave alone` lists paths not to touch. Under `scope: steering` the factory
reads it on the default branch at triage, keeps it on `spec.steering`, posts it once in the room after
the snapshot and puts it, quoted between two fence lines as the maintainers' guidance (not the
factory's instructions), in every implementer brief. Control and invisible characters are refused. An
absent note is no steering; an invalid one is not used, counted and logged, and the task proceeds; a
read that fails is retried. A repository the factory does not own (or whose fence cache is stale) is
not read: the task proceeds unsteered and the read counts as `unowned`. While any class has steering on, the
factory reads the diff of every pull request it would arm and refuses one that changes (or renames
away) a path under `.github/agent-factory/steering/` as `steering_path`, handed to a human; with
every class `off` there is no extra read. The repository should also make that directory a
policy-bot gate path. A first brief that cannot hold the note and a 14 KiB snapshot
points at the snapshot in the room instead.

## The scoper

Under `scope: scoper`, a read-only `triager` run with a scoping brief goes before the implementer
(after any [outcome-overlap evaluation](../factory.md#outcome-overlap-detection)), at the light
tier's run caps (at the task's own tier when it was offered a [split](../factory.md#splits), since it also plans
every part), and hands the implementer a bounded task through `room_handoff`'s `scope`: the
change, the files, the paths to leave alone, a plan, its assumptions and its acceptance checks.
Every implementer brief carries it after the steering note. The brief asks for the handoff as soon
as the scope is known, within `scoper.nudgeAt` tool calls (25 by default). The harness bounds its
exploration: it nudges at `scoper.nudgeAt` tool calls and ends the run at `scoper.maxToolCalls`
(40 by default), counted as `budget_calls`. A scoper that ends without a handoff on its own
limits (the run cap or the tool-call limit) or after finishing with no scope starts the implementer
unscoped, and the room says "scope not checked". Any other failure of the scoper (an agent error, a
stuck or lost run) never starts the implementer unscoped: the task escalates as `scoper_failed`, and
the issue's status comment says the scope was not checked. A model provider's refusal is neither: it
holds the task like any run's ([the hold](../factory.md#model-provider-refusals)). A run cap counts as `budget` in
`maku_factory_scoper_runs_total`, the tool-call limit as `budget_calls`. A person's stop, an
exhausted budget (task, payer, fleet or person), an unschedulable run (`run_unschedulable`), no
budget to resume it (`resume_budget`) or a deleted claim escalate as any run's. A question no
default answers bounces the task to the issue's author (`needs_author`); a task without an issue
takes the scoper's first assumption instead.

- **Read-only at the token level.** The triager's token is minted from the role table's triager
  row, which grants reads only: the run cannot push or open anything, whatever its brief says. The
  one exception is a demo's laptop PAT, `runController.gitSecret`, which every role shares. It also
  refuses a scoper run without the room tools (`broker.roomTools.auth: tokenreview`), since the
  scoper hands off with `room_handoff`.
- **The scope has one source.** The factory takes it only from the triager's handoff to the
  implementer, on the current scoper run, and validates it again (the broker stored it redacted).
  A scope on any other handoff is ignored, and the task proceeds unscoped (`missing`). It reaches
  implementer briefs fenced, as the scoper's claim, never as the factory's instructions.
- **The bounce posts one question.** The question and the scoper's first assumption go in one
  comment to the author (quoted for a public task, left in the room otherwise), with the label
  `factory/needs-author`. Only a maintainer ends the wait: a re-applied `factory/ready` (a new
  task, scoped again), or `/factory retry` from a maintainer with write access (the task proceeds
  on the assumption, state `assumed`, with no second scoper run). Otherwise it stops at
  `caps.escalatedHours` (`escalation_expired`). The author's own comments end nothing, and the
  room's owner sees the wait as an `answer` need.
