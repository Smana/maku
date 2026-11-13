# maku-gateway

The agent factory's model gateway, as objects for an [agentgateway](https://agentgateway.dev)
controller (v1.6): runs, the planner and the classifier call it with their own projected
ServiceAccount token, and only the gateway holds a provider key. The seam, the values and the
presets are in [`docs/gateway.md`](../../docs/gateway.md).

Off by default (`enabled: false`): an install that does not turn it on renders nothing. Inside the
`agent-factory` umbrella it is the optional subchart `maku-gateway`.

## Prerequisites

- Kubernetes ≥ 1.30 and Gateway API's standard CRDs ≥ v1.2.
- The agentgateway controller and its CRDs, v1.6: the umbrella's `agentgateway` subchart and the
  vendored CRDs in `config/agentgateway` (`kubectl apply --server-side`), or your own.
- One Secret per provider in the release namespace, holding its API key. The chart names it
  (`providers.<name>.existingSecret`, `key`) and never renders it.

## What it renders

| Object | Name | Why |
|---|---|---|
| Gateway | `<name>` | listeners `public` (8080), `internal` (8081), `clients` (8082), routes from the release namespace only |
| AgentgatewayParameters | `<name>` | the proxy's image by digest, replicas, hardening, the catalog |
| AgentgatewayPolicy | `<name>-strip-identity-headers`, `<name>-listener-{public,internal,clients}`, `<name>-frontend` | each caller's token verified, the identity headers set from it |
| AgentgatewayBackend | `<provider>-<alias>`, `<provider>-planner`, `<provider>-classifier` | one per alias and client, the model forced, the key by Secret name |
| HTTPRoute + AgentgatewayPolicy | `maku-models-<class>-<alias>`, `maku-clients-<client>` | one route per alias, the alias's audience required |
| ConfigMap | `<name>-model-catalog` | the prices the access log's `cost` uses |
| NetworkPolicy | `<name>` | default deny for the proxy pods |

## Tests

- `tests/render.sh` (in `task chart:check`): the values contract and every object's shape.
- `tests/probe/probe.sh`: what the chart relies on, asserted against agentgateway on a throwaway k3d.
