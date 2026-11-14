# maku chart

The room broker, the run controller (`runController`), and the runs namespace with its default-deny
policy. It has no factory: install [maku-factory](../maku-factory/README.md) on its own. It ships no
CRD: install [agent-factory-crds](../agent-factory-crds/README.md) first, which carries the `Room` and
`AgentRun` CRDs (rollout step 8 removed this chart's `crds/`).

## Values that matter

| Value | Meaning |
|---|---|
| `runs.{group,namespace,namePrefix}` | The only source of the run kind: the broker's `runs:` and its run-issuer `subPattern` both render from it |
| `runController.enabled` | On by default: the run controller runs the AgentRuns. `runs.group` must be `maku.ogenki.io`, the only group it reconciles; cloud-native-ref's `cloud.ogenki.io` composition was removed in v4 step 8 ([upgrading](../../docs/upgrading.md#the-xr-run-kind-and-octo-sts)) |
| `broker.image.tag` | `<version>@sha256:<digest>`; a bare tag is refused unless `e2e.unpinnedImages` |
| `broker.publicURL` | `oidc`: the https URL humans use. `local`: `http://localhost:<forwarded port>` (default `:8080`); the broker answers that port only, under `localhost`, `127.0.0.1` or `[::1]` |
| `broker.human.mode` | `oidc` (block passed through verbatim) or `local` (one loopback user; `:8080` is not exposed, use a port-forward) |
| `broker.runIssuer.mode` | `jwks` (`issuer`, `jwksURL`) or `tokenreview` (binds `system:auth-delegator`) |
| `broker.roomTools.enabled` | The agents' `:8090` room tools listener. On by default: the block absent, or `enabled` unset, means on. An explicit `false` (the quickstart's laptop profile) renders `roomTools.enabled: false`, and the broker binds no `:8090` at all. On, `ROOMS_MCP_KEY` alone decides whether a call is accepted, so supply it through `broker.extraEnv`; unset, every call is refused `401` |
| `broker.roomTools.auth` | `key` (unset: the gateway's `ROOMS_MCP_KEY` and verified subject, as above) or `tokenreview`: the broker verifies each run's own token (audience `room-tools`) with the apiserver. `tokenreview` needs `broker.runIssuer.mode: tokenreview` and reuses its `system:auth-delegator` binding; it adds the Service port `8090`, the broker CNP's ingress from the runs, and the run controller's `roomToolsURL` (`http://room-broker.<ns>.svc.<clusterDomain>:8090/mcp`, plain HTTP in the cluster), which gives each run with a room `MCP_URL` and the token, and its bridge `MCP_BARE_SERVER=room-broker` and `MCP_READ_ONLY_TOOLS` (the five room tools), so the bare tool names the harness sends pass the unattended policy |
| `broker.database.brokerSecret` / `ownerSecret` / `retentionSecret` | One DSN Secret per database role ([the database](../../docs/database.md)), each holding its DSN under `secretKey` (default `uri`). An `ownerSecret` (the `rooms_owner` DSN) adds the `migrate` init container, which reads it as `ROOMS_MIGRATION_DATABASE_URL`; the broker reads `brokerSecret` as `ROOMS_DATABASE_URL`. An empty `brokerSecret` omits `ROOMS_DATABASE_URL`: supply it through `broker.extraEnv`. A `retentionSecret` (the `rooms_retention` DSN) renders the CronJob `room-broker-retention` on `broker.retention.schedule` (`17 3 * * *`): the broker image with `retention`, no ServiceAccount token, a 900 s deadline, and its own network policy (no ingress; DNS and the database) |
| `broker.database.caSecret` | A Secret with `ca.crt`, the database's CA, mounted at `/etc/room-broker/db-ca/ca.crt` in the `migrate` init container, the broker and the retention job, for `sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt` |
| `broker.database.egress.cidrs` | A database outside the cluster: the broker's and the retention job's egress on 5432 go to these CIDRs (`toCIDR` in `cilium` mode, `ipBlock` in `kubernetes` mode) instead of `podLabel` or `podLabels` |
| `broker.tls.issuerRef.name` | Renders a cert-manager `Certificate` for `broker.tls.secretName`; empty = bring the Secret yourself |
| `broker.tls.caSecretName` | `room-broker-ca`: a Secret in the release namespace holding `ca.crt` only, which the run controller copies into each run. Bring it: an ExternalSecret, a trust-manager Bundle targeting a Secret, or the quickstart's `up.sh`. Never `broker.tls.secretName`, which also holds the broker's `tls.key`: the render refuses it |
| `broker.factoryURL`, `broker.factoryPodLabels` | The factory's run API, `https://agent-factory.<ns>.svc:8082` once the factory chart sets `api.tls.secretName`; an `http://` URL is refused unless `broker.factoryInsecure`. Set `factoryURL`, and the broker CNP lets the factory's pods (`factoryPodLabels`, default `app.kubernetes.io/name: agent-factory`, in the release namespace unless the labels set `io.kubernetes.pod.namespace`) reach `:8443`, and the broker reach them on `factoryURL`'s port. Cilium matches after service translation, so that port must be the factory pod's: 8082 with the factory chart |
| `broker.factoryInsecure` | `true` admits an `http://` `factoryURL`: human tokens cross to the factory in the clear. Default `false` |
| `preflight.enabled`, `preflight.strict`, `preflight.timeout`, `preflight.extraCRDs` | The install's preflight (v4 D24), on by default since rollout step 8: a Helm test hook and a suspended CronJob `<release>-preflight`, running the run-controller image's `preflight`. It checks the Kubernetes version, the CRDs, the RuntimeClass and the Secrets of [install Secrets](../../docs/install-secrets.md) the values enable. See [upgrading](../../docs/upgrading.md#the-preflight-v04x-v4-rollout-step-7) |
| `broker.oidc.existingSecret`, `broker.oidc.keys` | oidc mode: a Secret holding the OIDC client ids (keys `web-client-id`, `cli-client-id` and, under the zitadel preset, `project-id`), mounted at `/etc/room-broker/oidc`. It fills `human.clientIDFile`, `human.roomctlClientIDFile` and `human.projectIDFile` where the human block leaves them unset; a set path wins. See [identity](../../docs/identity.md) |
| `broker.factoryCA.secretName` | A Secret with `ca.crt`, the CA that signs the factory's certificate; mounted at `/etc/room-broker/factory-ca` and written as `factoryCAFile`. Empty: the system roots |
| `broker.modelGateway` | `{service, namespace, port, audiencePrefix}`: the model gateway's clients listener ([gateway](../../docs/gateway.md#the-planner-and-the-classifier)). Empty `service` (the default) changes nothing. Set, the broker mounts one projected token per client at `/var/run/secrets/maku/model-gateway/{planner,classifier}` (3600 s, read at use), its policy may reach the gateway's pods on `port` (8082), and `human.intake.plan` (when enabled) and an existing `human.slackIntents` get the gateway's `url` and the token as `apiKeyFile` when they set neither key. A block that sets both keeps them; a block that sets only one fails the render |
| `networkPolicies.mode` | `kubernetes` (the default), `cilium` or `none`. `cilium` renders the CiliumNetworkPolicies. `kubernetes` renders plain `networking.k8s.io/v1` NetworkPolicies on any CNI that enforces them: default-deny in the runs namespace, the broker's, the run controller's and the egress proxy's, and the run controller creates one per run and sends each harness's HTTPS through the [egress proxy](../../docs/security.md#the-run-paths-network-modes). Its preflight refuses runs until a probe pod proves the CNI denies a denied connection. `none` confines nothing and shows on the install banner. The old `networkPolicies.enabled` is removed ([upgrading](../../docs/upgrading.md#the-run-paths-old-keys)), and `kubernetes` needs `runController.enabled` and `runPodPolicy.enabled` (the proxy's trust root) |
| `networkPolicies.kubernetes` | `dns` (`{namespace, labels}`, default `kube-system`, `k8s-app: kube-dns`), opened on 53 to every component; `apiServerCIDRs`, the apiserver's addresses on `apiServerPorts` (empty: any address on those ports, a [residual risk](../../docs/security.md#the-run-paths-network-modes): set it, and give the broker's other 443 flows `extraEgress`); `privateCIDRs`, which the egress proxy never dials and its NetworkPolicy never opens (default RFC 1918, CGNAT and link-local); `metricsFrom`, NetworkPolicy peers allowed to scrape `:9090` (empty: none) |
| `networkPolicies.runsNamespaceDefaultDeny` | `true` (the default) renders the runs namespace's default-deny (`default-deny` in `runs.namespace`, a CiliumNetworkPolicy in mode `cilium`, a NetworkPolicy in mode `kubernetes`). `false` renders none, for an install whose platform already owns that policy (the two would conflict); the platform's must then deny both directions, as the chart's does |
| `egressProxy` | `replicas` (2, with a PodDisruptionBudget of `minAvailable: 1`), `resources`, `extraPorts`, ports besides 443 the proxy may dial (a model served on another port), and `maxTunnels` (1024) and `maxTunnelsPerClient` (64), the open tunnels per replica in all and from one run pod (over either: 429). The proxy is `runController.image` run with `egress-proxy`, in the release namespace |
| `broker.database.podLabels` | In `kubernetes` mode only: exact labels that select the PostgreSQL pods for the broker's and the retention job's 5432 rule, instead of `podLabel`'s key (the demo's `app.kubernetes.io/name: rooms-db`) |
| `runController.gitSecret` | `""`: each run mounts its own `<run>-git`, which maku-factory's `runTokens.perRun` mints (v4 D10); the Role gets Secrets by `get` without `resourceNames` ([docs/factory.md](../../docs/factory.md#run-tokens-the-role-table)). Set, demo-only (`demoValues`): a laptop's one PAT, which every role mounts, so a reviewer or triager could push; shown on the install banner. The older `gitTokens`, `gitReadSecret` and `unsafeSharedGitToken` are removed (v4 step 8): the render fails naming this key |
| `demoValues` | `false`. The demo marker: `true` admits `runController.model.mode: secret` and `runController.gitSecret`. `values-quickstart.yaml` sets it |
| `runController.model.mode` | `secret` (demo-only, needs `demoValues`; the banner shows `model_key_in_sandbox`): the harness calls `modelSecret`'s provider with its key. `gateway` (default): it calls the model gateway with the run's own projected token (audience `<audiencePrefix>.<dataClass>.<spec.model>`), and no provider key enters the runs namespace. `model.gateway` names the Service (default `model-gateway` in the release namespace), its `ports`, its `audiencePrefix` and the `aliases` each listener serves; `aliases.public` is required. See [the model gateway](../../docs/gateway.md#runs-through-the-gateway) |
| `sandbox.runtimeClassName`, `sandbox.unsafeAllowRunc` | The run pod's sandbox (D18), below |
| `runsQuota` | A ResourceQuota on `runs.namespace` (pods, CPU, memory), on by default. A run over it waits Pending, `QuotaExceeded` |
| `runPodPolicy` | The run pod's ValidatingAdmissionPolicy, on by default; `extraImages` adds image repositories ([the run pod's admission](../../docs/security.md#the-run-pods-admission)) |
| `agentRunPolicies` | `creators` and `factoryServiceAccount`: two optional AgentRun policies, each rendered only when set; `namespaces` narrows both |

### The sandbox keys

| Key | Rule |
|---|---|
| `sandbox.runtimeClassName` (default `gvisor`) | The RuntimeClass every run pod runs under. Empty, with `unsafeAllowRunc` false: the controller refuses every run |
| `sandbox.unsafeAllowRunc` | `true`, with `sandbox.runtimeClassName: ""`: no RuntimeClass, the run controller's `allowUnsandboxed: true`. With a `runtimeClassName` it fails the render |
| `runController.createRuntimeClass`, `runtimeHandler` | Render the class `sandbox.runtimeClassName` names |

The old `runController.runtimeClassName` and `runController.allowUnsandboxed` are removed
([upgrading](../../docs/upgrading.md#the-run-paths-old-keys)).

`values-quickstart.yaml` is the laptop profile: local humans, tokenreview, a namespaced `Issuer`.

```bash
helm template ap charts/maku -n maku -f charts/maku/values-quickstart.yaml \
  --set broker.image.tag=<version>@sha256:<digest> --set runController.image.tag=<version>@sha256:<digest>
```

## Caveats

| Topic | Behaviour |
|---|---|
| CRDs | None here: `agent-factory-crds` installs and upgrades them, and keeps them on uninstall |
| Upgrading with the run controller | Create the `broker.tls.caSecretName` Secret before upgrading: until it exists the controller starts no run and says `Secret <namespace>/room-broker-ca has no ca.crt` |
| Runs namespace | Kept on uninstall (`helm.sh/resource-policy: keep`) and keeps its Helm ownership annotations: reinstalling under another release name needs `createRunsNamespace: false` |
| `:8090` | `broker.roomTools.enabled: false` binds nothing. On in `key` mode, the port is not modelled in the chart: no Service port and no policy for it, so the `agent-router` route and its ingress are yours. In `tokenreview` mode the chart opens it to the runs |
| `oidc` mode network | Needs `broker.networkPolicy.extraEgress` (JWKS) and `extraIngress` (`:8080` from the proxy, `:9090` scraping). Default-deny leaves `:9090` unscrapable for both the broker and the controller |
| `createRuntimeClass` | Off by default (the RuntimeClass name is cluster-scoped); on in the quickstart |
| Broker runtime | The broker pod never takes a runtimeClassName (refused at render): kubectl port-forward cannot reach a runsc pod |
| `apiServerPorts` | `[443, 6443]`: the ports the egress policy opens to the apiserver |
| `AgentRun` admission | A cluster-scoped `ValidatingAdmissionPolicy` and binding, `room-broker-<release namespace>-agentruns` (Kubernetes 1.30+): an update by the broker's ServiceAccount may change the three suspend annotations of an AgentRun and nothing else |

Checks: `task chart:check`. The laptop install is the [quickstart](../../docs/quickstart.md).
