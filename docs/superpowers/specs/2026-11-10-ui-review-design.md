# UI review: settings, Ogenki theme, run rail: design

**Status:** design, agreed with the owner on 2026-11-10. **Scope:** `web/` (pages, CSS, preview,
tests), `internal/summary` (two additive flow fields), `docs/web-ui.md`, `docs/merge-policies.md`
(the settings editor section), `web/DESIGN.md` (new). **Not here:** the room header's metadata
row, the expired-approval card, `maku status` text; these are follow-ups.

Baseline: impeccable `critique` of 2026-11-10 (`.impeccable/critique/2026-11-10T17-46-36Z__web-src.md`),
24/40, three P1s (the merge-policy editor, dark surfaces, the settings lists) and two P2s (the rail,
mobile). Each part below names the findings it closes.

## Delivery

Three pull requests, in this order, each checked by the owner in the local preview
(`npm --prefix web run preview`) in both themes at 1440 px and 390 px before merge:

| PR | Part | Closes |
|---|---|---|
| 1 | Settings | P1 editor, P1 settings lists, P2 mobile (settings), detector `skipped-heading`, `line-length` |
| 2 | Theme | P1 dark surfaces, the flat type scale, detector `overused-font`, `.strip-next` side stripe |
| 3 | Rail | P2 rail, the ghost-milestone contrast miss, replay removed, room activity on nodes |

Skills, per step: impeccable `craft-floor` before every UI edit; `clarify` and `layout` in PR 1;
`document` in PR 2 (writes `web/DESIGN.md`); `polish` then `audit` and `impeccable detect` at the end
of each PR; `micro-interaction`, `accessible-animation`, `60fps-animation` and `svg-animation` for
every motion change in PRs 1 and 3.

## Part 1: Settings

### 1.0 Preview fixtures first

`web/preview/settings.ts` answers `/api/policy/repositories/<repo>` before the list route (its
prefix match let the list answer every repository) and serves two repositories:

- `Smana/maku`: the factory's golden, `internal/factory/policyview/testdata/ui/merge-policy.json`,
  served from a MergePolicy (the GitOps manifest path).
- `Smana/cloud-native-ref`: a repository-file policy with evidence on every class, one class
  eligible, one open promotion and one exception, so every state renders.

A `?path=` for each settings page is listed in the file's header comment and in
`docs/development.md`.

### 1.1 Settings home: overview cards

`/settings` shows one card per section: **Merge policies** (repositories, then classes by mode),
**Evidence** (classes eligible to promote), **Approval memory** (saved grants). Each card: a 24 px
line icon, the title, one sentence of purpose, the live counts, and the whole card is the link. The
counts come from the routes the sections already read; a failed read shows the card without counts
and says so in one line.

### 1.2 Repository list: a table

| Repository | Classes served from | Classes | Promotion |
|---|---|---|---|
| Smana/maku | MergePolicy `cloud-native-ref` | 1 live · 2 shadow · 2 off | docs-links 14/20 |

One row per repository, read with the per-repository route the Evidence page already fans out to,
at most 4 requests in flight. A row whose read fails keeps its name and says why. Mode counts are a
word and a colour, never a colour alone. No broker change.

### 1.3 Repository page: a class reads as a sentence

The page leads with the class cards, then the decision timeline, then the editor.

```text
┌ docs ─────────────────────────────── SHADOW · records, never merges ┐
│ Would auto-merge an agent's PR that only changes docs/**/*.md or     │
│ README.md, at most 10 files and 200 lines, once "Check the docs      │
│ links" passes. Never if it touches docs/security/. Asks a maintainer │
│ for docs/architecture/. At most 2 a day.                             │
│ Evidence ▓▓▓▓▓▓▓░░░ 14/20 clean merges to promote       [Edit] [YAML]│
└──────────────────────────────────────────────────────────────────────┘
```

- `describeClass(c: ClassPolicy, guardrails): string[]`, a new pure function in
  `web/src/policy-describe.ts`, turns a class into its sentences: one per concern (who, what it may
  touch, size, checks, deny, approval, limits). Every leaf of the closed vocabulary has a phrase; an
  unknown leaf reads "and a condition this page does not describe (`<key>`)", never silence. `any`
  reads "one of: … or …". Tested table-driven over every template and the golden.
