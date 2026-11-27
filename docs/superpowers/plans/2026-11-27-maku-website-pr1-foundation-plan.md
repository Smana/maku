# Maku website PR 1: Foundation, Ogenki design system, landing & existing docs — Implementation Plan

> **For agentic workers:** Execute task-by-task in a worktree branched from `origin/main` in `Smana/maku`. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Launch `https://maku.ogenki.io/` from `Smana/maku` on GitHub Pages with Hugo + Hextra deeply themed to Maku's Ogenki Day & Night Web UI design system, a bespoke `layouts/home.html` product front door, `/demo/` serving the interactive static Room Replay, all ~52 existing Maku reader docs migrated into `website/content/docs/`, updated code generators/charts/guards, and full automated + visual verification.

**Architecture:** Nested Hugo module in `website/` (`hugo-extended = "0.168.0"`, Hextra `v0.12.3`), self-hosted variable fonts copied from `web/src/assets/fonts/`, Ogenki Day & Night CSS tokens synced with `web/src/app.css` behind a token-parity & contrast check, `web/scripts/demo-site.sh` producing `/demo/` inside the same Pages artifact, and `refLinksErrorLevel: ERROR` + extended `hack/docs.sh` guarding every page.

**Tech Stack:** Hugo Extended 0.168.0, Hextra v0.12.3, CSS custom properties (Ogenki Day & Night), Node 24 (`esbuild` for `/demo/`), Bash/Perl (`hack/docs.sh`, `hack/site-check.sh`), Playwright (visual audit), GitHub Pages.

**Spec:** [`docs/superpowers/specs/2026-11-27-maku-website-design.md`](../specs/2026-11-27-maku-website-design.md)

---

## Global Constraints

- Execute in `Smana/maku` on a clean worktree branched from `origin/main`. Never override `HOME` (mise shims fork-bomb).
- `task check` must exit 0 before opening the PR.
- Zero external font CDN URLs (`fonts.googleapis.com`, `fonts.gstatic.com`); all fonts are local `.woff2` files matching `web/src/assets/fonts/`.
- Day (`:root`) and Night (`.dark`) tokens in `website/assets/css/custom.css` must match `web/src/app.css` verbatim and clear WCAG AA (4.5:1 text, 3:1 UI/borders) in both themes.
- `website/static/CNAME` must contain bare `maku.ogenki.io` with no trailing dot and no trailing newline.
- `docs/superpowers/` and `docs/assets/brand/` stay in `docs/` (never rendered onto the public site).
- Keep the shared `"## Holds, agreed, signal-checked"` section in `website/content/docs/use/merge-policies.md` plain Markdown (no Hugo shortcodes), because `hack/gen-policy-explainer.go` embeds it into the Web UI bundle.
- Commits are conventional, in English, with no co-author or agent-attribution trailers.

## Review Focus

1. **Token & font parity:** Does `website/assets/css/custom.css` override Hextra's default white/black surfaces completely so docs pages render on Ogenki Day (`#f5f6fc`/`#fdfdff`) and Night (`#0d1330`/`#161c42`) with zero contrast regressions?
2. **Zero broken inbound references:** Do `hack/gen-seams.go`, `hack/gen-reasons.go`, `hack/gen-policy-explainer.go`, `charts/*/README.md`, alert `runbook_url`s, and `README.md` all resolve cleanly after `docs/` moves?
3. **No silent content loss:** Are all ~52 reader docs accounted for (including `aliases:` replacing the 3 moved stubs `api.md`, `event-envelope.md`, `room-log.md`), with every heading anchor preserved?
4. **Demo isolation & theme sync:** Does `/demo/` load cleanly as a standalone full-screen Room Replay *and* embedded on `/` without broken relative asset paths?

---

### Task 1: Toolchain pin & Hugo + Hextra scaffold

**Files:**
- Modify: `mise.toml`, `.gitignore`
- Create: `website/go.mod`, `website/hugo.yaml`, `website/static/CNAME`, `website/content/_index.md`

- [ ] **Step 1: Pin Hugo Extended in `mise.toml`**

Add under `[tools]` in `mise.toml` (matching `ai-native-ref/mise.toml`):
```toml
# Documentation site (website/). "hugo-extended" is deliberate: Hextra targets extended.
hugo-extended = "0.168.0"
```
Run `mise install hugo-extended && hugo version` and confirm `extended` appears in the version output.

- [ ] **Step 2: Create nested Hugo module `website/go.mod`**

