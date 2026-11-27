# Maku website PR 2: Migrate User Guide, Concepts, 15 Product ADRs & Product Status — Implementation Plan

> **For agentic workers:** Execute task-by-task in a worktree branched from `origin/main` in `Smana/maku` **after PR 1 merges and `https://maku.ogenki.io/` is live**. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `https://maku.ogenki.io/` the complete, self-contained home of every Maku product document by migrating and generalizing the 12-chapter User Guide + Cheat Sheet, unique `how-it-works` explanations & Draw.io diagrams, the 15 Maku product ADRs (`0041`–`0062`), and the Product Status & Roadmap page from `Smana/ai-native-ref`.

**Architecture:** One home per fact. The 12-chapter User Guide lives under `website/content/docs/guide/` as a progressive developer handbook (generalized off `cloud-native-ref`/Tailscale/ZITADEL specifics and linking `use/` & `reference/` instead of restating tables); unique architecture explanations & diagrams fold into `website/content/docs/concepts/`; the 15 product ADRs keep their original numbers under `website/content/docs/decisions/` (exempt from historical-ID/cluster-name guards); and `website/content/docs/status.md` (alias `/docs/roadmap/`) owns Product Status & Roadmap citing `ai-native-ref` runbooks as external evidence.

**Tech Stack:** Hugo Extended 0.168.0, Hextra v0.12.3, `hack/docs.sh`, `hack/vocabulary.sh`, `hack/names.sh`, `hack/site-check.sh`, Playwright.

**Spec:** [`docs/superpowers/specs/2026-11-27-maku-website-design.md`](../specs/2026-11-27-maku-website-design.md)

---

## Global Constraints

- Execute in `Smana/maku` after PR 1 is merged. Read source pages directly from `Smana/ai-native-ref/website/content/docs/` (commit pinned in PR description).
- Zero host-specific assumptions (`aws-0`, `gcp-0`, `tailnet`, `ZITADEL` as only IdP, `priv.aws.ogenki.io`) outside labelled examples or immutable historical ADRs under `website/content/docs/decisions/`.
- Obey Diátaxis word ceilings (`hack/docs.sh`): `tutorial` / `how-to` ≤ 1,500 words, `explanation` / `operations` ≤ 4,000 words, `chapter` ≤ 1,200 words (or existing chapter ceiling).
- Every internal link uses `{{< relref >}}` (`refLinksErrorLevel: ERROR`). Never leave any `{{< maku ... >}}` shortcode behind from `ai-native-ref`.
- Every URL that PR 3's `redirects.yaml` in `ai-native-ref` will point to must exist and resolve in `website/public/` before PR 2 merges.
- `task check` must exit 0 before opening the PR.
- Commits are conventional, in English, with no co-author or agent-attribution trailers.

## Review Focus

1. **Generalization quality:** Check User Guide Chapter 2 (`before-your-first-task.md`) and Chapter 8 (`terminal-and-coding-agent.md`) — host CA / Tailscale / ZITADEL steps must read cleanly for any OIDC + Ingress/Gateway Maku install (keeping any cluster-specific detail as a short labelled example).
2. **No duplication with `use/` and `reference/`:** Chapters 8, 11, and 12 (`terminal-and-coding-agent`, `merge-policies`, `upkeep-routines`) stay concise developer walkthroughs linking to `use/cli.md`, `use/merge-policies.md`, `use/upkeep-routines.md`, and `reference/`.
3. **ADR immutability & lineage:** The 15 migrated ADRs preserve their historical text and original numbers (`0041`–`0062`), link retained ANR ADRs (`0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`) via `https://airef.ogenki.io/docs/decisions/...`, and never fail `hack/docs.sh` or `hack/names.sh`.
4. **Status purity:** `website/content/docs/status.md` states product maturity & links `ai-native-ref` runbooks as external evidence without claiming which reference cluster (`aws-0`/`gcp-0`) is active today.

---

### Task 1: Migrate & generalize the Developer Quickstart, 12-chapter User Guide & Cheat Sheet

