<!-- page-type: reference -->

# Components, ports, images and packages

Binaries, container images, listening ports, release tags, Kubernetes resources and Go package layout for [Architecture](../architecture.md).

## Ports

| Port | Binary | Who calls it |
|---|---|---|
| `:8080` | maku-broker | Humans in the web UI and `maku`, both through oauth2-proxy |
| `:8443` | maku-broker | The bridge in each run pod; system callers (the factory) |
| `:8090` | maku-broker | The agents' `room_*` tools through the model gateway |
| `:9090` | maku-broker | Metrics and probes |
| `:8082` | maku-factory | `POST /v1/runs`, TLS 1.3 when `api.tls` is set (`internal/tlsreload`): the broker's `start_run` |
| `:8080` | maku-factory | `POST /intake/runlore` |
| `:9090` | maku-factory | Metrics and probes |
| `:9090` | maku-run-controller | Metrics |
| `:9091` | maku-run-controller | Probes (`/healthz`, `/readyz`) |
| `:8085` | maku-bridge | The kubelet's probes of the native sidecar (`HEALTH_ADDR`) |
| `:8080` | maku-slack | Slack's events, `POST /slack/events` |
| `:8081` | maku-slack | The broker's webhooks, `POST /webhooks`, cluster only |
| `:9090` | maku-slack | Metrics and probes |

## Binaries and images

| Binary | Runs as | Image |
|---|---|---|
| `maku-broker` | Stateless service in `agent-system`; owns the `Room` CRD (`maku.ogenki.io/v1alpha1`); subcommands `serve`, `retention`, `migrate` | `ghcr.io/smana/maku-broker` |
| `maku-bridge` | Native sidecar in every `AgentRun` pod that names a room; relays harness events, carries the room token | `ghcr.io/smana/maku-bridge` |
| `maku-factory` | The factory's orchestrator in `agent-system`, two replicas, one leading; owns the `Task` CRD; chart `oci://ghcr.io/smana/charts/maku-factory` | `ghcr.io/smana/maku-factory` |
| `maku-harness` | Main container of every `AgentRun` pod: OpenHands agent-server plus `agent-run`, the git credential helper, `gh` with the PR footer and the trailer hook ([`images/maku-harness`](../../images/maku-harness/README.md)) | `ghcr.io/smana/maku-harness` |
| `maku-run-controller` | A Deployment, on clusters without cloud-native-ref's Crossplane runtime; reconciles `maku.ogenki.io` AgentRuns into gVisor pods | `ghcr.io/smana/maku-run-controller` |
| `maku-slack` | The factory's Slack App, a Deployment beside the factory; its config is `--config` or `SLACK_APP_CONFIG` ([Slack App](../slack.md)) | `ghcr.io/smana/maku-slack` |
| `maku` | A developer's CLI: list, follow, post, queue and fork rooms; `maku setup` writes the maku-handoff Agent Skill for local coding agents; `maku token` for scripts. Never steers or approves: those stay in the web UI | Release assets `maku-{linux,darwin}-{amd64,arm64}` + `maku.sha256`; before a release, `go build ./cmd/maku` |

Every image is multi-arch (`linux/amd64`, `linux/arm64`). The Go ones are static binaries on distroless `nonroot`; the harness is OpenHands' Python image, run as uid 10001.

```bash
maku configure --url https://rooms.<domain>                       # the broker serves the issuer and ids; maku doctor checks VPN, CA, login, GitHub
maku login                                                        # device flow, in any browser
maku rooms --needs-me                                             # issue, title, task, PR per room; --repo, --mine, --json
maku needs                                                        # what needs you across rooms, in one order; --since, --room, --repo
maku status 3kq7x2ma                                              # the brief's head, then the card; --json, --after seq:N
maku watch 3kq7x2ma
maku post 3kq7x2ma --queue "address L42"
maku fork 3kq7x2ma --at 42 --role implementer --egress pypi --note "try uv"
maku setup                                                        # registers MCP server and skill for your coding agent
```

## Releases

| Trigger | Tag | Workflow |
|---|---|---|
| Each push to a PR from this repo | `v<next-patch>-pr<N>.<sha8>`, `<sha8>` = the PR **head** | `ci.yaml` → `prerelease` |
| The same push, the factory chart | `<next-patch>-pr<N>.g<sha8>` (the `g` keeps it semver) | `ci.yaml` → `chart-prerelease` |
| A `v*` git tag | the tag, e.g. `v0.11.0` | `release.yaml` |

