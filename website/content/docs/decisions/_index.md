---
title: Architecture decisions
linkTitle: Decisions
weight: 70
description: "Maku's architecture decision records (0041–0062) — what was chosen, what was rejected, and what would reopen each decision."
---

These architecture decision records originated in [cloud-native-ref](https://cnref.ogenki.io/docs/decisions/) and [ai-native-ref](https://airef.ogenki.io/docs/decisions/) and moved here keeping their original numbers. Next Maku ADR: **0063**.

Model-serving and host-deployment ADRs (`0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`) stay on [airef.ogenki.io](https://airef.ogenki.io/docs/decisions/). Host infrastructure decisions (Flux, Cilium, OpenBao, cloud IAM) stay on [cnref.ogenki.io](https://cnref.ogenki.io/docs/decisions/).

Because ADRs are immutable historical records, the entries below preserve their original wording and pre-rebrand identifiers (`Smana/agent-platform`, `roomctl`, cluster examples) with dated amendment and supersession notes where later decisions evolved them.

## The records

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [0041]({{< relref "/docs/decisions/0041-agent-sandbox-gvisor-al2023.md" >}}) | Coding agents run in agent-sandbox Sandboxes under gVisor, on a dedicated spot pool per cloud, with OpenHands as the harness profile | Partly superseded: runs are `maku-run-controller` pods; gVisor and OpenHands stand | 2026-09-26 |
| [0042]({{< relref "/docs/decisions/0042-agent-router-identity-gateway.md" >}}) | Agent Router is the agents' identity gateway, with role and data class encoded in the token audience and an in-pod proxy holding the tokens | Partly superseded by [0053]({{< relref "/docs/decisions/0053-agent-router-on-agentgateway.md" >}}) and `maku-gateway` | 2026-09-26 |
| [0043]({{< relref "/docs/decisions/0043-octo-sts-for-agent-github-tokens.md" >}}) | Agents get GitHub tokens from a self-hosted octo-sts, scoped per repository and role, and two rulesets confine their App to agent branches and no tags | Partly superseded: `maku-factory` mints per-run tokens; branch and tag rulesets stand | 2026-09-26 |
| [0044]({{< relref "/docs/decisions/0044-room-session-protocol.md" >}}) | Rooms are an AHP-shaped log we own, stored in PostgreSQL, fanned out with Postgres LISTEN/NOTIFY | Accepted | 2026-09-27 |
| [0045]({{< relref "/docs/decisions/0045-merge-policy-gate.md" >}}) | palantir/policy-bot, required through a repository ruleset, decides which agent PRs merge | Accepted | 2026-09-27 |
| [0048]({{< relref "/docs/decisions/0048-agent-factory-orchestrator.md" >}}) | A custom Task controller with Kueue admission orchestrates the Agent Factory | Partly superseded: namespace `ResourceQuota` replaces Kueue; the Task controller stands | 2026-09-27 |
| [0049]({{< relref "/docs/decisions/0049-room-client-and-human-auth.md" >}}) | Humans reach rooms through a web UI the broker serves, behind oauth2-proxy | Accepted (amended 2026-11-05) | 2026-09-27 |
| [0051]({{< relref "/docs/decisions/0051-otel-collector-agent-trace-gate.md" >}}) | An OpenTelemetry Collector is the agent trace gate | Accepted | 2026-09-27 |
| [0053]({{< relref "/docs/decisions/0053-agent-router-on-agentgateway.md" >}}) | The agent router runs on agentgateway; ai-gateway stays on Envoy Gateway | Partly superseded: runs call `model-gateway`; agentgateway stands | 2026-10-01 |
| [0056]({{< relref "/docs/decisions/0056-local-first-factory-ux.md" >}}) | The factory is driven from the developer's local agent, and a room is as visible as its repo | Partly superseded by [0059]({{< relref "/docs/decisions/0059-factory-from-your-coding-agent.md" >}}) | 2026-10-08 |
| [0058]({{< relref "/docs/decisions/0058-reviewer-on-another-model.md" >}}) | The factory's reviewer on public tasks keeps the implementer's GLM model | Accepted; amended with a Kimi reviewer on self-merging classes | 2026-10-16 |
| [0059]({{< relref "/docs/decisions/0059-factory-from-your-coding-agent.md" >}}) | The factory is started, followed, and brought back from the developer's coding agent | Accepted | 2026-10-25 |
| [0060]({{< relref "/docs/decisions/0060-conversational-intake.md" >}}) | A request in plain words becomes a governed plan before any issue is filed, through one planner for every channel | Accepted | 2026-11-05 |
| [0061]({{< relref "/docs/decisions/0061-agents-write-through-safe-outputs.md" >}}) | Agent runs hold a read-only GitHub token and write only through outputs the factory validates and applies | Proposed | 2026-11-07 |
| [0062]({{< relref "/docs/decisions/0062-agent-runtime-remains-agent-sandbox.md" >}}) | Keep agent-sandbox and OpenHands; defer both kagent and Agent Substrate | Partly superseded: runs are `maku-run-controller` pods; kagent and Substrate stay deferred | 2026-11-11 |
