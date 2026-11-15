<!-- page-type: reference -->

# Install support and cluster reference

Version floors, multi-install cluster rules, generated directory layout and CLI preflight details for [Install on your cluster](../install.md).

## The support floor

| Piece | Floor |
|---|---|
| Kubernetes | 1.30 (the ValidatingAdmissionPolicies), tested on the three latest minors |
| Postgres | 16 |
| GitHub | GitHub.com, or GHES 3.14 |
| Helm | 3.15. Helm 3.14 assumes Kubernetes 1.29 when it renders offline, so its `helm template` needs `--kube-version` |
| Flux | 2.4 |
| Argo CD | 2.12 |
| Gateway API | v1.2 |

## Several installs in one cluster

Several teams means several installs, each in its own namespace with its own Apps, database,
runs namespace (`maku.runs.namespace`) and fence. Every cluster-scoped object an install renders
carries its namespace: the admission policies, the run-controller's and the preflight's cluster
roles, the broker's token review binding, and the factory's Flux binding. The roles every install
shares, the three aggregated into `admin`, `edit` and `view` and `agent-factory-flux-reader`, are
`agent-factory-crds`', installed once per cluster with the CRDs. `tests/two-installs.sh` renders two
installs of the same release name in two namespaces and finds no cluster-scoped name they share.

Two things stay the cluster's, so one install at most renders them:

- the RuntimeClass, when `maku.runController.createRuntimeClass` is set; leave it off elsewhere, or
  create the class yourself;
- agentgateway's controller (`agentgateway.enabled`): it serves every Gateway of its class, so the
  other installs set it false and use the same controller.

## Generated install directory

The directory it writes:

| Path | What it holds |
|---|---|
| `values.yaml` | the umbrella's values, every image by digest, no secret value |
| `crds/values.yaml` | the CRD chart's values |
| `factory/` | the `Factory`, one `Repository` per repository, and a `MergePolicy` per class, every class in shadow. The `Factory`'s `spec.mergeDefaults` holds the merge settings every repository shares, so each `Repository` names only its own ([below](#onboard-another-repository)), and it writes out the three workflows, `quick`, `standard` and `careful` ([the shipped workflows](../factory.md#the-shipped-workflows)) |
| `github/` | the merge gate's ruleset and the `gh` commands that create it, which it never runs |
| `secrets/` | mode 0700, files 0600, git-ignored: the Apps' keys, a generated cookie secret, the client ids, and templates with empty keys for what it never asks (the DSNs, the provider key, the issuer's client secret, Slack's) |
| `gitops/` | the same install for Flux and for Argo CD |
| `README.md` | the commands to run next, in order |

It refuses a non-empty directory without `--force`, and never overwrites `secrets/`. The factory's
API runs on TLS from the cert-manager ClusterIssuer you name (`tls.issuer`); without one, only an
explicit `factory.insecure: true` accepts plaintext, which the install banner then shows.

### Onboard another repository

`maku repo add owner/name` writes the next repository's `Repository`, fence and ruleset
([maku](../cli.md)). The `Factory` above already carries the settings every repository shares:

```yaml
spec:
  mergeDefaults:
    mergerLogin: <factory-app>[bot]   # the factory App, which merges (the merger folded in)
    verifyFor: 30m                    # a merge is watched this long for a failing verify check
    revertWindow: 168h                # a revert within it counts against the class
    breaker: {window: 10, maxReverts: 1}  # 1 revert in the last 10 merges demotes the class
```

So the `Repository` it writes is 12 lines: the repository, its maintainers, its trigger label and its
checks. A key the `Repository` sets wins whole ([merge policies](../merge-policies.md#factory-resources)).
For a `Factory` without `mergeDefaults`, `--merge-defaults=false` writes the four keys into the
`Repository` instead: the factory refuses a `Repository` that lacks them and has no default to fill.

## Preflight CLI permissions and checks

It adds the checks that only your machine can make, the `cli` rows of the table below: each hostname
of the web and Slack routes resolves, the web host is oauth2-proxy's and serves `/.well-known/roomctl`
through it, and the Slack host answers `POST /slack/events` only. `--no-outside` skips them. It exits 1
on a fail, and 2 when the release renders no preflight or the Job leaves no report. Your kubeconfig
needs, in the namespace, `get` and `list` on CronJobs, `create`, `get` and `delete` on Jobs, `list` on
Pods, `get` on `pods/log`, and `get` on HTTPRoutes and Ingresses ([CLI](../cli.md)).

For one repository, `maku repo check owner/name` asks the factory for its `apps`, `merge-gate` and
`private-code` rows alone (`GET /preflight?repository=owner/name`), and adds the repository's fence
and its `Repository`'s conditions; `maku repo add` writes the files those rows check
([CLI](../cli.md)).

## Private code surfaces

Without `privateCode.acknowledged: true` on a private or internal repository (or one whose visibility read fails), tasks already started finish while new work is refused (`factory/ready` comments and removes the label, the API answers `403 private_code_unacknowledged`, and neither schedules nor adoption start tasks):

| Surface | Reports |
|---|---|
| `Repository` `PrivateCode` condition | `False` `Unacknowledged` ("No new task starts until it is set"); `True` `Public` or `Acknowledged`; `Unknown` `NotChecked` before the first read or `NotServed` |
| Preflight `private-code` check | `fail` `private_code_unacknowledged`, naming the repositories |
| Metric and alert | `maku_factory_private_code_unacknowledged{repository}` (`1` or `0`); `AgentFactoryPrivateCodeUnacknowledged` fires after 15 minutes at `1` |
| Web UI install banner and logs | `private_code_unacknowledged` via `GET /v1/install` (requires `factoryURL` on the broker); one `WARN` per visibility change |
