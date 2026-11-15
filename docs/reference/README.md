<!-- page-type: reference -->
# Reference

| Page | Covers |
|---|---|
| [API](api.md) | The broker's `:8443`, `:9090`, `:8080` (human, WebSocket and `/mcp`) and `:8090` listeners, webhooks, and the factory's `:8082` task intake |
| [The event envelope](event-envelope.md) | Version 1 event envelope fields, actors, origins, payloads by type and size bounds |
| [The room log](room-log.md) | PostgreSQL schema, role grants, append-only guarantees, output events, sealing, retention, bridge lease and Atlas migrations |
| [The Room CRD](room-crd.md) | `rooms.maku.ogenki.io/v1alpha1` `spec` and `status` fields, validation rules and defaults |
| [The Task CRD](task-crd.md) | `tasks.maku.ogenki.io/v1alpha1` `spec`, `status`, annotations and labels |
| [The config file](config.md) | `maku-factory` and `maku-broker` configuration keys, `demo` rules, per-run token minting and role table, ownership fence, steering and the scoper |
| [Sponsored principals](sponsored-principals.md) | Machine-triggered `api` tasks (`api.sponsored`) and internal principals (`api.internalPrincipals`, `factory/publish`) |
| [Narration and task status](narration.md) | Issue and PR event comments, live status comment, workflow pin refusals, and waiting/escalation/terminal reason codes |
| [Budgets](budgets.md) | Model pricing, `meter.query` PromQL, daily USD ledger ConfigMaps, failover accounting and per-person caps |
| [Components, ports, images and packages](components.md) | Binaries, container images, listening ports, release tags, Kubernetes resources and Go package layout |
| [Metrics](metrics.md) | Prometheus metric series exported by the room broker and the factory |
| [Alerts](alerts.md) | PrometheusRule alert names, firing expressions, durations and actions |
| [Security controls](security-controls.md) | Role-table invariants, authorization matrix, TLS, redaction, database roles, network policies, admission, supply chain and hardening stages |
| [Web UI settings and standings](web-ui-settings.md) | Standing action matrices, `/settings` pages, room header fields, forks, themes and error codes |
| [Install support and cluster reference](support.md) | Version floors, multi-install cluster rules, generated directory layout, preflight CLI checks and private code surfaces |
