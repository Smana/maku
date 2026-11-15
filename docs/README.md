<!-- page-type: how-to -->
# maku documentation

<a href="../README.md">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/png/maku-lockup-horizontal-dark.png">
    <img src="assets/brand/png/maku-lockup-horizontal-light.png" alt="Maku, a controlled stage for delegated agent work" width="300">
  </picture>
</a>

Reference for the broker, bridge, factory, CLI and web UI. See the [status page](https://airef.ogenki.io/docs/status/) for live platform status.

## Try it

| Page | Answers |
|---|---|
| [Quickstart](quickstart.md) | One-command laptop demo (`task demo`) and what production adds |
| [Team evaluation](team-evaluation.md) | Adding approved workflows to the demo and comparing `apply` vs `observe` |

## Use it

| Page | Answers |
|---|---|
| [Onboard a repository](onboard-a-repository.md) | One path from a GitHub repository to a first task (`maku repo add` and `maku repo check`) |
| [Merge policies](merge-policies.md) | Catalog, bindings, rollout stages, fields, recipes, checking and promoting |
| [maku](cli.md) | Install, one-command setup, subcommands, MCP tools and `maku-handoff` |
| [Web UI](web-ui.md) | Signing in, the room list, working a room, approvals and refusals |
| [GitHub account link](github-link.md) | Linking a GitHub account, revocation and the activity window |
| [Slack App](slack.md) | Manifest, config, `/maku` and thread flows |

## Understand it

| Page | Answers |
|---|---|
| [Concepts](concepts.md) | Vocabulary of rooms and the factory, room phases, roles |
| [Architecture](architecture.md) | Services, trust boundaries, one task's life, runtime placement |
| [The factory](factory.md) | Task lifecycle, triage, budgets, merge gate, escalation, kill switch and splits |
| [Hardening](hardening.md) | Untrusted-input hardening across stages H1–H5 and T5 promotion |
| [Multi-repository coordination](multi-repository.md) | Cross-repository plans, confirmations, artifact dependencies and recovery |
| [Security](security.md) | Threat model, trust boundaries, identities, authorization and confinement |
| [Roadmap](roadmap.md) | Open directions |

## Run it

| Page | Answers |
|---|---|
| [Install on your cluster](install.md) | Production seams, `maku install --print`, Helm, Flux, Argo CD, preflight, private code |
| [Sandbox nodes](sandbox-nodes.md) | gVisor and Kata RuntimeClasses by platform and warm-node placeholders |
| [The database](database.md) | Postgres 16+ roles, DSN Secrets, bootstrap, managed services, CNPG and Atlas |
| [Identity](identity.md) | OIDC presets, roles claim, token use `id` and oauth2-proxy arguments |
| [GitHub Apps](github-apps.md) | Agents, factory, merger and policy-bot App permissions, keys and startup checks |
| [Model gateway](gateway.md) | agentgateway seam, values, provider presets and caller tokens |
| [Exposure](exposure.md) | `expose.mode`, HTTPRoute/Ingress surfaces and oauth2-proxy front door |
| [Install Secrets](install-secrets.md) | Every Secret an install reads and ExternalSecret examples |
| [Observability](observability.md) | Scrape objects, alert rules, dashboards and OpenTelemetry trace endpoints |
| [Integration](integration.md) | Connecting external agents or MCP clients and consumer version pins |
| [Operations](operations.md) | Index of the three operations guides |
| [Operating rooms](operations/rooms.md) | Opening, steering, inspecting and recovering a room |
| [Operating the factory](operations/factory.md) | Running Tasks, the merge gate, repository checks and coordination |
| [Failures, alerts and the kill switch](operations/failures.md) | Symptom tables, alert triage, the kill switch and backup/restore |
| [Injection drills](drills.md) | Drill repository, `maku drill plant`, scoring and the stop rule |
| [Upgrading](upgrading.md) | Release-by-release upgrade notes and chart adoption |

## Look it up

| Page | Answers |
|---|---|
| [Reference index](reference/README.md) | Index of every reference page |
| [API](reference/api.md) | Broker and factory HTTP routes, MCP tools, webhooks and status codes |
| [Event envelope](reference/event-envelope.md) | Signed event envelope fields, kinds and schema |
| [Room log](reference/room-log.md) | Append-only room log format, turns, verdicts and compaction |
| [Room CRD](reference/room-crd.md) | `Room` custom resource spec and status fields |
| [Task CRD](reference/task-crd.md) | `Task`, `Repository`, `MergePolicyCatalog`, `AutonomyBinding` and `CoordinationPlan` fields |
| [Config file](reference/config.md) | `~/.config/maku/config.yaml`, broker flags and bridge/factory contract |
| [Sponsored principals](reference/sponsored-principals.md) | Sponsored principal tokens, scopes and audit fields |
| [Narration](reference/narration.md) | GitHub and Slack narration templates and comment markers |
| [Budgets](reference/budgets.md) | Token, turn, wall-clock and daily repository budget fields |
| [Components](reference/components.md) | Images, ports, Go packages, charts and resource tables |
| [Metrics](reference/metrics.md) | Prometheus metrics emitted by the broker, factory, bridge and webhook |
| [Alerts](reference/alerts.md) | `PrometheusRule` alerts, expressions and runbook links |
| [Security controls](reference/security-controls.md) | Per-control enforcement matrix, container posture and network policies |
| [Web UI settings](reference/web-ui-settings.md) | Read-only setting and standing panels in `/ui/settings` |
| [Support](reference/support.md) | Supported Kubernetes, PostgreSQL, runtime and private-code surfaces |

## Contribute

| Page | Answers |
|---|---|
| [Contributing notes](contributing/README.md) | Contributor procedures and test harnesses ([CONTRIBUTING.md](../CONTRIBUTING.md), [AGENTS.md](../AGENTS.md)) |
| [Development](contributing/development.md) | Postgres test harness, migration checks, `web/` build and `demo-e2e.yaml` |
| [The one-run quickstart](contributing/one-run-quickstart.md) | `task quickstart` (`maku run`) and the `quickstart-e2e` CI job |
| [Stubbed spec-intake functional test](contributing/intake-functional-test.md) | Running `task chart:intake:k3d` locally against in-cluster stubs |

Design sources: [rooms](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-collaboration-rooms-design.md),
[factory](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-dark-factory-design.md),
[programme](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-factory-design.md)
and [local-first UX](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-10-08-agent-factory-local-first-ux-design.md).
