<!-- page-type: reference -->

# Security controls

Reference tables, admission rules, network modes, protocol invariants and per-stage residuals for [Security](../security.md).

## Contents

- [Role-table invariants](#role-table-invariants)
- [Authorization matrix](#authorization-matrix)
- [TLS on :8443](#tls-on-8443)
- [Redaction](#redaction)
- [Database roles](#database-roles)
- [The model gateway's identity](#the-model-gateways-identity)
- [Network policies and modes](#network-policies-and-modes)
- [The run pod's admission](#the-run-pods-admission)
- [Internal principals and publishing](#internal-principals-and-publishing)
- [Webhooks](#webhooks)
- [The Slack App](#the-slack-app)
- [maku on the developer's machine](#maku-on-the-developers-machine)
- [Supply chain](#supply-chain)
- [Untrusted-input hardening stages and residuals](#untrusted-input-hardening-stages-and-residuals)

## Role-table invariants

**Role-table invariants, and the tests that hold them.** Every permission a run or the publisher is
minted with comes from one role table, `minter.Roles` (`internal/factory/minter/roles.go`):

| Invariant | Test |
|---|---|
| Keys only from `contents`, `pull_requests`, `issues`, `checks`, `actions`, `metadata`: no role holds `statuses`, `workflows` or `administration` | `TestInvariants`, on both variants and every enforced combination |
| Access only `read` or `write`; `metadata: read` always; no `checks: write` or `actions: write` | `TestInvariants` |
| Only the publisher holds `issues: write` | `TestInvariants` |
| The reviewer's token is read-only (with push enforced) | `TestTableHR12` |
| No other code builds a run's permission map | `TestOneRoleTable` |
| A role absent from the table is an error, never read | `TestUnknownRole` |

## Authorization matrix

The permission matrix, enforced by the broker (`internal/policy`, tested row by row). Room roles are cumulative; the approver flag is independent; the driver is one token.

| Action | watcher | collaborator | approver | driver | owner | agent run | system |
|---|---|---|---|---|---|---|---|
| Read the log, live | ✓ | ✓ | — | ✓ | ✓ | `room_read` | ✓ |
| Chat (delivered to nobody) | — | ✓ | — | ✓ | ✓ | `room_post` | ✓ |
| Queue a message for the next run | — | ✓ | — | ✓ | ✓ | — | ✓ |
| Steer or interrupt the running run | — | — | — | ✓ | — | — | while driver |
| Start the next run | — | — | — | ✓ | ✓ | — | factory |
| Decide an approval | — | — | ✓ | with the flag | ✓ | **never** | `system:policy` |
| Driver token | — | request | — | give | take | — | give; yields to humans |
| Fork | ✓ | ✓ | — | ✓ | ✓ | — | ✓ |
| Invite, change roles, close | — | — | — | — | ✓ | — | ✓ |

### The factory writes run token Secrets it cannot read

Under `runTokens.perRun` the factory writes each run's `<run>-git` by server-side apply and
deletes it at the run's end. Its Role in the runs namespace holds `create`, `patch` and `delete` on
Secrets, with no `get`, `list` or `watch`, so the minter never reads a Secret back. A `patch` or a
`delete` answers with the object, so those verbs alone would let the factory read `agent-model`.
The chart therefore always renders, with that Role, the ValidatingAdmissionPolicy
`maku-<namespace>-run-token-secrets`: for the factory's ServiceAccount, a Secret create, update or
delete in the runs namespace is allowed only on a name `<namePrefix><8 characters>-git`, carrying
the factory's labels, and owned by the AgentRun of that name. A denied request answers with no
object (proved on envtest, `charts/maku-factory/tests/admission`). The run-controller gets `get` on
Secrets there without `resourceNames`: it already creates pods that mount any of them.

Residuals of per-run tokens:

- **A replaced token is not revoked:** a pod may still read it until the kubelet syncs the volume,
  so it lives at most 10 more minutes.
- **A run that ends while the factory restarts:** its token is not revoked (the new leader holds
  none in memory) and expires within the hour. Its Secret is deleted. Only the leader mints, so a
  follower never holds a token the leader's could overwrite unrevoked.
- **The pairing is enforced by the umbrella chart only.** The factory admits a repository with
  `enforceOutputs` once the factory has `runTokens.perRun`. The factory cannot see the
  run-controller's `gitSecret`: installed from the standalone `maku` and `maku-factory` charts, a
  run-controller given a laptop's PAT would mount it while the factory believes runs are scoped.
  The umbrella refuses that render, and the `maku` chart admits `gitSecret` only with `demoValues`.
- **An agent merge (until H3):** the implementer and tester keep `contents: write`, which can merge
  a pull request whose required checks are green, between the gate's `success` and the factory's
  own merge. Closed at H3.
- **The factory pod holds the agents App key** (T15's row extends to it): whoever controls the
  factory can mint any role's token on any repository the App is installed on.

### A sponsored principal's bounds

A sponsored principal ([factory](sponsored-principals.md)) is a ZITADEL machine user the
config binds to a maintainer. Every bound below is enforced server-side, and none comes from a token
claim except the client that issued it.

| Bound | What holds it |
|---|---|
| Identity | The token's `client_id` equals the principal's `clientIDFile`, read at each use, and its audience holds the project (empty or missing: refused). The client must be a machine client: every subject of it is `system:<name>`. Never the web UI's or `maku`'s client |
| Sponsor | A maintainer of the repository, from the config (or the `onBehalfOf` link map), checked at load and again at each request; recorded as `spec.source.sponsor` |
| Day | `dailyUSD`, held by the run-time reservation under `system:<name>`, enforced whatever `enforcePrincipal` says. The intake's check is advisory. A principal removed from the config is no longer held, and its tasks escalate `principal_unknown` |
| Ceiling | `max_cost` is mandatory and at most `maxCostUSD`; the task is capped at it and each run at a third, by the meter |
| Open tasks | `maxOpenTasks`, a soft cap (a burst of N can open cap+N-1) |
| Repositories | Its `repositories` list, exact spelling, and only ones this factory owns |
| Reads | `/mcp` only, `handoff`, `task`, `inbox` and `room_tail`, and a room only as one of its `watcher` members |
| Never | `/v1/runs`, a task note, `room_fork`, approvals, class, tier or auto-merge. Its text is machine-sent, redacted and stored as untrusted |

## TLS on :8443

The bridge-to-broker hop serves TLS on every cluster, whatever the CNI encrypts: a cluster whose Cilium
has no WireGuard (for example, the reference platform's GCP cluster) leaves pod traffic in clear, and
one configuration for every cluster is simpler than two. The manifests live in your GitOps repository.

| Piece | Setting |
|---|---|
| Certificate | cert-manager `Certificate room-broker-tls` in `agent-system`, issuer `ClusterIssuer openbao` (the platform's private CA) |
| Names | `room-broker.agent-system.svc.cluster.local`, `room-broker.agent-system.svc` |
| Lifetime | `duration: 720h`, `renewBefore: 240h` |
| Broker | Mounts the Secret at `/etc/room-broker/tls/{tls.crt,tls.key}`; `ListenAndServeTLS` with a `GetCertificate` that re-reads the pair when the files change, so a renewal needs no restart. A plain-HTTP request fails |
| CA distribution | An ExternalSecret `room-broker-ca` in `agents` copies the platform CA chain, key `ca.crt` (not from `agents-secrets`: that SecretStore is namespaced to `agent-system` and cannot serve `agents`) |
| Bridge | Mounts `room-broker-ca` read-only at `/etc/room-broker-ca`; loads `$BROKER_CA_FILE` (default `/etc/room-broker-ca/ca.crt`) as its only root. It refuses to start if the file is missing or `BROKER_URL` is not `https://` |
| Operators | Call `:8443` with `https://` and `--cacert` on the same CA |

The broker forwards each human's ZITADEL bearer token to the factory's `:8082` on a run request,
so that hop is TLS too once the factory chart sets `api.tls.secretName`: the same reloading loader
(`internal/tlsreload`), TLS 1.3 minimum, the broker trusting `factoryCAFile`. The broker refuses
an `http://` `factoryURL` at start unless `factoryInsecure: true` says the operator accepts
plaintext, the same rule as the bridge's `BROKER_URL`.

`:8080` stays behind oauth2-proxy and `:8090` behind the `agent-router` Gateway, unchanged.

## Redaction

Every payload is redacted **in the broker, before it is appended**: every string of the JSON
document, at any depth, object keys included: an env dump puts secrets in keys. NUL characters
are stripped first, since `jsonb` refuses them. Two keys that are one once redacted cannot keep
both values, so that item is stored as a `key_collision` stub. It uses gitleaks' `detect` package with its
default rule set. A match becomes `[REDACTED:<rule>]`, the rule id is listed in the event's
`redactions`, and `maku_rooms_redactions_total{rule}` counts it. Broker logs carry envelope metadata only,
never payloads (`internal/redact`).

A unit test pins the four rules the design names:

| Rule | Catches |
|---|---|
| `github-app-token` | `ghs_…` installation tokens, as the minter mints them |
| `github-pat` | `ghp_…` personal access tokens |
| `jwt` | ServiceAccount and ZITADEL tokens |
| `private-key` | PEM private keys |

**Limits.** Treat redaction as a safety net, not a guarantee.

| Limit | Consequence |
|---|---|
| Each string is scanned alone | A secret split across two fields, two array items or two events is not detected. Writers must not split tokens |
| Only known shapes | A secret gitleaks has no rule for is stored as written (T8's residual) |
| After the fact for the harness | The harness saw the secret; redaction protects the log and its readers, not the run |
| An oversize payload | Replaced by a stub, so its content never reaches the log at all |
| Broker-origin text from a claim | The one free text, a revocation annotation used as an end reason, is redacted like a payload, then cut to 64 bytes |
| gitleaks' `gitleaks:allow` marker | Ignored: a line carrying it is redacted like any other, because the marker is harness content too |

Human actions run every payload they append through the same redactor, a take's reason included,
and a queued message's row keeps the redacted text the next run's brief quotes. The action on an
approval card is redacted the same way. The harness also redacts GitHub tokens from its own step log
before printing.

## Database roles

| Role | Can | Cannot |
|---|---|---|
| `rooms_owner` | Own and migrate the schema | Create roles |
| `rooms_broker` | Append and read events; insert room rows; update only the `rooms` columns it must move | Update or delete events; unseal, re-date or re-time a room; move `last_seq` by anything but +1 |
| `rooms_retention` | Find sealed rooms closed past their retention, then delete their events and rows | Read any event column but `room_id`, or any `rooms` column but the four that decide expiry (column grants); see or delete a room that is not expired, or its events (row-level security) |

Details and the tests that prove them: [room log](room-log.md#the-guarantees).

## The model gateway's identity

The model gateway ([gateway](../gateway.md)) verifies each caller's projected ServiceAccount token against
the cluster's issuer keys. Where those keys come from changes what the cluster exposes:

| `identity.jwks.source` | The keys | What it widens |
|---|---|---|
| `url` | the issuer's public JWKS URL (GKE, EKS) | nothing in the cluster |
| `apiserver` | the in-cluster `/openid/v1/jwks`, fetched by agentgateway's controller with no credential | `allowAnonymousDiscovery: true` binds `system:service-account-issuer-discovery` to `system:unauthenticated`: any unauthenticated client that reaches the apiserver can read the discovery document and the public keys. On k3s, the apiserver must also run with `anonymous-auth=true`, which lets every unauthenticated request reach authorization, where only the bindings that name `system:unauthenticated` admit it |
| `inline` | the keyset in the values | nothing, but the value must be updated when the issuer rotates its keys, or new tokens get 401 |

The keys are public, and the binding is Kubernetes' documented setting for OIDC discovery. Where the
apiserver is reachable beyond the cluster, prefer `url` or `inline`.

To keep `apiserver` but narrow it, Kubernetes 1.32 and later accept an `AuthenticationConfiguration`
(`--authentication-config`) whose `anonymous.conditions` admit anonymous requests on named paths only:
list `/.well-known/openid-configuration` and `/openid/v1/jwks`, and leave `--anonymous-auth` unset.
Every other anonymous request is then refused at authentication, before any binding is consulted.

## Network policies and modes

These policies live in your GitOps repository. Default deny everywhere; one allow per flow.

| Endpoint | Ingress | Egress |
|---|---|---|
| `room-broker` | Run pods in `agents` (label `agents.ogenki.io/run-id`) and the factory on 8443; oauth2-proxy on 8080; `agent-router` proxies on 8090; `vmagent` and kubelet on 9090 | DNS with an L7 rule; the Kubernetes API; CNPG on 5432; the run issuer's JWKS host on 443; the identity provider; `api.github.com` on 443; the factory's run API on 8082 |
| Run pod | kubelet on the bridge's 8085 | The broker on 8443, only when `roomRef` is set (the run's own CNP) |
| CNPG `xplane-rooms` | The broker and the retention job on 5432; the Atlas and CNPG operators; `vmagent` on 9187 | DNS, the Kubernetes API, peers, the backup plugin, object storage |
| Retention job | None | DNS; CNPG on 5432 |

Where the identity provider sits behind the cluster's own gateway (for example, on the reference
platform's GCP cluster), its rule is `toEntities: [all]` without ports: a port-scoped rule to that gateway hairpins through the socket load balancer and is dropped.

### The run path's network modes

With the run controller, the `maku` chart's `networkPolicies.mode` decides what confines a run:

| Mode | What holds a run | Residual risk |
|---|---|---|
| `cilium` | A CiliumNetworkPolicy per run: DNS for its own names only, GitHub and its egress allow-lists on 443 by name, the model, the broker, and the OTLP collector's pods on their port when run tracing is on | None this table adds |
| `kubernetes` | A plain NetworkPolicy per run: the cluster DNS on 53, the egress proxy on 3128, the broker on 8443 (and the room tools port) when the run has a room, an in-cluster model's namespace on its port, and the OTLP collector's pods on their port when [run tracing](../observability.md#what-tracing-opens-on-the-network) is on. The proxy holds the run's HTTPS to its own hosts | DNS to the cluster DNS stays open, so DNS tunnelling is possible. `cilium` mode, which filters DNS names, closes it |
| `none` | Nothing | Everything: a run reaches any address. The install banner says so (`network_policy_none`) |

In `kubernetes` mode the runs namespace is default-deny in both directions, and the broker, the run
controller and the egress proxy each have their own default-deny NetworkPolicy with one allow per
flow. The factory's and the Slack App's egress policies ship in `networkPolicies.factory` and `maku-slack` (v0.48.0+).

**The egress proxy** is a CONNECT-only forward proxy: `runController.image` run with
`egress-proxy`, in the release namespace (only the run controller may create pods in the runs
namespace). Its guarantees:

- **CONNECT only.** Any other method gets 405.
- **A client is its source pod IP.** The proxy watches the run controller's pods in the runs
  namespace and maps an IP to exactly one unfinished pod. It refuses an unknown IP, a terminating
  pod's, and an IP two pods hold.
- **The allow-list is the pod's `maku.ogenki.io/egress` annotation**, `host:port` pairs the run
  controller writes at create: GitHub, the run's egress allow-lists and the model, the set the CNP
  opens in `cilium` mode. The run-pod policy refuses any change to it after create. The proxy allows
  a `host:port` only on an exact match.
- **The install set closes before the agent starts.** A check-enabled run's pod also carries
  `maku.ogenki.io/egress-install` (GitHub's release hosts) and a deadline,
  `maku.ogenki.io/egress-install-until`. The proxy allows those hosts until the deadline, or until
  the pod's own harness sends `POST /v1/close-install` with its pod UID, whichever comes first. The
  proxy records the close on the pod as `maku.ogenki.io/egress-install-closed: "true"`, so every
  replica keeps it, across restarts. A pod can close only its own set, and nothing reopens one: the
  run-pod policy refuses a change to the set or its deadline after create, refuses removing the
  closed annotation, and lets the proxy's ServiceAccount change that annotation and nothing else.
  A tunnel to an install host ends within a second of the close or the deadline. The harness checks
  the close with a CONNECT to `install-check.maku.invalid:443`, which the proxy never dials: 409
  while the set is open, 403 (`install_check`) once it is closed.
- **No private address.** It refuses an IP literal, and a name that resolves to any loopback,
  private, link-local, CGNAT, NAT64 (`64:ff9b::/96`), 6to4 (`2002::/16`), IPv4-compatible (`::/96`)
  or `privateCIDRs` address. It dials the address it checked, never the name again. Its own
  NetworkPolicy opens only 443 (and `egressProxy.extraPorts`) outside `privateCIDRs`.
- **Bounded.** Each replica holds at most `egressProxy.maxTunnels` tunnels (1024) and
  `maxTunnelsPerClient` (64) from one run pod; a CONNECT over either gets 429. A tunnel that moves no
  byte for 5 minutes is closed.
- **Every decision is logged**, `allow run=<id> <host:port>` or `refuse run=<id|-> <host:port>`,
  with `phase=install|agent` (install while the run's install set is open) and, on a refusal, a
  `reason`. It is counted in `maku_egress_proxy_connections_total{decision}`, and each refusal in
  `maku_egress_proxy_refusals_total{reason}`; `install_closed` raises
  `MakuEgressInstallClosedRefusal`.
- **Its trust root is the run-pod policy**, which alone lets the run controller write the
  annotation: `networkPolicies.mode kubernetes` with `runPodPolicy.enabled: false` fails the render.

Only the harness gets `HTTPS_PROXY`, with `NO_PROXY=127.0.0.1,localhost,.svc.<cluster domain>`: the
bridge talks in the cluster only. A run takes no ingress: NetworkPolicy always admits node-local
traffic, so the kubelet's probe of the bridge needs no rule.

**The preflight refuses runs on a CNI that does not enforce NetworkPolicy.** A plain NetworkPolicy
on such a CNI is silently ignored. In `kubernetes` mode the run controller proves enforcement
before any run starts ([the preflight](../operations/failures.md#the-run-controllers-preflight)). Two of its
negative controls (the apiserver, and `github.com:443` dialled past the proxy) are denied only by the
run's own egress rules, so a CNI that enforces ingress alone fails it too.

Residual risks of `kubernetes` mode:

- **DNS stays open** to the cluster DNS, so DNS tunnelling is possible; `cilium` mode closes it.
- **An empty `networkPolicies.kubernetes.apiServerCIDRs` opens `apiServerPorts` (443 and 6443) to
  any address** for the broker, the run controller and the egress proxy: their rule reaching the
  apiserver cannot name it otherwise. For the proxy that includes private addresses on 443, which its
  dialer still refuses. Set it to the apiserver's addresses after service translation (`kubectl get
  endpointslice -n default -l kubernetes.io/service-name=kubernetes`). It then closes every other 443
  for the broker too, so its other 443 flows (the run issuer's JWKS, the identity provider) need
  `broker.networkPolicy.extraEgress`. The chart does not look the addresses up: a `lookup` renders
  nothing under `helm template` and GitOps.
- **One node.** Where the apiserver runs on the very node a run lands on (a single-node k3s, kind or
  a laptop), the apiserver control is a dial to that node's own address. kube-router on a one-node
  k3s denies it (the demo-e2e `kubernetes` leg). A CNI that admits pod-to-own-node traffic whatever
  the policy would fail the preflight there although nothing else is open; the probe's `github.com`
  control still tells an ingress-only CNI apart, but the network check cannot be skipped: choose
  another mode on such a CNI.

### The run's model credential

The run controller hands each run one of two model credentials (`runController.model.mode`,
[the model gateway](../gateway.md#runs-through-the-gateway)):

| Mode | In the sandbox | Its residual |
|---|---|---|
| `secret` (demo-only: `demoValues`, on the banner as `model_key_in_sandbox`) | the provider key from `agent-model`, a file the harness reads | a key with no expiry, valid at the provider for anything, readable by every command the model runs |
| `gateway` (default) | the run's own projected ServiceAccount tokens: one for `<prefix>.<dataClass>.<spec.model>`, and one for the judge's alias with a judge | a stolen token is valid until it expires (the run's deadline plus 10 minutes), only at the gateway, only on its alias and data class; its spend is metered to its run |

In `gateway` mode no provider key enters the runs namespace. The gateway holds the keys, verifies each token offline,
and sends the provider its own key, never the token. The token is not rotated: the harness reads it
once, and gVisor raises no inotify event for a rotated file. So it lives the pod's deadline plus 10
minutes. The run controller's preflight (`gateway-token-ttl`) refuses gateway runs when the apiserver
caps token lifetimes under the longest run's. That check needs `create` on `serviceaccounts/token`
for the run controller's own ServiceAccount, by name, and nothing else. Its token is bound to the
controller's own pod (`POD_NAME` and `POD_UID` from the downward API), so the apiserver stops
accepting it when that pod goes. Its audience, `<prefix>.preflight`, is one that no gateway route and
no run admits. The run-pod policy adds two rules in this mode:
- a projected token's audience is `room-broker`, `room-tools`, `<prefix>.public.<alias>` or
  `<prefix>.internal.<alias>`, never the apiserver's, the broker's client audiences or another service's;
- no pod reads `modelSecret`: no Secret volume, projected source, `env` `secretKeyRef` or `envFrom` in
  any container or init container.

The run controller's Role no longer reads that Secret.

## The run pod's admission

With the run-controller, the `maku` chart holds the runs namespace with in-tree admission,
and needs no controller:

- **The run-pod policy** (`maku-<namespace>-run-pods`, a ValidatingAdmissionPolicy) admits a pod there only when:
  - the run-controller created it;
  - it runs under the sandbox RuntimeClass (unless `sandbox.unsafeAllowRunc`);
  - it has a restricted securityContext, emptyDir, configMap, secret or projected volumes (no hostPath), and CPU and memory limits;
  - its images come from the allow-list.

  The policy refuses ephemeral containers (`kubectl debug`), and a change to the
  `maku.ogenki.io/egress` annotation.
- **Pod Security `restricted`**, set on the namespace when the chart creates it, holds what the
  policy does not: hostPort, the SELinux type, AppArmor, sysctls and procMount.
- **The image allow-list is per repository, not per digest.** It is `runController.images`,
  `runController.image` and `runPodPolicy.extraImages`, matched on the repository without tag or
  registry normalisation. The policy also requires an `@sha256:` reference (unless
  `e2e.unpinnedImages`), but any digest of an allow-listed repository passes. Whoever can push to
  those repositories can run code in a run pod; the image signature, not the policy, ties a digest
  to a release.
- **A ResourceQuota `runs`** caps pods, CPU and memory as a safety net. A run over it waits.
- **Reviewer and triager runs hold a read-only token.** Each run mounts its own token, which the
  factory mints for its role from the role table. The one exception is a demo's laptop PAT,
  `runController.gitSecret` (`demoValues` only), which every role shares and the install banner
  shows.

## Internal principals and publishing

An internal principal is an authentication surface: a token whose client is declared in
`api.internalPrincipals` is a sponsored principal that may also carry internal data, and nothing else.
`POST /v1/runs` refuses it as it refuses any sponsored token. Its request refuses a `title`, an
`onBehalfOf`, a `ref`, public data, a repository or ceiling outside its entry, and a body past its
`minConfidence`, `dailyTasks`, `maxOpenTasks` and day. The investigate template is forced by the
principal, so alert text it sends reaches a public implementer only through a maintainer. A sponsored
principal is never allowed `internal`, and its request refuses an `incident`.

`factory/publish` is the one place internal text becomes public, and a maintainer's label starts it.
The proposal is the triager run's own handoff, never another writer's. It is held to the issue
template, made inert (`sanitize.Public`: no link, raw HTML, image or hidden character survives; URLs,
addresses, @mentions and issue or commit references are kept in code spans, so none pings or links),
scanned by the redactor, then rendered through GitHub's `POST /markdown`. The redactor reads the
draft before it is made inert as well as after, because a code span splits a token so that no rule
matches it. A secret refuses it as `redacted`, and nothing is sent to be rendered. Text the last scan cannot prove inert, or a page holding any anchor or mention, issue or
commit link, refuses it as `live_reference`. Each publishes nothing, and so does a render that
fails: the label then waits for the next poll, with no refusal. The scan does not use the parser the rewrite uses, and the render is
GitHub's own reading, so a misparse must pass both to reach a reader. The
factory files it once, with no trigger label, and records it on the task. An origin is that record, never
issue text. The fix reads the public issue only: the investigate task's internal text never reaches it.

With `publishStarts` the label is also the money-spending decision: the fix starts at once, and `requestedBy` and the charge are the
publisher's. The start skips intake's advisory day check, so the reservation at queueing binds: a person whose day is spent ends the
Task `Escalated` with `budget-person`, and a later `factory/ready` on the issue supersedes it. Only the fleet's own limit waits as
`waiting_daily_budget`. The label's actor is checked as for `factory/ready`: a maintainer with write access.

## Webhooks

An event carries ids and closed codes only, never room text: `reason`, `limit`, `state` and `verdict` are
clipped to fixed lists, and any other value is sent as `other`. A message is never decoded to build
an event.

A subscriber is `cluster` or `internet`, declared in the broker's config and never by a room. Cluster
subscribers are reached by their service name, which needs an egress allow on the broker's host
policy and the subscriber's own ingress allow: neither is implied. An internet
subscriber must be a public DNS name over `https`, and the dial guard checks every address after
resolution, so a name that rebinds is refused at connect. The guard refuses loopback, private,
link-local (the cloud metadata address included), CGNAT (the tailnet), unique-local, unspecified,
multicast and reserved addresses, and the NAT64 and 6to4 forms that embed an IPv4 address. No proxy
is used, which would dial on the broker's behalf past the guard, and redirects are not followed,
since the address they name was never declared.

Each delivery is signed with HMAC-SHA256 under a key of at least 32 bytes that lives in a mounted
secret, never in the config. The key file is re-read at each delivery.

## The Slack App

The factory's Slack App (`cmd/maku-slack`, [docs/slack.md](../slack.md)) is the only component with a public route: `POST /slack/events`, matched exactly. The invariants, each held by a named test:

| # | Invariant | Test |
|---|---|---|
| 5 | A bad Slack signature or a stale timestamp is `401` before the body is parsed. The signature is `v0=` + HMAC-SHA256 over `v0:<timestamp>:<raw body>`, compared in constant time, within 5 minutes, on a body capped at 1 MiB | `TestSlackSignature` |
| 6 | A check mark from an unlinked user or a non-maintainer files nothing | `TestReactionFlow` |
| 7 | No Slack message carries room text: thread lines are templates of ids, states, closed codes and the room's URL | `TestThreadMessagesCarryNoText` |
| 8 | No tool and no Slack *reply* approves or steers: the App reads no reply in a thread as a command, and no sponsored tool decides. The one Slack path that decides is a signed **button** on a broker-posted card, for `forge.push` only, resolved to the linked human's room standing and repository write, four-eyes included, and recorded as that human's decision | `TestSlackNeverDecidesOnAThreadReply`, `TestSponsoredToolList`, `TestSlackDecidesForgePushOnly` |

- **One class, one surface.** A card's buttons call the broker's `POST /api/slack/decisions` ([api](api.md#post-apislackdecisions)), which exists only when the broker sets `human.slackDecide` and takes the App's sponsored principal alone. The class is the constant `forge.push`: every other class keeps the room page, whatever the clicker's standing. The click asserts no identity — the Slack user resolves through `slack_links` to a ZITADEL account, its *current* GitHub login, and that login must hold two standings at once: an approver or owner of the room (from the Room CR only, never from a ZITADEL group), and write on the room's repository. The room's four-eyes rule refuses the person who prompted the run, the branch in the body must be the run's own (`binding_mismatch` otherwise), and the reason is empty by construction: a press carries a decision and no sentence a model could be steered by. The room log records a human, never the App: `actor_id=human:<sub>`, with the surface in the idempotency scope (`origin_client=human:<sub>:slack:<approvalId>`). The store's single-decision fence is unchanged, so the card is a view and never the fence.
- **A press is answered in a closed vocabulary, and never with detail.** Every refusal is a `200` with one code, which the App turns into one fixed sentence for the clicker alone ([Slack](../slack.md#the-card)): no repository, branch, GitHub login, approver's name or count of approvers. The card path's codes, beside the two decisions:

  | `result` | What it means |
  |---|---|
  | `approved`, `denied` | The decision was recorded by this press |
  | `already_decided` | Someone closed the approval first, or this same press replayed; nothing was appended |
  | `approval_expired` | The deadline had passed |
  | `not_linked` | The Slack user has no link, or the account has no GitHub identity |
  | `link_unverified` | the broker's links or GitHub could not be asked, and it fails closed |
  | `not_permitted` | Not an approver or owner of this room, or the room is admins-only |
  | `repository_permission` | The resolved login cannot write the room's repository |
  | `four_eyes` | This human prompted the run, and the room sets four eyes |
  | `binding_mismatch` | The branch is not the run's, or the approval binds none |
  | `not_supported` | The class is not `forge.push` |
  | `not_found` | No such approval in the room log |
  | `sealed` | The room is sealed |

- **One workspace.** `teamID` is required; an event from any other workspace is dropped.
- **A check mark files only when** the reacting user is linked, in the factory's `onBehalfOf.slack` or by a self-serve link, the message is the bot's own draft (its user is the bot's, and its metadata hash matches), and the channel is configured. A second check mark on the same draft is a duplicate and files nothing.
- **A self-serve link is proved on both sides** ([design and threat model](../superpowers/specs/2026-10-28-slack-self-serve-link-design.md)): Slack proves the Slack user (a signed mention, answered ephemerally), ZITADEL the account (the rooms web UI's session only, never a maku or MCP token, and a same-origin POST). A link never moves to another account silently. The code is single use, 10 minutes, stored as its sha256 only, for the App's one workspace. The record names a ZITADEL account, read by `system:factory` alone; the factory resolves the account's current GitHub login itself and still holds it to the maintainers, and the Filed line names the sponsor. The App can mint codes and unlink, never create or read a link.
- **Secrets stay in the App.** The bot token and the client-credentials secret are mounted files, read at use. Neither reaches the broker or a run: the broker gets a ZITADEL access token, never Slack's, and a run never sees either.
- **The App's token** is a client-credentials grant with the project-audience scope, the same scope `maku` uses. The factory proves the principal itself from that token and the `on_behalf_of` it names.
- **A key problem is not a bad signature.** A webhook key the App cannot read answers `503 no_key`, counted apart from `bad_signature`. It is not a hold: the broker retries after 10s, 1m and 5m, then counts a failure, and three given-up deliveries disable the subscriber until its `generation` is bumped, so events are lost meanwhile ([recovery](../slack.md#state)).
- **Redelivery.** Slack events and broker webhooks are deduplicated in memory (one replica). Two concurrent redeliveries of one id can both pass the check before either finishes; the worst case is one duplicate thread line.

## maku on the developer's machine

`maku mcp` and `maku checkout` run as the developer, beside a local coding agent that room
text and a branch can try to steer ([maku](../cli.md#the-mcp-server)).

- **The start form.** The factory starts only from a form the model cannot answer, in `start` (and in `answer`, which can restart a task). `File this issue` defaults to no; `Start now` defaults to yes and is offered only where the caller writes to the repository and `human.mcp.start` allows it.
  The model can call the tool but cannot answer the form. The factory still takes only a
  maintainer's label, and the per-task, per-person and daily dollar caps bound the spend.
  **Accepted risk:** a client that auto-accepts forms (some headless modes) lets the agent start
  work. That is not new: the agent can already label with the developer's `gh`, which local-first
  calls "advice, not a control".
- **Checking out an agent's branch.** A sandboxed agent can commit agent or editor config,
  hooks or mise files that run when a tool opens the folder or when the developer commits.
  `room_checkout` and `maku checkout` therefore fetch only `agent/<room>` from an `origin`
  verified to be the room's repository, run git with an argument list and `core.hooksPath=/dev/null`
  (no hook runs during the fetch or the worktree creation), confine the new worktree beside the
  repository, refuse an existing path, and confirm the changed files that run code, in a form that
  defaults to no or with `y/N` on a terminal. Without either they refuse, naming the files.
  `maku checkout` asks on a terminal only. An agent whose shell runs in a pseudo-terminal can
  answer `y` itself; that agent can also run `git worktree add` directly, so the question is
  advice, not a control.
  The residual: a developer who confirms, then opens the folder or commits there, runs what the
  agent wrote. Read the listed files first.

## Supply chain

| Control | How |
|---|---|
| Pinned actions | Every `uses:` is pinned to a 40-character commit SHA, with the version in a comment; Dependabot keeps them current |
| Least privilege in CI | `permissions: {}` at the top of every workflow, granted per job; `persist-credentials: false` on every checkout; timeouts on every job |
| Static analysis | CodeQL (`security-extended`, Go) on every PR, on `main` and weekly; `golangci-lint` with `gosec`, `noctx`, `errorlint`, `bodyclose`, `sqlclosecheck` and more ([AGENTS.md](../../AGENTS.md)); no rule disabled in production code, while tests and `cmd/` carry scoped exclusions |
| Vulnerabilities | `govulncheck` in `task check`, so on every PR and before every release |
| Dependency drift | `go mod tidy -diff` in `task check`. Dependabot for actions, Go modules and the Docker base images (the harness's `agent-server` base excepted): weekly, a 7-day cooldown, majors ignored while the stack is open. The harness's Python pins (`requirements.in` and its compiled lock) have no pip ecosystem entry, so version bumps are by hand; security updates still arrive as Dependabot pip PRs (repository vulnerability alerts are on), and the base image is held back by its ignore in `.github/dependabot.yml` |
| Scorecard | OpenSSF Scorecard weekly on `main`, results published and uploaded to code scanning |
| Images | The Go images are static binaries on digest-pinned distroless `static-debian12:nonroot`; the agent harness is a Python image on the OpenHands `agent-server` base. All multi-arch (`linux/amd64`, `linux/arm64`). Never `latest`: consumers pin by digest |
| Attestations | An SPDX SBOM and SLSA provenance (`mode=max`) attached to every pushed image |
| Signing | A keyless cosign signature on the digest, never the tag, from the workflow's OIDC identity |
| Releases | The release job re-runs `task check` (a tag can name a commit that never passed CI) and uses no build cache |
| Repository | `main` is protected by the repository ruleset `main`: pull requests only (squash, the only merge method the repository enables), required checks `check`, `analyze` and `harness-test`, linear history, no force-push or deletion; admins may bypass, through a pull request only. Private vulnerability reporting and Dependabot security updates are on |

Verify an image before you pin it. **The default accepts only images built by the release workflow
from a `v*` tag:**

```bash
IMAGE=ghcr.io/smana/maku-broker@sha256:<digest>

cosign verify "$IMAGE" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/Smana/(agent-platform|maku)/\.github/workflows/release\.yaml@refs/tags/v'

docker buildx imagetools inspect "$IMAGE" --format '{{ json .SBOM }}'        # SPDX SBOM per platform
docker buildx imagetools inspect "$IMAGE" --format '{{ json .Provenance }}'  # SLSA provenance
```

Releases and pre-releases before v0.30.0 carry the `Smana/agent-platform` identity, so each regexp
accepts both repository names.

**PR pre-releases are not release-grade: they run unreviewed branch code. Use them for integration
testing only.** To verify one, accept the PR workflow instead:

```bash
cosign verify "$IMAGE" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/Smana/(agent-platform|maku)/\.github/workflows/ci\.yaml@'
```

## Untrusted-input hardening stages and residuals

This release runs it in **audit** only ([Hardening, audit](../hardening.md#hardening-audit)):

- **What runs.** The checks run on the writes a run already made, as `output_shadowed` records.
- **What is recorded.** Each task records the lowest author trust its runs could read.
- **What changes for a run.** Nothing is refused or withheld. The canary line at the end of a
  writer run's brief is the only change a run sees.

Readiness stays `false` until `mode: enforce` (below). The shadow pass's log lines
and metrics carry the layer, a bounded reason and the GitHub object, never the written text.

Residuals while in audit:

- The run's own token can still write anything its role policy allows, and T13's filter is
  still the only control on what it reads.
- A run's outputs are checked against that run's canary only: an older run's canary quoted from
  the room is not an alert.

A repository listed in `enforceOutputs` goes one stage further
([Hardening, non-code outputs enforced](../hardening.md#hardening-non-code-outputs-enforced)):

- **What is enforced.** Its writer runs request `pr`, `comment`, `labels` and `issue` through
  `output_*` tools. The factory applies each one after T1 to T4 at enforce, or refuses it with a
  bounded reason.
- **Whose voice.** Agent text is published as the agents App, with a token the factory mints from
  the agents App with the role table's publisher row (`hardening.publisher.minter`), never as the
  factory App. Only `issue` is filed by the factory App, as `factory/proposed`.
- **What is checked after.** At a run's end the tracer reads back the task's pull request (its
  title and body), the agents App's comments on the task's issue and pull request, and the labels
  it added there. Each must be exactly what an `output_applied` published. A pull request's
  footer must name the newest applied `pr` output, and its title (blanks around it aside) and
  body must be that output's. A comment must end with an applied output's marker and hold that
  output's text. A direct open, or a direct edit of an applied object, is reported
  `untraced_write` and checked as in audit.
- **Human edits.** An applied body that only people edited since its answer is logged
  `human_edit` and not counted untraced, but its text is still checked as in audit. The tracer
  reads every body edit's editor from GraphQL `userContentEdits`, since REST names none. Every
  edit in that window must be by a `User` other than the agents App. One agents-App, bot or
  deleted editor, or an edit list past its page cap, leaves the write untraced. So does any run
  path but cloud-native-ref's XR: there a run writes with a shared token, and GitHub credits its
  direct edits to that token's user.
- **What the applier refuses.** It refuses to edit an open pull request whose footer names none of
  the task's applied outputs (`output_target`), so a direct open is never adopted.
- **What closes the bypass.** The sandbox role's `pull_requests: write` is removed in the
  repository's role policy. The permission gauge shows it.

Residuals with non-code outputs enforced:

- `push` writes from the sandbox unless the repository enforces it (below), and the run still
  reads raw text with `gh` (H4).
- Until the role policy drops `pull_requests: write`, the run's token can still write directly,
  with `gh api`, curl, or gh with a token from the credential helper. The harness's `gh` wrapper
  turns the obvious commands away, but it is advisory. The tracer reports what it observes, and
  does not stop it.
- The tracer does not observe:
  - reviews and review comments;
  - comments and labels on any thread but the task's issue and pull request;
  - label removals;
  - a closed or reopened pull request or issue;
  - an edit made and reverted between two reads.

  Only the role policy covers these.
- The publisher and the sandbox roles share the agents App's login, so GitHub's author alone
  cannot tell an applied write from a direct one. The tracer's comparison with the factory's
  record does, for what it observes.
- Agents-App issues are read by author since the run started, so two concurrent runs of the
  agents App each count the other's issues. On a repository that enforces `issue`, the tracer can
  over-count them as untraced.

A repository whose `enforceOutputs` names `push` goes one stage further
([Hardening, push enforced](../hardening.md#hardening-push-enforced)):

- **What is enforced.** The harness turns the run's `git push` into a git bundle, requested
  through the harness-only `output_push`. The factory inspects the bundle outside the sandbox, in
  a bare, blobless mirror with fsck on, no hooks, an https-only transport list, a timeout and an
  output cap per call, and the publisher's token only in git's environment. It checks one ref
  (`agent/<task>`), fast-forward or rebase onto `main`, the tree (no submodule, escaping
  symlink, `.git` path or binary file), each new blob's size, the class caps, then T1 to T4.
- **Who pushes.** The publisher, with `--force-with-lease`. A protected path waits for a
  maintainer's approval bound to the bundle's digest, decided on the room page or the Slack card
  by someone with repository write.
- **What is checked after.** Each agents-App push to the task branch must match an applied
  push's head, or it is `untraced_write`.
- **What closes the bypass.** The implementer's and tester's tokens lose `contents: write`: the
  role table does it for per-run tokens. This also closes the
  agent-merge residual above.

Residuals with push enforced:

- The `git` shim is advisory. Until the role policy drops `contents: write`, the run can still
  push directly, and the tracer reports it without stopping it.
- An approved push that changes `.github/workflows/**` is refused `output_unavailable` unless the
  publisher's policy holds `workflows: write`.
- Blobs the remote already holds (a change's old side, or content reverted to it) are not sized
  before the diff reads them. `core.bigFileThreshold` (1 MiB) turns a large one into a binary
  change, which is refused.
- A push whose reply is lost is answered by its recorded intent on the task
  (`agents.ogenki.io/push-intent`). The record holds the task's latest push only.
- Enforcement needs `runTokens.perRun`, so the role table is what removes the write; the
  permission gauge shows it.

A repository at `mode: enforce` goes one stage further
([Hardening, reads filtered](../hardening.md#hardening-reads-filtered)):

- **What is withheld.** The run reads its issue, pull request and checks through `forge_read`. The
  factory reads them with its App and withholds text below the repository's `minAuthorTrust`
  before any model reads it. Withheld text never leaves the factory: not in the answer, not in a
  log line, not in the `input_read` record.
- **What the sandbox loses.** `api.github.com` egress: the run-controller drops it from the run's
  CNP and the egress proxy's allow-list. `git` keeps `github.com`. The `gh` wrapper's refusals
  are advisory: the egress is what holds.
- **Where it holds.** With all five output types enforced.

Residuals with reads filtered:

- `github.com` serves the issue's HTML page, which `git`'s egress reaches.
- An approved author's text is delivered: an injection from a maintainer or a collaborator is
  not withheld.
- A run-controller older than the factory ignores `forge-api: deny`. The chart pins both to one
  version.

A repository is **ready** when all seven readiness checks hold ([Hardening, ready](../hardening.md#hardening-ready)), and
only then does coordination's execute admit it. Residuals once ready:

- Readiness is read at each admission and every five minutes, not continuously: a Secret swapped
  to a write token on the run-controller is seen at the next read, not at the swap.
- The `drill_stale_or_failed` check proves the configuration a drill ran under. A drill scenario a role never ran fails the drill,
  so a suite narrowed by configuration cannot pass.
- T5 is not a readiness fact, and a promoted type's T5 is single-turn: split or encoded injections
  pass it, as the drills measure. T5 never reads a T3 refusal, so it adds nothing where a secret
  or the canary was already caught.
