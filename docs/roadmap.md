# Roadmap

Feature level only. What shipped, release by release, is the
[releases page](https://github.com/Smana/maku/releases); what is proven live, and on which cluster,
is the platform's [status page](https://airef.ogenki.io/docs/status/).

## Landed

| Feature | Docs |
|---|---|
| Merge policies declared in your repository: read, simulation and dual evaluation; repository authority, approval memory, promotion from shadow and the settings editor, each behind its own switch and off by default | [Merge policies](merge-policies.md) |
| The merge gate's head check: an approved head no run reported waits as `head_unreported`, and the harness reports a head the agent pushed but left unreported | [The merge gate](factory.md#the-merge-gate) |
| Multi-repository coordination, plan mode: a validated plan across two public repositories, confirmed once, that files nothing. The integration runtime is built and inert | [Multi-repository coordination](factory.md#multi-repository-coordination) |
| Coordination alerts and the age of the oldest standing recovery | [Operations](operations.md#coordination) |
| Untrusted-input hardening contracts (stage H0): trust levels, safe-output and `forge_read` tools, the readiness port, the drill scorer, all off | [Design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md) |
| The room page: a header grouped by topic, an action on every need, folded briefs, settings in tabs | [Web UI](web-ui.md) |
| The factory from your coding agent: hand off, file, wait (`room_wait`) and check out (`room_checkout`) through the MCP server | [The MCP server](cli.md#the-mcp-server) |
| Slack: self-serve linking and the proposal message | [Slack App](slack.md) |

## In progress

| Feature | What remains |
|---|---|
| Merge policies, from shadow to authority | Stage 3's exit: a week of dual decisions with no unexplained disagreement. Then a repository's cutover with central guardrails, a policy-bot rule generated from a class, `maku policy init`, a hold label, and simulation over real pull requests ([UX review](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-08-merge-policy-ux-review.md#the-proposal-ranked)) |
| The planner's grounding | It accepted a false premise in a request; a plan must check its claims against the repository |

## Roadmap

| Feature | Trigger or dependency |
|---|---|
| Untrusted-input hardening, H1 to H5: audit, non-code outputs enforced, push enforced, reads filtered, ready | After H0; each stage has its own exit ([design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md)) |
| Multi-repository coordination, execute | Repository authority and hardening both ready ([prerequisites](factory.md#execution-prerequisites)) |
| OTel spans across bridge → broker → store, `traceparent` on every hop | `AGENTS.md`, Observability |
| The platform-agnostic decision after the UX sign-off | 2 of 4 triggers: daily use, AHP 1.0 without identity and audit, a second harness or runtime, outside demand (`AGENTS.md`, Seams) |
| A two-pane room page: the task panel on the left, the stream and the dock on the right, two tabs on a phone | Usage says people live in the page ([room readability](superpowers/specs/2026-10-15-room-readability-design.md#later-two-panes)) |
| A migration that drops `rooms.bridge_seen_at` | Every broker runs a release that no longer writes it ([room log](room-log.md#schema)) |
