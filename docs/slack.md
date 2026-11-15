# The Slack App

`maku-slack` is the factory's Slack App: a mention in a thread plans the work as one proposal (or, with [`proposals.enabled`](#config) off, drafts one issue), a maintainer's Confirm or check mark files it and starts the work, and the broker's signed webhooks tell the same thread how it goes. After filing, the thread answers questions about its task, and in channels that opt in, lets a linked maintainer stop a queued or running task or retry an escalated one behind a confirmation. Steering, every other approval class, and merges stay in the [web UI](web-ui.md); a `forge.push` approval has a card you can decide. A question from the run is linked in the thread, never answered there.

```mermaid
sequenceDiagram
  participant U as Slack user
  participant S as Slack
  participant A as maku-slack
  participant B as maku-broker
  participant F as maku-factory
  U->>S: @factory what to do
  S->>A: app_mention (signed)
  A->>B: handoff (proposals on) or issue_draft
  A->>S: proposal or draft message (metadata)
  U->>S: Confirm, or a check mark
  S->>A: block_actions or reaction_added (signed)
  A->>B: run_create (sponsor, on_behalf_of, handoff)
  B->>F: task
  B-->>A: webhook (signed, ids only)
  A->>S: thread line (end line edited in place)
```

## Install

The umbrella chart installs the App as the subchart `slack` (`charts/maku-slack`), off by default. Turn
it on with [`values-slack.example.yaml`](../charts/agent-factory/values-slack.example.yaml), which
holds the four blocks that must agree. Helm cannot set one subchart's values from another's, so the
render checks them and fails naming the key that disagrees:

| Block | What it must say |
|---|---|
| `slack` | the App: its image, `issuerURL`, `roomsURL`, and `config` (`teamID`, `channels`, `proposals`) |
| `maku.broker.webhooks` | an entry named `slack.principal` (`slack-app`), `network: cluster`, `url: http://factory-slack-app.<namespace>.svc.<clusterDomain>:8081/webhooks`, `existingSecret` equal to `slack.webhookSecret` |
| `maku.broker.human.mcp.sponsored` | an entry named `slack-app` |
| `maku-factory.config.api.sponsored` | an entry named `slack-app` |

`slack.enabled: false` with a broker subscriber that still targets `factory-slack-app` fails too: every
event would go to a subscriber that does not exist, and the broker would disable it.

**What it renders:** one replica (`factory-slack-app`), its Service (`events` 8080, `webhooks` 8081,
`metrics` 9090), a ServiceAccount, a Role with `get` and `update` on `factory-slack-threads` alone, the
thread map, and the config built from `slack.config` plus the derived file paths and addresses.

**The Secrets, by name:**

