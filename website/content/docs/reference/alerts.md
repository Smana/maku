---
title: "Alerts reference"
weight: 120
description: "Alert rules shipped in the umbrella chart's PrometheusRule maku (Alerts)."
diataxis: reference
---

Alert rules shipped in the umbrella chart's `PrometheusRule` `maku` ([Alerts]({{< relref "/docs/operate/failures.md#alerts" >}})).

## Room alerts

| Alert | Fires when | For | First step |
|---|---|---|--- |
| `RoomBrokerDown` | No broker replica is up | 5 min | `kubectl get pods -l app.kubernetes.io/name=room-broker`. Runs keep working and their bridges buffer; nothing reaches the log |
| `RoomLogAppendErrors` | Any append failed on the database in 10 min | 10 min | The database the broker's DSN names, and the broker's log |
| `RoomRedactionsSpike` | More than 20 secrets redacted in 15 min | — | An agent is handling credentials: find the rooms with [the redactions query]({{< relref "/docs/operate/rooms.md#reading-the-log-with-sql" >}}) |
| `RoomStalled` | A room with a `Running` run has had no event for 30 min | — | Its run is `Running` but silent: read the bridge's logs (below) |
| `RoomBridgeHarnessStalled` | A bridge reported a stall on the harness log, by reason | — | The bridge's logs (below): the transcript stops at the stall |
| `RoomBridgeItemsRefused` | The broker answered `400` to a lone harness item | — | The bridge and the broker disagree on the envelope: the broker logs `bridge item refused` |
| `RoomBridgeItemsUnstorable` | Items kept as stubs, `invalid_value` or `key_collision` (info) | — | Expected now and then; a steady rate is a harness pattern worth a look |
| `RoomAuthnJWKSStale` | An issuer's keys have not been refreshed for 6 h | 5 min | The broker's egress to that issuer; at 24 h its tokens are refused |
| `RoomAuthnHumanIssuerNeverFetched` | The broker is up and has never fetched `human.issuer`'s keys; rendered only with an issuer, outside `human.mode: local` | 30 min | The broker's egress to the issuer: every human is refused until it does |
| `RoomRejectedActionsSpike` | More than 30 actions refused in 10 min | — | Someone is probing, or the UI disagrees with the policy |
| `RoomVerdictsNotReachingGitHub` | Posting errors, and no successful post in 30 min | 30 min | The factory App's key; egress to `api.github.com` |
| `RoomApprovalPendingTooLong` | An approval has waited over 15 min | — | Decide it; undecided, it is denied at 30 min (attended) or `approvals.ttl` (unattended) |
| `RoomWebhookDisabled` | A webhook subscriber was disabled after three given-up deliveries | 1 min | [Webhooks]({{< relref "/docs/operate/factory.md#webhooks" >}}) |
| `RoomKnowledgeFailing` | More than half of the knowledge calls forwarded to `roomTools.knowledge.url` failed (`upstream_error`) or timed out, role and argument refusals left out; rendered only with `maku.broker.roomTools.knowledge.url` | 30 min | The endpoint, the broker's egress to it (`knowledge.networkPolicy.to`), and the broker's `knowledge call failed` log lines. Triager runs investigate without the knowledge base meanwhile |


`RoomWebhookDisabled` fires on `max by (subscriber) (maku_rooms_webhook_disabled) == 1`. The rule lives in
ai-native-ref's `vmrule-agent-factory.yaml`, which comes with ai-native-ref's PR F: until it merges, nothing alerts.


## Factory alerts

