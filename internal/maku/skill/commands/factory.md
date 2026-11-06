---
description: "The agent factory: /factory handoff <what> | status [#issue] | inbox"
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
- `inbox`: call `rooms_list` with `needsMe`, then with `mine`.

With no subcommand, or one that is not listed, show these three lines and stop.
