# Room page follow-ups: design

**Status:** design, agreed with the owner on 2026-11-11 in a grilling (Q1–Q8 below). **Scope:**
`web/` (the strip, the needs cards, the dock, one duration helper), `internal/roomview` (the
`maku status` card), `docs/web-ui.md`, `web/DESIGN.md`. Follows
`2026-11-10-ui-review-design.md`, whose "Not here" line named these three follow-ups: the room
header's metadata row, the expired-approval card, the `maku status` text. **Delivery:** one pull
request stacked on the run-rail PR (#435).

## Facts this design rests on

Read from the code on 2026-11-11, before the questions:

- **Expiry is already enforced by the broker.** A leader sweep every 30 s closes each approval past
  `expires_at` with `approval_decided{decision: expired}` and the agent is told no
  (`internal/store/approvals.go`, `internal/bridgeapi/deliver.go`). A human decision after the
  deadline is refused (`approval_expired`) whether or not the sweep ran. The server's `needsYou`
  already drops an approval past its deadline; the web card does not, so between the deadline and
  the sweep it says "The deadline has passed … You can decide." with live buttons that end in a
  rejection (`web/src/approval-help.ts`, `web/src/needs.ts`).
- **The header's fact row** is `chips()` in `web/src/strip.ts`, four clusters (run, cost, links,
  who); below 640 px it is one line that scrolls sideways (`app.css` `.page-header .strip-chips`).
  The NEXT box (`strip.ts` tail) holds the waiting line, an approvals banner and "Your move: …, in
  Needs you below".
- **Durations** have four web formatters (`since` in `strip.ts`, `ago` in `summary.ts`,
  `inMinutes` in `approval-help.ts`, the settings `timeText`) and two in Go (`duration` in
  `internal/roomview/card.go`, `ago` in `internal/roomview/inert.go`); none rolls hours into days.
- **The phone dock** is sticky; nothing reserves its height, so mid-scroll it floats over approval
  buttons. `--dock-h` is measured (`room.ts`) but only the desktop rail uses it.
- **"On GitHub: the room has no button for it."** is the shared fallback line of several need
  kinds in `needs.ts` (`needCard`), with a merge-approval variant.
- **`maku status`** prints `roomview.Card`: a "Runs" line per run (role, trigger, outcome,
  duration, cost), queued notes, and live approvals only; no flow marks.

## Decisions

| # | Decision |
|---|---|
| D1 (Q1) | **The header in two tiers.** Tier 1, always visible and wrapping: phase, persona, how long the run has been going, the Needs-you pill. Tier 2: run id, model and effort, tokens, PR, issue, dashboards, data, driven by; it wraps on desktop and folds behind a "Details" disclosure (`<details>`, closed by default) below 640 px. Nothing in the header scrolls sideways. |
| D2 (Q2) | **Durations use the two largest units:** "40 s", "12 min", "5 h 47 min", "37 d 17 h"; past forms "37 d ago" ("just now" under a minute). The exact time sits in a `<time datetime>` (web) and stays in the stamp the CLI already prints. One TypeScript helper replaces `since`, `ago`, `inMinutes` and the settings `timeText` (an exception's future end keeps its locale date); one Go helper replaces `roomview`'s `duration` and `ago` (no date cap). Both are tested against one shared case table (a JSON file read by the Go and the web tests). |
| D3 (Q3, Q7) | **A verb per need kind** replaces "On GitHub: the room has no button for it.": merge "Merging happens on GitHub.", review "Reviewing happens on GitHub.", question "Answer it on GitHub.", failed check "Fix it on GitHub.", plan confirmation "Confirm it on GitHub.", any other kind "This happens on GitHub."; merge approval "Approve the pull request on GitHub: the merge gate reads the approval there." |
| D4 (Q4) | **An expired approval card.** Once the deadline passes by the browser's clock, the card turns "Expired" (the word plus `--muted-foreground`/warning tone, never colour alone): "No one decided by 16:30, so the agent was told no. It can ask again." No approve, deny or reason field; "details" stays. The card leaves when the broker's `approval_decided{expired}` lands, as today. The approvals count and the Needs-you pill drop it at the deadline, like the server's `needsYou`. |
| D5 (Q5) | **NEXT says only what the room waits on.** The approvals banner and "Your move: …, in Needs you below" leave the box (the pill links to the cards). A "Your move" step that is not a Needs-you item stays. The box hides when it would be empty. |
| D6 (Q6) | **The phone dock never hides a decision.** The room body reserves `padding-bottom: var(--dock-h)`; needs cards and their buttons get `scroll-margin-bottom: var(--dock-h)`. The dock stays sticky. |
| D7 (Q8) | **`maku status` catches up with the page:** each run in "Runs" shows its steers and chat ("implementer running · 1 min · ↪ steered ×2 · 3 chat", omitted when zero), and every duration and "ago" uses D2. Expired approvals stay unprinted, as on the page once the broker closes them. The card goldens are updated with `UPDATE_GOLDEN=1`. |

## Testing

- Web: vitest for the duration helper (the shared table), the strip's tiers and NEXT rules, the
  expired card (fake clock past the deadline: no buttons, the sentence, pill count), the per-kind
  lines, the dock CSS (padding and scroll margin read as text). Screenshots at 1440 and 390, both
  themes.
- Go: `roomview` table tests and goldens for the runs line and durations; the shared duration
  table.
- `task check` passes, `ui:check` included.