| Alert | Fires when | For | First step |
|---|---|---|---|
| `AgentFactoryIntakeErrors` | The triggers' polls fail | 15 min | Every replica's log, `poll failed`; a 401 is the factory App's key or installation |
| `AgentFactoryAutoMergeReverted` | A merged agent pull request was reverted | — | Its class is paused by the breaker; read why on the Task (`Reverted`) |
| `AgentFactoryMergeRegression` | The post-merge monitor found a regression, by class and repository | — | The class is demoted at its next decision; the pull request's monitor comment names the signal |
| `AgentFactoryClassPausedLong` | A class (any but the empty one) has been paused 7 days | 7 days | The [breaker]({{< relref "/docs/concepts/factory.md#the-merge-gate" >}}). A shadow or unarmed class's pause is inert: leave the rule out with `disabled` to keep a live-classes-only variant |
| `AgentFactoryBindingRefused` | A Repository's `BindingsValid` condition has been false 15 min; rendered with `kubeStateMetrics` | 15 min | The condition's message names the binding and the fix; the last good policy may keep serving |
| `AgentFactoryPolicyPausedLong` | A Repository's merge policies (`spec.merge.paused`) have been paused 24 h; rendered with `kubeStateMetrics` | 24 h | Read the reason in the `Paused` condition; resume by removing the block |
| `AgentFactoryDailyBudgetHigh` | A principal spent over 80 % of today's USD cap | 5 min | [Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}}) |
| `AgentPersonCapShadowRefusal` | A run was admitted that `enforcePrincipal` would refuse (info) | — | [Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}}) |
| `AgentFactoryTaskStuck` | A task has been in a working phase over 2 h | 5 min | `kubectl get task <task> -o yaml`; the logs carry `task.id` |
| `AgentFactoryKillSwitchEngaged` | The stop is engaged (info) | — | [The kill switch]({{< relref "/docs/operate/failures.md#escalated-tasks-and-the-kill-switch" >}}) |
| `FactoryGitHubUnreachable` | `maku_factory_github_up == 0` | 10 min | GitHub's status and the App key (`agent-factory-github`): [below]({{< relref "/docs/operate/failures.md#factory-failures" >}}) |
| `AgentFactoryMeterBlind` | The meter has not read usage for 5 min, or never | 1 min | [Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}}) |
| `AgentFactoryRepositoryNotOwned` | `max by (repository) (maku_factory_repository_owned) == 0` | 20 min | The repository's `.github/agent-factory.yaml` on its default branch: absent, unparsable, or naming another factory. A read that fails turns the gauge 0 after 15 min |
| `AgentFactoryCoordinationRecoveryRequired` | A family has waited on its recovery over 15 min | — | [Coordination]({{< relref "/docs/operate/factory.md#coordination" >}}): act on the parent's one proposed action |
| `AgentFactoryCoordinationIntegrationFailed` | An integration run ended `checks_failed` or `infrastructure_failed` | — | [Coordination]({{< relref "/docs/operate/factory.md#coordination" >}}) |
| `AgentFactoryCoordinationAuthorizationLost` | A child's admission was refused for lost authority | — | [Coordination]({{< relref "/docs/operate/factory.md#coordination" >}}) |
| `AgentFactoryCoordinationStalled` | A primary has a `Coordinating` parent and no family change in 24 h | 15 min | [Coordination]({{< relref "/docs/operate/factory.md#coordination" >}}): it usually waits on a person |
| `AgentModelUnpriced` | A model served runs without a `meter.prices` row | — | [Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}}) |
| `AgentMeterInputExcludesCache` | A gateway counted a model's input without its cache tokens | — | [Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}}) |
| `FactoryAgentMerged` | An `agent_merged` outcome in 15 min, the first one of a repository included | — | An agent token merged a pull request past the factory's own merge. Find it in the factory's error log (`the agents App merged a pull request`), check the ruleset requires `maku/merge-gate` from the posting App, that the agents App is not one of its bypass actors and that no run holds a person's token, and review the merged change |
| `FactoryMergeGateNotRequired` | `min by (repository) (maku_factory_merge_gate_required) == 0` | 30 min | The repository's ruleset no longer requires `maku/merge-gate` from the App that posts it: [branch protection for the merge gate]({{< relref "/docs/concepts/factory.md#branch-protection-for-the-merge-gate" >}}) |
| `FactoryMergeGateRulesUnread` | `increase(maku_factory_merge_gate_rules_unread_total[30m]) > 0` | 30 min | The gauge is stale: the factory's log names the failed read (the merger App id file, the repository, or the branch rules) |
| `AgentFactoryPrivateCodeUnacknowledged` | `max by (repository) (maku_factory_private_code_unacknowledged) == 1` | 15 min | A served repository is private code without `spec.privateCode.acknowledged`: set it on the `Repository`, or stop serving it ([private code]({{< relref "/docs/install/_index.md#private-code" >}})). The next release refuses it |
| `FactoryRoutinePaused` | `max by (repository, routine) (maku_factory_routine_paused) == 1` (info) | 5 min | Inspect state with `maku routine status <repo> <routine>` and resume with `maku routine resume <repo> <routine>` when ready |
| `FactoryUpkeepBudgetExhausted` | `system:upkeep` spent reached `budgets.upkeepDailyUSD` (> 0) or cron upkeep skipped with `reason="upkeep_budget"` | 5 min | Human-triggered tasks and manual runs continue unaffected; inspects today's ledger or wait for 00:00 UTC reset ([Budget signals]({{< relref "/docs/operate/failures.md#budget-signals" >}})) |