- `slackSecret` holds `signing-secret`, `bot-token` and `bot-user-id`.
- `principalSecret` holds `client-id` and `client-secret`, plus `project-id` under the [preset: ZITADEL](identity.md#the-presets).
- `webhookSecret` holds `signing-key`. The broker mounts the same Secret.

**Exposure.** Only `POST /slack/events` must be public, through [`expose.slack`](exposure.md).

**Network.** `slack.networkPolicy.mode` must equal `maku.networkPolicies.mode`, and the App then
admits:

- Slack's events from the gateway;
- the webhooks from the broker;
- the metrics from the scraper.

It reaches DNS, the apiserver, the broker's `:8080`, `slack.com` and the IdP. The broker's own policy
admits `:8080` from the web front door alone, so the subchart adds `room-broker-from-factory-slack-app`,
which admits the App, and nothing else, to the broker's `:8080`.

**Off removes** the App, its route, its ServiceMonitor and the `maku-slack` alert group. Without it the
factory loses:

- mention intake and proposals;
- thread lines;
- `forge.push` cards;
- `@factory link`.

MCP, the CLI and the web UI are unaffected.

**The thread map survives upgrades.** It is rendered with no `data` and kept on uninstall. A Flux
`driftDetection` or Argo CD sync still needs the ignore rule in
[the chart's README](../charts/maku-slack/README.md#the-thread-map-survives-upgrades). An install
whose map already exists hands it to Helm first, as that README says.

## Manifest

The App is one workspace's: create it from this manifest, install it, and put the signing secret and the bot token in the secret store.

```json
{
  "display_information": { "name": "Factory" },
  "features": { "bot_user": { "display_name": "factory", "always_online": true } },
  "oauth_config": {
    "scopes": { "bot": ["app_mentions:read", "chat:write", "reactions:read", "channels:history", "groups:history", "users:read"] }
  },
  "settings": {
    "event_subscriptions": {
      "request_url": "https://factory-slack.<domain>/slack/events",
      "bot_events": ["app_mention", "reaction_added", "message.channels", "message.groups"]
    },
    "interactivity": { "is_enabled": true, "request_url": "https://factory-slack.<domain>/slack/events" },
    "org_deploy_enabled": false,
    "socket_mode_enabled": false
  }
}
```

Interactivity, and no slash command. Interactivity carries both kinds of button the App posts, a proposal's Confirm and Cancel and an approval card's two buttons. Its request URL is **a setting of its own**, apart from the Events request URL: the console keeps one under Event Subscriptions and one under Interactivity & Shortcuts, and both hold the same value, the App's `/slack/events`. Slack signs a button press exactly as it signs an event, so one door with one signature check, one size cap and one workspace check serves both, and the body's content type says which shape arrived ([security](reference/security-controls.md#the-slack-app)). Both URLs change when the App moves (a cloud move, a new domain). Left stale, every button press gets Slack's generic `could not handle this payload` while events still arrive, so mentions and replies work and no button does. After any move, press one proposal's Confirm before anything else and check that `maku_slack_app_requests_total{route="interactions"}` rose ([operations](operations/factory.md#intents-in-shadow)). The bot scopes do not change: `chat:write` covers `chat.update` (a card rewritten in place) and `chat.postEphemeral` (the line one clicker sees) as it already covers `chat.postMessage`. `chat:write` also covers the [self-serve link](#self-serve-link)'s answer, a direct message from the bot (`chat.postMessage` with the user's id as channel, no `im:write` and no `conversations.open`) and, when that fails, `chat.postEphemeral`. `users:read` is optional: with it the link page names the Slack user by their handle, then their display name (`users.info`), without it by their id only; an App created from an earlier copy adds it under OAuth & Permissions and reinstalls.

Draft editing needs the `message.channels` (and, for private channels, `message.groups`) bot events: an App created from an earlier copy of this manifest must add them under Event Subscriptions in the Slack console and reinstall. The scopes (`channels:history`, `groups:history`) are already there. Without the events, mentions and check marks work as before and replies are never seen.

## Config

`maku-slack --config <file>`, strictly decoded (an unknown key is refused).

| Key | Meaning |
|---|---|
| `teamID` | Required. The one workspace served; any other workspace's events are dropped |
| `channels` | Required, at least one: `<channel id>: {repositories: [owner/name], maxCostUSD, approvals}`. The repositories a draft there may name, and the `max_cost` a check mark there sends (at least 0.01) |
| `channels.<id>.approvals` | Optional, default `false`: a `forge.push` wait in that channel is drawn as [the card](#the-card), and a press on it reaches the broker. Off, the wait keeps the text line, and a press on a card posted while it was on is answered "approvals are not turned on in this channel" with the room link, without reaching the broker. Turning it off is the fast rollback; it is not the authority, since a broker without `human.slackDecide` answers the route with a 404 and a press then says the factory could not be reached |
| `channels.<id>.intents` | Optional, `off` (the default), `shadow` or `on`: whether [tier 1](#understanding-a-reply) reads the replies of a filed thread that the phrase table does not know. `shadow` classifies each one, counts and logs it, and posts today's unknown line. `on` is accepted and, until the read intents are served, behaves as `shadow`. Write it quoted or bare: YAML reads a bare `off` or `on` as a boolean, and the App reads it back as the word. Off is the rollback, and the fast one: tier 1 is never asked |
| `channels.<id>.collectReplies` | Optional, default `false`: the channel's agreement that its replies are stored, redacted, with the label the classifier gave them, for the broker's `retentionDays` (30 by default) and then deleted. It needs `intents` `shadow` or `on`. A channel without it stores nothing a person typed |
| `channels.<id>.intentActions` | Optional: the write actions the channel may offer, any of `stop`, `retry`, `answer`, `steer`, `amend`, each once. `stop`, `retry` and `answer` are served without needing `intents: on` — `stop` and `retry` on the phrase table (`stop`, `stop it`, `how do i stop it`, `retry`, `try again`) and as buttons on the waiting-for-human and escalated end lines, and `answer` on any free-text reply while the task waits on a question, each behind a ten-minute confirmation named to the presser while the broker's `human.slackStop`, `human.slackRetry` or `human.slackAnswer` is on. `steer` and `amend` have no effect |
| `channels.<id>.notify` | Optional: the webhook transitions that post a line in that channel, by full name. Absent means the four defaults (`room.task_triaged`, `room.waiting_for_human`, `room.pr_opened`, `room.ended`), so a run start, a review verdict, a budget notice and an approval close post nothing by default; `room.merged` and `room.reverted` rewrite [the end line](#follow-and-the-end-line) in place rather than posting a separate line (listing `room.reverted` in `notify` also posts a line of its own); `[]` means none. A name that is not a webhook type, a name listed twice, and a type the App draws no line for (`room.task_received`, `room.run_ended`, `room.monitor_verdict`) are refused at start. A card and its close follow `approvals`, not this list |
| `proposals.enabled` | Optional, default `false`: a mention is planned by the broker's `handoff` tool and posted as one [proposal](#flows) with Confirm and Cancel. Off, a mention is a draft, as below; on, when planning is off or unavailable on the broker, a mention answers `Planning is not available here; file on GitHub.`. Turning it off is the rollback: a Confirm on a proposal posted while it was on says to react with a check mark instead |
| `signingSecretFile`, `botTokenFile`, `webhookKeyFile`, `botUserIDFile` | Required mounted files; each must be readable and non-empty, and the webhook key must parse (one or two base64 lines of at least 32 bytes): the App refuses to start otherwise, naming the field and never the content |
| `clientIDFile`, `clientSecretFile`, `projectIDFile`, `issuerURL` | Required: the machine client at your identity provider, the project whose audience the token carries ([preset: ZITADEL](identity.md#the-presets); `generic` takes `audience` instead, below), and the issuer (an absolute `https` URL; `http` is accepted only for a loopback host, in tests). The three files are checked at start like the others |
| `preset`, `tokenURL`, `scopes`, `audience` | Optional. An empty `preset` is `zitadel`: the request above, `projectIDFile` required. `generic` mints at any OIDC issuer: `tokenURL`, else its discovery document's `token_endpoint`, with `scopes` and `audience` sent as a parameter; `projectIDFile` is refused there. See [identity](identity.md#the-slack-app) |
| `brokerURL`, `roomsURL` | The broker's base URL, its human listener (`http://<svc>.<ns>.svc.cluster.local:8080`, no path: the App calls `<brokerURL>/mcp`), and the room pages' base URL |
| `namespace` | Where the `factory-slack-threads` ConfigMap lives |
| `eventsAddr` `:8080` | Slack's events, `POST /slack/events`: the public route |
| `webhooksAddr` `:8081` | The broker's webhooks, `POST /webhooks`: cluster only |
| `opsAddr` `:9090` | Metrics and the probes |

The App's token is a client-credentials grant from your identity provider ([preset: ZITADEL](identity.md#the-presets): the project-audience scope, `openid` and `urn:zitadel:iam:org:project:id:<project id>:aud`). The factory serves a Slack user listed in its `onBehalfOf.slack`, or, with `onBehalfOf.slackLinks`, one who linked themselves ([self-serve link](#self-serve-link), [The factory](reference/sponsored-principals.md)).

The broker needs one `webhooks` entry for the App ([operations](operations/factory.md#webhooks)):

```yaml
webhooks:
  - name: slack-app
    network: cluster
    url: http://<svc>.<ns>.svc.cluster.local:8081/webhooks
    events: [room.task_triaged, room.run_started, room.waiting_for_human, room.pr_opened, room.verdict_posted,
             room.merged, room.reverted, room.budget_reached, room.ended, room.approval_closed]
    repositories: [Smana/ai-native-ref]
    secretFile: /etc/webhook/key   # the key file the App mounts as webhookKeyFile
```

- `network: cluster`; the port `:8081` is mandatory and the path is exactly `/webhooks`.
- `events` are the ten above, by their full names: the broker refuses a short one. The App posts no line for `room.task_received`, `room.run_ended` or `room.monitor_verdict`, and none for `room.run_started` unless a channel's `notify` lists it. `room.approval_closed` is what makes a card inert after the deadline or another surface's decision, so a subscriber serving a channel with `approvals: true` must list it.
- `repositories` is required.
- `secretFile` is the same key as the App's `webhookKeyFile`: one or two base64 lines, each at least 32 bytes decoded.

## Flows

**Proposal** (`proposals.enabled`). A mention is the request, in the person's own words; no template. The App calls `handoff` (and `handoff_check` for the canonical plan hash and offered workflows) and posts **one proposal message** in the thread: the repository, the issue (with the planner's suggestion when the repository's catalogue answered: `Suggested: class … · workflow …` and the planner's why, inert — no merge policy or outcome is named before a pull request exists), the `Workflow` section (`<workflow> (<roles>)`) and the `Budget` section (`Budget    up to $X`), with a header naming the revision and the plan's hash, then Confirm and Cancel. The broker stores the plan for 24 hours under its handoff id; the message's metadata (`factory_proposal`: `id`, `hash`, `author`, `rev`, `repo`) carries the handoff id and revision rather than the plan body. A plan the composer wrote because the planner could not is labelled as such. When the plan's author maintains the repository and `handoff_check` offers workflows, the proposal also carries a static select whose placeholder is the plan's workflow (`Workflow: factory's choice` and each offered workflow); nothing for anyone else, nor while a gap remains. Choosing a workflow pins it: the App calls `handoff_check` with `{id, on_behalf_of, workflow}` (no model call, no daily plan) and redraws the message as the next revision with its new hash and a `Pinned: workflow …` line; Confirm binds the hash shown, so a Confirm on the hash the select replaced is refused `plan_changed`. The broker checks the pin against the presser's own catalogue, so a person who is not a maintainer is refused `not_maintainer` in an ephemeral line and the message stays as it was. A refused plan is one `Not proposed: …` line per closed reason. With planning off or unavailable on the broker, a mention answers the fixed line `Planning is not available here; file on GitHub.`. In a thread that already holds a proposal whose planner later went away, a reply or a mention is told `Not re-planned: this deployment no longer plans from Slack; …` and nothing is drafted beside it. The two refusals made before the planner is asked — a request past 16 KB and a thread already filed or cancelled — stay the proposal's, with its lines, and count as a proposal `mention`.

- **A slow plan says so.** When `handoff` has not answered after 4 seconds, the App posts one `Planning…` message in the thread. The proposal then replaces that message in place (same message, same buttons rules); when Slack refuses the edit, the proposal is a new message and the status is edited to `Planning finished.`. A refusal also edits it to `Planning finished.` and says the refusal below it. A plan that answers inside 4 seconds posts no status.
- **A refused first mention orients.** `not_linked`, `repository_not_allowed`, `repository_not_served_here` (the request names a repository this channel does not serve) and `no_write` are causes the person can fix, so the App says the cause, the fix (`@factory link`, or the repositories this channel serves) and the rooms page in a `chat.postEphemeral` only the mentioner sees (`chat:write` covers it, no new scope), in the thread. When Slack refuses the ephemeral, the same line is posted in the thread. The thread itself stays clean.
- **A reply re-plans.** A reply by the mention's author continues the stored handoff (`handoff` with `{id, answer}`) and **updates the same message** in place, so its buttons always bind the plan they sit under. Changed sections are marked, and when a hashed field moved (the repository, the budget, an issue's title, body or class, the roles or their order) the header says `requires a new confirmation`. At most 40 revisions per proposal. When Slack refuses the edit, the App posts the revision as a new message and makes the replaced one inert.
- **Confirm** is a maintainer's: it files the stored plan through `run_create` with `handoff` and `plan_hash`, the same path as a check mark, and rewrites the message into the Filed line. An expired handoff answers `Not filed: this proposal expired; mention me again to plan it.`. The presser alone is told why a press filed nothing (`Not filed: a newer revision of this proposal is in this thread; press that message's buttons.`, a repository the channel no longer serves, a hash that no longer binds the plan, and the check mark's refusals below).
- **Cancel** is the author's alone (anyone else is told `Only @author cancels this proposal; a maintainer confirms it.`); it rewrites the message into `Proposal cancelled by @author; mention me again to plan another.`, with no buttons, and starts nothing.
- **A check mark** on the proposal is the fallback for Confirm: the same checks and the same filing, answered in the thread.

**After filing, the thread answers questions.** A reply in a filed thread is matched, lowercased and without its closing `?`, `.` or `!`, against a closed list of phrases, and answered from the room the Filed line names — never from the thread's text:

| Ask | Answer, from |
|---|---|
| `status`, `how is it going`, `where is it` | `State:`, the issue, `Budget: $X of $Y (Z %)` (`so far` until `spendFinal`), `Merge:` with the gate's decision once there is one, and the room link (`task`) |
| `needs me`, `what needs me` | one line per need with its action and link, or `Nothing waits on this task right now.` (`inbox`) |
| `what changed`, `what has changed` | the ten newest milestones, then `More: the room page` (`task`) |
| `the pull request`, `pr`, `the pr` | its number and link, or `No pull request yet in this room.` (`task`) |
| `stop`, `stop it`, `how do i stop it` | with `stop` in `intentActions`, a ten-minute confirmation (`@<user>: stop task #<issue>? Its run ends now and the pull request stays open.`) with `Stop the task` and `Cancel`; without it, ``I can't stop a run from here. Apply the `factory/stop` label on the issue, or use Stop on the room page: <link>`` and no broker call |
| `retry`, `try again` | with `retry` in `intentActions`, a ten-minute confirmation (`@<user>: retry task #<issue>? It runs again from where it stopped.`) with `Retry the task` and `Cancel`; without it, ``I can't retry a task from here. Comment `/factory retry` on the issue, or open <link>`` and no broker call |
| anything else | (in a channel with `intents` `shadow` or `on`, also [read by tier 1](#understanding-a-reply), which changes nothing the thread shows) I only understand these here: `status`, `needs me`, `what changed`, `the pull request` (and `stop`, which says how to stop), then the room link |

An answer is at most 12 lines; a longer one is cut at the tenth with `…`. Every value it quotes (a title, a need, a milestone) is escaped and stripped of control and invisible characters, so a `<!channel>`, a mention or a link in it fires nothing; the only links are the App's own room page and the URLs the broker built. Each answer makes one sponsored read at most, and the disabled `stop`/`retry` and unknown answers none. A Filed line whose marker names no room is answered `I can't tell which room this thread filed; open the room page from the Filed line above.`, a room the broker cannot read `The room could not be read just now; try again in a minute.`, and a cancelled thread nothing.

**Stop, retry and answering a question.** When a channel lists `stop` in `intentActions`, `room.waiting_for_human` lines carry a `Stop` button; when it lists `retry`, escalated `room.ended` lines carry a `Retry` button (merged, stopped and completed end lines carry none). Pressing either button posts the same ten-minute confirmation named to the presser as typing the phrase. When a channel lists `answer` in `intentActions` and the task waits on a scoper question (`needs_author` or an open `scope.question` on an escalated or waiting task), any reply that is not one of the closed phrases above posts a ten-minute confirmation named to the reply's author (`I understood: answer "<exact words>"`) with `Answer` and `Cancel`. Only the named presser may confirm or cancel (`Only @<user> confirms this.`); after ten minutes a press answers `This confirmation expired.` and clears the buttons. Confirm calls [`POST /api/slack/stops`, `/api/slack/retries` or `/api/slack/answers`](reference/api.md#post-apislackstops-post-apislackretries-and-post-apislackanswers) when `human.slackStop`, `human.slackRetry` or `human.slackAnswer` is on, which re-resolves the GitHub login from `slack_links` and forwards to the factory's [`POST /v1/tasks/{task}/stops`, `/retries` and `/answers`](reference/api.md#post-v1taskstaskstops-post-v1taskstaskretries-and-post-v1taskstaskanswers) to verify repository access and apply the transition; the confirmation message is rewritten in place (`Stopped.`, `Retried.`, `Answered — scoper restarted.`, `Answered — a maintainer will restart the task.`, `Only a maintainer of this repository can stop a task.`, `Only the issue author or a maintainer of this repository can answer this question.`, `This task is not running.`, `This task is not escalated.`, `This task is not waiting for an answer.`).

## Understanding a reply

A reply in a filed thread is read in two tiers. **Tier 0** is the table of five questions above, unchanged and always first: a reply it knows never reaches tier 1. **Tier 1** reads a reply tier 0 does not know, in a channel with `intents` `shadow` or `on`, from a linked Slack user, against a broker that serves [`intent_classify`](reference/api.md#intent_classify-on-mcp). The broker makes one bounded call on a cheap model, through the model gateway's classifier route ([gateway](gateway.md#the-planner-and-the-classifier)), shown the reply alone and the thread's phase as one closed word, never the thread's history, and maps the reply to one of nine intents: `status`, `needs`, `changes`, `pr`, `steer`, `amend`, `stop`, `retry` or `unknown`. A label with a confidence under the threshold, or one the model got wrong in shape, is `unknown`.

In **shadow** nothing a person can see changes. The thread gets exactly today's unknown line, posted before the classifier is asked, and the App counts `maku_slack_intents_total{tier="1", intent, outcome="shadowed"}` and logs the label, its confidence, the phase and the reply's SHA-256 and length, never its text. A classification the broker refuses (an author who is not linked, a cap reached, a model that timed out) leaves the same line and counts `outcome="refused"`; a broker without the tool counts `not_enabled`. No reply approves, steers, stops or confirms anything: a label is a count and nothing else.

What shadow keeps is the channel's choice. With `collectReplies: true` each classified reply is stored, with its label, through the redactor, for `human.slackIntents.retentionDays` (30 by default) and then deleted; the broker's retention job removes it with the rooms. Without the flag nothing is stored, so the platform holds no copy of thread text. `maku-broker intent-samples` exports what is stored, to label it and to measure the classifier ([operations](operations/factory.md#intents-in-shadow)).

**Draft** (`proposals.enabled` off). A mention whose text starts with the App's own `<@bot>` is free text: the App composes the issue template itself, so nobody types headings (Slack's composer strips `##`, rewrites `- ` as `•` and sends on Enter). Write it like this:

```text
@factory [Smana/ai-native-ref] docs: runbook 18 never says where the key lives
The runbook names the factory's API but not where its key is stored.
Should: say which Secret holds it.
Acceptance: docs/runbooks/18.md gains one line naming the Secret.
```

- **Line 1** is `[<owner/repo>] <title>`; the brackets are optional, and in a channel that serves exactly one repository the repository is optional too. A title that has the factory's `<area>: <what>` shape (`docs: ...`, `fix(agents): ...`) is used as is. Otherwise the App prefixes `docs: ` when the text mentions a doc, runbook or README, else `chore: `: the two areas that never claim a behaviour change. The factory's title rule is not relaxed.
- **Further lines** become `## What is wrong`; with none, the title does.
- A line starting with `should:`, `expected:` or `want:` opens `## What it should be` (default: "As the title says.").
- A line starting with `acceptance:`, `acceptance check:`, `done when:` or `check:` opens `## Acceptance check`, up to the end of the message. Slack's decoration (`*`, `_`, `>`, `•`, `-`) before the keyword is ignored, and the keyword also counts glued to a sentence end (`...the API.Acceptance:`, or followed by a line break). `•` bullets become `- `. With no acceptance given the section says so, and the factory's scoper asks the author on the issue.
- A text that already holds the template's `## What is wrong` heading is passed as typed.

A one-line mention is a complete draft. A malformed line 1 (no repository where the channel serves several) is refused with a `Try:` line built from the user's own text (the channel's repository in front of it; with several repositories the first, plus the list), shown inert like every refusal. The composed issue is posted in the thread, so the check mark files what is shown. The broker's `issue_draft` judges it against the template and a refusal is posted back as is. A good draft is posted in the thread by the bot with metadata `{repo, title, body, sha256}`. When that metadata would exceed 8 KiB (well under Slack's cap) nothing is posted as a draft: the thread gets a short line saying the draft is too long to file from Slack, with none of its text, and the request counts as `too_large`.

**Edit.** A reply in the draft's thread by the Slack user who wrote the mention amends the pending draft, and the App posts the result as a new draft reply (the earlier one stays as history, and a ✅ on it is answered "react on the latest draft"). A reply is read as the mention's free text: `Title: …` replaces the title (the area prefix rule applies again), `Should: …` or `Expected: …` replaces "What it should be", `Acceptance: …`, `Done when: …` or `Check: …` replaces the acceptance check (several lines allowed), anything else is appended to "What is wrong". The draft goes through the broker's `issue_draft` again. A reply that is only a ✅ files the latest draft as a reaction does, and never edits. Other replies from anyone else, in a thread with no draft, after the draft is filed, or that start with a mention of the App are ignored without a word; a reply the broker refuses is answered "Change refused: …". At most 20 drafts per thread, each reply is capped at 4000 characters, and the draft stays under the metadata cap. The state is the draft message's metadata (`author`, `rev`), read back with `conversations.replies`; the App's "Filed" line carries a `factory_filed` metadata marker that ends editing. Nothing is stored beyond what the App kept already.

The draft is shown in Slack's mrkdwn (an intro line, then `*What is wrong*`, `*What it should be*` and `*Acceptance check*` on their own lines with the text below, and a hint to reply or ✅); the issue body keeps the `##` template.

**File.** A `white_check_mark` files the task only when all of these hold: the message is the thread's latest draft; the user is linked, in `onBehalfOf.slack` or by a [self-serve link](#self-serve-link) (and the linked login is a maintainer); the message is the bot's own draft, re-read from Slack (a `conversations.replies` call on the message `ts` with `limit=1`; the message's user is the bot's) with a matching hash; the channel is configured. The idempotency key is the thread (`<team>/<channel>/<thread_ts>`): a second check mark in the thread, on any revision, is a duplicate and files nothing. A thread too long to read (over 5 pages) fails closed: nothing is edited or filed. The App then calls `run_create` with `on_behalf_of: slack:<team>/<user>`, the draft, the channel's `maxCostUSD` and `ref: slack/<team>/<channel>/<thread_ts>`. The thread map records the room, and the Filed line names the sponsor (`Filed #12 for @dana, sponsored by GitHub @smana`), so a wrong link shows at its first use.

**No silent check mark.** Every refusal on the check-mark path (reaction or ✅ reply) is one thread line naming the reason and its fix, said once per draft, user and reason (in memory, like the event dedup), so re-adding the ✅ does not repeat it:

| Case | Line |
|---|---|
| The channel no longer lists the draft's repository | `Not filed: <repo> isn't served in this channel; ask an operator to add it to the channel's repositories.` |
| The channel is no longer served | `Not filed: this channel is no longer served; ask an operator to add it to the Slack App's channels.` |
| The draft's hash fails | `Not filed: this draft no longer matches its check; start a new one with a mention.` |
| A replaced draft, a thread too long | `react on the latest draft`; `start a new one with a mention` |
| `not_linked` | `Not filed: @u is not linked to a GitHub account here; mention me with link to link it, then react again.` |
| `not_a_maintainer` | `Not filed: @u's GitHub account is not a maintainer of <repo>; a maintainer must react.` |
| `repository_not_allowed`, `repository_not_owned` | `ask an operator to add it to the Slack App's repositories` |
| `principal_budget_exhausted` | `the Slack App's budget for today is spent; react again tomorrow` |
| `daily_task_cap` | `the factory's daily task cap is reached; react again after 00:00 UTC` |
| Any other code | `Not filed: <the broker's refusal>` |

A passing failure (`kill_switch`, `principal_task_cap`, `link_unverified`, `ledger_unavailable`, `tasks_unavailable`, `intake_failed`, `meter_unpriced`, the factory or Slack unreachable) says to react again, at most once per 5 minutes per draft, user and reason. **Silent on purpose:** a ✅ on a message that is not the App's draft, a malformed id, the App's own user, a bot's event, another workspace, an event already seen, and a message Slack cannot return (no thread is known to answer in; logged and counted `error`).

### The card

One `forge.push` wait is one card in the thread it was filed from, and the card's two buttons are the only thing in a thread that decides anything ([security](reference/security-controls.md#the-slack-app) has the route, the standings and the closed codes).

**When a card is drawn.** All four hold: the room was filed from Slack (so its thread is in the map), the channel has `approvals: true` **and** lists the wait's repository, the class is `forge.push`, and the event carries an approval id and a branch inside the run pattern. Everything else keeps today's text line, which sends a person to the room page: `forge.pr`, `forge.other`, `mcp.write`, `shell.high`, `egress.new`, and a `forge.push` without a branch. One approval, one message: the App reads the thread, finds the card whose tagged metadata names the same approval id, and rewrites it in place, so a webhook redelivery — or a restart that lost the in-memory delivery set — cannot double it. A thread too long to read posts the card anyway and counts `too_long`.

**What it shows.** The class prose the room page shows, the branch, the deadline as Slack's own date token (the client renders it in the reader's timezone, so the App holds neither a clock nor a zone in the message), the approval's id, the buttons **Allow the push** and **Deny**, and **Open the room**. No agent text, no reason, no GitHub login, no other person's name: invariant 7 holds over the card as over every line. A card drawn after its deadline is posted already inert — prose and room link, no buttons — because the store would refuse the press; the App's clock decides only what is drawn, never what is decided.

**A press.** The buttons carry `v1|forge.push|<approvalId>|<branch>`, which is a hint and never authority: the broker re-reads the approval, the class, the run's branch and the clicker's standing, and a card that outlived the state it was drawn from decides nothing. Slack answers a press within 3 seconds, so the one door verifies the signature, deduplicates and enqueues, and a worker does the rest. The clicker alone is told what became of it, in one fixed sentence in the card's thread (an ephemeral): `Decided: the push goes out.`, `Not decided: your GitHub account cannot write to this room's repository.`, and so on — one line per closed code, naming no repository, branch, login or approver, and never repeating a broker's prose. Only a recorded decision takes the buttons off.

**Teardown.** Two `chat.update` writes, both best effort and both idempotent: the press's own, which turns the card into its header, class, branch, approval and the clicker's line; and the broker's `room.approval_closed`, which rewrites the same card for any close — approved, denied, expired, superseded — whoever decided, the room page included. A press names no room, so the room link leaves the card on the first write and comes back on the second. A failed update is logged as `card update failed` and counted `error`, and it is never retried: the card is a view, the store's single-decision fence is what refuses a stale press, and three given-up webhook deliveries would disable the subscriber.

**A close with no card is silent.** In a channel with `approvals: true`, a wait that drew only a text line (another class, or a `forge.push` without a branch) gets no close line either: the thread already told that wait's story, and its decision lives where its wait pointed, on the room page. A channel with `approvals` off draws no cards at all, and if its `notify` lists `room.approval_closed` its close is the thread's line, `The approval closed: <decision>. Room: <url>`. A channel removed from `channels:` after its card was posted takes that same text-line path: the card keeps its buttons, and a press there is `ignored` before any broker call: the App does not serve that channel, so nothing decides.

### Self-serve link

The link routes answer `Referrer-Policy: same-origin` (the rest of the rooms UI is `no-referrer`): under no-referrer a browser sends the form POST's `Origin: null`, which the broker's origin check refuses.

A Slack user links themselves instead of waiting for an `onBehalfOf.slack` entry in git ([design](superpowers/specs/2026-10-28-slack-self-serve-link-design.md)):

1. `@factory link` in a served channel. The App calls the broker's `slack_link_code` with its team and user id (and handle and display name, with `users:read`) and answers **in a direct message from the bot** (only that user sees it; the first line is the labelled link and the bare URL follows on its own line; it says never to send it to anyone; the thread gets a one-line pointer, never the URL; if the DM fails, an ephemeral in the channel, not in the thread, is the fallback, and the Slack error is logged) with `https://rooms.<domain>/link/slack?code=<code>`: single use, 10 minutes, replacing their earlier code.
2. The user opens it, signs in to the rooms UI (your identity provider), and sees `Link Slack user @handle (<id>) in workspace <team> to your account (GitHub: @login)?`, the display name below it marked as set by that user, and "never confirm a link someone sent you". **Link** posts the form, from the web UI's session only (a maku or MCP token is refused); the broker records `slack(team/user) → the account`.
3. From then on their ✅ files sponsored by that account's **current** GitHub login, still held to the repository's maintainers: a link grants nothing a maintainer did not have.

`@factory unlink` removes the user's self-serve link (never a config entry); the page, opened without a code, lists the account's links with an Unlink button each. Refusals on the page: the code expired or was used, the code is for another workspace, the account has no GitHub link (with the broker's [GitHub account link](github-link.md) on, the page sends them to `/link/github`), it holds 5 Slack users already, or the Slack user is linked to another account (it unlinks first: a link never moves silently). With the broker's `human.slackLink` off, both commands answer that self-serve linking is not turned on. `link` and `unlink` are the whole mention; a draft whose title holds the word is a draft.

Both sides must opt in: the broker's `human.slackLink` (principal, team, repository; [integration](integration.md)) and the factory's `onBehalfOf.slackLinks` ([The factory](reference/sponsored-principals.md)). A user in `onBehalfOf.slack` keeps that entry whatever they link: **the config wins**.

### Follow and the end line

The broker's webhooks name a room; the App finds its thread in the map and posts four transitions by default:

- `room.task_triaged`: `Started #<issue>. Room: <url>` (after one `task` read for the issue number, or `Started. Room: <url>`).
- `room.waiting_for_human`: [the card](#the-card) for `forge.push` when `approvals: true`; when the task was bounced with a scoper question (`needs_author`), `The scoper asks: <question>. Reply to answer. Room: <url>` (falling back to `Needs you` when no question text is recorded), never an end line; otherwise `Needs you (<reason>)` — carrying the merge gate's decision (`waits for a human: policy <name> is in shadow here`, `waits for a human: touches a protected file`, …) when the task waits on a merge — plus the room link.
- `room.pr_opened`: `Pull request #<pr> opened.`.
- `room.ended`: **one end line per thread**, stored under `endTS` in `factory-slack-threads`: outcome (`Merged #<pr>`, `Ended: done`, `Ended: stopped`, `Ended: escalated (out of budget)`, `Reverted #<pr>`), spend against budget (`$0.16 of $2.00`, marked `so far` until `spendFinal`), duration from the first run's `startedAt` to the last run's `endedAt`, and the merge gate's decision (`merged automatically by policy <name>`, `merged on @<login>'s approval`, `waits for a human: touches a protected file`). A `room.merged` or `room.reverted` that arrives later edits `endTS` in place (`chat.update`) and posts nothing unless `room.reverted` is explicitly listed in `notify`; if `room.merged` arrives before `room.ended`, it posts the end line first and `room.ended` updates it.

## State

- The thread map is the ConfigMap `factory-slack-threads`, the only object the App's Role names (`channel`, `ts`, `at`, `endTS`, `coordination`). Entries older than 30 days are pruned on write, and the encoded map is bounded near 900 KiB (a ConfigMap holds 1 MiB).
- One replica. Slack events (on `event_id`), button presses (on the approval, the user, the action id and the action's own `action_ts`, so Slack's retry of one press is a duplicate) and webhooks (on `webhook-id`) are deduplicated in an in-memory LRU, so a restart can post one duplicate line. Two concurrent redeliveries of one id can both pass the check before either finishes (known race): the worst case is one duplicate thread line, and for a press the store's single-decision fence still records one decision.
- **The message metadata is tagged.** Every message the App posts carries `event_type` and its payload: `factory_draft` (`repo`, `title`, `body`, `sha256`), `factory_proposal` (`id`, `hash`, `author`, `rev`, `repo`), `factory_filed` (`repo`, the `room` it started, and `cancelled` on a Cancel's), `factory_approval` (`approvalId`, `class`, `branch`) on a card, and `factory_act` (`action`, `room`, `presser`, `expires`, plus `text` on an `answer`, rewritten to `factory_act_closed` once resolved) on a stop, retry or answer confirmation. The tag is what finds a card again — a webhook redelivery rewrites it instead of posting a second one, and `room.approval_closed` reads the branch back from it — so a card posted without its tag would be doubled. A draft's payload on the wire is byte-identical to what it always was.
- **Two switches, both off by default, and they do different things.** The broker's `human.slackDecide` is the authority: without it the decide route is the mux's `404`, and a press says the factory could not be reached. The channel's `approvals` is who gets the affordance: without it a wait is a text line and a press never reaches the broker. The broker's switch must be on before any card offers a button, and Slack's interactivity URL must be set before the first card with one is posted.
- A webhook key the App cannot read answers `503 no_key`. That is not a hold: the broker retries a receiver's non-2xx after 10s, 1m and 5m, then counts a failure, and three given-up deliveries disable the subscriber until its `generation` is bumped. A rotated or missing key, or a Slack outage that makes the App answer `502`, therefore loses those events and can disable the subscriber. **Recovery:** fix the key (or wait for Slack), bump `generation` on the broker's `webhooks` entry and restart the broker (a `webhooks` change applies only at restart); the events of the disabled window are not replayed, and a room's page still shows its state. A failed card update is never one of these: it is logged and counted, and the close it was rewriting is answered `200`.
- Series: `maku_slack_intents_total{tier, intent, outcome}` counts what tier 1 read (`tier` is `1`, `intent` one of the nine, `outcome` `shadowed`, `refused` or `not_enabled` until the read intents are served); the thread's own question counter, `maku_slack_thread_answers_total`, keeps counting tier 0. `maku_slack_app_requests_total{route, outcome}` (`events`, `webhooks`, `interactions`; the closed list is `not_linked`, `repo_not_allowed`, `repo_not_served`, `no_write` (a first mention refused for that cause, see [the flows](#flows)), `drafted`, `edited`, `proposed`, `updated`, `reposted`, `multi_issue`, `answered`, `refused`, `filed`, `posted`, `card_posted`, `card_updated`, `card_closed`, `decided`, `ignored`, `not_enabled`, `not_supported`, `duplicate`, `link`, `unlinked`, `ok`, `bad_request`, `bad_signature`, `no_key`, `too_large`, `too_long`, `busy` and `error`). `busy` is an event or a press answered 503 because the App's 8 workers and its 64 queued items are all taken, or it is shutting down: Slack retries it. A press's own words are `decided` (a decision was recorded), `duplicate` (the fence had already closed it), `not_supported` (the class is not `forge.push`), `refused` (any other closed code the broker answered) and `error` (the broker, Slack or the clicker's line failed).
- `maku_slack_proposal_updates_total{action}` counts the proposal's actions (`mention`, `reply`, `confirm`, `cancel`, `repost`), and `maku_slack_thread_answers_total{question}` the filed thread's answers by question (`status`, `needs`, `changes`, `pr`, `stop`, `retry`, `unknown`): both on the broker's metric set, served under `maku_`.
- Every refusal is one structured `Info` line with `reason` (the broker's closed code, never the request's text), `channel` and `user`: `proposal refused` for a mention or a re-plan, `check mark refused` for a ✅. With the three outcomes above, `kubectl logs` and the counter say which cause hit.
- A refusal said once is remembered in memory (at most 4096): a restart can say it once more.
- `maku_slack_app_build_info{version}`: the running build, constant 1.

## What it never does

- **Answer anything but a `forge.push` approval, and only from a card's button — or stop, retry or answer a task's question only from its confirmation's button.** A signed press on the card the App itself posted decides that approval, for `forge.push` only, as the linked human the broker resolves — one class, no reason (the decision carries an empty one), and no steering; and a signed press on a `factory_act` confirmation by the person it names stops a queued or running task, retries an escalated one, or answers an open scoper question as the linked human. Everything below still holds everywhere else.
- Answer an approval, steer a run, or execute a command directly from a thread reply. A reply is an edit before filing (the author's, re-planning a proposal or amending a draft), one of the closed questions after (where `stop`, `retry` and `answer`, when listed in `intentActions`, post a confirmation card and never act by themselves), and never a way to steer a run. A reply that is only a ✅, from anyone, files the latest draft as a reaction does (the same checks, the same lines); any other reply in a draft's thread from anyone else, or after the draft is filed, is dropped unread, and one in a filed proposal's thread is answered and acts on nothing — saying `approve`, `deny` or `/factory retry` in the thread of a card does nothing at all, and neither does a ✅ on the card.
- Decide any other class, from any surface it owns: `forge.pr`, `forge.other`, `mcp.write`, `shell.high` and `egress.new` stay on the room page, and a press on a card for another class is `not_supported`.
- Merge. A card's approval releases one push; merging stays a review of the current head on GitHub.
- Take a reason, or show one. A press sends a decision and nothing a model could read as a sentence, and no card carries agent text.
- Post a link code where anyone but its user sees it, or trust a GitHub login from Slack.
- Post room text or anything an agent wrote.
- Answer a slash command.
- Serve another workspace, an unlisted channel or a draft it did not post.
- Hand its Slack token or its identity-provider client secret to the broker or a run.

## What a live run shows

Offline, `internal/slackapp` and `internal/app` cover the draft, the file, the card and the teardown; the two switches' live behaviour is [the design's eight proofs](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-05-v3x-slack-push-approval-design.md), L1–L8, run on one channel with `approvals: true`. On the cluster, also confirm two things about Slack itself: `conversations.replies` with the reply's `ts` and `limit=1` returns the reply (not the thread's parent), and a bot's messages carry `user=<the bot's U-id>`. The check-mark filter relies on both. The App reads its own message metadata through `conversations.replies`, so `metadata.message:read` is not needed; confirm that too, and that the manifest's bot scopes suffice.
