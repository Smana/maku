---
title: Model providers
linkTitle: Providers
weight: 80
description: "How Maku isolates model providers by data class, configures per-role provider overrides for public work, and pairs hosted APIs with optional self-hosted serving."
diataxis: explanation
---

Maku is model-agnostic: `maku-gateway` ships presets for OpenAI-compatible and Anthropic-compatible APIs (Anthropic, Z.ai, Moonshot Kimi, Mistral, OpenAI, Gemini, Vertex AI, or any custom self-hosted endpoint; see [The model gateway]({{< relref "/docs/install/gateway.md#presets" >}})). Every run asks for `agent-default` unless its workflow or role picks another alias, and each run's cost is metered at the served model's configured price in `meter.prices` ([Budgets]({{< relref "/docs/reference/budgets.md" >}})).

## Data-class separation {#data-classes}

Each repository and task carries a **data class**: `public` or `internal`. Providers approved only for public repositories are isolated structurally across three layers:

1. **Gateway listeners:** `maku-gateway` exposes separate `public` (`:8080`) and `internal` (`:8081`) listeners with distinct JWT audiences (`maku-gateway.public.<alias>` vs `maku-gateway.internal.<alias>`). A public-only alias does not exist on the `internal` listener.
2. **Run admission:** `maku-run-controller` refuses an `internal` `AgentRun` that names a public-only provider alias ([Roles]({{< relref "/docs/concepts/roles.md#profiles" >}})).
3. **Factory config validation:** `maku-factory` applies public-only per-role overrides to `public` tasks only.

Example (the [ai-native-ref](https://airef.ogenki.io/) reference deployment): `agent-default` maps to GLM on Z.ai for `public` tasks and Claude on the Anthropic API for `internal` tasks, with Moonshot Kimi and Mistral configured as opt-in public-only aliases:

| Alias | Model (reference example) | Tier | Processed in |
|---|---|---|---|
| `kimi-frontier` | Kimi K3.1 Pro (`k3.1-pro`) | frontier | China |
| `mistral-standard` | Mistral Medium 3.5 (`mistral-medium-2604`) | standard | EU |
| `mistral-light` | Mistral Small 4 (`mistral-small-2603`) | light | EU |

Field note from live runs: Mistral is **not recommended as an implementer** because it can spend a run's full token budget on a small change.

## Selecting a second provider per role {#switch-a-workflow-on}

A workflow can run its reviewer (or judge) on a second model family so correlated mistakes do not slip past review ([ADR-0058]({{< relref "/docs/decisions/0058-reviewer-on-another-model.md" >}})). Configure `models.<role>` on a workflow in your `Factory` resource:

```yaml
workflows:
  standard:
    models: {reviewer: kimi-frontier}
```

| Rule | Why |
|---|---|
| Each alias serves its own tier only; config load refuses cross-tier mismatches | Run caps stay aligned with the tier and the gateway's hourly rate limit |
| Any role without an override keeps the default alias | No unintended change to existing workflows |
| An implementer stage's `judge` may name a light alias (for example `mistral-light`) | Judges run a single fresh-context check |
| Every served model has a row in `meter.prices` (including cache reads) | Dollar caps and spend attribution stay accurate |

A run never switches model mid-conversation, and there is no automatic failover to another provider: a provider `429` ends the run cleanly rather than silently moving data to a different processor.

**Provider quirk (Mistral):** `agentgateway` v1.6 expects Chat Completions `content` as a string, whereas Mistral reasoning models return structured content chunks; `maku-gateway`'s Mistral preset forces `reasoning_effort: "none"`.

## Hosted APIs vs self-hosted models {#why-self-host-at-all}

Hosted frontier APIs are faster to adopt, cheaper at moderate volume, and stronger on hard coding tasks. Self-hosted open-weights serving (such as vLLM on cluster GPUs) makes sense when you need:

- **Zero external egress:** prompts never leave your cluster network.
- **Weight pinning:** model weights are pinned to an exact commit SHA rather than silently updated by a vendor.
- **Dedicated latency:** warm in-cluster replicas for sub-200 ms editor tab completion.

For a full GitOps reference stack that pairs Maku with self-hosted GPU model serving on Kubernetes (including autoscaling and the four-GPU cost floor), see [AI Native Reference › Model serving](https://airef.ogenki.io/docs/serving/).
