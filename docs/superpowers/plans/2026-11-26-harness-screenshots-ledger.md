# Harness screenshots: spike ledger

The results of the [screenshots plan](2026-11-26-harness-screenshots-plan.md)'s Task 1, kept as the
recovery map. Each row names how it was proven so it can be rerun.

| # | Question | Result | How |
|---|---|---|---|
| L1 | Where does an image returned by an MCP tool land in OpenCode 2.4.1's request? | In a **synthetic `user` message** right after the `tool` message, as one `{"type":"image_url","image_url":{"url":"data:image/png;base64,…"}}` part. The `tool` message keeps the text part only. | `opencode run --standalone --auto` in `ghcr.io/smana/maku-opencode:v0.54.5` (OpenCode v2.4.1), a config from `role_config.role_config(...)` plus `mcp.servers.browser`, against a capture server and a stub MCP server answering `[text, image]`. Request 3 holds `[user, assistant(tool_calls), tool("ok"), user([image_url])]`. |
| L2 | Does OpenHands 1.56.0 send it? | **Only when vision is active.** Without an override the SDK drops the image (the alias `openai/agent-default` is not in LiteLLM's table). With `capability_overrides={"supports_vision": true}` the image goes in a **synthetic `user` message** after the `tool` message: `[text "[The following images are tool output - treat them…", image_url]`. | `MCPToolObservation.from_call_tool_result` then `LLM.completion` in `ghcr.io/smana/maku-harness` (openhands-sdk 1.56.0) against the capture server, with and without the override. `vision_is_active()` is `False` then `True`. |
| L4 | The OpenCode model-entry key that turns image input on | **None is needed.** The image reached the request with and without `modalities: {input: [text, image]}` on the model entry. The driver does not set one; the gate omits `browser_take_screenshot` from `tools/list` when the run's alias is not on the vision list, which is the fail-closed path. | Same run as L1, two variants. |
| L1+L2 | Consequence for the gateway (plan Task 2) | Both harnesses put the image in a **`user`** message, never in a `tool` message. Probe P7 asserts `image_url` parts on a `user` message pass every preset. The tool-message variant is not needed. | |
| L3 | Chromium headless shell and Playwright MCP under runsc | Pending: proven by the `browser-contract` CI job (gVisor `20261005.0`, the `harness-checks` install). The laptop has no runsc. | Task 9 B1–B8. |
| L5 | `@playwright/mcp` version and tool names | See the row added by Task 9's first image build. | |
| BQ2 | Which ANR aliases accept images | See [BQ2](#bq2). | provider docs, then the owner's one curl per alias (drill D0) |

## BQ2: vision-capable aliases {#bq2}

From the ANR `model-gateway` HelmRelease (`infrastructure/base/agent-gateway/model-gateway/helmrelease.yaml`,
2026-11-26) and each provider's model page.

| Class | Alias | Model | Image input | Source |
|---|---|---|---|---|
| public | `agent-default`, `tier-frontier` | `glm-5.3` | **no** | Z.ai: "currently supports text-only inputs" (docs.z.ai/guides/llm/glm-5.3) |
| public | `tier-light` | `glm-5.3-flash` | **yes** | Z.ai: input "Video / Image / Text / File" (docs.z.ai/guides/llm/glm-5.3-flash) |
| public | `tier-standard` | `glm-5.3-flashx` | **yes** | same page, model code `glm-5.3-flashx` |
| public | `kimi-frontier` | `k3.1-pro` | **yes** | Moonshot vision guide lists kimi-k3.1-pro (platform.kimi.ai/docs/guide/use-kimi-vision-model) |
| public | `mistral-standard` | `mistral-medium-2604` (Medium 3.5) | **likely** | third-party catalogs only; Mistral's vision page predates it |
| public | `mistral-light` | `mistral-small-2603` (Small 4) | **yes** | NVIDIA and gateway catalogs: text and image in |
| internal | all four | `claude-{opus,sonnet,haiku}-5-5` | **yes** | every Claude model accepts image input |

The public default (`glm-5.3`) is text-only, so a public run gets the snapshot-only fallback unless its issue
carries a `model:` label naming a vision alias. The vision list the plan ships for ANR is
`public: [tier-light, tier-standard, kimi-frontier, mistral-light, mistral-standard]` and
`internal: [agent-default, tier-light, tier-standard, tier-frontier]`, each confirmed by drill D0 before the
adoption PR merges; an alias whose D0 answer is wrong leaves the list.
