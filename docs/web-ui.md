<!-- page-type: how-to -->

# Web UI

The web UI is the place to watch rooms, steer runs, decide approvals, fork, and manage upkeep routines. It is embedded in the broker behind oauth2-proxy; [maku](cli.md) handles terminal reads, while steering and approvals stay here. See [Concepts](concepts.md) for room vocabulary and [Web UI reference and settings](reference/web-ui-settings.md) for standing matrices, `/settings`, `/routines`, forks, themes and refusal codes.

## Signing in

| Step | What happens |
|---|---|
| Open `https://rooms.<domain>` | Browser reaches the broker ([Exposure](exposure.md)) |
| oauth2-proxy | Signs you in via your identity provider (`SameSite=Strict` cookie) |
| Broker group check | `agents-member` reads permitted rooms; `agents-admin` owns and approves every room |
| Expired session | A `401` redirects back through `/oauth2/start` |

- **Neither group (`403`):** ask an operator to add you to `agents-member` ([Integration](integration.md)).
- **Visibility follows GitHub:** members see rooms whose repositories their [linked GitHub account](github-link.md) can read (unlinked accounts see an empty list; unreadable rooms answer `404`). See [maku troubleshooting](cli.md#troubleshooting).

Sources: `docs/api.md`, `internal/policy/policy.go`.

## The room list

Readable rooms appear in one table, `Needs you` first, then newest activity. Unsafe install settings show an alert banner above the list ([unsafe codes](reference/web-ui-settings.md#install-banner-unsafe-codes)).

<img alt="The room list: factory rooms from two repositories, the ones that need you first, each with its repository, status line, PR, last activity and room id" src="assets/web-ui/rooms-light.png" width="800">

Columns show `Needs you`, `Task`, `Repository`, `Status`, `PR`, `Last activity` and `Room` ([room list columns](reference/web-ui-settings.md#room-list-columns)). Click a row to open it, a header to sort, or **Search** to filter ([sorting and search](reference/web-ui-settings.md#sorting-and-searching-the-room-list)). Auto-refresh defaults to 10 s (`Off`–`1 min`, slowing to 1 min in background tabs).

Filter buttons refetch from the API ([API](reference/api.md#8080--human-api)): **mine**, **needs me** (`question`, `plan`, `review`, `blocked`, `failed`), a repository picker, and **closed** (`closed=1`, last 30 days).

### New task and Investigate with an agent

**New task** and **⋯ More** › **Investigate with an agent** open forms above the list:

- **New task** plans through `/api/handoffs` (prompt, any served repository or Auto, optional **Plan first**, maintainer workflow pin) and shows `Outcome`, `Workflow`, `Will run`, `Cost`, any planner question, and the drafted issue. Maintainers with write access get **Start**; the factory runs only from the broker's start record ([broker-confirmed handoffs](security.md#authorization)).
- **Investigate with an agent** opens an interactive room tied to no issue.

| Field | Rule |
|---|---|
| Data class | `internal` or `public`, no default; fixed once created |
| Repository | `owner/name`, required; readable by GitHub collaborators |

The `Use maku from your terminal` fold gives copyable [maku](cli.md) setup commands.

<img alt="The Use maku from your terminal fold: three numbered steps, install, point it at this broker and sign in, each in a code block with a Copy button" src="assets/web-ui/cli-setup-light.png" width="800">

## The room page

<img alt="A room page during a live review: the run chip reads reviewer, its id, the model agent-default, the effort high and its age, then the budget, PR, issue, Dashboards and the Now line; the factory brief folded under Show more; the dock" src="assets/room-light.png" width="800">

Work a room top to bottom ([component reference](reference/web-ui-settings.md#room-page-components)):

- **Title strip and workflow card:** Tier 1 shows the workflow chip, status, active run, and `Needs you · N`; Tier 2 shows spend, PR/issue links, verdict, data class, driver, `Dashboards`, `Next`, and `Now`. Two-press **Stop** optionally discards the PR, branch, and issue. **Workflow** shows `Workflow`, `Will run`, `Cost`, and refused pins.
- **Clear `Needs you`:** decide approvals inline, and act on `question` (**Answer**), `plan_confirmation` (**Accept the split** / **Run it whole**), `plan_review` (**Approve** / **Revise** / **Split**), and `recovery` (**Retry** / **Run again deeper**) cards when `human.roomActs` enables them. Cards unchanged after 30 s warn to check the room log.
- **Inspect context:** expand `Notes`, `How it was chosen`, `Merge policy`, and `Raw events`, or click any **Rail** node (`#run=<id>`) to filter events to that run.
- **Steer or fork:** use **Controls** (`Run`, `Driver`, `Queued for the next run`, `Room`) per [standing permissions](reference/web-ui-settings.md#what-each-standing-may-do), or **fork here** on any message ([forks](reference/web-ui-settings.md#forks)).
- **Dock:** choose `Deliver to` (room chat, queue for next run, or steer — driver only), toggle [theme](reference/web-ui-settings.md#theme), and confirm destructive acts (**Stop**, **interrupt**, **take**, **steer now**, **close room**) within 10 s.

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

Gated actions (`forge.push`, `forge.pr`, `forge.other`, `mcp.write`, `shell.high`, `egress.new`) open a `Needs you` card with fixed rule prose, deadline, and redacted `details` ([approval card and rule classes](reference/web-ui-settings.md#approval-card-and-rule-classes)).

1. Check your standing (`You can decide`, `only approvers decide`, or `Waiting for another approver` under four-eyes).
2. Inspect `details` rather than agent chat prose.
3. Click **approve** or **deny** (optional reason ≤ 1 KiB). First valid decision wins; expired requests deny automatically (`30 min` attended, `4 h` unattended).

## Routines

Open **Routines** (`/routines`) to monitor and configure upkeep routines ([Routines reference](reference/web-ui-settings.md#routines)):

1. **Overview (`/routines`):** filter by repository, inspect 14-day sparklines, merge rate, `$/merged`, next run, and breaker state (`ok`, `half_open`, `paused`), and click **Run now**, **Pause**, or **Resume**.
2. **Routine Gallery (`/routines/catalog`):** browse shipped and custom `Routine` definitions, pick a target repository, and click **Enable** or **Configure**.
3. **Guided Configurator (`/routines/<owner>/<repo>/<name>/edit`):** pick a delivery mode (`propose`, `draft`, `pr`, `disabled`), choose a UTC cron preset with live local/UTC next-3-runs preview, tighten bounds inside `Routine` ceilings, validate a repo prompt (`.github/agent-factory/upkeep/<name>.md`) or open its GitHub edit link, run **Check & Generate YAML**, and copy/download `repository-<name>.yaml` — or click **Save to cluster** when `routines.uiWrite: true`.
4. **Detail & Run History (`/routines/<owner>/<repo>/<name>`):** inspect recent tasks, prompt SHA changes, and skip diagnostics.

## When something fails

Refused actions show a reason code in the dock ([all refusal reasons](reference/web-ui-settings.md#action-refusal-reasons)):

- **`not_permitted` / `four_eyes` / `access_unverified`:** check [standings](reference/web-ui-settings.md#what-each-standing-may-do), ask another approver, or ask a maintainer for protected-path pushes.
- **`stale_epoch` / `no_running_run` / `room_busy`:** refresh the driver holder, queue for the next run, or wait for a starting run.
- **Page and room errors:** `404` means missing or unreadable on GitHub; room creation returns `400` (bad class/repo), `404` (unreadable repo), or `503` (GitHub check failed).
