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
| `broker.database.brokerSecret` / `ownerSecret` | Secrets with key `uri`. An `ownerSecret` (the `rooms_owner` DSN) adds the `migrate` init container, which reads it as `ROOMS_MIGRATION_DATABASE_URL`; the broker reads `brokerSecret` as `ROOMS_DATABASE_URL`. An empty `brokerSecret` omits `ROOMS_DATABASE_URL`: supply it through `broker.extraEnv` |
| `broker.tls.issuerRef.name` | Renders a cert-manager `Certificate` for `broker.tls.secretName`; empty = bring the Secret yourself |
| `broker.tls.caSecretName` | `room-broker-ca`: a Secret in the release namespace holding `ca.crt` only, which the run controller copies into each run. Bring it: an ExternalSecret, a trust-manager Bundle targeting a Secret, or the quickstart's `up.sh`. Never `broker.tls.secretName`, which also holds the broker's `tls.key`: the render refuses it |
| `broker.factoryURL`, `broker.factoryPodLabels` | The factory's run API, `https://agent-factory.<ns>.svc:8082` once the factory chart sets `api.tls.secretName`; an `http://` URL is refused unless `broker.factoryInsecure`. Set `factoryURL`, and the broker CNP lets the factory's pods (`factoryPodLabels`, default `app.kubernetes.io/name: agent-factory`, in the release namespace unless the labels set `io.kubernetes.pod.namespace`) reach `:8443`, and the broker reach them on `factoryURL`'s port. Cilium matches after service translation, so that port must be the factory pod's: 8082 with the factory chart |
| `broker.factoryInsecure` | `true` admits an `http://` `factoryURL`: human tokens cross to the factory in the clear. Default `false` |
| `broker.factoryCA.secretName` | A Secret with `ca.crt`, the CA that signs the factory's certificate; mounted at `/etc/room-broker/factory-ca` and written as `factoryCAFile`. Empty: the system roots |
| `networkPolicies.mode` | `cilium`, `kubernetes` or `none`; empty (the default) keeps the old key's meaning, `networkPolicies.enabled` `true` for `cilium` and `false` for `none`. `cilium` renders the CiliumNetworkPolicies, as before. `kubernetes` renders plain `networking.k8s.io/v1` NetworkPolicies on any CNI that enforces them: default-deny in the runs namespace, the broker's, the run controller's and the egress proxy's, and the run controller creates one per run and sends each harness's HTTPS through the [egress proxy](../../docs/security.md#the-run-paths-network-modes). Its preflight refuses runs until a probe pod proves the CNI denies a denied connection. `none` confines nothing and shows on the install banner. `cilium` with `enabled: false` fails the render, and `kubernetes` needs `runController.enabled` and `runPodPolicy.enabled` (the proxy's trust root) |
| `networkPolicies.kubernetes` | `dns` (`{namespace, labels}`, default `kube-system`, `k8s-app: kube-dns`), opened on 53 to every component; `apiServerCIDRs`, the apiserver's addresses on `apiServerPorts` (empty: any address on those ports, a [residual risk](../../docs/security.md#the-run-paths-network-modes): set it, and give the broker's other 443 flows `extraEgress`); `privateCIDRs`, which the egress proxy never dials and its NetworkPolicy never opens (default RFC 1918, CGNAT and link-local); `metricsFrom`, NetworkPolicy peers allowed to scrape `:9090` (empty: none) |
| `egressProxy` | `replicas` (2, with a PodDisruptionBudget of `minAvailable: 1`), `resources`, `extraPorts`, ports besides 443 the proxy may dial (a model served on another port), and `maxTunnels` (1024) and `maxTunnelsPerClient` (64), the open tunnels per replica in all and from one run pod (over either: 429). The proxy is `runController.image` run with `egress-proxy`, in the release namespace |
| `broker.database.podLabels` | In `kubernetes` mode only: exact labels that select the PostgreSQL pods for the broker's 5432 rule, instead of `podLabel`'s key (the demo's `app.kubernetes.io/name: rooms-db`) |
| `runController.gitSecret` / `gitReadSecret` | Implementer and tester runs mount `gitSecret`; reviewer and triager runs mount `gitReadSecret` (default `agent-git-read`), a read-only token. The controller refuses those roles without it |
| `runController.unsafeSharedGitToken` | `true`: every role mounts `gitSecret`, so a reviewer or triager could push. For a laptop's one PAT only; shown on the install banner |
| `sandbox.runtimeClassName`, `sandbox.unsafeAllowRunc` | The run pod's sandbox (D18), mapped onto the run controller's older keys below |
| `runsQuota` | A ResourceQuota on `runs.namespace` (pods, CPU, memory), on by default. A run over it waits Pending, `QuotaExceeded` |
| `runPodPolicy` | The run pod's ValidatingAdmissionPolicy, on by default; `extraImages` adds image repositories ([the run pod's admission](../../docs/security.md#the-run-pods-admission)) |
| `agentRunPolicies` | `creators` and `factoryServiceAccount`: two optional AgentRun policies, each rendered only when set; `namespaces` narrows both |

### The sandbox keys

| Key | Old key, still honoured | Rule |
|---|---|---|
| `sandbox.runtimeClassName` (default `""`) | `runController.runtimeClassName` (default `gvisor`) | The new key wins when set; else the old one |
| `sandbox.unsafeAllowRunc` | `runController.allowUnsandboxed` | `true`: no RuntimeClass, `allowUnsandboxed: true`. With `sandbox.runtimeClassName` it fails the render |
| (none) | `runController.createRuntimeClass`, `runtimeHandler` | Unchanged; renders the class under the resolved name |

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
