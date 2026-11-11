# Room page follow-ups: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the room page's header reads in two tiers, an expired approval stops offering a decision, durations read in days, need cards say where the action happens, the phone dock never hides a decision, and `maku status` shows the same run marks and durations.

**Architecture:** one shared duration rule (a JSON case table read by a TypeScript helper's and a Go helper's tests), then four web/Go changes on top: `roomview`'s runs line, the strip's tiers and NEXT box, the needs cards (expired state, per-kind lines) with the dock CSS, and docs.

**Tech Stack:** TypeScript DOM + vitest, CSS, Go (`internal/roomview`, stdlib tests, goldens with `UPDATE_GOLDEN=1`).

**Spec:** `docs/superpowers/specs/2026-11-11-room-page-follow-ups-design.md` (decisions D1–D7). Design reference: `web/DESIGN.md`.

**Branch:** `ui/room-followups`, from `ui/rail` (#435).

## Global Constraints

- D2 durations: `span` = the two largest units: `40 s`, `12 min`, `5 h 47 min` (`5 h` when minutes are 0), `37 d 17 h` (`37 d` when hours are 0). `ago` = the largest unit only plus " ago" (`12 min ago`, `5 h ago`, `37 d ago`), `just now` under 60 s. Exact time in a `<time datetime>` on the web. One TS helper, one Go helper, one shared JSON table.
- D3 lines, verbatim: merge "Merging happens on GitHub.", review "Reviewing happens on GitHub.", question "Answer it on GitHub.", failed check "Fix it on GitHub.", plan confirmation "Confirm it on GitHub.", other "This happens on GitHub.", merge approval "Approve the pull request on GitHub: the merge gate reads the approval there."
- D4 sentence, verbatim with the deadline's local HH:MM: "No one decided by 16:30, so the agent was told no. It can ask again."
- A status is a word plus a colour; no emoji; type and colour only from `web/DESIGN.md`'s tokens; motion opacity/transform only, none under reduced motion; nothing scrolls sideways at 390 px.
- `task check` before the last commit (offline: `vuln` skipped and stated); each commit keeps `ui:check` clean with the rebuilt bundle. Never `HOME=…`/`env -i`, never GOTMPDIR inside the repo; never push; no attribution lines.

## Rulings made while planning

- **FR1 (`ago` granularity).** The spec's examples give "37 d 17 h" for a span and "37 d ago" for a past time: `span` keeps two units, `ago` one.
- **FR2 (the shared table's home).** `internal/roomview/testdata/durations.json`, read by the Go test directly and by the web test through an import (as `web/preview/settings.ts` imports a Go golden).

---

### Task 1: one duration rule, in TypeScript and Go

**Files:**
- Create: `internal/roomview/testdata/durations.json`, `web/src/duration.ts`, `web/test/duration.test.ts`
- Modify: `internal/roomview/inert.go` (`ago`, ~182), `internal/roomview/card.go` (`duration`, ~560), their tests; `web/src/strip.ts` (`since`, 47–52), `web/src/summary.ts` (`ago`, 172–179), `web/src/approval-help.ts` (`inMinutes`, 63–67), `web/src/settings-api.ts` (`timeText`, ~163) and every caller.

**Interfaces — produces:** `span(ms: number): string`, `ago(ms: number): string` in `web/src/duration.ts`; `Span(d time.Duration) string`, `Ago(d time.Duration) string` in `internal/roomview` (exported only if another package needs them; else unexported `span`/`agoText`).

- [ ] **Step 1: the table** `internal/roomview/testdata/durations.json`:

```json
[
  {"ms": 0, "span": "0 s", "ago": "just now"},
  {"ms": 40000, "span": "40 s", "ago": "just now"},
  {"ms": 60000, "span": "1 min", "ago": "1 min ago"},
  {"ms": 720000, "span": "12 min", "ago": "12 min ago"},
  {"ms": 18000000, "span": "5 h", "ago": "5 h ago"},
  {"ms": 20820000, "span": "5 h 47 min", "ago": "5 h ago"},
  {"ms": 86400000, "span": "1 d", "ago": "1 d ago"},
  {"ms": 3258000000, "span": "37 d 17 h", "ago": "37 d ago"},
  {"ms": 3258060000, "span": "37 d 17 h", "ago": "37 d ago"}
]
```

- [ ] **Step 2: failing tests.** Go (`inert_test.go` or a new `duration_test.go`): read the JSON, `t.Run` per row, compare `span`/`agoText` of `time.Duration(ms)*time.Millisecond`. Web (`duration.test.ts`): `import table from "../../internal/roomview/testdata/durations.json"` and `it.each(table)`.
- [ ] **Step 3:** run both; they fail.
- [ ] **Step 4: implement** both helpers to the Global Constraints rule (floor each unit; negative input reads as 0). Replace the six old formatters and their callers; a future date (the settings exception's end) keeps its locale date and is not routed through `ago`. Where the web shows a span or an ago, wrap it in `<time datetime="<ISO>">` with `title` = locale date-time if the caller has the timestamp. Update every test that pinned the old strings (each named in the report). Regenerate `roomview` goldens with `UPDATE_GOLDEN=1` and check the diff is only duration text.
- [ ] **Step 5:** `go test -race ./internal/roomview ./internal/app -p 2`, `task lint`, `cd web && npx vitest run --maxWorkers=2`, `(cd web && npx tsc --noEmit)`, `task ui:build`, `task ui:check`.
- [ ] **Step 6: commit** `feat(ui,roomview): one duration rule, days past 24 h`.

---

### Task 2: `maku status` runs show steers and chat

**Files:** `internal/roomview/card.go` (`runPhrase`, ~540–585), `internal/roomview/card_test.go`, goldens under `internal/roomview/testdata/`, the summary input `internal/summary/testdata/normal.json` (already has a node with `steered: 1, chat: 1` from PR #435).

**Interfaces — consumes:** `summary.FlowNode.Steered`, `.Chat` (Go fields from #435); `span` (Task 1).

- [ ] **Step 1: failing test.** A table test: a run whose flow node has `Steered: 2, Chat: 3` renders `implementer running · 1 min · ↪ steered ×2 · 3 chat`; zero counts render neither mark; a run with no matching flow node renders as today. Match a run to its node by run id (read how `card.go` builds runs and how `summary.Flow.Nodes` names the run).
- [ ] **Step 2:** fails.
- [ ] **Step 3:** implement in `runPhrase` (marks after the duration, before the cost; the `↪` glyph is text; the markdown and text formats both get it — check `inert.go` escaping keeps them inert).
- [ ] **Step 4:** `UPDATE_GOLDEN=1 go test ./internal/roomview`, then without; review the golden diff (only the runs line of `normal` changes); `go test -race ./internal/roomview ./internal/app ./internal/humanapi ./internal/maku -p 2`; `task lint`.
- [ ] **Step 5: commit** `feat(roomview): maku status shows each run's steers and chat`.

---

### Task 3: the header in two tiers, NEXT says what the room waits on

**Files:** `web/src/strip.ts` (`chips`, 201–260; `fact-group` clusters 177–185; the tail 360–392), `web/src/app.css` (`.page-header .strip-chips` 341–345 and the ≤640 px block 424–433; `.strip-next`), the strip's test file.

- [ ] **Step 1: failing tests.**
  - Tier 1 (`.strip-tier1`): phase, persona, the run's `span` (Task 1), the Needs-you pill, in that order; tier 2 (`details.strip-details > summary` "Details" plus the run id/model/effort, tokens, PR, issue, dashboards, data, driven by). Above 640 px tier 2 renders open and its summary hidden (CSS), wrapping; at ≤640 px it is a closed disclosure. CSS read as text: no `overflow-x: auto` / `nowrap` on the strip's rows.
  - NEXT: with `summary.status` waiting and two approvals pending and a step of kind `approve`, the box holds only the waiting line; with a step that is not a Needs-you item (e.g. kind `merge` when no merge need card exists — read `summary.step` kinds) it holds the waiting line and "Your move: …"; with nothing to say the box is absent (no `.strip-next` in the DOM).
- [ ] **Step 2:** fail.
- [ ] **Step 3:** implement. Keep the clusters' semantics; remove the approvals banner and the "in Needs you below" step from the tail; the Now-line hint suppression (`strip.ts:101-103`) keys on what NEXT still shows.
- [ ] **Step 4:** tests pass; screenshots `/preview/full.html` at 1440×900 and 390×844, both themes (Details closed and open at 390); fix; stop the server; restore `web/preview/list.js`. Read `/home/smana/.claude/skills/impeccable/reference/craft-floor.md` before the CSS edit.
- [ ] **Step 5: commit** `feat(ui): the room header in two tiers; NEXT says what the room waits on`, bundle included.

---

### Task 4: expired approvals, per-kind GitHub lines, the phone dock

**Files:** `web/src/needs.ts` (card build 151–197, `needCard` 84–91), `web/src/approval-help.ts` (`decideStatus` 88–97, deadline text 73–80, `bannerText`), `web/src/app.css` (dock and room body; a `.approval[data-state="expired"]` rule), `web/src/room.ts` (only if the pill/count needs the clock), tests for needs and approval-help.

- [ ] **Step 1: failing tests.**
  - With a fake clock past `expiresAt`: the approval card has `data-state="expired"`, a visible "Expired" word, the D4 sentence with the deadline's HH:MM, no approve/deny buttons, no reason input, "details" still present; a re-render at the deadline flips a live card to expired without a rebuild of the details; the Needs-you count and pill exclude it; before the deadline nothing changes.
  - Per kind (`needCard`): each D3 line verbatim for merge, review, question, failed check, plan confirmation, an unknown kind, and the merge-approval variant; "the room has no button for it" appears nowhere (`rg` in the test over the module's output).
  - CSS read as text: the room body has `padding-bottom: var(--dock-h)`; needs cards (and their buttons' container) have `scroll-margin-bottom: var(--dock-h)`; the dock stays `position: sticky`; the stale "Static, not sticky" comment is corrected.
- [ ] **Step 2:** fail.
- [ ] **Step 3:** implement. The expiry check uses the same clock the deadline text already uses (`needs.ts:194-197` refreshes it each render); `decideStatus` takes the deadline into account so "You can decide." never shows past it. The expired card's tone: the word in the warning colour on its tint, contrast pinned in `contrast.test.ts` if it introduces a new pair.
- [ ] **Step 4:** tests pass; screenshots at 390×844 light (an expired card in the fixture, and the dock not covering a live card's buttons when scrolled so the card is at the bottom) and 1440 dark; stop the server; restore `web/preview/list.js`.
- [ ] **Step 5: commit** `feat(ui): expired approvals stop offering a decision; need cards say where the action happens; the dock never hides a decision`, bundle included.

---

### Task 5: docs, polish, gate

- [ ] **Step 1:** `docs/web-ui.md` (header tiers and Details, NEXT, expired card, durations), `web/DESIGN.md` (the duration rule and the header tiers as patterns), and the `maku status` doc if one describes the runs line (`rg -n "maku status" docs`). `task docs`.
- [ ] **Step 2:** impeccable `polish` and `audit` on `/preview/full.html` (both themes, 1440 and 390), `impeccable detect --json`; fix or list as intentional.
- [ ] **Step 3:** gate, offline: `task spdx tidy lint`, Go tests per package `-p 2` in batches, `task crd:check chart:check quickstart:check migrations docs names scripts ui:test ui:check`; known environmental failures (`.worktrees/` test, offline envtest) verified on a clean worktree and reported.
- [ ] **Step 4: commits** `docs(ui): the room page follow-ups as shipped`, `fix(ui): follow-ups polish` (if any).

## Self-review

D1 → T3; D2 → T1 (+ T3 uses it); D3 → T4; D4 → T4; D5 → T3; D6 → T4; D7 → T1 (durations) + T2 (marks). Names: `span`/`ago` (TS), `span`/`agoText` (Go), `durations.json`, `.strip-tier1`, `details.strip-details`, `data-state="expired"`.
