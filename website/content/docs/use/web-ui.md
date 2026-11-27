---
title: "Web UI"
weight: 30
description: "The web UI is the place to watch rooms, steer runs, decide approvals, fork, and manage upkeep routines. It is embedded in the broker behind oauth2-proxy; maku handles terminal."
diataxis: how-to
aliases:
  - /docs/web-ui/
---

The web UI is the place to watch rooms, steer runs, decide approvals, fork, and manage upkeep routines. It is embedded in the broker behind oauth2-proxy; [maku]({{< relref "/docs/use/cli.md" >}}) handles terminal reads, while steering and approvals stay here. See [Concepts]({{< relref "/docs/concepts/glossary.md" >}}) for room vocabulary and [Web UI reference and settings]({{< relref "/docs/reference/web-ui-settings.md" >}}) for standing matrices, `/settings`, `/routines`, forks, themes and refusal codes.

## Signing in

| Step | What happens |
|---|---|
| Open `https://rooms.<domain>` | Browser reaches the broker ([Exposure]({{< relref "/docs/install/exposure.md" >}})) |
| oauth2-proxy | Signs you in via your identity provider (`SameSite=Strict` cookie) |
| Broker group check | `agents-member` reads permitted rooms; `agents-admin` owns and approves every room |
| Expired session | A `401` redirects back through `/oauth2/start` |

- **Neither group (`403`):** ask an operator to add you to `agents-member` ([Integration]({{< relref "/docs/install/integration.md" >}})).
- **Visibility follows GitHub:** members see rooms whose repositories their [linked GitHub account]({{< relref "/docs/use/github-link.md" >}}) can read (unlinked accounts see an empty list; unreadable rooms answer `404`). See [maku troubleshooting]({{< relref "/docs/use/cli.md#troubleshooting" >}}).

Sources: `docs/api.md`, `internal/policy/policy.go`.

## The room list

Readable rooms appear in one table, `Needs you` first, then newest activity. Unsafe install settings show an alert banner above the list ([unsafe codes]({{< relref "/docs/reference/web-ui-settings.md#install-banner-unsafe-codes" >}})).

<img alt="The room list: factory rooms from two repositories, the ones that need you first, each with its repository, status line, PR, last activity and room id" src="/images/web-ui/rooms-light.png" width="800">

Columns show `Needs you`, `Task`, `Repository`, `Status`, `PR`, `Last activity` and `Room` ([room list columns]({{< relref "/docs/reference/web-ui-settings.md#room-list-columns" >}})). Click a row to open it, a header to sort, or **Search** to filter ([sorting and search]({{< relref "/docs/reference/web-ui-settings.md#sorting-and-searching-the-room-list" >}})). Auto-refresh defaults to 10 s (`Off`–`1 min`, slowing to 1 min in background tabs).

Filter buttons refetch from the API ([API]({{< relref "/docs/reference/api.md#8080--human-api" >}})): **mine**, **needs me** (`question`, `plan`, `review`, `blocked`, `failed`), a repository picker, and **closed** (`closed=1`, last 30 days).

### New task and Investigate with an agent

**New task** and **⋯ More** › **Investigate with an agent** open forms above the list:

- **New task** plans through `/api/handoffs` (prompt, any served repository or Auto, optional **Plan first**, maintainer workflow pin) and shows `Outcome`, `Workflow`, `Will run`, `Cost`, any planner question, and the drafted issue. Maintainers with write access get **Start**; the factory runs only from the broker's start record ([broker-confirmed handoffs]({{< relref "/docs/concepts/security.md#authorization" >}})).
- **Investigate with an agent** opens an interactive room tied to no issue.

| Field | Rule |
|---|---|
| Data class | `internal` or `public`, no default; fixed once created |
| Repository | `owner/name`, required; readable by GitHub collaborators |

The `Use maku from your terminal` fold gives copyable [maku]({{< relref "/docs/use/cli.md" >}}) setup commands.

<img alt="The Use maku from your terminal fold: three numbered steps, install, point it at this broker and sign in, each in a code block with a Copy button" src="/images/web-ui/cli-setup-light.png" width="800">

## The room page

<img alt="A room page during a live review: the run chip reads reviewer, its id, the model agent-default, the effort high and its age, then the budget, PR, issue, Dashboards and the Now line; the factory brief folded under Show more; the dock" src="/images/web-ui/room-light.png" width="800">

Work a room top to bottom ([component reference]({{< relref "/docs/reference/web-ui-settings.md#room-page-components" >}})):

