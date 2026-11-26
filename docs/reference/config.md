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
- [Broker fields](#broker-fields)
- [The bridge's environment](#the-bridges-environment)

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
| `templates` | Deprecated in favor of `workflows.<name>.stages` (warns at load; refused from the next minor). `<name>: {roles, maxReviewRounds, reviewerModel}`; every template but a lone `triager` has an `implementer`; `reviewerModel` (a gateway logical name) needs a `reviewer` role. `solo` and `pair` are chosen by triage by class and tier, and a workflow's template replaces triage's; `trio` is refused. Once `runlore` or `api.internalPrincipals` is set, `investigate` is required as `{roles: [triager]}` | The [shipped workflows](../factory.md#the-shipped-workflows)' `solo`, `pair` and `investigate` where the file omits them; `reviewerModel` unset: the other tier's model |
| `caps` | `activeTasks`, `concurrentRuns`, `tasksPerDay`, `maxTextBytes` (≤ 14336), `awaitingHumanWIP`, `maxPendingMinutes` (5–480), `escalatedHours`, `preferRevisions` (a revision runs before new intake, caps still binding) | `maxPendingMinutes` 30, `escalatedHours` 168, `preferRevisions` false |
| `budgets` | `enforceTask`, `enforcePrincipal`, `factoryDailyUSD`, `humanDailyUSD` ([daily budgets](budgets.md#daily-budgets)), `perPerson: {enabled, dailyUSD}` ([per-person caps](budgets.md#per-person-caps)), each day positive. The token days `factoryDaily`, `humanDaily` and `perPerson.dailyTokens` load in this release only, ignored, each named in a startup warning | `enforceTask` true, `factoryDailyUSD` 42.50, `humanDailyUSD` 8.50, `perPerson.enabled` false, `perPerson.dailyUSD` `humanDailyUSD` |
| `resume` | `maxPerTask` (1–5) | 2 |
| `scoper` | `nudgeAt`, `maxToolCalls` (`1 <= nudgeAt < maxToolCalls <= 200`): the [scoper](#the-scoper)'s tool-call bounds, written as its brief's first line, `<!-- agent-factory scoper nudgeAt=N maxToolCalls=M -->`, which stays first when the run is resumed. `planTier` (`light`, `standard` or `frontier`): the tier whose model and run caps the scoper runs at (`maku install --print` writes `frontier`) | `nudgeAt` 25, `maxToolCalls` 40, `planTier` `light`, each defaulted alone |
| `meter` | `url`, `query`, `prices`, and the gateway's 429s from one source: `throttle.promQuery` (PromQL on `url`), or `logsURL` with `throttleQuery` (VictoriaLogs) | Required but `prices`; one 429 source, never both (`meter.throttle.promQuery and meter.logsURL with meter.throttleQuery: choose one`) |
| `runs` | `group`, `namespace`, `namePrefix`: where AgentRuns live; `gatewayIdentity: true` states that each portable run calls the model gateway with its own projected token (the run-controller's `model.mode: gateway`, [the gateway](../gateway.md)), so the meter prices it and `loops` or `workflows` model overrides and `split` are accepted. `group` is `maku.ogenki.io`, the run-controller's: cloud-native-ref's `cloud.ogenki.io` was removed in v0.49.0 and is refused ([upgrading](../upgrading.md#the-xr-run-kind-and-octo-sts)) | the run-controller's AgentRun (`maku.ogenki.io`, `agents`, `run-`); `gatewayIdentity` false |
| `tracing` | `otlpEndpoint`, `host:port` | Empty: tracing off |
| `api` | `listen` (the chart's `api` port is `:8082`), `humanIssuer`, `humanJWKS`, `clientIDFiles`; `groups: {admin, member}`, the two groups the API admits; `systemIssuer`, `systemJWKS` (both or neither), `systemPrincipals`; `systemRepositories: {<principal>: [owner/name]}`, the listed repositories each system principal may start runs on; `access: {links: broker, ttl}`, the broker's GitHub account links that resolve a human caller to their GitHub login (`links` must be `broker`, `ttl` at most 5m); `tls: {certFile, keyFile}`, both or neither: the API serves TLS 1.3 from that pair, re-read when it is renewed, and the broker's `factoryURL` becomes `https://` (the chart's `api.tls` writes it); absent, plaintext; `projectIDFile` and `sponsored`, see [sponsored principals](sponsored-principals.md); `humanJWKS` is optional with an `https://` `humanIssuer`, whose discovery document names it; `preset` is `zitadel` (the default, today's token rules) or `generic`, any OIDC issuer, which needs `rolesClaim` (a claim name, or an RFC 6901 pointer such as `/realm_access/roles`) and takes `tokenUse` (`id`, the default: the broker forwards the human's ID token, whose `aud` holds one of `clientIDFiles`' ids; or `access`, with `audience` or `audienceFile`, which `sponsored` needs too, in place of `projectIDFile`) and `sponsoredClientClaim` (`generic` only: the claim naming a sponsored token's client, one of `cid` (Okta), `appid` (Entra), `azp` or `client_id`; `azp` or `client_id` naming another client still refuses) | Required but `groups`, `access` and the system keys; `groups` defaults to `agents-admin` and `agents-member`, the broker's values. Without `access` every human run is refused |
| `runlore` | `listen`, `tokenFile`, `minConfidence` (0–1], `dailyCap`; `repository`, the listed repository its issues and tasks go to | Required but `repository`, which defaults to the first entry |
| `sandbox` | `suspend: {enabled}`: a run `POST /v1/runs` starts in an attended room gets `spec.suspendable: true`, which lets the room broker suspend it while its approval waits on an idle room (the broker's `suspend` config). Fixed on the claim at create | `enabled` false |
| `loops` | `<workflow>: {ask, plan, tests, search, review, stallSteps (1–500), continuations (0–5), judge (an alias or none), scope, split, models}`, keyed by a workflow or `default` (every workflow without a row); a row named after a class is refused. A task reads its workflow's `loop`, else the row named after its workflow, else `default`'s. A row inherits each key it leaves unset from `default`'s (WF10): `models` role by role and tier by tier, `split` whole, and a key the row sets, `ask: false`, `plan: false`, `review: false`, `scope: off`, `search: false` and `judge: none` included, is its own; the factory warns at load about each key a row inherits, and when that switches its loop on. `ask` (`bool`) switches on the acceptance guard at intake; `plan` (`bool`) is the stage switch for the scoper (`plan: true` is `scope: scoper`, `plan: false` is `scope: off`; contradicting `scope` on the same row is refused, while `scope: steering` keeps its own value); `review` (`bool`) resolves the template for a workflow that names none (`pair` when `true`, `solo` when `false`) and is refused if it contradicts a workflow's explicit template. Rows that set none of `ask`, `plan` and `review` resolve as before (`plan` from `scope`, `review` from the workflow's template, `ask` from the scoper). None of `ask`, `plan`, `review`, `scope`, `search`, `tests`, `split` or `models` switches the loop policy on. A row switches the class's [loop policy](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-10-21-loops-and-teams-design.md) on when it sets any of `stallSteps`, `continuations` or `judge`, and an unset one is 80, 2 or `tier-light`; `scope` (`off`, `steering`, `scoper`; unset is `off`, and a bare `off` in YAML reads as `off`) picks the class's [steering](#steering) or [scoper](#the-scoper): never part of the loop, so `{scope: steering}` alone switches no loop on; `split` (`{maxChildren 2–5, familyUSD, childTier, anyTier}`; `familyUSD` defaults to four of the child tier's `taskUSD`, `childTier` to `standard`) is refused without `plan: true` or `scope: scoper` on the same row, on `docs-links`, `revert` or an oracle class, and without `meter.prices`; it lets the scoper of a task C7 itself rated `frontier` propose [a split](../factory.md#splits); `anyTier: true` (default `false`; set on the shipped `careful` row) offers it on every task of the class, whatever tier C7 rated it (needed where no classifier is deployed, so every task is `standard`). `search: true` offers the class's implementer runs the [search tool](../../images/maku-harness/README.md) (`spec.loop.search`, which the harness reads as `AGENT_SEARCH=1`); like `scope`, it never switches the loop on, and a class row without `search` takes `default`'s. `tests: true` (set on the shipped `careful` row) starts a public task of the row with the tester when its repository sets `workflow.testFirst: true`; like `scope`, it never switches the loop on, and a row without `tests` inherits `default`'s. The room-bridge classes a bare `search` as the plain built-in only on a run given it, assuming MCP tools reach it prefixed (`server_tool`): an unprefixed MCP tool named `search` would also class plain (and the SDK refuses the duplicate tool name at start). `models` (`{<role>: {<tier>: <alias>}}`) puts a role on a provider alias for a **public** task of that tier, the run's tier (the reviewer's is the other tier, the scoper's `planTier`): `kimi-frontier` (Moonshot's `k3.1-pro`) on `frontier`, `mistral-standard` (Mistral Medium 3.5) on `standard`, `mistral-light` (Mistral Small 4) on `light`, `gemini-standard` (Gemini 4 Neon) on `standard`, `gemini-light` (Gemini 4 Hydrogen) on `light`, and no alias on another tier, so a run keeps its tier's caps. A role or tier without an entry keeps its tier's `model`, or the template's `reviewerModel`, unless the workflow's own `models` field overrides it; this loops row never gives an internal task a provider alias, since Moonshot processes in China, Mistral in the EU and Gemini on Google's terms, and the run's `provenance.modelSource` records `tier`, `reviewerModel` or `classModels`. `judge` may be `mistral-light` or `gemini-light` too, and an internal task of the class then gets `tier-light`. With `meter.prices` set, each alias needs a row: its served model's for a query by `gen_ai_request_model`, else its own. No tier's `model` and no `reviewerModel` may name a provider alias, `POST /v1/runs` refuses one on an internal run (`provider_alias_is_public_only`), every claim is refused one on an internal run, as its model or its judge's, and the Task and AgentRun CRDs hold the same rule; like `scope`, `models` never switches the loop on. The loop policy and the scoper run on either runtime. On the portable `maku.ogenki.io` AgentRun, `models` and `split` need `runs.gatewayIdentity` (`loops.<class>.models needs runs.gatewayIdentity (a model gateway, rollout step 4)`, and the same for `split`), and `split` with it also needs a meter (`meter.url` and `meter.query`: `loops.<class>.split needs a meter: a family's cap is in USD`). With `runs.gatewayIdentity`, each run calls the model gateway with its own token, on the alias its claim names (`spec.model`), and the meter prices it. Without it, USD budgets are not metered on that runtime: its runs carry no gateway identity, so `familyUSD`, the tiers' `runUSD` and `taskUSD`, and the principal caps spend nothing, and a configured meter says so at startup. That runtime then runs every role on modelSecret's model, and resolves no judge alias: its judge calls the run's model endpoint (`JUDGE_BASE_URL`) with the model Secret's optional `judge_model` key, or the run's own model without it (`JUDGE_MODEL`); the demo's `demo.model.judgeName` writes that key | The [shipped workflows](../factory.md#the-shipped-workflows)' `default` (`{ask: true, review: true, stallSteps: 80, judge: none}`), `quick` (`{ask: true, review: false, stallSteps: 80, judge: none}`) and `careful` rows: `quick` and `standard` end a run that commits nothing in 80 steps; only `careful` runs the completion judge |
| `workflows` | `<name>: {budget (light, standard or frontier), stages (up to 8 ordered stages), stepUp, description (≤ 120 bytes), effort, models}` or the legacy bundle `{template, tier, checks, loop, escalation}`; a workflow sets `stages` or the legacy keys (`template`, `tier`, `loop`, `checks`), not both. `none` is an adopted pull request's (no run) and takes no other key. In `stages`, the list is linear (`scoper` → `tester` → `implementer` → `reviewer`) with exactly one `implementer`: `scoper` takes `split: {maxParts: 2–5}` and `gate: {when: plan-first \| always}`; `tester` takes `when: repository.testFirst`; `implementer` takes `checks`, `search`, `stall`, `judge` (`evidence` or a judge alias) and `maxRounds` (0–5); `reviewer` takes `maxRounds` (0–5). `stepUp` names the one workflow a failed run steps up to on a verified failure (`quick` → `standard` → `careful` in the shipped ladder; `careful` names none); it cannot name itself, `none`, a cycle or a lower-ceiling workflow, and is mutually exclusive with `escalation`. `effort` and `models` are per-role overrides for a non-implementer role (built-in or declared in `roles`): absent effort is the harness's `high`, while a set value reaches the claim as `spec.effort` and the harness as `LLM_REASONING_EFFORT`; an absent model keeps the tier's model, then the loops row's public provider override. An internal run's model override is `tier-light`, `tier-standard` or `tier-frontier`, never a provider alias. Model overrides need `runs.gatewayIdentity`. The map's old name and a workflow's `testFirst` are refused ([upgrading](../upgrading.md#workflows-name-what-runs)) | The [shipped workflows](../factory.md#the-shipped-workflows) in the list form where the file omits them |
| `roles` | `<name>: {capability, model?, effort?, brief}` (at most 16): custom roles declared by the platform, each named like a workflow (lowercase DNS-like persona, never a built-in role). `capability` binds one of the closed profiles (`read` → `reviewer` read-only token; `tests` → `tester`; `write` → `implementer`); custom roles may bind `read` only (`tests` and `write` are reserved for the built-in tester and implementer). `brief` is a relative path under the factory's configuration directory (e.g. `roles/security-reviewer.md`, projected via the chart's `roleBriefs` value), read at load, at most 16 KiB; path traversal and symlinks escaping the config directory are refused | None: built-in roles only |
| `evaluation` | `{controlPercent (0–100), mode (observe or apply), treatment, treatmentHarness, treatmentModel, treatmentRoles}`: the team evaluation's control cohort of tasks with no pinned workflow (a class label is no pin), whether a recommended workflow is only recorded or used, and the workflow the treatment cohort runs on a task on the repository's default (a defined workflow other than `none`, else refused). Hashed apart from `triage.controlPercent`. `mode` governs the scoper's recommendation only: a workflow's `escalation` steps under `observe` too. `treatmentModel` (a gateway alias) puts the treatment cohort's `treatmentRoles` (default `[implementer]`) on that alias and changes nothing else, frozen as `status.profile.model` at triage, with `modelSource: treatment` on the runs; a provider alias reaches public tasks only. It is the measurement's one variable: refused beside `treatmentHarness` or `treatment`, and `treatmentRoles` without it is refused | `controlPercent` 0: no cohort; `mode` `observe` |
| `mergeGuardrails` | `{protectedFiles, mandatoryChecks, maxPatchFiles, maxModifiedLines, maxPerDay, maxGrantLifetime}`: central bounds every resolved policy gains. `protectedFiles` defaults to `^\.github/`, `^\.policy\.yml$`, `^CODEOWNERS$`; caps default to 10 files, 300 lines and 5 per day. A policy cap above one is clamped; an explicit binding raise is `GuardrailMismatch`; an unset cap inherits it. `minPromotionEvidence` was removed: set `evidence` on each `MergePolicy` | Defaults as listed; `maxGrantLifetime` 2160h (90 days) |
| `policyManagement.stage` | `read`, `simulate` or `manage`: how far repository-file management is rolled out. `read` observes the file, `simulate` adds structural/fixture simulation and dual evaluation, `manage` adds repository authority, approval memory and promotion. Legacy switch blocks are refused; see [upgrading](../upgrading.md#merge-policies-removals) | `manage` |
| `policyManagement.operators` | GitHub logins who decide promotion exceptions; read by the broker | None: no exception can be approved |
| `gitops` | `{repository, path, branch?}`: one GitHub repository and manifest file where policy-change pull requests are opened. Used by `maku policy pause/resume` and promotion; rollback drills revert a merged PR in its own repository. `branch` defaults to `main`. Maku never merges those pull requests | Unset: print the manifest edit or `gh pr create` command instead of opening a PR |

Each `repositories` entry:

| Key | Holds | Default |
|---|---|---|
| `name` | `owner/name` | Required |
| `maintainers` | GitHub logins that may label, retry and review on this repository; never a bot | Required |
| `triggerLabel` | The start label, `factory/ready` | Required |
| `access` | `{start: maintainers \| write}`: who may start a task. `write` lets anyone GitHub lets write start one, from a label, a hand-off or Slack, and charges it to them; it takes effect only with `budgets.perPerson.enabled` and `budgets.enforcePrincipal`, and until then a writer's label is answered `access_start_inactive`. Approving a plan, merging, `/factory retry` and the settings stay with `maintainers` | `maintainers` |
| `classes` | `<name>: {live, shadow, author: {adopt: {logins, branches}}, releaseAgeCheck, allowedFiles, protectedFiles, modifyOnly, maxPatchFiles, maxModifiedLines, diffShape, holdoutChecks, requires, oracle: {probe: dead-links, workflow, maxFindings ≤ 16}, maxPerDay, monitor: {oracleRuns, flux: {source, within}, followUpWindow}}`; `requires` names up to 4 stages (`plan`, `tests`, `judge`, `review`) added to any workflow touching the class's paths and never removes a stage (`tests` requires `tests.paths` and `merge.contractCheck`, else the gate hands off as `required_stage_missing`); `oracle` needs `holdoutChecks`, `diffShape: links-only`, `modifyOnly: true`, a non-empty `allowedFiles`, and, when `maxModifiedLines` is set, `maxModifiedLines >= 2 x oracle.maxFindings` (a retarget changes two lines); `live` and `shadow` never both; every `live` or `shadow` class needs `allowedFiles` or `diffShape`; `allowedFiles` patterns anchored `^…$`; `author.adopt` and the semver shapes: see [adopted classes](../factory.md#adopted-classes); `docs-links` holds `diffShape: links-only` built in; `review` and workflow fields (`checks`, `testFirst`, `harness`, `harnessAllowed`, `escalation`) are refused ([upgrading](../upgrading.md#merge-policies-removals)); see [merge policies](../merge-policies.md#a-binding) | Required when not served from a `Repository` |
| `checks` | `{egress: [profiles]}`: what a check-enabled run requests beyond `github`, at most 4 of `pypi`, `npm`, `golang`, `crates`, `maven` (Maven Central and Gradle's plugin portal and distributions, for the Maven and Gradle wrappers), `node` (`nodejs.org`, where `mise` installs Node). The repository's `.github/agent-checks.yaml` never names egress | None: github only |
| `merge` | `requiredChecks`, `verifyChecks`, `leakScanCheck` (in both lists), `contractCheck` (the job that runs a task's test contract: not one of `requiredChecks`, and set whenever the repository sets `workflow.testFirst`), `policyBotLogin` (required with `policyBot: true`), `policyBot` (true routes the gate through policy-bot, an opt-in; unset is false, so `maku/merge-gate` is the status branch protection requires, which needs `gateStatus`), `gateStatus` (posts the gate's verdict as the commit status `maku/merge-gate` on the pull request; unset is true; `demo` requires it), `mergerLogin` (bots), `autoRevert` (optional, false: on, the merger merges a green revert only at the head GitHub created), `paused` (optional `{reason}`: every automatic policy decision waits `paused`, approve-to-merge still works; remove it to resume), `maxPerDay` (the repository's auto-merges a day; unset inherits the guardrail's cap; `autoMergesPerDay` is refused), `fixRuns`, `checkReruns`, `verifyFor`, `revertWindow`, `awaitingCIFor`, `breaker: {window, maxReverts, maxFollowUps, recoverAfter}`, `approveToMerge` (`awaitingCIFor` optional, 24h when omitted, `0s` for no bound; `maxFollowUps` and `recoverAfter` optional, 0 is off; `recoverAfter` below `window`; `approveToMerge` optional, false is off: on, a maintainer's `APPROVED` review of an agents pull request's current head arms the merger. It acts only when a class of any configured repository is live, shadow or monitored (factory-wide, as the merger key is): with none, the task hands off `merge_gate_off` before the approval is read) | Required |
| `workflow` | `{default, testFirst, stepUp}`: `default` is the workflow a task runs when no label and no source names one (`standard` when unset; `none` is refused); `testFirst: true` starts every public task whose workflow's row sets `tests` with the tester and needs `merge.contractCheck`; `stepUp` (`auto`, `ask`, `off`; default `auto`) controls whether a failed task steps up automatically, waits with a deeper-run offer, or skips step-up | `standard`; no tester; `stepUp: auto` |
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
`mergePolicies` and a [`hardening`](../hardening.md#the-repositorys-hardening-block) block, neither of which decides
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

## Broker fields

### The broker's configuration

One file, `ROOMS_CONFIG` (a ConfigMap, Flux-substituted). Parsing is strict: an unknown key or a
`subPattern` without exactly one capture group fails the rollout, not the first request. The broker
reads it once at start, so restart it after a change. A machine issuer whose JWKS the broker cannot
fetch at start fails the rollout too; the `human` issuer does not: until it answers, humans get
`401` and agents are unaffected.

| Key | Meaning |
|---|---|
| `publicURL` | `https://rooms.<private domain>`, used in links |
| `runIssuers[]` | `{mode, issuer, jwksURL, subPattern}`. `mode` is `jwks` (default): the cluster's OIDC issuer, its JWKS URI per cloud (`<issuer>/keys` on EKS, `<issuer>/jwks` on GKE). `mode: tokenreview` asks the apiserver instead and takes no `issuer` or `jwksURL`: for a cluster whose issuer the broker cannot fetch (kind); the broker's ServiceAccount needs `system:auth-delegator` |
| `systemIssuer` | `{issuer, jwksURL}` for `rooms-system` tokens. Optional: only the factory uses it, and without it the system routes answer `401` |
| `systemPrincipals` | `{<sub>: <principal>}`, e.g. `system:serviceaccount:agent-system:agent-factory: system:factory` |
| `human.mode` | `oidc` (default): the block below. `local`: one `human:local` user in the admin and member groups, `:8080` bound to loopback (reach it by port-forward), `publicURL` exactly `http://localhost:<port>` (or a loopback address); the broker answers that port under `localhost`, `127.0.0.1` and `[::1]` and refuses any other Host or Origin. It takes no issuer, files, `origin`, `groups` or `access`; the config is refused otherwise ([quickstart](../quickstart.md)) |
| `human` | `{issuer, jwksURL, clientIDFile, roomctlClientIDFile, projectIDFile, origin, groups: {admin, member}}`. The ids are files read at use, because an IdP may mint new ones on every build; the group names are literals. `jwksURL` is optional with an `https://` issuer: its discovery document names `jwks_uri`, read at the first token. `preset` is `zitadel` (the default, today's token rules) or `generic`, any OIDC issuer: `rolesClaim` (required: a claim name, or an RFC 6901 pointer such as `/realm_access/roles`) holds the `groups` values, `tokenUse` is `id` (the default: the ID token is the credential) or `access` (with `audience` or `audienceFile`, which a sponsored principal needs too), and `projectIDFile` is refused. `mcp.sponsoredClientClaim` (`generic` only: `cid`, `appid`, `azp` or `client_id`) names a sponsored token's client, as the factory's `api.sponsoredClientClaim` does |
| `human.access` | `{ttl}`: room visibility follows GitHub ([room access](../security.md#identities)), the caller's login read from their [GitHub account link](../github-link.md); it needs `human.githubLink`. `ttl` caches each answer, default and maximum `5m`. Needs `ROOMS_GITHUB_APP_DIR`, or the broker refuses to start. Unset: only `agents-admin` sees rooms |
| `roomTools.knowledge` | `{url, tools, roles, timeout, maxBytes}`: a knowledge MCP endpoint, RunLore's, which `:8090` forwards [`kb_search` and `kb_get`](api.md#8090--room-tools) to. `url` is https, or http to an in-cluster `<svc>.<ns>.svc[.<domain>]`, with no credentials; `tools` a subset of `kb_search` and `kb_get` (both by default); `roles` AgentRun roles (`triager` by default); `timeout` 1 s to 12 s (10 s), under `:8090`'s 15 s bound on one call; `maxBytes` at most 256 KiB (64 KiB). Unset: no knowledge tool is served. The maku chart's `broker.roomTools.knowledge` renders it, and opens the broker's egress to `networkPolicy.to` on the URL's port, or `networkPolicy.port`; its schema refuses what the broker would (an external `http://` URL, credentials in it, a timeout over 12 s) |
| `human.mcp` | `{enabled}`: serve [`POST /mcp`](api.md#post-mcp), the MCP server `maku mcp` relays. Off by default. On, it needs `human.roomctlClientIDFile`, since `/mcp` takes the CLI's tokens only, or the broker refuses to start |
| `human.mcp.start` | `{default, except[]}`: where `maku mcp`'s `start` offers "Start now" (the form's field that also labels the issue `factory/ready`), for a caller who writes to the repository on GitHub. `default` is true unless set: on when `human.intake.plan.enabled` is; `except` lists `owner/name` repositories where the form never offers it (matched case-insensitively, listed once). Needs `human.mcp.enabled` and `human.access` (without the GitHub check `canStart` is always off). `human.mcp.sessionStart` was removed: a config that sets it is refused, `human.mcp.sessionStart was removed: use human.mcp.start` |
| `human.mcp.sponsored[]` | `{name, clientIDFile}`: the service accounts that may call `run_create` ([sponsored principals](sponsored-principals.md)); each is also declared in the factory's `api.sponsored`, which decides everything else. Needs `human.mcp.enabled` and `factoryURL`. The name is a DNS label, not reserved (`factory`, `runlore`, `scheduler`, `room-broker`, `policy`) and unique |
| `human.needs` | `{enabled}`: serve [`GET /api/needs`](api.md#get-apineeds) and the needs list of the `inbox` tool on `/mcp`, needs/v1's two doors. On by default: the list is read-only, and the room page, `maku needs` and the App read it. Off, both doors go together: the route is `404`, and `inbox` lists the caller's own tasks instead. Needs no other key |
| `human.slackLink` | `{principal, teamID, repository}`: the [Slack self-serve link](../slack.md#self-serve-link). `principal` is one of `human.mcp.sponsored` (the Slack App), the only caller of `slack_link_code` and `slack_unlink`; `teamID` the one workspace a code may be for; `repository` the `owner/name` whose GitHub App installation names the account's login on the page. Needs `human.access`. Serves `/link/slack` and, on `:8443`, `GET /v1/links/slack` to `system:factory` only. Off by default |
| `human.approvalMemory` | `true` serves saved approval grants (merge policy stage 5): [the page and its API](api.md) to humans, and `POST /v1/grants/lookup`, `/v1/grants/{id}/use` and `/v1/grants/{id}/disable` on `:8443` to the factory alone. Off by default: every path is a `404` and the factory's lookups are refused. The factory consults grants only under its own `policyManagement.approvalMemory` |
| `human.slackDecide` | `{principal, teamID}`: the Slack App's one deciding route, [`POST /api/slack/decisions`](api.md#post-apislackdecisions), where the Approve and Deny buttons of a `forge.push` card decide the approval. `principal` is one of `human.mcp.sponsored` (the Slack App), the only caller; `teamID` the one workspace whose clicks this broker decides. Needs `human.access`, since a decision is the linked human's room standing *and* repository write. Off by default: the path is then the mux's `404`. The App's own `channels.<id>.approvals` is the other half of the pair, and no card is drawn without it |
| `human.slackIntents` | `{principal, teamID, url, apiKeyFile, model, deadline, minConfidence, confirmTTL, maxInputChars, dailyCalls, perUserPerHour, retentionDays}`: serves [`intent_classify`](api.md#intent_classify-on-mcp), the Slack App's read-only classification of a thread reply into one of nine intents ([Slack](../slack.md#understanding-a-reply)). `principal` is one of `human.mcp.sponsored` (the Slack App), the only caller; `teamID` the one workspace whose users are classified; `url` the model gateway's in-cluster classifier route and `apiKeyFile` the `classifier` client's bearer, read at use; the maku chart fills both from `broker.modelGateway` when the block leaves them unset ([gateway](../gateway.md#the-planner-and-the-classifier)). `model` can only be `tier-light`. Defaults, tuned by the shadow data: `deadline` `5s`, `minConfidence` `0.7`, `confirmTTL` `10m` (1m to 1h), `maxInputChars` `2000` (at least 200), `dailyCalls` `500` and `perUserPerHour` `20` (the App's own budget, never a person's), `retentionDays` `30` (how long an opted-in channel's replies are kept). Off by default: the tool is then on no list. Refused in local mode |
| `human.dashboards[]` | `{label, url}`: links the room page shows in its title strip, such as Grafana dashboards, e.g. `- {label: Room metrics, url: "https://grafana.example.com/d/agent-room/rooms?var-room={room}"}`. None by default, and the strip then shows no group. The page fills `{room}` (the room id), `{run}` (its current or last run), `{task}` (its Task, named as the room), `{created}` (when the room was created, in epoch milliseconds) and `{ended}` (when its log was sealed, in epoch milliseconds, or the literal `now` while it runs) with `encodeURIComponent`, and hides a link whose placeholder the room has no value for. `…&from={created}&to={ended}` opens a Grafana dashboard on the room's own lifetime. At most 8; `label` is 1 to 40 printable characters; `url` is an absolute `https://` URL with no userinfo, its placeholders only `{room}`, `{run}`, `{task}`, `{created}` and `{ended}` and only in the path, query or fragment. Any other `{…}`, an unmatched brace or an unknown key fails startup. Allowed in local mode too |
| `factoryURL` | The factory's run API on the factory Service's `api` port: `https://agent-factory.agent-system.svc:8082` once the factory serves `api.tls`. The broker forwards each human's bearer token there, so an `http://` URL is refused at start unless `factoryInsecure` is `true`. The upgrade step is in [operations](../operations/rooms.md#upgrades). It moves with the factory chart's port and its `api.listen`. Unset: `start_run` returns a manifest for the owner to apply |
| `factoryCAFile` | PEM file of the CA that signs the factory's `api` certificate, read on every run request, so a rotation needs no restart. Unset: the system roots. The maku chart mounts `broker.factoryCA.secretName` (key `ca.crt`) at `/etc/room-broker/factory-ca/ca.crt` |
| `factoryInsecure` | `true` admits an `http://` `factoryURL`, sending human tokens to the factory in the clear: for a cluster whose CNI encrypts pod traffic, or a test. Default `false` |
| `tls` | `{certFile, keyFile}` of `:8443`; default `/etc/room-broker/tls/tls.{crt,key}`. Re-read when they change |
| `runs` | `{group, namespace, namePrefix}`: where AgentRuns live. Defaults `maku.ogenki.io`, `agents`, `run-` (the run-controller's); `group` takes no other value ([upgrading](../upgrading.md#the-xr-run-kind-and-octo-sts)). The factory's config takes the same block. Read once at start. At least one `runIssuers[].subPattern` must match `system:serviceaccount:<namespace>:<namePrefix><id>` and capture the id, or the broker refuses to start |
| `suspend` | `{enabled, after}`: the suspend trigger. Off by default; `after` defaults to `5m`, within `[1m, 25m]`, under the attended approval's 30 minutes. On, at each 15 s resync of an attended room, a run with `spec.suspendable: true` that is `Running`, has a pending approval and sits in a room whose log has not moved for `after` gets `agents.ogenki.io/operating-mode: Suspended` and `agents.ogenki.io/remaining-seconds`: its budget, or the seconds its pod was given, less its running time since `status.startedAt` or its last resume. Once no approval of the run is pending (decided, expired or superseded), it gets `operating-mode: Running` and `agents.ogenki.io/resumed-at` (RFC 3339). Each write is a merge patch under the claim's `resourceVersion`. It needs `patch` on `agentruns` in `agents`, and the platform's admission policy should limit the broker's AgentRun updates to these three annotations on a suspendable run |

| Broker environment | Meaning |
|---|---|
| `ROOMS_CONFIG` | Path of the config file |
| `ROOMS_DATABASE_URL` | The `uri` of `xplane-rooms-cnpg-role-rooms-broker` (the retention job: `…-rooms-retention`) |
| `ROOMS_MIGRATION_DATABASE_URL` | `migrate` only: the `uri` of `xplane-rooms-cnpg-role-rooms-owner`. The migrations run as that role and refuse any other, so the broker's own DSN never reaches them |
| `POD_NAMESPACE` | Where Rooms and the leader lease live |
| `LOG_FORMAT`, `LOG_LEVEL` | `json`, `info` (defaults); `text` and `debug` locally |
| `ROOMS_MCP_KEY` | The key the MCPRoute injects. Unset: `:8090` refuses every call |
| `ROOMS_GITHUB_APP_DIR` | The factory App's key volume (`app_id`, `private_key`; optional, `defaultMode: 0440` with the pod's `fsGroup`). Unset: no verdict is posted. Set but empty: verdicts wait, up to 24 h, for the key, and room access checks fail closed. Room access reads `GET /user/{id}` and `GET /repos/{o}/{r}/collaborators/{login}/permission` with it (`metadata: read`) |

### The bridge's environment

Set by the `AgentRun` composition or run controller.

| Variable | Value |
|---|---|
| `ROOM_ID`, `RUN_ID` | The run's `roomRef` and id |
| `CONVERSATION_ID` | The harness conversation: the `AgentRun`'s uid |
| `BROKER_URL` | `https://room-broker.agent-system.svc.cluster.local:8443` |
| `BROKER_CA_FILE` | `/etc/room-broker-ca/ca.crt` |
| `HARNESS_URL` | `http://127.0.0.1:8000` |
| `ROOM_TOKEN_FILE` | `/var/run/secrets/agents/room/token` |
| `EGRESS_PROFILES` | The run's egress allow-lists, comma-separated. The classifier reads them: a package install outside them is `egress.new` |
| `HEALTH_ADDR` | `:8085` (default): `/healthz` for kubelet only, and `POST /final-read` for the harness on loopback: the bridge reads the harness log to its end and mirrors it, then answers `{"events","unmirrored","sealed"}` within 3 s (`504` past it). Its body may be `{"endReason": "<reason>"}` (at most 256 bytes), the reason one of `judge_impossible`, `stalled`, `not_met` and `budget_calls`: the bridge records it once as `state_changed{harness_end}` before it reads the log. `agent-run` calls it on SIGTERM before it stops agent-server. The bridge serves no metrics; the broker counts its stalls and stubs |
| `LOG_FORMAT`, `LOG_LEVEL` | `json`, `info` (defaults); `text` and `debug` locally |
| `FLUSH_GRACE` | How long the SIGTERM drain may take; `25s` (default), at most `28s`. Set it lower when the harness uses much of the pod's 30 s grace: the kubelet signals the sidecar only after the harness exits |
| `GOMEMLIMIT` | Unset: the binary sets `48MiB`, the soft heap limit its buffer budget is sized for inside the 64 Mi limit. A pod spec may set its own |
| `BRANCH` | The run's branch: the one push that is `forge.push`. Unset, every push is `forge.other` |
| `MCP_READ_ONLY_TOOLS` | The MCP tools that only read, comma-separated `<server>__<tool>` names (a tool the gateway calls `<server>_<tool>`, as agentgateway does, matches the same entry); the classifier lets them through as plain, and every other MCP tool is `mcp.write`. Unset, every MCP tool is `mcp.write`, the room tools included (fail closed), and the bridge logs a `WARN` at start. Set it in the composition before a bridge that reads it rolls out: an older bridge ignores it, a newer one without it classes every room tool call `mcp.write`, which an attended room sends to an approver and an unattended one denies |
| `MCP_BARE_SERVER` | A server name (no underscore): a tool name with no `__` that is not a builtin is read as `<server>__<tool>` and looked up in `MCP_READ_ONLY_TOOLS`. For a harness that calls the server directly, as the demo's runs call the broker's room tools; the run controller sets it to `room-broker`, with the five room tools and `kb_search` and `kb_get` in `MCP_READ_ONLY_TOOLS`, when `roomToolsURL` is set. The knowledge tools are always listed: the broker alone decides whether it serves them. Unset, a bare name is `mcp.write` |
| `BROWSER_GATE_URL` | The run's browser gate, `http://127.0.0.1:9223` on a run with screenshots; only an `http` loopback URL is taken. Set, the bridge polls `GET /shots?after=<n>` every 2 s once it holds the room, uploads each new screenshot once to `POST /v1/bridge/screenshots`, retries a failure that may pass, drops a refused image with one log line, and flushes what is left at the final read and the SIGTERM drain. Unset, no screenshot is relayed |
| `AGENT_SUSPENDABLE` | `true` on a run whose `AgentRun` has `spec.suspendable: true`, the harness's variable of the same name. Its new pod's conversation reloads paused, and the bridge alone continues it: a pending step is answered from the room's decision, as a waiting one is; with none, one `/run`. Only a pause this bridge found at its first status counts, so a human's interrupt stays one. Unset, a paused conversation is left paused |

The list the bridge carried built in before `MCP_READ_ONLY_TOOLS` existed, which the composition sets:

- `flux-operator-mcp`: `search_flux_docs`, `get_flux_instance`, `get_kubernetes_api_versions`, `get_kubernetes_resources`, `get_kubernetes_metrics`, `get_kubernetes_logs`
- `mcp-victoriametrics`: `documentation`, `query`, `query_range`, `metrics`, `metrics_metadata`, `labels`, `label_values`, `series`, `alerts`, `rules`, `explain_query`, `prettify_query`, `metric_statistics`, `tsdb_status`, `active_queries`, `top_queries`
- `mcp-victorialogs`: `documentation`, `query`, `hits`, `facets`, `field_names`, `field_values`, `stats_query`, `stats_query_range`, `streams`, `stream_ids`, `stream_field_names`, `stream_field_values`, `flags`
- `room-broker`: `room_read`, `room_post`, `room_progress`, `room_handoff`, `room_verdict` (the room tools only append to the room)

A name matches on its last `<server>__<tool>` pair, so a prefix the harness adds cannot hide the server.

Resources: requests 20m / 32Mi, limits 100m / 64Mi; read-only root filesystem, all capabilities
dropped.

### The gate

The composition adds `maku-bridge gate`, the same image with the argument
`gate`, as a plain init container after the bridge and before the harness. It reads the bridge's
`/admission` on `127.0.0.1` at `HEALTH_ADDR`'s port, exits 0 once the bridge holds the room, and
exits 1 once the room refuses the run. The pod's `restartPolicy: Never` then fails the run before
the harness starts. Without the gate the lease only holds the log: a refused run still executes,
unrecorded, on the room's shared branch.

### What the broker reads from `AgentRun`

`spec.roomRef` (immutable), `spec.role`, `spec.principal`, `spec.branch`, `spec.budget.maxMinutes`,
`spec.dataClass`, `spec.egress.profiles`, `spec.repository`, `status.phase`, `status.startedAt`, the
annotation `agents.ogenki.io/revoked`, and `metadata.uid`. The run's CNP already opens egress to
`room-broker` on 8443, and DNS for its name, when `roomRef` is set.

### What the factory gets

| Contract | Where |
|---|---|
| Create a room | Create a `Room` CR with `spec.owner` and `spec.driver: system:factory` and the required `spec.dataClass` ([fields](room-crd.md)) |
| Read a room | `GET /v1/rooms/{id}/events` on `:8443`, audience `rooms-system` ([API](api.md#get-v1roomsidevents)) |
| Report task state | `POST /v1/rooms/{id}/messages`, `kind: task_state` |
| Report task facts | `POST /v1/rooms/{id}/task`: phase, run, budget, issue and PR as `state_changed{kind: task}`, which the room summary and `Room.status.task` read ([API](api.md#post-v1roomsidtask)) |
| Queue a message for the next run | `POST /v1/rooms/{id}/queue`, listed by `GET` and consumed by `POST …/queue/consume` ([API](api.md#the-queue-routes)) |
| Verdicts and handoffs | `message{kind: review_verdict}` and `handoff` in the log; the verdict comment is the broker's, the factory never posts it again |
| Who drives | `Room.status.driver` and `driverEpoch`: the factory never advances a room while a `human:` holds the token |
| Run requests | `POST {factoryURL}/v1/runs` on the factory's `:8082` (TLS, or plaintext under `factoryInsecure`) with `{role, repository, baseRef, task, dataClass, roomRef, egressProfiles?}`, the human's own access token as `Authorization: Bearer` (never an asserted `sub`), and `Idempotency-Key: <roomId>:<originClient>:<originSeq>`, the act's own key. The broker requests the run, then records it, so a client retrying a failed record sends the same key and gets the same run; a request without a key is refused before the call (`internal/runrequest`). How the factory answers a key, a replay and a refusal: [the factory's API](api.md#the-factorys-api) |
