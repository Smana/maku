# maku documentation

<a href="../README.md">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/png/maku-lockup-horizontal-dark.png">
    <img src="assets/brand/png/maku-lockup-horizontal-light.png" alt="Maku, a controlled stage for delegated agent work" width="300">
  </picture>
</a>

The reference for the broker, the bridge, the factory, the CLI and the web UI; the platform's docs site holds the overview, and its [status page](https://airef.ogenki.io/docs/status/) says what is proven live.

## Pages

| Page | Answers |
|---|---|
| [Install on your cluster](install.md) | A production install on a cluster your team runs: the seams you bring, `maku install --print`, Helm, Flux and Argo CD, the preflight, private code, sandbox nodes, the support floor |
| [Quickstart](quickstart.md) | The factory loop on a laptop in one command (`task demo`), and what production adds |
| [Team evaluation](team-evaluation.md) | Adding approved workflows to the demo and comparing `apply` and `observe` outcomes |
| [Concepts](concepts.md) | The vocabulary of rooms and the factory; a room's phases; who does what |
| [Architecture](architecture.md) | The services and images, the trust boundaries, one task's life, where each piece runs |
| [The factory](factory.md) | What a label starts, the Task lifecycle, triage and budgets, the merge gate, escalation, the kill switch and splits |
| [Hardening](hardening.md) | Untrusted-input hardening across stages H1–H5: audit, safe outputs, enforced push, filtered reads, drills, readiness and T5 promotion |
| [Multi-repository coordination](multi-repository.md) | Cross-repository plan, confirmations, exact artifact dependencies, integration manifests, budgets and recovery |
| [GitHub Apps](github-apps.md) | The agents App and the factory App, the optional merger App and policy-bot: each App's permissions, where its key goes, the startup check |
| [Merge policies](merge-policies.md) | A repository's merge policies: start here, the catalog, bindings, rollout stages, every field, worked examples, recipes, checking and promoting, reasons, rollback |
| [maku](cli.md) | Install and verify, one-command setup, every command, the six MCP tools, the maku-handoff skill |
| [Web UI](web-ui.md) | The room list, the room page, what each standing may do |
| [GitHub account link](github-link.md) | Each human links their GitHub account once: the flow, revocation, the activity window, a human with no link |
| [Slack App](slack.md) | The factory's Slack App: manifest, config, the two flows, what it never does |
| [The database](database.md) | Bring your own Postgres 16 or later: the three roles and their DSN Secrets, the bootstrap, the start-up checks, managed services, CNPG and Atlas adoption |
| [Reference](reference/README.md) | The broker and factory [API](reference/api.md), [event envelope](reference/event-envelope.md), [room log](reference/room-log.md), [Room CRD](reference/room-crd.md), [Task CRD](reference/task-crd.md), [config file](reference/config.md), [sponsored principals](reference/sponsored-principals.md), [narration](reference/narration.md) and [budgets](reference/budgets.md) |
| [Injection drills](drills.md) | The drill repository and accounts, `maku drill plant`, how the factory scores a drill, reading a report, the stop rule |
| [Exposure](exposure.md) | `expose.mode`: the web and Slack surfaces by HTTPRoute or Ingress, what must be public, the web front door (oauth2-proxy) and its policies, what the probe proved |
| [Install Secrets](install-secrets.md) | Every Secret an install reads, by name and key; the umbrella's optional ExternalSecrets, with OpenBao, CNPG and AWS examples |
| [Identity](identity.md) | Any OIDC issuer: the presets, the roles claim, token use `id`, oauth2-proxy's arguments, what each IdP issues, moving off ZITADEL |
| [Security](security.md) | Threat model, identities, TLS, redaction, database roles, network, supply chain |
| [Model gateway](gateway.md) | agentgateway in front of every model: the seam, the values, the provider presets, each caller's token |
| [Integration](integration.md) | What this repo ships and what each consumer pins; the broker's config |
| [Observability](observability.md) | The scrape objects, the alert rule and the dashboards the umbrella chart renders, what each component serves; traces, their two endpoints and what run tracing opens on the network |
| [Operations](operations.md) | Metrics, alerts, SQL on the log, retention, backups, failures; the factory's signals |
| [Upgrading](upgrading.md) | What changes in the install steps, release by release: from the demo to production (the production seams), adopting the umbrella from `maku-factory`, the CRD chart and its adoption, `values-demo.yaml` |
| [Roadmap](roadmap.md) | What shipped, what is open |

## Reading paths

| You want to | Read |
|---|---|
| Try it | [Quickstart](quickstart.md) |
| Onboard a repository | [Install](install.md) → [GitHub Apps](github-apps.md) → [Merge policies: start here](merge-policies.md#start-here); `maku repo add` and `maku repo check` write and check one repository's files ([maku](cli.md#commands)) |
| Turn on auto-merge | [Merge policies](merge-policies.md) |
| Operate it | [Install](install.md) → [Operations](operations.md) → [Observability](observability.md) → [Security](security.md) |

To change the code, read [AGENTS.md](../AGENTS.md) and [Contributing](contributing/README.md), then the
[event envelope](reference/event-envelope.md), the [room log](reference/room-log.md) and the [API](reference/api.md).

Design sources: the [rooms design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-collaboration-rooms-design.md),
the [factory design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-dark-factory-design.md),
the [programme design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-factory-design.md)
and the [local-first UX design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-10-08-agent-factory-local-first-ux-design.md).
The ruling ids in code comments point into their plans.