- **Title strip and workflow card:** Tier 1 shows the workflow chip, status, active run, and `Needs you · N`; Tier 2 shows spend, PR/issue links, verdict, data class, driver, `Dashboards`, `Next`, and `Now`. Two-press **Stop** optionally discards the PR, branch, and issue. **Workflow** shows `Workflow`, `Will run`, `Cost`, and refused pins.
- **Clear `Needs you`:** decide approvals inline, and act on `question` (**Answer**), `plan_confirmation` (**Accept the split** / **Run it whole**), `plan_review` (**Approve** / **Revise** / **Split**), and `recovery` (**Retry** / **Run again deeper**) cards when `human.roomActs` enables them. Cards unchanged after 30 s warn to check the room log.
- **Inspect context:** expand `Notes`, `How it was chosen`, `Merge policy`, and `Raw events`, or click any **Rail** node (`#run=<id>`) to filter events to that run.
- **Steer or fork:** use **Controls** (`Run`, `Driver`, `Queued for the next run`, `Room`) per [standing permissions]({{< relref "/docs/reference/web-ui-settings.md#what-each-standing-may-do" >}}), or **fork here** on any message ([forks]({{< relref "/docs/reference/web-ui-settings.md#forks" >}})).
- **Dock:** choose `Deliver to` (room chat, queue for next run, or steer — driver only), toggle [theme]({{< relref "/docs/reference/web-ui-settings.md#theme" >}}), and confirm destructive acts (**Stop**, **interrupt**, **take**, **steer now**, **close room**) within 10 s.

Scrolling a finished factory room, from the strip down through its turns and tool calls:

<img alt="Scrolling down a factory room's conversation: the triager's scope, folded tool calls with their status, state changes folded into turns" src="/images/web-ui/room-scroll.gif" width="800">

A factory brief folded under `Show more`, with the `Dashboards` chip at the end of the strip:

{{< themed-img light="/images/web-ui/room-fold-light.png" dark="/images/web-ui/room-fold-dark.png" alt="A factory room whose task brief shows its first lines under a fade and a Show more link; the strip ends with a Dashboards chip linking Room metrics and Run" >}}

On a phone the strip stays compact above the conversation:

<img alt="The same room on a phone: the status line first, the title and the waiting lines clamped to two lines" src="/images/web-ui/room-phone-light.png" width="300">

## Approvals

Gated actions (`forge.push`, `forge.pr`, `forge.other`, `mcp.write`, `shell.high`, `egress.new`) open a `Needs you` card with fixed rule prose, deadline, and redacted `details` ([approval card and rule classes]({{< relref "/docs/reference/web-ui-settings.md#approval-card-and-rule-classes" >}})).

1. Check your standing (`You can decide`, `only approvers decide`, or `Waiting for another approver` under four-eyes).
2. Inspect `details` rather than agent chat prose.
3. Click **approve** or **deny** (optional reason ≤ 1 KiB). First valid decision wins; expired requests deny automatically (`30 min` attended, `4 h` unattended).

## Routines

Open **Routines** (`/routines`) to monitor and configure scheduled repository maintenance ([Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}}), [Routines reference]({{< relref "/docs/reference/web-ui-settings.md#routines" >}})):

{{< themed-img light="/images/web-ui/routines-overview-1280-light.png" dark="/images/web-ui/routines-overview-1280-dark.png" alt="Routines overview showing bound routines, 14-day outcome sparklines, merge rate, cost per merged PR, open PR counts, and maintainer controls" >}}

1. **Overview (`/routines`):** filter by repository, state, or mode; inspect 14-day sparklines, merge rate, `$/merged`, next run, and breaker state (`ok`, `half_open`, `paused`); click **Run now**, **Pause**, or **Resume**.
2. **Routine catalog (`/routines/catalog`):** browse shipped and custom `Routine` definitions, pick a target repository, and click **Enable** or **Configure**.
3. **Guided configurator (`/routines/<owner>/<repo>/<name>/edit`):** pick a delivery mode (`propose`, `draft`, `pr`, `disabled`), choose a UTC cron preset with live local/UTC preview, tighten bounds inside `Routine` ceilings, validate a repo prompt (`.github/agent-factory/upkeep/<name>.md`), and run **Check & Generate YAML** (or **Save to cluster** when `routines.uiWrite: true`).
4. **Detail & Run History (`/routines/<owner>/<repo>/<name>`):** inspect recent tasks, prompt SHA changes, and skip diagnostics.

## Settings and environment

Open **Settings** (`/settings`) for section cards (**Merge policies**, **Evidence**, **Approval memory**) and the **Environment** panel below them, showing broker and factory versions (flagging any mismatch), factory name, namespace, sign-in mode, default harness, merge policy stage, hardening mode, routines UI write setting, and install notices ([Settings reference]({{< relref "/docs/reference/web-ui-settings.md#settings" >}})).

{{< themed-img light="/images/web-ui/settings-env-1280-light.png" dark="/images/web-ui/settings-env-1280-dark.png" alt="Settings home showing the Merge policies, Evidence, and Approval memory cards above the Environment panel with broker and factory versions and cluster settings" >}}

## When something fails

Refused actions show a reason code in the dock ([all refusal reasons]({{< relref "/docs/reference/web-ui-settings.md#action-refusal-reasons" >}})):

- **`not_permitted` / `four_eyes` / `access_unverified`:** check [standings]({{< relref "/docs/reference/web-ui-settings.md#what-each-standing-may-do" >}}), ask another approver, or ask a maintainer for protected-path pushes.
- **`stale_epoch` / `no_running_run` / `room_busy`:** refresh the driver holder, queue for the next run, or wait for a starting run.
- **Page and room errors:** `404` means missing or unreadable on GitHub; room creation returns `400` (bad class/repo), `404` (unreadable repo), or `503` (GitHub check failed).
