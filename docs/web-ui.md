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
([API](reference/api.md#8080--human-api)). An empty table says why: `No rooms match` under a filter,
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

Work a room from top to bottom ([component reference](reference/web-ui-settings.md#room-page-components)):

- **Read the title strip**: Tier 1 shows the status line, the active run's persona and age, and a `Needs you · N` button; Tier 2 shows token and dollar spend, PR and issue links, last verdict, data class, current driver and optional Grafana links (`Dashboards: Room · Run · Task`), followed by a `Next` box while waiting and the `Now` line for the latest call.
- **Clear `Needs you`**: decide pending approvals in place, or follow direct links for GitHub merges, reviews and scoper questions.
- **Inspect runs and context**: expand `Notes`, `Workflow`, `Merge policy` and `Raw events` folds as needed, or click any run node on the right-hand **Rail** (`#run=<id>`) to filter events to that run.
- **Steer, queue or run**: use **Controls** (`Run`, `Driver`, `Queued for the next run`, `Room`) according to [what each standing may do](reference/web-ui-settings.md#what-each-standing-may-do), or pick **fork here** on any message or handoff ([forks](reference/web-ui-settings.md#forks)).
- **Send from the dock**: choose `Deliver to` (post to the room, queue for the next run's brief, or steer the running agent — driver only), toggle the [theme](reference/web-ui-settings.md#theme), and watch connection notices. Destructive acts (**interrupt**, **take**, **steer now**, **close room**) require a second `confirm: …` click within 10 s.

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

Every dock refusal code and recovery action is listed in [Action refusal reasons](reference/web-ui-settings.md#action-refusal-reasons).
