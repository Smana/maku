---
title: "2. Before your first task"
linkTitle: "2. Before your first task"
weight: 20
description: "The five things you need before you hand the factory a task, and who gives you each one."
diataxis: how-to
---

By the end of this chapter you will have what you need, and know whom to ask for each piece.

It is a one-time set-up. To hand over a task from GitHub you need only the first item; the other four let you watch it in its room, and Slack and your coding agent need them too. If you do not know your platform team, ask whoever told you about the factory.

## The five things {#five-things}

**1. Maintainer rights on the repository.** Labels and reviews are how you talk to the factory. A *maintainer* is someone the repository lets approve and merge, usually GitHub's Maintain or Admin role. The repository's owner grants it. The repository must also be connected to your Maku install by your platform team ([Onboard a repository]({{< relref "/docs/use/onboard-a-repository.md" >}})).

**2. Reachability to the rooms URL.** Ask your platform team for your rooms web address (for example `https://rooms.example.com`). Depending on how your cluster exposes Maku ([Exposure]({{< relref "/docs/install/exposure.md" >}})), this may be on your corporate VPN or private network, or behind a standard HTTPS Ingress/Gateway.

**3. Certificate trust (private CA installs only).** If your rooms URL uses an internal certificate authority rather than a public certificate (such as Let's Encrypt), ask your platform team for the CA certificate and add it to your operating system trust store once.

**4. Membership in the rooms access group.** You sign in to the room pages with your organisation's OIDC identity provider (Okta, Entra ID, Keycloak, ZITADEL, or any standard OIDC IdP). Sign in once so your account exists, then ask an administrator to add you to the configured member group (commonly `agents-member`; see [Identity]({{< relref "/docs/install/identity.md" >}})).

**5. Your GitHub account linked to your sign-in.** You see a room only if GitHub lets you read its repository, so the broker must know who you are on GitHub. Link the accounts yourself once through the room UI ([GitHub link]({{< relref "/docs/use/github-link.md" >}})), or check if your identity provider already supplies the GitHub claim automatically.

## Check it works {#check}

Open the room list. You should see your repositories' rooms, or an empty list with no error. A `403` means step 4 is missing; an empty list with a GitHub link prompt means step 5 is. The operator's side of identity and exposure is in [Install › Identity]({{< relref "/docs/install/identity.md" >}}) and [Install › Exposure]({{< relref "/docs/install/exposure.md" >}}).

Two more, for the path you choose in [chapter 3]({{< relref "your-first-task.md" >}}):

- **Slack:** mention `@factory link` once in a channel the app serves, and confirm the page it sends you.
- **Your coding agent:** install `maku`, then run `maku setup <client>` and `maku doctor` ([chapter 8]({{< relref "terminal-and-coding-agent.md#from-a-terminal-maku" >}})).

Before the agents can test their work on your repository, it needs three small files: [Make a repository ready]({{< relref "/docs/use/make-a-repository-ready.md" >}}).

**Next:** [chapter 3]({{< relref "your-first-task.md" >}}) takes your first task from request to merge.
