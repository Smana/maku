# Upgrading

What changes in the install steps, release by release. A release that is not listed here upgrades with
the same `helm upgrade` as before. Until v4.0.0 the chart values may still change in a 0.x minor; each
such change has a section here. From v4.0.0, a breaking value change happens only in a major version.

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
