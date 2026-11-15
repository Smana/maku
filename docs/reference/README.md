<!-- page-type: reference -->
# Reference

| Page | Covers |
|---|---|
| [API](api.md) | The broker's `:8443`, `:9090`, `:8080` (human, WebSocket and `/mcp`) and `:8090` listeners, webhooks, and the factory's `:8082` task intake |
| [The event envelope](event-envelope.md) | Version 1 event envelope fields, actors, origins, payloads by type and size bounds |
| [The room log](room-log.md) | PostgreSQL schema, role grants, append-only guarantees, output events, sealing, retention, bridge lease and Atlas migrations |
| [The Room CRD](room-crd.md) | `rooms.maku.ogenki.io/v1alpha1` `spec` and `status` fields, validation rules and defaults |
| [The Task CRD](task-crd.md) | `tasks.maku.ogenki.io/v1alpha1` `spec`, `status`, annotations and labels |
| [The config file](config.md) | `maku-factory` configuration keys, `demo` rules, per-run token minting and role table, ownership fence, steering and the scoper |
| [Sponsored principals](sponsored-principals.md) | Machine-triggered `api` tasks (`api.sponsored`) and internal principals (`api.internalPrincipals`, `factory/publish`) |
