# maku chart

The umbrella chart: the room broker, the run controller (`runController`), the `Room` and `AgentRun`
CRDs (`crds/`), and the runs namespace with its default-deny policy. It has no factory: install
[maku-factory](../maku-factory/README.md) on its own.

## Values that matter

| Value | Meaning |
|---|---|
| `runs.{group,namespace,namePrefix}` | The only source of the run kind: the broker's `runs:` and its run-issuer `subPattern` both render from it |
| `runController.enabled` | On by default. Refused at render with any `runs.group` but `maku.ogenki.io`, the only group the controller reconciles: turn it off where another controller runs the AgentRuns (cloud-native-ref's `cloud.ogenki.io` composition) |
| `broker.image.tag` | `<version>@sha256:<digest>`; a bare tag is refused unless `e2e.unpinnedImages` |
| `broker.publicURL` | `oidc`: the https URL humans use. `local`: `http://localhost:<forwarded port>` (default `:8080`); the broker answers that port only, under `localhost`, `127.0.0.1` or `[::1]` |
| `broker.human.mode` | `oidc` (block passed through verbatim) or `local` (one loopback user; `:8080` is not exposed, use a port-forward) |
| `broker.runIssuer.mode` | `jwks` (`issuer`, `jwksURL`) or `tokenreview` (binds `system:auth-delegator`) |
| `broker.roomTools.enabled` | The agents' `:8090` room tools listener. On by default: the block absent, or `enabled` unset, means on. An explicit `false` (the quickstart's laptop profile) renders `roomTools.enabled: false`, and the broker binds no `:8090` at all. On, `ROOMS_MCP_KEY` alone decides whether a call is accepted, so supply it through `broker.extraEnv`; unset, every call is refused `401` |
| `broker.roomTools.auth` | `key` (unset: the gateway's `ROOMS_MCP_KEY` and verified subject, as above) or `tokenreview`: the broker verifies each run's own token (audience `room-tools`) with the apiserver. `tokenreview` needs `broker.runIssuer.mode: tokenreview` and reuses its `system:auth-delegator` binding; it adds the Service port `8090`, the broker CNP's ingress from the runs, and the run controller's `roomToolsURL` (`http://room-broker.<ns>.svc.<clusterDomain>:8090/mcp`, plain HTTP in the cluster), which gives each run with a room `MCP_URL` and the token, and its bridge `MCP_BARE_SERVER=room-broker` and `MCP_READ_ONLY_TOOLS` (the five room tools), so the bare tool names the harness sends pass the unattended policy |
| `broker.policyManagement` | Rendered verbatim as the broker's `policyManagement` (`promotion`, `operators`; the factory's type). `promotion: true` serves `/settings` and `/api/policy/*` and needs `broker.factoryURL`. Empty (the default): no settings page |
| `broker.database.brokerSecret` / `ownerSecret` | Secrets with key `uri`. An `ownerSecret` (the `rooms_owner` DSN) adds the `migrate` init container, which reads it as `ROOMS_MIGRATION_DATABASE_URL`; the broker reads `brokerSecret` as `ROOMS_DATABASE_URL`. An empty `brokerSecret` omits `ROOMS_DATABASE_URL`: supply it through `broker.extraEnv` |
| `broker.tls.issuerRef.name` | Renders a cert-manager `Certificate` for `broker.tls.secretName`; empty = bring the Secret yourself |
| `broker.tls.caSecretName` | `room-broker-ca`: a Secret in the release namespace holding `ca.crt` only, which the run controller copies into each run. Bring it: an ExternalSecret, a trust-manager Bundle targeting a Secret, or the quickstart's `up.sh`. Never `broker.tls.secretName`, which also holds the broker's `tls.key`: the render refuses it |
| `broker.factoryURL`, `broker.factoryPodLabels` | The factory's run API, `https://agent-factory.<ns>.svc:8082` once the factory chart sets `api.tls.secretName`; an `http://` URL is refused unless `broker.factoryInsecure`. Set `factoryURL`, and the broker CNP lets the factory's pods (`factoryPodLabels`, default `app.kubernetes.io/name: agent-factory`, in the release namespace unless the labels set `io.kubernetes.pod.namespace`) reach `:8443`, and the broker reach them on `factoryURL`'s port. Cilium matches after service translation, so that port must be the factory pod's: 8082 with the factory chart |
| `broker.factoryInsecure` | `true` admits an `http://` `factoryURL`: human tokens cross to the factory in the clear. Default `false` |
| `broker.factoryCA.secretName` | A Secret with `ca.crt`, the CA that signs the factory's certificate; mounted at `/etc/room-broker/factory-ca` and written as `factoryCAFile`. Empty: the system roots |

`values-quickstart.yaml` is the laptop profile: local humans, tokenreview, a namespaced `Issuer`.

```bash
helm template ap charts/maku -n maku -f charts/maku/values-quickstart.yaml \
  --set broker.image.tag=<version>@sha256:<digest> --set runController.image.tag=<version>@sha256:<digest>
```

## Caveats

| Topic | Behaviour |
|---|---|
| CRDs | `crds/` are installed once and **not upgraded by Helm**: `kubectl apply -f charts/maku/crds` on every upgrade |
| Upgrading with the run controller | Create the `broker.tls.caSecretName` Secret before upgrading: until it exists the controller starts no run and says `Secret <namespace>/room-broker-ca has no ca.crt` |
| Runs namespace | Kept on uninstall (`helm.sh/resource-policy: keep`) and keeps its Helm ownership annotations: reinstalling under another release name needs `createRunsNamespace: false` |
| `:8090` | `broker.roomTools.enabled: false` binds nothing. On in `key` mode, the port is not modelled in the chart: no Service port and no policy for it, so the `agent-router` route and its ingress are yours. In `tokenreview` mode the chart opens it to the runs |
| `oidc` mode network | Needs `broker.networkPolicy.extraEgress` (JWKS) and `extraIngress` (`:8080` from the proxy, `:9090` scraping). Default-deny leaves `:9090` unscrapable for both the broker and the controller |
| `createRuntimeClass` | Off by default (the RuntimeClass name is cluster-scoped); on in the quickstart |
| Broker runtime | The broker pod never takes a runtimeClassName (refused at render): kubectl port-forward cannot reach a runsc pod |
| `apiServerPorts` | `[443, 6443]`: the ports the egress policy opens to the apiserver |
| `AgentRun` admission | A cluster-scoped `ValidatingAdmissionPolicy` and binding, `room-broker-<release namespace>-agentruns` (Kubernetes 1.30+): an update by the broker's ServiceAccount may change the three suspend annotations of an AgentRun and nothing else |

Checks: `task chart:check`. The laptop install is the [quickstart](../../docs/quickstart.md).