**Files:**
- Create: `website/content/docs/guide/_index.md`
- Create: `website/content/docs/guide/developer-quickstart.md`
- Create: `website/content/docs/guide/meet-the-factory.md`
- Create: `website/content/docs/guide/before-your-first-task.md`
- Create: `website/content/docs/guide/your-first-task.md`
- Create: `website/content/docs/guide/writing-a-good-task.md`
- Create: `website/content/docs/guide/following-and-steering.md`
- Create: `website/content/docs/guide/when-the-factory-needs-you.md`
- Create: `website/content/docs/guide/merging.md`
- Create: `website/content/docs/guide/terminal-and-coding-agent.md`
- Create: `website/content/docs/guide/multi-repository.md`
- Create: `website/content/docs/guide/when-things-go-wrong.md`
- Create: `website/content/docs/guide/merge-policies.md`
- Create: `website/content/docs/guide/upkeep-routines.md`
- Create: `website/content/docs/guide/cheat-sheet.md`

- [ ] **Step 1: Copy all 15 files from `ai-native-ref/website/content/docs/use/` (`developer-quickstart.md` + `use/guide/*.md`)**

Assign ordered sidebar weights (`5` for `developer-quickstart`, `10`..`120` for Chapters 1–12, `130` for `cheat-sheet`).

- [ ] **Step 2: Generalize host specifics & rewrite links to local `relref`s**

For every file in `website/content/docs/guide/`:
- Replace `{{< maku "docs/..." >}}` shortcodes with local `{{< relref "/docs/..." >}}` links.
- Replace `{{< relref "/docs/use/..." >}}`, `{{< relref "/docs/how-it-works/..." >}}`, `{{< relref "/docs/reference/..." >}}` with Maku's new paths (`concepts/`, `use/`, `install/`, `operate/`, `reference/`).
- Replace host-specific wording (`tailnet`, `ZITADEL`, `rooms.priv.aws.ogenki.io`, `aws-0`/`gcp-0`) with generic Maku wording (`your rooms URL`, `your identity provider`, linking `install/identity.md` and `install/exposure.md`).
- In Chapters 8, 11, and 12, keep the developer workflow narrative and link `use/cli.md`, `use/merge-policies.md`, `use/upkeep-routines.md`, and `reference/` rather than repeating command/CRD tables.
- Update any Web UI screenshots to use `{{< themed-img >}}` pointing to `/images/web-ui/...`.

- [ ] **Step 3: Verify Hugo build & doc guards**

Run `task docs && task site:check`.
Expected: zero broken `relref`s, zero cluster-name or old-vocabulary hits, all word ceilings pass.

- [ ] **Step 4: Commit**

`git commit -m "docs(website): migrate and generalize 12-chapter User Guide and Cheat Sheet"`

---

### Task 2: Migrate unique `how-it-works` explanations & Draw.io architecture SVGs into `concepts/`

**Files:**
- Create: `website/content/docs/concepts/roles.md` (from `ai-native-ref` `how-it-works/roles.md`)
- Create: `website/content/docs/concepts/alternatives.md` (from `ai-native-ref` `how-it-works/alternatives.md`)
- Create: `website/content/docs/concepts/runlore.md` (from `ai-native-ref` `how-it-works/runlore.md`)
- Create: `website/content/docs/concepts/providers.md` (from `ai-native-ref` `how-it-works/providers.md`)
- Create: `website/content/docs/concepts/routing-and-cost.md` (from `ai-native-ref` `how-it-works/routing-and-cost.md`)
- Create: `website/static/images/diagrams/agent-factory-overview.svg` (native-text SVG copied from `ai-native-ref/website/static/images/diagrams/agent-factory-overview.svg`) + `website/layouts/_shortcodes/hero-video.html` (if `roles.md` embeds `agent-factory-v3` clip, copy the clip to `website/static/media/` or convert to a clean `<figure>` video partial)
- Modify: `website/content/docs/concepts/architecture.md` (embed `agent-factory-overview.svg` with crisp light-card styling and zoom)
- Modify: `website/content/docs/concepts/_index.md` (add cards for Roles, Alternatives considered, RunLore integration, Providers, Routing and cost)

