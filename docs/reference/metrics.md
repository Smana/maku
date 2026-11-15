<!-- page-type: reference -->
# Metrics reference

Prometheus metric series exported by the room broker and the factory. For scraping, probes and operational context see [Operating rooms](../operations/rooms.md#metrics), [Operating the factory](../operations/factory.md#factory-metrics) and [Observability](../observability.md).

## Contents

- [Room broker metrics](#room-broker-metrics)
  - [Bridge stall and stub metrics](#bridge-stall-and-stub-metrics)
- [Webhook metrics](#webhook-metrics)
- [Factory metrics](#factory-metrics)
- [Coordination metrics](#coordination-metrics)
- [Hardening metrics](#hardening-metrics)
- [Spend and principal metrics](#spend-and-principal-metrics)

## Room broker metrics

| Metric | Type | Labels | Meaning |
|---|---|---|--- |
| `maku_rooms_build_info` | gauge | `version` | Always 1; carries the running version |
| `rooms` | gauge | `phase` | Rooms per phase |
| `maku_rooms_coordination_views_total` | counter | `outcome` (`ok`, `restricted`, `not_found`, `unverified`, `other`) | Coordination parent views asked for, and whether a child was withheld |
| `maku_rooms_coordination_checkout_manifests_total` | counter | `outcome` (`served`, `restricted`, `not_ready`, `unavailable`, `other`) | Coordination checkout documents asked for |
| `maku_rooms_policy_changes_total` | counter | `repository`, `class`, `change` | With `policyManagement.promotion`, the [promotion workflow](../factory.md#promotion)'s records: `promotion_requested`, `promotion_pr_opened`, `promotion_merged`, `promotion_abandoned`, `exception_requested`, `exception_approved`, `exception_refused`. `repository` and `class` are only those the factory's evidence named; the request and exception ids live in `policy_promotions` and `operator_exceptions`, never in a label |
| `maku_rooms_human_roles_claim_refused_total` | counter | `reason` (`missing`, `type`, `bounds`) | Human tokens whose roles claim (`human.rolesClaim`) gave no group: absent, another shape than an array of strings, an object or one string, or over 256 names or 256 bytes a name. The token's principal holds no group and is refused as not in an agents group. The factory counts its own as `maku_factory_human_roles_claim_refused_total{reason}` |
| `maku_rooms_events_appended_total` | counter | `type`, `origin` | Durable events appended |
| `maku_rooms_append_seconds` | histogram | — | Append latency |
| `maku_rooms_append_errors_total` | counter | — | Appends that failed on the database: not refusals of the value (SQLSTATE class 22), the room or the lease |
| `maku_rooms_redactions_total` | counter | `rule` | Secrets redacted |
| `maku_rooms_last_event_timestamp_seconds` | gauge | `room` | Last durable event of each room with a `Running` run, whatever its phase: 30 silent minutes turn such a room `AwaitingHuman`, and the series must outlive that flip |
| `maku_rooms_authn_jwks_last_refresh_timestamp_seconds` | gauge | `issuer` | Last successful JWKS fetch per issuer. Held keys stop verifying 24 h after it. Every replica refreshes each issuer hourly, with jitter, whether or not tokens arrive; a failed refresh leaves it, so it ages only while the issuer is unreachable. The human issuer has no series until its first fetch: the broker starts without it |
| `maku_rooms_connections` | gauge | `kind` | Open connections |
| `maku_rooms_connections_dropped_total` | counter | `reason` | Connections the broker closed: `reauth`, `slow_consumer`, `write_timeout` (a live peer took no frame within 10 s), `ping_timeout`, `shutdown`, `log_unavailable`, `protocol` (a malformed or oversize frame, or a first frame that is not `hello`, or none within 10 s), `access_revoked` and `access_unverified` (the room-access re-check on an open socket) |
| `maku_rooms_run_info` | gauge | `run`, `model`, `effort` | 1 for each live run whose bridge reported a `harness_status` with an effort (an older harness reports none, and has no series). Each label's source: `run` is the `AgentRun` the broker's watch holds; `model` is that claim's `spec.model`, read from the watch and bounded to 1 to 64 of `A-Za-z0-9._:/@+-`, never the sandbox's; `effort` is the run's harness, which the agent can rewrite, so it is a label only as one of the seven `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, and the broker strips any other. It is the one payload field among the broker's labels, accepted because that set bounds it. The replica that appended the status holds the series until its `AgentRun` watch sees the run end; a bridge that reconnects to another replica moves it, so aggregate with `max by (run, model, effort)`. A replica that restarts has none until the run's next status change. At most 1024 runs per replica: at the cap, ended runs make room first. Join it on `run` with the factory's `maku_factory_run_charge_info` or the gateway's series |
| `maku_rooms_run_info_dropped_total` | counter | — | Runs `maku_rooms_run_info` left out because its 1024 places held only live runs. Anything above 0 is a fault |
| `maku_rooms_participants` | gauge | `kind` | Live participants. `kind="run"`: live runs naming a room, counted by every replica's `AgentRun` watch, so aggregate with `max`, never `sum`. `kind="human"`: open viewer WebSockets on this replica, so `sum` across replicas |
| `maku_rooms_fanout_lag_seconds` | histogram | — | Append to delivery on a viewer's connection; the target is p95 < 0.5 s |
| `maku_rooms_fanout_listener_up` | gauge | — | 1 while the replica's fan-out hub holds its `LISTEN` connection. At 0 the hub polls every subscribed room each second, so viewers still get every event, up to a second late; `/readyz` ignores it on purpose |
| `maku_rooms_rejected_actions_total` | counter | `reason` | Actions refused (`not_permitted`, `stale_epoch`, `approval_expired`, `mcp_needs_roomctl`, …) |
| `maku_rooms_knowledge_calls_total` | counter | `tool` (`kb_search`, `kb_get`, `other`), `outcome` (`ok`, `clipped`, `refused`, `timeout`, `upstream_error`, `bad_answer`, `invalid_arguments`, `other`) | With `roomTools.knowledge`, the knowledge tools' calls on `:8090`: `refused` is a role that may not call them, `invalid_arguments` a call the broker refused on its arguments (it never reached the endpoint), `upstream_error` an answer the endpoint refused or failed, `bad_answer` one the broker cannot read, over max(8 × `maxBytes`, 1 MiB) included |
| `maku_rooms_human_mcp_calls_total` | counter | `tool`, `outcome` | `POST /mcp` tool calls: `ok`, `refused` (the tool's own refusal or a room read's) or `error`. A `room_tail` task counts `ok` when it is created and `error` when a read of it fails |
| `maku_rooms_slack_links_total` | counter | `outcome` | The [Slack self-serve link](../slack.md#self-serve-link): `link_code`, `linked`, `unlinked`, and the refusals `code_invalid`, `not_linked_github`, `wrong_team`, `too_many_links`, `too_many_codes`, `already_linked`, `not_web` (a maku or MCP token on the page), `bad_origin`, `error` |
| `maku_rooms_github_links_total` | counter | `outcome` | The [GitHub account link](../github-link.md)'s steps and refusals: `started`, `linked`, `relinked`, `unlinked`, `admin_unlinked`, `state_invalid`, `cookie_mismatch`, `sub_mismatch`, `github_account_taken`, `github_error`, `not_web`, `too_many_pending`, `bad_origin`, `error` |
| `maku_rooms_slack_decisions_total` | counter | `result`, `decision` | The Slack App's [deciding route](api.md#post-apislackdecisions), one count per answer: `result` is the closed code it answered with (`approved`, `denied` and every refusal of that table, `bad_request`, `rate_limited`, `unavailable`), and `decision` the decision it answered with, empty when the answer carried none. A replayed click counts as the decision it answered with, while the log keeps its one event. A rise in `result="unavailable"` is a store, room or log read that failed; the App retries those, and no other code |
| `maku_intent_classify_total` | counter | `outcome` | The broker's [`intent_classify`](api.md#intent_classify-on-mcp) calls: `ok`, `low_confidence` and `invalid_output` (the model's answer, read as unknown), `timeout`, `capped` (the App's daily or the author's hourly cap) and `error` (the gateway). See [intents in shadow](../operations/factory.md#intents-in-shadow) |
| `maku_intent_classify_duration_seconds` | histogram | — | One classification, the model call included |
| `maku_rooms_verdict_posts_total` | counter | `result` | Verdict comments `posted`, `not_posted` or `error` |
| `maku_rooms_driver_changes_total` | counter | — | Driver token changes: a human's request, give or take, and the leader's lease expiries |
| `maku_rooms_approvals_pending` | gauge | — | Undecided approvals in open rooms: the sum of the Rooms' `status.pendingApprovals` |
| `maku_rooms_approvals_oldest_pending_seconds` | gauge | — | Age of the oldest undecided approval in an open room, set by the leader's approval sweep every 30 s; 0 on a replica that stopped leading |
| `maku_rooms_approval_decision_seconds` | histogram | — | Request to decision |
| `maku_rooms_db_tls` | gauge | `role` (`rooms_broker`) | 1 while the broker's database connection is encrypted, 0 while it is plain: a plain connection is logged once at start and served (the demo's in-cluster hop is plain). See [the database](../database.md) |


### Bridge stall and stub metrics

Two more count what bridges tell their rooms. Nothing dials into a sandbox, so the bridge serves
no metrics: the broker counts these events as it appends them, and exports them on `:9090` with the
rest. Neither carries a run or room label.

| Metric | Type | Labels | Meaning |
|---|---|---|--- |
| `maku_rooms_bridge_harness_stalls_total` | counter | `reason` | Stalls on the harness log (`event_too_large`, `cursor_lost`, `next_page_unreadable`): a bridge's `state_changed{harness_error}` with that code, told once per stall; the cursor holds. The broker cannot tell a harness's own `ConversationErrorEvent` from a stall when its code is one of these three, so that one is counted too; any other code is not |
| `maku_rooms_bridge_items_stubbed_total` | counter | `reason` | Harness items kept as a stub in their slot, by why, from exactly this set: `refused` (the broker answered `400` to a lone item; the bridge's `harness_event{harnessKind: refused}`), `invalid_value` (the store rejected a value, SQLSTATE class 22, such as `\u0000` in tool output), `key_collision` (redaction refused keys that are one once case-folded or NUL-stripped), `oversize` (the bridge's or the store's size stub). A harness event whose own kind is one of these names is counted the same way |


## Webhook metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `maku_rooms_webhook_deliveries_total` | counter | `subscriber`, `outcome` | Attempts: `delivered`, `retried`, `failed` (given up), `no_key` (the key file is unreadable: the room is held, counted once per sweep, and nothing is dropped or counted against the subscriber), `unresolved`, `mismatch` (the log and the Room name different repositories) |
| `maku_rooms_webhook_disabled` | gauge | `subscriber` | 1 while the subscriber is disabled |


## Factory metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `maku_factory_build_info` | gauge | `version` | Always 1; carries the running version |
| `maku_factory_tasks` | gauge | `phase`, `source`, `predicted_class`, `tier`, `repository`, `principal` | Tasks by state and payer ([spend per identity](../operations/factory.md#spend-per-identity)) |
| `maku_factory_task_phase_seconds` | gauge | `task`, `phase` | Seconds an open task has spent in its phase |
| `maku_factory_kill_switch_engaged` | gauge | — | 1 while the stop object exists; `factory/stop` on the control issue engages it by creating that object |
| `maku_factory_time_to_pr_seconds` | histogram | `source`, `tier`, `template`, `repository` | From intake to the pull request opening |
| `maku_factory_pr_outcomes_total` | counter | `class`, `outcome`, `repository`, `principal` | `auto_merged`, `approved_merged` (the merger on a maintainer's approval; the pull request carries the label `factory/approved-merge`), `human_merged`, `agent_merged` (merged by `agentsLogin`, an agent token with `contents: write`, until H3: logged at error level and otherwise ended as a human merge, see [security](../security.md)), `closed`, `reverted` (a [drill](../factory.md#the-merge-gate) revert included: the breaker does not count it, this counter does) |
| `maku_factory_merge_gate_required` | gauge | `repository` | For a repository with `merge.gateStatus`: 1 when its default branch's rulesets require `maku/merge-gate` from the App that posts it, else 0, as the last fence pass read it. On each drop to 0 the factory logs `maku/merge-gate is not required from the App that posts it on <branch>: anyone with write access can merge an agents pull request`. A failed read keeps the last value and counts `maku_factory_merge_gate_rules_unread_total`. It never refuses a merge. It reads the rules only: not the rulesets' bypass actors, so a ruleset an App or the agents' role may bypass still reads 1, and not their enforcement mode (GitHub documents that `evaluate` rulesets are left out of the read). See [branch protection for the merge gate](../factory.md#branch-protection-for-the-merge-gate) |
| `maku_factory_merge_gate_rules_unread_total` | counter | `repository` | Fence passes over a repository with `merge.gateStatus` that could not read its App id, its default branch or its branch rules. `maku_factory_merge_gate_required` keeps its last value meanwhile, so a rising count means that value is stale |
| `maku_factory_github_app_permission_missing` | gauge | `app` (`agents`, `factory`), `permission` | Under `runTokens.perRun`, read once when the replica becomes leader and never refreshed since: 1 for each permission an App's installation lacks on a served repository, against that App's required set in [GitHub Apps](../github-apps.md); 0 only when every installation was read and grants it. Each missing permission is also logged once. A run whose token needs it fails to mint with `permission_not_granted`; grant the permission on the installation and restart the factory |
| `maku_factory_prs_opened_total` | counter | `principal`, `repository` | Task pull requests opened, where `maku_factory_time_to_pr_seconds` records |
| `maku_factory_task_tokens` | histogram | `tier`, `template`, `predicted_class`, `repository` | Tokens per finished task |
| `maku_factory_budget_cap_usd` | gauge | `principal` | The principal's daily cap in USD |
| `maku_factory_budget_remaining_usd` | gauge | `principal` | USD the principal has left today, 0 once its day is spent |
| `maku_factory_budget_spent_usd` | gauge | `principal` | USD the principal spent today, unclamped: how far past its cap a day ran. Each principal of yesterday reads 0 from the day's first settled tick |
| `maku_factory_human_interventions_total` | counter | `kind`, `cohort`, `profile`, `recommended`, `repository` | A human stepping in: `stop`, `retry`, `request_changes`, `steer`, `takeover`, `approve`, `split`; or the factory needing one: `needs_author` (the scoper bounced to the author), `human_commit` (once, when the pull request's first human commit is read), `escalated` (each entry into Escalated, a bounce apart). `cohort` and `profile` are the task's (`labeled`, `control`, `treatment`), `none` without a profile; `recommended` is the scoper's recommendation as `task_outcomes_total` labels it |
| `maku_factory_merge_decisions_total` | counter | `class`, `verdict`, `reason`, `repository` | The merge gate's decisions: `arm`, `shadow`, `human`, `escalate`, `ci_red`, `ci_cancelled`; a wait is none. Each also leaves a [decision record](../factory.md#the-merge-gate) in its comment |
| `maku_factory_class_mismatch_total` | counter | `predicted`, `matched` | The predicted class differs from the one policy-bot matched |
| `maku_factory_tier_fit_total` | counter | `classifier`, `tier`, `fit`, `control` | A finished task's tier fit, per classifier |
| `maku_factory_intake_errors_total` | counter | `source`, `repository` | Failed intake polls or requests: `issue`, `runlore`, `schedule`, `api` |
| `maku_factory_label_events_truncated_total` | counter | `label` | Label polls that met the forge's event cap and left the label for the next poll |
| `maku_factory_config_wire_errors_total` | counter | `repository` | Repositories a config publish listed that the factory could not wire (a GitHub App not installed, a merger key missing for a live or shadow class): not served until the minute's retry or a later publish wires them. The same at start: the factory logs `a repository is not wired yet` and serves the others |
| `maku_factory_run_revocations_total` | counter | `reason`, `limit`, `principal` | Runs the factory revoked; `limit` when the run meter did it (below); `principal` the run's payer, or the key over for `budget-principal-shadow` |
| `maku_factory_run_overshoot_tokens_total` | counter | — | Tokens runs spent past their cap before the meter revoked them: a tick's lag, or a blind interval's spend after an outage |
| `maku_factory_run_cost_usd` | histogram | `tier`, `role`, `cohort`, `profile` | A priced run's USD cost, reported two minutes after it ended (below); `role` the AgentRun's (`implementer`, `reviewer`, `tester`, `triager`), or `scoper` for a triager run that scoped a task; any other role is `other`. `cohort` and `profile` are the task's when the run started, as its claim carries them, `none` without a profile |
| `maku_factory_run_spend_usd` | gauge | `run`, `model` | A priced run's USD spend so far, until its cost is reported |
| `maku_factory_run_settled_usd` | gauge | `run` | A run's USD as the day ledgers settled it: the sum of its `settled.<runId>` entries |
| `maku_factory_model_cost_usd_total` | counter | `model` | USD the runs spent, by served model, added as each run's cost is reported |
| `maku_factory_charged_usd_total` | counter | `principal`, `kind` | USD the day ledgers charged, by the run's payer and `kind` (`task`, `room`), added when the write that settles it succeeds ([spend per identity](../operations/factory.md#spend-per-identity)) |
| `maku_factory_run_charge_info` | gauge | `run`, `principal`, `task`, `room` | 1 for each run not ended, and each ended one for two minutes after its `finishedAt`; leader only. `task` is absent for a room's run |
| `maku_factory_human_wait_seconds_total` | counter | `principal`, `phase`, `reason`, `class` | Seconds tasks waited in `AwaitingHuman` or `Escalated`, added as each leaves the phase, under the task's payer, by the hand-off's reason and the task's class. `reason` is one of the factory's closed codes (every reason narrate has prose for, the gate's refusals such as `class_paused` and `patch_too_large` included, escalation codes, meter revocations, run ends), `other` for free text such as a hand-written `revoked` annotation, and empty when the phase has none |
| `maku_factory_handoff_to_merge_seconds` | histogram | `class`, `reason`, `how` | From a pull request's first hand-off (`status.pullRequest.handedOffAt`) to its merge, as `_bucket`, `_sum` and `_count`; buckets 5 min, 15 min, 1 h, 4 h, 1 d, 3 d, 7 d. `how` is `approved` (the merger, on a maintainer's approval) or `human`; `reason` is as for `maku_factory_human_wait_seconds_total`. A task already in `AwaitingHuman` at upgrade has no start, so records nothing; a hand-off into `Escalated` does not start it |
| `maku_factory_run_budget_usd` | gauge | `tier` | Each tier's USD run cap; absent for a tier capped in tokens |
| `maku_factory_run_budget_fallbacks_total` | counter | `scope`, `why` | Runs and tasks of a USD-capped tier held to their token cap (below) |
| `maku_factory_meter_unpriced_model_runs_total` | counter | `model` | Runs a model served with no price (below) |
| `maku_factory_meter_input_excludes_cache_runs_total` | counter | `model` | Runs whose `input` a model reported without its cache tokens (below) |
| `maku_factory_meter_usage_read_errors_total` | counter | — | Meter ticks whose usage read failed: no cap was checked on them |
| `maku_factory_meter_last_success_timestamp_seconds` | gauge | — | When the meter last read usage and settled it in the day's ledger; set by the leader only, so alert on its max across pods. A tick whose ledger write fails sets nothing: it still revokes runs at their own caps, but the daily budgets wait and the increase is retried whole on the next tick |
| `maku_factory_model_refused_total` | counter | `cause`, `role` | Runs the model provider refused (`billing`, `auth`, `quota`), once per refusal: the task is held and retried, not escalated. Alert on its increase ([model provider refusals](../factory.md#model-provider-refusals)) |
| `maku_factory_resumes_total` | counter | `reason` | Runs resumed after their pod was `Disrupted` or `PodLost` |
| `maku_factory_run_token_mints_total` | counter | `role`, `result` | Installation tokens the per-run minter asked, by role and result: `ok`, `not_installed`, `permission_not_granted` (a 422: the App's installation lacks a permission the [role table](config.md#run-tokens-the-role-table) asks, or the repository), `scope_mismatch` (GitHub granted a wider or different scope, or another repository; the minter revokes that token, counted below) or `error`. A mint's 404 from a reinstalled App is retried once with a fresh lookup. Mints run under `runTokens.perRun` |
| `maku_factory_run_token_refusals_total` | counter | `reason` | Runs the per-run minter refused a token, once per run: `unbound_task` (no live Task names it), `unbound_api` (an API run with no ledger reservation naming its repository and role, or no request hash), `repository_mismatch` (its Task or reservation names another repository), `role_unknown` or `role_mismatch` (its reservation names another role). The run waits Pending `SecretMissing` |
| `maku_factory_run_token_revokes_total` | counter | `result` | Installation tokens the per-run minter revoked, `ok` or `error`, the revoke of a `scope_mismatch` token included. An `error` leaves the token live until it expires, within the hour |
| `maku_factory_loop_runs_total` | counter | `class`, `end`, `repository` | Implementer runs under the loop policy as they end: `judge_impossible`, `stalled`, `not_met`, `finished`, `other`. The wandering share is `stalled` over all |
| `maku_factory_contracts_total` | counter | `class`, `end`, `repository` | Tester runs as their end is read: `ready`, `not_testable`, `contract_not_red`, `contract_outside_tests`, `contract_invalid`, `unreported` |
| `maku_factory_class_without_holdout` | gauge | `repository`, `class` | 1 for a live class that declares no `holdoutChecks`, set at start and at each config publish, which sets it back to 0 for a class that gained them, left live or was removed; the config is refused from the next minor |
| `maku_factory_class_paused` | gauge | `repository`, `class` | 1 while the breaker holds the class paused, 0 once it reads clear; recorded at each gate decision and each monitor pass of the class's merges, so a class with neither keeps its last value: a class without a monitor that recovers by maturity alone reads 1 until its next decision. Recorded for shadow and unarmed classes too, where a pause is inert |
| `maku_factory_steering_reads_total` | counter | `class`, `read`, `repository` | Steering notes read at triage, in a mode other than `off`: `used`, `absent`, `invalid`, or `unowned` (the repository's fence does not name this factory, or its cache is stale: not read) |
| `maku_factory_scoper_runs_total` | counter | `class`, `outcome`, `repository` | Scoper runs as they end: `ready`, `question`, `assumed`, `missing`, `failed`, `budget` (the run cap ended it before any handoff; its `status.scope` is `failed`), `budget_calls` (the harness stopped it at `scoper.maxToolCalls` tool calls before any handoff; its `status.scope` is `failed`). A scoper that ends without a handoff says so in the room |
| `maku_factory_overlap_evaluations_total` | counter | `classification`, `source` | Persisted final [outcome-overlap evaluations](../factory.md#outcome-overlap-detection), once per checked source: six classifications and `tasks`, `runs`, `rooms`, `github`. Pending and replays add nothing. One evaluation with several sources increments several series, so do not sum these labels to count Tasks |
| `maku_factory_overlap_scoper_runs_total` | counter | `outcome` | Deterministic candidate-evaluation scoper runs by final classification; a failed, missing, malformed or inconclusive assessment counts `unverified_overlap`. Exact-active and empty-candidate evaluations create no scoper and no observation; exact non-active matches can be evaluated by the scoper |
| `maku_factory_overlap_evaluation_seconds` | histogram | — | Initial candidate retrieval/evaluation time, recorded after its status persists. It does not include the subsequent candidate-scoper run; replays and failed status writes record nothing |
| `maku_factory_profile_recommendations_total` | counter | `class`, `cohort`, `recommended`, `applied` | Resolved tasks offered workflows: the scoper's recommendation (`none` without one, `other` for a name not offered) and whether it was applied (`true` only for the treatment cohort under `evaluation.mode: apply`) |
| `maku_factory_task_outcomes_total` | counter | `class`, `cohort`, `profile`, `source`, `recommended`, `outcome`, `repository` | Ended tasks with a workflow, once each, when their usage has settled: 2× the meter poll period + 30s after the first end (`status.endedAt`), on a requeue. The outcome is the first end's (`status.outcome`): a `Reverted` before the settle neither changes it nor restarts the window. `accepted` (merged, every commit of the pull request bound to a push of the factory's; a commit that cannot be proven the factory's reads as a human's), `merged_human`, `no_op`, `closed`, `stopped`, `rejected`, `reverted`, `failed`. The merged pull request's commits and branch record are read for this alone: a task with no profile and no `status.evaluated` mark (a part's triage writes it when the parent has a profile; the merge never reads the parent) completes as before the evaluation and reads neither. `recommended` is the scoper's recommendation as `profile_recommendations_total` labels it (an offered name, `none`, `other`), so a control task compares with the treatment tasks recommended the same profile. An approved split's family counts once, on the parent, under the parent's triage profile, over every part of the split, opened or not: `accepted` when every part merged with no human commit, `merged_human` when any part had one, else the first other outcome by part index; a part never opened, or not ended when the parent counts, reads as the parent's own outcome, a part the parent recorded reverted as `reverted`. The parent counts once every part's usage has settled too, waiting on the task poll past its own window and no longer than twice the window from its end, after which it counts what it has (a part deleted, or never ended). A read the count needs that fails (the family's) leaves the task unsettled and uncounted; the next pass retries. The parts carry no profile and count nothing of their own |
| `maku_factory_task_usd_total` | counter | `class`, `cohort`, `profile`, `source`, `recommended`, `outcome` | USD of those tasks, every attempt summed, counted with the outcome once the usage has settled, so a cost the meter prices after the end is in it; an approved split's family sums the parent's and every part's, every generation, each part's once settled (or as it stands at the bound). A task that never ran counts at its end: it has nothing to settle |
| `maku_factory_scope_bounces_total` | counter | `class`, `exit`, `repository` | Bounced tasks leaving their wait: `relabel`, `retry`, `expired` |
| `maku_factory_splits_total` | counter | `class`, `outcome`, `repository` | Split handoffs, once each: `approved`, `declined`, `superseded`, `expired`, `ignored`, and `unstated` (offered a split, the scoper's handoff summary describes one but its `room_handoff` carries no `split`: the task runs whole, and a room note says so) |
| `maku_factory_split_children_total` | counter | `class`, `outcome`, `repository` | Parts of a split, once each when their parent ends: `landed`, `dropped`, `reverted` |
| `maku_factory_rooms_closed_at_cap_total` | counter | `reason` | Rooms closed at the cap (below): `human_driver`, `facts_due`, `drill_unscored` (a drill task the drill runner never scored) |
| `maku_factory_github_up` | gauge | — | 1 while any GitHub call succeeded in the last 3 minutes, per replica |
| `maku_factory_github_rate_remaining` | gauge | — | The factory App's remaining REST rate limit |
| `maku_factory_trace_export_abandoned_total` | counter | — | Task spans given up after the collector refused them for a day |
| `maku_factory_repository_owned` | gauge | `repository` | 1 while the repository's `.github/agent-factory.yaml` names this factory (`factory` in the config), per replica; 0 means no intake, schedules, API runs or merges there. A repository a config publish removes keeps its last value until the pod restarts |
| `maku_factory_policy_load_errors_total` | counter | `repository`, `reason` | Reads of a repository's merge policy (`mergePolicies` in its `.github/agent-factory.yaml`) that failed: `invalid`, counted at every read, so a rate shows it is still bad (a binding over a guardrail is refused alone, `GuardrailMismatch`, never the file); and, for a cut-over repository only, `unreadable` (no read yet) or `stale` (older than the fence's 15-minute window), counted at each gate pass that pauses on it |
| `maku_factory_policy_central_divergence` | gauge | `repository` | Classes on which the repository's merge policy differs from the central config, as last read; 0 is equivalent. A repository never compared has no series; after a failed read or a removed policy the gauge keeps its last compared value until the process restarts, so stage 1's exit reads it together with `maku_factory_policy_load_errors_total`: divergence 0 and no load errors |
| `maku_factory_policy_changes_total` | counter | `repository`, `class`, `change` | A cut-over repository's (`classAuthority: repository`) policy file changed a class: `tightening`, or `relaxation`, which runs a live class in shadow until the factory restarts. A relaxation also logs `repository merge policy relaxed` with why |
| `maku_factory_policy_dual_decisions_total` | counter | `repository`, `class`, `agreement` | With `policyManagement.dualEvaluation`, each merge decision the gate acts on, decided again under the repository's policy: `agree`; `explained`, a difference on a class `maku_factory_policy_central_divergence` already counts; `unread`, the repository's decision needed the diff the central one did not read; `unexplained`; `no_policy`. Stage 3 exits after a week with no `unexplained`. Nothing is decided on it; the decision record carries `policy=<hash> dual=<agreement>` |
| `maku_factory_approval_grants_active` | gauge | `repository`, `class` | With `policyManagement.approvalMemory`, the active saved approval grants of the class, as the last lookup before a decision found them. Grant ids live in the decision record (`grant=`) and the broker's audit, never in a label |
| `maku_factory_approval_grant_events_total` | counter | `repository`, `class`, `event` | Grant lifecycle the factory caused or a lookup recorded: `used` (a merge on it), `disabled` (its merge reverted), `suspended` (made under another policy), `expired`; `created`, `reconfirmed`, `revoked` and `renewed` are the humans' and live in the broker's `approval_grant_events` |
| `maku_factory_admission_refusals_total` | counter | `repository`, `reason`, `principal` | Run requests refused on their repository: `repository_differs_from_the_room`, `repository_differs_from_the_branch`, `repository_permission`, `repository_not_owned`; and `person_budget_exhausted`, an API run or a label refused on its person's day ([per-person caps](../factory.md#per-person-caps)) |
| `maku_revision_deferred_total` | counter | — | First runs that gave up a slot-time to a revision while `caps.preferRevisions` is on (CD22); the row's reason is `waiting_revision_first` ([when the factory waits for you](../factory.md#when-the-factory-waits-for-you)). The design's own family name, so it is served without the `maku_factory_` prefix |

A label value outside its known set is recorded as `other`; `repository` takes only the configured repositories' names.


## Coordination metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `maku_factory_coordination_families_total` | counter | `repository`, `outcome` | Families by primary: `filed` once, when the last issue and the parent's `filed` write land; `done` once, when the final integration completed the parent. A parent has no closed or stopped end: `factory/stop` pauses it |
| `maku_factory_coordination_plan_versions_total` | counter | `repository`, `outcome` | Plan versions by primary: `confirmed` by the confirmation that completed them, `superseded` when a newer version was accepted, `stale_refused` for a confirmation of a hash that is no longer current |
| `maku_factory_coordination_admission_refusals_total` | counter | `repository`, `reason` | Refused filings and integration admissions (on the primary) and child admissions or resumes (on the child's repository), once per change of reason, never per pass; `reason` is one of the [reason codes](../factory.md#reason-codes) |
| `maku_factory_coordination_children_total` | counter | `repository`, `class`, `outcome` | Children in their own repository, by plan class: `admitted` (Task created), `landed` (merged, in landing order), `dropped` (removed by an accepted version), `reverted` |
| `maku_factory_coordination_integration_runs_total` | counter | `repository`, `purpose`, `result` | Integration attempts on the primary, at their end: `passed`, `checks_failed` (a typed failure or no result); and `infrastructure_failed` once when its run was lost and the Task escalated (an attempt resumed after that is counted again at its end) |
| `maku_factory_coordination_wait_seconds` | histogram | `repository`, `reason` | Ended waits: `authorization` (the version's acceptance to its last confirmation), `artifact` (a dependent's earliest producer admitted to the dependent admitted), `integration` (an integration Task created to its result), `recovery` (a proposal made to replaced or cleared); buckets 1 min, 5 min, 15 min, 1 h, 4 h, 1 d, 3 d, 7 d |
| `maku_factory_coordination_allocated_micro_usd` | gauge | `repository`, `kind` | Micro-USD the live families whose current plan the parent confirmed allocate, summed by primary: `planning`, `child`, `integration` (`maxRuns × runMicroUSD`); leader only, read from the cache at scrape time |
| `maku_factory_coordination_recovery_age_seconds` | gauge | `repository` | Seconds the oldest recovery proposal standing on a live parent has stood, by primary; no series while none stands. A proposal stands until it is replaced or cleared. Leader only, read from the cache at scrape time; `AgentFactoryCoordinationRecoveryRequired` fires on it past 15 minutes |

`repository` takes only configured repositories, every other label a closed set; anything else is
`other`. Family and child ids, artifact names, plan and acceptance text, viewers and channels are
never label values. Per-run cost and per-person charge stay on the existing series.

A parent mints a root span when it first reconciles and exports it at its end, as every task does.
It adds `agent.coordination.plan_version` and `agent.coordination.plan_hash_prefix` (the first 12
hex of the plan hash) and links to each child's and integration Task's root span; no plan text,
acceptance or artifact bytes. The parent's reason is the span's `agent.task.reason`.


## Hardening metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `maku_factory_hardening_output_seconds` | histogram | `repository`, `type` | From a run's `output_requested` to the factory's answer (AC7: p95 at most 2 s per non-push output) |
| `maku_factory_hardening_untraced_total` | counter | `repository`, `type` | Agents-App writes of an enforced type no `output_applied` explains. The H2 gate holds it at 0 |
| `maku_factory_hardening_sandbox_permission` | gauge | `repository`, `role`, `permission`, `access` | 1 for each permission a sandbox role or the publisher (`role="publisher"`) gets, as the last fence pass read it, from the [role table](config.md#run-tokens-the-role-table): the sandbox roles' under `runTokens.perRun`, the publisher's under `hardening.publisher.minter`. A pair a later pass no longer reads is 0 |
| `maku_factory_hardening_approval_wait_seconds` | histogram | `repository`, `decision` | How long a protected-path push waited for its approval, by how it closed: `approved`, `denied`, `expired`, `superseded` |
| `maku_factory_hardening_drill_last` | gauge | `result` | Unix time of the last injection drill the factory scored, `pass` or `fail`. A `fail` newer than the last `pass` leaves `drill_stale_or_failed` missing until a fix and a passing drill |
| `maku_factory_hardening_t5_total` | counter | `repository`, `type`, `result`, `drill` | T5 classifier calls on outputs, and on a target or T4 refusal whose text T3 cleared: `hit`, `pass`, `unavailable`. T5's false positives are `hit` on applied outputs with `drill="false"`; its detection on the drills is `hit` with `drill="true"` |
| `maku_factory_hardening_ready` | gauge | `repository` | 1 when the repository's hardening readiness (all seven checks) held at the last five-minute report, else 0. Reported only: execute's admission computes its own |
| `maku_factory_hardening_missing` | gauge | `repository`, `missing` | 1 for each readiness fact that did not hold at the last report (`mode_not_enforce`, `sandbox_write_token`, `publisher_policy`, `layer_not_enforced`, `read_filter_off`, `drill_stale_or_failed`, `demo_profile`, or `error` when a fact could not be read), else 0 |


## Spend and principal metrics

| Series | What it answers |
|---|---|
| `maku_factory_charged_usd_total{principal, kind}` | What the ledgers charged for the work a principal caused. Per person key, a day's increase is that day's `spent`; summed over payers, the fleet's `spent` with per-person caps on. A failover lift reaches a run's `settled.<runId>`, never `spent`, and is never counted. A leader that exits loses the charges of its last scrape interval from its counter, never from the ledger, nor does it count a landed write still in doubt when leadership changes: the counter can read low, never high (it never counts twice). Read it with `increase_pure` on VictoriaMetrics: a payer's series is born at its first charge |
| `maku_factory_run_charge_info{run, principal, task, room}` | Which runs, tasks and rooms a principal paid for: join a run's gateway series (`ar_agent` ends in `<runs.namePrefix><run>`, `run-<run>` by default) or kube-state-metrics' `agentrun_*{run_id}` on it |
| `principal` on outcomes, openings, refusals, revocations, waits and `maku_factory_tasks` | The payer the series is recorded under. An API refusal names the caller as authenticated (`human:<sub>`), and a refusal on a person's day that person. The shadow keeps the key over |

`principal` takes a ledger key's shape (`system:<name>`, `github:<login>`, `human:<sub>`), else
`other`. `run`, `task` and `room` appear only on the per-live-run series.


| Series | What it answers |
|---|---|
| `maku_factory_api_intake_total{principal, outcome}` | `POST /v1/tasks` requests by outcome: `admitted`, `duplicate`, or a refusal's code ([API](api.md#post-v1tasks)); `other` for anything else |
| `maku_factory_max_cost_usd_total{principal}` | The `max_cost` the intake admitted: what principals asked to be allowed, not what they spent (`maku_factory_charged_usd_total{principal="system:<name>"}`) |
| `maku_factory_principal_sponsor_info{principal, sponsor}` | 1 per declared principal, naming the maintainer accountable for it |