```go
module github.com/Smana/maku/website

go 1.27.0

require github.com/imfing/hextra v0.12.3 // indirect
```
Confirm root `go test ./internal/version/...` and `task tidy` are unaffected (nested module is ignored by root Go commands).

- [ ] **Step 3: Create `website/static/CNAME` and ignore Hugo build outputs**

```bash
mkdir -p website/static
printf 'maku.ogenki.io' > website/static/CNAME
```
Add `website/public/`, `website/resources/`, and `_site/` to `.gitignore`.

- [ ] **Step 4: Create `website/hugo.yaml`**

Configure:
- `baseURL: https://maku.ogenki.io/`
- `title: Maku`
- `enableRobotsTXT: true`, `enableGitInfo: true`
- `refLinksErrorLevel: ERROR` (fail-closed internal link guard)
- `module.hugoVersion.min: "0.146.0"`, `module.imports: [{path: github.com/imfing/hextra}]`
- `markup.goldmark.renderer.unsafe: true`, `markup.highlight.noClasses: false`
- `menu.main`: `Get started` (`/docs/get-started`), `Docs` (`/docs`), `Demo` (`/demo/`), `Roadmap` (`/docs/roadmap`), `GitHub` (`https://github.com/Smana/maku`, icon `github`), `Search` (`type: search`)
- `params`:
  - `description`: sovereign Kubernetes coding-agent factory summary
  - `images: [images/social/og-card.png]`
  - `navbar`: `displayTitle: false`, `displayLogo: true` (or Orbit hex mark + real lowercase `maku` wordmark via custom navbar-title partial so wordmark renders in `Space Grotesk 700` at `-0.03em` with `.dark`-switched SVG mark — matching `web/src/brand.ts`!)
  - `search`: `enable: true`, `type: flexsearch`, `tokenize: forward`
  - `editURL`: `enable: true`, `base: "https://github.com/Smana/maku/edit/main/website/content"`
  - `footer`: `enable: true`, `displayCopyright: true`, `displayPoweredBy: false`
  - `imageZoom.enable: true`
  - `theme`: `default: system`, `displayToggle: true`
  - `sponsor` & `pilot` params for the footer sponsor line and partner pilot CTA

- [ ] **Step 5: Verify empty Hugo build**

Create minimal placeholder `website/content/_index.md` and run `cd website && hugo --gc --minify`.
Expected: exits 0, produces `website/public/CNAME` matching `maku.ogenki.io` with no trailing dot.

- [ ] **Step 6: Commit**

`git commit -m "build(website): scaffold Hugo + Hextra site with pinned hugo-extended"`

---

### Task 2: Brand assets, self-hosted fonts, Ogenki Day/Night theme & token-parity guard

**Files:**
- Create: `website/static/fonts/` (copied from `web/src/assets/fonts/`), `website/static/favicon*` & `apple-touch-icon.png` (from `docs/assets/brand/favicon/`), `website/static/images/brand/` (Orbit hex SVGs), `website/static/images/web-ui/` (from `docs/assets/web-ui/`), `website/static/images/social/og-card.png` (from `docs/assets/brand/social/github-social-preview.png`)
- Create: `website/assets/css/custom.css`
- Create: `website/layouts/_partials/custom/head-end.html`
- Create: `website/layouts/_partials/custom/footer.html`
- Create: `website/layouts/_shortcodes/themed-img.html`
- Create: `hack/site-check.sh`, `hack/site-check_test.sh`

- [ ] **Step 1: Write the failing site contract test (`hack/site-check_test.sh`) first (TDD)**

Test cases to assert `hack/site-check.sh` catches:
1. Trailing dot or newline in `website/static/CNAME`.
2. Missing or mismatched font `.woff2` file vs `web/src/assets/fonts/`.
3. Any external font URL (`fonts.googleapis.com`, `fonts.gstatic.com`, `https://` inside `@font-face`) in `website/assets/css/` or `website/layouts/`.
4. Any drift between the core Ogenki Day/Night hex values in `web/src/app.css` (`--surface-000`, `--surface-100`, `--surface-200`, `--line`, `--foreground`, `--muted-foreground`, `--blue`, `--violet`, `--pink`, `--green`, `--amber`) and `website/assets/css/custom.css`.
5. Any `transition: all` in `website/assets/css/custom.css`.

Run `bash hack/site-check_test.sh` and confirm it fails before implementation.

- [ ] **Step 2: Copy fonts, favicons, brand SVGs, and Web UI screenshots into `website/static/`**

