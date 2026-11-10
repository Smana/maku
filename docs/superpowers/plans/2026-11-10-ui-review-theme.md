# Ogenki theme: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the web UI takes the Ogenki design system's palette, type and card rules (Night for dark, Day for light), keeping the maku mark and every component's markup.

**Architecture:** the existing semantic tokens in `web/src/app.css` (`--background`, `--card`, `--primary`, `--muted-foreground`, `--flow-*`, …) keep their names and take Ogenki values, so no component changes for colour. Three self-hosted variable fonts and five font-size tokens replace today's stacks and 11 sizes. A handful of rules (cards, focus, the two bars, `.strip-next`) change shape. CSS tests pin the result: contrast, the allowed sizes, the font families and where the gradient may appear.

**Tech Stack:** CSS custom properties, self-hosted woff2, TypeScript DOM, vitest (CSS read as text), esbuild.

**Spec:** `docs/superpowers/specs/2026-11-10-ui-review-design.md`, **Part 2**. Source of truth for values: the Ogenki design system zip, `/home/smana/Backup/Ogenki/ogenki-design-system-and-template.zip`, `Ogenki/design-system/tokens.css` and `README.md` (unpack it to the scratchpad, never into the repo).

**Branch:** `ui/ogenki-theme`, created from `ui/review-settings` (PR #433, not merged yet: Part 1 rewrote much of `app.css`, so branching from `main` would conflict line by line). The PR targets `ui/review-settings` until #433 merges, then is retargeted to `main`.

## Global Constraints

- Token **names** do not change; components do not change for colour. Only `web/src/app.css`, the fonts, CSS tests, `web/DESIGN.md` and docs change, plus the rare component file that hard-codes a colour or a font (each one listed in its task's report).
- Light (`:root`) = **Day**, dark (`.dark`) = **Night**. `web/src/theme.ts` keeps light / dark / system and its `.dark` class contract. No `prefers-color-scheme` block replaces it.
- Night: page / card / raised `#0D1330` / `#161C42` / `#1F2754`; line `#3A4478`; text / secondary `#EEF0FA` / `#A9B1D6`; link, primary, focus `#5AA9F0`; ok / warn / critical `#3DDC97` / `#FFB547` / `#FF6FB5`; violet `#A78BFA`; brand `#00549C`; ink-on-accent `#0D1330`.
- Day: page / card / raised `#F5F6FC` / `#FDFDFF` / `#ECEEF8`; line `#B9BFDC`; text / secondary `#12163A` / `#545B85`; link, primary, focus `#1D63B0`; ok / warn / critical `#11795A` / `#965800` / `#C2306F`; violet `#6A4BD8`; brand `#00549C`; ink-on-accent `#FDFDFF`.
- Lanes: writer = blue, judge = violet, plan = amber, each theme's own value.
- Text on an accent fill uses ink-on-accent (`--primary-foreground` and kin), never literal white.
- A status is always a word plus a colour.
- Type: headings Space Grotesk 700, body Inter 400, code, ids and metrics JetBrains Mono. Self-hosted under `web/src/assets/fonts/` with their three OFL licences; no external font request, no CSP change.
- Screen scale: **32 / 22 / 16 / 14 / 13** px (13 = eyebrow: mono, uppercase, `letter-spacing: 0.08em`). Emphasis by weight or colour, never a new size.
- `aurora-bar` (`linear-gradient(90deg, #FF6FB5 0%, #A78BFA 100%)`) on the token budget bar and the evidence bar only; at most one gradient per screen; no glow backgrounds.
- Cards: `surface-100` (`--card`), 1 px `line` (`--border`) border, `radius-md` 16 px, padding `space-4` 16 px; hover raises to `surface-200`; selected adds a 1 px blue (`--primary`) border.
- Focus: 2 px solid focus ring with a 2 px page-colour gap.
- Contrast: 4.5:1 for text, 3:1 for input boundaries and focus; every pair pinned in `web/test/contrast.test.ts`. A pair that fails is adjusted and the adjustment listed in the PR.
- No emoji. Motion: opacity and transform only, none under `prefers-reduced-motion`.
- `task check` exits 0 before the last commit; each commit keeps `ui:check` clean (rebuilt bundle in the same commit). Never `HOME=…` or `env -i`; never push; no attribution lines in commits.

## Rulings made while planning

- **T-R1 (13 px).** Today 76 declarations use 13 px for ordinary text. The scale keeps 13 px for eyebrows only, so ordinary 13 px text becomes 14 px (`small`) and labels that are eyebrows become mono uppercase 13 px. This is the "fonts too small" fix the owner asked for.
- **T-R2 (rail sizes).** Spec Part 3 names 15 px persona and 14 px meta for the rail. 15 is off the scale Part 2 binds ("the rail and settings use the same scale"), so PR 3 uses 16 / 14. This PR sets the rail's text on the scale now and leaves its layout (360 px, glyphs) to PR 3.
- **T-R3 (the header).** Today the light header is white so the light-theme mark sits on white. Under Day the header is `surface-100` (`#FDFDFF`, near white) and under Night `surface-000`; the maku mark files stay as they are. If the mark lacks contrast on Night's header, the task reports it rather than recolouring the mark (the owner keeps the mark).

## File map

| File | Change |
|---|---|
| `web/src/assets/fonts/` | three variable woff2 (Space Grotesk, Inter + italic, JetBrains Mono), three OFL licences; the three static Space Grotesk files and their `LICENSE` removed |
| `web/src/app.css` | `@font-face`; font, size, space and radius tokens; the two token blocks at Ogenki values; card, focus, bar, `.strip-next` rules; every `font-size` and `font-family` declaration onto the tokens |
| `web/test/contrast.test.ts` | re-pinned pairs, new pairs for the new surfaces |
| `web/test/type-scale.test.ts` (new) | sizes, families and gradient placement read from `app.css` |
| `web/DESIGN.md` (new) | written with impeccable `document` from the shipped tokens |
| `docs/web-ui.md`, `docs/development.md` | the theme and the fonts |
| `internal/humanapi/ui/dist/` | rebuilt |

---

### Task 1: the three fonts, self-hosted

**Files:** `web/src/assets/fonts/*`, `web/src/app.css` (the `@font-face` block, lines 11–22; a new font-token block in `:root`; every `font-family:` declaration), `web/test/type-scale.test.ts` (new).

**Interfaces — produces:** `--font-display: "Space Grotesk", Arial, sans-serif;`, `--font-sans: Inter, Arial, sans-serif;`, `--font-mono: "JetBrains Mono", "Courier New", monospace;` in `:root`.

- [ ] **Step 1: failing test** `web/test/type-scale.test.ts`:

```ts
import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
const fonts = join(__dirname, "../src/assets/fonts");

describe("fonts", () => {
  it.each(["SpaceGrotesk-Variable.woff2", "Inter-Variable.woff2", "Inter-Variable-Italic.woff2", "JetBrainsMono-Variable.woff2",
    "LICENSE-space-grotesk.txt", "LICENSE-inter.txt", "LICENSE-jetbrains-mono.txt"])("ships %s", (f) => {
    expect(existsSync(join(fonts, f))).toBe(true);
  });
  it("loads nothing from another origin", () => {
    expect(css).not.toMatch(/url\(\s*["']?https?:/);
    expect(css).not.toMatch(/@import/);
  });
  it("names a family only through the three tokens", () => {
    const decls = [...css.matchAll(/font-family:\s*([^;]+);/g)].map((m) => m[1].trim());
    for (const d of decls) expect(d, d).toMatch(/^var\(--font-(display|sans|mono)\)$|^inherit$/);
  });
});
```

- [ ] **Step 2:** `npm --prefix web test -- type-scale` fails.
- [ ] **Step 3:** copy the four woff2 and three licences from the unpacked zip's `Ogenki/design-system/fonts/` into `web/src/assets/fonts/` (keep the zip's file names); delete `space-grotesk-latin-{400,500,700}-normal.woff2` and `LICENSE`. `@font-face` from the zip's `tokens.css`, with `src: url("/assets/fonts/<file>")` (the path the build copies `src/assets/` to). Add the three tokens. `body` takes `var(--font-sans)`; `h1, h2, h3, h4` and whatever today names Space Grotesk take `var(--font-display)` at weight 700; every `ui-monospace…` / `SFMono…` stack becomes `var(--font-mono)`. Grep `web/src/*.ts` for an inline `fontFamily` and move it to a class if one exists.
- [ ] **Step 4:** tests pass; `task ui:build`; open the preview (`npm --prefix web run preview:build && node web/preview/serve.mjs`, restore `web/preview/list.js`, stop the server after) and check in DevTools-free fashion with one headless screenshot that the page renders Inter (glyph shape) and no font 404s appear in the server log.
- [ ] **Step 5: commit** `feat(ui): self-host the Ogenki fonts`, bundle included.

---

### Task 2: Night and Day on the existing tokens, contrast re-pinned

**Files:** `web/src/app.css` (`:root`, line 23; `.dark`, line 92; the file's header comment), `web/test/contrast.test.ts`.

**Interfaces — produces:** every existing token at its Ogenki value; new raw tokens beside them for later tasks: `--surface-000`, `--surface-100`, `--surface-200`, `--line`, `--blue`, `--violet`, `--pink`, `--green`, `--amber`, `--ink-on-accent`, `--aurora-bar`, `--radius-sm: 8px`, `--radius-md: 16px`, `--radius-pill: 9999px`, `--space-1` … `--space-16` (4 px base).

- [ ] **Step 1: failing test.** In `contrast.test.ts`, pin the exact token values for both blocks before the pairs (a table test: `expect(light["--background"]).toBe("#f5f6fc")` …) for at least: `--background`, `--muted`, `--card`, `--surface-raised`, `--foreground`, `--card-foreground`, `--muted-foreground`, `--border`, `--primary`, `--link`, `--ring`, `--primary-foreground`, `--success-text`, `--warning`, `--destructive`, `--flow-writer`, `--flow-judge`, `--flow-plan`. Mapping (Day / Night):

| Token | Day | Night |
|---|---|---|
| `--background`, `--muted` (the page) | `#f5f6fc` | `#0d1330` |
| `--card`, `--brand-header` | `#fdfdff` | `#161c42` (header Night: `#0d1330`, see T-R3) |
| `--surface-raised` | `#eceef8` | `#1f2754` |
| `--foreground`, `--card-foreground`, `--brand-header-foreground` | `#12163a` | `#eef0fa` |
| `--muted-foreground` | `#545b85` | `#a9b1d6` |
| `--border` | `#b9bfdc` | `#3a4478` |
| `--primary`, `--link`, `--ring`, `--flow-writer` | `#1d63b0` | `#5aa9f0` |
| `--primary-foreground`, `--accent-foreground`, `--success-foreground` | ink-on-accent `#fdfdff` | `#0d1330` |
| `--flow-judge` | `#6a4bd8` | `#a78bfa` |
| `--flow-plan`, `--warning` | `#965800` | `#ffb547` |
| `--success`, `--success-text` | `#11795a` | `#3ddc97` |
| `--destructive`, `--destructive-text` | `#c2306f` | `#ff6fb5` |

  `--input`, `--placeholder`, `--warning-foreground`, `--warning-solid-foreground`, `--warning-border`, `--accent` and `--destructive-foreground` have no direct Ogenki twin: derive each from the palette so its pairs pass (input boundary ≥ 3:1 on the card and the dock; placeholder ≥ 4.5:1), and list each derived value with its ratio in the commit body and the task report.
- [ ] **Step 2:** the value table fails.
- [ ] **Step 3:** set the values; add the raw tokens; rewrite the file's header comment (the Maku-palette rationale goes; say Day/Night from the Ogenki design system and that `contrast.test.ts` pins them). Keep every existing pair in `contrast.test.ts`; add pairs for: muted text on `--surface-raised` (hover and table header), `--primary` as a 1 px selected border on `--card` (3:1), each status word colour on `--card` and on the page, `--ink-on-accent` on `--primary`, the focus ring on the page, on a card and on `--surface-raised` (3:1). Run; for each failing pair adjust the **derived** token only, never an Ogenki value; if an Ogenki value itself fails a pair, stop and report it (the spec says the adjustment is listed in the PR: the controller rules).
- [ ] **Step 4:** `npm --prefix web test` passes (other tests that pin a colour string are updated to the new value, each named in the report).
- [ ] **Step 5: commit** `feat(ui): Ogenki Night and Day on the existing tokens`, bundle included; the commit body lists every derived value and ratio.

---

### Task 3: one type scale

**Files:** `web/src/app.css` (all 110 `font-size` declarations, headings, `.eyebrow`-like labels), `web/test/type-scale.test.ts`.

**Interfaces — produces:** `--text-display: 32px` (h1), `--text-heading: 22px` (h2), `--text-body: 16px`, `--text-small: 14px`, `--text-eyebrow: 13px`, each with its line height (40 / 30 / 26 / 20 / 16 px) as `--leading-*`.

- [ ] **Step 1: failing tests** appended to `type-scale.test.ts`:

```ts
describe("type scale", () => {
  it("sizes text only through the five tokens", () => {
    const decls = [...css.matchAll(/font-size:\s*([^;]+);/g)].map((m) => m[1].trim());
    for (const d of decls) expect(d, d).toMatch(/^var\(--text-(display|heading|body|small|eyebrow)\)$|^inherit$|^1em$/);
  });
  it("defines the scale", () => {
    for (const [k, v] of [["display", "32px"], ["heading", "22px"], ["body", "16px"], ["small", "14px"], ["eyebrow", "13px"]])
      expect(css).toMatch(new RegExp(`--text-${k}:\\s*${v}`));
  });
  it("keeps 13px for mono uppercase eyebrows", () => {
    // every rule that sets var(--text-eyebrow) also sets the mono family and uppercase
    for (const m of css.matchAll(/([^{}]+)\{([^}]*var\(--text-eyebrow\)[^}]*)\}/g)) {
      expect(m[2], m[1]).toMatch(/var\(--font-mono\)/);
      expect(m[2], m[1]).toMatch(/text-transform:\s*uppercase/);
    }
  });
});
```

- [ ] **Step 2:** fails.
- [ ] **Step 3:** define the tokens; map each declaration (T-R1): 13 px ordinary text → `--text-small`; a label that names a section, a column or a fact (table heads, the strip's fact names, `.policy-header dt`, settings eyebrows) → `--text-eyebrow` + mono + uppercase + `letter-spacing: 0.08em`; 14 px → small; 16 px, 0.95rem → body; 18 / 20 px → heading (22) where it is a section title, else body; 26 px → display (32) for h1, else heading; `.875em` / `0.85rem` → small. The log's measure (`--log-w`) and line heights follow the new sizes. Emphasis that used a size becomes weight 600/700 or colour.
- [ ] **Step 4:** tests pass, then look: headless screenshots at 1440×900 and 390×844, both themes, of `/preview/full.html`, `/preview/list.html`, `/preview/settings.html?path=/settings/merge-policies/Smana/cloud-native-ref` (`?theme=dark`). Nothing scrolls sideways at 390 (`web/test/overflow.test.ts` passes). Fix wrapping and truncation the larger sizes cause, batching fixes. Stop the preview server.
- [ ] **Step 5: commit** `feat(ui): one Ogenki type scale`, bundle included.

---

### Task 4: cards, focus, the two bars, the strip

**Files:** `web/src/app.css`, `web/test/type-scale.test.ts` (rename the describe or add `web/test/surfaces.test.ts` if clearer).

- [ ] **Step 1: failing tests** (CSS as text):
  - `aurora-bar` (the token, or `linear-gradient`) appears only in the token definition and in the budget bar's and the evidence bar's fill rules (`.evidence-fill` and the budget meter's fill selector — find it by `grep -n "budget" web/src/app.css`); no `radial-gradient` anywhere.
  - `.class-card`, `.settings-card`, the room page's card selectors (list them from `grep -n "var(--card)" web/src/app.css`) use `border-radius: var(--radius-md)` and `padding: var(--space-4)`.
  - `:focus-visible` rules use `outline: 2px solid var(--ring)` and `outline-offset: 2px` (the 2 px page-colour gap is the offset over the page background); no `box-shadow` focus rings remain.
  - `.strip-next` has no `border-left`/`border-inline-start` side stripe.
- [ ] **Step 2:** fails.
- [ ] **Step 3:** implement. Cards: `background: var(--card); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-4)`; hover (where a card is a link or selectable) `background: var(--surface-raised)` via the existing opacity/`::after` pattern if background transitions would animate anything but opacity — no `background` transition; selected `border-color: var(--primary)`. Keep the existing `--shadow-*` only where Ogenki's `shadow-card` fits (the dock); others drop their shadow for the border. Bars: the fill takes `background: var(--aurora-bar)`; a blocked evidence bar keeps its distinct state (a muted fill plus its word), never the gradient. `.strip-next`: the stripe goes; its emphasis is its heading weight or a `--primary` word.
- [ ] **Step 4:** tests pass; screenshots as in Task 3; impeccable `craft-floor` read before the edit (`/home/smana/.claude/skills/impeccable/reference/craft-floor.md`).
- [ ] **Step 5: commit** `feat(ui): Ogenki cards, focus ring and the aurora bars`, bundle included.

---

### Task 5: DESIGN.md, docs, polish, gate

**Files:** `web/DESIGN.md` (new), `docs/web-ui.md`, `docs/development.md`, `internal/humanapi/ui/dist/`.

- [ ] **Step 1: `web/DESIGN.md`** with impeccable `document` (`/home/smana/.claude/skills/impeccable/reference/document.md`, after `/home/smana/.claude/skills/impeccable/scripts/impeccable context`), from the shipped tokens: the two themes and their token map, the type scale, fonts and licences, cards, focus, status = word + colour, the gradient rule, motion rules, and the contrast test as the gate. It is the one reference for later UI work (PR 3 reads it).
- [ ] **Step 2: docs.** `docs/web-ui.md`: the theme button (light = Day, dark = Night, system); `docs/development.md`: the fonts' location and licences, `type-scale.test.ts` and `contrast.test.ts` as the guards. `task docs` passes.
- [ ] **Step 3: polish and audit.** impeccable `polish` then `audit` on the room page, the room list and the settings pages (both themes, 1440 and 390); `impeccable detect --json` on each preview URL. Fix, or list as intentional in the report with the reason. The previous run's `overused-font` and `flat-type-hierarchy` findings must be gone or explained.
- [ ] **Step 4: gate.** `task ui:build` and commit the bundle; `task check` exits 0 (the known environmental `TestProductionCodeNeverWiresTheDefaultKind` failure from untracked `.worktrees/` is verified on a clean worktree of HEAD and reported, not silenced).
- [ ] **Step 5: commits** `docs(ui): DESIGN.md and the Ogenki theme`, `fix(ui): theme polish` (if polish changed CSS).

---

## Self-review

- Spec Part 2 coverage: tokens (T2), Night/Day values (T2), lanes (T2), ink-on-accent (T2), status word + colour (constraint; T2 pairs), type families and self-hosting (T1), scale 32/22/16/14/13 and emphasis rule (T3), aurora on two bars only, one gradient, no glow (T4), cards and hover/selected (T4), `.strip-next` stripe (T4), focus 2 px + 2 px gap (T4), contrast re-pin and listed adjustments (T2), theme button (constraint; T5 docs), `web/DESIGN.md` via `document` (T5); detector `overused-font` (T5 step 3).
- Names: `--font-display|sans|mono` (T1) used by T3's eyebrow test; `--text-*` (T3); `--radius-md`, `--space-4`, `--aurora-bar`, `--surface-raised` (T2) used by T4.
