# Roadmap

Feature level only. What shipped, release by release, is the
[releases page](https://github.com/Smana/maku/releases); what is proven live, and on which cluster,
is the platform's [status page](https://airef.ogenki.io/docs/status/).

As of v0.49.0.

## Landed

| Feature | Docs |
|---|---|
| Merge policies declared in your repository: read, simulation and dual evaluation; repository authority, approval memory, promotion from shadow and the settings editor, each behind its own switch and off by default | [Merge policies](merge-policies.md) |
| The merge gate's head check: an approved head no run reported waits as `head_unreported`, and the harness reports a head the agent pushed but left unreported | [The merge gate](factory.md#the-merge-gate) |
| Multi-repository coordination, plan mode: a validated plan across two public repositories, confirmed once, that files nothing. The integration runtime is built and inert | [Multi-repository coordination](factory.md#multi-repository-coordination) |
| Coordination alerts and the age of the oldest standing recovery | [Operations](operations.md#coordination) |
| Untrusted-input hardening contracts (stage H0): trust levels, safe-output and `forge_read` tools, the readiness port, the drill scorer, all off | [Design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md) |
| Untrusted-input hardening, audit (stage H1): every agent write shadowed and checked, a canary in writer briefs, `status.inputTrust`, the agent-guard client; off by default, nothing refused | [Hardening, audit](factory.md#hardening-audit) |
| Untrusted-input hardening, non-code outputs (stage H2): `pr`, `comment`, `labels` and `issue` requested through `output_*` tools and applied by the factory, per repository; the tracer and the permission gauge; nothing enforced unless a repository is listed | [Hardening, non-code outputs enforced](factory.md#hardening-non-code-outputs-enforced) |
| Untrusted-input hardening, reads filtered and the drills (stage H4): `forge_read`, no `api.github.com` egress at mode `enforce`, `maku drill plant` and the factory's drill scorer | [Hardening, reads filtered](factory.md#hardening-reads-filtered), [Injection drills](drills.md) |
| Untrusted-input hardening, ready (stage H5): per-repository readiness reported (gauge, API, Evidence page), coordination's execute gated on class authority and readiness, T5 promoted per output type | [Hardening, ready](factory.md#hardening-ready) |
| Untrusted-input hardening, push enforced (stage H3): `git push` becomes a bundle the factory inspects and pushes as the publisher, a protected path waits for a maintainer's approval of its digest, and the tracer traces pushes; per repository, off by default | [Hardening, push enforced](factory.md#hardening-push-enforced) |
| The room page: a header grouped by topic, an action on every need, folded briefs, settings in tabs | [Web UI](web-ui.md) |
| The factory from your coding agent: hand off, file, wait (`room_wait`) and check out (`room_checkout`) through the MCP server | [The MCP server](cli.md#the-mcp-server) |
| Slack: self-serve linking and the proposal message | [Slack App](slack.md) |
| The factory on a laptop: `task demo`, the `charts/agent-factory` umbrella chart and the quickstart | [Quickstart](quickstart.md) |
| Factory resources: `Factory`, `Repository` and `MergePolicy` objects the factory reads live, with the last good version kept on a bad edit, and `maku config migrate` and `validate` (v0.41.x) | [Merge policies](merge-policies.md) |
| Team evaluation profiles with outcome metrics | [Web UI](web-ui.md) |
| A form that never answers: `issue_file` falls back to the draft after 60 s, and the handoff skill checks the repository first (v0.42.0) | [The MCP server](cli.md#the-mcp-server) |
| Slack quick wins: a refused first mention is explained, planner progress shows, the closed phrases are listed (v0.42.0) | [Slack App](slack.md) |
| The chart contract, v4 rollout step 1: the `agent-factory-crds` chart, the demo in `values-demo.yaml`, a values schema, `kubeVersion` 1.30 or later, the umbrella published and signed | [Upgrading](upgrading.md#the-chart-contract-v04x-v4-rollout-step-1) |
| The run path, v4 rollout step 2: the scoper and the loop policies on the run controller, read-only tokens for reviewers and triagers, the sandbox value and the preflight, the runs quota, the run-pod admission policy, and the `kubernetes` network mode with its egress proxy | [Upgrading](upgrading.md#the-run-path-v04x-v4-rollout-step-2), [Security](security.md#the-run-paths-network-modes) |
| Reusable merge policies: a `MergePolicy` is one class definition, a `Repository` binds it with its mode and parameters, each binding resolved and refused on its own; `Factory.spec.mergeDefaults`; status per binding and per policy; grants bound to the resolved class; `maku config migrate` with its equality proof and `validate --base-ref`; the settings pages per binding and per policy | [Merge policies](merge-policies.md#factory-resources), [Upgrading](upgrading.md#reusable-merge-policies-v04x-release-n) |
| Reusable merge policies, the per-repository shape removed: the CRD's `repositoryRef`, `classes` and `merge` on `MergePolicy`, `maku config migrate --manifests`, and the repository file's `classes:` and `conditions.any`, refused | [Upgrading](upgrading.md#the-per-repository-merge-policy-removed-v04x-release-n1) |
| Install UX, v4 rollout step 7: the preflight's cluster checks as a Helm test hook, private code by explicit opt-in (warned), both GitHub Apps through the manifest flow, `maku install --print`, the demo through the model gateway, and the install page (v0.48.0) | [Install on your cluster](install.md) |
| The clean-up, v4 rollout step 8: the paths steps 1 to 7 kept for older installs removed and their defaults flipped to the product path, one migration note, and the values contract reported on each chart pull request (v0.48.0 and v0.49.0; PR F, the ZITADEL reader, is open in [#581](https://github.com/Smana/maku/pull/581) and ships in the next minor) | [Upgrading](upgrading.md#the-clean-up-v04x-v4-rollout-step-8) |
| The merge gate as a required status, v4 rollout step 3 (D11): `merge.policyBot: false` outside the demo, `maku/merge-gate` `success` only when the factory merges, a documented ruleset and its observer, and `agent_merged` | [Branch protection for the merge gate](factory.md#branch-protection-for-the-merge-gate) |

## In progress

| Feature | What remains |
|---|---|
| Merge policies, from shadow to authority | Stage 3's exit: a week of dual decisions with no unexplained disagreement. Then a repository's cutover with central guardrails, a policy-bot rule generated from a class, `maku policy init`, a hold label, and simulation over real pull requests ([UX review](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-08-merge-policy-ux-review.md#the-proposal-ranked)) |
| The planner's grounding | It accepted a false premise in a request; a plan must check its claims against the repository |
| Natural-language intents in Slack rooms | Design, open ([#441](https://github.com/Smana/maku/pull/441)) |
| A suggested workflow and profile override at intake | Design, open ([#443](https://github.com/Smana/maku/pull/443)) |

## Roadmap

| Feature | Trigger or dependency |
|---|---|
| Untrusted-input hardening live: H2 to H5 on a first repository, two consecutive passing weekly drills, then T5 promoted per type | H1's exit: at most 1% of legitimate outputs refused by T1 to T4 under audit; each stage has its own exit ([design](https://github.com/Smana/ai-native-ref/blob/main/docs/superpowers/specs/2026-11-07-untrusted-input-hardening-design.md)) |
| Multi-repository coordination, execute | Repository authority and hardening both ready ([prerequisites](factory.md#execution-prerequisites)) |
| OTel spans across bridge → broker → store, `traceparent` on every hop | `AGENTS.md`, Observability |
| The platform-agnostic decision after the UX sign-off | 2 of 4 triggers: daily use, AHP 1.0 without identity and audit, a second harness or runtime, outside demand (`AGENTS.md`, Seams) |
| A two-pane room page: the task panel on the left, the stream and the dock on the right, two tabs on a phone | Usage says people live in the page ([room readability](superpowers/specs/2026-10-15-room-readability-design.md#later-two-panes)) |
| v4.0.0, the production path: the chart's defaults are the product path, proved on GKE and kubeadm | Not cut now (the owner, 2026-11-14). Five further specs, planned for the week of 2026-11-16; step 8 PR F in the next minor; the recorded GKE and kubeadm installs on it, once gcp-0 is rebuilt; then the owner's go ([the v4.0 decision pack](v4-readiness.md)) |
| A migration that drops `rooms.bridge_seen_at` | Every broker runs a release that no longer writes it ([room log](room-log.md#schema)) |
