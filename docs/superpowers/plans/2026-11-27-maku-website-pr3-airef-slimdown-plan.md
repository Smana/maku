# Maku website PR 3: Slim `airef.ogenki.io`, cross-domain redirects & Q14 amendment — Implementation Plan

> **For agentic workers:** Execute task-by-task in a worktree branched from `origin/main` in **`Smana/ai-native-ref`** **only after PR 1 and PR 2 are merged in `Smana/maku` and live on `https://maku.ogenki.io/`**. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refocus `https://airef.ogenki.io/` on **Self-hosted GPU Model Serving**, a concise **Maku on Cloud Native Ref** deployment & day-2 section, **Host contract**, the **7 serving/host ADRs**, and **Reference Deployment Status** — while preserving every moved inbound URL via auto-generated cross-domain redirect stubs (`redirects.yaml`), retargeting `{{< maku >}}` to `https://maku.ogenki.io/docs/...`, and amending v5 roadmap Q14.

**Architecture:** `Smana/ai-native-ref` keeps 100% of its Kubernetes/Flux deployment manifests (`clusters/`, `infrastructure/`, `tooling/`, `apps/`, `observability/`, `security/`) and operational runbooks (`docs/runbooks/`). Only `website/` and `README.md` slim down. Cross-domain redirects are generated post-Hugo from a single `website/data/redirects.yaml` file so they never pollute Hextra's sidebar, FlexSearch, or `sitemap.xml`.

**Tech Stack:** Hugo Extended 0.168.0, Hextra v0.12.3, Python 3 (`scripts/ci/build-redirects.py`, `scripts/ci/check-site-contract.py`), GitHub Pages.

**Spec:** [`docs/superpowers/specs/2026-11-27-maku-website-design.md`](../specs/2026-11-27-maku-website-design.md) (in `Smana/maku`)

---

## Global Constraints

- Execute in **`Smana/ai-native-ref`**, in a worktree branched from `origin/main`. Never override `HOME`.
- **Pre-condition gate:** Do NOT open PR 3 until every destination URL in `website/data/redirects.yaml` returns HTTP 200 on live `https://maku.ogenki.io/`.
- Do NOT modify or delete any Flux manifests (`clusters/`, `infrastructure/`, `tooling/`, `apps/`, `security/`, `observability/`) or runbooks (`docs/runbooks/`).
- Preserve `check-site-contract.py`'s `params.makuVersion` pin check and `activeCluster` check on `website/content/docs/status.md`.
- Preserve reader-first rules (`scripts/ci/check-reader-first.py`) and path verification (`scripts/ci/verify-doc-paths.sh`).
- `task check` in `ai-native-ref` must exit 0 before opening the PR.
- Commits are conventional, in English, with no co-author or agent-attribution trailers.

## Review Focus

1. **Zero 404s on moved URLs:** Every deleted page *and* every legacy alias (`/docs/platform/ai-platform/...`) previously declared in its front matter appears in `website/data/redirects.yaml`.
2. **Zero collision between live pages and redirects:** `scripts/ci/build-redirects.py` fails hard if any `from` path in `redirects.yaml` already exists in `website/public/`.
3. **Clean FlexSearch & sitemap:** Redirect HTML files are generated *after* `hugo --gc --minify`, carry `<meta name="robots" content="noindex">`, and never appear in `sitemap.xml` or FlexSearch.
4. **Serving-first framing:** `airef.ogenki.io/` immediately makes clear what lives here (GPU serving + `ai-gateway` + Cloud Native Ref deployment of Maku) and links `https://maku.ogenki.io/` as the product home.

---

### Task 1: Cross-domain redirect generator (`redirects.yaml` + `build-redirects.py`)

**Files:**
- Create: `website/data/redirects.yaml`
- Create: `scripts/ci/build-redirects.py`, `scripts/ci/test_build_redirects.py`
- Modify: `taskfile.yaml` (`docs` task runs `build-redirects.py` after `hugo --gc --minify`)
- Modify: `.github/workflows/` Pages build workflow (run `build-redirects.py` right after `hugo --gc --minify` before uploading Pages artifact)

- [ ] **Step 1: Write the failing unit tests (`scripts/ci/test_build_redirects.py`) first (TDD)**

Test cases:
1. Generates `<public>/<from>/index.html` containing `<meta name="robots" content="noindex">`, `<link rel="canonical" href="<to>">`, `<meta http-equiv="refresh" content="0; url=<to>">`, and an escaped clickable link.
2. Fails non-zero if `<public>/<from>/index.html` already exists (prevents clobbering a live `airef` page).
3. Fails non-zero on duplicate `from` entries or non-`https://maku.ogenki.io/` targets (unless explicitly allowed).
4. Strips leading/trailing slashes consistently and rejects `..` path traversal.

