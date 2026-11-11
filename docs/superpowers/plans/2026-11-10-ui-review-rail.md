# Run rail: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the room page's run rail drops replay, reads at the Ogenki scale, shows the room's activity on each node (steers, chat, queued notes), and becomes a labelled strip on narrow screens.

**Architecture:** one additive Go change to `summary/v1` (`steered` and `chat` counts on `FlowNode`, folded from the room's human `message` events by seq), then web changes to the rail (`web/src/rail.ts`, `railpane.ts`, `flow.ts`, `app.css`): replay removed, the column at 360 px, full-contrast text, new marks and the queued chip, and the narrow strip.

**Tech Stack:** Go (stdlib `testing`, goldens with `UPDATE_GOLDEN=1`), TypeScript DOM, vitest, CSS.

**Spec:** `docs/superpowers/specs/2026-11-10-ui-review-design.md`, **Part 3**. Design reference: `web/DESIGN.md` (written in PR 2).

**Branch:** `ui/rail`, created from `ui/ogenki-theme` (PR 2): the rail uses PR 2's type and colour tokens.

## Global Constraints

- `summary/v1` changes are additive: absent `steered`/`chat` read as zero, so an older broker renders as today.
- Rail text on the PR 2 scale (plan T-R2): persona `var(--text-body)` (16 px), meta `var(--text-small)` (14 px); glyphs 22 px; column 360 px (from 300).
- Finished nodes and ghosts lose their opacity fade (`opacity: .72`, `.6`): state shows as the ✓ or chip and a muted lane border; text stays at full contrast (fixes the ghost milestone at 4.0:1). `web/test/contrast.test.ts` pins the ghost milestone and plan ghost text at 4.5:1.
- Marks: `↪ steered ×N` (hover/focus lists who and when from the events the page holds; "earlier" when not loaded); chat: a 2 px-stroke chat icon and N as a muted count (no emoji); `N queued → next run` on the next ghost node, or in the gap after the live node, from `status.queued`.
- Mobile below 1100 px: the dot row becomes a labelled strip ("scoper ✓ · tester ✓ · implementer ● · reviewer ○") that still opens the drawer.
- Motion: a new mark or chip fades in over 150–200 ms (opacity and transform only, no bounce); the edge into a new node draws as today; under reduced motion nothing moves.
- A status is a word plus a colour; no emoji; headings not skipped; nothing scrolls sideways at 390 px.
- `task check` exits 0 before the last commit; every commit keeps `ui:check` clean (rebuilt bundle in it). Never `HOME=…`/`env -i`; never push; no attribution lines.

## Rulings made while planning

- **RR1 (who counts).** "The room's `message` events" means messages a human (or a sponsored principal acting for one) posts to the room: `actor` not a run. A run's own messages and judge verdicts do not count. `steered` = `delivery: steering`, `chat` = `delivery: none`; `queued` is already `status.queued`.
- **RR2 (attribution by seq).** The fold is in seq order, so a human message counts on every run whose window is open at its seq: started (`FromSeq ≤ seq`) and not ended (`ToSeq == 0` or `seq ≤ ToSeq`). At a boundary, seq == FromSeq or seq == ToSeq counts on that run. A message during no run counts nowhere (it shows in the log as today).
- **RR3 (replay).** The in-app replay (`web/src/replay.ts`, the rail's Replay / Next / Skip buttons and their hold-refetch logic) and the preview's replay (`web/preview/replay.ts`, `replay-bar.ts`, the demo's use of them) go. The demo (`npm run demo`) keeps working as a static live-fixture page without replay. Other uses of the word "replay" in the code (event re-delivery, tail catch-up) are not replay and stay.

---

### Task 1: `steered` and `chat` on a flow node

**Files:** `internal/summary/flow.go` (`FlowNode`, line 217; `foldFlow`, line 78; `flowRun` and `flowView`), `internal/summary/flow_test.go`, goldens under `internal/summary/testdata/` (regenerate with `UPDATE_GOLDEN=1`), `web/src/summary.ts` (`FlowNode` type: `steered?: number; chat?: number`).

- [ ] **Step 1: failing table test** in `flow_test.go`, cases: a steer inside a run (count 1 on it); a steer at `seq == FromSeq` and one at `seq == ToSeq` (each counts); a chat during no run (no node counts it); a steer during a live run (`ToSeq` open) counts; two overlapping runs (both open) each count the same steer; a run's own `delivery: none` message and a judge verdict count nothing; a `delivery: queued` message counts nothing on nodes. Use the file's existing event builders.
- [ ] **Step 2:** `go test ./internal/summary -run Steer -count=1` fails.
- [ ] **Step 3:** `FlowNode` gains `Steered int \`json:"steered,omitempty"\`` and `Chat int \`json:"chat,omitempty"\``; `flowRun` counts them; in `foldFlow`, on an `envelope.Message` whose actor is not a run (match how the file already tells a run's event from a human's, e.g. `ev.RunID == ""` and the actor kind) and whose payload `Delivery` is `DeliverySteering` or `DeliveryNone`, increment each open run (RR2). Doc comments state RR1/RR2.
- [ ] **Step 4:** `UPDATE_GOLDEN=1 go test ./internal/summary`, then without; `go test -race ./internal/summary ./internal/humanapi/... ./internal/roomview/... -count=1`; `task lint`. Web: the type gains the two optional fields; `npm --prefix web test`.
- [ ] **Step 5: commit** `feat(summary): count steers and chat on each flow node`.

---

### Task 2: replay removed

**Files:** delete `web/src/replay.ts`, `web/preview/replay.ts`, `web/preview/replay-bar.ts`, `web/test/flow-replay.test.ts`; modify `web/src/rail.ts` (the import on line 12, the `rail-replays` controls at lines 107–135, the held-refetch path), `web/preview/demo.ts`, `web/preview/demo.html`, `web/src/app.css` (`.replay-bar` rules, `.rail-replays`/`.rail-replay`), `docs/web-ui.md` (the Rail row's "Replay: …" sentence), any test that asserts the Replay buttons.

- [ ] **Step 1:** a failing test in the rail's test file: a finished task's rail renders no `.rail-replays` and no button named Replay, Next or Skip; a refetch during what used to be a replay applies at once.
- [ ] **Step 2:** fails.
- [ ] **Step 3:** remove per RR3. `rg -n "replay" web/src web/preview web/test docs/web-ui.md` afterwards: each remaining hit is a non-replay meaning and is named in the report.
- [ ] **Step 4:** `npm --prefix web test`, `(cd web && npx tsc --noEmit)`, `npm --prefix web run preview:build` succeeds (restore `web/preview/list.js`), `bash web/scripts/demo-site.sh <scratch dir>` still builds, `task docs`.
- [ ] **Step 5: commit** `refactor(ui): remove the rail's replay`, bundle included.

---

### Task 3: the rail at full size and contrast, with the room's activity

**Files:** `web/src/rail.ts`, `web/src/flow.ts` (if it builds node rows), `web/src/glyphs.ts` / `icons.ts` (a 2 px-stroke chat icon, inline SVG like the existing glyphs), `web/src/app.css` (lines ~915–1020: the rail block and the two `300px` grid columns), `web/test/contrast.test.ts`, the rail's test file.

**Interfaces:** consumes `FlowNode.steered`, `FlowNode.chat` (Task 1) and `Summary.status.queued` (existing; read how `web/src/summary.ts` types it).

- [ ] **Step 1: failing tests.**
  - A node with `steered: 2` shows `.rail-steered` with text `↪ steered ×2`; its hover/focus card lists each steering message the page holds whose seq is in the node's window as "<who>, <time ago>", and "earlier" when the count exceeds what is loaded. `steered` 0 or absent: no mark.
  - A node with `chat: 3` shows `.rail-chat` with an `svg` icon and `3`, and an `aria-label` "3 chat messages during this run".
  - With `status.queued` of length 2: the first ghost node shows `.rail-queued` "2 queued → next run"; with no ghost, the chip sits after the live node; none when the queue is empty.
  - CSS (read as text): `.rail-node[data-state="done"] > .rail-button`, `[data-state="ghost"]`, `.rail-milestone[data-state="ghost"]`, `.rail-dot[data-state="done"]`, `.rail-edge[data-state="ghost"] .rail-stroke` set no `opacity` below 1 (the edge stroke may stay dashed); the grid columns use `360px`; rail persona uses `var(--text-body)` and meta `var(--text-small)`; the glyph size is 22 px.
  - `contrast.test.ts`: the ghost milestone text (`--flow-plan`) and ghost persona text on a card at 4.5:1; the muted lane border at 3:1.
- [ ] **Step 2:** fail.
- [ ] **Step 3:** implement. The marks fade in with the existing `rail-enter` pattern at 150–200 ms (opacity + transform), disabled under reduced motion. Where `--flow-plan` fails 4.5:1 as text in a theme, give ghost text `--muted-foreground` and keep `--flow-plan` for the dashed border (state still reads from the dashed lane and the "planned" word).
- [ ] **Step 4:** tests pass; screenshots 1440×900, both themes, `/preview/full.html` (fixture nodes need `steered`/`chat` and a queued note: add them to the preview fixture). Read the impeccable craft floor and the `micro-interaction`, `accessible-animation`, `svg-animation` skills before the motion edit.
- [ ] **Step 5: commit** `feat(ui): the rail at full size, with steers, chat and queued notes`, bundle included.

---

### Task 4: the narrow strip

**Files:** `web/src/railpane.ts` (the dots button, line 20), `web/src/rail.ts` (the dot row), `web/src/app.css` (`@media (max-width: 1099.98px)`, line ~984), the railpane test.

- [ ] **Step 1: failing test:** below 1100 px the `.rail-dots` button's content is a labelled strip: for nodes scoper (done), tester (done), implementer (running), reviewer (ghost), its text reads `scoper ✓ · tester ✓ · implementer ● · reviewer ○`; it keeps its `aria-expanded`/`aria-controls` and opens the drawer; a long path wraps, never scrolls sideways (overflow test at 390 px).
- [ ] **Step 2:** fails.
- [ ] **Step 3:** implement with the state glyphs as text plus the words in an accessible name ("scoper done, tester done, implementer running, reviewer planned").
- [ ] **Step 4:** tests pass; screenshot 390×844 both themes.
- [ ] **Step 5: commit** `feat(ui): a labelled run strip on narrow screens`, bundle included.

---

### Task 5: docs, polish, gate

- [ ] **Step 1:** `docs/web-ui.md` Rail row: 360 px, the marks, the queued chip, the narrow strip, no replay; the summary/v1 doc (find it: `rg -n "summary/v1" docs`) gains `steered` and `chat` with RR1/RR2 in one sentence each. `task docs`.
- [ ] **Step 2:** impeccable `polish` then `audit` on `/preview/full.html` (both themes, 1440 and 390), `impeccable detect --json`; fix or list as intentional.
- [ ] **Step 3:** `task ui:build`, commit the bundle; `task check` exits 0 (the environmental `.worktrees/` test verified on a clean worktree, reported).
- [ ] **Step 4: commits** `docs(ui): the rail as shipped`, `fix(ui): rail polish` (if any).

## Self-review

Part 3 coverage: replay removed (T2), size and contrast (T3), steered/chat with hover and "earlier" (T1, T3), queued chip (T3), absent fields as zero (T1 omitempty + optional TS), fold tests at boundaries / no run / live (T1), mobile strip (T4), motion and reduced motion (T3). Names: `steered`, `chat` (Go JSON ↔ TS), `.rail-steered`, `.rail-chat`, `.rail-queued`.
