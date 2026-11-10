# Web UI

The web UI is the one place to watch a room, steer a run, decide an approval and fork. It is
embedded in the broker and served behind oauth2-proxy; [maku](cli.md) reads and posts but
never steers or approves. What it shows is the room's [log](concepts.md#the-log), folded for a
person; the vocabulary is in [Concepts](concepts.md).

## Signing in

| Step | What happens |
|---|---|
| Open `https://rooms.<domain>` | Your browser needs the tailnet; the host resolves only there |
| oauth2-proxy | Sends you to ZITADEL and keeps your session in a `SameSite=Strict` cookie |
| The broker checks your groups | `agents-member` reads the rooms you are let into and acts where a room grants it; `agents-admin` is owner and approver in every room |
| An expired session | A `401` from the API sends the page back through `/oauth2/start` |

- **Neither group: `403`.** Ask an operator to add you to `agents-member`; the group names are the broker's config ([Integration](integration.md)).
- **Room visibility follows GitHub.** A member sees a room only if GitHub lets the account linked to their ZITADEL user read the room's repository. No link, no rooms: the list is empty rather than an error. An admin sees every room. A room you may not read answers exactly like one that does not exist. The fix for an empty list is the one in [maku's troubleshooting](cli.md#troubleshooting).

Sources: `docs/api.md` (the `:8080` checks), `internal/policy/policy.go` (`Groups`, `Resolve`).

## The room list

A table, one row per room you can read. Rooms that need you come first, then the most recently
active, then by id. The whole row opens the room page: the task cell's link covers the row, so it
stays one link for the keyboard and a screen reader. The status says where each room stands in the
room page's words: the same status line, computed by the broker with the same function, so the list
and the room page never disagree.

<img alt="The room list: factory rooms from two repositories, the ones that need you first, each with its repository, status line, PR, last activity and room id" src="assets/web-ui/rooms-light.png" width="800">

| Column | Shows |
|---|---|
| Needs you | A count of what waits for you: approvals you could decide, a task held on a room you drive or own, a PR waiting for your merge. Empty when nothing does |
| Task | The factory task's issue number and title; `Room <id>` for a room with no task |
| Repository | The room's `owner/name` |
| Status | The room page's status line: an icon, a colour accent and fixed words, such as `⏳ Waiting for you — approve a pending action` or `✓ Done — merged` (the API's `status`). The task's phase, else the room's, on hover; the phase chip alone when the broker sends no status. On a phone it replaces the repository column |
| PR | `#n`, when the task has one |
| Last activity | How long ago the room's log last moved (`lastEventAt`), the exact time on hover; `—` for a room with no events |
| Room | The room id |

The list refreshes on its own, every 10 s by default. The control before `New room` picks the
interval (`Off`, `5 s`, `10 s`, `30 s`, `1 min`; remembered in the browser), pauses it, and says
when the list was last read, or that the last refresh failed. A background tab slows to once a
minute and its title counts the rooms that need you, `(2) Maku rooms`. A refresh that changes
nothing leaves the table as it is, and one that does keeps the focus on the same room.

