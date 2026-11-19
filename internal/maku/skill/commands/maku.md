---
description: "Maku: /maku handoff <what> | task [#issue] | inbox | cancel <task>"
---

You were asked: `/maku $ARGUMENTS`. Use the `maku` MCP server's tools. The first word is the
subcommand; the rest is its argument. Show each tool's text as it is, and never apply a label.

- `handoff [what]`: call `handoff` with the developer's words word for word as `text` (default:
  the work just discussed); never reword or shorten them. Pass `repository` (owner/name, from
  `git remote get-url origin`) and, in `context`, the paths you read. When it returns a question,
  ask the developer that question with its recommendation and offer "accept the rest"; do not
  answer it yourself, and do not add questions of your own. Call `handoff` with its id and their
  answer exactly as they typed it, or with `acceptRest: true`. When the plan is ready, call
  `start` at once with the id: the form is the developer's confirmation, so never ask "do you want
  me to start it?" first. When `start` says the form was not answered or not confirmed, offer to
  call `start` again (the plan is kept). Once it started, run `wait` on the task in a background
  task. Never write the plan yourself, and never apply a label.
- `task [#issue]`: call `task` with the task as typed (`#<issue>`, `PR #<n>`, a number, a title
  fragment or an id); with none, call `inbox` and take its first task.
- `inbox`: call `inbox` once and show its text as it is. Empty, say so in one line.
- `cancel <task> [discard]`: call `cancel` with the task (and `discard: true` if asked); its form is the confirmation.

With no subcommand, or one that is not listed, show these four lines and stop.
