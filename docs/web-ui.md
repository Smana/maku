<!-- page-type: how-to -->

# Web UI

The web UI is the one place to watch a room, steer a run, decide an approval and fork. It is embedded in the broker behind oauth2-proxy; [maku](cli.md) reads and posts from a terminal, while steering and approvals stay here. See [Concepts](concepts.md) for the room vocabulary and [Web UI reference and settings](reference/web-ui-settings.md) for standing matrices, `/settings` pages, forks, themes and error codes.

## Signing in

| Step | What happens |
|---|---|
| Open `https://rooms.<domain>` | Your browser must reach the broker's address (see [Exposure](exposure.md)) |
| oauth2-proxy | Sends you to your identity provider and keeps your session in a `SameSite=Strict` cookie |
| The broker checks your groups | `agents-member` reads the rooms you are let into and acts where a room grants it; `agents-admin` is owner and approver in every room |
| An expired session | A `401` from the API sends the page back through `/oauth2/start` |

- **Neither group: `403`.** Ask an operator to add you to `agents-member`; the group names are the broker's config ([Integration](integration.md)).
- **Room visibility follows GitHub.** A member sees a room only if GitHub lets the account they linked ([GitHub account link](github-link.md)) read the room's repository. No link, no rooms: the list is empty rather than an error. An admin sees every room. A room you may not read answers exactly like one that does not exist. The fix for an empty list is the one in [maku's troubleshooting](cli.md#troubleshooting).

Sources: `docs/api.md` (the `:8080` checks), `internal/policy/policy.go` (`Groups`, `Resolve`).

## The room list

Rooms you can read appear in one table: rooms that need you first, then most recently active, then by id. If an install uses demo-only or unsafe settings, an alert banner above the list names each code ([install banner unsafe codes](reference/web-ui-settings.md#install-banner-unsafe-codes)).

<img alt="The room list: factory rooms from two repositories, the ones that need you first, each with its repository, status line, PR, last activity and room id" src="assets/web-ui/rooms-light.png" width="800">

Each row shows `Needs you`, `Task`, `Repository`, `Status` (computed with the same broker function as the room page), `PR`, `Last activity` and `Room` ([room list columns](reference/web-ui-settings.md#room-list-columns)), and clicking anywhere on the row opens the room.

The list refreshes every 10 s by default (`Off`, `5 s`, `10 s`, `30 s`, `1 min` before **Open a live session**, remembered in the browser), slows to once a minute in a background tab with `(N) Maku rooms` in the title, and keeps focus across updates.

Above the table, filters refetch the list from the API: **mine** (issues you filed or labelled, or PRs you authored or review), **needs me** (waiting on you, sorted and filterable by `question`, `plan`, `review`, `blocked`, `failed`), and a repository picker ([API](reference/api.md#8080--human-api)). **closed** (`closed=1`) lists rooms closed in the last 30 days (disabling **mine** and **needs me**); sealed rooms stay reachable at `/r/<id>` until retention purges the log. Empty results explain why (`No rooms match` or `No rooms you can read yet`).

### New task and Open a live session

**New task** (primary) and **Open a live session**, at the right end of the filters row, open forms above the list.

- **New task** plans through `/api/handoffs`: enter what should change, pick a repository (or leave it on Auto), and click **Plan**. It shows the workflow card (`Outcome`, `Workflow`, `Will run`, `Cost`), the planner's question if any, and the drafted issue. Maintainers can pick a described workflow, and any member can toggle **Plan first**. Only maintainers get **Start**; unlinked viewers get a `/link/github` link.

  The factory App files and labels the issue; the factory starts it, as your task, only from the broker's record of your start ([broker-confirmed handoffs](security.md#authorization)).
- **Open a live session** creates a room you own and drive, tied to no issue, then opens it.

| Field | Rule |
|---|---|
| Data class | `internal` or `public`, no default; fixed once created. Sets model route, tools and egress, not read access |
| Repository | `owner/name`, required. Readable by anyone who can read the repository on GitHub |

The `Use maku from your terminal` fold under the list gives copyable setup steps for [maku](cli.md) (install, configure, sign in; or URL + `maku run` on a local broker).

<img alt="The Use maku from your terminal fold: three numbered steps, install, point it at this broker and sign in, each in a code block with a Copy button" src="assets/web-ui/cli-setup-light.png" width="800">

## The room page

<img alt="A room page during a live review: the run chip reads reviewer, its id, the model agent-default, the effort high and its age, then the budget, PR, issue, Dashboards and the Now line; the factory brief folded under Show more; the dock" src="assets/room-light.png" width="800">

Work a room from top to bottom ([component reference](reference/web-ui-settings.md#room-page-components)):

- **Read the title strip and workflow card**: Tier 1 shows the workflow chip, status line, active run's persona and age, and `Needs you · N`; Tier 2 shows spend, PR/issue links, last verdict, data class, driver and `Dashboards`, followed by `Next` and `Now`. The header has one **Stop** button (behind two-press confirm with a **Discard PR, branch and issue** checkbox) for whoever may stop a live task. Below the strip, **Workflow** shows `Workflow`, `Will run`, `Cost` and any refused pin (`How it was chosen` holds raw details).
- **Clear `Needs you`**: decide approvals in place, and act on `question` (**Answer**), `plan_confirmation` (**Accept the split** / **Run it whole**), `plan_review` (**Approve** / **Revise** / **Split**) and `recovery` (**Retry** / **Run again deeper**) cards when enabled in `human.roomActs`; disabled acts, merges and reviews link to GitHub. An accepted act replaces the buttons with a status (`✓ Approved by you — the implementer starts next.`); a refusal shows its reason; a card unchanged 30 s later warns `Sent, but the task has not moved yet — see the room log.` **Stop** reports the same way.
- **Inspect runs and context**: expand `Notes`, `How it was chosen`, `Merge policy` and `Raw events` folds as needed, or click any run node on the right-hand **Rail** (`#run=<id>`) to filter events to that run.
- **Steer, queue or run**: use **Controls** (`Run`, `Driver`, `Queued for the next run`, `Room`) according to [what each standing may do](reference/web-ui-settings.md#what-each-standing-may-do), or pick **fork here** on any message or handoff ([forks](reference/web-ui-settings.md#forks)).
- **Send from the dock**: choose `Deliver to` (post to the room, queue for the next run's brief, or steer the running agent — driver only), toggle the [theme](reference/web-ui-settings.md#theme), and watch connection notices. Destructive acts (**Stop**, **interrupt**, **take**, **steer now**, **close room**) require a second `confirm: …` click within 10 s.

Scrolling a finished factory room, from the strip down through its turns and tool calls:

<img alt="Scrolling down a factory room's conversation: the triager's scope, folded tool calls with their status, state changes folded into turns" src="assets/web-ui/room-scroll.gif" width="800">

A factory brief folded under `Show more`, with the `Dashboards` chip at the end of the strip:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/web-ui/room-fold-dark.png">
  <img alt="A factory room whose task brief shows its first lines under a fade and a Show more link; the strip ends with a Dashboards chip linking Room metrics and Run" src="assets/web-ui/room-fold-light.png" width="800">
</picture>

On a phone the strip stays compact above the conversation:

<img alt="The same room on a phone: the status line first, the title and the waiting lines clamped to two lines" src="assets/web-ui/room-phone-light.png" width="300">

## Approvals

When a run stops on a gated action (`forge.push`, `forge.pr`, `forge.other`, `mcp.write`, `shell.high`, `egress.new`), a card opens in `Needs you` showing fixed prose for what the agent wants, the rule that stopped it, the deadline, and—under `details`—the redacted raw command and action ([approval card and rule classes](reference/web-ui-settings.md#approval-card-and-rule-classes)).

1. Check your standing on the card (`You can decide`, `only approvers decide`, or `Waiting for another approver: you prompted this turn` when four-eyes is on).
2. Inspect `details` to verify the raw redacted action rather than the agent's chat prose.
3. Click **approve** or **deny** with an optional reason (up to 1 KiB, redacted and shown to the agent). First valid decision wins; unapproved requests deny automatically at the deadline (`30 min` in attended rooms, `4 h` default in unattended rooms).

## When something fails

A refused action shows its reason code in the dock:

- **`not_permitted` / `four_eyes` / `access_unverified`**: check [what each standing may do](reference/web-ui-settings.md#what-each-standing-may-do), ask another approver when you prompted the turn, or ask a repository maintainer for protected-path pushes.
- **`stale_epoch` / `no_running_run` / `room_busy`**: refresh who holds the driver token, queue your message for the next run if the agent already finished, or wait for a requested run to join.
- **Page and room-creation errors**: `404` means no such room or no read access on GitHub; creating a room returns `400` for an invalid data class or repository, `404` when you cannot read the repository, and `503` when GitHub access cannot be verified.

Every dock refusal code and recovery action (including **Retry** and **Run again deeper** on escalated cards) is listed in [Action refusal reasons](reference/web-ui-settings.md#action-refusal-reasons).