Above the table, three filters refetch the list from the API: **mine** (tasks whose issue you
filed or labelled, or whose PR you authored or review, matched on your linked GitHub login),
**needs me** (rows with something waiting for you) and a repository picker, filled from the
repositories of the rooms the page first listed. They are the API's `mine`, `needs_me` and
`repo=owner/name`, applied after the access check so none reveals an unreadable room
([API](api.md#8080--human-api)). An empty table says why: `No rooms match` under a filter,
`No rooms you can read yet` without one.

**closed** lists the rooms closed in the last 30 days instead: when a task ends the factory deletes
its Room, so its sealed room leaves the default list, though `/r/<id>` still opens it until
retention purges the log. Each row is a room its link would open for you (the same access and role
checks); **mine** and **needs me** are set aside while it is on. It is the API's `closed=1`.

### New room

**New room**, the primary button at the right end of the filters row, opens a form above the list that creates a room you own and
drive, then opens it.

| Field | Rule |
|---|---|
| Data class | `internal` or `public`, no default. It cannot change once the room exists: it sets the model route, the tools and the egress its runs get. It does not change who can read the room |
| Repository | `owner/name`, required. Anyone who can read it on GitHub can read the room. You must be able to read it yourself |

A `Use maku from your terminal` fold under the list sets up [maku](cli.md) for this
broker in numbered steps (install, point it at this broker, sign in), each command in its own
block with a Copy button that copies it exactly as shown. On a local broker the steps configure
by URL alone and end with `maku run` instead of a sign-in.

<img alt="The Use maku from your terminal fold: three numbered steps, install, point it at this broker and sign in, each in a code block with a Copy button" src="assets/web-ui/cli-setup-light.png" width="800">

## The room page

<img alt="A room page during a live review: the run chip reads reviewer, its id, the model agent-default, the effort high and its age, then the budget, PR, issue, Dashboards and the Now line; the factory brief folded under Show more; the dock" src="assets/room-light.png" width="800">

From the top:

| Part | Shows |
|---|---|
| Title strip | The status line, the task's phase on hover. Then the task's issue number and title, a link to the issue (`Room <id>` without a task). Then a line of facts, set as text with a dot between them: the run (role, id, model, effort and age, while an agent is in the room), `Tokens 189 k of 1.5 M` with a bar (amber from 80 %, red once spent; the percentage on hover), the PR link (and `Issue #N` when the title cannot carry it), the last verdict, `Data: public` or `Data: internal` (which model backends the runs may reach, on hover), `Driven by you`, `the factory` or a short principal (what a driver may do, on hover), and `View only` when you are a watcher who cannot approve; an owner, collaborator or approver sees no role. The phase shows as a fact only when no status line does. When the operator configures [`human.dashboards`](integration.md#the-brokers-configuration), `Dashboards: Room · Run · Task` links them, filled with the room's ids, each opening in a new tab; none configured, none shown. A `Needs you · N` button jumps to the panel below. Then a **Next** box while the task waits: `<why> → <next>`, each backticked command set as code with a Copy button, followed by `Your move: <step>`: `review and merge or close PR #N` (a link to the PR, which is decided on GitHub, not in the room; under `merge.approveToMerge`, `review, then approve the current commit or close PR #N`, and the status line says `Waiting — review and approve PR #N`) or `approve here` (the Needs you panel below); in a sealed room, `Closed <when> by <who>` (or `Sealed`) instead, with no box. Last, the **Now line**: what the agent is doing (the latest call, its status, how long ago), a link that opens and focuses that row; `No run yet` before any run, `No run` once the agent left, with `Start one under Session › Run` for an owner or the driver unless the room already says what comes next. The connection is the dock's, not the strip's. On a phone the facts keep one line that scrolls sideways, `Needs you` first, and the title, the waiting lines and the Now line stop at two lines each, the full title kept as its tooltip |
| Needs you | Shown only when something waits. One card per pending approval, decided right there (below); then the PRs waiting for your merge (`Approve:` rather than `Merge:` under `merge.approveToMerge`, where approving the current commit merges it), the issues whose author must answer the scoper (linked to the issue), and the tasks held on your room. A viewer who may not decide reads the cards under `Waiting for an approver` |
| Notes | The latest note from the agents, labelled as the agent's claim; the earlier ones, newest first, behind a `Notes (N)` toggle. No notes, no block |
| Harness profile | A fold, closed by default, in a room whose task carries `status.profile`: `Harness profile · <name> · <cohort>`. Inside: the profile's source, then the scoper's recommendation, reason and explanation when present. Observe mode keeps those distinct: the active floor is in the heading, while `Recommended` names the variant the scoper offered but the factory did not apply. Older rooms and classes without harness profiles show no block |
| Merge policy | A fold, closed by default, in a factory room once the factory posts its policy: `Merge policy · <class> · <mode>` (`live`, `shadow` or `unarmed`; `gate off` when no class is live or in shadow). Inside: the class's conditions (diff shape, caps, allowed and protected files, holdout checks, adopted logins, `approve to merge` under `merge.approveToMerge`), what it takes to merge (the class's conditions, a maintainer's approval of the current commit that the merger App merges, or a maintainer's merge; a pull request touching a gate path always needs a human merge), the gate's decision when there is one (`it would auto-merge, and the merge gate is in shadow` is the shadow decision, or how the merger merged it), and the repository's classes. Read-only: the policy is the factory's config, changed by a pull request to it |
| Rail | The task's path, right of the conversation between the strip and the dock (300 px wide): one node per run with its persona, its state or verdict, its age, cost and model, the plan's next steps dashed after it, the tool calls and the rest on hover or focus. Below 1100 px it folds into a row of dots that opens it as a drawer (`Close` or `Esc` shuts it). As you scroll, the node whose events are on screen is marked. Clicking a node shows that run's events in a view of their own, Raw events included, with `All runs` to return; the page's address gets `#run=<id>`, so a link reopens it. The view loads at most 2,000 events of a run; a live run's keep arriving. A run whose events are not in this room's log says so. Motion: a new node fades in, its edge draws, a judge verdict ticks and a milestone fills, all off under reduced motion. Replay: a finished task replays its nodes then its milestones in at most 20 s, with `Skip` to end it and `Next` to step under reduced motion. Split: a scoper that split the task shows one chip per part (done, dropped, reverted, running or queued) linking to its room, and each part's rail opens with a "part k of N of #parent" node that links back |
| Conversation | Messages as bubbles, each actor named by room role and short id (`you` for you); a tool call and its result as one collapsed row; the policy's decisions as badges on that row, only when not a plain allow or when a person decided. Runs of plumbing (state changes, turns, membership lines) fold into one `· turn <id> · N changes` line, open on click; a harness error, a seal and a driver change to or from you stay out of the fold. A message longer than about twelve lines shows its start under a fade with `Show more`, and stays open once you open it, whatever arrives after. Each message and handoff can open a fork |
| Raw events | A fold, closed by default, with every event as the broker wrote it. It is always as current as the conversation |
| Controls | Named groups, each shown only to the standings that use it: **Run** (start a run as a role; a PR URL for a reviewer, egress profiles under `advanced`), **Driver** (who holds the token; give, interrupt, request, take with a reason), **Queued for the next run** (remove; `steer now` for the driver), and **Room**, a fold for owners (members, invite, how to stop the task with `factory/stop` on its issue, close) |
| Dock | Pinned at the foot: the composer (the text, `Deliver to` — post to the room, queue for the next run's brief, or steer the running agent — and Send), the page's notices, the theme button and the connection |

Scrolling a finished factory room, from the strip down through its turns and tool calls:

<img alt="Scrolling down a factory room's conversation: the triager's scope, folded tool calls with their status, state changes folded into turns" src="assets/web-ui/room-scroll.gif" width="800">

A factory brief folded under `Show more`, with the `Dashboards` chip at the end of the strip:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/web-ui/room-fold-dark.png">
  <img alt="A factory room whose task brief shows its first lines under a fade and a Show more link; the strip ends with a Dashboards chip linking Room metrics and Run" src="assets/web-ui/room-fold-light.png" width="800">
</picture>

On a phone the strip stays compact above the conversation:

<img alt="The same room on a phone: the status line first, the title and the waiting lines clamped to two lines" src="assets/web-ui/room-phone-light.png" width="300">

Destructive acts ask first: **interrupt**, **take**, **steer now** and **close room** turn into
`confirm: …` beside a cancel, and go back after 10 s, on cancel, or when the control is hidden.

The summary behind the strip, the panel and the notes refetches at most once a second, and only
when an event it folds arrives (a task phase, a limit, progress, a verdict, an approval), not on
every chat line or tool call. A sealed room shows who closed it, has no waiting line, no needs
and no actions, and hides the composer, the Run and Driver groups and the invite. Everything an
agent wrote is shown as text; a URL becomes a link only when it is `https`. The page keeps the
newest 5,000 events; older ones leave the page, never the log.

A run without gVisor (`ALLOW_UNSANDBOXED=1`) puts a `Not sandboxed` banner above the page for as long as it is open.

## What each standing may do

A standing is cumulative: watcher, then collaborator, then owner. The **approver** flag adds
deciding on top of any of them. The **driver** is whoever holds the driver token, whatever their role.
`agents-admin` is owner and approver everywhere. The broker checks every action; a hidden
button is a courtesy. Source: `internal/policy/policy.go`.

| Action | Watcher | Collaborator | Owner | Approver flag | Driver |
|---|---|---|---|---|---|
| Read the room, live | yes | yes | yes | | yes |
| Fork | yes | yes | yes | | yes |
| Chat (a message no agent receives) | | yes | yes | | yes |
| Queue a message for the next run | | yes | yes | | yes |
| Remove a queued message | | yes, your own | yes, your own | | yes, any |
| Steer the running agent (web only) | | | | | yes |
| Interrupt (web only) | | | | | yes |
| Promote a queued message to a steer (web only) | | | | | yes |
| Hand a task to a role (start a run) | | | yes | | yes |
| Request the driver token (web only) | | yes, if not the holder | yes, if not the holder | | |
| Give the driver token (web only) | | | | | yes |
| Take the driver token (web only) | | | yes | | |
| Decide an approval (web only) | | | yes | yes | |
| Invite, change roles, close | | | yes | | |

Reading the table:

- **Web only** means a token issued to maku is refused, because a local agent could run maku. Chat, queue, fork and start a run work from either client.
- **Steering is the driver's alone.** An owner who is not the driver takes the token (with a reason) before steering.
- **Without the driver token**, a message sent from the composer can be posted to the room or queued; the steer option is disabled. With no agent in the room it is not offered at all. A draft whose steer option goes away (the token moved, the run ended) stays, and the composer asks where it should go.
- **A sealed room** accepts no action. Only its owner or an admin may fork it.
- **An agent run** can read and post in its own room and never decides an approval; only the broker's policy engine can decide on a system's behalf.

## Approvals

When a run is about to do something a human must clear, a card appears in the Needs you panel,
where it is decided; the raw events show the same request, read-only.

| Part | Rule |
|---|---|
| The banner | When approvals are pending, the title strip says so under the phase: `⏳ 2 approvals wait for you — review`, or `… wait for another approver` when you may decide none of them. The link scrolls to the first card and focuses it. Absent when none wait, and in a sealed room |
| The card | Read top to bottom: **what the agent wants** (plain words), **why it stopped** (the rule that raised it), **if you approve / if you deny** (what each answer does), **the deadline** (`Decide by 14:30 (in 22 min); if nobody decides: the request expires and the agent is told no`, or `No deadline.`), and **whether you may decide**. Then, behind `details`, the raw command, the class, the run and the **raw action** as redacted text. All of the plain prose is fixed per rule code; the agent's words are never the reason. A link takes you to the call's row while the page holds it. `approve` and `deny` with an optional reason; the card leaves once the log records a decision, whoever made it |
| The rules | The broker names the rule by the approval's class: `forge.push` (a push to the forge), `forge.pr` (a pull request opened or changed), `forge.other` (a command outside the allowlist of shapes the broker can read as safe), `mcp.write` (an MCP tool not on the read-only list), `shell.high` (a command its harness flagged high risk), `egress.new` (a destination outside the egress profiles). Any other code reads as a generic policy stop. Denying, or letting the deadline pass, always tells the agent no and nothing of it runs |
| Who decides | An owner, or a member with the approver flag, in the web UI. The card says which case you are in: `You can decide`, `only approvers decide`, or, under four-eyes, `Waiting for another approver: you prompted this turn`. Others see the request but no buttons |
| First decision wins | The first valid decision stands; every later one, and a superseded request, answers `already_decided`. One that reaches the broker after the deadline answers `approval_expired`, even before the sweep has closed it |
| Four-eyes | When the room requires it, nobody who prompted the turn may decide it: not whoever requested the run, steered it, or wrote a queued message it consumed |
| Reason | Optional, up to 1 KiB, redacted. The agent reads it |
| Expiry | 30 minutes in an `attended` room; the room's configured delay (default 4 hours) in an `unattended` one. An expired request is denied |
| Superseded | A request whose call already ran, or whose run has left, is closed without a decision |

Sources: `docs/api.md` (approvals, `decide`), `web/src/controls.ts` (`approvalCard`), `web/src/needs.ts`, `web/src/approval-help.ts` (the prose per rule code).

## Forks

Pick **fork here** on a message or handoff to branch the room at that point.

| Question | Answer |
|---|---|
| What is copied | Events 1 to the chosen one, with their `seq`, then a marker naming the source. The source's data class, repository and retention. The messages still queued at that point, queued again in the fork |
| What is not | The source's members: the fork has none |
| Who owns it | You: owner and driver |
| Approvals | A source owner or admin keeps the source's approval settings; anyone else gets, class by class, the stricter of the source's and a fresh room's |
| A first run | Optional: pick a role (and a PR for a reviewer, egress profiles for a non-`internal` room). It starts on a new branch from the latest agent commit at or before the chosen event, on your token and budget. If the run fails, the fork stays made and the panel says why |
| Limits | A prefix over 5,000 events or 32 MiB is refused (`too_large`): fork from an earlier message. Three forks at once, then one a minute |
| Sealed source | Only its owner or an admin |

## Settings

With `policyManagement.promotion` on, `/settings` opens the merge-policy pages; off, they are a
`404`. Nothing links to them from the room list yet: open the address.

| Page | Shows |
|---|---|
| `/settings` | Section cards (merge policies, evidence, approval memory) with live counts |
| `/settings/merge-policies` | One row per repository: its `MergePolicy` with its Accepted and InSync conditions in words (or "Not migrated", or "status unknown" before the factory reports them), its classes by mode, the class closest to promotion |
| `/settings/merge-policies/<owner>/<name>` | A header saying once what serves the classes: the `MergePolicy`, its namespace, authority and generation, its conditions as words on their colour, and a legend for the three modes; one line on `.github/agent-factory.yaml` (a notice only when an invalid file pauses auto-merge). Then one card per class: its name and mode word, its [description](merge-policies.md#description), its conditions in one line, its last decision, evidence toward the [promotion](factory.md#promotion) with the widening steps, the full conditions folded, its promotion and exception. A repository with no `MergePolicy` says "Not migrated yet" and has no editor |
| `/settings/merge-policies/<owner>/<name>/decisions/<pr>` | The pull request's decision timeline |
| `/settings/evidence` | Every class of every repository you read, in one table |

| Who | May |
|---|---|
| A reader of the repository | Read everything above |
| A maintainer (write on GitHub), MergePolicy under `central` authority | **Check** a proposal in the policy editor (guided form, templates or expert YAML): validation, structural change, simulation and the `MergePolicy` manifest, to copy or download and commit where the platform's GitOps applies it. A shadow class is promoted by editing the `MergePolicy`: its card says "set `live: true` on this class in the MergePolicy and merge the change" and **Edit this class** opens it in the editor. Ask an operator for an exception below the minimum, with a justification |
| A maintainer, under `repository` authority or not migrated | **Check** in the policy editor (none on a not-migrated page): the file to submit with its edit link. **Promote to live** an eligible shadow class: the page shows the file with that one line changed and the GitHub edit link, and you open the pull request yourself. Record its number, then merged or abandoned. Ask an operator for an exception below the minimum, with a justification. A not-migrated page has no editor but keeps these promotion and exception requests: they are the broker's records, and the file proposal works there as before |
| An operator (`policyManagement.operators`) | Approve or refuse a requested exception, once |

The broker checks each of these on every request; a hidden button is only a convenience.

With four items or more, the repository table and a repository page's class cards have a filter
above them. It matches, case-insensitive, every word typed against the repository and
`MergePolicy` names (table) or a class's name, mode and description (cards), shows "3 of 9
repositories", and names the query with a **Clear** button when nothing matches. The query is kept
in `?q=`, so a filtered page can be shared; `/` focuses the field, except while you type in another
one, and Escape clears it.

## Theme

Every page has a theme button, in the room page's dock and in the header elsewhere. Each press
moves it to the next of three modes:

| Button | Theme |
|---|---|
| `theme: light` | Day: light surfaces |
| `theme: dark` | Night: deep navy surfaces |
| `theme: system` | Day or Night as the OS prefers, following it when it changes; the mode until you choose |

The choice is kept in the browser (`localStorage`), so it holds across rooms and reloads. Both
themes are the Ogenki design system's, and every text and border pair meets WCAG AA in each: a
status is always a word as well as a colour. The design reference is
[`web/DESIGN.md`](../web/DESIGN.md).

## When something fails

A refused action shows its reason in the dock. Source: `web/src/controls.ts`.

| Reason | What it means | What to do |
|---|---|---|
| `not_permitted` | Your role, or this client, does not allow it; or the factory refused you a run | Check the table above; ask an owner to invite you with a wider role |
| `stale_epoch` | Someone else holds the driver token now | Read the new holder, try again if it still applies |
| `rate_limited` | Over 10 actions a second, or over the fork limits | Wait and retry |
| `no_running_run` | Nothing is running to steer or interrupt | Queue the message for the next run |
| `room_busy` | A run is already running, or one was just requested and has not joined | Wait for it to join or end |
| `reviewer_needs_pr` | A reviewer has no PR to review | Give the PR's URL |
| `over_budget` | The factory refused the run: you are over your budget | See [the factory](factory.md) |
| `factory_unavailable` | The run could not be requested now | Retry |
| `bad_action` | The request was malformed, or a give went to someone who cannot drive | Check the fields; give to a member who can drive, or the room's system holder |
| `not_queued` | The message was already delivered or removed | Nothing |
| `sealed` | The room is closed and takes no more actions | Fork it, if you own it or are an admin |
| `conflict` | The room changed at the same moment | Retry |
| `log_unavailable` | The room's log is unavailable | Retry |
| `already_decided` | Another approver decided first | Nothing |
| `approval_expired` | The decision reached the broker after the deadline; the request was denied as expired | Nothing: the agent may ask again |
| `four_eyes` | The room needs an approver who did not prompt the turn | Ask another approver |
| `too_large` | The fork would copy over 5,000 events or 32 MiB | Fork from an earlier message |

Other rooms-side outcomes: a `404` on the page means no such room or no access (the same
answer on purpose); creating a room fails with `400` for a data class or repository not
accepted, `404` when you cannot read the repository, `503` when your GitHub access cannot be
checked.
