---
title: "1. Meet the factory"
linkTitle: "1. Meet the factory"
weight: 10
description: "What Maku does for you, the deal between you and the agents, and its four safety rules."
diataxis: how-to
---

By the end of this chapter you will know what the factory does for you, and the rules it keeps.

<figure class="maku-shot">
  <video controls preload="metadata" playsinline poster="/media/maku-run.jpg" style="width:100%;height:auto;border-radius:var(--radius-card);background:#070b22;">
    <source src="/media/maku-run.mp4" type="video/mp4">
    <track kind="captions" src="/media/maku-run.en.vtt" srclang="en" label="English" default>
  </video>
  <figcaption>A real run from <code>/maku handoff</code> in OpenCode through room review, maintainer approval on GitHub, merge, and Grafana spend attribution (2:06).</figcaption>
</figure>

You have more small fixes than time. A dead link, a version number that drifted, a script that needs one more flag. Maku takes that kind of work off your plate.

Here is the deal. You describe the fix: in a Slack thread, to your coding agent, or in a GitHub issue labelled `factory/ready`. The factory hands it to an AI agent, which works on its own branch, in a locked-down sandbox, and opens a pull request.

A second agent reviews that pull request before you see it. If the checks fail, the first agent fixes them. Only then does the factory ask for you.

You review it the way you review any pull request. Ask for changes, and the agent revises it on the same branch, with your review as its instructions. Approve it, and that approval is the merge.

Every task has a room: a live page of what each agent did and said. You can watch it, leave a note, or stop the task at any time.

## Four safety rules {#never}

- **It never merges without your approval**, except a few low-risk kinds of change your platform team lets through, such as link fixes.
- **It never touches CI, branch protection, or its own configuration.**
- **It never sees a long-lived secret.** Each run gets short-lived credentials for one repository.
- **It never reaches the internet** beyond the few hosts it needs.

Every run also has a deadline and a spending cap, so a confused agent stops on its own.

Two names you will meet. *Maku* is the software inside the factory, and `maku` its command-line tool. Your *platform team* is the people who run the factory for your organisation.

**Next:** [chapter 2]({{< relref "before-your-first-task.md" >}}) checks you have what you need.
