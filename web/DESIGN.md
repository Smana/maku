---
name: maku
description: The room UI of maku, the log of record for agent runs, in the Ogenki design system's Night and Day.
colors:
  night-page: "#0d1330"
  night-card: "#161c42"
  night-raised: "#1f2754"
  night-line: "#3a4478"
  night-ink: "#eef0fa"
  night-ink-muted: "#a9b1d6"
  night-blue: "#5aa9f0"
  night-violet: "#a78bfa"
  night-green: "#3ddc97"
  night-amber: "#ffb547"
  night-pink: "#ff6fb5"
  night-input: "#6f79b0"
  night-warning-text: "#ffcf85"
  night-ink-on-accent: "#0d1330"
  day-page: "#f5f6fc"
  day-card: "#fdfdff"
  day-raised: "#eceef8"
  day-line: "#b9bfdc"
  day-ink: "#12163a"
  day-ink-muted: "#545b85"
  day-blue: "#1d63b0"
  day-violet: "#6a4bd8"
  day-green: "#11795a"
  day-amber: "#965800"
  day-pink: "#c2306f"
  day-input: "#7a82a8"
  day-warning-text: "#6b3f00"
  day-ink-on-accent: "#fdfdff"
typography:
  display:
    fontFamily: "Space Grotesk, Arial, sans-serif"
    fontSize: "32px"
    fontWeight: 700
    lineHeight: "40px"
  heading:
    fontFamily: "Space Grotesk, Arial, sans-serif"
    fontSize: "22px"
    fontWeight: 700
    lineHeight: "30px"
  body:
    fontFamily: "Inter, Arial, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: "26px"
  small:
    fontFamily: "Inter, Arial, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: "20px"
  mono:
    fontFamily: "JetBrains Mono, Courier New, monospace"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: "20px"
    fontFeature: "tnum"
  eyebrow:
    fontFamily: "JetBrains Mono, Courier New, monospace"
    fontSize: "13px"
    fontWeight: 500
    lineHeight: "16px"
    letterSpacing: "0.08em"
rounded:
  control: "6px"
  row: "8px"
  card-legacy: "12px"
  md: "16px"
  pill: "9999px"
spacing:
  "1": "4px"
  "2": "8px"
  "3": "12px"
  "4": "16px"
  "6": "24px"
  "8": "32px"
  "12": "48px"
  "16": "64px"
components:
  card:
    backgroundColor: "{colors.night-card}"
    textColor: "{colors.night-ink}"
    rounded: "{rounded.md}"
    padding: "16px"
  card-hover:
    backgroundColor: "{colors.night-raised}"
  button:
    backgroundColor: "{colors.night-card}"
    textColor: "{colors.night-ink}"
    rounded: "{rounded.control}"
    padding: "2px 10px"
  button-primary:
    backgroundColor: "{colors.night-blue}"
    textColor: "{colors.night-ink-on-accent}"
    rounded: "{rounded.control}"
  input:
    backgroundColor: "{colors.night-card}"
    textColor: "{colors.night-ink}"
    rounded: "{rounded.control}"
    padding: "2px 6px"
  chip:
    textColor: "{colors.night-ink-muted}"
    rounded: "{rounded.pill}"
    padding: "0 8px"
    typography: "{typography.small}"
  needs-pill:
    backgroundColor: "{colors.night-amber}"
    textColor: "{colors.night-ink-on-accent}"
    rounded: "{rounded.pill}"
    padding: "2px 10px"
---

# Design System: maku

The one reference for UI work on `web/`. The values live in `web/src/app.css`; this file says
how to use them. When the two disagree, `app.css` and its tests win and this file is stale: fix it
in the same change. The frontmatter lists the Night value of each component; Day swaps every
`night-*` for its `day-*` twin.

## Overview

**Creative North Star: "A healthy system at night"**

maku borrows Ogenki's Night Ops look: the precision of an operations console, calm and dark,
with light only where it means something. A room page is read for long stretches by someone
deciding whether an agent may act, so the surfaces stay quiet and the colour is spent on state:
what is running, what waits on you, what failed.

The density is that of a working tool, not a landing page. A tool call is one line, a status is a
word with a colour, a number carries its unit. Space Grotesk names things, Inter carries the
prose, JetBrains Mono carries everything a machine wrote: ids, commands, timestamps, counters.

