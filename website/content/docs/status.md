---
title: Status and roadmap
linkTitle: Status
weight: 85
diataxis: explanation
aliases:
  - /docs/roadmap/
description: "What is proven in live runs, what is implemented awaiting validation, what is in progress, the v5 roadmap, and known limitations."
---

Use this page to see what Maku can do today and what comes next. **Proven live** means demonstrated end to end on a real cluster and recorded in a runbook; release history is on the [releases page](https://github.com/Smana/maku/releases) and in [Upgrading]({{< relref "/docs/operate/upgrading.md" >}}). For the reference deployment's currently active cluster, pinned release tag, and GPU serving state, see [airef.ogenki.io › Status](https://airef.ogenki.io/docs/status/).

<span id="roadmap"></span><span id="v2-roadmap"></span>

## Recently shipped (v0.55–v0.58) {#recently-shipped}

| Feature | Reference |
|---|---|
| Upkeep routines: `Routine` CRD, 6-routine starter catalog, deterministic probes (`dead-links`, `renovate-red`, `actions-findings`), isolated `upkeepDailyUSD` budget, yield breaker, `propose`/`draft`/`pr` modes, repo prompts & skills, `/routines` overview/catalog/configurator UI, optional `routines.uiWrite`, `maku routine` CLI, and Slack upkeep channel & weekly digest | [Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}}) |
| Settings environment and version info: broker and factory versions (with skew indicator) and bounded cluster posture on `/settings` via `GET /api/install` | [Web UI]({{< relref "/docs/use/web-ui.md#settings-and-environment" >}}) |
| Harness visual verification: opt-in loopback `maku-browser` Playwright sidecar, vision aliases, and room screenshot strip | [Browser verification]({{< relref "/docs/use/browser-verification.md" >}}) |
| Opt-in loopback service sidecars: per-repository PostgreSQL and Redis sidecars inside sandboxed run pods | [The factory]({{< relref "/docs/concepts/factory.md" >}}) |
| Gemini and Vertex AI gateway presets, GitHub install egress window, and built-in `maven`/`node` egress allowlists | [Model gateway]({{< relref "/docs/install/gateway.md" >}}) |
| Dedicated Maku website (`maku.ogenki.io`) with interactive `/demo/` room replay, 12-chapter User Guide, and product ADRs | [User guide]({{< relref "/docs/guide/_index.md" >}}), [Decisions]({{< relref "/docs/decisions/_index.md" >}}) |

## Proven live {#landed}