- The mode badge always carries its meaning: **live** merges, **shadow** records what it would
  have done, **off** does nothing. "(central)" becomes "set by the factory" with a tooltip naming
  where.
- Evidence is a bar toward the minimum (`aurora-bar` after PR 2) with the count in words; the
  29/46/61 widening ladder is three labelled steps under it. While the factory reads it: "Reading
  evidence from GitHub…" once for the page, not per row.
- Promotions and exceptions render inside their class's card. Empty: one line ("No promotion
  requested"), no bare table.
- Guardrails render as a short list ("Every class: at most 20 files, 400 lines, 5 a day; never
  `.github/`…"), never `JSON.stringify`.
- Each class card has an anchor (`#class=<name>`) so a class can be linked.

### 1.4 Editor: grouped by the question each field answers

- **Pick vs create:** a class picker lists only the classes; "New class from an example" opens the
  gallery (1.5). The legend names the class being edited.
- **Groups**, in order: *Who writes it* (author, adopt logins and branches, harness profile and
  `harnessAllowed`, escalation), *What it may touch* (allowedFiles, companionFiles, protectedFiles),
  *How big* (maxPatchFiles, maxModifiedLines, modifyOnly, diffShape), *What must pass*
  (holdoutChecks, releaseAgeCheck, requiredLabel), *When to ask a human* (requiresApproval
  changedFiles), *Limits* (maxPerDay, followUpWindow). The form now edits `harnessAllowed`,
  `escalation` and `adopt`, which today only the YAML can.
- **Each field:** a plain label, the key in mono beside it ("Files it may touch · `allowedFiles`"),
  one help line from `docs/merge-policies.md` Concepts, and a "Learn more" link to its anchor.
  Number fields are `type=number` with the guardrail as `max`; path lists say "one regular
  expression per line" and mark a line that does not compile.
- **Empty groups fold:** a condition group with no value shows as "+ Add a size limit" etc.
- **The live sentence:** `describeClass` of the form's current value sits at the top of the editor
  and updates as you type.
- **Check stays the judge:** "Check, compare and simulate" moves to a sticky footer of the editor;
  the broker's answer (errors on their field and line, the structural diff, the simulation) shows
  above it. The expert YAML view is a tab beside the form, collapsed by default, and keeps its
  round trip and line marks.
- **Headings:** h2 per page section, h3 per editor group; no level skipped.
- **Mobile:** under 720 px the label stacks above its field; nothing scrolls sideways.

### 1.5 Example gallery

Two rows, **Simple** and **Advanced**, each card: its sentence (from `describeClass`), "Why you'd
want this" (one or two lines), the YAML, and "Start from this".

| Level | Example | Shape |
|---|---|---|
| Simple | `docs-broad` | allowedFiles, size, a link check, deny, approval paths |
| Simple | `deps-patch` | an adopted Renovate patch bump, semver-patch, modifyOnly, release age |
| Simple | `generated-files` | regenerated files only, a holdout regeneration check |
| Advanced | `deps-minor` | semver-minor, shadow only (M2) |
| Advanced | `docs-or-tests` (new) | an `any` of two branches: docs only, or tests only |
| Advanced | `infra-with-approval` (new) | shadow, infrastructure paths with `requiresApproval` on a sensitive subtree and a daily cap |

The examples stay in `web/src/policy-templates.json`, so `TestEveryEditorTemplatePassesCheck`
checks the two new ones too. Level and "why" live in `web/src/policy-examples.ts`, keyed by
template name; a test fails when a template has no entry or an entry has no template.

## Part 2: Theme (Ogenki, full adoption)

The Ogenki design system (`ogenki-design-system-and-template.zip`, `design-system/`) replaces the
palette and type; the maku mark stays.

- **Tokens:** the existing semantic names in `web/src/app.css` (`--background`, `--card`,
  `--primary`, `--muted-foreground`, `--flow-*`, …) keep their names and take Ogenki values, so
  components do not change.

  | Role | Night (dark) | Day (light) |
  |---|---|---|
  | Page / card / raised | `#0D1330` / `#161C42` / `#1F2754` | `#F5F6FC` / `#FDFDFF` / `#ECEEF8` |
  | Line | `#3A4478` | `#B9BFDC` |
  | Text / secondary | `#EEF0FA` / `#A9B1D6` | `#12163A` / `#545B85` |
  | Link, primary, focus | `#5AA9F0` | `#1D63B0` |
  | Ok / warn / critical | `#3DDC97` / `#FFB547` / `#FF6FB5` | `#11795A` / `#965800` / `#C2306F` |
  | Lanes: writer / judge / plan | blue / violet / amber | same, Day values |

  Text on an accent fill uses `ink-on-accent`. A status is always a word plus a colour.
- **Type:** headings Space Grotesk 700, body Inter 400, code, ids and metrics JetBrains Mono. The
  three woff2 files and their OFL licences are self-hosted under `web/src/assets/fonts/`, so no
  external font request and no CSP change. Screen scale: 32 / 22 / 16 / 14 / 13 (eyebrow, mono,
  uppercase); emphasis by weight or colour, never a new size. The rail and settings use the same
  scale.
- **Aurora:** `aurora-bar` on the token budget bar and the evidence bar only, at most one gradient
  per screen; no glow backgrounds on working pages.
- **Cards:** `surface-100`, 1 px `line` border, `radius-md`, padding `space-4`; hover raises to
  `surface-200`; selected adds a 1 px blue border. The `.strip-next` box loses its side stripe.
- **Focus:** 2 px solid focus ring with a 2 px page-colour gap.
- **Contrast:** `web/test/contrast.test.ts` re-pins every pair to the new values: 4.5:1 for text,
  3:1 for input boundaries and focus. A pair that fails is adjusted, and the adjustment is listed in
  the PR.
- **Theme button:** keeps light / dark / system; Day is light, Night is dark.
- **`web/DESIGN.md`:** written by impeccable `document` from the shipped tokens, the one reference
  for later UI work.

## Part 3: Rail

- **Replay removed:** `web/src/replay.ts`, the replay bar, their tests, the preview replay page and
  the Replay sentence in `docs/web-ui.md`.
- **Size:** the column goes from 300 to 360 px; the persona 15 px, meta 14 px, glyphs 22 px.
  Finished nodes lose `opacity: .72` and ghosts `opacity: .6`: state shows as the ✓ or chip and a
  muted lane border, and text stays at full contrast (fixes the ghost milestone at 4.0:1).
- **Room activity on nodes:** `summary/v1` gains two additive fields on `FlowNode`, counted from the
  room's `message` events whose seq falls inside the run (`FromSeq`..`ToSeq`, or open while live):
  - `steered`: messages with `delivery: steering`; the node shows `↪ steered ×N`, and its hover
    lists who and when (from the events the page holds; "earlier" when not loaded).
  - `chat`: messages with `delivery: none`; the node shows a 2 px-stroke chat icon and N as a muted count (no emoji: Ogenki).

  Queued notes are already `status.queued`: the next ghost node, or the gap after the live node,
  shows `N queued → next run`. Absent fields read as zero, so an older broker renders as today.
  `internal/summary` tests the fold table-driven: a steer inside a run, at its boundaries, during
  no run, and a live run.
- **Mobile:** below 1100 px the dot row becomes a labelled strip ("scoper ✓ · tester ✓ ·
  implementer ● · reviewer ○") that still opens the drawer.
- **Motion:** a new mark or chip fades in over 150–200 ms (opacity and transform only, no bounce);
  the edge into a new node draws as today. Under reduced motion nothing moves; marks appear.

## Testing

- vitest for every new pure function (`describeClass`, the example table, the mode counts, the
  rail marks) and DOM tests for the settings home, list, class card and editor groups, in the
  existing `web/test/settings.test.ts` style.
- `internal/summary` table tests for `steered` and `chat`; the summary golden updated with
  `-update-golden`.
- `task check` passes, `ui:check` included (the committed bundle rebuilt).
- Each PR ends with impeccable `polish`, `audit` and `impeccable detect --json` on the preview
  pages; findings either fixed or listed as intentional in the PR.
