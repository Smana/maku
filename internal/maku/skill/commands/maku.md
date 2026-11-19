---
description: "Maku: /maku handoff <what> | task [#issue] | inbox"
---

You were asked: `/maku $ARGUMENTS`. Use the `maku` MCP server's tools. The first word is the
subcommand; the rest is its argument. Show each tool's text as it is, and never apply a label.

- `handoff [what]`: call `handoff` at once with the developer's words word for word as `text`
  (default: the work just discussed) and no other argument; never reword or shorten them. Load no
  skill, read no file and run no command first, git included: maku reads the repository from the
  workspace. Then do what each result's `next` says, and never show `next` itself:
  - a question: ask the developer that one question with its recommendation, nothing else. In a
    question tool, offer two options, the recommendation and "Accept the rest". Do not answer it
    yourself, and do not add questions of your own. Call `handoff` with its id and their answer
    exactly as they typed it, or with `acceptRest: true`.
  - a ready plan: show it as it is, then call `start` at once with the id, in the same turn. The
    form is the developer's confirmation, so never ask "do you want me to start it?" first.
  - `start` not answered or not confirmed: say nothing was filed and the plan is kept; call
    `start` again only when the developer asks.
  - started: run `wait` on the task in a background task.
  - a refusal: do its `next`; never reword the request to get past it.
  Never write the plan yourself, and never apply a label.
- `task [#issue]`: call `task` with the task as typed (`#<issue>`, `PR #<n>`, a number, a title
  fragment or an id); with none, call `inbox` and take its first task.
- `inbox`: call `inbox` once and show its text as it is. Empty, say so in one line.

With no subcommand, or one that is not listed, show these three lines and stop.