Run `python3 -m unittest scripts/ci/test_build_redirects.py` and confirm it fails.

- [ ] **Step 2: Implement `scripts/ci/build-redirects.py`**

Supports `--public website/public --map website/data/redirects.yaml` plus `--verify-live` (optional flag that HEAD/GET-checks every unique target URL on live `maku.ogenki.io` and fails on any non-200).

- [ ] **Step 3: Author `website/data/redirects.yaml` covering every moved page and legacy alias**

Complete redirect map:

| `from` (on `airef.ogenki.io`) | `to` (on `maku.ogenki.io`) |
|---|---|
| `/docs/use/` and `/docs/platform/ai-platform/agents/use/` | `https://maku.ogenki.io/docs/guide/` |
| `/docs/use/developer-quickstart/` | `https://maku.ogenki.io/docs/guide/developer-quickstart/` |
| `/docs/use/guide/` and `/docs/platform/ai-platform/agents/use/user-guide/` | `https://maku.ogenki.io/docs/guide/` |
| `/docs/use/guide/<chapter>/` (all 12 chapters + `cheat-sheet`) | `https://maku.ogenki.io/docs/guide/<chapter>/` |
| `/docs/use/slack/` | `https://maku.ogenki.io/docs/use/slack/` |
| `/docs/use/make-your-repository-ready/` | `https://maku.ogenki.io/docs/use/make-a-repository-ready/` |
| `/docs/how-it-works/` and `/docs/platform/ai-platform/agents/how-it-works/` and `/docs/platform/ai-platform/agents/` and `/docs/get-started/agent-factory/` | `https://maku.ogenki.io/docs/concepts/` |
| `/docs/how-it-works/roles/` | `https://maku.ogenki.io/docs/concepts/roles/` |
| `/docs/how-it-works/merge-gate/` | `https://maku.ogenki.io/docs/use/merge-policies/` |
| `/docs/how-it-works/factory/` | `https://maku.ogenki.io/docs/concepts/factory/` |
| `/docs/how-it-works/rooms/` | `https://maku.ogenki.io/docs/use/web-ui/` |
| `/docs/how-it-works/runtime/` | `https://maku.ogenki.io/docs/concepts/architecture/` |
| `/docs/how-it-works/runlore/` and `/docs/platform/ai-platform/agents/how-it-works/runlore/` | `https://maku.ogenki.io/docs/concepts/runlore/` |
| `/docs/how-it-works/providers/` and `/docs/platform/ai-platform/agents/how-it-works/providers/` | `https://maku.ogenki.io/docs/concepts/providers/` |
| `/docs/how-it-works/routing-and-cost/` and `/docs/platform/ai-platform/routing/` | `https://maku.ogenki.io/docs/concepts/routing-and-cost/` |
| `/docs/how-it-works/alternatives/` and `/docs/platform/ai-platform/agents/alternatives/` | `https://maku.ogenki.io/docs/concepts/alternatives/` |
| `/docs/operate/` and `/docs/platform/ai-platform/agents/tune/` | `https://maku.ogenki.io/docs/operate/` |
| `/docs/operate/access/` | `https://maku.ogenki.io/docs/install/identity/` |
| `/docs/operate/tune-a-run/` | `https://maku.ogenki.io/docs/operate/factory/` |
| `/docs/operate/auto-merge-safety/` | `https://maku.ogenki.io/docs/use/merge-policies/` |
| `/docs/reference/harness/` | `https://maku.ogenki.io/docs/concepts/opencode-harness/` |
| `/docs/reference/factory-configuration/` | `https://maku.ogenki.io/docs/reference/config/` |
| `/docs/reference/commands/` | `https://maku.ogenki.io/docs/use/cli/` |
| `/docs/reference/planner/` | `https://maku.ogenki.io/docs/concepts/factory/` |
| `/docs/reference/gate-reasons/` | `https://maku.ogenki.io/docs/use/merge-policies/` |
| `/docs/decisions/<0041..0062-slug>/` (all 15 moved product ADRs) | `https://maku.ogenki.io/docs/decisions/<0041..0062-slug>/` |

Before deleting any page in Task 3, inspect each deleted page's `aliases:` front matter and verify every single alias is listed in `redirects.yaml`.

- [ ] **Step 4: Run live target verification gate**

```bash
python3 -m unittest scripts/ci/test_build_redirects.py
python3 scripts/ci/build-redirects.py --map website/data/redirects.yaml --verify-live
```
Expected: 100% of target URLs on `https://maku.ogenki.io` return HTTP 200.

- [ ] **Step 5: Commit**

`git commit -m "feat(website): add cross-domain redirect generator for moved Maku docs"`