Never `latest`. Consumers pin by digest. Every pushed image carries SLSA provenance (`mode=max`) and
an SBOM as attestations, and a keyless cosign signature on its digest:

```bash
cosign verify ghcr.io/smana/maku-broker@sha256:<digest> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/Smana/(agent-platform|maku)/\.github/workflows/release\.yaml@refs/tags/v'
```

Releases before v0.30.0 carry the `Smana/agent-platform` identity, hence both names in the
regexp. That accepts released images only. PR pre-releases are not release-grade (they run
unreviewed branch code): see [verifying an image](security-controls.md#supply-chain) for their variant.

## Where each piece lives

| Resource | Namespace | Defined in | Built from |
|---|---|---|---|
| `agent-factory` Deployment, Service, PDB, RBAC and config | `agent-system` | `charts/maku-factory` here | Image `ghcr.io/smana/maku-factory`, this repo |
| `Task` CRD | cluster; CRs in `agent-system` | `charts/maku-factory`, a copy of `config/crd/` | `api/factory/v1alpha1` here |
| `maku` chart | broker, run controller, `Room` and `AgentRun` CRDs, runs namespace | `charts/maku` here | The umbrella for a cluster without cloud-native-ref; the laptop profile is the [one-run quickstart](../contributing/one-run-quickstart.md). The broker runs `human.mode: local` and `runIssuers[].mode: tokenreview` there ([broker fields](config.md#broker-fields)); `room-broker migrate` is its schema init container |
| Factory secrets: `agent-factory-github`, `agent-factory-merger`, `openbao-ca`, `agent-factory-oidc`, `agent-factory-intake` | `agent-system` | Mounted per the [chart README](../../charts/maku-factory/README.md), which names each secret's source | The platform's secret store |
| `room-broker` Deployment and Service (`App` claim `room-broker`) | `agent-system` | [cloud-native-ref](https://github.com/Smana/cloud-native-ref) `infrastructure/base/room-broker/` | Image `ghcr.io/smana/maku-broker`, this repo |
| `Room` CRD | cluster; CRs in `agent-system` | Vendored by cloud-native-ref as `crd-rooms.yaml` | `api/v1alpha1`, `config/crd/` here |
| `SQLInstance xplane-rooms` (CNPG) | `agent-system` | cloud-native-ref | Composition in [crossplane-configuration](https://github.com/Smana/crossplane-configuration); migrations `internal/store/migrations/` here |
| Retention CronJob `room-broker-retention` | `agent-system` | cloud-native-ref | The broker image, `retention` subcommand |
| `Certificate room-broker-tls` | `agent-system` | cloud-native-ref | cert-manager, ClusterIssuer `openbao` |
| `room-bridge` sidecar and its `room-token` volume | `agents` | crossplane-configuration's `AgentRun` composition | Image `ghcr.io/smana/maku-bridge`, this repo |
| `agent-harness` image | `agents` | crossplane-configuration's `AgentRun` composition | `images/maku-harness`, this repo |
| Secret `room-broker-ca` | `agents` | cloud-native-ref ExternalSecret | The platform's private CA |
| CNPs | `agent-system` | cloud-native-ref | — |
| `ServiceMonitor`s, `PodMonitor`s, `PrometheusRule` `maku` | the release namespace | The umbrella chart, with `monitoring.*.enabled` ([observability](../observability.md)); cloud-native-ref runs its own `VMServiceScrape`s and `VMRule`s until it turns them on | `charts/agent-factory`, this repo |
| oauth2-proxy, `HTTPRoute` | `agent-system` | cloud-native-ref | — |
| `agent-router` MCPRoute backend for `:8090` | `agent-system` | cloud-native-ref | — |

[Integration](../integration.md) lists what each consumer carries and pins.

### Where it plugs in

| Repo | Consumes |
|---|---|
| [cloud-native-ref](https://github.com/Smana/cloud-native-ref) | Deploys the broker (`App` claim), its CNPG database `xplane-rooms`, the `Room` CRD, RBAC and network policies |
| [crossplane-configuration](https://github.com/Smana/crossplane-configuration) | The `AgentRun` composition injects the `room-bridge` sidecar and its room token |

Design: [collaboration rooms](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-collaboration-rooms-design.md),
part of the [Agent Factory programme](https://github.com/Smana/cloud-native-ref/blob/main/docs/superpowers/specs/2026-09-23-agent-factory-design.md).

The programme's sub-project codes, where you meet them in the specs: SP2 is the rooms, SP3 the factory.

## Code layout

The code follows Go conventions and a clean architecture modelled on
[RunLore](https://github.com/Smana/runlore). [AGENTS.md](../../AGENTS.md) owns the full standard; five
rules shape the tree.

| Rule | In practice |
|---|---|
| Packages split by domain or pipeline stage, never by layer | `envelope`, `redact`, `store`, `authn`, `runwatch`, `roomctrl`, `bridgeapi`, `bridge`, `policy`, `fanout`, `humanapi`, `mcp`: no `service/`, `repository/` or `handler/` |
| `cmd/<bin>` stays thin | `cmd/maku-broker`, `cmd/maku-bridge`, `cmd/maku`, `cmd/maku-factory`, `cmd/maku-run-controller` and `cmd/maku-slack` parse arguments and dispatch; the wiring lives in `internal/app` |
| Interfaces are declared on the consumer side | A package asks for the few methods it calls (the bridge API's `Log`, the controller's `Store`) instead of importing a wide type |
| The store is the only SQL adapter | Nothing outside `internal/store` writes SQL, the retention job's `DELETE`s included |
| One egress client | Every outbound HTTP call goes through `internal/httpx`; a lint rule bans the bare `net/http` helpers elsewhere |

Broker, bridge and CLI, by pipeline stage:

| Package | Stage |
|---|---|
| `cmd/maku-broker` | `serve`, `retention` and `migrate` subcommands |
| `cmd/maku-bridge` | The sidecar binary |
| `cmd/maku-run-controller` | The AgentRun controller binary |
| `cmd/maku`, `internal/maku` | The human CLI and its client; `maku mcp`, the stdio relay to `/mcp` |
| `cmd/maku-slack`, `internal/slackapp` | The factory's Slack App: intake from a thread, webhooks to the thread ([Slack App](../slack.md)) |
| `internal/maku/skill` | The embedded maku-handoff Agent Skill (MCP first, shell fallback), released with maku; `maku setup` and `skill install` write it, `skill.Version` reads its stamp for the doctor's `skill` drift check |
| `internal/app` | Wiring per binary, the only importer of every adapter |
| `internal/config` | The broker's config file: strict decode, defaults, validation |
| `internal/logging` | A binary's `*slog.Logger`: JSON or text, level by env |
| `internal/version` | Build version stamped by `-ldflags` |
| `internal/httpx` | The one audited egress client: timeouts, a redirect cap, no credential header across hosts |
| `internal/bridge` | The sidecar: harness adapter over loopback, event mapping, status, uploader, SSE consumer |
| `internal/wire` | Bridge and browser frames: the types both ends of a connection share |
| `internal/authn` | Offline JWT verification for runs, system callers and humans; principal mapping |
| `internal/sponsored` | What the broker and the factory must agree on for a sponsored principal |
| `internal/envelope` | Event types, payload builders, validation, limits |
| `internal/redact` | Secret detection over every JSON string of a payload |
| `internal/store` (+ `migrations/`) | Append, range, cursors, seal; the only package that speaks SQL |
| `api/v1alpha1` | The `Room` types and deepcopy; the CRD is generated into `config/crd/` |
| `internal/runwatch` | The `AgentRun` informer: liveness, membership, run events, end reason |
| `internal/roomctrl` | The `Room` reconciler: row, first event, finalizer, status projection |
| `internal/suspend` | The suspend trigger: a suspendable run waiting on an approval in an idle attended room is suspended, and resumed once nothing waits |
| `internal/runctl` | The run controller: `maku.ogenki.io` AgentRuns into pod, ServiceAccount, ConfigMaps and CNP |
| `internal/bridgeapi` | `:8443`: hello, events, SSE stream, system API, approvals |
| `internal/tlsreload` | The reloading certificate loader of `:8443` and the factory's run-request API; `tlsreload/tlsreloadtest` is its test-only pair writer |
| `internal/mcp` | `:8090` MCP server, the `room_*` tools |
| `internal/mcpwire` | The JSON-RPC wire both MCP servers share: one message in, one answer out |
| `internal/github`, `internal/verdictpost` | The factory App client; the leader's verdict comments |
| `internal/brief`, `internal/runrequest` | The fenced brief; manifest and factory run requesters |
| `internal/prpattern` | A repository's pull-request URL patterns, compiled once per repository in a bounded cache |
| `internal/webhook` | The broker's outbound events: id-only payloads, signed as Standard Webhooks sign them |
| `internal/fanout` | LISTEN/NOTIFY hub: coalesced per-room reads, a 1 s poll while the listener is down |
| `internal/policy` | The permission matrix |
| `internal/summary` | The pure fold of a room's log into `summary/v1`, the one object the room page and `maku status` render |
| `internal/roomview` | The factory's four room views as `maku` renders them; the one place that makes untrusted text inert |
| `internal/ghidentity`, `internal/repoaccess` | A member's GitHub login from their IdP link; whether it may read a room's repository, cached, failing closed |
| `internal/humanapi` + `ui/dist/` | `:8080` WebSocket, room list, actions, embedded UI; `POST /mcp`, the humans' MCP server |
| `web/` | TypeScript UI and its vitest suite |
| `internal/metrics` | The metric set and the Prometheus exporter maku-broker serves |

The factory, by the same stages. Its wiring is `internal/app/factory.go`.

| Stage | Package | Contract |
|---|---|---|
| Entry | `cmd/maku-factory` | The binary; `FACTORY_CONFIG`, `POD_NAMESPACE` |
| Entry | `internal/factory/config` | The factory's config file: strict decode, defaults, validation, its hash |
| Intake | `internal/factory/intake` | The issue poller: maintainer labels, snapshots, dedup, stop labels |
| Intake | `internal/factory/sanitize`, `internal/factory/taskid` | Untrusted text defused to a fixpoint; task names and ids |
| Intake | `internal/factory/fence` | The single-owner fence: a repository is acted on only by the factory its default branch's `.github/agent-factory.yaml` names |
| Intake | `internal/factory/finding` | What both RunLore intakes write the same way: the task text, the public stub issue, the label charset that keeps attacker-influenced identifiers inert |
| Tasks | `api/factory/v1alpha1` | The `Task` types and deepcopy; CRD generated into `config/crd/` |
| Tasks | `internal/factory/triage`, `internal/factory/reconciler` | Template, tier and budget; the `Task` state machine |
| Tasks | `internal/factory/runs`, `internal/factory/killswitch` | The `AgentRun` claim; the stop object |
| Tasks | `internal/factory/steering` | A task class's steering note, read on the default branch at triage; an agent pull request that changes one is refused |
| Tasks | `internal/factory/ledger` | The budget ledger: one ConfigMap per UTC day, its resourceVersion the transaction; admission reserves a run's ceiling, the run's end releases it, the meter owns the spent and settled columns; `api`, `intake`, `meter` and `reconciler` import it |
| Gate | `internal/factory/filefence`, `internal/factory/mdlinks`, `internal/factory/semverdiff` | A class's file conditions on a diff, one implementation for the gate and the adopt poller; Markdown link targets as the links-only shape reads them; whether a diff is a dependency bump of one kind |
| Gate | `internal/factory/decision` | the per-decision record: the values each declared row read, so the tally can replay a cap |
| Gate | `internal/factory/fluxwatch` | Flux's objects for the post-merge monitor (MC7): uncached, read-only, `unstructured` |
| Out | `internal/factory/forge`, `internal/factory/narrate` | GitHub as the factory App (`internal/factory/forge/forgetest`: its test-only fake); narration, at least once |
| Out | `internal/factory/minter` | Per-run tokens: `Roles`, the one role table every run and publisher token is minted from, and `Minter`, a token for one repository and role from the agents App |
| Out | `internal/factory/rooms` | The broker's system API over TLS |
| Ops | `internal/factory/meter`, `internal/factory/fmetrics`, `internal/factory/tracing` | The run meter; the metrics; one root span per task |
| API | `internal/factory/api` | `POST /v1/runs` |
