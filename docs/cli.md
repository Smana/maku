# maku

`maku` is a room in a terminal: read where a task stands, follow the log, post a note, fork a room. It never steers a run, interrupts it, moves the driver token or approves anything (`status` may print an approve command, for display only); those stay in the [web UI](web-ui.md). On a local install (the quickstart) `run` also starts one.

It calls the broker through the platform's sign-in proxy with your own access token; the broker filters what it returns, so it sees only the rooms your GitHub identity may read.

## Install and verify

Download the binary for your system from the latest GitHub release, with `maku.sha256`:

| Asset | System |
|---|---|
| `maku-linux-amd64`, `maku-linux-arm64` | Linux |
| `maku-darwin-amd64`, `maku-darwin-arm64` | macOS |
| `maku.sha256` | the checksums of all four |

```bash
sha256sum -c --ignore-missing maku.sha256
sudo install maku-linux-amd64 /usr/local/bin/maku
```

From a checkout: `go build -o maku ./cmd/maku`. The release workflow is `.github/workflows/release.yaml`.

## Configure and sign in

```bash
maku configure --url https://rooms.<domain>   # the broker serves the other three values
maku login                                    # device flow: open the printed URL, enter the code
maku doctor                                   # check the setup
```

- `configure` reads the issuer, client id and project id from the broker's unauthenticated `/.well-known/roomctl`. Pass `--issuer`, `--client-id` or `--project-id` to override one. The URL must be `https`, except for a local install (`humanMode: local`), whose listener is the `kubectl port-forward`: `http://localhost:<port>` is accepted on `localhost` or a loopback address only, and anything else in clear is refused before any request.
- `configure --from <rooms URL>` replaces the whole config with what the broker serves now (a room page's URL works: only its origin is used). Use it after a platform rebuild: a stale client id makes the identity provider answer `invalid_client`, and `maku login` then prints this fix. It refuses a non-`https` URL or issuer — a local install configures with `--url http://localhost:<port>` instead — and cannot be combined with the other flags. It prints the four values it will write (and the old ones when they change) and, on a terminal, asks `y/N`; `--yes` skips the question. An issuer outside the URL's registrable domain (`rooms.ogenki.io` and `auth.ogenki.io` share one) is refused unless `--yes` says you checked it.
- A local broker needs no `login` and takes no token: `login` says so, `token` says so, and no request carries an `Authorization` header.
- The config and token files sit in `<user config dir>/maku/` (`config.json`, `token.json`), owner-only. The directory a CLI before v0.30.0 wrote (formerly `roomctl/`, beside it) is copied there once, the first time `maku` runs without one; the old directory stays.
- `login` prints `Open <verification URL> and enter the code …` (the URL is the one your identity provider returns), then `logged in; …`.
- `doctor` prints one line per check, `ok` or the failure with its fix: `dns`, `tls`, `login`, `github`, `skill`, and `factory capabilities`. The room list cannot tell an admin without a GitHub link from one with it, so `github` asks `issue_draft` (the check `issue_file` relies on when it is given no plan) (on up to three of your rooms' repositories) and prints a `warn` line, which does not fail the run, when none can answer: it never says "linked" unproven. A broker that stated the link in the room list would make this probe unnecessary; the probe stays as the fallback for brokers that do not. `factory capabilities` reports what a coding client declared when it opened `maku mcp`, from `capabilities.json` beside the config: whether it answers with forms, with URL mode, with its roots and with prompts, and the protocol revision it asked for, with the day maku measured it. `--client <name>` names one client; a name no session ever gave reads `not measured yet — run a session and ask again`, and nothing else is printed about it. Without the flag, the five clients measured most recently are listed, the last one first. It reads a file, so it answers with the VPN down or the URL wrong, and it never fills a client maku has not met from a guess.

## Commands

`maku --help` prints the list below. A `<room>` is a room id, the last part of a room page's `…/r/<room>` URL. The MCP tools and prompts also take `#<issue>`, `PR #<n>`, a bare number or a title fragment ([below](#the-mcp-server)); the CLI takes ids only, since a shell drops an unquoted `#42`.

| Command | What it does |
|---|---|
| `configure --url URL [--issuer URL --client-id ID --project-id ID]` | Saves the config. The broker's URL is enough |
| `configure --from URL [--yes]` | Replaces the config with the values the broker serves now, whatever is stored |
| `login` | Signs in with the device flow |
| `setup <claude\|codex\|copilot\|cursor\|gemini\|opencode> [--project] [--print]` | Registers `maku mcp` as the `factory` MCP server of a coding agent and writes the factory-handoff skill and, for `claude` and `opencode`, the short `/factory` command ([below](#short-commands)). `--project` writes under the repository's top instead of your home; `--print` shows what it would do and writes nothing |
| `demo app --dir DIR [--owner ORG] [--name NAME]` | The laptop demo's GitHub App, through GitHub's App manifest flow. Serves a page on `127.0.0.1` that posts the manifest (seven permissions, `actions: write` among them so a cancelled `ci` can be re-run, no webhook events, private) to GitHub, takes the callback once its `state` matches, converts the code and writes `app_id`, `private_key` (mode 0600), `slug` and `html_url` into DIR (mode 0700), each file is written through a temporary file and renamed into place (a symlink there is replaced, not followed), `html_url` last: its presence means the set is complete. It then prints the link that installs the App on your repository. The callback works once and only for the `127.0.0.1` host. It never prints the key and gives up after five minutes. `--owner` creates the App in an organization |
| `doctor [--client <name>]` | Checks the VPN, the CA, your login, your GitHub link and the installed skill's version (`skill`), with the fix of each, then `factory capabilities`: what that coding client declared when it opened `maku mcp` — forms, URL mode, roots, prompts, the protocol revision it asked for and the day it was measured — or `not measured yet` for a client no session gave. Without `--client`, the five clients measured most recently |
| `token` | Prints a valid access token, for scripts. Treat it as a secret |
| `run --repo owner/name --task TEXT [--role implementer\|triager]` | Quickstart only, on a local broker ([quickstart](quickstart.md)): creates a room, queues TEXT for the run's brief, starts the run, applies the claim the broker rendered with your own kubectl and kubeconfig, and follows the room until the run ends. Exit 0 only when the run ended Succeeded. It stops at the first failing step and names it: room, task, start_run (with the broker's reason), claim (kubectl's own error), run. On any other broker it refuses: the factory starts runs there |
| `status <room> [--json] [--after seq:N]` | Where a room stands. It leads with the [brief](api.md#get-apiroomsidbrief)'s head — the `Resume:` line, what was asked, the plan, what changed in the last 24 hours, what blocks it and the one `Next:` step — then the room card (below): the title, the state, the facts (run, spend, PR and last verdict, next, why), what needs you, the runs line, the agents' notes and what you can do, then a `cursor: seq:N` line. The head is a read of its own and takes no cursor: it is always the brief's default window, the last day, whatever `--after` asks of the notes. `--json` prints the broker's summary as is; `--after` shows only the notes after a cursor (the output prints it as `seq:N`) |
| `rooms [--repo owner/name] [--mine] [--needs-me] [--all] [--json]` | The rooms you can read, as a table that leads with the task and ends with the room id: TASK (`#issue title`), REPO (when several), STATE, WHERE IT IS, PR, UPDATED, ROOM. `--mine`: tasks of issues you filed or labelled, PRs you authored or review. `--needs-me`: an approval you could decide, a task held on your room, a PR waiting for your merge, or an Escalated task waiting for its issue's author when you own the room. `--all`: the rooms that ended over a week ago too. `--json`: the broker's rows as is |
| `needs [--since RFC3339] [--room id] [--repo owner/name] [--format md\|text] [--json]` | What needs you across every room you can read, in one order: the kind, what it asks, the one action and where it happens. It decides nothing: each item's action is a link — a merge on GitHub, an approval on the room page. `--since`: only what moved after that instant, RFC 3339 as the answer's own cursor is; anything else is refused before any request, since it is neither everything nor nothing. Without it, the cursor the last call stored in `cursors.json`, which each call moves on. `--room` and `--repo` narrow the list; `--json` is the broker's answer as is. An empty list says so in one line, and a room whose state could not be read is named rather than passed off as quiet. Needs a broker whose `human.needs.enabled` serves `needs_list` |
| `changes <room> [--cursor seq:N] [--format md\|text] [--json]` | What changed in a room since the cursor of your last call (the first call: 24 hours). The cursor is kept in `cursors.json` beside the config. Needs a broker with `room_changes` |
| `checkout <room>` | Checks the room's branch, `agent/<room>`, out into a new git worktree beside the repository you are in, `<repo>.<room>`, and prints its path. It never touches your current checkout, so a dirty tree or your branch is safe, and a second call prints the same path. It refuses outside a checkout of the room's repository, and prints the `gh repo clone` command. When the branch changes files that run code on open or on commit, it lists them and asks `y/N`, and refuses without a terminal ([below](#checking-out-an-agents-branch)) |
| `coordination checkout <parent> [--workspace owner/name=path]...` | Checks every child of a coordination parent out, each in a detached worktree at the exact commit the parent names (an open PR's head, or a landed merge on the default branch), under `../<repo>.<parent>/<child>`, and writes `maku-coordination.json` there last. The current directory is the primary's clone; `--workspace` names each other repository's clone, and a missing one refuses with its `gh repo clone` command. It never clones, creates or moves a branch, or touches a clone's checkout. A moved head, a merge no longer on the default branch or a changed plan refuses as `checkout_stale`; a second identical call reuses the worktrees; a root left incomplete is reported, never repaired. It asks `y/N` once for every child's files that run code |
| `watch <room> [--tail N]` | Follows a room, one line per event: its `seq`, who (`role (id)` for a participant with a room role), what, and its text. Shows the last `N` events first (default 50). It reconnects after a drop and picks up where it stopped |
| `post <room> [--queue] <text>` | Posts a chat message and prints its `seq`. With `--queue` the text is kept for the next run's brief instead. The broker stores it labelled as [machine-sent](api.md#machine-sent-text), as any text from maku |
| `skill install [--dir .agents/skills]` | Writes the factory-handoff skill. Needs no config or login |
| `mcp` | Serves the factory's MCP tools on stdio, for a local coding agent ([below](#the-mcp-server)) |
| `fork <room> --at SEQ [--role R] [--pr URL] [--egress pypi,npm] [--note TEXT] [--approvals attended\|unattended]` | A room of your own from events 1 to `SEQ`, with an optional run. `--role`: `implementer`, `reviewer` or `triager`; `--pr`: the reviewer's pull request; `--egress`: extra profiles (`pypi`, `npm`, `golang`, `crates`); `--note`: why you fork, under 1 KiB once labelled as machine-sent (about 900 bytes); `--approvals`: `attended` by default, so the fork asks before risky actions even when the source is an unattended factory room, `unattended` keeps an unattended source's policy (an attended source stays attended). Prints the new room's URL |

Flags may come before or after the positional arguments. Source: `internal/app/maku.go`.

`status` leads with the brief's head, prints the room card, then the cursor. For a task's room:

```text
**Resume: 26zfnuxm · #42 docs: chart version is wrong**
Asked: #42, chart version is wrong
Plan: scope ready · budget $1.50 ceiling
Changed since 2026-11-04 18:20 UTC (16 h):
Blocked on: `approval` — "git push to agent/26zfnuxm" waits for you
Next: approve it on the room page → https://rooms.example/r/26zfnuxm#01M4A

#42 docs: chart version is wrong · 26zfnuxm
needs you · an agent is coding · Smana/cloud-native-ref · open the room (https://rooms.example/r/26zfnuxm)

Run   implementer cf4ato2x (human review) · started 19:11 UTC, 18 min ago
Spend $0.31 of $1.50 (21 %) · 189 k tokens
PR    #2239 (https://github.com/Smana/cloud-native-ref/pull/2239) · reviewer approved at 18:49 UTC

Needs you
1. approve "git push to agent/26zfnuxm" by 19:45 UTC → https://rooms.example/r/26zfnuxm#01M4A

Runs implementer (first run) succeeded · 40 min · $0.18 → reviewer approved · 10 min · $0.03 → implementer (human review) running · 18 min · $0.10

Agents' notes, their claims, not checked
- 19:12 UTC cf4ato2x "found both versions on line 12-13, fixing"

You can queue a note for the next run · steer the running agent · stop it with the label factory/stop
cursor: seq:7
```

Each line is left out when its data is absent: a room with no task has no facts table, a sealed
room no `Needs you` or `You can`. The spend line reads dollars when the factory priced the task
and tokens (`182 k of 1.5 M tokens (12 %)`) when it did not; token counts read like the room
page's, under 1,000 as is, then `k`, then `M`. Times are UTC, dated when not today. A run that
ended reads `succeeded`, `failed` and so on, and a stopped task's unfinished run reads `ended`,
never `running`. On a terminal, with `NO_COLOR` unset, the state word is bold when it needs you.
The agents' text is quoted; an approval is a link to its card on the room page, never a command.

Room links print only under the broker's own address: the card takes it from the summary's URL
(the broker's `PublicURL`), and falls back to the URL `maku` is configured with when the summary
carries none. A broker reached by another name (a port-forward) therefore still shows its links.
Source: `internal/roomview/card.go`.

The head above the card is the first six section lines of the room's
[brief](api.md#get-apiroomsidbrief), taken as they are: the changed block's items, the acceptance
checks and the links stay in the brief an agent reads in full, since the card already carries what
needs you. A broker that serves no `room_brief` prints the card alone, as before; a brief that fails
for another reason says so in one line on stderr, because the card is still what you asked for.
Source: `internal/maku/status.go`.

## The MCP server

`maku mcp` is an MCP server on stdio. It relays each message to the broker's
[`/mcp`](api.md#post-mcp) with your token, so your agent configures one command and no token is
pasted:

```bash
claude mcp add factory -- maku mcp    # Claude Code; any agent that runs a stdio MCP server works
```

- The broker's operator turns `/mcp` on (`human.mcp.enabled`). Off, every call answers `mcp_off`.
- The tools: `rooms_list`, `room_summary`, `room_brief`, `room_tail`, `room_changes`, `needs_list`,
  `room_queue`, `room_fork` and `issue_draft` from the broker; `intake_plan` and `plan_check` too, when
  the broker's operator has turned the planner on ([below](#the-plan)); and `issue_file`, `room_wait`
  and `room_checkout`, maku's own. `room_tail` also runs as an MCP Task that waits for the room's next
  events; `maku mcp` polls it for your agent. The broker also serves the prompts `handoff` (`what`),
  `plan` (`what`), `status` (`room`), `resume` (`room`) and `inbox`, which your agent offers as slash
  commands (see [Short commands](#short-commands) for how to type them).
- `resume` is the one to start a session with: it calls `needs_list` once, then `room_brief` of the
  first room that needs you — or of the room you passed — and names the brief's `Next:` line as the
  one step. `inbox` is one `needs_list` call: when the list is empty it says so in one line and names
  `rooms_list` with `mine` instead of asking the broker twice.
- `issue_file` files a draft with your `gh`, as you, only after you accept it in a prompt your agent
  shows you (MCP elicitation). Pass it a plan and the hash `intake_plan` answered with, and the
  broker's `plan_check` re-validates both before the prompt is built: the prompt then shows the
  plan's own issue and its ceiling, and a plan edited after it was shown is refused (`plan_changed`)
  instead of filed. A call with no plan is checked by the broker's `issue_draft`, as it always was;
  either way the prompt shows exactly what is filed. An agent that cannot prompt you is refused: file the draft yourself
  with `gh issue create`. The form's "file" toggle defaults to no (so Claude Code shows it set, not "required"), and a second field, "start", defaults to no too and appears
  only when `plan_check` (with a plan) or `issue_draft` (without one) says `canStart`: yes labels the issue `factory/ready` with
  your `gh`, which starts the factory. A client that returns the form without `start` (or with a value that is not a boolean) gets `started:false` and a `next` that says start was not chosen, and `maku mcp` logs it on stderr: your agent then asks you rather than guessing. It is a form field, never a tool argument: your agent can
  call `issue_file` but cannot answer the form.
- Every tool and prompt that takes a room (`room_summary`, `room_brief`, `room_changes`, `room_queue`,
  `room_tail`, `room_fork`, `room_wait`, `room_checkout`, the `status` and `resume` prompts) accepts
  the room id, `#<issue>`
  (the issue only), `PR #<n>` (the PR only), a bare number (an issue or a PR), or a fragment of the
  task's title (3 characters or more, case-insensitive; not an id or a number). `maku mcp` resolves
  it through the room list, open rooms first and then the ended ones. When several rooms match (the
  same number in two repositories, a bare number that is an issue in one room and a PR in another,
  a fragment in several titles), the tool lists them with their ids and asks, and calls nothing: your
  agent asks you. Text that matches nothing goes to the broker as typed, which refuses it. Cards and
  the overview lead with the issue number and title, and end with the room id.
- `room_wait` polls `rooms_list` from your machine until a room needs you, opens a pull request or
  ends, or until its timeout (300 s by default, at most 1800 s), and returns the room's card. A
  cancellation ends it at once. Run it in a background task where your client has them.
- `room_checkout {room}` is `maku checkout`, for your agent. The repository and the pull request
  come from the broker's row of the room, and the workspace is your client's first root, else
  where `maku mcp` runs.
- `coordination_checkout {parent, workspaces?}` is `maku coordination checkout`, for your agent: the
  family comes from the broker's checkout document, the workspace is your client's first root, and
  one form asks about every child's files that run code. It confirms, files, merges and recovers
  nothing.
- No tool approves, steers or stops, and none labels on the agent's say: only your answer to the start form does. Approvals stay on the room page.
- What your agent queues is stored labelled as [machine-sent](api.md#machine-sent-text).
- Every request reads `token.json` and refreshes the token when it has expired, one refresh at a
  time. When the refresh is refused, a call answers `not_logged_in`: run `maku login` in a
  terminal, and the running server uses the new token, with no restart.

The [factory-handoff skill](#the-factory-handoff-skill) does the same jobs through the shell, for an
agent with no MCP configuration. Configure one of the two.

### The plan

`intake_plan` and `plan_check` are the broker's governed planner: your own sentence about what is
wrong becomes a plan of factory work, and the plan's hash is what every door after it re-validates
before anything is filed. The broker's operator turns them on (`human.intake.plan.enabled`); off,
both are unknown tools, as `run_create` is to your token, and the skill's switch-off route takes
over.

| Tool | What it does | What it answers |
|---|---|---|
| `intake_plan` | Plans the factory work a sentence asks for, from `text` (your own words), `repository` or `candidates` (owner/name), the `context` your agent has read and the `evidence` you passed it | `plan/v1`, the hash the broker computed over it, at most one question to put to you, and `canStart`: whether you may start the factory on it. One model call, on public text only; it files nothing |
| `plan_check` | Re-validates a plan you were shown, against the hash the broker answered with | The plan as it was validated, its hash, and `canStart`. It calls no model, spends no daily plan and files nothing |

`issue_file` calls `plan_check` itself, so you never call it by hand: it is there so an agent can
re-check a plan before showing you a form for it, and so a plan that changed after it was shown
files nothing rather than something else.

What a plan shows, as `intake_plan` answers and your agent shows it back:

````text
**Plan** · Smana/maku · `sha256:1c99586a84c6b989c46ff70880ee92f709be6db398101b25241fca552fd8363f`
Ceiling $2.00 · class:docs
Planner tier-frontier on session · 09:12 UTC

1. `docs: the install link in the coding-agent guide 404s` · class docs · roles scoper, implementer, reviewer

Issue 1
```
## What is wrong
`docs/cli.md` links to
`https://github.com/Smana/maku/releases/latest/download/maku-linux`, which 404s since the
assets were renamed.

## What it should be
The link points at the release asset the page describes, `maku-linux-amd64`.

## Acceptance check
The page's link resolves, and `hack/docs.sh` passes.
```

Question `Should the link point at the latest release, or at a pinned version?`
Recommended `the latest release, as the rest of the page does` · `the page already reads as a live guide`

Why this repository
- `docs/cli.md is the page that carries the install section`
- `nothing else in the repository names that asset`
````

Its sections, in the order they are read:

- **The repository, and its evidence.** The head line names the one repository the plan files into
  and the plan's hash. `Why this repository` is the planner's own reading — a path, a thread, a
  channel — one quoted item per line and never a link: the links are the broker's to build, never
  the model's. A repository outside the ones this request may use refuses the plan whole, and is
  not named back.
- **The issue.** One numbered line per issue (one to three), then `Issue 1`, `Issue 2` … with each
  draft as it will be filed. One call files the **first**: with a longer plan, say the others are
  not filed.
- **The roles.** `roles scoper, implementer, reviewer` and `class docs` on the issue's line, from
  the class's template in the broker's config, never from the model: a role the reply names is
  dropped and logged. `waits for #1` says an issue comes after an earlier one.
- **The acceptance check.** `## Acceptance check`, the third heading inside the issue's block, with
  `## What is wrong` and `## What it should be` before it: the factory reads the same three sections
  on a hand-written issue, and a draft without them is refused before a plan exists.
- **The ceiling.** `Ceiling $2.00 · class:docs`: the money this plan's work is charged against and
  where that number came from, both from the broker's config. The roles and the money are the
  config's whatever the reply says, and a class the config does not define makes no plan.

A plan may carry one question, with the broker's recommendation and why. It is the one thing the
planner needs from you to plan the rest of the work, and the answer is yours. A plan marked
`degraded` came from the broker's deterministic template rather than the model
(`human.intake.plan.fallback: composer`): it says so in its first line, and it is not the
natural-language planner.

Each attempt costs one of your day's plans (`human.intake.plan.dailyPlans` per requester, reset at
00:00 UTC) and one model call: there is no retry loop. Neither tool files, labels, stores or
approves anything: the plan is shown, and `issue_file` files its first issue once you answer its
form.

The refusals you can meet, with the next step. The broker answers a plan or a check it will not make
with one reason of a closed list, each with its own sentence; the whole list is
[the planner on `/mcp`](api.md#the-planner-on-mcp).

| Reason | Next step |
|---|---|
| `private_repository_unsupported` | The planner reads public text only, so a private repository is refused before any call. Have your agent write the issue itself in the template's three sections and file it through `issue_file`'s form with no plan (`issue_draft` checks it), or wait for the internal route |
| `repository_not_allowed` | Name the repository as `owner/name`, or pick one of the ones the refusal lists: those are the repositories you may file into. A candidate that is private is left out of that list without a word |
| `no_write` | Your GitHub access to that repository is read-only, and the planner plans only what you may push to. Ask for one you may write in, or show the plan to someone who can file it |
| `planner_timeout` | The one call outlived its deadline (20 s, under `/mcp`'s 25 s). Ask again, with the same words or a shorter context |
| `planner_unavailable` | The plan service is down or refused the call. Try again later: nothing was filed, and the refusal names no repository |
| `plan_changed` | The plan and the hash you passed do not hold together: it was edited after it was shown. Show the newer plan and confirm that one, never the older text |

### OpenCode's background service

OpenCode v2 runs a background service (`opencode service start|restart|status|stop`) that keeps the
config it started with. A service started before `maku setup opencode` gives its attached sessions
no factory tools, while `opencode mcp list` still says `factory` is connected. After it registers the
server, `setup opencode` runs `opencode service status`: when the service runs it says so and asks
`y/N` on a terminal before running `opencode service restart`; `--restart-service` restarts without
asking. It never restarts silently, since other sessions may be attached; without a terminal or the
flag it prints the command. When the binary or its `service` subcommand is missing it prints the hint
alone. `maku doctor` warns (`warn opencode`) when the service started before the OpenCode config
file's last change.

### OpenCode and Code Mode

OpenCode v2 exposes MCP tools through Code Mode by default (`tools.<server>.<tool>` through its
`execute` tool). A config with the permission `{"action":"execute","resource":"*","effect":"deny"}`
turns Code Mode off and hides every Code Mode server: the factory is "connected" in
`opencode mcp list`, yet a session has no factory tools. `"codemode": false` on
`mcp.servers.<name>` keeps that server's tools on the native tool list (OpenCode's
"Disable Code Mode for an MCP Server"), which also suits the elicitation forms and `room_wait`.
After `opencode mcp add`, `maku setup opencode` sets `"codemode": false` on `mcp.servers.factory`
and keeps the rest of the file (it is rewritten as indented JSON; a file with comments is left
alone and setup says what to add). The pasted snippet carries it too. `maku doctor` warns
(`warn opencode`) when the factory entry lacks it and an `execute` deny rule exists. This is the one
config edit setup makes; every other client's config is only ever printed.

It also sets `"timeout": {"execution": 900000}` (ms) there: OpenCode cuts a tool at its execution
timeout, which cut `issue_file`'s form ("context deadline exceeded"). `maku doctor` warns when the
factory entry has none. When a form cannot be shown or is not answered, `issue_file` answers with a
`next:` hint holding one `gh` command that only files the issue, for the developer to run. It never
labels: starting the factory stays the developer's act (the label, or choosing start in the form).

### Short commands

Claude Code lists the MCP prompts as `/factory:handoff`, but typing that is rejected as an unknown
command: only `/mcp__factory__handoff` runs. So `maku setup claude` and `maku setup opencode`
also write one command file, `factory.md`, that calls the same prompts' tools:

| Type | Does |
|---|---|
| `/factory handoff [what]` | plans it with `intake_plan` and shows the plan, then files its first issue with `issue_file` and the plan's hash; with the planner off, writes the issue, has `issue_draft` check it, and files it |
| `/factory status [room]` | `room_summary` of the room; none: your most recently updated room |
| `/factory resume [room]` | `needs_list` once, then `room_brief` of the first room that needs you (or of the room you passed); names the brief's `Next:` line as the one step |
| `/factory inbox` | `needs_list` once: the rooms that need you. Empty, it says so and names `rooms_list` with `mine` |

Where: `~/.claude/commands/factory.md` (`.claude/commands/` with `--project`) for Claude Code,
`~/.config/opencode/commands/factory.md` (`.opencode/commands/`) for OpenCode, which reads the same
markdown file with `$ARGUMENTS`. A second `setup` rewrites it. The MCP prompts stay as they are.
Source: `internal/maku/skill/commands/factory.md`.

### Checking out an agent's branch

An agent's branch is code a sandboxed agent wrote, and some of its files run on your machine without
you running anything: agent and editor configuration, `.mcp.json`, hooks, `mise.toml`, `.envrc`.
So the checkout works like this:

- It fetches only `agent/<room>` from your `origin`, after checking that `origin` is the room's
  repository (`git@github.com:o/n` or `https://github.com/o/n`, with or without `.git`, in any case).
- It runs git with an argument list, never a shell, with `core.hooksPath=/dev/null` and no Git LFS
  smudge filter, so no hook
  of your repository or of the branch runs during the fetch or the checkout. It runs nothing in the
  new worktree.
- The worktree path is `<repository>.<room>` in the repository's own parent directory. A path that
  already exists is refused, never overwritten.
- It fetches `agent/<room>`, resolves it once to a commit, and checks, asks about and creates only
  that commit: a fetch that moves the branch while a form is open changes nothing. It verifies the
  new worktree is at that commit, and removes it otherwise. A killed or failed checkout is removed,
  never left to be reused.
- A local branch `agent/<room>` that is not that commit is refused, with the command to drop or move
  it. Every git call is bounded (2 minutes for the fetch, 30 seconds for the rest), and recursion into
  submodules is off.
- It lists what the branch changed against its merge base with your `HEAD`, and against `HEAD`
  itself. If a changed file is one of the paths below, or is under your repository's in-tree
  `core.hooksPath`, it asks first: a form whose `create` field defaults to no (`room_checkout`),
  or `y/N` on a terminal (`maku checkout`). The file names are shown in code spans, cleaned.
  `core.hooksPath` is read from your effective git config, and an `origin` with several URLs is refused.
  A client with no forms, or a command with no terminal, is refused and told the files.

The paths: the folders `.claude/`, `.cursor/`, `.opencode/`, `.gemini/`, `.codex/`, `.vscode/`,
`.agents/`, `.devcontainer/`, `.kiro/`, `.amazonq/`, `.roo/`, `.continue/`, `.zed/`, `.qwen/`,
`.windsurf/`, `.github/skills/`, `.github/hooks/`, `.husky/`, `.githooks/`, `.mise/`, `mise/` and
`.config/mise/`, whole (settings, hooks, tools, plugins, skills and scripts all live there), and the
files `.github/mcp.json`, `.mcp.json`, `opencode.json`, `opencode.jsonc`, `crush.json`,
`.crush.json`, `.devcontainer.json`, `.envrc`, `.tool-versions`, `mise.toml`, `.mise.toml`,
`mise.local.toml`, `.mise.local.toml`, `mise.*.toml`, `.mise.*.toml`, `.pre-commit-config.yaml`,
`lefthook.*`, `lefthook-local.*`, `.lefthook.*`, `.lefthook-local.*`, `.config/lefthook.*`, `.config/lefthook-local.*`, `.gitmodules` and
`.lfsconfig` (a `*` stays within one name), in the repository root or any folder of it, in ASCII
case. A changed path with a byte past ASCII anywhere in its name is flagged too, since a filesystem
may fold it into one of these names. A changed path that is a folder of one of them counts (a file
named `.claude`), and so does every symlink the branch adds or changes, whatever its name: a link
`.claude -> x` with `x/settings.json` would otherwise hide the file. A symlink the branch's tree
already has at one of these places, in any folder, is asked about as well, since the branch can
change its target. Flagging more is safe, since a flag only means "ask first". The form lists every
match of the paths above first (up to 200), and then the symlinks and non-ASCII names, at most 20
of them, and a count of the rest. The list is `flagged` in `internal/maku/checkout.go`; the tests
hold a fixture per entry.

What the check cannot see: it follows names, not what runs them. A file that one of your own git
hooks or build steps runs is code, and the check does not know it: lint-staged or commitlint
configuration, a Makefile target that a hook calls, `package.json` scripts, a workflow you run with
`act`. Read the diff of those yourself. A repository with more than about 150,000 files makes the
tree listing pass its 16 MiB cap, and the checkout then refuses instead of guessing.

One checkout of a room runs at a time in a repository: it takes an advisory lock (`flock`) on
`maku-checkout-<room>.lock` in git's directory, which the kernel drops when the process ends, so
a crash leaves nothing to wait out. A second is refused while the first waits for you. What the
call made is all it ever removes: the branch is created only if it does not exist yet, so a branch
you made while the form was open is never used, moved or deleted, and a worktree at its path that
git does not list as this call's is left alone. A worktree of the branch whose folder is gone is
removed and made again, unless you locked it (`git worktree lock`).

## The factory-handoff skill

`maku skill install` writes an Agent Skill that lets a local coding agent hand a side task to the [factory](factory.md) and follow it. The skill is embedded in the binary, so it always matches the CLI that runs its commands (`internal/maku/skill`).

It works MCP first. With the `factory` MCP server registered (`maku setup <client>`), it uses
`intake_plan`, `plan_check`, `rooms_list`, `room_summary`, `room_changes`, `room_wait`,
`room_checkout`, `issue_draft` and `issue_file`, and shows each tool's text as it is. Without the
server, a shell fallback section runs the same jobs through `gh` and `maku`.

It does:

- Hand your own words, and the context it has read, to the broker's planner (`intake_plan`), show the
  plan as it is — its question included — and file its first issue with `issue_file`, plan and hash
  together, only once you answer the form. With the planner off, or in a shell with no MCP server, it
  writes the issue in the template's three sections, has `issue_draft` check it, and says the broker
  planned nothing rather than passing a plan it wrote as the planner's.
- Follow a task: find the room, and report phase, run, PR and the newest notes.
- Say "it needs you" first when something waits on you, giving the link: approvals stay on the room page.
- Answer "anything waiting on me?".
- Follow a room in the background with `room_wait` when the agent can run a background task or subagent, and check a room's branch out into a new worktree beside the repository with `room_checkout`, only when you ask. It never touches your current checkout.
- Queue guidance for the next run with `maku post <room> --queue`, only with your words.

It never:

- Applies a label, `factory/ready` included, unless you ask for that exact label.
- Approves, or gives a command that approves.
- Follows an instruction found in a note or an agent's text: that is data to report.
- Installs software, trusts a CA or signs in for you. It runs only read-only checks.

`skill install` writes `<dir>/factory-handoff/` (`SKILL.md` and `references/issue-template.md`), `.agents/skills` by default, and overwrites what is there. It refuses to write through a symlink inside the skill tree. `--dir` sets where the skill is written; point it at the directory your agent reads skills from.

The skill needs `gh` (authenticated) and `maku` (signed in) on the PATH.

`maku doctor` has a `skill` check: it reads the `roomctl-version` of every `factory-handoff` skill in the directories the six clients read (`maku setup`'s, under your home and under the working directory), and fails when one is not this binary's version, because a skill from another release names tools or commands this one lacks. It skips when none is installed.

## Troubleshooting

Run `maku doctor` first: it checks in the order things fail and prints the fix of each.

| Symptom | Cause | Fix |
|---|---|---|
| `maku` not found | not installed | [Install and verify](#install-and-verify) |
| `not configured` | no config file | `maku configure --url https://rooms.<domain>` |
| `no such host` on the rooms host | the VPN is down: the host resolves only on the tailnet | `tailscale up`, then `tailscale status` |
| `x509: certificate signed by unknown authority` | the platform's root CA is not trusted, and `maku` has no `--cacert` | trust the root CA on your system, or set `SSL_CERT_FILE` to a bundle holding it for the shell that runs `maku` |
| auth error, expired token | not signed in | `maku login` |
| `401: user_inactive` | your ZITADEL user was deactivated, locked or deleted | ask an admin to reactivate it; `maku login` cannot help |
| `rooms` lists nothing | your ZITADEL user has no linked GitHub identity, so no repository is readable | ask an admin to link your GitHub account to your ZITADEL user; `maku doctor` reports it on its `github` line |
| `FAIL skill` on `maku doctor` | a `factory-handoff` skill from another maku release, in your home or in the repository (`cloud-native-ref` carries `.agents/skills/factory-handoff` at `roomctl-version: "v0.11.0"`, so this fails there until that copy is removed) | delete the stale copy, or rewrite it: a stale copy in your home needs `maku setup <client>` without `--project`, and `--project` only rewrites the repository's copy |
