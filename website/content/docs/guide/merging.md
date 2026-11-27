---
title: "7. Merging"
linkTitle: "7. Merging"
weight: 70
diataxis: how-to
description: "Live and shadow policies, why a pull request comes to you and what each reason asks, approving the exact commit you read, and undoing a merge."
---

By the end of this chapter you will know why a pull request is waiting for you, what to do about it, and how to make sure the commit that merges is the one you read.

**The short version:** approve the pull request on GitHub once no run is in progress, and the merge bot merges the commit you approved within a minute. No agent merges its own work. The full gate rules and flow diagram live in [Merge policies]({{< relref "/docs/use/merge-policies.md#holds-agreed-signal-checked" >}}).

## Live and shadow {#live-and-shadow}

A pull request merges with no human only when its [merge policy]({{< relref "writing-a-good-task.md#classes" >}}) is **live** and every one of its checks holds: CI is green, the reviewer approved the current commit, the diff stays inside the policy's limits, and the policy's daily cap is not reached.

A **shadow** policy runs the same checks and records what it *would* have done, then hands the pull request to you. Each shadow decision you merge unchanged is recorded as an **agreed** decision; a policy earns promotion to `live` only after enough agreed evidence and a rollback drill.

Most changes match no live policy and always come to you. That is the default, not a failure.

The gate's comments and the **Settings** pages use a few words precisely. A policy **holds** when every check passes on one head: the change is inside the policy, not proven right. A decision is **signal-checked** when a check independent of the author, such as a link checker, judged what the change means. A shadow decision you merge unchanged is **agreed**, and a live merge with no revert, regression, or follow-up in its watch window is **quiet**. Every term: [Merge policies]({{< relref "/docs/use/merge-policies.md#holds-agreed-signal-checked" >}}).

## Why a pull request comes to you {#why-a-human}

The hand-off comment carries a reason. The ones you will meet:

| Reason | What it means | What you do |
|---|---|---|
| `class_unarmed` | This kind of change never merges on its own | Review it; approve to merge, or request changes |
| `policy_pending` | The repository's policy waits for a maintainer | Approve the current commit |
| `shadow_would_arm` | A shadow policy would have merged it | Review it; approve if the gate was right |
| `approval_stale` | Your approval was of an older commit, or came while an agent was pushing | Approve again, on the current head |
| `approval_required` | The diff touches files that always need a maintainer | Review those files, then approve |
| `auto_merge_cap`, `class_paused` | A live policy hit its daily cap, or the breaker paused it | Approve, or wait for tomorrow's cap |
| `human_commit_in_pr` | A person pushed a commit to the agent's branch | Read that commit; an admin merges, or close the pull request |

The last row is the one an approval cannot fix. The factory cannot vouch for a commit it did not make, so the merge bot refuses it, and the ruleset blocks a plain merge. A maintainer with admin rights and the ruleset bypass merges it by hand, after reading it.

A pull request that conflicts with main does not come to you first: the factory starts one rebase run, counted as one of the task's CI fix runs. With none left, it waits for you as `not_mergeable`.

Every gate reason and its check is listed in [Merge policies]({{< relref "/docs/use/merge-policies.md" >}}).

## Approve the exact head {#approve-the-exact-head}

GitHub attaches an approval to the latest commit at the moment it arrives. If an agent pushes while you read, a command-line approval can approve a commit you never saw, and the merge bot then merges it.

1. Wait until the room shows no run in progress: no **NOW** line, the rail's last node finished.
2. Approve from GitHub's web page, which shows you the commit you are approving.
3. From a terminal, name the commit you read:

```bash
gh api repos/<owner>/<repo>/pulls/<number>/reviews \
  -f commit_id=<sha> -f event=APPROVE
```

If the factory comments `approval_stale`, a new commit arrived after your approval: read it, approve again.

## The merge policy in the room {#merge-policy}

Every factory room has a **Merge policy** fold. Open it to see the task's policy and mode (`live`, `shadow`, or `disabled`), the policy's conditions, what it takes to merge, and, once there is one, the gate's decision. It is read-only: a repository's policy changes through a pull request.

Maintainers change a policy, check a change, and promote a shadow policy from the **Settings** pages: [chapter 11]({{< relref "merge-policies.md" >}}).

## Undo a merge {#revert}

Label a pull request the factory merged on its own `factory/revert`, within 7 days. The factory opens a pull request that undoes it, and the policy's breaker counts the revert. A merge you approved, you revert by hand, as usual.

The full design and specification behind the gate: [Merge policies]({{< relref "/docs/use/merge-policies.md" >}}).

**Next:** [chapter 8]({{< relref "terminal-and-coding-agent.md" >}}) brings the factory into your terminal and your coding agent.
