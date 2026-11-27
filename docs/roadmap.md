<!-- page-type: explanation -->

# Roadmap

Feature-level direction only. Shipped milestones are recorded per release on the [releases page](https://github.com/Smana/maku/releases) and in [Upgrading](upgrading.md); what is proven live on the reference deployment is on the [status page](https://airef.ogenki.io/docs/status/).

## Recently shipped (v0.55–v0.58)

| Feature | Reference |
|---|---|
| Upkeep routines: `Routine` CRD, 6-routine starter catalog, deterministic probes (`dead-links`, `renovate-red`, `actions-findings`), isolated `upkeepDailyUSD` budget, yield breaker, `propose`/`draft`/`pr` modes, repo prompts & skills, `/routines` overview/catalog/configurator UI, optional `routines.uiWrite`, `maku routine` CLI, and Slack upkeep channel & weekly digest | [Upkeep routines](upkeep-routines.md) |
| Settings environment and version info: broker and factory versions (with skew indicator) and bounded cluster posture on `/settings` via `GET /api/install` | [Web UI](web-ui.md#settings-and-environment) |
| Harness visual verification: opt-in loopback `maku-browser` Playwright sidecar, vision aliases, and room screenshot strip | [Browser verification](browser-verification.md) |
| Opt-in loopback service sidecars: per-repository PostgreSQL and Redis sidecars inside sandboxed run pods | [The factory](factory.md) |
| Gemini and Vertex AI gateway presets, GitHub install egress window, and built-in `maven`/`node` egress allowlists | [Model gateway](gateway.md) |

## In progress

| Feature | What remains |
|---|---|
| Merge policies, from shadow to authority | Stage 3's exit: a week of dual decisions with no unexplained disagreement. Then a repository's cutover with central guardrails, a policy-bot rule generated from a class, `maku policy init`, a hold label, and simulation over real pull requests ([UX review](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-08-merge-policy-ux-review.md#the-proposal-ranked)) |
| The planner's grounding | It accepted a false premise in a request; a plan must check its claims against the repository |
| Natural-language intents in Slack rooms | Design, open ([#441](https://github.com/Smana/maku/pull/441)) |

## Agent Factory v5: proven in the field

v4 lets anyone run the factory; v5 proves Maku with one external GitHub and Kubernetes engineering
team on its own private repositories as an Apache-2.0 open-source collaboration ([the platform's v5
goals](https://airef.ogenki.io/docs/status/#v5), [v5 roadmap
spec](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-22-v5-roadmap-design.md),
[v5 challenge specs](https://github.com/Smana/ai-native-ref/pull/488)).

| Feature | What it takes |
|---|---|
| Pre-pitch readiness and Tier-1 database adoption | Private-repository planning, Tier-1 hardened bundled PostgreSQL (PVC, TLS, automatic three-role bootstrap, optional S3/GCS backups) plus a one-step Tier-2 CNPG or external-database bootstrapper, one recorded generic Kubernetes Helm install, outside-user laptop quickstart checks ([Quickstart](quickstart.md)), a standalone front door, a two-minute real-run video, and a static room replay |
| External team pilot on private repositories | One GitHub and Kubernetes team runs a Tier-1 Pilot (one GitHub App, shadow merge gate, bundled PostgreSQL, standard OIDC/Ingress or Slack Socket Mode, mandatory gVisor) and lands at least ten merged pull requests; hardening (H1–H5), team RBAC (`write` access and GitHub Teams), and per-repository runtime images land alongside the team's stack; also satisfies the "outside demand" trigger of the platform-agnostic decision below |
| Pluggable three-tier harnesses, OpenCode default | Turn `AgentRun.spec.harness` into a three-tier contract: OpenCode as the slim default once parity passes ([evaluation](https://github.com/Smana/ai-native-ref/pull/404)), OpenHands as the proven fallback, and best-effort bring-your-own Claude Code |
| Upkeep routines field validation | Bind starter and custom routines on live repositories across `propose` → `draft` → `pr`, measure merge yield and `$/merged` over real runs, and prove the yield breaker end to end ([Upkeep routines](upkeep-routines.md)) |
| Slack security model and Socket Mode | Hybrid personal-bound principals over one workspace bot (no personal Slack user tokens): direct-message intake with dynamic per-user repository access and per-person USD budgets, strict author-only thread isolation, Socket Mode as the zero-public-ingress default (unsigned `forge.push` buttons off by default), and broker-verified `X-Slack-Signature` closing the Slack deputy residual in Events API mode while keeping live steering on the room page |
| Spec-driven development intake | Ingest open markdown specs (GitHub Spec Kit, OpenSpec, or EARS sections in issues or `specs/`) natively when present, falling back to a minimal compatible Maku spec written by the planner and scoper |
| Outcome analytics and live merge proof | Join merge rate, revert rate, human wait time, and USD cost per merged pull request into one view by repository, policy, model, and workflow; prove at least one engineering class beyond `docs-links` (`deps-patch` or `docs`) live with a revert drill and a second-family reviewer |
| Time-boxed `kagent` and Agent Substrate check | After the first external team pilot ships, spend at most two days rerunning [ADR-0062](https://airef.ogenki.io/docs/decisions/0062-agent-runtime-remains-agent-sandbox/)'s comparison against current releases and its triggers, then adopt, borrow from, or keep deferring each |

## Next

| Feature | Trigger or dependency |
|---|---|
| Untrusted-input hardening live: H2 to H5 on a first repository, two consecutive passing weekly drills, then T5 promoted per type | H1's exit: at most 1% of legitimate outputs refused by T1 to T4 under audit; each stage has its own exit ([Hardening](hardening.md)) |
| Multi-repository coordination, execute | Repository authority and hardening both ready ([prerequisites](multi-repository.md#execution-prerequisites)) |
| OTel spans across bridge → broker → store, `traceparent` on every hop | `AGENTS.md`, Observability |
| The platform-agnostic decision after the UX sign-off | 2 of 4 triggers: daily use, AHP 1.0 without identity and audit, a second harness or runtime, outside demand (`AGENTS.md`, Seams) |
| A two-pane room page: the task panel on the left, the stream and the dock on the right, two tabs on a phone | Usage says people live in the page ([room readability](superpowers/specs/2026-10-15-room-readability-design.md#later-two-panes)) |
| Recorded installs beyond the reference: the chart's defaults as the product path, proved on GKE and kubeadm | Agent Factory [v4.0.0](https://github.com/Smana/ai-native-ref/releases/tag/v4.0.0) shipped on the reference GKE and EKS clusters; the recorded GKE and kubeadm installs come next ([the v4.0 decision pack](superpowers/v4-readiness.md)) |
| A migration that drops `rooms.bridge_seen_at` | Every broker runs a release that no longer writes it ([room log](reference/room-log.md#schema)) |
