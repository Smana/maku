<!-- page-type: explanation -->

# Roadmap

Feature-level direction only. Shipped milestones are recorded per release on the [releases page](https://github.com/Smana/maku/releases) and in [Upgrading](upgrading.md); what is proven live on the reference deployment is on the [status page](https://airef.ogenki.io/docs/status/).

## In progress

| Feature | What remains |
|---|---|
| Merge policies, from shadow to authority | Stage 3's exit: a week of dual decisions with no unexplained disagreement. Then a repository's cutover with central guardrails, a policy-bot rule generated from a class, `maku policy init`, a hold label, and simulation over real pull requests ([UX review](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-08-merge-policy-ux-review.md#the-proposal-ranked)) |
| The planner's grounding | It accepted a false premise in a request; a plan must check its claims against the repository |
| Natural-language intents in Slack rooms | Design, open ([#441](https://github.com/Smana/maku/pull/441)) |
| A suggested workflow and a workflow override at intake | Design, open ([#443](https://github.com/Smana/maku/pull/443)) |

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