These capabilities were demonstrated end to end on real Kubernetes clusters; evidence links point to the [ai-native-ref](https://github.com/Smana/ai-native-ref) validation runbooks and session logs.

| Capability | Evidence |
|---|---|
| A labelled issue becomes a reviewed pull request using `maku-run-controller`, `maku-broker`, `maku-factory`, `maku-gateway`, CRDs, and `maku-slack` | [End-to-end run](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/07-end-to-end.md), [Architecture]({{< relref "/docs/concepts/architecture.md" >}}) |
| Sandboxed gVisor runs receive per-run model and GitHub tokens, default-deny network policy, and egress allow-lists | [Security]({{< relref "/docs/concepts/security.md" >}}), [run-controller proof](https://github.com/Smana/ai-native-ref/pull/383) |
| People follow, steer, and approve in attended rooms; private-repository visibility follows GitHub permissions and the account link | [Web UI]({{< relref "/docs/use/web-ui.md" >}}), [private repositories](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/11-v1-validation.md) |
| A run lost to node or spot preemption resumes from its branch and room log | [Disruption recovery](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/10-disruption.md) |
| Link fixes and their reverts merge without a person under a live `MergePolicy`; a regression pauses the policy until agreed merges restore it | [Auto-merge and breaker](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/12-live-auto-merge-drill.md), [results](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/results-aws-0-2026-10-30.md) |
| A maintainer approves the exact current head to merge it through the merger App | [Approve-to-merge results](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/results-aws-0-2026-10-30.md) |
| Scoper, completion judge, repository checks, test-first contracts, and task splitting guide implementation | [Roles]({{< relref "/docs/concepts/roles.md" >}}), [tester runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/21-test-first-tester.md), [splitter runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/17-splitter.md) |
| Slack and local coding agents (`maku` MCP + `/maku` skill) start, follow, and inspect work | [Slack runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/22-slack.md), [coding agents runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/19-factory-from-your-agent.md) |
| RunLore investigates alerts, hands off fix proposals via signed webhooks, and records post-merge alert verdicts | [RunLore]({{< relref "/docs/concepts/runlore.md" >}}), [agent-to-agent runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/20-runlore-agent-to-agent.md), [KB runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/25-runlore-kb-phase-1.md) |
| Model calls are authenticated, routed by data class, and metered in USD with cache-aware pricing | [ADR-0053]({{< relref "/docs/decisions/0053-agent-router-on-agentgateway.md" >}}), [Providers]({{< relref "/docs/concepts/providers.md" >}}), [Budgets runbook](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/04-gateway-secrets-budgets.md) |
| Natural-language handoff with confirmation form and diff-driven shadow merge gate | [v4 validation](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/28-v4-aws-validation.md), [results](https://github.com/Smana/ai-native-ref/blob/main/docs/runbooks/agent-factory/results-aws-0-2026-11-16.md) |
| Upkeep routines catalog, `/routines` UI, preview/ceiling validation, and `/settings` environment and version panel | [Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}}), [Maku v0.58.0](https://github.com/Smana/maku/releases/tag/v0.58.0) |

<span id="class-modes"></span>

By default every starter merge policy runs in `shadow` mode, so every pull request waits for a maintainer's approval until a repository explicitly promotes a policy to `live`.

## Implemented, awaiting live validation {#experimental}

Built and shipped in Maku releases, awaiting broader live field evidence or switched off by default.

| Capability | Current state |
|---|---|
| Repository-declared merge policies | `MergePolicy` dual evaluation and Settings editor ship with `policyManagement.stage: manage`; repository cutover (`classAuthority: repository`) and remembered approvals stay opt-in. [Merge policies]({{< relref "/docs/use/merge-policies.md" >}}) |
| Conversational UX | Slack proposals work live; the unified needs list, return brief, Slack push approval, and animated room rail need wider live runs. [User guide › Follow and steer]({{< relref "/docs/guide/following-and-steering.md" >}}) |
| Multi-repository integration runtime | Planning works in `plan` mode; exact-commit worktrees and typed integration checks stay inert until repository authority and hardening ship. [Multi-repository]({{< relref "/docs/concepts/multi-repository.md" >}}) |
| Untrusted-input hardening | Stage H0 contracts ship off by default; stages H1–H5 follow incrementally. [Hardening]({{< relref "/docs/concepts/hardening.md" >}}) |
| Workflows as stage lists, step-up retries, and `plan-first` / `report` / `factory/review` acts | Shipped on `main`; awaiting full live run coverage across real repositories. [The factory]({{< relref "/docs/concepts/factory.md" >}}) |
| OpenCode as an opt-in harness | Maintainer `harness:opencode` label and 50/50 trial routing ship; OpenHands remains the default pending parity results. [OpenCode harness]({{< relref "/docs/concepts/opencode-harness.md" >}}) |
| Upkeep routines scheduled yield and breaker trips | All six starter `Routine` objects, `/routines`, catalog, and preview validation ship; measuring rolling merge yield and live breaker trips on active repositories is next. [Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}}) |
| Harness visual verification and loopback service sidecars | Opt-in `maku-browser` Playwright sidecar and loopback Postgres/Redis sidecars ship in v0.58 and await live repository validation. [Browser verification]({{< relref "/docs/use/browser-verification.md" >}}) |
| Laptop quickstart outside-user checks | `task demo` runs end to end; outside-user onboarding checks on fresh laptops remain. [Quickstart]({{< relref "/docs/get-started/quickstart.md" >}}) |

## In progress {#in-progress}

<span id="v3x"></span>