No series, no alert: on a release without a gauge the expression is empty, so a rule is safe to ship ahead of it.

## Run path alerts

| Alert | Fires when | For | First step |
|---|---|---|---|
| `RunControllerPreflightFailing` | A [preflight]({{< relref "/docs/operate/failures.md#the-run-controllers-preflight" >}}) check refuses new runs, by `check` | 10 min | The run-controller's `preflight failed` line names the check and its fix |
| `RunControllerDown` | The run-controller is not up | 5 min | `kubectl get pods -l app.kubernetes.io/name=run-controller`. No run starts or ends; running pods keep running |
| `EgressProxyDown` | No egress proxy replica is up; rendered in `networkPolicies.mode kubernetes` | 5 min | `kubectl get pods -l app.kubernetes.io/name=egress-proxy`. The runs' HTTPS fails |
| `MakuEgressInstallClosedRefusal` | The egress proxy refused a run's CONNECT to a host of its install set (GitHub's release hosts) after the set closed; rendered in `networkPolicies.mode kubernetes` | none | `kubectl logs -l app.kubernetes.io/name=egress-proxy \| grep install_closed` names the run; read its steps for the download it tried |
| `AgentSandboxPodPending` | A pod in the runs namespace has been `Pending` 15 min; rendered with `kubeStateMetrics` | 15 min | `kubectl describe pod -n <runs namespace> <pod>`: capacity, the sandbox RuntimeClass's nodes, the runs quota |

## Slack alerts

| Alert | Fires when | For | First step |
|---|---|---|---|
| `SlackDecisionsSpike` | More than five `forge.push` approvals decided from Slack in 10 min | — | The broker's `slack decision` lines and the room log: a burst is the shape of a compromised App pod |
| `SlackCardUpdatesFailing` | The App has failed approval-card updates | 1 h | The App's `card update failed` lines: a revoked `chat:write`, a removed message |

## Gateway alerts

| Alert | Fires when | For | First step |
|---|---|---|---|
| `AgentRouterMetricContractBroken` | Model routes answer 200 with no `agentgateway_gen_ai_client_token_usage_count` increase, or a token type other than `input`, `output`, `input_cache_read`, `input_cache_write` appears | 15 min | An agentgateway upgrade renamed what `meter.query` reads: the meter reads zero or misprices until the query follows |
| `AgentGateway5xxRatioHigh` | Over 5 % of the gateway's answers are 5xx, and more than five in 10 min | 10 min | Split by `route` and `reason`: `UpstreamFailure` or `Timeout` is the provider, `Internal` the proxy |
| `AgentGatewayModelErrorRatioHigh` | Over 10 % of model calls carry an `error_type` | 10 min | The access log's `http.status` and `error` fields name the cause |
| `AgentGatewayUnauthorizedBurst` | More than 20 `JwtAuth` or `Authorization` refusals in 5 min | — | Replayed or foreign tokens, or a caller whose token projection broke |
| `AgentGatewayCostUnpriced` | A catalog lookup other than `Exact`, by caller, model and status | — | `Missing` or `Unpriced`: add the model to `meter.prices` (or `maku-gateway.modelCatalog.prices`). `NoCatalog`: the catalog is not mounted |
| `AgentCostCrossCheckMismatch` | A run's ledger total and the gateway's priced cost differ by more than max(1 %, $0.01) once its cost stopped moving, or the gateway priced spend the ledger never saw | 15 min | Neither side is presumed right: a double charge, a lost ledger write, a catalog price that differs from `meter.prices`, an unpriced lookup, a scrape gap |
