# Natural-language intents in Slack rooms: design

**Status:** design, decided by the owner on 2026-11-11 (SI1–SI14 below); nothing is built. It
**reverses CD25** of the [conversational developer experience design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-05-v3x-conversational-devex-design.md)
and rewrites the Slack App's invariant 8 ([security](../../security.md#the-slack-app)). **Scope:**
`internal/slackapp` (the reply router, the confirm message, its press), `internal/humanapi` (one
sponsored read, `intent_classify`, and four gated routes modelled on `human.slackDecide`), the
factory's handling of a Slack stop, retry and amendment, `docs/slack.md` and `docs/security.md`.
**Not here:** the intake planner's class and harness-profile suggestion and the `profile:` override
(a separate spec, see [SI11](#decisions)), approvals beyond `forge.push`, merges, free chat.

## Conclusion

A reply in a **filed** thread is read in two tiers. **Tier 0** is today's exact-phrase table
(`questionOf` in `internal/slackapp/answers.go`), unchanged. A reply tier 0 does not know goes to
**tier 1**: a read-only classifier on a cheap model maps it to one of nine intents plus the text it
extracted. Read intents are answered at once from the sponsored reads the App already makes. Write
intents never execute on the classification: the App posts **`I understood: <action> <text>`**
with **Confirm / Cancel**, and only a signed press by the reply's own author, resolved by the
broker to that human's standing, executes it, through one gated route per action.

| Intent | Kind | Executes through | Flag |
|---|---|---|---|
| `status`, `needs`, `changes`, `pr` | read | `room_summary`, `needs_list`, `room_changes` (as tier 0) | `channels.<id>.intents` |
| `steer` | write | `POST /api/slack/steers`: the text queued for the next run's brief | `human.slackSteer` |
| `amend` | write | `POST /api/slack/amendments`: the task's spec amended, issue comment and room note | `human.slackAmend` |
| `stop` | write | `POST /api/slack/stops`: what `factory/stop` does | `human.slackStop` |
| `retry` | write | `POST /api/slack/retries`: what `/factory retry` does | `human.slackRetry` |
| `unknown` | none | today's unknown line, naming what can be asked | none |

```mermaid
sequenceDiagram
  participant U as Author (linked)
  participant A as maku-slack
  participant B as maku-broker
  participant M as model gateway
  participant F as maku-factory
  U->>A: reply in a filed thread (signed)
  A->>A: tier 0: exact phrase?
  alt tier 0 match
    A->>B: one sponsored read
    A->>U: answer (as today)
  else no match, intents on
    A->>B: intent_classify(reply)
    B->>M: one bounded call, closed schema
    B-->>A: {intent, text, confidence}
    alt read intent
      A->>B: one sponsored read
      A->>U: answer
    else write intent
      A->>U: "I understood: stop the task — Confirm / Cancel" (tagged metadata)
      U->>A: Confirm (block_actions, signed)
      A->>B: POST /api/slack/stops (team, user, room, message ts)
      B->>B: resolve link, standing, phase; idempotency fence
      B->>F: stop requested (attributed human)
      B-->>A: closed code
      A->>U: one fixed line; message made inert
    end
  end
```

## Decisions

| # | Decision | Rejected |
|---|---|---|
| SI1 | **Reverse CD25.** A filed thread understands more than five phrases, and four of its intents act. CD25's two reasons are answered, not dropped: tier 1 is one stateless classification into a closed set, not a dialogue engine (SI3); and a stop is now something the App carries out, but only as the human's own confirmed press, with the standing the `factory/stop` label already requires (SI5, SI6). The card path ([push approval](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-05-v3x-slack-push-approval-design.md)) already made a signed press resolved to a linked human a decision surface; this extends that exception, never the reply rule | (a) keep CD25 and grow the phrase table: every new phrasing is a release, and the unknown share (`maku_slack_thread_answers_total{question="unknown"}`) does not fall. (b) a slash command grammar: Slack strips and rewrites formatting, people do not learn grammars, and it is still a command parsed from text |
| SI2 | **Tier 0 stays exactly as it is** (phrases, answers, the stop line, the 12-line cap), and runs first. A tier 0 match never reaches the classifier. Tier 1 runs only on a tier 0 miss, in a filed thread, in a channel with `intents: true`, against a broker that serves `intent_classify`. Otherwise the miss gets today's unknown line | (a) the classifier for every reply: cost and latency for phrases a map already answers. (b) dropping tier 0: a model outage would take `status` down with it |
| SI3 | **The classifier is a sponsored read on the broker, `intent_classify`**, beside `intake_plan`: one bounded call through the model gateway on its own `classifier` client and one fixed cheap alias (a constant, as `PlanModel` is), JSON-schema output `{intent, text, confidence}` with `intent` from the closed nine. It reads no room, holds no tool, stores nothing and decides nothing. Input: the reply alone (≤ 2,000 characters after Slack's decoding), plus the thread's phase as one closed word. Output that fails the schema, names another intent, or has `confidence < 0.7` is `unknown` | (a) a model call in the App: a second key and egress beyond slack.com and the broker. (b) the planner's frontier alias: ten to fifty times the price for a nine-way label. (c) the thread's history as input: more injection surface, more cost, and the App would ship thread text to a model it never shipped before |
| SI4 | **Extracted text is a substring of the reply.** For `steer` and `amend`, `text` must be a contiguous span of the normalized reply (case and whitespace folded); otherwise the whole reply is used. The classifier can choose what to quote, never write it | Trusting the model's paraphrase: the human would confirm words they never typed, and a reply could steer the classifier into rewriting itself |
| SI5 | **Write intents never execute on classification.** The App posts one confirm message in the thread: `I understood: <action> <text>` with Confirm and Cancel, carrying tagged metadata `factory_intent` (`intent`, `room`, `author`, `textHash`, `expires`, and for `steer`/`amend` the text). Only the reply's author may press; anyone else is told `Only @author confirms this.` The message expires after 10 minutes (buttons removed, `expires` checked again by the broker). Cancel rewrites it to `Cancelled by @author.` and calls nothing | (a) execute high-confidence intents directly: one misread stop ends someone's run. (b) an ephemeral confirm: no audit trail in the thread, and Slack keeps no metadata on it. (c) any maintainer may confirm: lets one person plant a reply and another approve it unread |
| SI6 | **One route per action, each its own flag, each modelled on `human.slackDecide`** (`internal/humanapi/slackdecide.go`): the App's sponsored principal alone, a JSON body of shapes checked before any read (team, user, channel, message ts, room, intent's text for steer/amend), 4 KiB cap, per-user limiter (10/min, burst 5), identity resolved never asserted (`slack_links` → ZITADEL account → current GitHub login), a closed result code and never a sentence, idempotency scope `origin_client=human:<sub>:slack-intent:<channel>/<message ts>`, and the room log records `actor_id=human:<sub>`. Unset, the route is the mux's 404 | (a) one `/api/slack/intents` route with an `action` field: one flag for four powers, and a widening would be a body change, not a route. (b) new MCP tools on the sponsored list: the sponsored list's "never steers or decides" bound would end |
| SI7 | **Standing per action**, held by the broker at the press: `stop` and `retry` require a maintainer with `write` on the task's repository, as the label and the comment do today ([factory](../../factory.md)); `steer` requires an owner or approver of the room (Room CR only) plus `write`, as a card does; `amend` requires the issue's author or a maintainer. Phase: `retry` only on an escalated task (`not_escalated`), `stop` only on a running or queued one (`not_running`), `steer` and `amend` refused once the task ended (`task_ended`) | Inheriting the App's sponsor's standing: a press would act as `@smana` whoever pressed |
| SI8 | **Raw thread text never reaches a running agent.** A reply is input to the classifier and nothing else. Steer and amend text reaches the room only through the confirmed route, as the human's attributed message (`origin=slack`), redacted like any human message. `steer` is delivered `queued` (the next run's brief, exactly the room page's queue): live steering needs the driver token, and the App never takes it | (a) live steer from Slack: the route would take the driver token from the factory, a fence the room page makes a human take with a reason. (b) forwarding the reply verbatim on confirm: SI4's span is what was confirmed, nothing beside it |
| SI9 | **Amend before filing is today's path, unchanged; amend after filing is a new route.** Before filing, every author reply already re-plans a proposal or amends a draft ([Slack](../../slack.md#flows)), and tier 1 does not run there. After filing, a confirmed `amend` appends a `task_amended` event to the room (queued for the next run's brief, read by the scoper if no run started yet), and the factory mirrors it as an issue comment `Amendment from @login via Slack: <text>`, so the issue stays the spec of record. A running run is untouched; to redirect it now is `steer` | (a) editing the issue body: rewrites the author's spec and races GitHub edits. (b) re-filing: loses the room, the spend and the run history |
| SI10 | **The per-channel `intents: false` default is the rollback.** Off, tier 1 is never asked and a press on a confirm posted while on is answered `intents are not turned on in this channel` with the room link, without reaching the broker. The broker flags are the authority, the channel switch is the affordance, as `approvals` and `human.slackDecide` already split it | A single global switch: the first channel's trouble would turn every channel off |
| SI11 | **"Change the workflow or the merge policy" is not an intent.** Class and harness profile are chosen at intake: the planner suggests both, and the user overrides with `profile:` within the class's `harnessAllowed` (the intake profile spec, separate). Before filing, such a reply is ordinary proposal context and re-plans. After filing, tier 1 labels it `unknown` and the answer says the class and profile were fixed at filing (stop and re-file, or `/factory retry` on an escalated task), and that a merge policy is a `MergePolicy` manifest changed by PR ([factory resources D9](2026-11-09-factory-resources-design.md#decisions)) | (a) a `policy` intent: a thread would edit what Flux owns. (b) changing a running task's class: its budget, reviewers and merge rule were bound at filing |
| SI12 | **Who is classified.** Tier 1 runs only on replies by a linked Slack user (self-serve or `onBehalfOf.slack`); anyone else gets tier 0 and the unknown line. A reply that starts with a mention of the App stays a new mention, as today | Classifying everyone: cost from bystanders, and confirms nobody in the thread may press |
| SI13 | **Cost is capped where the planner's is.** `human.slackIntents.dailyCalls` (default 500) and a per-user limiter (20/hour); past either, the reply is `unknown` with `classifier_busy` counted. The call is metered on the gateway's `classifier` client. At a few hundred input tokens and under 50 output on a light alias, a call costs a fraction of a cent | No cap: a looping bot or a busy channel spends without a ceiling |
| SI14 | **Observability is counted in closed labels and logged without text** (below). Every executed action is in the room log as the human's; every classification is a structured log line with the text's hash and length, never the text | Storing replies for evaluation by default: a new copy of thread text in the platform, which today holds none |

## Security analysis

| Threat | What holds |
|---|---|
| **Prompt injection into the classifier** (a reply crafted to make it emit `stop`, or to rewrite the steer text) | The classifier has no tools and no reads; its output is a closed enum plus a span of the same reply (SI3, SI4); a write output only draws a confirm that the reply's own author must press (SI5). The worst case is a wrong confirm the author cancels. A reply that injects is, by SI12, a linked user's: they could press the room page's buttons directly |
| **Injection into the agent** (thread text, or pasted agent output, reaching a run as instructions) | SI8: the only text that reaches a room is the confirmed span, attributed to the human who typed and confirmed it, queued as the room page's queue would. Nothing reaches a running agent live. Answers keep `answerText`'s escaping, so room text echoed in the thread fires no mention or link (invariant 7 unchanged) |
| **Impersonation** (pressing as someone else, or acting with the App's sponsor's rights) | Slack's signature on the event and the press (invariant 5); the user id comes from the signed payload; the press must be the reply's author (SI5); the broker resolves the human through `slack_links` and checks their own current standing per action (SI7), never the App's sponsor. A `not_linked` or `link_unverified` press does nothing |
| **Replay** (a redelivered press, a stale confirm pressed later, a button value copied to another message) | The in-memory press dedup (on `action_ts`); the 10-minute `expires` re-checked by the broker; the idempotency scope on the confirm message's channel and ts, so one confirm executes once even across a restart; the button value is a hint and the broker re-reads the room, its phase and the metadata's `textHash` (`binding_mismatch` otherwise) |
| **A classifier error leading to the wrong action** | No write without a confirm naming the action in fixed words (`stop the task`, `retry the task`, `queue for the next run: …`, `amend the task: …`); `confidence < 0.7` is `unknown`; phase guards refuse an action that makes no sense now (SI7). Cancel and expiry rates per intent are the precision signal (below); a rising rate is the trigger to raise the threshold or switch a channel off |
| **Cost** (a busy channel, a loop, a bot) | Tier 0 first; tier 1 only for linked users in channels that opted in; daily and per-user caps (SI13); bots' and the App's own events are dropped as today |
| **Privacy** (thread text leaving Slack) | Tier 1 sends one reply, no history, to the platform's own gateway; no reply is stored; logs carry a hash and a length (SI14) |

Invariant 8 becomes: *No reply executes anything. A reply may draw a confirm; only a signed press by
the reply's author, resolved by the broker to that human's standing and gated per action, executes a
stop, a retry, a queued steer or an amendment. The card's `forge.push` decision is unchanged.* Its
tests gain `TestReplyAloneNeverActs`, `TestIntentConfirmAuthorOnly`, `TestIntentSpanIsSubstring`.

## Observability

| Signal | Shape |
|---|---|
| `maku_slack_intents_total{tier, intent, outcome}` | `tier` `0`/`1`; `intent` the nine; `outcome` `answered`, `proposed`, `confirmed`, `cancelled`, `expired`, `refused`, `not_enabled`, `not_author`. Replaces nothing: `maku_slack_thread_answers_total` keeps counting tier 0 |
| `maku_intent_classify_total{outcome}`, `maku_intent_classify_duration_seconds` | `ok`, `low_confidence`, `invalid_output`, `timeout`, `capped`, `error` |
| `maku_slack_intent_actions_total{action, result}` | Each route's closed codes, as `slackDecide`'s are counted |
| Precision proxy | `cancelled + expired` over `proposed`, per intent: the dashboard's headline. Alert above 30 % over a day |
| Decision log | The room log records each executed action as `actor_id=human:<sub>`, `origin=slack`, the intent and, for steer and amend, the confirmed text. A cancelled or expired confirm is not a room event. The App logs each classification: room, intent, confidence, latency, model alias, text sha256 and length, never the text |

## Rollout

| Stage | What turns on | Exit evidence |
|---|---|---|
| 0 | This spec, CD25 marked superseded in ai-native-ref | Owner's merge |
| 1 | `intent_classify` in **shadow** on one channel: tier 1 runs and logs, the thread still gets today's unknown line | A week's tier 0 misses hand-labelled against the logged intents (by hash, from the author's own reading): write-intent precision ≥ 95 % |
| 2 | Read intents live on that channel | Unknown share down; no answer complaint |
| 3 | `stop` and `retry` routes, then `steer`, then `amend`, one flag per release, each proved live (on aws-0) before the next | Precision proxy under 30 %; every route's refusal codes seen once in a live proof |
| 4 | Other channels, each by its own `intents: true` | |

## Not in this spec

- **Class and harness-profile suggestion and the `profile:` override** at intake: the intake profile spec (SI11).
- **Live steering** of a running agent from Slack (SI8).
- **Approvals** other than the card's `forge.push`, and merges.
- **Multi-turn dialogue** or clarifying questions from the App.
- **Storing replies** to build an evaluation set (SI14).

## Open questions

1. Should tier 0's `stop` phrase offer the Confirm when `human.slackStop` is on? Tier 0 is "unchanged" (SI2), so today it still prints the label instructions, which reads oddly next to a tier 1 `stop the task` that acts.
2. Which gateway alias is the classifier's, and is its spend charged to the per-person cap of the reply's author or only to the App's principal?
3. Does a Slack `steer` count as having **prompted** the next run for four-eyes, as a queued room-page message does? (Proposed: yes, same rule.)
4. Is the 0.7 threshold, the 10-minute expiry and the 2,000-character input right, or should stage 1's data set them?
5. Who may `amend` a task filed by someone else: the issue's author only, or any maintainer (SI7 says either)?
6. Should the evaluation set in stage 1 be built from opt-in stored replies, given SI14 keeps none?