**Key Characteristics:**
- Two themes on the same token names: Night (`.dark`) and Day (`:root`, the default).
- Flat, bordered surfaces in three steps: page, card, raised.
- One type scale of five sizes; emphasis by weight or colour.
- Colour means state; a state is always a word too.
- One gradient per screen: the aurora, on the budget bar only.

## Colors

A deep navy ground with a cool blue voice; amber, pink and green are kept for state.

### Primary
- **Signal Blue** (night `#5aa9f0`, day `#1d63b0`): links, the primary button, the focus ring
  (`--link`, `--primary`, `--ring`), a running state, the selected card's border, the writer's
  lane on the rail (`--flow-writer`).

### Secondary
- **Judge Violet** (night `#a78bfa`, day `#6a4bd8`): the judge's lane on the rail
  (`--flow-judge`). Nowhere else.

### Tertiary (state)
- **Healthy Green** (`--success`, `--success-text`): finished, done, a live class.
- **Waiting Amber** (`--warning`, `--warning-border`, `--flow-plan`): something waits, the
  Needs you pill, a plan step no run has reached, a budget past 80 %. Amber text on a tint uses
  `--warning-foreground` (night `#ffcf85`, day `#6b3f00`), never the base amber.
- **Critical Pink** (`--destructive`, `--destructive-text`): failed, denied, an error, a budget
  spent.

### Neutral
- **Night Ground / Day Paper** (`--background`, `--muted`): the page, and inset surfaces.
- **Panel** (`--card`): cards, the table, bubbles, inputs.
- **Raised** (`--surface-raised`): the dock, a hovered card, table headers.
- **Hairline** (`--border`): every 1 px border and divider. Decorative: about 1.8:1 on a card,
  never the only edge of something you must find. The boundaries that must be seen meet 3:1:
  `--input`, a state border (`--warning-border`, `--destructive`), the focus ring, a status icon.
- **Ink / Muted Ink** (`--foreground`, `--muted-foreground`): text and secondary text, readable on
  all three surfaces in both themes. `--placeholder` is muted ink.
- **Input Edge** (`--input`, night `#6f79b0`, day `#7a82a8`): a form control's boundary, derived to
  reach 3:1 on a card and on the dock where the hairline does not.
- **The maku mark** keeps its own colours (`#8B5CF6` core with `#0A0A0F` ink in Day,
  `#F3F2FA` in Night); Ogenki's logo blue is not used.

### Named Rules
**The Word-and-Colour Rule.** A state is always a word plus a colour: "Waiting for you" with an
amber clock, "rejected" with its icon. Never a coloured dot, border or bar alone.

**The Ink-on-Accent Rule.** Text on an accent fill (the primary button, the Needs you pill, a
solid warning) uses `--ink-on-accent`, or a semantic twin set to the same value (`--primary-foreground`,
`--warning-solid-foreground`, …), never literal white. The twins repeat the hex rather than
`var()` it; `contrast.test.ts` pins them equal.

**The Derived-Token Rule.** When an Ogenki value misses its contrast pair, keep the Ogenki value
and give that one use a derived token (`--input`, `--warning-foreground`). Never shift the base.

## Typography

**Display Font:** Space Grotesk (with Arial, sans-serif)
**Body Font:** Inter (with Arial, sans-serif)
**Label/Mono Font:** JetBrains Mono (with Courier New, monospace)

**Character:** a geometric grotesk for names and titles over a neutral workhorse sans; the mono
is not a costume, it marks text a machine produced.

All three are variable WOFF2 files self-hosted in `web/src/assets/fonts/` under the SIL Open Font
License 1.1 (`LICENSE-space-grotesk.txt`, `LICENSE-inter.txt`, `LICENSE-jetbrains-mono.txt`),
loaded with `font-display: swap` from the page's own origin. No font CDN; the CSP stays
`font-src 'self'`. Every family goes through `--font-display`, `--font-sans` or `--font-mono`.

### Hierarchy
- **Display** (700, 32 px / 40 px, `--text-display`): the room's title, once per page. 22 px on a
  phone.
- **Heading** (700, 22 px / 30 px, `--text-heading`): the wordmark, a list or settings page title,
  a settings section.
- **Body** (400, 16 px / 26 px, `--text-body`): prose, bubbles, h2 to h4 (in 700). The log's
  measure is `--log-w` (72ch).
