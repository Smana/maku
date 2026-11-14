# maku documentation

<a href="naming.md">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/png/maku-lockup-horizontal-dark.png">
    <img src="assets/brand/png/maku-lockup-horizontal-light.png" alt="Maku, a controlled stage for delegated agent work" width="300">
  </picture>
</a>

The reference for the broker, the bridge, the factory, the CLI and the web UI; the platform's docs site holds the overview and the status.

## Pages

| Page | Answers |
|---|---|
| [Install on your cluster](install.md) | A production install on a cluster your team runs: the seams you bring, `maku install --print`, Helm, Flux and Argo CD, the preflight, private code, sandbox nodes, the support floor |
| [Quickstart](quickstart.md) | The factory loop on a laptop in one command (`task demo`), what production adds, and the older one-run path |
| [Concepts](concepts.md) | The vocabulary of rooms and the factory; the room's phases; the Room CRD |
| [Architecture](architecture.md) | The services and images, the trust boundaries, one task's life, where each piece runs |
| [The factory](factory.md) | What a label starts, the Task lifecycle, triage and budgets, the merge gate, escalation, the kill switch; the Task CRD, the config file, the factory's API |
| [GitHub Apps](github-apps.md) | The agents App and the factory App, the optional merger App and policy-bot: each App's permissions, where its key goes, the startup check |
| [Merge policies](merge-policies.md) | A repository's own merge classes: the model, the six rollout switches, every field, recipes, checking and promoting, grants, reasons, rollback |
| [maku](cli.md) | Install and verify, configure, every command, the factory-handoff skill |
| [Web UI](web-ui.md) | The room list, the room page, what each standing may do |
| [GitHub account link](github-link.md) | Each human links their GitHub account once: the flow, revocation, the activity window, moving off the ZITADEL reader |
| [Slack App](slack.md) | The factory's Slack App: manifest, config, the two flows, what it never does |
| [Event envelope](event-envelope.md) | The C4 envelope, its payloads and limits |
| [The database](database.md) | Bring your own Postgres 16 or later: the three roles and their DSN Secrets, the bootstrap, the start-up checks, managed services, CNPG and Atlas adoption |
| [Room log](room-log.md) | The schema, the guarantees the database enforces, sealing, retention, migrations |
| [API](api.md) | The broker's four listeners, their errors and limits |
| [Injection drills](drills.md) | The drill repository and accounts, `maku drill plant`, how the factory scores a drill, reading a report, the stop rule |
| [Exposure](exposure.md) | `expose.mode`: the web and Slack surfaces by HTTPRoute or Ingress, what must be public, the web front door (oauth2-proxy) and its policies, what the probe proved |
| [Install Secrets](install-secrets.md) | Every Secret an install reads, by name and key; the umbrella's optional ExternalSecrets, with OpenBao, CNPG and AWS examples |
| [Identity](identity.md) | Any OIDC issuer: the presets, the roles claim, token use `id`, oauth2-proxy's arguments, what each IdP issues, moving off ZITADEL |
| [Security](security.md) | Threat model, identities, TLS, redaction, database roles, network, supply chain |
| [Model gateway](gateway.md) | agentgateway in front of every model: the seam, the values, the provider presets, each caller's token |
| [Integration](integration.md) | What this repo ships and what each consumer pins; the broker's config |
| [Observability](observability.md) | The scrape objects, the alert rule and the dashboards the umbrella chart renders, what each component serves; traces, their two endpoints and what run tracing opens on the network |
| [Operations](operations.md) | Metrics, alerts, SQL on the log, retention, backups, failures; the factory's signals |
| [Upgrading](upgrading.md) | What changes in the install steps, release by release: from the demo to production (`profile: production`'s seams), adopting the umbrella from `maku-factory`, the CRD chart and its adoption, `values-demo.yaml` |
| [The group move](maku-group-migration.md) | History: v0.31.0's quiet window, `Room` and `Task` to `maku.ogenki.io`; its command is removed |
| [Development](development.md) | The store test harness, migrations, the web build and preview |
| [Roadmap](roadmap.md) | What shipped, what is open |
| [Naming](naming.md) | Maku the product name, the unchanged technical names, the visual identity |

## Reading paths

| You are | Read |
|---|---|
| New here | [README](../README.md) → [Concepts](concepts.md) → [Architecture](architecture.md) |
| A maintainer with a factory issue or PR | [The factory](factory.md) → [Web UI](web-ui.md) → [Concepts](concepts.md) |
| A developer who wants the CLI | [maku](cli.md) → [Web UI](web-ui.md) |
| Operating a cluster | [Architecture](architecture.md) → [Integration](integration.md) → [chart README](../charts/maku-factory/README.md) → [Operations](operations.md) → [Observability](observability.md) → [Security](security.md) |
| Changing the code | [AGENTS.md](../AGENTS.md) → [Development](development.md) → [Event envelope](event-envelope.md) → [Room log](room-log.md) → [API](api.md) |

Design sources: the [rooms design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-collaboration-rooms-design.md),
the [factory design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-dark-factory-design.md),
the [programme design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-factory-design.md)
and the [local-first UX design](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-10-08-agent-factory-local-first-ux-design.md).
The ruling ids in code comments point into their plans.
