---
description: "Maku: /maku handoff <what> | task [#issue] | inbox"
---

You were asked: `/maku $ARGUMENTS`. Use the `maku` MCP server's tools. The first word is the
subcommand; the rest is its argument. Show each tool's text as it is, and never apply a label.

- `handoff [what]`: call `handoff` with the developer's own words for what they gave (default: the
  work just discussed) and the context you have: the repository you are in, the paths you read.
  When it asks one question, ask the developer that question with its recommendation and offer
  "accept the rest"; call `handoff` with its id and their answer, or with `acceptRest: true`. When
  the plan is ready, call `start` with the id: only the developer's answer to its form files the
  plan's first issue and starts the work. Once it started, run `wait` on the task in a background
  task. Never write the plan yourself.
- `task [#issue]`: call `task` with the task as typed (`#<issue>`, `PR #<n>`, a number, a title
  fragment or an id); with none, call `inbox` and take its first task.
- `inbox`: call `inbox` once and show its text as it is. Empty, say so in one line.

With no subcommand, or one that is not listed, show these three lines and stop.