- **Small** (14 px / 20 px, `--text-small`): meta, chips, code, tool I/O. Code, ids, times and
  counters set it in mono with tabular numerals.
- **Eyebrow** (mono 500, 13 px / 16 px, uppercase, `letter-spacing: 0.08em`, `--text-eyebrow`): a
  label that names a column, a fact or a group (`TOKENS`, `NEXT`, `STATUS`). One shared rule in
  `app.css`; a component sets only its colour.

### Named Rules
**The Five-Sizes Rule.** 32 / 22 / 16 / 14 / 13 and nothing else. Emphasis is weight (700) or
colour, never a sixth size. `web/test/type-scale.test.ts` refuses a `font-size` or a `font:`
shorthand off the scale.

**The Label-Not-Kicker Rule.** An eyebrow names what follows it on the same line or in the same
column. It is never a kicker above a heading and never a sentence.

## Layout

The room page is three bands: the header (title strip), the log with the rail beside it from
1100 px (`--log-w` column, then a 360 px rail), and the dock pinned at the foot. Below 1100 px
the rail folds into a labelled strip (each run's persona and its state glyph) that opens it as a
drawer. Phone breakpoints sit at 640 px (header and dock reflow, the settings nav becomes a
scrolling row) and 720 px (settings forms and the repository table stack).

Spacing tokens are the Ogenki 4 px scale (`--space-1` to `--space-16`: 4, 8, 12, 16, 24, 32, 48,
64), for new work: most existing rules still write literal px (6, 10, 14 among them), and only
`--space-4` (card padding) is referenced so far. The same holds for the raw Ogenki tokens
(`--surface-*`, `--line`, `--blue`, …): declared and pinned, while rules read the semantic
names. Page
gutters are 16 px at every width; the settings pages inset their content 16 px more. Group tightly, separate generously.

Every interactive target reaches `--hit`: 40 px on a dense desktop, 44 px at 640 px and below. A
control drawn smaller extends with a pseudo-element, never over a neighbour's.

## Elevation & Depth

Flat by tone. Depth comes from the three surfaces (page, card, raised) and the 1 px hairline, not
from shadows. Cards carry none. The shadows that remain:

### Shadow Vocabulary
- **Dock** (`--shadow-dock`): under the dock that floats over the stream, and the rail drawer.
- **Ring** (`--shadow-border`): the log's own boxes, which are not cards: a bubble and the brief
  (both at `--radius-card`, 12 px), the checked delivery segment, and the rail popover (with the
  dock shadow).
- **Hover ring** (`--shadow-border-hover`): a button, a need's Go link and a rail button on
  hover.

### Named Rules
**The No-Glow Rule.** No glow backgrounds, no coloured halo, no glass on a working page.

**The One-Gradient Rule.** At most one gradient per screen. `--aurora-bar` (pink to violet,
90deg) fills the room page's token budget bar (`.meter-bar::after`) and nothing else. Night uses
Ogenki's aurora; Day derives its own from Day pink to Day violet, so both stops hold 3:1 on the
bar's track (the bar is a meaningful graphic). A budget
past 80 % or spent turns its bar solid amber or pink. The evidence bar repeats once per class on
a repository page, so it fills with solid `--primary`, muted when blocked.
`web/test/surfaces.test.ts` pins the one user.

## Shapes

Gentle, concentric corners. A card is 16 px (`--radius-md`), a row (a call's body, the
composer, the YAML editor) 8 px (`--radius-row`), a control 6 px (`--radius-control`), a pill
9999 px. A box inside a bordered box takes the outer radius minus the border (a tool row's
summary is `calc(var(--radius-md) - 1px)`). `--radius-card` (12 px) survives on bubbles and
the brief; new work uses `--radius-md`. Literal 6 px and 999px radii remain in older rules; they
equal `--radius-control` and `--radius-pill`.

Borders are 1 px, with the side stripes listed under Don't as the only exceptions.

## Components

### Buttons
- **Shape:** gently rounded (6 px), 1 px hairline, card fill.
- **Primary:** Signal Blue fill with ink-on-accent text (approve, Send, Open the pull request).
- **Ghost (deny):** the destructive word, no border until hovered, then a 10 % pink tint.
- **Hover / Focus / Active:** hover adds the hover ring (150 ms ease-out); focus is the ring
  below; press scales to .96 only where motion is welcome; disabled is 55 % opacity.

### Chips
- **Style:** a pill with a hairline border and muted small text (`.chip`). A mode badge (live,
  shadow, off) takes its state's border and text colour.
- **Needs you pill:** solid amber with ink-on-accent text, the count in bold.

### Cards / Containers
- **Corner Style:** 16 px (`--radius-md`).
- **Background:** Panel (`--card`).
- **Shadow Strategy:** none (see Elevation & Depth).
- **Border:** 1 px hairline. A refused call tones its border `--warning-border`, a failed call
  `--destructive`. A pending approval or need keeps a 3 px `--warning-border` bar on its left
  edge (the owner's choice, #345), drawn straight between the 16 px corners, never bent round
  them.
- **Hover:** a link card (settings home) fades a raised layer in behind its text and lifts 1 px.
- **Selected:** the 1 px border turns `--primary`. Today only the class a link points at
  (`.class-card:target`) is selectable.
- **Internal Padding:** 16 px (`--space-4`). A tool row keeps its dense 6 px / 10 px summary.

### Inputs / Fields
- **Style:** card fill, 1 px `--input` edge (3:1), 6 px radius, placeholder in muted ink.
- **Focus:** the ring below.
- **Error:** the field's message in `--destructive-text`; the YAML gutter marks the line in bold
  pink.

### Navigation
- **Settings nav:** small 500 text in muted ink; the current page in full ink with a 2 px blue
  bar under it. Below 640 px it is a single row that scrolls sideways under a fade.
- **Theme button:** in the header or the dock, cycles `theme: light` (Day), `theme: dark`
  (Night) and `theme: system` (follows the OS). `web/src/theme.ts` owns the `.dark` class on
  `<html>`; no `prefers-color-scheme` block replaces it.

### Focus ring
A 2 px solid `--ring` outline with a 2 px offset, so the page colour shows as the gap. A ring
inside a clipping container (the settings nav's phone row, the YAML editor's textarea) insets at
-2 px; each one is named with its reason in `web/test/surfaces.test.ts`. Never a box-shadow ring.

### Status line
A room's status, in the room list and the title strip: an icon in its state's colour, then the
words ("Running — implementer, 13 min"). The icon is aria-hidden; the words say it all.

## Do's and Don'ts

### Do:
- **Do** add a colour through a semantic token in both `:root` and `.dark`, and pin its pair in
  `web/test/contrast.test.ts` first: 4.5:1 for text, 3:1 for a border, an icon, an input
  boundary or the focus ring. The test is the gate; a pair that fails gets a derived token.
- **Do** say every state in words as well as in colour.
- **Do** set code, ids, times and counters in `--font-mono` with tabular numerals.
- **Do** animate with opacity and transform only, 150 to 200 ms ease-out. Gate motion on
  `prefers-reduced-motion: no-preference`, or cancel it in a `reduce` block (the file does both:
  the press scale and the rail sit inside `no-preference`, `settle-in` and the settings-card lift
  are cancelled under `reduce`); under reduce, nothing moves and marks simply appear. Hover may fade a colour or the hover ring over 150 ms.
- **Do** use the theme's `accent-color` and selection tint; they come from `--primary`.

### Don't:
- **Don't** draw a side stripe (a coloured `border-left` or an inset bar) on a card, a row or a
  list item: tone the card's own 1 px border or the status icon. The stripes that ship, and
  stay:
  - the approval and need bar: 3 px `--warning-border`, a `::before` drawn straight between the
    16 px corners (the owner's choice, #345);
  - the rail's lanes: `.rail-button` and `.rail-coordination-node`, 3 px in the lane's colour,
    muted to 70 % over the card once the run ends, dashed for a planned step;
  - the step thread: `.step-calls`, a 2 px hairline (pink-tinted when the step failed) that ties
    a step's calls together.
- **Don't** add a font size, a font family literal or a second gradient.
- **Don't** put literal white text on an accent, or a colour outside the tokens.
- **Don't** use emoji; icons are 2 px-stroke line SVGs in one ink.
- **Don't** give a card a shadow, or nest a card in a card.
- **Don't** transition `background` or a layout property on hover; fade a layer's opacity instead.