- [ ] **Step 1: Port the 5 explanation pages & diagram assets**

- In `roles.md`, rewrite all `relref`s to Maku paths (`concepts/factory.md`, `use/merge-policies.md`, `reference/budgets.md`).
- In `alternatives.md`, update ADR links (`0053`, `0062`) to local `{{< relref "/docs/decisions/..." >}}` (created in Task 3 — or commit Task 2 and Task 3 together if `refLinksErrorLevel: ERROR` checks immediately; sequence Task 3's ADR stub or land Task 2+3 in order so every commit passes `task site:check`!).
- In `providers.md` and `routing-and-cost.md`, keep measured reference-host cost figures explicitly labelled as examples from the reference deployment (`ai-native-ref`) and link `airef.ogenki.io` once for the optional self-hosted GPU serving side.
- In `runlore.md`, link `https://runlore.io/` and Maku's `reference/sponsored-principals.md` / `reference/api.md`.

- [ ] **Step 2: Verify SVG rendering & doc guards**

Confirm `agent-factory-overview.svg` uses native SVG text (no broken `foreignObject` in `<img>` mode) and sits on `color-scheme: light` white card in both Day and Night modes.

- [ ] **Step 3: Commit**

`git commit -m "docs(website): migrate roles, alternatives, RunLore, providers, and routing-and-cost"`

*(Note: link `alternatives.md` / `providers.md` to `/docs/decisions/...` in Task 3 once the ADRs exist so every commit builds green under `refLinksErrorLevel: ERROR`.)*

---

### Task 3: Migrate the 15 Maku product ADRs (`0041`–`0062`) into `decisions/`

**Files:**
- Create: `website/content/docs/decisions/_index.md`
- Create (copied from `ai-native-ref/website/content/docs/decisions/`):
  - `0041-agent-sandbox-gvisor-al2023.md`
  - `0042-agent-router-identity-gateway.md`
  - `0043-octo-sts-for-agent-github-tokens.md`
  - `0044-room-session-protocol.md`
  - `0045-merge-policy-gate.md`
  - `0048-agent-factory-orchestrator.md`
  - `0049-room-client-and-human-auth.md`
  - `0051-otel-collector-agent-trace-gate.md`
  - `0053-agent-router-on-agentgateway.md`
  - `0056-local-first-factory-ux.md`
  - `0058-reviewer-on-another-model.md`
  - `0059-factory-from-your-coding-agent.md`
  - `0060-conversational-intake.md`
  - `0061-agents-write-through-safe-outputs.md`
  - `0062-agent-runtime-remains-agent-sandbox.md`
  *(Count check: 15 files! `0041`, `0042`, `0043`, `0044`, `0045`, `0048`, `0049`, `0051`, `0053`, `0056`, `0058`, `0059`, `0060`, `0061`, `0062` — verify every one is a Maku product ADR; the 7 retained in `ai-native-ref` are `0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`, plus `0047` reserved.)*
- Modify: `hack/docs.sh`, `hack/docs_test.sh`, `hack/vocabulary.sh`, `hack/names.sh` (exempt `website/content/docs/decisions/` from design-id, cluster-name, status-tag, old-artifact-name, and vocabulary scans because ADRs are immutable historical records — matching `ai-native-ref`'s `decisions/` exemption)
- Modify: `website/content/docs/concepts/alternatives.md`, `website/content/docs/concepts/providers.md`, `website/content/docs/install/gateway.md` (wire local `{{< relref "/docs/decisions/..." >}}` links)

- [ ] **Step 1: Add `decisions/` exemption test to `hack/docs_test.sh`, `hack/vocabulary_test.sh`, and `hack/names_test.sh` (TDD)**

Confirm a historical ADR under `website/content/docs/decisions/` containing `aws-0`, `roomctl`, `D4`, or `harness profile` passes while the same text under `website/content/docs/concepts/` still fails.

- [ ] **Step 2: Copy the 15 product ADR files and author `website/content/docs/decisions/_index.md`**

- State the lineage clearly at the top of `decisions/_index.md`: *"These records originated in `cloud-native-ref` and `ai-native-ref` and moved here keeping their original numbers. Next Maku ADR: **0063**. Serving and host-deployment ADRs (`0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`) live on [airef.ogenki.io](https://airef.ogenki.io/docs/decisions/)."*
- Rewrite `relref` links inside the copied ADRs:
  - References to other migrated ADRs stay `{{< relref "/docs/decisions/..." >}}`.
  - References to retained ANR ADRs (`0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`) become `https://airef.ogenki.io/docs/decisions/...`.
  - References to `cloud-native-ref` ADRs keep `https://cnref.ogenki.io/docs/decisions/...`.
  - References to moved ANR doc pages become local `{{< relref "/docs/..." >}}`.

- [ ] **Step 3: Verify Hugo build & guards**

Run `task docs && task names && task site:check`.
Expected: exits 0.

- [ ] **Step 4: Commit**

`git commit -m "docs(website): migrate Maku product ADRs (0041-0062) into website/content/docs/decisions"`

---

### Task 4: Upgrade `roadmap.md` → `status.md` (Product Status & Roadmap)

**Files:**
- Move (`git mv`): `website/content/docs/roadmap.md` → `website/content/docs/status.md`
- Modify: `website/content/docs/status.md`
- Modify: `website/hugo.yaml` (add `User guide` → `/docs/guide` to top nav; switch `Roadmap` nav entry to `Status` → `/docs/status`)
- Modify: `website/content/docs/_index.md`, `README.md` (point status links to `https://maku.ogenki.io/docs/status/` instead of `airef.ogenki.io/docs/status/`)

- [ ] **Step 1: Author `website/content/docs/status.md`**

Front matter:
```yaml
---
title: Status and roadmap
linkTitle: Status
weight: 85
aliases:
  - /docs/roadmap/
description: "What is proven in live runs, what is implemented awaiting validation, what is in progress, the v5 roadmap, and known limitations."
---
```
Sections:
1. **Intro & link to reference deployment:** Explain that "Proven live" cites end-to-end evidence from the reference deployment (`airef.ogenki.io/docs/status/` for the currently active cluster & pinned tag) and releases (`github.com/Smana/maku/releases`). Zero `aws-0`/`gcp-0` names in prose.
2. **Recently shipped (`v0.55`–`v0.58`)** (carried from `roadmap.md`).
3. **Proven live** (Maku product capability rows migrated from `ai-native-ref`'s `status.md`, keeping direct links to `ai-native-ref` runbooks and result logs as external evidence).
4. **Implemented, awaiting validation** (Maku product rows only — drop serving-only items like Semantic Router metrics).
5. **In progress** & **Agent Factory v5: proven in the field** & **Next** (merged cleanly from `roadmap.md` + `status.md` without duplication).
6. **Known limitations** (Maku product limitations only).

- [ ] **Step 2: Update `website/hugo.yaml` nav & `website/content/docs/_index.md` cards**

Add `User guide` (`/docs/guide`) to `menu.main` and point `Status` to `/docs/status`. Update `README.md` status links to `https://maku.ogenki.io/docs/status/`.

- [ ] **Step 3: Cross-check PR 3 redirect targets against built `website/public/`**

Verify every canonical destination URL planned in PR 3's `redirects.yaml` (all `/docs/guide/...`, `/docs/concepts/...`, `/docs/use/...`, `/docs/install/...`, `/docs/operate/...`, `/docs/reference/...`, `/docs/decisions/...`, `/docs/status/`) exists as a generated `index.html` inside `website/public/`.

- [ ] **Step 4: Run Playwright visual check & full `task check`**

Inspect `/docs/guide/`, `/docs/concepts/alternatives/`, `/docs/decisions/`, and `/docs/status/` in Day + Night at 1440px and 390px (verify Mermaid diagrams, SVG cards, tables, and sidebar depth).
Run `task check`. Expected: exits 0.

- [ ] **Step 5: Commit & open PR 2**

`git commit -m "docs(website): add Product Status and Roadmap, update nav for migrated docs"`
