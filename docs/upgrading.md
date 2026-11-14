# Upgrading

What changes in the install steps, release by release. A release that is not listed here upgrades with
the same `helm upgrade` as before. Until v4.0.0 the chart values may still change in a 0.x minor; each
such change has a section here. From v4.0.0, a breaking value change happens only in a major version.

## The shared ClusterRoles in the CRD chart (v0.4x, v4 rollout step 7)

Several installs can share a cluster (D2, R10). The roles every install shares move from the factory
chart to `agent-factory-crds`, under the same names, so whatever binds them keeps working:
`maku-factory-admin`, `maku-mergepolicy-editor` and `maku-viewer` (aggregated into `admin`, `edit` and
`view`) and `agent-factory-flux-reader`. Each is kept on uninstall, as the CRDs are. The umbrella
renders none of them (`maku-factory.clusterRoles: false`), and the factory's Flux binding is now the
install's, `agent-factory-<namespace>-flux-reader`.

| New key | Default | What it does |
|---|---|---|
| `agent-factory-crds`: `clusterRoles` | `true` | renders the four shared ClusterRoles |
| `maku-factory.clusterRoles` | `true` in the factory chart, `false` under the umbrella | renders them from the factory chart, for an install of that chart alone |

**An umbrella install hands the roles to `agent-factory-crds` before upgrading,** as it did the CRDs
([adopting the CRDs](#adopting-the-crds)). Without it, Helm refuses the CRD chart's upgrade, since the
roles belong to the umbrella's release. `helm.sh/resource-policy: keep` then stops the umbrella's
upgrade from deleting what it no longer renders:

```bash
roles="maku-factory-admin maku-mergepolicy-editor maku-viewer agent-factory-flux-reader"
kubectl get clusterrole $roles --ignore-not-found -o name | xargs -r -I{} sh -c '
  kubectl label {} app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate {} meta.helm.sh/release-name=af-crds meta.helm.sh/release-namespace=maku helm.sh/resource-policy=keep --overwrite'
helm upgrade af-crds oci://ghcr.io/smana/charts/agent-factory-crds --version <version> -n maku
helm upgrade af oci://ghcr.io/smana/charts/agent-factory --version <version> -n maku -f values.yaml
```

Use your CRD release's name and namespace. Each role keeps its UID, and `task chart:adopt-roles:k3d`
proves the sequence from the previous chart. `task demo` runs it for you.

**An install of the `maku-factory` chart alone changes nothing:** `clusterRoles` stays `true` there.
When it moves under the umbrella ([adopting the umbrella](#adopting-the-umbrella-from-maku-factory)),
run the same commands first: they hand the roles from the factory's release to the CRD chart's.

## The demo's model gateway (v0.4x, v4 rollout step 7)

`values-demo.yaml` now runs the demo through the model gateway (step 7 R4), as production does:
`maku.runController.model.mode: gateway`, `agentgateway.enabled` and `maku-gateway.enabled`, the
runs' `gatewayIdentity`. `values-demo-gateway.yaml`, which layered these before, is deleted. A demo
install now:

- applies Gateway API's CRDs (v1.2 or later) and agentgateway's (`kubectl apply --server-side -f
  config/agentgateway`) before the umbrella;
- sets `maku-gateway.identity.issuer` (the cluster's ServiceAccount issuer), the provider's host
  (`maku-gateway.providers.demo.host`, with `port`, `path` and `tls`) and each alias's `model`, and
  only `demo.model.apiKey`: `demo.model.baseURL`, `name` and `judgeName` are refused in gateway mode;
- on k3s (k3d), starts the apiserver with `--kube-apiserver-arg=anonymous-auth=true`: the gateway's
  controller reads its JWKS anonymously. kind and kubeadm leave it on.

`task demo` does all three, and `values-mine.example.yaml` shows the values. agentgateway's controller
and the gateway's proxy each request 128 MiB (limits 512 MiB and 1 GiB) on the laptop. To keep the provider key in the sandbox instead, set
`maku.runController.model.mode: secret` with `maku-gateway.enabled: false`, `agentgateway.enabled: false`
and `maku-factory.config.runs.gatewayIdentity: false`: still demo-only (`maku.demoValues`).

`maku install --print` writes a production install's files ([maku](cli.md)): values, Secrets, the
factory objects, the merge gate's ruleset and the GitOps files, and it applies nothing.

## From the demo to production

From v0.4x (v4 rollout step 6), `profile: production` renders. Start from
[`values-production.example.yaml`](../charts/agent-factory/values-production.example.yaml), never from
`values-demo.yaml`: the render refuses the demo's own values. It checks the seams of each enabled
component and fails once, with every missing seam on its own line, its key and its doc:

```text
Error: execution error at (agent-factory/templates/gate/renders/before/subcharts.yaml:5:4): profile production: 9 missing or refused seams (docs/upgrading.md#from-the-demo-to-production):
  - maku.broker.image.tag: <version>@sha256:<digest>, never a bare tag (got ""; docs/security.md#supply-chain)
  - maku.broker.human.issuer: the OIDC issuer humans sign in at (docs/identity.md#the-keys)
  ...
```

| Component | Its seams | Refused |
|---|---|---|
| The release | none | `demoValues`, `postgres.enabled` (D9), `networkPolicies.plain` |
| `maku` | the four images by digest; `broker.human.mode: oidc` with an `issuer`; `broker.runIssuer.issuer`, or `runIssuer.mode: tokenreview`; `broker.database.brokerSecret` ([The database](database.md)); an https `broker.publicURL`; `sandbox.runtimeClassName`; with `runController.gitTokens: perRun`, `maku-factory.runTokens.perRun` | `maku.demoValues`, `runController.model.mode: secret` (D7), `sandbox.unsafeAllowRunc` and `runController.allowUnsandboxed` (D18: the demo and kind only) |
| `maku-factory` | its image by digest; the factory App's Secret (`apps.factory.existingSecret`, or `githubSecret`); with `maku`, `config.api.humanIssuer`, which must agree with the broker's identity ([Identity](identity.md)) | `demo`, `config.demo` |
| `slack` | the agreements of [`values-slack.example.yaml`](../charts/agent-factory/values-slack.example.yaml) and its three Secrets by name | |
| `webProxy` | `existingSecret`, and the broker's identity it derives its arguments from ([Exposure](exposure.md)) | |
| `expose` | each surface's hosts, and its parentRefs or IngressClass; the gateway's pods in the release's policy mode | |

`maku.networkPolicies.mode: none` and `maku.broker.factoryInsecure` stay allowed in production: the
web UI shows them on its unsafe-settings banner. The chart renders none of the demo's own objects: no
Postgres, no generated TLS (bring `room-broker-tls` and `room-broker-ca`, or set
`maku.broker.tls.issuerRef` for cert-manager, and the factory's `openbao-ca`), no `agent-model` or
`agent-factory-github` Secret ([Install Secrets](install-secrets.md)).

`task chart:production:k3d` installs this path on a throwaway k3d cluster: a test-only RuntimeClass
`gvisor` on runc, a Postgres outside the chart, the generic OIDC stub as the issuer, the web through
agentgateway, and the monitoring objects. A cluster that runs no sandbox runtime has no production
render: `sandbox.unsafeAllowRunc` is for the demo and kind only.

### Adopting the umbrella from `maku-factory`

An install that runs the `maku-factory` chart alone adopts the umbrella one component at a time (D25).
`maku.enabled: false` with `maku-factory.enabled: true` is a valid production render, so the factory
moves first, under the same release name:

1. Install [`agent-factory-crds`](../charts/agent-factory-crds/README.md) and adopt the CRDs
   ([Adopting the CRDs](#adopting-the-crds)).
2. Nest your `maku-factory` values under `maku-factory:` (Flux's `valuesFrom` cannot prefix a whole
   file: re-nest the file itself), and upgrade the release to the umbrella:

   ```bash
   helm upgrade agent-factory oci://ghcr.io/smana/charts/agent-factory --version <version> -n agent-system \
     --set profile=production --set maku-factory.enabled=true -f factory-values.yaml
   ```

   The factory's Deployment, Service and ConfigMap keep their names, so Helm upgrades them in place:
   the Deployment keeps its UID, and the Tasks are untouched. `helm history` shows an upgrade, not an
   install.
3. Then turn `maku.enabled` on with the broker's values, and the rest (`expose`, `webProxy`, `slack`,
   `monitoring`) each in its own upgrade.

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
`slack.enabled` off, and loses that alert group and the `expose.slack` route with it.

## The runs namespace's default-deny, optional (v0.4x)

`networkPolicies.runsNamespaceDefaultDeny` (default `true`: the render is unchanged) controls the
runs namespace's `default-deny` policy, in mode `cilium` or `kubernetes`. An install whose platform
owns the runs namespace's default-deny sets it to `false`, and the chart renders none, so the two no
longer conflict. The platform's policy must then deny both directions, as the chart's does: each run's
own policy adds only its allowances on top of it.

## The clean-up (v0.4x, v4 rollout step 8)

Step 8 deletes the paths steps 1 to 7 kept for older installs and flips the defaults they left at
their old values. Each change below is breaking: a removed or flipped value is refused by the schema
or by a render `fail` that names its replacement, never silently ignored.

### The run path's old keys

The `maku` chart's keys that step 2 kept beside their replacements are removed, and the defaults are
the product path's: plain NetworkPolicies and gVisor. The schema refuses each old key with a message
naming its replacement; under `--skip-schema-validation` the template does.

| Removed | Set instead | Default now |
|---|---|---|
| `networkPolicies.enabled` | `networkPolicies.mode`: `cilium` for `true`, `none` for `false` | `kubernetes` (was `cilium`, from `enabled: true`) |
| `runController.runtimeClassName` | `sandbox.runtimeClassName` | `gvisor` (was `""`, falling back to the old key's `gvisor`) |
| `runController.allowUnsandboxed` | `sandbox.unsafeAllowRunc`, with `sandbox.runtimeClassName: ""` | `false` |

An install that set none of them and ran on Cilium now renders plain NetworkPolicies and the egress
proxy: set `networkPolicies.mode: cilium` to keep the CiliumNetworkPolicies. Because
`sandbox.runtimeClassName` now defaults to `gvisor`, `sandbox.unsafeAllowRunc: true` needs it emptied,
or the render fails with `choose one`. `values-quickstart.yaml` sets `mode: cilium`, and the umbrella's
`values-demo.yaml` sets `mode: none` and the unsandboxed pair.

The factory no longer writes `spec.queueName` on the AgentRuns it creates (D16: Kueue is dropped).
The runs quota (`runsQuota`) and the factory's slots and budgets bound the runs.

### The old App keys

The `maku-factory` keys that step 3 kept beside `apps.*` are removed (D12). The schema refuses each
with a message naming its replacement; under `--skip-schema-validation` the template does. The
factory's config refuses the `runTokens` file aliases the same way when it starts.

| Removed | Set instead |
|---|---|
| `githubSecret` | `apps.factory.existingSecret`, whose default is now `agent-factory-github` |
| `mergerSecret` | `apps.merger.existingSecret` (new; empty means `agent-factory-merger`), with `apps.merger.mode: separate` |
| `runTokens.appSecret` | `apps.agents.existingSecret`, mounted at `/etc/agent-factory-agents` |
| `runTokens.appIDFile`, `runTokens.privateKeyFile` (factory config) | `github.agentsAppIDFile`, `github.agentsKeyFile`, which `apps.agents` writes |

`runTokens.enabled` now needs `apps.agents.existingSecret`; the `/etc/agent-factory-run-tokens` mount
is gone. Upgrade the chart and the image together: the config is decoded strictly.

### Private code is refused without its acknowledgement

A Repository serving private code (`private` or `internal` on GitHub, or a visibility the factory
cannot read) without `spec.privateCode.acknowledged: true` is now refused new tasks, where step 7
warned (D3, owner ruling R9). Its `factory/ready` gets the refusal and loses the label, the API
answers `403 private_code_unacknowledged`, schedules and adoption start nothing there, and the
preflight fails `private_code_unacknowledged`. Tasks already started finish.

Before upgrading, check that the gauge reads 0, or acknowledge each repository it names:

```bash
kubectl -n <namespace> get repository -o custom-columns=NAME:.metadata.name,REPO:.spec.repository,PRIVATE:'.status.conditions[?(@.type=="PrivateCode")].reason'
kubectl -n <namespace> patch repository <name> --type merge -p '{"spec":{"privateCode":{"acknowledged":true}}}'
```

That field states the repository's code is sent to the configured models, which are usually SaaS
([private code](factory.md#private-code)). The demo still serves public repositories only.

### The gateway is the model path

| Value | Before | Now |
|---|---|---|
| `maku.runController.model.mode` | `secret` by default | `gateway` by default: set `model.gateway.aliases.public` (and `maku-factory.config.runs.gatewayIdentity: true` under the umbrella), or the render fails ([runs through the gateway](gateway.md#runs-through-the-gateway)) |
| `maku.runController.model.mode: secret` | any install | demo-only: refused without `demoValues: true` (`runController.model.mode secret is demo-only (v4 D7)`), and on the install banner as `model_key_in_sandbox` |
| `demoValues` (maku chart) | absent | `false`. `values-quickstart.yaml` and the umbrella's `values-demo.yaml` (as `maku.demoValues`) set it |

An install whose run controller still uses `agent-model`:

1. Move it to the gateway, with the [step 4 steps](#runs-through-the-model-gateway-v04x-v4-rollout-step-4).
2. Or, for a demo only, set `model.mode: secret` and `demoValues: true` explicitly.

An install with `runController.enabled: false` (runs reconciled elsewhere) reads neither value and
renders as before.

### The single-repository config

The factory config's top-level `repository`, `maintainers`, `triggerLabel`, `controlIssue`,
`classes`, `merge` and `schedules` (v2 D1) are refused at start, each by name:

```text
config: repository is the single-repository shape, removed (v2 D1): serve the repository from a Repository object, which maku config migrate writes from this file (docs/upgrading.md#the-single-repository-config)
```

Convert the file before you upgrade. `maku config migrate` still reads the old shape: it writes the
`Factory`, `Repository` and `MergePolicy` objects and the residual file, with `controlIssue` moved to
`control` on that repository and `api.repositories` dropped (the list derives it):

```bash
maku config migrate --from factory.yaml --residual factory.new.yaml > resources.yaml
```

### The group migration

`maku-broker migrate-group` and its v0.30.0 upgrade e2e are removed. An install still serving `Room`
and `Task` under the pre-rebrand API group (older than v0.31.0) first upgrades to the last v0.4x release
before this one, runs [the group move](maku-group-migration.md) with that release's `maku-broker`,
then upgrades here.

### The Slack App's metric names

The Slack App's metrics are served as `maku_slack_app_*`, with no dual export:

| Before | Now |
|---|---|
| `factory_slack_requests_total{route, outcome}` | `maku_slack_app_requests_total{route, outcome}` |
| `factory_slack_build_info{version}` | `maku_slack_app_build_info{version}` |

The chart's `SlackCardUpdatesFailing` rule moves with it. Rewrite any query, rule or dashboard you
keep outside the chart before the upgrade, or read both names across it
(`maku_slack_app_requests_total or factory_slack_requests_total`).

## The per-repository merge policy removed (v0.4x, release N+1)

Breaking. The release after [reusable merge policies](#reusable-merge-policies-v04x-release-n)
removes the per-repository shape:

| Removed | What happens to it now |
|---|---|
| `MergePolicy.spec.repositoryRef`, `classes` and `merge` | Gone from the CRD. The API server prunes them on read: a forgotten per-repository object reads as an empty class policy with `CONSUMERS 0`, which serves nothing and binds nothing. `maku config validate` refuses one: `the per-repository MergePolicy was removed in this release: maku config migrate --manifests (from the previous release)` |
| `MergePolicy.status.authority`, `classCount`, `classes`; the `REPO` column; `Repository.status.mergePolicy` | Gone. A repository's authority, classes and counts are on its `Repository` |
| The `NoMergePolicy` and `DuplicatePolicy` reasons | Gone. A `Repository` without bindings is served with no class but `review`, or refused `Invalid` by the config's own rules |
| `maku config migrate --manifests` | Gone: it converted per-repository manifests. `--from` still converts a config file's `repositories[]` |
| A `.github/agent-factory.yaml` with `classes:` (and `conditions.any`) | Refused, `RepositoryFileValid` `False`: `the classes shape was removed in this release: write mergePolicies with maku policy export; under repository authority, …rewrite each class by hand as a mergePolicies binding of a MergePolicy`. Under central authority `maku policy export` writes the file; under repository authority, rewrite it by hand, and until then auto-merge pauses, as for any invalid file |
| The catalogue's `generations.mergePolicy` (MCP `classes_list`) | Gone; `generations.mergePolicies` has each bound class's policy generation |
| The settings page's per-repository rows and its class-file editor | Gone. A bound repository edits its bindings; a policy is edited on its own page |

Before you upgrade, on every cluster:

```bash
kubectl get mergepolicy -A -o jsonpath='{..repositoryRef}'   # prints nothing
```

and no repository's `.github/agent-factory.yaml` has a `classes:` key. Convert what is left with the
previous release's `maku config migrate --manifests` and `maku policy export` first. Then upgrade the
CRDs (`agent-factory-crds`) and the factory together.

## Reusable merge policies (v0.4x, release N)

A `MergePolicy` becomes one reusable class definition, named by its object, and a `Repository`
binds the ones it uses under `spec.mergePolicies`, with its own `spec.merge`
([factory resources](merge-policies.md#factory-resources)). This release reads both shapes; the next
minor, now shipped, [removes the per-repository one](#the-per-repository-merge-policy-removed-v04x-release-n1).
Nothing changes until a manifest does: a repository served from
a per-repository `MergePolicy` is decided exactly as before, with one warning per such object at
every start (`MergePolicy <name> is in the per-repository shape, removed in the next minor: maku
config migrate`).

1. Upgrade the chart and the CRDs (`agent-factory-crds`), then bump the factory.
2. Convert, offline, and check that no resolved class changes:

   ```bash
   maku config migrate --config factory.yaml --manifests resources.yaml > bound.yaml
   maku config validate --config factory.yaml --base-ref origin/main --expect-unchanged bound.yaml
   ```

   `migrate` writes nothing unless every resolved class and merge setting is unchanged, and prints a
   `changed:` line for each difference.
3. Commit the converted manifests in one pull request per repository: the policies it is the first
   to need, its `Repository` with `merge` and `mergePolicies`, and its per-repository `MergePolicy`
   deleted. Flux applies the new objects, then prunes the old one; between the two the per-repository
   `MergePolicy` is refused `DefinedTwice` and the `Repository` is already served from its bindings.
4. Check what the factory serves:

   ```bash
   kubectl get mergepolicy,repository -n agent-system
   ```

   Every policy's `CONSUMERS` and `CURRENT` match, and each `Repository` is `Accepted` with
   `BindingsValid` `True`.
5. A repository's own `.github/agent-factory.yaml` with `classes:` is rewritten in the binding shape
   with `maku policy export`.

Before the next minor, check that no per-repository object and no `classes:` file is left: its
[section](#the-per-repository-merge-policy-removed-v04x-release-n1) has the checks.

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

Each component then answers for itself, on its ops port (`:9090`, beside `/metrics`, never exposed
publicly): `GET /preflight` runs its own checks through its own network path and credentials, each
bounded to 10 s and all within 30 s, and caches the answer for 30 s. The answer holds codes and fixed
messages only: no DSN, key, token or upstream error body. A refusal's detail stays in the
component's own log. The components serve `/preflight` only when the preflight is enabled
(`MAKU_PREFLIGHT=true`, which the charts set), so on a default install no pod can make them spend
their credentials.

| Component | Its checks |
|---|---|
| broker | `database` (its own role, RLS, the schema), `oidc`, `github-link`, `knowledge` (`tools/list` names `kb_search` and `kb_get`), `models` (one-token completion per planner and classifier alias) |
| run-controller | `run-path`: its own preflight's last result, one code per refusal (`tracing` and `models` report `skip`) |
| factory | `apps` (each installation's grants), `merge-gate`, `private-code`, `exposure`, `oidc` |

| New key | Default | What it does |
|---|---|---|
| `maku-factory.preflight.enabled` | `false` | Serves the factory's `/preflight`, and grants the factory `get` and `list` on HTTPRoutes, GRPCRoutes, TLSRoutes and Ingresses in its namespace, for `exposure`: a route to the broker's Service, on any port, fails. The umbrella refuses `maku.preflight.enabled` without it |
| broker config `preflight.timeout` | `10s` | One broker check's bound, between 1 s and 30 s |

With `maku.preflight.enabled`, the chart adds a `run-controller` Service on `:9090` for the
collection. An image older than the chart answers 404 there, reported as `component_too_old`.

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
| `values-demo-gateway.yaml` | not layered | the demo with the gateway: `demo.model.apiKey` becomes the provider's Secret `demo-model` in the release namespace, and no `agent-model` is rendered. Folded into `values-demo.yaml` and deleted in step 7 ([the demo's gateway](#the-demos-model-gateway-v04x-v4-rollout-step-7)) |
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