---

### Task 2: Retarget `{{< maku >}}` shortcode & update `check-site-contract.py`

**Files:**
- Modify: `website/layouts/_shortcodes/maku.html`
- Modify: `scripts/ci/check-site-contract.py` (+ its unit tests)

- [ ] **Step 1: Update `website/layouts/_shortcodes/maku.html`**

Change target base from `https://github.com/Smana/maku/blob/%s/%s` to `https://maku.ogenki.io/docs/%s` (trimming any leading `docs/` or `/` prefix so `{{< maku "get-started/quickstart/" >}}` and `{{< maku "install/gateway/" >}}` emit clean canonical `https://maku.ogenki.io/docs/...` links). Keep `site.Params.makuVersion` for `{{< maku-version >}}` on `status.md`.

- [ ] **Step 2: Update `scripts/ci/check-site-contract.py`**

- Keep `check_pin` (`params.makuVersion` == Helm values tag), `check_active_cluster` (`status.md` == `.github/agent-factory.yaml`), product-name guard, and Maku-version guard intact.
- Update `RAW_MAKU_LINK_RE` so raw `github.com/Smana/maku/(blob|tree)/...` doc links *and* hardcoded `maku.ogenki.io/docs/` links outside `decisions/` use the `{{< maku >}}` shortcode (keeping all cross-site doc links uniform and greppable).

- [ ] **Step 3: Verify tests**

Run `python3 scripts/ci/check-site-contract.py` and the script's unit tests.

- [ ] **Step 4: Commit**

`git commit -m "chore(website): retarget maku shortcode to maku.ogenki.io/docs"`

---

### Task 3: Remove moved Maku pages & create concise `Maku on CNR` deployment section

**Files:**
- Delete: `website/content/docs/use/guide/` (all 14 files), `use/developer-quickstart.md`, `use/slack.md`, `use/make-your-repository-ready.md`, `use/_index.md`
- Delete: `website/content/docs/how-it-works/{roles,merge-gate,factory,rooms,runtime,runlore,providers,routing-and-cost,alternatives}.md` and `how-it-works/_index.md`
- Delete: `website/content/docs/operate/{access,tune-a-run,auto-merge-safety,_index}.md`
- Delete: `website/content/docs/reference/{harness,factory-configuration,commands,planner,gate-reasons}.md`
- Delete: the 15 moved product ADRs (`0041`–`0045`, `0048`–`0049`, `0051`, `0053`, `0056`, `0058`–`0062`) from `website/content/docs/decisions/`
- Create: `website/content/docs/maku-deployment/_index.md` (≤ 500 words: how `ai-native-ref` deploys Maku via the `agent-platform` Flux umbrella on Cloud Native Ref, pinned host seam values for OIDC/CNPG/Cilium/agentgateway/GitHub Apps, which policies & routines are active here, and `{{< maku >}}` links for every product guide)
- Move (`git mv`): `website/content/docs/operate/day-2.md` → `website/content/docs/maku-deployment/day-2.md` (with `aliases: [/docs/operate/day-2/]`)

- [ ] **Step 1: Verify every deleted page's path + `aliases:` is present in `website/data/redirects.yaml`**

Run a quick check over `git diff --name-only --diff-filter=D` against `redirects.yaml` before deleting.

- [ ] **Step 2: Author `website/content/docs/maku-deployment/_index.md` and move `day-2.md`**

Keep `maku-deployment/_index.md` crisp and strictly deployment-specific (`diataxis: values`), linking `{{< maku "install/" >}}`, `{{< maku "get-started/quickstart/" >}}`, `{{< maku "guide/" >}}`, and `{{< maku "operate/" >}}`.

- [ ] **Step 3: Commit**

`git commit -m "docs(website): replace moved Maku product pages with concise Maku-on-CNR section"`

---

### Task 4: Promote Serving & reframe homepage, nav, decisions & Reference Status

**Files:**
- Move (`git mv`):
  - `website/content/docs/how-it-works/serving/_index.md` → `website/content/docs/serving/_index.md` (add alias `/docs/how-it-works/serving/`)
  - `website/content/docs/how-it-works/serving/autoscaling-and-gpu.md` → `website/content/docs/serving/autoscaling-and-gpu.md` (add alias `/docs/how-it-works/serving/autoscaling-and-gpu/`)
  - `website/content/docs/how-it-works/gateways.md` → `website/content/docs/serving/ai-gateway.md` (add aliases `/docs/how-it-works/gateways/`, `/docs/platform/ai-platform/gateways/`)
  - `website/content/docs/use/coding-clients.md` → `website/content/docs/serving/coding-clients.md` (add aliases `/docs/use/coding-clients/`, `/docs/platform/ai-platform/coding-clients/`)
  - `website/content/docs/operate/observability.md` → `website/content/docs/serving/observability.md` (add aliases `/docs/operate/observability/`, `/docs/platform/ai-platform/observability/`)
