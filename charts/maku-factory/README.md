# maku-factory chart

Deploys the agent factory: two replicas of the `maku-factory` controller (Deployment `agent-factory`) behind a
PodDisruptionBudget, RBAC in two namespaces (a Role in the release namespace and a Role in
`config.runs.namespace`, default `agents`, `templates/rbac.yaml`), and the `Task` CRD (`crds/`). It ships as
`oci://ghcr.io/smana/charts/maku-factory`, at the version of the release tag.

Install it only after the secrets below exist in the release namespace. The factory's behaviour
is described in [docs/factory.md](../../docs/factory.md).

## Prerequisites

| Secret (value) | Default name | Keys | Mounted at (`templates/deployment.yaml`) | Used by | Required |
|---|---|---|---|---|---|
| `githubSecret` | `agent-factory-github` | `app_id`, `private_key` (the factory App, from the `agents-secrets` store) | `/etc/agent-factory-github`, mode 0440 | `config.github.appIDFile`, `config.github.privateKeyFile` | Yes: the mount is not optional |
| `mergerSecret` | `agent-factory-merger` | `app_id`, `private_key` (the merger App: arming, reverts, checks) | `/etc/agent-factory-merger`, mode 0440, optional | `config.github.mergerAppIDFile`, `config.github.mergerKeyFile` | **Hard prerequisite** when any merge class is live or in shadow (see below) |
| `brokerCASecret` | `openbao-ca` | `ca.crt` (the broker's CA) | `/etc/agent-factory/openbao-ca` | `config.broker.caFile` | Yes |
| `oidcSecret` | `agent-factory-oidc` | the ZITADEL client ids the API accepts tokens from (`rooms-proxy`, and the CLI's, formerly `roomctl`): ids, not secrets | `/etc/agent-factory-oidc`, mode 0444 | the factory API's token check | Yes |
| `intakeSecret` | `agent-factory-intake` | `token` (the RunLore intake's bearer token, from the `agents-secrets` store) | `/etc/agent-factory-intake`, mode 0440, optional | `config.runlore.tokenFile` | Only for the RunLore intake: without it the intake answers 503 to every request |
| `zitadelReaderSecret` | `agent-factory-zitadel-reader` | `reader.json` (the broker's read-only ZITADEL reader: `{"pat", "githubIdpId"}`) | `/etc/agent-factory-zitadel-reader`, mode 0440, optional | `config.api.access.readerFile` | v2 D4: resolves an API caller to their GitHub login; without it every human run is refused `repository_permission` |
| `sponsoredSecret` | `agent-factory-sponsored` | `<name>-client-id`, one per sponsored principal (the machine client's ZITADEL client id), and `project-id` (the project every sponsored token's audience must hold) | `/etc/agent-factory-sponsored`, mode 0440, optional | `config.api.sponsored[].clientIDFile`, `config.api.projectIDFile` | F4: only with `config.api.sponsored`, which needs both files; without the Secret every sponsored token is refused |

The broker token is not a Secret: the chart projects a service-account token with audience
`roomsAudience` (`rooms-system`) at `/var/run/secrets/agents/rooms/token`, and
`config.broker.tokenFile` names it.

**The merger key is a hard prerequisite whenever any merge class is live or in shadow.** Its
mount is optional so that a factory with every class off starts without it, but with a live or
shadow class the binary refuses to start when the key is missing: the pod goes to
`CrashLoopBackOff` with a clear log line. cloud-native-ref's config runs two classes (`docs-links`, `revert`) in shadow, so
treat the merger Secret as required unless you turn every class off. No other workload mounts
it. The 0440 mode with the pod's `fsGroup` (65532) is what makes it readable, by the factory's
uid and nobody else.

## Values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` | `ghcr.io/smana/maku-factory` | The image |
| `image.tag` | `""` (required) | `<version>@sha256:<digest>`; never a bare tag |
| `replicas` | `2` | Controller replicas |
| `resources` | requests 100m / 256Mi, limits 500m / 512Mi | The container's resources |
| `githubSecret`, `mergerSecret`, `brokerCASecret`, `oidcSecret`, `intakeSecret` | see the table above | Secret names |
| `monitor.flux.enabled` | `false` | Renders the read-only ClusterRole and ClusterRoleBinding `agent-factory-flux-reader` (`get` on gitrepositories, `list` on kustomizations and helmreleases, no watch) for a class's `monitor.flux`; without it the Flux signal ends `inconclusive` ([docs/factory.md](../../docs/factory.md#the-post-merge-monitor)) |
| `roomsAudience` | `rooms-system` | Audience of the projected token for the broker's system API |
| `api.tls.secretName` | `""` | The run-request API's key pair (`tls.crt`, `tls.key`), mounted at `/etc/agent-factory-api-tls`; the chart writes `config.api.tls` to read it and names the Service port `https`. Empty: plaintext, and the broker needs `factoryInsecure: true` |
| `api.tls.issuerRef` | `{kind: ClusterIssuer, name: ""}` | `name` set: a cert-manager `Certificate` `agent-factory-api-tls` for `agent-factory.<ns>.svc[.<clusterDomain>]` writes `api.tls.secretName`; empty: bring the Secret yourself |
| `clusterDomain` | `cluster.local` | The cluster's DNS domain, for the Certificate's names |
| `config` | `{}` | The factory config, rendered through `tpl` (`{{ .Release.Namespace }}` works); an empty config fails the start |

## Ports

| Service port | Container port | Serves |
|---|---|---|
| `metrics` 9090 | `ops` | Metrics and the probes |
| `api` 8082 (`https` with `api.tls.secretName`) | `api` | `POST /v1/runs` and `POST /v1/tasks`; TLS 1.3 when `api.tls.secretName` is set, plaintext otherwise; the broker's `factoryURL` points here. Before this chart moved it, the API sat on 8443 |
| `intake` 8080 | `intake` | `POST /intake/runlore` |

## RBAC

In the release namespace, the Role reads and writes `Task`s and their status, reads, creates and
deletes `Room`s, takes the leader lease and records events. Its one ConfigMap rule (`get`, `list`,
`watch`, `create`, `update`, `delete`) names no `resourceNames`: it covers the stop object
`agent-factory-stop`, the meter heartbeat `agent-factory-meter` and the day ledgers
`agent-factory-ledger-usd-<UTC day>`, whose names carry the date; the meter also deletes the token
ledgers they replaced, `agent-factory-ledger-<UTC day>`, once 35 days old. The namespace is the bound, so the
factory can touch every ConfigMap in it, its own config included: keep no ConfigMap there it must
not touch. In `config.runs.namespace` (default `agents`), a second Role manages the `AgentRun`s of `config.runs.group` (default `cloud.ogenki.io`): the values the binary reads, so the two cannot disagree. `tests/render.sh` asserts the ConfigMap rule's shape and the 8082 ports.

Three ClusterRoles cover the `Factory`, `Repository` and `MergePolicy` kinds for people, not for
the factory. Each aggregates into a built-in role, so a RoleBinding to `view`, `edit` or `admin`
grants it in that namespace: `maku-viewer` (`get`, `list`, `watch` on the three kinds) into `view`,
`maku-mergepolicy-editor` (every verb on `mergepolicies` only) into `edit`, and `maku-factory-admin`
(every verb on the three kinds) into `admin`. Their names are fixed: one factory per cluster.
`tests/render.sh` asserts each role's rules.

## Config

`config` is rendered as-is into `/etc/agent-factory/config/config.yaml` (a ConfigMap; a change
rolls the pods). Its keys, defaults and validation are in
[docs/factory.md](../../docs/factory.md#the-config-file). Keys added since a file was written
default to the platform's values, so an older file loads unchanged: `api.groups` (`agents-admin`,
`agents-member`) and a schedule's `probeAuthor` (`renovate[bot]`). The daily budgets are in USD:
this release still loads the token days `budgets.factoryDaily`, `budgets.humanDaily` and
`budgets.perPerson.dailyTokens`, ignores them and logs a warning naming the USD key to use; the
next one refuses them. For example:

```yaml
api: {groups: {admin: agents-admin, member: agents-member}}
budgets: {factoryDailyUSD: 42.50, humanDailyUSD: 8.50}
caps: {escalatedHours: 72}
meter: {prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40, source: "https://docs.z.ai/guides/overview/pricing", checkedAt: "2026-10-16"}}}
```
