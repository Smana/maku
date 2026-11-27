<!-- page-type: reference -->
# Narration and task status

## Contents

- [Notification classes](#notification-classes)
- [Event comments, status comment and outbox](#event-comments-status-comment-and-outbox)
- [Workflow pin refusals](#workflow-pin-refusals)
- [When the factory waits for you](#when-the-factory-waits-for-you)

## Notification classes

Every narration event carries a `Class` (`needs_you`, `changed`, or `done`) that governs whether and how it mentions a person:

| Class | Mention | Shape |
|---|---|---|
| `needs_you` | Yes, the one person who can act | What is asked, one recommended action (a button, reply, or command), and what happens if nothing is done (for example, "it stays parked" or, in Slack, "it stops in 7 days" from the factory's `caps.escalatedHours`) |
| `changed` | No | A state transition with no mention (run start, workflow card, step-up, pull request opened, CI fix, resume, monitor verdict) |
| `done` | Yes, once | The terminal end line (`Done`, `NoOp`, `Rejected`, `Closed`, `Stopped`, `Reverted`, or merged) |

Escalated endings (`PhaseEscalated`, including `needs_author` and `split_proposed`) carry `needs_you` rather than `done`, because the task waits for a person to act before its escalation expires.

## Event comments, status comment and outbox

The factory App comments on the task's issue, or on its pull request when it has none: at triage, before the first run, `task-card` states the workflow, why it was chosen, what will run, its dollar ceiling and how a maintainer can pin another workflow (`workflow:<name>`); each run's start names the persona (for example, `scoper`, `implementer`, `reviewer`), run id, branch, dollar budget (or tokens when unpriced), room link and stop label. The task's first implementer run's start also says what the sandbox did not run, and that CI is the judge of a merge: the default branch's `.github/agent-checks.yaml` `ciOnly` entries (at most 10, quoted as code), that the repository declares no agent checks, or that the run does not run them. Then the pull request, CI fix and resume, each hand-off with its reason and the merge command, and the end with the tokens used. When intake removes images from an issue and the repository does not turn on screenshots, `images-removed` says how many, and asks the author to describe them in words. `profile_chosen` (`profile-chosen-<runs>`) is posted only when the workflow changes after the card (for example, when the scoper picks another workflow under `evaluation.mode: apply`), so a default task gets one workflow comment, not two. On a task with no issue, `profile_chosen` and `profile_escalated` wait for the pull request, and are dropped if the task ends without one.

A workflow with `stepUp` (or legacy `escalation`) steps a task up once before a maintainer is asked, narrates `profile_escalated` (`profile-escalated-<runs>`) with what changed, why, the new task ceiling (and settled spend so far) and how to stop it, and queues a run with trigger `escalation` and the review rounds reset. When automatic step-up is held back (pinned workflow, capped below the target ceiling, `workflow.stepUp: ask`, or a confirmed plan that did not show the stepped-up figure), the factory narrates `step-up-offered` naming why and offering `/factory retry deeper` (when the target workflow's ceiling, at the tier its `budget:` names, is more than the cost confirmed for the task, the offer names both figures and says to start the task again with that workflow, because a retry would be refused with `deeper_over_cap`):

| Escalated reason | Legacy group | Step to |
|---|---|---|
| `budget-run`, `stalled`, `budget-task` | `out_of_budget` | `stepUp` (or `escalation.outOfBudget` for `budget-run`, `stalled`) |
| `not_met`, `review_rounds_exhausted` | `not_converged` | `stepUp` (or `escalation.notConverged`) |
| `ci_red` | — | `stepUp` |
| `judge_impossible`, `needs_author`, `split_proposed`, `manual` | — | the maintainer |

`budget-run`, `stalled` and `not_met` step only when the implementer's run ended so (the in-run judge's `not_met` included); a tester's or reviewer's own end goes to the maintainer. `review_rounds_exhausted`, the review loop's verdict on the implementer's work after a verifier's run, steps too. There is no step for a task with an unresolved workflow, a control-cohort task, a group the workflow's `escalation` does not map, a task that has already stepped (`/factory retry` keeps it stepped), or a target whose tier leaves less than one run in the task cap (a sponsor's ceiling still caps it), or a target that is the workflow the task already runs, pinned or scoper-applied alike: the same workflow again rarely converges, so a maintainer is asked straight away. The step reads the workflow's `escalation` alone, not `evaluation.mode`: a workflow that declares it steps under `observe` too. A step raises the tier, never lowers it; it never turns on `testFirst`, since the tester branches from main. The stepped run gets the revision brief with the completion judge's last verdict fenced in it, on the same branch and pull request. The step renews the task's usual limits once: the review rounds and the CI fix runs count from it (`status.fixRunsBeforeStep`), so a stepped task gets `merge.fixRuns` repairs of its own. The escalated task's end comment names the workflow already tried.

An issue task also has one live **Factory status** comment, created by the reconciler with the first event narration, never by the intake poller; a refused intake label creates no Task and no status comment. Later material transitions edit that same comment, while an unchanged reconcile makes no GitHub status calls. It shows the phase and its last transition time, room and waiting guidance, spec hash, Decision and Assumption counts, and pull request. Live checks, token usage and spend are deliberately omitted. Its ID and last successful body are persisted in Task status; recovery reads all issue comments, a newer generation adopts it, and an older generation cannot edit it. Transient creation or edit failures retry without rolling back the task transition or its event comments. A permanent GitHub refusal or an ownership stand-down marks the status inactive instead: that Task makes no further status calls, without inventing a successful ID or body, and terminal room close can proceed.

A maintainer's **Request changes** is answered where it was made, on the pull request:

| Event (key) | When | Says |
|---|---|---|
| Revising (`revise-<runs>`) | The review sends the task back for a revision | The next run starts on the same branch with the review in its brief |
| Revised (`revised-<runs>`) | That revision run ends | The new head (short sha, linked) and the files GitHub says changed since the head it started on, the room link, and that the pull request is ready for re-review; or that the revision ended without a new commit. If it took a path the task names (a `## Files` line, or a path in a code span of its acceptance check) out of the pull request, a **Scope changed** line names it and the task waits as `scope_changed` |

Neither quotes the review or the agent's handoff.

Input that arrives while it cannot apply, or from someone the factory takes no input from, is answered rather than dropped ([Commands posted early](../factory.md#commands-posted-early-and-input-that-is-not-acted-on)):

| Event (key) | Class | When |
|---|---|---|
| Command held (`held-<comment>`) | `changed` | A maintainer's command was posted before the phase it applies in; names that phase |
| Plan command noted (`held-<comment>`) | `changed` | A plan command was posted while no plan was under review; the next plan's comment names it |
| Held command applied (`held-applied-<comment>`) | `changed` | The task reached the phase and the command was acted on |
| Held command expired (`held-expired-<comment>`) | `needs_you` | It was not applied: the task moved on without it, ended, or 24 hours passed. Mentions its author |
| Command not applicable (`not-applicable-<comment>`) | `changed` | A maintainer's command this task can never act on, and why |
| Comment review (`comment-review`) | `changed` | A maintainer's newest review since the last run is a **Comment** with text or inline comments: it starts nothing, and the reply names **Request changes** and **Approve**. Once per task |
| Not acted on (`not-acted-<comment or review>`) | `changed` | A command or **Request changes** review from someone without the standing to steer the factory; at most one per person per task per hour, on the issue or pull request it was posted on |

None quotes the comment or the review. Each comment carries a hidden marker and is posted at least once and never twice, from an outbox written with the transition that caused it. The outbox holds 16 comments: past that the oldest are dropped, an ending's last, and logged `narration_dropped`. A comment GitHub refuses for good (its issue deleted, issues turned off, or the issue locked) is dropped and logged the same way, and the comments after it still post. Comments hold the factory's own words and ids, never text a user wrote.

The task's room gets the issue snapshot as its first `task_state` message, then the task's facts (phase, reason, current run, the run history with each run's outcome, end and dollars, token budget, dollar spend, issue and pull request), which the room page and `maku status` show. The history keeps the last 20 runs, oldest first. A run's outcome is `succeeded`, `failed`, `budget_exhausted`, `revoked`, `lost` (a disruption, a lost pod or a vanished claim) or `ended` (the task ended or escalated with the run unfinished); it is empty while the run goes on. A run's dollars and the task's spend are exact in the facts posted, but a tick of a running run never posts: the spend steps by 10% of its cap, or $0.10 without one. A run the meter could not price reads $0, so a task mixing priced and unpriced runs shows a partial dollar figure while its token cap binds: a known limit.

## Workflow pin refusals

When a maintainer's workflow pin ([Pinning a workflow](../factory.md#pinning-a-workflow)) is refused at intake or triage, the task runs as if unpinned and the factory narrates one `profile_refused` line with the API code recorded in `status.profile.refused` (alongside `status.profile.requested` in the `profile` task fact and `summary/v1`). Human-facing surfaces display the `workflow_*` spelling (`workflow_unknown`, `workflow_conflict`, `workflow_no_tests_paths`, `workflow_not_offered`) while the API, `status.profile.refused`, and metrics keep emitting `profile_*`:

| Code | Where | Sentence |
|---|---|---|
| `profile_unknown` | intake, triage | `<p>` is not a workflow of this factory. The repository's [catalogue](api.md#get-v1catalogueownername) lists them. |
| `profile_not_offered` | intake | `<p>` names no roles, so they are chosen at triage; it runs only as the factory's choice. |
| `not_maintainer` | intake, triage | Only a maintainer of this repository can choose the workflow; the factory will choose it. |
| `profile_conflict` | triage | The issue names more than one workflow; none was applied. |
| `profile_no_tests_paths` | triage | `<p>` writes tests first, and this repository declares no tests paths. |
| `catalogue_unavailable` | intake | The factory's list of classes could not be read; try again in a minute. |

| Task | Cohort | `source` | Scoper consulted? |
|---|---|---|---|
| pinned, honoured | `labeled` | `label` | no |
| pinned, refused | by hash (`control` / `treatment`) | its source, `control`, `treatment`, `repository` or `scoper` | as for an unpinned task |
| no pin, the repository's default | by hash | `control` / `treatment` / `repository` / `scoper` | yes |
| no pin, a schedule's, a revert, an oracle finding | by hash | `schedule` / `revert` / `oracle` | recorded, never applied |

`maku_factory_profile_pins_total{class,profile,outcome,reason}` counts the pins triage saw (`honoured` or `refused`, `reason` the code or `none`; a workflow no longer in config is `other`). `maku_factory_profile_agreement_total{class,outcome}` counts each resolved task offered a choice by how the planner's suggestion (`spec.intake.suggested`) and the scoper's recommendation compare: `agree`, `differ`, `no_suggestion` (the issue path carries none) or `no_recommendation`. The suggestion is read by that count alone: no brief carries it, so the scoper never echoes the planner.

## When the factory waits for you

The room page and `maku status` show the reason and the next step below; the factory's issue comments use the same words.

**Queued:** the next run waits. Next step, unless the row says otherwise: nothing to do, the run starts once the room and the factory are free.

| Reason | Why |
|---|---|
| `waiting_run_slot` | Every one of `caps.concurrentRuns` factory runs is live |
| `waiting_revision_first` | `caps.preferRevisions` is on, and another task waits for a slot to run a revision (a run that continues it): the first run lets the revision go first. Caps still bind and nothing is reserved, so it waits one slot-time instead; `maku_revision_deferred_total` counts it |
| `waiting_active_tasks` | `caps.activeTasks` tasks are active |
| `waiting_review_wip` | `caps.awaitingHumanWIP` tasks already wait on a maintainer; only a task's first run of a class that is not live waits for this |
| `waiting_daily_budget` | `budgets.factoryDailyUSD` is spent. It resumes after 00:00 UTC |
| `waiting_meter_blind` | The meter cannot read usage, so the daily cap cannot be checked |
| `waiting_room_log` | The room's log is not open on the broker yet |
| `waiting_broker_permission` | The broker does not let the factory write to rooms yet |
| `waiting_human_driver` | A person drives the room. Next step: hand the driver back, or let the lease lapse |
| `waiting_room_busy` | Another run in the room is still going. Next step: end it, or wait for it |
| `rereview_verdict_stale` | The head moved after the approval, so a new review run is queued |

**Implementing:** a tester run ended and GitHub has not recorded its push yet: nothing to do.

| Reason | Why |
|---|---|
| `contract_push_unrecorded` | The tester's harness said the contract was ready, and GitHub's activity does not show the push yet; the task waits up to five minutes, then goes on without a contract |

**Escalated** by the tester stage: a maintainer reads the room, then decides whether to retry (`/factory retry`) or close it.

| Reason | Why |
|---|---|
| `contract_invalid` | The tester's branch was not the tester's alone (another login, a second push, a history too long to read) or its contract commit was malformed (trailers naming another run, no file or id, over 50 files or ids) |
| `contract_too_large` | The contract names more than 50 files or test ids, or a path or id over 512 bytes |
| `contract_disputed` | The implementer handed off to the `tester` role: it says the tests are wrong |

**AwaitingCI:** the merge gate reads the pull request. Next step: nothing to do.

| Reason | Why |
|---|---|
| `ci_pending` | A required check (`merge.requiredChecks`) has not finished |
| `ci_rerun` | A cancelled required check was re-run (at most `merge.checkReruns` per head) |
| `reentered_policy` | Back from a `policy_pending` or `policy_absent` hand-off: policy-bot now passes the head it was handed off at |
| `reentered_head_reported` | Back from a `head_unreported` hand-off: the room now reports the head as a commit of the task's run |
| `reentered_ci_green` | Back from a `ci_cancelled` hand-off, or a `ci_red` escalation: the required checks are now green on the head |
| `reentered_approved` | Back from a hand-off a human's judgement covers: a maintainer approved the head it was handed off at (`merge.approveToMerge`), or, for an escalated task (`budget-task`, `ci_red`, ...), the pull request's current head. Once per review, not counted against the returns limit. Any other escalation on a green head replaces a stale `maku/merge-gate` `ci_red` with `a human decides, <reason>` |
| `reentered_day_turned` | Back from an `auto_merge_cap` hand-off: the UTC day turned, so the cap is counted again. With `merge.approveToMerge` only; counted against the returns limit |
| `reentered_unpaused` | Back from a `class_paused` hand-off: a clean ledger lifted the pause, so the gate decides it again. With `merge.approveToMerge` only; counted against the returns limit |
| `head_moved` | A commit landed after the decision; the new head is decided again |
| `contract_unread` | The factory has not yet read which files changed since the test contract (row 2c) |

**AwaitingHuman:** the pull request is a maintainer's. Next step, unless the row says otherwise: a maintainer reviews and merges it. The repository's ruleset blocks a plain merge, so a maintainer with admin ticks "bypass rules" on the pull request, or runs `gh pr merge <n> --squash --admin --repo <owner/name>`; the comment names the exact command. A **Request changes** review sends the task back for a revision instead.

With `merge.approveToMerge`, a maintainer's approval of the current head is the merge: the merger merges it, outside the caps, as a human merge (`factory/approved-merge`, no automatic revert), and the comment says "To merge: approve this pull request" instead of naming the bypass. The reasons a human fixes on the pull request itself (`not_agent_authored`, `gate_path`, `contract_modified`, `not_owned`, `steering_path`, `foreign_trailer`, `not_mergeable`, `changes_requested`, `human_commit_in_pr`) keep the bypass instructions: no approval returns them to the gate. A `changes_requested` hand-off returns on an approval once no maintainer's newest review requests changes.

With `merge.approveToMerge` off, an approval merges nothing and returns nothing to the gate, but the factory shows it on agents pull requests only (an adopted class keeps today's path). The task keeps its hand-off reason, whatever it is (`shadow_would_arm`, `policy_pending`, `not_mergeable`, `human_commit_in_pr`…), so every return and every reason's own next step stand; the room's NEXT line is prefixed with "approved by @login at commit `<sha>`", apart from the agent reviewer's "last verdict". Once commits land after the approval, it reads "@login's approval is of commit `<sha>`, an older commit: new commits since". An approval of the head submitted before the task's last pushing run ended (a force push moves a review onto the new head) reads "@login approved commit `<sha>` before the factory's last push ended: approve it again". Each approval is one issue comment, and one more once stale; the merge instructions go with a current approval only, and no comment repeats a `maintainer_approved` hand-off. A dismissed or vetoed approval is no longer shown. The task's `status.pullRequest.approval{by, commit, stale}` holds it (`stale`: `older_commit` or `before_runs_ended`, empty while current), and the room's facts carry it as `pr.approvedBy`, `pr.approvedCommit` and `pr.approvalStale`.

**Approve the commit you read.** GitHub binds an approval sent without a commit to whatever the head is when the approval arrives. If a run pushes while you read, `gh pr review --approve` approves a commit you never saw. Approve from the web UI, which sends the commit you are looking at, or name it:

```bash
gh api repos/<owner>/<name>/pulls/<n>/reviews -f commit_id=<sha> -f event=APPROVE
```

An approval counts only once the head has stopped moving. GitHub must have recorded the head's push (its server time, from the branch's activity) no later than the end of the task's last pushing run, and the approval must come more than a minute after that end. Otherwise the gate refuses it with `approval_stale`: the approval came too early, or the push could not be read. A force push moves an existing review onto the new head, so the review's commit alone proves nothing. Approve again once the runs have ended. A head pushed after the runs ended, by a human or by any other run, is not the task's: it hands off as `human_commit_in_pr`, and no approval returns it.

| Reason | Why |
|---|---|
| *(none)* | A revision a maintainer asked for is pushed |
| `scope_changed` | That revision changed a path the task names and the pull request no longer touches it: a maintainer confirms the scope change before merging, or requests changes again |
| `merge_gate_off` | No class is live or in shadow, so the factory does not gate the pull request |
| `policy_pending` | policy-bot's `policy-bot: main` status needs a maintainer's approval |
| `policy_absent` | policy-bot has not evaluated the pull request, or the status is not from `merge.policyBotLogin` |
| `maintainer_approved` | A maintainer approved it. With `merge.approveToMerge` off, a human merges it; on, an approval of the current head arms the merger, so this reason is left for adopted classes and for a task handed off before the switch was turned on: that approval predates the hand-off, so a fresh approval of the head returns the task to the gate |
| `approval_stale` | A maintainer's approval is of an older head, or came before the head stopped moving: before the task's last pushing run ended, or within a minute after. It is also refused when the head's push could not be read from the branch's activity. Next step: approve the current head |
| `gate_path` | It touches a gate path, so an agent can never merge it; a human re-authors the change |
| `steering_path` | Steering is on for some class and the pull request changes a [steering note](config.md#steering); a human merges it |
| `not_mergeable` | GitHub refused the merge as the pull request stands: a protection or approval it lacks, or a conflict with main once the CI fix runs that rebase it are spent |
| `not_agent_authored` | The pull request is not from `agentsLogin` |
| `no_policy_matched` | No policy of the repository holds on the diff (each one's refusal is in the record's `considered=`): a maintainer reviews and merges it. Not counted in `maku_factory_class_mismatch_total` |
| `reviewer_verdict_missing` | In `considered=`: a policy with no `diffShape` and no `oracle` holds only on a reviewer's approve, and the workflow has no reviewer |
| `class_unarmed` | Before the gate picked the policy from the diff: the predicted class was neither live nor shadow. Kept on older tasks only |
| `class_mismatch` | The predicted class's `diffShape` does not hold (`docs-links` diffs change only link targets) |
| `file_not_allowed` | It changes a file outside its class's allowed files |
| `file_protected` | It changes a file its class protects |
| `file_added_or_deleted` | Any change but a content modification: an add, delete, rename, copy or mode-only change, which its class does not allow |
| `patch_too_large` | It changes more files or lines than its class allows |
| `holdout_red` | A holdout check its class requires failed, was cancelled, neutral or skipped, or was not posted by GitHub Actions; also when the pull request has more than 100 checks, since some are unread |
| `holdout_pending` | A holdout check its class requires has not passed yet |
| `oracle_finding_open` | A finding of the task is still a link target in the head, was not one on the base, or the task has no findings |
| `oracle_unrequested` | The head moves a link target no finding asked to move, or moves one anywhere but the exact snapshot its finding gave |
| `no_approving_verdict` | The template has a reviewer and its verdict is not approve |
| `head_unreported` | No implementer run of the task reported the current head in the room (a `handoff` naming the commit). An approval of that head does not merge it: it returns to the gate once a run of the task reports it, and the approval then arms it. Meanwhile, a maintainer with admin merges it by bypass. The harness reports the pushed head itself when the agent finished without a handoff, or with one that names another commit |
| `verdict_stale` | The approval was for an older head. Next step: review the current head, or the factory asks for a new review |
| `foreign_trailer` | The head commit's `Agent-Run` trailer names another task's run |
| `human_commit_in_pr` | It holds a commit no run of the task pushed, so the factory neither fixes nor merges on top of it |
| `closes_parent` | A part's pull request body closes its parent issue; the parent closes with its last part, so edit the body and merge it yourself |
| `class_paused` | The class's breaker tripped: reverts, and with `maxFollowUps` set follow-up fixes and regressions, among its newest merges |
| `ledger_truncated` | A merge-ledger read filled its page of 100 and its oldest pull request is still inside the breaker window or today, so the ledger may under-count; nothing arms. A maintainer decides |
| `ci_wait_expired` | The pull request waited `merge.awaitingCIFor` in `AwaitingCI` and a required check never reported. A release-age status posted as pending waits longer, up to 7 days or the bound if that is larger. The message names the checks. A maintainer decides |
| `auto_merge_cap` | `merge.maxPerDay` is reached |
| `held` | The pull request carries `factory/hold`. Removing the label returns it to the gate |
| `not_owned` | The repository's `.github/agent-factory.yaml` does not name this factory ([the fence](config.md#the-ownership-fence)) |
| `shadow_would_arm` | Every check holds and the class is in shadow. Next step: merge or close it |
| `plan_review` | A `plan-first` task's scoper proposed a plan (label `factory/plan-proposed`, no run running). The issue author or a maintainer answers `/factory plan approve`, `/factory plan revise <comment>` (once per task; a second revise escalates as `plan_revision_limit`), or `/factory plan split` (or calls `POST /v1/tasks/{task}/plans`). Past `workflow.planGateHours` (default 72) the status comment marks it parked and never expires it into a run or stop |
| `ci_cancelled` | A required check was cancelled and re-running it did not settle it |
| `contract_check_unset` | The task has a contract but `merge.contractCheck` is no longer set: nothing proves it, so a maintainer decides |
| `contract_modified` | The pull request changes a test the tester wrote as the task's contract (row 2c): a maintainer reads the change, then merges it or sends the task back |

A holdout's workflow file must be a gate path (policy-bot's gate paths cover it), otherwise a pull request can edit the workflow and post SUCCESS itself; the host's gate-coverage check enforces it. A compare that lists 300 files or more, GitHub's cap, is `patch_too_large` without being read. The gate compares GitHub logins (`agentsLogin`, `factoryLogin`, `merge.policyBotLogin`, `merge.mergerLogin`) case-insensitively, as GitHub does. `policy_pending`, `policy_absent`, `head_unreported` and `ci_cancelled` go back to the merge gate by themselves when their input clears, on the same head and with no maintainer review or comment since, up to three times per task.

**Escalated:** the factory could not finish. Next step, unless the row says otherwise: a maintainer comments `/factory retry` on the issue, or pushes to the branch. The comment mentions every maintainer.

| Reason | Why |
|---|---|
| `stuck` | The run showed no room activity for 10 minutes |
| `deadline` | The run hit its wall-clock limit (`tiers.<tier>.runMinutes`) |
| `agent_error`, `agent_stuck` | The agent stopped on an error, or said it was stuck |
| `model_unavailable_billing`, `model_unavailable_auth`, `model_unavailable_quota` | The model provider refused the run six times in a row (the hold bound) (see [Model provider refusals](../factory.md#model-provider-refusals)): its balance is empty, its key was rejected, or its quota is spent. Fix the provider, then `/factory retry` |
| `scoper_failed` | The scoper failed (an agent error, a stuck or lost run) before it checked the scope, so the implementer was not started: the status comment says "scope not checked". `/factory retry` runs the scoper again |
| `judge_impossible`, `stalled`, `not_met` | Under the [loop policy](config.md) (`loops`): the completion judge found the task impossible, the run made no commit within its step limit (or a judge continuation added none within its 15-minute time box), or the judge still found the acceptance checks unmet after the last continuation (or a continuation did not finish within its time box). The harness ends a continuation before the run's wall clock, so a continuation ends with the loop's reason, not `deadline`; its note in the room names which continuation and why. The issue gets the category; the judge's text stays in the room |
| `pod_lost`, `Disrupted`, `PodLost`, `PodFailed` | The sandbox failed or was lost and was not resumed. A sandbox lost to its node is resumed by itself, up to `resume.maxPerTask` times |
| `resumes_exhausted`, `resume_budget` | The sandbox was lost again, and no resume or no budget is left |
| `run_unschedulable` | The cluster did not admit the run within `caps.maxPendingMinutes` |
| `loop_unsupported` | The class has a `loops` row, `search: true` or `checks: true`, but the cluster refused the run claim's `spec.loop` (its AgentRun schema predates the loop policy, `loop.checks` or `loop.search`). No run started and none is retried: roll out crossplane-configuration with `spec.loop` (and `loop.checks`, `loop.search`) first ([operations](../operations/rooms.md#upgrades)) |
| `run_refused` | The cluster refused a Strict run claim (one with a loop policy, `checks: true` or `search: true`) on a field other than `spec.loop`. No run started and none is retried: the factory log line `run claim refused by the cluster` names the refused `fields` |
| `run_lost`, `deleted` | The run disappeared, or its claim was deleted |
| `committed_no_pr` | The run finished after committing, but its branch never reached the remote: the commit existed only in the sandbox and is lost. Comment `/factory retry` |
| `pushed_no_pr` | The run pushed its branch but opened no pull request. Open one from `agent/<task>`, or comment `/factory retry` |
| `revoked` | The run was stopped by hand |
| `budget-run` | The run spent its run cap |
| `needs_author` | Either the intake `ask` guard (before any run starts, when the issue states no acceptance check) or the scoper asked the issue's author one question: the issue gets it (a public task's, quoted) and the label `factory/needs-author`. The author edits the issue and a maintainer re-applies `factory/ready` (a new task), or a maintainer comments `/factory retry` (the task proceeds as assumed), or it expires |
| `split_proposed` | The scoper proposes to land the task as parts: the issue gets the plan and the label `factory/split-proposed`. A maintainer comments `/factory split` to approve it, or `/factory retry` to run the task whole (at the parent's own tier cap), or edits the issue and re-applies `factory/ready` (a new task, which supersedes this one), or it expires at `caps.escalatedHours` |
| `budget-family` | The family's cap (`spec.split.familyMicroUSD`) has no room for another whole run of the next part. `/factory retry` is ignored: the merged parts stay, so open the rest as a new issue; it expires at `caps.escalatedHours` |
| `budget-principal` | The factory's daily budget in dollars is spent |
| `budget-fleet` | The agent gateway's fleet-wide token budget is spent |
| `failed` or another run phase, lowercased | The run ended without a more specific reason from the room or the meter |
| `budget-task` | The task cap has no room for another whole run (a reviewer: a quarter of one). Under `budgets.enforceTask`, `/factory retry` is answered and not taken: the cap is part of the task's spec, so raise it on a new task |
| `budget-person` | [Per-person caps](budgets.md#per-person-caps): the day of the person the next run is charged to is spent; the comment names them. A maintainer's `/factory retry` charges the retrier instead |
| `ci_red` | CI stayed red after `merge.fixRuns` fix runs; the comment names the failing checks |
| `secret_scan_red` | The secret scan (`merge.leakScanCheck`) found a live credential. Next step: a maintainer revokes the key and closes the pull request; the factory does not retry |
| `review_rounds_exhausted` | The reviewer still asked for changes after `maxReviewRounds`; its last verdict is posted on the pull request |
| `no_verdict` | The last review run ended without a verdict the factory can act on, with no round left |
| `room_log_unreadable` | The room's log, which holds the verdict, could not be read |
| `foreign_room` | A room of the task's name exists and is not the factory's |

A task ends with one of these:

| Phase | Reasons |
|---|---|
| `Done` | `children_merged` (a split's last part merged), `merged` (by a maintainer), `merged_verified` (auto-merged, main green for `merge.verifyFor`), `merged_unverified` (auto-merged, main's CI gave no result within twice `merge.verifyFor`, a check cancelled or never finished; the comment names the merge commit, and `factory/revert` still reverts it within `merge.revertWindow`), `proposal_ready` (a triager's proposal: label its issue `factory/publish`, read the new issue the factory files, then label that one `factory/ready`) |
| `NoOp` | `no_pr` (the run changed nothing and opened no pull request), `no_action` (the triager found nothing to change). Add detail and re-apply `factory/ready` |
| `Rejected` | `text_too_long` (over `caps.maxTextBytes`), `daily_task_cap` (`caps.tasksPerDay`), `overlap_active_duplicate` (an enforced exact or scoper-confirmed active outcome match; see [outcome-overlap detection](../factory.md#outcome-overlap-detection)). Fix it and re-apply `factory/ready`; an overlap refusal's citations identify the existing work to reuse or coordinate with |
| `Closed` | `pr_closed`, `stale`, `child_dropped` (a part's issue closed unmerged, or a part reverted) |
| `Stopped` | `stopped_by_label`, `stopped_by_annotation`, `superseded`, `kill_switch`, `escalation_expired`, `family_dropped` (a part stopped because another part was dropped or reverted) |
| `Reverted` | `main_red`, `revert_requested` |