- Modify:
  - `website/content/docs/serving/ai-gateway.md` (keep `ai-gateway` / Envoy AI Gateway / Semantic Router / LoRA canaries; replace the Maku `model-gateway` section with a 3-line pointer to `{{< maku "install/gateway/" >}}`)
  - `website/content/docs/serving/coding-clients.md` (keep `ai-gateway` model configs for OpenCode/Continue/etc.; link `{{< maku "use/cli/" >}}` and `{{< maku "guide/terminal-and-coding-agent/" >}}` for Maku hand-offs)
  - `website/content/docs/serving/observability.md` (keep Serving metrics/KEDA + shared Victoria stack wiring; link `{{< maku "install/observability/" >}}` for Maku dashboards/alerts)
  - `website/content/docs/decisions/_index.md` (list the 7 retained serving & host ADRs: `0003`, `0004`, `0021`, `0046`, `0050`, `0054`, `0057`; link `https://maku.ogenki.io/docs/decisions/` for Maku's 15 product ADRs)
  - `website/content/docs/status.md` (shrink to **Reference Deployment Status**: active cluster, pinned `{{< maku-version >}}`, serving status & known serving gaps, runbook log index, and prominent link to `https://maku.ogenki.io/docs/status/` for Maku product maturity & v5 roadmap)
  - `website/content/docs/get-started/_index.md` (slim to "Deploy on Cloud Native Ref" or redirect into `serving/` + `maku-deployment/`)
  - `website/content/docs/reference/_index.md` (cards for `InferenceService` claim and `Host contract`)
  - `website/content/docs/_index.md`, `website/content/_index.md`, `website/hugo.yaml` (reframe homepage & top nav around `Serving`, `Maku on CNR`, `Host contract`, `Decisions`, `Status`, `Maku ↗`, `Cloud Native Ref ↗`)

- [ ] **Step 1: Move & trim the Serving section pages**

Ensure every old URL keeps an `alias:` (for pages staying on `airef.ogenki.io`) and zero dangling `relref`s remain.

- [ ] **Step 2: Reframe `website/content/_index.md`, `website/hugo.yaml`, `decisions/_index.md`, and `status.md`**

- `website/content/_index.md`: Lead with **Self-hosted GPU Model Serving & the Maku Reference Deployment on Cloud Native Ref**, primary CTA to **Model Serving** (`/docs/serving/`), clear CTA to **`maku.ogenki.io ↗`** for the standalone coding-agent factory, and CTA to **Cloud Native Ref ↗**.
- Verify `status.md` retains `activeCluster:` front matter and `{{< maku-version >}}` so `check-site-contract.py` stays green.

- [ ] **Step 3: Verify Hugo build + redirects + doc contracts**

Run `task docs` in `ai-native-ref`.
Expected: `hugo --gc --minify`, `build-redirects.py`, `verify-doc-paths.sh`, `check-reader-first.py`, and `check-site-contract.py` all exit 0 with zero collisions.

- [ ] **Step 4: Commit**

`git commit -m "docs(website): refocus airef.ogenki.io on Serving, Maku-on-CNR, and Reference Status"`

---

### Task 5: Update `README.md`, amend v5 roadmap Q14 & run full `task check`

**Files:**
- Modify: `README.md` (point Maku product links to `https://maku.ogenki.io/` and `airef.ogenki.io` links to updated `/docs/serving/` & `/docs/maku-deployment/` paths)
- Modify: `docs/superpowers/specs/2026-11-22-v5-roadmap-design.md` (add dated amendment under Q14 recording the 2026-11-27 decision to launch `maku.ogenki.io` now and slim `airef.ogenki.io`, linking Maku's `docs/superpowers/specs/2026-11-27-maku-website-design.md`)

- [ ] **Step 1: Update `README.md` and amend Q14 in `2026-11-22-v5-roadmap-design.md`**

Keep `README.md` within the 900-word budget and free of Maku version literals or cluster names (checked by `check-site-contract.py` and `check-reader-first.py`).

- [ ] **Step 2: Run full `task check` in `ai-native-ref` & audit redirects**

```bash
task check
python3 scripts/ci/build-redirects.py --public website/public --map website/data/redirects.yaml --verify-live
```
Inspect `website/public/sitemap.xml` and FlexSearch index to confirm zero moved/redirect URLs appear in either.

- [ ] **Step 3: Commit & open PR 3**

`git commit -m "docs: update README and amend v5 roadmap Q14 for maku.ogenki.io launch"`
