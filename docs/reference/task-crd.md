<!-- page-type: reference -->
# The Task CRD

`tasks.maku.ogenki.io/v1alpha1`, namespaced, created by the factory at runtime and never
committed to Git. Its name is an 8-character id (`[a-z2-7]{8}`) derived from the source's key, so
creating it twice is the dedup. Source: `api/factory/v1alpha1/task_types.go`.

| Field | Meaning |
|---|---|
| `spec.source.kind` | `issue`, `runlore`, `schedule` or `api` |
| `spec.source.ref` | `owner/name#n`, `<alert>/<resource>`, the schedule's name, or an `api` task's `ref` (default `api:<name>`) |
| `spec.source.key` | The idempotency key the name derives from |
| `spec.source.requestedBy` | `github:<login>`, `system:runlore`, `system:scheduler`, or `system:<name>` for a sponsored principal |
| `spec.source.sponsor` | `api` tasks only: `github:<login>`, the maintainer accountable for the request (immutable) |
| `spec.source.onBehalfOf` | `api` tasks only: `slack:<team>/<user>` when a link chose the sponsor |
| `spec.maxMicroUSD` | `api` tasks only: the request's `max_cost` ceiling in micro-USD, immutable; 0 for every other task. See [sponsored principals](sponsored-principals.md) |
| `spec.source.trust` | `untrusted` text is fenced as data for the agent; `trusted` (schedules) is not |
| `spec.source.contentSHA256` | sha256 of the text as labelled |
| `spec.repository` | `owner/name` |
| `spec.issue` | The issue the factory narrates on; 0 for none |
| `spec.issueAuthor` | The issue's author, for the room list's "mine" filter |
| `spec.text` | The sanitised snapshot; admission caps it at `caps.maxTextBytes` |
| `spec.dataClass` | `public` or `internal`; every run of the task gets it |
| `spec.predictedClass` | The merge class triage predicted. Intent, not authority: only the gate merges |
| `spec.template` | `solo`, `pair`, `trio` or `investigate` |
| `spec.budget` | `tier`, `model`, `runTokens` (≤ 5,000,000), `taskTokens`, `runMinutes` (≤ 480), `runMicroUSD`, `taskMicroUSD` (0: the token caps) |
| `spec.loop` | `stallSteps`, `continuations`, `judgeModel` (empty: no judge): the class's `loops` row, resolved at triage; every implementer run of the task gets it as `spec.loop`. Unset: the loop is off |
| `spec.scopeMode` | `off`, `steering` or `scoper`: the class's `loops` row, fixed at triage. Empty: triaged before the field existed, read as `off`. An `investigate` task is always `off` |
| `spec.search` | `true` when the class's `loops` row sets `search`, fixed at triage; every implementer run of the task gets `spec.loop.search`. Omitted: off |
| `spec.steering` | `path`, `sha` (the git blob), `text`: the class's steering note on the default branch, read at triage; unset for none |
| `status.phase`, `status.reason`, `status.phaseSince` | The phase above, why, and since when |
| `status.classification` | The classifier's `tier`, `confidence`, `classifier`, `fallback` (`none`, `default`, `static`), `shadow` verdicts, `control` (forced to `frontier`) and `fit` (scored at the end) |
| `spec.split` | `maxChildren` (2–5), `familyMicroUSD`, `childTier`: set at triage on a task C7 rated `frontier` (any task, with `split.anyTier`) whose class has a `split` row. The family's cap lives here; the parent's `spec.budget` stays its tier's |
| `spec.parent` | On a part: `task`, `issue`, `index`, `of`. Fixed at creation. A part is requested by the approver and carries its parent's snapshot whole (`text`, `contentSHA256`) |
| `spec.scope` | On a part: its own scope (`state: ready`, `task`, `touches`, `leave`, `plan`, `assumptions`, `acceptance`), from the approved split. Never changes once set; a revision brief keeps it whole |
| `status.runs[]` | Each run: `id`, `role`, `trigger` (`initial`, `review`, `human`, `ci`, `retry`, `resume`), `round`, `phase`, `reason`, `verdict`, `tokens`, `costMicroUSD`, `unpriced`, `startSeq`, `headSHA`, `started`, `finished`, and `provenance`, what it ran on: `factoryVersion` (the claim's CREATE-only `agents.ogenki.io/factory-version`), `compositionRevision` (the claim's `spec.crossplane.compositionRevisionRef.name`), `harnessImage` (the claim's `status.harnessImage`, the harness container's `imageID`) and `servedModels` (the meter's `gen_ai_request_model`, at most 8, each at most 64 characters: a longer name is skipped, never truncated). Each is written once, as it becomes known; empty means unknown. A run created before the upgrade (its claim has no factory version) stays empty, and a value over the CRD's bound is not recorded |
| `status.roomRef` | The task's room, named like the task |
| `status.pullRequest` | `number`, `url`, `nodeID`, `headSHA`, `mergeCommitSHA`, `mergedBy`, `autoMerged`, `armedAt`, `mergedAt`, `revertNumber`, `author`, `reviewers` |
| `status.usage` | `tokens` and `costMicroUSD` summed over the runs; never drops |
| `status.usageSettled` | The meter's late readings had their window and the total is final |
| `status.endedAt`, `status.outcome` | The task's first end and what it ended as (`accepted`, `merged_human`, `no_op`, `closed`, `stopped`, `rejected`, `reverted`, `failed`): the settle window counts from the first. A later `Reverted` revises neither. `task_outcomes_total` reports `outcome` for a task of its own; an approved split's parent reports the family's outcome, read from its parts at the settle, and its own `outcome` is a part's fallback (a part never opened, or not ended) |
| `status.evaluated` | A split's part the evaluation counts under its parent's workflow: written at the part's triage when the parent has one. Its merge's provenance is read for the mark, never for the parent |
| `status.reviewRounds`, `status.fixRuns`, `status.retries`, `status.resumes` | Counters behind `maxReviewRounds`, `merge.fixRuns`, `/factory retry` and `resume.maxPerTask`. `status.fixRunsBeforeStep` is `fixRuns` at the escalation step: the cap counts from it |
| `status.checkReruns`, `status.checkRerunsHead` | Re-runs of cancelled checks, per head |
| `status.gateReentries` | Returns from a transient hand-off to the gate (at most 3) |
| `status.verdict` | The last review's effective verdict: `approve`, `changes` or `none` |
| `status.narrated`, `status.outbox` | Keys of the comments posted; comments still owed |
| `status.roomSeq`, `status.facts` | The task's message sequence in its room; the facts last written there |
| `status.nextTrigger`, `status.nextRole` | Why the next run exists; the verifier it is for |
| `status.handled` | GitHub review and comment ids already acted on |
| `status.configHash` | The config that triaged the task |
| `status.contract` | `runId`, `commit` (40 hex), `files`, `ids` (1 to 50 each): the tester's commit as GitHub recorded it. Set once, when its push is read; every implementer run is held to it |
| `status.scope` | `state` (`ready`, `question`, `assumed`, `missing`, `failed`) and `seq` (the scope's handoff): the scoper's outcome, kept when the task stops |
| `status.overlap` | The bounded [outcome-overlap evaluation](../factory.md#outcome-overlap-detection): repository-scoped `fingerprint` (64 lowercase hex), `classification`, at most 20 `matches` (Task/run 8-character ids, positive issue/PR numbers, `exact`/`lexical`, phase), at most four checked `sources` (`tasks`, `runs`, `rooms`, `github`), optional deterministic evaluation-run `scoperRun`, `checkedAt` and a note of at most 512 characters. Pending has neither classification nor `checkedAt`; a final classification and its evidence are immutable |
| `status.split` | `state` (`proposed`, `approved`, `declined`, `superseded`, `expired`, `ignored`), `seq` (the split's handoff), `of`, `approvedBy` (`github:<login>`, every part's payer) and `children[]` (`index`, `issue`, `task`, `outcome`: `landed`, `dropped`, `reverted`) |
| `status.lastActivity` | The running run's last room event, for stuck detection |
| `status.trace` | The task's root span ids |

| Metadata | Meaning |
|---|---|
| Annotation `agents.ogenki.io/stop` | Stop this task: `true` by hand, `label` from `factory/stop`, `superseded` by a new `factory/ready`, `family_dropped` on a part whose family ended `Closed` |
| Annotation `agents.ogenki.io/revert` | A maintainer's `factory/revert` |
| Label `agents.ogenki.io/issue` | The issue number |
| Label `agents.ogenki.io/parent` | A part's parent task: how the factory lists a family and sums its spend. Only `spec.parent` counts as membership |
| Label `agents.ogenki.io/source-key` | Groups a RunLore finding's tasks |
