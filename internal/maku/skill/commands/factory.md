---
description: "The agent factory: /factory handoff <what> | status [room] | resume [room] | inbox"
---

You were asked: `/factory $ARGUMENTS`. Use the `factory` MCP server's tools. The first word is the
subcommand; the rest is its argument. Show each tool's text as it is, and never apply a label.

- `handoff [what]`: call `intake_plan` with the developer's own words for what they gave (default:
  the side task just discussed) and the context you have — the repository you are in, the paths you
  read — then show its text as it is and ask its question if it carries one. Once the developer has
  answered, call `issue_file` with the plan and its hash: it files the plan's first issue, so say
  when a longer plan's others are not filed. Never write the plan yourself and never apply a label.
  If `intake_plan` is not listed the planner is off: write the issue in the template's three sections
  (`## What is wrong`, `## What it should be`, `## Acceptance check`), have `issue_draft` check it,
  then call `issue_file`, and say the broker planned nothing.
  When `issue_file` returns `started: true`, start a background `room_wait` on the room at once (see the skill), without being asked.
- `status [room]`: call `room_summary` for the room. The room may be `#<issue>`, `PR #<n>`, a bare
  number or a room id, passed as typed; with none, use the `rooms_list` row updated most recently.
- `resume [room]`: call `needs_list` once. With a room given, pass it as typed (`#<issue>`,
  `PR #<n>`, a bare number or a room id) to `room_brief` and to `needs_list` with `room` for what it
  waits on; with none, take the first item's room and call `room_brief` for it. Show each tool's text
  as it is, name the brief's `Next:` line and its action link as the one next step, and never say a
  room exists before `room_brief` shows it. If the list is empty, say so in one line and name
  `rooms_list` with `mine`.
- `inbox`: call `needs_list` once and show its text as it is. Empty, say so in one line and name
  `rooms_list` with `mine`; do not call `rooms_list` unasked.

With no subcommand, or one that is not listed, show these four lines and stop.