| Work | Where it stands |
|---|---|
| Merge policies, from shadow to authority | Closing Stage 3 (a week of dual decisions with no unexplained disagreement), then repository cutover with central guardrails, generated policy-bot rules, `maku policy init`, a hold label, and simulation over real pull requests ([UX review](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-08-merge-policy-ux-review.md#the-proposal-ranked)) |
| Planner grounding | Checking factual claims in a request against the repository before proposing an issue |
| Untrusted-input hardening (H1–H5) | Audit, enforced non-code outputs, enforced pushes, filtered reads, and readiness drills ([Hardening]({{< relref "/docs/concepts/hardening.md" >}})) |
| Natural-language intents in Slack rooms | Design open ([#441](https://github.com/Smana/maku/pull/441)) |
| Workflow and model trials | Comparing `quick`, `standard`, and `careful` workflows by cost per merged pull request and merge rate ([Design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-14-workflows-review-design.md)) |

## Agent Factory v5: proven in the field {#v5}

v4 lets anyone run Maku standalone; v5 proves Maku with an external GitHub and Kubernetes engineering team on real private repositories as an Apache-2.0 open-source collaboration ([Tier-1 Pilot]({{< relref "/docs/get-started/pilot.md" >}}), [v5 roadmap spec](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-22-v5-roadmap-design.md)).

| Goal | What it takes |
|---|---|
| **Pre-pitch readiness and Tier-1 database adoption** | Private-repository planning, Tier-1 hardened bundled PostgreSQL (PVC, TLS, automatic three-role bootstrap, optional S3/GCS backups) plus a one-step Tier-2 CNPG/external-DB bootstrapper, one recorded generic Kubernetes Helm install, and outside-user laptop quickstart checks ([Quickstart]({{< relref "/docs/get-started/quickstart.md" >}})) |
| **External team pilot on private repositories** | One GitHub and Kubernetes team runs a Tier-1 Pilot (one GitHub App, shadow merge gate, bundled PostgreSQL, standard OIDC/Ingress or Slack Socket Mode, mandatory gVisor) and lands at least ten merged pull requests; hardening (H1–H5), team RBAC (`write` access and GitHub Teams), and per-repository runtime images land alongside the team's stack |
| **Pluggable three-tier harnesses, OpenCode default** | Turn `AgentRun.spec.harness` into a three-tier contract: OpenCode as the slim default once parity passes ([evaluation](https://github.com/Smana/ai-native-ref/pull/404)), OpenHands as the proven fallback, and best-effort bring-your-own Claude Code |
| **Upkeep routines in production** | Bind starter and custom routines (`dead-links`, `renovate-red`, `doc-drift`, and repository hygiene) across `propose` → `draft` → `pr`, track rolling merge yield and `$/merged` via the weekly Slack digest, and prove the yield breaker live ([Upkeep routines]({{< relref "/docs/use/upkeep-routines.md" >}})) |
| **Slack security model and Socket Mode** | Hybrid personal-bound principals over one workspace bot (no personal Slack user tokens): DM intake with dynamic per-user repository access and per-person USD budgets, author-only thread isolation, Socket Mode as the zero-public-ingress default, and broker-verified `X-Slack-Signature` in Events API mode |
| **Spec-driven development intake** | Ingest open markdown specs (GitHub Spec Kit, OpenSpec, or EARS sections in issues or `specs/`) natively when present, falling back to a minimal compatible Maku spec written by the planner and scoper |
| **Outcome analytics and live merge proof** | Join merge rate, revert rate, human wait time, and USD cost per merged pull request into one view by repository, policy, model, and workflow; prove at least one engineering class beyond `docs-links` (`deps-patch` or `docs`) live with a revert drill and a second-family reviewer ([ADR-0058]({{< relref "/docs/decisions/0058-reviewer-on-another-model.md" >}})) |
| **Time-boxed `kagent` and Agent Substrate check** | After the first external team pilot ships, spend at most two days rerunning [ADR-0062]({{< relref "/docs/decisions/0062-agent-runtime-remains-agent-sandbox.md" >}})'s comparison against current releases and reconsideration triggers |

## Next {#later}

<span id="v4"></span><span id="v4-core"></span><span id="v4-increments"></span>

| Direction | Trigger or dependency |
|---|---|
| PR continuation loop, cloud-to-local takeover, and review inbox | Partner-triggered v5.x fast-follows: fix inline review comments and merge conflicts, pull stalled runs locally (`maku takeover`), and give maintainers one risk-ranked review queue |
| Multi-repository coordination execution | Repository merge authority and hardening both live ([prerequisites]({{< relref "/docs/concepts/multi-repository.md#execution-prerequisites" >}})) |
| OTel spans across bridge → broker → store (`traceparent` on every hop) | End-to-end distributed traces across rooms and factory (`AGENTS.md`, Observability) |
| Two-pane room page | Task panel on the left, live stream and dock on the right ([room readability design](https://github.com/Smana/maku/blob/main/docs/superpowers/specs/2026-10-15-room-readability-design.md#later-two-panes)) |
| Recorded installs beyond the reference (GKE and kubeadm) | Chart defaults proved on standalone clusters beyond the reference deployment |
| Reviewer calibration, GitLab/Jira/Linear, event triggers, signed provenance, and Gemini evaluation | Partner demand or funded eval bench; Gemini preset already ships in `maku-gateway` with per-role permissions and cost checks next |
| Platform-agnostic spin-out review | Reached when 2 of 4 triggers hold: daily use, AHP 1.0 still omitting identity/audit, second harness/runtime needed, outside demand (`AGENTS.md`, Seams) |
| Schema cleanup: drop `rooms.bridge_seen_at` | Once every running broker no longer writes it ([room log]({{< relref "/docs/reference/room-log.md#schema" >}})) |

## Known limitations {#before-v3}

- The conversational planner reads public repositories only; for private repositories, file the issue on GitHub or through `maku`'s local issue flow (private-repository planning is part of v5 pre-pitch readiness).
- A completion judge can quote a CI log only when the sandbox's egress allow-list includes GitHub's Actions log host.
- Some final costs settle after task status is written ([maku#279](https://github.com/Smana/maku/issues/279), [maku#280](https://github.com/Smana/maku/issues/280)); a few approval-binding edge cases remain ([maku#304](https://github.com/Smana/maku/issues/304)).
- Mistral can spend an entire run budget on a small change and is not recommended as an implementer.