Copy:
- `web/src/assets/fonts/*` → `website/static/fonts/`
- `docs/assets/brand/favicon/*` → `website/static/favicon/` (plus `favicon.svg` / `favicon.ico` wiring in `head-end.html`)
- `docs/assets/brand/svg/maku-mark-light.svg` & `maku-mark-dark.svg` → `website/static/images/brand/`
- `docs/assets/web-ui/*` → `website/static/images/web-ui/`
- `docs/assets/brand/social/github-social-preview.png` → `website/static/images/social/og-card.png`

- [ ] **Step 3: Author `website/layouts/_partials/custom/head-end.html` & `footer.html`**

- `head-end.html`: preload `SpaceGrotesk-Variable.woff2`, `Inter-Variable.woff2`, `JetBrainsMono-Variable.woff2` (`as="font" type="font/woff2" crossorigin`), link Maku SVG/PNG/apple-touch favicons, and add lightweight Mermaid click-to-zoom script (matching `ai-native-ref`'s theme-aware zoom overlay).
- `footer.html`: clean 3-column navigation footer (Docs / Product / Community & Reference: GitHub, Releases, `airef.ogenki.io`, `cnref.ogenki.io`, `blog.ogenki.io`) + muted GitHub Sponsors footnote.

- [ ] **Step 4: Author `website/layouts/_shortcodes/themed-img.html`**

Shortcode accepting `light`, `dark`, `alt`, optional `caption`, `width`, `height`. Renders both `<img>` elements inside `<figure class="maku-shot">`: `.maku-img-light` hidden under `.dark`, `.maku-img-dark` shown under `.dark` (strictly `.dark` class, never `@media (prefers-color-scheme)`, matching `web/src/brand.ts`), with `1px` neutral-opacity outline (`oklch(0 0 0 / 0.1)` Day / `oklch(1 0 0 / 0.1)` Night) and concentric border radius.

- [ ] **Step 5: Author `website/assets/css/custom.css` with Ogenki Day & Night parity**

Include:
1. Local `@font-face` declarations for `Space Grotesk`, `Inter` (normal + italic), `JetBrains Mono` (`font-display: swap`).
2. Full Ogenki Day (`:root`) and Night (`.dark`) custom property blocks matching `web/src/app.css` verbatim.
3. Hextra primary-hue alignment (`212deg` Day / `208deg` Night) + full Hextra chrome overrides so `html`, `body`, navbar, sidebar, TOC, cards, tables, inline code, callouts, and search modal render on Ogenki `--maku-bg` / `--maku-surface` / `--maku-raised` / `--maku-ink` / `--maku-line` (never raw `#fff`/`#111`).
4. Typography rules: `Space Grotesk 700` headings (`text-wrap: balance`), `Inter 400` body (`text-wrap: pretty`, `-webkit-font-smoothing: antialiased`), `JetBrains Mono` code/eyebrows/tabular numbers, wordmark lowercase `maku` at `-0.03em` with `16px` clearspace around the `32px` Orbit hex mark.
5. Accessibility & polish rules: `scroll-margin-top: 5rem` on content headings, ≥24px/40px interactive targets, Chroma light-mode code comment contrast ≥4.5:1 (`#545b85`), dark-mode primary button ink `#0d1330`, drawio diagram `color-scheme: light` card treatment, explicit `transition-property` (zero `transition: all`), `@media (prefers-reduced-motion: reduce)` handling.

- [ ] **Step 6: Implement `hack/site-check.sh` and verify tests pass**

Run `bash hack/site-check_test.sh && bash hack/site-check.sh`.
Expected: exits 0.

- [ ] **Step 7: Commit**

`git commit -m "feat(website): add Ogenki Day/Night design system, local fonts, and site checks"`

---

### Task 3: Bespoke landing page (`website/layouts/home.html`) & `/demo/` Room Replay integration

**Files:**
- Create: `website/layouts/home.html`
- Create: `website/static/media/maku-run.mp4`, `maku-run.jpg`, `maku-run.en.vtt` (or wire `pages.yaml` / `task site:build` to copy from `web/preview/media/` without duplicating binary blobs in Git!)
- Modify: `website/assets/css/custom.css` (landing page band styles, Day + Night tuned)
- Modify: `web/scripts/demo-site.sh` (add `--replay-only` or `--embedded` flag — or let `demo-site.sh` put `replay.html` at `index.html` when mounted at `_site/demo/`, so `/demo/` opens the interactive Room Replay directly without duplicating the landing page!)

- [ ] **Step 1: Avoid duplicating binary media blobs in Git**

Keep `web/preview/media/maku-run.{mp4,jpg,en.vtt}` as the single tracked source in Git; mount `web/preview/media` into Hugo via `module.mounts` in `website/hugo.yaml` (`source: ../web/preview/media`, `target: static/media`). Verify `hugo --gc --minify` outputs `public/media/maku-run.mp4` with zero duplicate Git blobs!

- [ ] **Step 2: Adapt `web/scripts/demo-site.sh` for `/demo/` mount**

Check if `--replay-root` flag (default keeps existing behavior, `--replay-root` copies `preview/demo.html` to `$out/index.html` AND keeps `$out/replay.html` as a copy for backwards compatibility, plus adds a subtle top-right "← maku.ogenki.io" / "Docs" link or keeps the clean Room Replay header). Also add a small `postMessage` or `storage` listener so toggling Day/Night on `/` syncs the embedded `/demo/` iframe theme cleanly!
Run `task ui:test` to verify no regression in `web/test/brand-style.test.ts` or `web/test/demo-*.test.ts`.

- [ ] **Step 3: Build `website/layouts/home.html` with all 6 bands**

Inherit Hextra's `baseof.html` navbar & footer, and author the 6 semantic sections:
1. **Hero:** Day + Night tuned radial aurora sky + masked 28 px dot grid; emerald live eyebrow (`Apache-2.0 · Sovereign Kubernetes Agent Factory`); `Space Grotesk 700` display H1 with `--maku-aurora` gradient emphasis; crisp 2-sentence sovereign lead; primary (`Try on your laptop · 15 min`), secondary (`Drive live replay`, `Run a Tier-1 Pilot`) CTAs; 4 tabular-num proof tiles (`12 min`, `2 agents`, `1 human click`, `$0.14`).
2. **Real run stage:** 2-column framed 2:06 screencast (`preload="metadata"`, poster, captions) + synchronized 10-step interactive **run rail** (`you`/`factory`/`agent`/`done` color-coded nodes, aurora progress line, click-to-seek, keyboard accessible).
3. **Architecture & four sovereign pillars:** Hand-crafted crisp inline SVG trust-boundary flow diagram (theme-aware via CSS variables, readable in both Day and Night) + 2×2 card grid (Sovereign control plane / Sandboxed & bounded / Collaborative rooms / Earned auto-merge & upkeep routines).
4. **Inside Maku — real Web UI gallery:** Accessible ARIA tablist (`Live room`, `Merge policies`, `Upkeep routines`, `Cluster settings`) displaying matched Day/Night 1280px screenshots (`*-light.png` / `*-dark.png` via `.dark`), 1-sentence caption, and doc link.
5. **Drive one yourself (interactive Room Replay):** Lazy-loaded iframe (`src="/demo/"`, `loading="lazy"`) inside concentric framed window + "Open full screen ↗" link to `/demo/`.
6. **Adopt on your terms:** Copyable `git clone https://github.com/Smana/maku && cd maku && task demo` terminal block (fixed dark terminal well with crisp copy button + accessible feedback), Tier-1 Pilot vs Tier-2 Hardened comparison cards, and Apache-2.0 partner collaboration CTA card (contrast-safe gradient button ≥4.5:1 across the full ramp).

- [ ] **Step 4: Build & smoke-test locally**

```bash
cd website && hugo --gc --minify --destination ../_site
bash web/scripts/demo-site.sh --replay-root _site/demo
```
Verify `_site/index.html`, `_site/demo/index.html`, `_site/media/maku-run.mp4`, and `_site/fonts/SpaceGrotesk-Variable.woff2` all exist.

- [ ] **Step 5: Commit**

`git commit -m "feat(website): add bespoke Day/Night landing page and /demo/ replay mount"`

---

### Task 4: Move all ~52 existing Maku reader docs to `website/content/docs/`

**Files:**
- Move (`git mv`): all reader `.md` files listed in the spec's PR 1 file mapping table from `docs/` → `website/content/docs/` (`get-started/`, `use/`, `concepts/`, `install/`, `operate/`, `reference/`, `roadmap.md`, `contributing/`)
- Delete: `docs/api.md`, `docs/event-envelope.md`, `docs/room-log.md` (replaced by `aliases:` on `reference/{api,event-envelope,room-log}.md`)
- Create: section `_index.md` files (`docs/_index.md`, `get-started/_index.md`, `use/_index.md`, `concepts/_index.md`) with Hextra `cards`
- Rewrite: `docs/README.md` as a concise 15-line pointer to `https://maku.ogenki.io/docs/`

- [ ] **Step 1: Record inbound link & anchor baseline**

Snapshot every `#anchor` referenced across `README.md`, `docs/`, `charts/`, `internal/`, and `web/` so zero anchors disappear during the move.

- [ ] **Step 2: Move files with `git mv` and add Hugo YAML front matter**

For every moved file:
1. Replace leading `<!-- page-type: X -->` + `# Heading` with YAML front matter:
   ```yaml
   ---
   title: "<exact heading>"
   weight: <ordered weight>
   description: "<1-sentence reader summary>"
   diataxis: <tutorial|how-to|explanation|operations|reference>
   aliases: [...] # where URL moved from flat docs/ or old stub
   ---
   ```
   (Hextra renders `title` as the page's `<h1>`, so drop the duplicate Markdown `# Heading`.)
2. Convert internal cross-doc Markdown links to `{{< relref "/docs/..." >}}` (preserving `#anchors`).
3. Convert links to repo files outside `website/content/` (`../CONTRIBUTING.md`, `../SECURITY.md`, `../AGENTS.md`, `../charts/...`, `../examples/...`) to `https://github.com/Smana/maku/blob/main/...` (per ADR-0004).
4. Convert matched light/dark `<picture>` screenshot blocks in `use/web-ui.md`, `use/merge-policies.md`, and `use/upkeep-routines.md` to `{{< themed-img >}}` pointing to `/images/web-ui/...` so they track `.dark` toggle rather than OS media query!
5. Keep `"## Holds, agreed, signal-checked"` in `website/content/docs/use/merge-policies.md` strictly plain Markdown (no shortcodes) for `hack/gen-policy-explainer.go`.

- [ ] **Step 3: Build Hugo with `refLinksErrorLevel: ERROR`**

Run `cd website && hugo --gc --minify`.
Expected: exits 0 with zero broken `relref`s or warnings.

- [ ] **Step 4: Commit**

`git commit -m "docs(website): migrate existing Maku documentation into website/content/docs"`

---

### Task 5: Update Go generators, CI guards, charts & `README.md`

**Files:**
- Modify: `hack/gen-seams.go` (`website/content/docs/install/install-secrets.md`), `hack/gen-reasons.go` (`website/content/docs/use/merge-policies.md`), `hack/gen-policy-explainer.go` (`website/content/docs/use/merge-policies.md`)
- Modify: `hack/docs.sh`, `hack/docs_test.sh` (scan `website/content/docs/**/*.md`; read `diataxis:` from YAML front matter in addition to `<!-- page-type: -->`; keep existing rules for status vocabulary, design IDs, cluster names, and relative links in `README.md`, `charts/`, `AGENTS.md`, `CONTRIBUTING.md`)
- Modify: `hack/vocabulary.sh`, `hack/vocabulary_test.sh` (scan `website/content/docs/**/*.md`, skipping `reference/` and `operate/upgrading.md` where wire terms belong; check `agreed`/`quiet` on `use/merge-policies.md`, `concepts/factory.md`, `reference/web-ui-settings.md`)
- Modify: `hack/names.sh`, `hack/names_test.sh` (update `slack_allow` from `:!docs/upgrading.md` to `:!website/content/docs/operate/upgrading.md`)
- Modify: `hack/ci-changes.sh`, `hack/ci-changes_test.sh` (treat `website/*` as non-deployable so website-only PRs skip prerelease Go/image builds)
- Modify: `taskfile.yaml` (update `chart:seams` git-diff path to `website/content/docs/install/install-secrets.md`; add `site:build`, `site:check`, and wire `site:check` into `check:static`)
- Modify: `charts/*/README.md`, `charts/agent-factory/files/rules/*.yaml`, `charts/agent-factory/templates/NOTES.txt`, `charts/agent-factory/tests/golden/*.yaml` (update `docs/...` links and alert `runbook_url`s to `https://maku.ogenki.io/docs/...`)
- Modify: `README.md` (point doc table, CTAs, and "Watch a run" link to `https://maku.ogenki.io/` & `https://maku.ogenki.io/docs/...`; keep `docs/assets/brand/png/...` and `docs/assets/maku-run.webp` local so GitHub renders them natively)
- Modify: `AGENTS.md` (update quality-gate table and package/docs paths)

- [ ] **Step 1: Update the 3 generators & run `task chart:seams` + `go run ./hack/gen-reasons.go` + `go run ./hack/gen-policy-explainer.go`**

Verify `git diff --exit-code -- web/src/reasons.ts web/src/generated/how-policies-decide.ts` (or commit expected regenerated output) and confirm `task ui:check` passes.

- [ ] **Step 2: Update `hack/docs.sh`, `hack/vocabulary.sh`, `hack/names.sh`, `hack/ci-changes.sh` and their `*_test.sh` suites**

Run each test script first (TDD) and confirm all four suites pass cleanly against the moved docs.

- [ ] **Step 3: Update `charts/` docs/runbooks/goldens and `README.md`**

Run `task chart:check` and confirm Helm lint, render tests, PromQL tests, and golden diffs all pass.

- [ ] **Step 4: Wire `site:build` and `site:check` into `taskfile.yaml`**

- `site:build`: `cd website && hugo --gc --minify --destination ../_site` + `bash web/scripts/demo-site.sh --replay-root _site/demo`
- `site:check`: runs `hack/site-check_test.sh`, `hack/site-check.sh`, and `cd website && hugo --gc --minify`
- Add `site:check` to `check:static`.

- [ ] **Step 5: Commit**

`git commit -m "chore(website): update generators, doc guards, charts, and README for maku.ogenki.io"`

---

### Task 6: Unified Pages workflow, Playwright Day/Night visual audit & full gate verification

**Files:**
- Delete: `.github/workflows/demo.yaml`
- Create: `.github/workflows/pages.yaml`

- [ ] **Step 1: Replace `.github/workflows/demo.yaml` with `.github/workflows/pages.yaml`**

Triggers: `push` to `main` (`paths: [website/**, web/**, docs/assets/**, .github/workflows/pages.yaml]`), `tags: ["v*"]`, `workflow_dispatch`.
Build job:
1. `actions/checkout` (with `fetch-depth: 0` so Hugo `enableGitInfo: true` gets real commit dates)
2. `jdx/mise-action` (installs `hugo-extended`, `go`, `nodejs`)
3. `cd website && hugo --gc --minify --destination ../_site`
4. `bash web/scripts/demo-site.sh --replay-root _site/demo`
5. Guard: `grep -qxF 'maku.ogenki.io' _site/CNAME` and `test -f _site/demo/index.html`
6. `actions/configure-pages` + `actions/upload-pages-artifact` (`path: _site`)
Deploy job: identical `actions/deploy-pages` to `github-pages` environment.

- [ ] **Step 2: Run Playwright visual & polish audit (Day + Night, Desktop 1440×900 + Mobile 390×844)**

Serve `_site` locally (`python3 -m http.server` on loopback) and capture screenshots + DOM assertions across:
- `/` (Day + Night, 1440px + 390px) — inspect hero contrast, aurora sky in Day & Night, chapter rail click-to-seek, UI tab switching + light/dark screenshot swap, lazy `/demo/` iframe, horizontal overflow = 0.
- `/demo/` (Day + Night, 1440px + 390px) — inspect standalone Room Replay load, fonts, controls.
- `/docs/get-started/quickstart/` & `/docs/use/upkeep-routines/` (Day + Night, 1440px + 390px) — inspect Hextra sidebar, TOC, callouts, code blocks, matched light/dark screenshots, search modal (`Ctrl K`), and Orbit hex navbar lockup.
Fix every visual defect found before committing.

- [ ] **Step 3: Run full `task check`**

Run `task check` (all static gates + Go tests with `-race` + UI tests + Hugo build).
Expected: exits 0.

- [ ] **Step 4: Commit & open PR 1**

`git commit -m "ci(website): deploy unified maku.ogenki.io and /demo/ via GitHub Pages"`

---

### Post-merge cutover checklist (before starting PR 2)

- [ ] Add DNS record: `maku.ogenki.io` `CNAME` → `smana.github.io`.
- [ ] Configure GitHub Pages custom domain `maku.ogenki.io` on `Smana/maku` and wait for TLS certificate issuance (`gh api repos/Smana/maku/pages --jq '{cname, status, https_enforced, html_url}'`).
- [ ] Verify `https://maku.ogenki.io/`, `https://maku.ogenki.io/demo/`, and `https://maku.ogenki.io/docs/` return HTTP 200 over HTTPS.
- [ ] Check `https://blog.ogenki.io/maku/`: verify redirect to `https://maku.ogenki.io/` (if 404, add a 5-line redirect page at `maku/index.html` in `Smana/smana.github.io`).
