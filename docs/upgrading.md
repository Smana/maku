# Upgrading

What changes in the install steps, release by release. A release that is not listed here upgrades with
the same `helm upgrade` as before. Until v4.0.0 the chart values may still change in a 0.x minor; each
such change has a section here. From v4.0.0, a breaking value change happens only in a major version.


## The Slack App's subchart, the broker's parity keys, the factory's default-deny (v0.4x, v4 rollout step 6)

New keys, all off by default, so a release that sets none of them renders as before:

| New key | Default | What it does |
|---|---|---|
| `slack` (the `maku-slack` subchart) | `enabled: false` | the Slack App ([Slack App, Install](slack.md#install)). Its values must agree with `maku.broker.webhooks` and both `sponsored` lists (`values-slack.example.yaml`) |
| `maku.broker.webhooks`, `suspend`, `policyManagement`, `coordination` | unset | the broker's own blocks, verbatim. A webhook may name its key's Secret (`existingSecret`, `key`) and its peers (`networkPolicy.to`) |
| `networkPolicies.factory` | `enabled: false` | the factory's default-deny, in maku's mode; refused beside the demo's `networkPolicies.plain` |

**`slack.enabled: true` now installs the App.** Before this release, `slack.enabled: true` only added
the `maku-slack` alert group, for an App installed outside the chart. It now renders the App and checks
its agreements, so an install that kept its own App either moves it to the subchart, or turns
`slack.enabled` off (and loses that alert group).
## Exposure and the web front door (v0.4x, v4 rollout step 6)

New keys, all off by default, so a release that sets none of them renders as before:

| New key | Default | What it does |
|---|---|---|
| `expose.mode` | `none` | `httproute` or `ingress` renders a route per surface: `web` (to the web front door) and `slack` (`POST /slack/events`, with `slack.enabled`), each with its own hosts and parentRefs or class ([Exposure](exposure.md)) |
| `webProxy.enabled` | `false` | oauth2-proxy as `rooms-web-proxy`, its arguments derived from `maku.broker.human`, its credentials from `webProxy.existingSecret` |
| `maku.broker.webProxy` | `false` | the broker's `:8080` admits the front door's pods alone; must equal `webProxy.enabled` |

An install that runs its own oauth2-proxy and HTTPRoute (cloud-native-ref's `rooms-oauth2-proxy`)
keeps them until it turns these on, then deletes them: both would serve the same host. The chart's
proxy keeps the cookie name `_rooms`, so sessions survive the switch when the cookie secret is the
same Secret.

## The preflight (v0.4x, v4 rollout step 7)

One new key, off by default, so a release that does not set it renders as before:

| New key | Default | What it does |
|---|---|---|
| `maku.preflight.enabled` | `false` | Renders the install's preflight: a Helm test hook (`helm test <release>`), and a suspended CronJob, `<release>-preflight`, from which `kubectl create job --from=cronjob/<release>-preflight` runs it under Argo CD. It runs the run-controller image's `preflight` subcommand and checks the Kubernetes version (1.30 or later), the six maku CRDs (and `preflight.extraCRDs`), the RuntimeClass, and each Secret of [install Secrets](install-secrets.md) the values enable, with its keys. Each result is a JSON line on the Job's log, and the summary is its termination message. `preflight.strict` fails on a warning too |

The pod reads the API server only, through its own ServiceAccount. Its Role gets the Secrets by
name, and its ClusterRole gets the named CRDs, the RuntimeClass and `/version`. In `kubernetes` and
`cilium` modes, a policy limits its egress to the API server and the components' `:9090`, and the
broker and the run-controller admit it there.

## Private code (v0.4x, v4 rollout step 7)

One new optional field on the `Repository`, `spec.privateCode.acknowledged`. A Repository serving private code needs `spec.privateCode.acknowledged: true`. That field states the code is sent to the configured models, which are usually SaaS.
This release warns and serves; the next one refuses
([private code](factory.md#private-code)).

1. Apply the CRD chart first: the new field is in `agent-factory-crds`.
2. After the upgrade, list the repositories it warns about:

   ```bash
   kubectl get repositories -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="PrivateCode")].status}{"\n"}{end}'
   ```
3. Set `privateCode: {acknowledged: true}` on each one that reads `False`, or stop serving it.

A repository defined in the factory's config file takes the same key on its `repositories` entry.
The web UI shows the banner only when the broker has `factoryURL`, and the broker and factory images
move together: an older factory answers 404 on `GET /v1/install`, and the banner shows nothing for it.

## Secrets by name (v0.4x, v4 rollout step 5)

Every Secret an install reads is listed in [install Secrets](install-secrets.md), with its keys.
Two new keys, off by default, so a release that sets neither renders as before:

| New key | Default | What it does |
|---|---|---|
| `maku.broker.reloader` | `false` | Annotates the broker's Deployment for Stakater Reloader, so a rotated DSN Secret rolls it ([rotation](install-secrets.md#rotation)) |
| `externalSecrets` | `enabled: false` | The umbrella renders an `ExternalSecret` for each seam listed under `externalSecrets.secrets`, against `storeRef`, a store you run ([ExternalSecrets](install-secrets.md#externalsecrets)) |

The broker and the factory now check the files of the chart's mounted Secrets at start. A missing or
empty one stops the start, and the message names the seam, the Secret and its key. Before, the
component started and refused at the first use. An optional mount (the intake's token, the ZITADEL
reader, the sponsored ids, the merger App) is still refused at use, as before.

## The GitHub account link (v0.4x, v4 rollout step 5)

New keys, all off by default, so a release that sets none of them renders and runs as before:

| New key | Default | What it does |
|---|---|---|
| `maku.broker.githubLink.existingSecret` | `""` | The [GitHub account link](github-link.md)'s OAuth client (`client_id`, `client_secret`), mounted for `human.githubLink`; the broker's egress then reaches GitHub on 443 |
| `maku.broker.githubLink.webURL`, `activeWindow` | `https://github.com`, `168h` | GitHub's site (a GHES), and how long a link stays active without its human signing in |
| `maku-factory.config.api.access.links` | `""` | `broker`: the factory reads each human's GitHub link from the broker (`GET /v1/links/github`) |

To move an install off the ZITADEL reader:

1. Register `<publicURL>/link/github/callback` on the factory App, and store its client in a Secret.
2. Set `broker.githubLink.existingSecret`, add `skip-auth-route: GET=^/link/github/callback$` to
   oauth2-proxy, and set `api.access.links: broker` in the factory. Keep both `readerFile`s: the reader
   then answers only for an account with no link.
3. Upgrade the broker and the factory together: both decode their configs strictly, and the broker's
   migration adds `github_links`, `github_link_states` and `github_link_events`.
4. Each human opens `/link/github` once. Once every active human has linked, remove
   `human.access.readerFile`, `api.access.readerFile` and `zitadelReaderSecret`.

## RunLore's knowledge base for runs (v0.4x, v4 rollout step 6)

New keys, off by default, so a release that does not set it renders as before:

| New key | Default | What it does |
|---|---|---|
| `maku.broker.roomTools.knowledge.url` | `""`: no knowledge tool | the broker's `:8090` serves `kb_search` and `kb_get` to the listed roles (`triager`) and forwards them to this MCP endpoint ([RunLore](integration.md#runlore)) |
| `maku.broker.roomTools.knowledge.networkPolicy.to` | `[]` | the peers the broker's egress opens on the URL's port; required with `url` in `networkPolicies.mode` `cilium` or `kubernetes`. In cilium mode an entry is an endpoint selector or a whole peer (`toFQDNs`, `toCIDR`, …) |
| `maku.broker.roomTools.knowledge.networkPolicy.port` | the URL's port | the port the policies open, when the Service's `targetPort` differs |

The run controller now lists `room-broker__kb_search` and `room-broker__kb_get` in every room run's
`MCP_READ_ONLY_TOOLS`, whether or not the broker serves them. The broker decodes its config strictly,
so a `roomTools.knowledge` block needs this release's broker image: chart and images move together.

## Run traces (v0.4x, v4 rollout step 6)

One new key in the `maku` chart, off by default, so a release that does not set it renders as before:

| New key | Default | What it does |
|---|---|---|
| `runController.tracing.otlpEndpoint` and `runController.tracing.collector` | unset: runs export no span | each run's harness exports its span over OTLP/HTTP under its task's trace, and each run's policy opens the endpoint's port to the collector's pods ([Observability](observability.md#traces)) |

`maku-factory.config.tracing.otlpEndpoint` keeps its meaning: the factory's gRPC `host:port`.

The harness no longer hands agent-server a trace endpoint, because agent-server's spans carry
prompts and tool output ([what spans contain](observability.md#what-spans-contain)). A run template
that relied on those spans, such as cloud-native-ref's AgentRun composition, sets
`AGENT_TRACE_CONTENT=1` in the harness's environment to keep them, behind a collector that filters
span attributes. Set this way, the harness's own root span is unchanged.

The run controller decodes its config strictly, so a `tracing:` block needs this release's run
controller image: chart and images move together. Runs already started keep their pods, without
traces, until they end.

## Scrape objects, alerts and dashboards (v0.4x, v4 rollout step 6)

New umbrella keys, all off by default, so a release that sets none of them renders as before:

| New key | Default | What it does |
|---|---|---|
| `monitoring.serviceMonitor` | `enabled: false` | `ServiceMonitor`s for the broker and the factory, `PodMonitor`s for the run-controller, the egress proxy and the model-gateway proxies ([observability](observability.md)). The scraper must be admitted: `maku.networkPolicies.kubernetes.metricsFrom` in maku's mode kubernetes, `monitoring.serviceMonitor.cilium.fromEndpoints` in its mode cilium (the default), `maku-gateway.metrics.allowFrom` with the gateway's policy on |
| `monitoring.prometheusRule` | `enabled: false` | The `PrometheusRule` `maku`, a group per component ([alerts](operations.md#alerts)); with `maku-gateway.enabled`, the [gateway group](operations.md#gateway-alerts) and the cost cross-check |
| `monitoring.dashboards` | `enabled: false` | A ConfigMap per product dashboard, `maku-dashboard-<uid>`, for Grafana's sidecar or a grafana-operator `configMapRef` ([dashboards](observability.md#dashboards)). An install that runs cloud-native-ref's `GrafanaDashboard`s with the same uids removes them first, or Grafana holds two dashboards with one uid |
| `slack.enabled` | `false` | Until the Slack App's subchart ships, only the `maku-slack` alert group, for an App installed outside the chart |

Both need the prometheus-operator CRDs. An install that runs its own copies of these rules (the
`VMRule`s cloud-native-ref carries) removes them when it turns `prometheusRule` on, or both fire. Two
alerts are renamed from what this page's [factory alerts](operations.md#factory-alerts) documented:
`FactoryRepositoryNotOwned` is `AgentFactoryRepositoryNotOwned`, as cloud-native-ref named it, and
`AgentFactoryClassPausedLong` watches every class, not a fixed list.

## Runs through the model gateway (v0.4x, v4 rollout step 4)

New keys, all off by default, so a release that sets none of them renders as before:

| New key | Default | What it does |
|---|---|---|
| `maku.runController.model.mode` | `secret`: `runController.modelSecret`, as before | `gateway`: runs call the model gateway with their own token, and `modelSecret` is not read ([the model gateway](gateway.md#runs-through-the-gateway)) |
| `maku.runController.model.gateway` | `model-gateway` in the release namespace, ports 8080 and 8081, prefix `maku-gateway`, no aliases | the gateway and the aliases each listener serves |
| `values-demo-gateway.yaml` | not layered | the demo with the gateway: `demo.model.apiKey` becomes the provider's Secret `demo-model` in the release namespace, and no `agent-model` is rendered |
| `maku-factory.config.runs.gatewayIdentity` | unset | `true`: portable runs carry a gateway identity, so the meter prices them, and `loops.<class>.models` and `split` are accepted ([metering the runs](gateway.md#metering-the-runs)). The umbrella requires it with `model.mode: gateway` and a portable factory |
| `maku-factory.config.meter.throttle.promQuery` | unset | the gateway's 429s from PromQL on `meter.url`, in place of `meter.logsURL` with `meter.throttleQuery`; one of the two is required outside the demo, never both |
| `maku-gateway.modelCatalog.prices` (umbrella) | `maku-factory.config.meter.prices` | the gateway's catalog takes the factory's prices when its own are empty |

To move an install to `gateway` mode:

1. Run the gateway, with `maku-gateway` and its providers' Secrets.
2. Set `model.mode: gateway` and `model.gateway.aliases` to the gateway's aliases, and
   `maku-factory.config.runs.gatewayIdentity: true`.
3. Upgrade.
4. Delete `agent-model` from the runs namespace: nothing reads it any more.

Runs already started keep their pods and their key until they end. The run controller and the
factory decode their configs strictly, so a `model:` block, `runs.gatewayIdentity` and
`meter.throttle` need this release's images: chart and images move together. The refusal messages
of `loops.<class>.models` and `split` on a portable runtime now name `runs.gatewayIdentity`.
Under `runs.gatewayIdentity` the factory stamps each AgentRun `spec.meteredBy: gateway`: upgrade
`agent-factory-crds` first, since an older AgentRun CRD prunes the field, and a secret-mode run
controller then starts the run unmetered.

## Push enforced (v0.4x, hardening stage H3)

Opt-in per repository; without `push` in an `enforceOutputs` list nothing changes.

| Key | Chart | What it does |
|---|---|---|
| `config.hardening.repositories.<repo>.enforceOutputs: [..., push]` | `maku-factory` | The factory pushes the run's bundle as the publisher ([Hardening, push enforced](factory.md#hardening-push-enforced)) |
| `hardening.gitCache.sizeLimit` | `maku-factory` | The push applier's mirror cache. Required once a repository enforces `push`: without it the factory refuses to start |
| `config.hardening.applier.approvalTTL`, `mirrorMaxBytes` | `maku-factory` | How long a protected-path push waits (24 h, at most 168 h), and one mirror's size limit (1 GiB, at least 64 MiB) |
| `broker.roomTools.pushRepositories` | `maku` | The repositories the broker serves `output_push` to; list the same ones |

`mode: enforce` is now refused with "enforce arrives with stage H4". The factory and broker
configs are decoded strictly: upgrade the charts and the images together. The order of the steps
is in [Operations](operations.md#hardening-push-enforced).

## Two GitHub Apps (v0.4x, v4 rollout step 3)

New `maku-factory` keys name the Apps ([GitHub Apps](github-apps.md)). Each maps onto an old
key, which keeps working, and with none of them set the render is unchanged.

| New key | Old key | Rule |
|---|---|---|
| `apps.factory.existingSecret` | `githubSecret` | The new key wins when set |
| `apps.agents.existingSecret` | `runTokens.appSecret` | The new key wins; both set and different fail the render. Mounted at `/etc/agent-factory-agents`, read through `config.github.agentsAppIDFile` and `agentsKeyFile` |
| `apps.merger.mode: factory` | `mergerSecret` | The merger signs with the factory App: no merger volume, and every `merge.mergerLogin` must be `factoryLogin`. `mergerSecret` set beside it fails the render |

`mergerSecret` now defaults to `""`, which means `agent-factory-merger` as before. The demo's
`values-demo.yaml` sets `apps.merger.mode: factory` in place of its explicit merger files: its
factory config is unchanged, and its pod no longer mounts the absent `agent-factory-merger` Secret.
The config also gains `github.agentsAppIDFile` and `agentsKeyFile`; it is decoded strictly, so
upgrade the chart and the image together.

## Per-run tokens (v0.4x, v4 rollout step 3)

Two opt-in keys, which go together; without them nothing changes (`gitTokens: shared` stays the
default, and `runTokens.enabled` alone keeps the two shared Secrets).

| Key | Chart | What it does |
|---|---|---|
| `runTokens.perRun: true` | `maku-factory` | The factory mints each run's own token, `<run>-git`, from the App in `runTokens.appSecret` (the demo: its own App). Its runs-namespace Role becomes `create`, `patch`, `delete` on Secrets with no `get`, and the admission policy that bounds it renders with it. Needs `config.runs.group: maku.ogenki.io` and, outside the demo, `agentsLogin` different from `factoryLogin` |
| `runController.gitTokens: perRun` | `maku` | Each run mounts its own Secret; `gitSecret`, `gitReadSecret` and `unsafeSharedGitToken` are not read (setting `unsafeSharedGitToken` fails the render) |

Set `runController.gitReadSecret: ""` with `perRun` (the chart refuses both). The umbrella fails a
render that sets one key without the other, or `perRun` without
`maku.agentRunPolicies.creators` naming the release's factory. `values-demo.yaml` sets all three
(its creators name `system:serviceaccount:maku:agent-factory`: install the demo in another
namespace with your own). The App needs every permission of the
[role table](factory.md#run-tokens-the-role-table). The factory and run-controller configs are
decoded strictly: upgrade the chart and the images together.

With the standalone `maku` and `maku-factory` charts nothing pairs the two keys, and the factory
cannot read the run-controller's mode: set both, or an `enforceOutputs` repository the factory
admits (HR13) would run on the shared write token. API runs reserved before the upgrade carry no
repository in their reservation and get no per-run token: let them end before enabling `perRun`.

**The publisher from the minter.** `config.hardening.publisher.minter: true` (opt-in, needs
`runTokens.enabled`) mints the safe-output publisher's token from the agents App instead of
exchanging it at octo-sts; it is refused together with `hardening.publisher.octoSTS.url`. Without
it nothing changes. Readiness and the permission gauge now read the token source the runs get: the
octo-sts policy files on the XR as before, the role table under `runTokens.perRun`. Moving an
enforced repository off the XR is a runbook in
[Operations](operations.md#leaving-the-xr-with-an-enforced-repository-hr13).

## The run path (v0.4x, v4 rollout step 2)

New keys in the `maku` chart, each rendered only with `runController.enabled`, so an install without
the run controller (cloud-native-ref's AgentRun composition) renders as before. Every older key keeps
its meaning until rollout step 8 deletes it.

| New key | Default | Old key, still honoured |
|---|---|---|
| `sandbox.runtimeClassName` | `""`: the old key | `runController.runtimeClassName` (`gvisor`) |
| `sandbox.unsafeAllowRunc` | `false` | `runController.allowUnsandboxed` |
| `networkPolicies.mode` (`cilium`, `kubernetes`, `none`) and `networkPolicies.kubernetes` | `""`: the old key | `networkPolicies.enabled` (`true` is `cilium`, `false` is `none`) |
| `egressProxy` | 2 replicas, rendered only in mode `kubernetes` | None |
| `broker.database.podLabels` | `{}`: `podLabel`'s key | None |
| `runController.gitReadSecret`, `runController.unsafeSharedGitToken` | `agent-git-read`, `false` | None: a reviewer or triager run needs one of them |
| `runsQuota`, `runPodPolicy` | On | None |
| `agentRunPolicies` | Off until set | None |

Where an old and a new key conflict (`sandbox.unsafeAllowRunc` with `sandbox.runtimeClassName`,
`networkPolicies.mode: cilium` with `networkPolicies.enabled: false`), the render fails naming both.
The run controller decodes its config strictly, so its image and the chart move together; the
egress proxy is the same image.

## The chart contract (v0.4x, v4 rollout step 1)

This release changes three things in `charts/agent-factory`.

| Change | What you do |
|---|---|
| The CRDs ship in their own chart, `agent-factory-crds`, as templates every tool upgrades, each kept on uninstall (`helm.sh/resource-policy: keep`) | Install it first, release `af-crds`. On a cluster that already has the CRDs, adopt them first (below) |
| The demo's defaults moved from `values.yaml` to `values-demo.yaml`; `values.yaml` is the production-safe skeleton | Add `-f charts/agent-factory/values-demo.yaml` before your own values files. `--set profile=demo` alone now fails with `profile demo needs -f values-demo.yaml` |
| `values.schema.json` rejects unknown top-level keys and malformed values, and every chart requires Kubernetes 1.30 or later (`kubeVersion: ">=1.30.0-0"`) | Fix the key or value the error names. Use Helm 3.15 or later: Helm 3.14 assumes Kubernetes 1.29 when it renders offline, so its `helm template` needs `--kube-version 1.30.0` or later |

`maku` and `maku-factory`, used on their own, change only by their `kubeVersion`. They keep their
`crds/`, so a Flux HelmRelease with `crds: CreateReplace` keeps working unchanged.

### Two charts, one set of CRDs

The `maku` and `maku-factory` subcharts keep their `crds/` until rollout step 8, so the umbrella still
carries the same six CRDs. Once `agent-factory-crds` is installed, it alone manages them: tell your
GitOps tool to skip the umbrella's copies.

| Tool | On the umbrella's release |
|---|---|
| Argo CD | `spec.source.helm.skipCrds: true` |
| Flux | `spec.install.crds: Skip` and `spec.upgrade.crds: Skip` |
| Helm | nothing: `helm upgrade` never touches `crds/`, and `helm install` skips a CRD that exists |

### Adopting the CRDs

On a cluster where the CRDs came from the `maku` and `maku-factory` subcharts' `crds/` (any install from
before this release), Helm refuses `agent-factory-crds`: each CRD "exists and cannot be imported into the
current release". Hand the CRDs to the release you are about to install (here `af-crds` in the
namespace `maku`) with three commands:

```bash
crds=$(kubectl get crd -o name | grep '\.maku\.ogenki\.io$')
kubectl label $crds app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate $crds meta.helm.sh/release-name=af-crds meta.helm.sh/release-namespace=maku --overwrite
```

The two annotations must name the release exactly as the tool that installs the chart names it. With
Flux, `meta.helm.sh/release-name` is the HelmRelease's computed name: `<targetNamespace>-<name>` when
`spec.targetNamespace` is set, `<name>` otherwise, unless `spec.releaseName` sets it.
`meta.helm.sh/release-namespace` is the namespace the release is installed in (`spec.targetNamespace`,
else the HelmRelease's own). A name that differs gives the same "cannot be imported" refusal.

Then install the CRD chart and upgrade the umbrella:

```bash
helm install af-crds charts/agent-factory-crds -n maku
helm upgrade af charts/agent-factory -n maku -f charts/agent-factory/values-demo.yaml -f values-mine.yaml
```

Existing Rooms and Tasks are untouched: the CRDs are relabelled, never deleted. The chart's install
test proves it (`ADOPT=1 bash charts/agent-factory/tests/install.sh k3d`): it creates a Room before
the adoption and checks the same Room, by uid, after it. `task demo` runs these commands itself on a
demo cluster from before this release.
