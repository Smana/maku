# v4 rollout step 6, exposure, components and observability: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a platform team exposes Agent Factory through the Gateway or Ingress it already runs. It installs only the components it wants, and watches the install with the Prometheus stack it already has. cloud-native-ref (cnref) then consumes the umbrella chart one component at a time, so ai-native-ref (ANR) shrinks to values (D22).
- **D19, exposure.** `expose.mode` is `httproute`, `ingress` or `none` (the default). There are two surfaces:
  - `web`: the broker's human listener, behind the web front door. It serves the rooms UI, `/api`, `/v1/ws`, `/mcp`, `/.well-known/roomctl` and `/link/*`.
  - `slack`: `POST /slack/events` only.

  Each surface has its own hostnames, and its own parentRefs or ingress class. So the webhook can be public while the web stays private. The user's gateway or ingress terminates TLS, and the chart issues no certificate. The web front door (oauth2-proxy) is rendered from the broker's `human` keys.
- **D20, components.**
  - The core is always installed: the factory, the broker and the run-controller.
  - The Slack App becomes the subchart `charts/maku-slack`, aliased `slack` and off by default (`slack.enabled: false`).
  - RunLore stays outside the chart. Its MCP endpoint is one broker value, `maku.broker.roomTools.knowledge.url`, which gives triager runs `kb_search` and `kb_get`.
- **D21, observability.**
  - Always on:
    - `/metrics` on every component. This is already true; the inventory is EX12;
    - OTLP traces to a configurable endpoint. The factory already has this; this step adds it for runs.
  - Opt-in:
    - ServiceMonitors and PodMonitors;
    - a PrometheusRule with the product alerts, ported from ANR's VMRules;
    - Grafana dashboards as ConfigMaps with the sidecar label.
- **`profile: production` renders.** Step 1's PR3 left it failing "until steps 2 to 6". It now checks the seams of each enabled component, and fails with every missing seam named. This is what lets cnref adopt the umbrella one component at a time (D22, D25).
- **Owed by step 2 (its PR13):** default-deny egress policies for the factory and the Slack App.

Every existing install keeps working (D25):
- These are the defaults:
  - `expose.mode: none`;
  - `webProxy.enabled: false`;
  - `slack.enabled: false`;
  - every `monitoring.*.enabled: false`;
  - no `runController.tracing.otlpEndpoint`;
  - no `broker.roomTools.knowledge`.

  The umbrella's demo render and the subcharts' default renders are byte-identical.
- `maku-factory.config.tracing.otlpEndpoint` keeps its meaning (gRPC `host:port`).
- The broker's verbatim `human` block is unchanged. The new broker keys (`webhooks`, `suspend`, `policyManagement`, `coordination`) render nothing when unset.
- ANR's hand-written broker, Slack App, HTTPRoutes, VMRules, scrapes and dashboards keep working until each ANR follow-up flips them.

**Architecture:**
- **The umbrella owns exposure**, because only it sees every component:
  - `templates/expose.yaml` renders one HTTPRoute or Ingress per enabled surface. Each targets a fixed Service name: `rooms-web-proxy` for the web, `factory-slack-app` for Slack.
  - `templates/web-proxy.yaml` renders oauth2-proxy (Deployment, Service and policies), with its arguments derived from `maku.broker.human`. The parent can read a subchart's values, but cannot compute them (EX8).
- **`charts/maku-slack`** is a new subchart:
  - a single-replica Deployment, its Service, ServiceAccount and Role;
  - the thread ConfigMap, the config ConfigMap and its policies.

  The umbrella checks that the broker and the factory agree with it (its webhook subscriber, its sponsored principal). The umbrella fails the render when they disagree, in the house style of `afumbrella.gateway`.
- **The maku chart gains parity keys** that cnref's broker uses and the chart cannot render today:
  - `broker.webhooks` (with a Secret per subscriber, mounted for it, and the egress it needs);
  - `broker.suspend`;
  - `broker.policyManagement`;
  - `broker.coordination`.
- **Monitoring is one umbrella block (`monitoring`)**:
  - ServiceMonitors for the broker, the factory, the Slack App and the web proxy;
  - PodMonitors for the run-controller, the egress proxy and the model-gateway proxy;
  - one PrometheusRule with groups per component, each group rendered only when its component is;
  - one dashboard ConfigMap per dashboard.

  Rules and dashboards live as plain files (`files/rules/*.yaml`, `files/dashboards/*.json`) with `__PLACEHOLDER__` tokens. They are read with `.Files.Get` and `replace`, never `tpl`, so `{{ $labels.x }}` passes through and promtool tests the rendered output.
- **Traces:** the run-controller gives each run `OTEL_EXPORTER_OTLP_ENDPOINT` and `TRACEPARENT`, the latter from the AgentRun's `agents.ogenki.io/traceparent` annotation. When the endpoint is set, the run's policy opens egress to the collector. The factory's task spans are unchanged.
- **RunLore:** the broker's `:8090` room tools gain two pass-through tools, `kb_search` and `kb_get`, for the triager role. The broker forwards them to the configured MCP endpoint. Runs keep one MCP server, the broker, and D6's run egress is unchanged.
- **The production gate** is `afumbrella.production`, beside `afumbrella.profile`. It lists every missing seam of every enabled component in one failure.

**Tech Stack:**
- Gateway API v1 `HTTPRoute` (standard channel, v1.2 or later, as step 4 requires): `RequestHeaderModifier`, `Exact` path plus method match, `timeouts`;
- `networking.k8s.io/v1` Ingress;
- oauth2-proxy v7, the version ANR's chart 10.7.1 runs, pinned by digest;
- prometheus-operator `monitoring.coreos.com/v1` (`ServiceMonitor`, `PodMonitor`, `PrometheusRule`);
- the Grafana sidecar convention (`grafana_dashboard: "1"`, as the vendored agentgateway chart uses);
- promtool 3.15.0, ANR's pin;
- Go: the existing OTel SDK (`otlptracegrpc` in the factory), controller-runtime, and `internal/mcpwire`;
- Python: the harness's existing OTLP/HTTP exporter (no change);
- Helm 3.15, JSON Schema, the bash render tests (`yq`);
- k3d with Traefik (Ingress), agentgateway as the Gateway API implementation (HTTPRoute), and the e2e stub as the OIDC issuer, Slack signer and OTLP/HTTP sink.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D6 (default-deny for every component; what runs may reach);
- D14 (Secrets by name);
- D19 (exposure);
- D20 (components);
- D21 (observability);
- D22 (the ANR shrink);
- D25 (additive);
- D26 (support floor);
- "Rollout", step 6;
- the 2026-11-12 owner rulings: PR9 (the unsafe settings stay a banner) and Release (a minor is cut per step, after the step's last PR).

**Read against:**
- Maku `origin/main` `be93df29`. Merged:
  - steps 1 and 2 (PR A to C);
  - step 3 (PR A to E);
  - step 4 PR A to C;
  - hardening H1 to H3c.

  Not merged: step 4 PR D and all of step 5. PR D (web front door) and PR G (production gate) below name the step 5 seams they read.
- ANR `origin/main` `1529b3b`.

**Branch:** seven PRs, each from `origin/main` once its dependencies merge.

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, traces end to end (D21) | `feat/v4-obs-traces` | 1–3 | M | any time |
| B, scrape objects and alerts (D21) | `feat/v4-obs-alerts` | 4–6 | M | any time |
| C, dashboards (D21) | `feat/v4-obs-dashboards` | 7–8 | L | after B (it extends B's `monitoring` block) |
| D, exposure and the web front door (D19) | `feat/v4-expose` | 9–12 | L | after step 5 PR B (it reads `human.preset`, `rolesClaim`, `scopes`, `broker.oidc`) |
| E, the Slack subchart, broker parity, component egress (D20, D6) | `feat/v4-slack-subchart` | 13–16 | L | after D (its route is D's `slack` surface) |
| F, RunLore's MCP endpoint (D20) | `feat/v4-runlore-mcp` | 17–18 | M | any time |
| G, `profile: production` renders | `feat/v4-profile-production` | 19–21 | M | last; needs D, E and step 5 PR D (DSN Secrets) |

The step's v0.4x minor is cut after PR G. A, B, C and F may also ship in interim minors.

## Global Constraints

- **No v4.** This step ships v0.4x minors only: no v4 tag, release or chart version.
- **Additive (D25).**
  - Every new behaviour is opt-in:
    - umbrella: `expose.mode`, `webProxy.enabled`, `slack.enabled`, `monitoring.serviceMonitor.enabled`, `monitoring.prometheusRule.enabled`, `monitoring.dashboards.enabled`, `networkPolicies.factory`, `networkPolicies.slack`;
    - maku chart: `runController.tracing.otlpEndpoint`, `broker.roomTools.knowledge`, `broker.webhooks`, `broker.suspend`, `broker.policyManagement`, `broker.coordination`.
  - Default renders stay byte-identical. Prove it with a render diff against `origin/main` for:
    - the umbrella demo;
    - the maku quickstart;
    - `runController.enabled=false`;
    - `maku-factory`'s default;
    - `maku-gateway`'s minimal values;
    - `agent-factory-crds`.
  - Old keys keep working. Where an old and a new key conflict, the render fails with a message naming both.
- **Fail closed.**
  - An enabled surface with no hostnames fails the render.
  - `expose.web` fails the render in two cases:
    - in local mode, where the broker binds loopback;
    - in oidc mode without `webProxy.enabled`, because the broker expects oauth2-proxy's headers.
  - The Slack route matches `POST /slack/events` exactly and nothing else. Every other path on that host is the gateway's 404.
  - A client never brings its own identity headers. The web route strips them (HTTPRoute), and oauth2-proxy strips them again (`skip-auth-strip-headers`, proved in Task 9). Under step 5's `tokenUse: id` the access header is not parsed, so this strip is what keeps P18.
  - Monitoring objects render only when asked: no `.Capabilities` guessing, so offline renders (Flux, Argo CD, `helm template`) are deterministic.
  - A knowledge call that fails is a tool error, never a partial answer.
  - The production gate names every missing seam in one message.
- **No credential in a log, a metric, an error or a rendered value.** The oauth2-proxy client and cookie secrets, the Slack secrets and the webhook keys are Secrets by name only (D14).
- **Bounded vocabularies.** `maku_rooms_knowledge_calls_total{tool, outcome}` has closed sets. Every alert carries `runbook_url` and `dashboard` annotations into Maku's docs (ANR's M9 rule, moved here).
- **PromQL, not MetricsQL.** The chart's rules and dashboards run on Prometheus, and a test refuses `increase_pure`, `tlast_over_time`, `label_set` and every other MetricsQL-only function.
- **Strict decoding.** The broker, run-controller and Slack App configs are decoded strictly, so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until the release. Name this in each PR body.
- **Maku house rules:**
  - the SPDX line on every Go file;
  - stdlib `testing`, table-driven;
  - conventional commits in English, with no attribution lines or trailers;
  - golangci-lint v2's full set;
  - no error string ends in punctuation.
- **This machine:**
  - wrap every test run in `systemd-run --user --scope -q -p MemoryMax=6G`;
  - never set `HOME=` or use `env -i`;
  - `internal/store` needs Docker.
- **Task commands:**
  - `task chart:check` after a chart change. It gains `charts/maku-slack` and `charts/agent-factory/tests/monitoring.sh`, which needs promtool: add `promtool = "3.15.0"` to `mise.toml`;
  - `task harness:test` is not needed: the harness already reads both variables;
  - `task check` before the last commit of each PR.

## Rulings made while planning

- **EX1 (what exists today; this step builds only what is missing).**

  | Spec item | Today on `origin/main` (Maku) and on cnref (ANR) | Missing, built here |
  |---|---|---|
  | **D19: the web** | The broker's human listener is `:8080` (`charts/maku/templates/broker-service.yaml`, port `human`, not rendered in local mode). It serves the UI (`/`, `/r/{id}`, `/assets`), `/api/*`, `/v1/ws`, `POST /mcp` (the `maku mcp` and Slack App bearer), `/.well-known/roomctl`, `/link/slack` (`internal/humanapi/server.go:242–282`, `slacklink.go:247`), and from step 5, `/link/github/*`. No Maku chart renders an Ingress, an HTTPRoute or oauth2-proxy. On cnref: `infrastructure/base/room-broker/httproute.yaml` (host `rooms.${private_domain_name}`, `platform-tailscale-general`, `RequestHeaderModifier` removing eight identity headers, `timeouts.request: 0s` for WebSockets) → `oauth2-proxy.yaml` (HelmRelease `rooms-oauth2-proxy`, chart 10.7.1, ZITADEL-shaped args) → `room-broker:8080` | `expose`, `webProxy` (Tasks 10–12) |
  | **D19: Slack** | `maku-slack` listens on `:8080` (`POST /slack/events`, events and interactivity on one door), `:8081` (`POST /webhooks`, cluster only) and `:9090` (`internal/app/slackapp.go:116–153`). On cnref: `tooling/base/agent-factory/slack-app/httproute.yaml` (host `factory-slack.${public_domain_name}`, `platform-public`/`factory-slack`, `Exact /slack/events`, `POST`) | The `slack` surface (Task 15) |
  | **D19: GitHub webhooks** | None exist. The factory polls (`internal/factory/intake/issues.go:3–6`: "the factory polls for labels and exposes no public endpoint"). The demo App is created with "no webhook events" (`docs/cli.md:49`) | Nothing. EX3 amends the spec |
  | **The factory's ports** | `:8082` the run API, called only by the broker (`docs/architecture.md:480`); `:8080` `POST /intake/runlore` (RunLore, in-cluster); `:9090` ops. On cnref: `tooling/base/agent-factory/httproute.yaml` routes `POST /v1/runs` on `factory.${private_domain_name}` (tailnet), and no Maku client calls it | Nothing exposed (EX3) |
  | **D20: the Slack App's packaging** | An image only (`images/maku-slack`), no chart. On cnref it is 14 hand-written files in `tooling/base/agent-factory/slack-app`: Deployment (1 replica, R26), Service, ServiceAccount, Role (`get`/`update` on `factory-slack-threads`), the thread ConfigMap (Flux SSA `IfNotPresent`), config (configMapGenerator), CNP, HTTPRoute, VMServiceScrape, four ExternalSecrets and a Password generator. The broker side lives in `infrastructure/*/room-broker/config.yaml`: `webhooks[slack-app]`, `human.mcp.sponsored[slack-app]`, `slackLink` and `slackDecide`. The factory side lives in `tooling/*/agent-factory/helm-values-sponsored.yaml`: `config.api.sponsored[slack-app]` | `charts/maku-slack` and the agreements (Tasks 14–15) |
  | **D20: RunLore** | Three integrations, all optional: <ul><li>RunLore pushes findings to the factory (`POST /intake/runlore`, `runlore.*` config, or as an internal principal);</li><li>it subscribes to the broker's webhooks (`webhooks[runlore]`);</li><li>its knowledge MCP (`kb_search`, `kb_get`) reaches XR triager runs through ANR's agentgateway MCP federation (`infrastructure/base/agent-gateway/mcp.yaml`, target `runlore-kb`).</li></ul> Run-controller runs have one MCP server, the broker's `:8090`, so they have no knowledge tools. The investigate prompt already says "if knowledge-base tools such as kb_search and kb_get are available" (`internal/factory/reconciler/text.go:192`) | The knowledge pass-through (Tasks 17–18) |
  | **D21: metrics** | `/metrics` is on `:9090` everywhere: the broker (`ops`), the factory (`ops`), the Slack App, the run-controller and the egress proxy. Families: `maku_rooms_*`, `rooms`, `maku_intake_*`, `maku_needs_*`, `maku_intent_*`, `maku_slack_*`, `maku_revision_*` (broker); `maku_factory_*` (factory); `factory_slack_*` (App); `maku_run_controller_preflight_passed` plus controller-runtime's (run-controller); `maku_egress_proxy_*` (proxy). The run-controller has no Service, and the egress proxy's Service has no metrics port. `networkPolicies.kubernetes.metricsFrom` opens `:9090` to a scraper for the broker, the run-controller and the proxy. On cnref, three `VMServiceScrape`s (broker, factory, App) | ServiceMonitors and PodMonitors (Task 4) |
  | **D21: traces** | The factory exports task spans over OTLP/gRPC to `tracing.otlpEndpoint` (`internal/factory/tracing/tracing.go:118–130`), and stamps each AgentRun with `agents.ogenki.io/traceparent` (`internal/factory/runs/runs.go:280`). The harness exports its run span to `OTEL_EXPORTER_OTLP_ENDPOINT` (OTLP/HTTP) under `TRACEPARENT` (`images/maku-harness/agent_run.py:206–245`). The run-controller sets neither (`internal/runctl/pod.go:119–261`), so run-controller runs have no traces. The broker, the run-controller and the Slack App emit no spans. On cnref, `agent-traces-collector` (`observability/base/agent-platform`) | The run's two variables and its collector egress (Tasks 1–2) |
  | **D21: alerts** | None in Maku. `docs/operations.md` documents ANR's (`## Alerts`, `### Factory alerts`), including three that no rule file ships: `FactoryAgentMerged`, `FactoryMergeGateNotRequired` and `FactoryMergeGateRulesUnread` (step 3). On cnref: `vmrule.yaml`, `vmrule-rooms.yaml`, `vmrule-agent-factory.yaml` and `vmrule-logs.yaml`, about 60 rules, with `namespace="agent-system"`, `xplane-run-` and `${…}` substitutions hard-coded, and one `increase_pure`. They are tested by `promtool` scripts under `scripts/ci/tests/` | The PrometheusRule (Tasks 5–6, EX13) |
  | **D21: dashboards** | None in Maku. On cnref, seven `GrafanaDashboard` CRs with inline JSON (grafana-operator, folder `agents`): overview, factory, room, run, identity, fleet and gateway. They use 64 `increase_pure`, 21 `tlast_over_time` and 2 `label_set` (MetricsQL), plus VictoriaLogs and VictoriaTraces panels and Karpenter panels | Dashboard ConfigMaps (Tasks 7–8, EX14) |
  | **D6: component egress** | Step 2 gave `kubernetes`-mode policies to the runs, the run-controller, the egress proxy and the broker. The factory has only the umbrella's demo ingress policy (`networkPolicies.plain`). The Slack App has no Maku policy. On cnref, Cilium CNPs in `tooling/base/agent-factory/network-policy.yaml` and `slack-app/network-policy.yaml` | Default-deny for the factory, the App and the web proxy (Task 16) |
  | **D22: cnref's broker** | A Crossplane `App` XR (`infrastructure/base/room-broker/app.yaml`), not the maku chart. Its config uses `webhooks`, `suspend` and `policyManagement`, which `charts/maku/templates/broker-config.yaml` cannot render | The broker parity keys (Task 13) |
  | **Step 1 PR3** | `profile: production` fails with "its seams arrive in rollout steps 2 to 6" (`charts/agent-factory/templates/_helpers.tpl`) | The production gate (Tasks 19–21) |

- **EX2 (the `expose` shape).**

  ```yaml
  expose:
    mode: none                # none | httproute | ingress
    httproute:
      parentRefs: []          # [{name, namespace, sectionName}]: every surface's, unless it sets its own
      timeouts: {}            # rendered on the web route only when set, e.g. {request: 0s} (WebSockets on Envoy)
    ingress:
      className: ""
      annotations: {}         # every surface's; e.g. cert-manager.io/cluster-issuer (optional: the chart issues nothing)
    web:
      enabled: true           # with mode != none; the human listener through webProxy
      hostnames: []
      parentRefs: []          # httproute: replaces expose.httproute.parentRefs for this surface
      className: ""           # ingress: replaces expose.ingress.className
      annotations: {}         # ingress: merged over expose.ingress.annotations (WebSocket timeouts are the controller's)
      tls: []                 # ingress only: [{secretName, hosts}], the user's Secret
    slack:                    # rendered only with slack.enabled
      hostnames: []
      parentRefs: []
      className: ""
      annotations: {}
      tls: []
    networkPolicy:
      from: []                # kubernetes mode: NetworkPolicy peers of the gateway's or ingress controller's pods
      cilium:
        fromEntities: [ingress]   # cilium mode: Cilium's Gateway and Ingress identity
  ```

  - **Per-surface attachment is the point.** cnref attaches the web to its tailnet Gateway and the Slack events to its public one.
  - **HTTPRoute, web:**
    - `PathPrefix /`;
    - a `RequestHeaderModifier` removing `Authorization`-adjacent identity headers: `X-Forwarded-Access-Token`, `X-Forwarded-User`, `X-Forwarded-Email`, `X-Forwarded-Groups`, `X-Forwarded-Preferred-Username`, `X-Auth-Request-*`;
    - the backend `rooms-web-proxy:80`;
    - `timeouts` when set.
  - **HTTPRoute, Slack:** `Exact /slack/events` with `method: POST`, to `factory-slack-app:8080`.
  - **Ingress, web:** `Prefix /`. Ingress has no portable header removal, so the strip rests on oauth2-proxy (Task 9 proves it). The docs say so.
  - **Ingress, Slack:** `pathType: Exact` `/slack/events`. Ingress has no method match, and the App answers anything but `POST` with 405.
  - **Rejected:**
    - **Routing the web straight to the broker** (`backend: broker`): it would let a client forge the access header, which breaks P18. A team with its own front door sets `expose.web.enabled: false` and routes it itself, and the docs show how.
    - **Rendering the Gateway itself:** the Gateway belongs to the platform. A per-surface parentRef is the seam.

- **EX3 (what must be public: the Slack events path only).**
  - GitHub needs no public path: the factory polls (EX1). The spec's "the webhook paths (GitHub, Slack)" gets a one-line amendment in PR D's commit.
  - Step 5's `/link/github/callback` is a browser redirect, not a call from GitHub. It rides the web host, which may stay private.
  - The factory's `:8082` (broker only) and `:8080` (RunLore, in-cluster) are never exposed. ANR's `factory.${private_domain_name}` route goes in follow-up 3.
  - `docs/exposure.md` gives one table: each surface, its callers, whether it must be public, and its path.

- **EX4 (the web front door is an umbrella template, not a vendored chart).**
  - **Why not a subchart:** step 5's PR11 said "subchart". But the arguments must be derived from `maku.broker.human` (issuer, preset, roles claim, scopes, the CLI client), and Helm cannot compute a subchart's values from the parent's. A vendored oauth2-proxy chart would make the user restate them. The umbrella reads `.Values.maku.broker.human` directly and renders its own Deployment, Service, PodDisruptionBudget and policies, with the image pinned by digest.
  - **Values:**

    ```yaml
    webProxy:
      enabled: false
      image: quay.io/oauth2-proxy/oauth2-proxy:<v7.x>@sha256:<digest>   # Task 9 records ANR's
      replicas: 2
      existingSecret: ""     # keys below; D14
      keys: {clientID: client-id, clientSecret: client-secret, cookieSecret: cookie-secret}
      cookieRefresh: 1h      # under the ID token's lifetime (step 5 PR3)
      metrics: {port: 44180}
      extraArgs: {}          # appended; a key the chart derives may not be overridden (render fails naming it)
      resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {cpu: 200m, memory: 128Mi}}
    ```

  - **Derived arguments:**
    - `provider=oidc`, `oidc-issuer-url` = `human.issuer`;
    - `redirect-url` = `<broker.publicURL>/oauth2/callback`;
    - `upstream` = `http://room-broker.<ns>.svc.<domain>:8080`;
    - `pass-authorization-header`, `pass-access-token`, `skip-auth-strip-headers=true`, `pass-user-headers=false`, `pass-basic-auth=false`, `set-xauthrequest=false`;
    - `cookie-secure`, `cookie-samesite=strict`, `cookie-refresh`, `reverse-proxy`, `skip-provider-button`;
    - `skip-jwt-bearer-tokens` with `OAUTH2_PROXY_EXTRA_JWT_ISSUERS=<issuer>=<cli client id>`. The CLI id is read from step 5's `broker.oidc.existingSecret` key `cli-client-id` through `$(VAR)` expansion;
    - `skip-auth-route`: `GET=^/\.well-known/roomctl$`, plus `GET=^/link/github/callback$` when `human.githubLink` is set;
    - `metrics-address=:44180`.
  - **Per preset:**
    - `zitadel`: the scope `openid profile email offline_access urn:zitadel:iam:org:project:id:$(PROJECT_ID):aud`, with `project-id` from the OIDC Secret;
    - `generic`: `openid profile email offline_access` plus `human.scopes`.
  - **Groups:** `oidc-groups-claim` and `allowed-group` (`groups.admin,groups.member`) are set only when the roles claim is a top-level name that Task 9 shows oauth2-proxy can read. The broker stays the authority, because it refuses a token in no agents group. The proxy's check is defense in depth.
  - **The broker's ingress on `:8080`** opens to the web proxy pods (kubernetes and cilium mode), in place of today's hand-written `extraIngress`.

- **EX5 (TLS is the user's).** The chart renders no Certificate and no Issuer for any exposed surface.
  - **`httproute`:** TLS is the Gateway listener's.
  - **`ingress`:** `tls` names the user's Secret, and `annotations` may carry `cert-manager.io/cluster-issuer`.
  - **In-cluster:** web proxy → broker and gateway → App are plain HTTP inside default-deny (D19).
  - **Unchanged:** the broker's own `:8443` certificate (`broker.tls`) is the run bridge's, not an exposed surface.

- **EX6 (the network of exposed pods).** The umbrella reads `.Values.maku.networkPolicies.mode` (`kubernetes`, `cilium` or none) and renders the matching objects for the web proxy and the Slack App:
  - **kubernetes:** NetworkPolicies. Ingress comes from `expose.networkPolicy.from` on the surface's port, and from `maku.networkPolicies.kubernetes.metricsFrom` on the metrics port.
  - **cilium:** CNPs with `fromEntities` from `expose.networkPolicy.cilium.fromEntities`.
  - **Refused:** `expose.mode` other than `none` in kubernetes mode with an empty `from` fails the render, because default-deny would drop every request: "expose.networkPolicy.from is empty: name the pods of your gateway or ingress controller".
  - **Demo only:** the umbrella's `networkPolicies.plain` stays the demo's, and production refuses it (Task 19).

- **EX7 (the Slack subchart; what off removes).**
  - **The subchart:** `charts/maku-slack`, an umbrella dependency with `alias: slack` and `condition: slack.enabled`. So the spec's `slack.enabled: false` is literally the key. It is published by the release workflow like `maku-gateway`.
  - **What it renders:**
    - the Deployment `factory-slack-app` (1 replica, R26), the Service (`events` 8080, `webhooks` 8081, `metrics` 9090), the ServiceAccount, and the Role and RoleBinding (`get`, `update` on `factory-slack-threads` only);
    - the ConfigMap `factory-slack-threads` (EX10), and the config ConfigMap from `config` (teamID, channels, proposals: verbatim) plus the derived file paths and URLs;
    - its policies (EX6, Task 16).
  - **Secrets by name (D14):**
    - `slackSecret`: `signing-secret`, `bot-token`, `bot-user-id`;
    - `principalSecret`: `client-id`, `client-secret`, and `project-id` under the zitadel preset;
    - `webhookSecret`: `signing-key`.

    The names default to cnref's, so ANR's ExternalSecrets keep working.
  - **Off (the default) removes all of the above**, the `slack` exposure surface and the Slack alert group. Without it the factory loses:
    - mention intake and proposals;
    - thread lines;
    - `forge.push` cards;
    - the `@factory link` code.

    MCP, the CLI and the web UI are unaffected (D20). The broker's `slackLink`, `slackDecide` and `slackIntents` are broker features that stay as configured; nothing calls them without the App.
  - **What fails the render:** `slack.enabled: false` with a `maku.broker.webhooks` entry whose URL is the App's Service, because every event would end in a disabled subscriber and `RoomWebhookDisabled`.

- **EX8 (agreement, not derivation).** Helm cannot set a subchart's values from the parent. So, as `afumbrella.gateway` does, the umbrella checks that its own and each subchart's values agree. With `slack.enabled`, the render fails naming both keys unless:
  - `maku.broker.webhooks` has an entry with `name: <slack.principal>` (default `slack-app`), `network: cluster`, `url: http://factory-slack-app.<ns>.svc.<clusterDomain>:8081/webhooks`, and `existingSecret` equal to `slack.webhookSecret`;
  - `maku.broker.human.mcp.sponsored` and `maku-factory.config.api.sponsored` both name `<slack.principal>`;
  - `slack.config.brokerURL`, when set, is the broker's `:8080` Service URL.

  `values-slack.example.yaml` shows the four blocks together.

- **EX9 (broker parity keys).** These are rendered verbatim into the broker config, so the strict decoder stays the validator:
  - `broker.suspend`, `broker.policyManagement` and `broker.coordination`;
  - `broker.webhooks`. Each entry may add `existingSecret` and `key` (default `signing-key`). The chart then mounts that Secret at `/etc/room-broker/webhooks/<name>/` and fills `secretFile`; a set `secretFile` wins.

  Each `network: cluster` entry may add `networkPolicy.to` (peers). The chart renders the broker's egress to the URL's port on those peers, or on the App's pods for the Slack entry. A `network: internet` entry opens 443 to public addresses, excluding `privateCIDRs`, as the egress proxy does. This retires ANR's `gen-webhook-egress.py`.

- **EX10 (the thread map survives Helm and drift detection).**
  - The App writes `factory-slack-threads`. The chart renders it with no `data`, and with `helm.sh/resource-policy: keep`. A Helm upgrade does not touch `data`, because the three-way merge has no `data` to patch.
  - Flux's `driftDetection: enabled` (cnref's HelmReleases) would reset it. `charts/maku-slack/README.md` and `docs/slack.md` give the `driftDetection.ignore` rule (`/data`, that ConfigMap) and Argo CD's `ignoreDifferences`. Task 14's render test asserts the template has no `data` key.

- **EX11 (RunLore's MCP endpoint: a broker pass-through, not gateway federation).**
  - **The value:**

    ```yaml
    broker:
      roomTools:
        knowledge:
          url: ""               # RunLore's MCP endpoint, e.g. http://runlore.runlore.svc.cluster.local:8080/mcp
          tools: [kb_search, kb_get]   # a subset of these two; nothing else is forwarded
          roles: [triager]
          timeout: 10s
          maxBytes: 65536
          networkPolicy: {to: []}      # peers for the broker's egress (kubernetes mode)
    ```

  - **How a call goes:**
    - the broker lists the configured tools for the listed roles;
    - it forwards `tools/call` to the endpoint over MCP Streamable HTTP: one stateless `initialize` per call, no session, no run token forwarded;
    - it clips the answer to `maxBytes` and returns it as data.
  - **The URL:** https is required unless the host is an in-cluster `<svc>.<ns>.svc[.<domain>]`, the model URL's rule.
  - **The counter:** `maku_rooms_knowledge_calls_total{tool, outcome}`, with outcomes `ok`, `clipped`, `refused` (role), `timeout`, `upstream_error` and `bad_answer`.
  - **The bridge:** the run-controller always lists `room-broker__kb_search,room-broker__kb_get` in `MCP_READ_ONLY_TOOLS`, which is harmless when the tools are absent.
  - **Why the broker:**
    - D6 lets runs reach only DNS, agentgateway, the egress proxy and the broker's room tools, and this keeps it so;
    - the harness has one MCP entry (`MCP_URL`);
    - the broker already scopes tools by role (`internal/mcp/tools.go`, `Roles`).
  - **Rejected:** MCP federation in `maku-gateway`, which is ANR's shape for XR runs. Step 4 deferred it, it needs the gateway path for room tools (`roomTools.auth: key`), and it is larger than one value.

- **EX12 (the metrics inventory and the scrape objects).**

  | Component | Port | Service with a metrics port | Object (`monitoring.serviceMonitor.enabled`) |
  |---|---|---|---|
  | broker `room-broker` | `ops` 9090 | `room-broker:metrics` | ServiceMonitor |
  | factory `agent-factory` | `ops` 9090 | `agent-factory:metrics` | ServiceMonitor |
  | Slack App `factory-slack-app` | 9090 | `factory-slack-app:metrics` (Task 14) | ServiceMonitor, with `slack.enabled` |
  | web proxy `rooms-web-proxy` | 44180 | `rooms-web-proxy:metrics` (Task 12) | ServiceMonitor, with `webProxy.enabled` |
  | run-controller | `metrics` 9090 | none | PodMonitor |
  | egress proxy | `metrics` 9090 | none (its Service is the proxy's) | PodMonitor, in kubernetes mode |
  | model-gateway proxy | 15020 | none | PodMonitor on `gateway.networking.k8s.io/gateway-name: <maku-gateway.name>`, with `maku-gateway.enabled` |
  | agentgateway controller | its chart's | its chart's | `agentgateway.monitoring.enabled` (vendored; NOTES says to set it) |

  - **Why PodMonitors:** they add no Service, so default renders stay byte-identical. The VictoriaMetrics operator converts PodMonitors as it converts ServiceMonitors (D21).
  - **Refused:** `monitoring.serviceMonitor.enabled` in kubernetes mode with an empty `maku.networkPolicies.kubernetes.metricsFrom` fails the render ("the scraper could not reach :9090"). The new components' policies (Tasks 12, 14, 16) read the same `metricsFrom`.
  - **Left alone:** `factory_slack_*` keeps its name. Renaming it is out of scope, and noted under "After this step".

- **EX13 (the PrometheusRule: what moves from ANR, what stays, what goes).**
  - **The values:**

    ```yaml
    monitoring:
      serviceMonitor: {enabled: false, interval: 30s, labels: {}}
      prometheusRule:
        enabled: false
        labels: {}
        disabled: []            # alert names to leave out (an install's own variant lives elsewhere)
        kubeStateMetrics: false # the rules that read kube_* series
        docsURL: ""             # empty: https://github.com/Smana/maku/blob/v<appVersion>/docs
      dashboards: {enabled: false, labels: {grafana_dashboard: "1"}, annotations: {}}
    ```

  - **The placeholders in `files/rules/*.yaml`:** `__NAMESPACE__`, `__RUNS_NAMESPACE__`, `__RUN_SUBJECT__` (`system:serviceaccount:<runs ns>:<namePrefix>`), `__HUMAN_ISSUER__`, `__GATEWAY_NAME__` and `__DOCS__`.
  - **One group per component**, each rendered only with it:

    | Group | Rules (ANR's name kept unless noted) | Source |
    |---|---|---|
    | `maku-rooms` | `RoomBrokerDown` (`up{job="room-broker",namespace=…}`), `RoomLogAppendErrors`, `RoomRedactionsSpike`, `RoomStalled`, `RoomBridgeHarnessStalled`, `RoomBridgeItemsRefused`, `RoomBridgeItemsUnstorable`, `RoomAuthnJWKSStale`, `RoomAuthnHumanIssuerNeverFetched` (issuer from `maku.broker.human.issuer`), `RoomRejectedActionsSpike`, `RoomVerdictsNotReachingGitHub`, `RoomApprovalPendingTooLong`, `RoomWebhookDisabled`; new: `RoomKnowledgeFailing` (with `knowledge.url`) | `vmrule-rooms.yaml`, `vmrule-agent-factory.yaml` |
    | `maku-factory` | `AgentFactoryIntakeErrors`, `AgentFactoryAutoMergeReverted`, `AgentFactoryMergeRegression`, `AgentFactoryClassPausedLong` (any class, not cnref's two), `AgentFactoryDailyBudgetHigh`, `AgentPersonCapShadowRefusal`, `AgentFactoryTaskStuck`, `AgentFactoryKillSwitchEngaged`, `FactoryGitHubUnreachable`, `AgentFactoryMeterBlind`, `AgentFactoryRepositoryNotOwned`, the four `AgentFactoryCoordination*`, `AgentModelUnpriced`, `AgentMeterInputExcludesCache`; and, first shipped here, `FactoryAgentMerged`, `FactoryMergeGateNotRequired`, `FactoryMergeGateRulesUnread` (as `docs/operations.md` states them) | `vmrule-agent-factory.yaml`, `docs/operations.md` |
    | `maku-run-path` | new: `RunControllerPreflightFailing` (`min by (check) (maku_run_controller_preflight_passed) == 0`, 10 min), `RunControllerDown`, `EgressProxyDown` (kubernetes mode); with `kubeStateMetrics`: `AgentSandboxPodPending` on `__RUNS_NAMESPACE__` | `vmrule.yaml` (generalized) |
    | `maku-slack` | `SlackDecisionsSpike`, `SlackCardUpdatesFailing` | `vmrule-agent-factory.yaml` |
    | `maku-gateway` | `AgentGateway5xxRatioHigh`, `AgentGatewayModelErrorRatioHigh`, `AgentGatewayUnauthorizedBurst`, `AgentRouterMetricContractBroken` (step 4's promise, on `__GATEWAY_NAME__`), `AgentGatewayCostUnpriced`, the record `agent_cost:gateway_run_usd` and `AgentCostCrossCheckMismatch` (on `__RUN_SUBJECT__`, not `xplane-run-`), all on agentgateway's native names (step 4 PR D) | `vmrule.yaml`, `vmrule-agent-factory.yaml` |

  - **Stays in ANR** (cnref's platform or thresholds):
    - `AgentGvisorPoolNearLimit` (Karpenter);
    - `AnthropicFleetNearCap`;
    - `AgentRateLimitDown`, the budget records, `AgentBudgetShadowOverrun` and `AgentBudgetRefused` (ANR's ratelimit and budget objects, which step 4 left ANR's);
    - the `agent-cache` group;
    - `AgentRunTokenSpendHigh` and `AgentFleetTokenSpendHigh`;
    - `RoomLogDiskFilling` (CNPG's PVC);
    - `vmrule-logs.yaml` (VictoriaLogs).
  - **Deleted with its component:** `PolicyBotUnavailable` (step 3's follow-up drops policy-bot).

- **EX14 (the dashboards).**
  - **What ships:** `files/dashboards/{agents-overview,agent-factory,agent-room,agent-run,agent-identity,agent-fleet}.json`, plus `agent-gateway.json` with `maku-gateway.enabled`. One ConfigMap per dashboard (`maku-dashboard-<uid>`), labelled with `monitoring.dashboards.labels` and annotated with `annotations` (for example `grafana_folder: Agents`), as the vendored agentgateway chart does.
  - **The uids stay ANR's**, so cnref's `human.dashboards` links and every cross-dashboard link keep resolving.
  - **The datasource** stays the `${datasource}` variable of type `prometheus`. In the files, ANR's `$${…}` Flux escapes become `${…}`.
  - **The PromQL translation:**
    - `increase_pure(x[r])` → `increase(x[r])`. ANR's own test already treats them as equal for series that start at 0;
    - `tlast_over_time(x[r])` → `max_over_time(timestamp(x)[r:1m])`;
    - `label_set` → `label_replace`;
    - `namespace="agent-system"` → `__NAMESPACE__`, and `xplane-run-` → `__RUN_SUBJECT__`.
  - **Dropped:** the VictoriaLogs and VictoriaTraces panels and the Karpenter and node panels. ANR keeps them in one cnref dashboard (follow-up 5).
  - **ConfigMaps only, as D21 says.** A grafana-operator user wraps each one with a `GrafanaDashboard` whose `configMapRef` names it, as cnref will.

- **EX15 (traces: who emits, and the one widening of run egress).**
  - **Emitters:**
    - the factory (task spans, unchanged);
    - each run's harness (its root span, parented on the task's `TRACEPARENT`).

    The broker, the run-controller and the Slack App emit no spans today, and D21 does not ask for new instrumentation ("Not here").
  - **The value:**

    ```yaml
    runController:
      tracing:
        otlpEndpoint: ""        # OTLP/HTTP base URL, e.g. http://collector.obs.svc.cluster.local:4318
        collector: {namespace: "", labels: {}}   # the collector's pods, for the runs' egress
    ```

    The run-controller then sets `OTEL_EXPORTER_OTLP_ENDPOINT` and, when the AgentRun's annotation is valid W3C, `TRACEPARENT`.
  - **The policies open the collector's port** to the runs: the runs-namespace NetworkPolicy in kubernetes mode, the per-run CNP in cilium mode. This is the only addition to D6's list, it exists only when configured, and the docs say a run can send its own data there. An endpoint without a collector selector fails the render. ANR's collector already admits runs.
  - **The factory's endpoint:** `maku-factory.config.tracing.otlpEndpoint` is unchanged (gRPC `host:port`). The factory's egress (Task 16) opens it from `networkPolicies.factory.otlp.to`.

- **EX16 (`profile: production`: a gate per enabled component).**
  - **`afumbrella.production` refuses:**
    - `demoValues`, `postgres.enabled` and `networkPolicies.plain` (demo-only);
    - an enabled component whose required seam is missing.
  - **The seams per component:**
    - `maku`:
      - pinned images;
      - `broker.human.mode: oidc` with an issuer;
      - `broker.database.brokerSecret` (step 5's DSN Secrets);
      - `broker.publicURL`;
      - a sandbox: `sandbox.runtimeClassName`, or `sandbox.unsafeAllowRunc`, which is a banner;
      - with `runController.gitTokens: perRun`, `maku-factory.runTokens.perRun` (step 3's rule).
    - `maku-factory`:
      - a pinned image;
      - the factory App's Secret;
      - `config.api` humans matching the broker's (step 5's rule).
    - `slack`: EX8's agreement and its three Secrets.
    - `webProxy`: `existingSecret`.
    - `expose`: EX2's rules.
  - **Unsafe settings are allowed.** `networkPolicies.mode` none, `factoryInsecure` and `unsafeAllowRunc` stay allowed in production and appear on the banner (owner ruling PR9).
  - **The message** lists every missing seam on its own line, with the key and the doc.
  - **Staged adoption:** `maku.enabled: false` with `maku-factory.enabled: true` is a valid production render. That is cnref's first follow-up: one component, one seam, at a time (D25).

- **EX17 (spec amendments, one line each, in the PR that makes them true):**
  - **D19 (PR D):** "only the Slack events path must be public; the factory polls GitHub and needs no public path".
  - **D20 (PR E):** "the minimal install is the three core Deployments plus Postgres and agentgateway; the egress proxy (D6) and the web front door (D19, oidc) come with their seams".
  - **D6 (PR A):** "and the OTLP collector, when tracing is configured".

---

## PR A: traces end to end (D21)

### Task 1: the run-controller hands each run its trace (M)

**Files:**
- Modify:
  - `internal/runctl/config.go`: `Tracing{OTLPEndpoint string; Collector struct{Namespace string; Labels map[string]string}}`. `Validate` requires an `http`/`https` URL with a host, and a collector selector when the endpoint is set;
  - `internal/runctl/pod.go`: in the harness env, `OTEL_EXPORTER_OTLP_ENDPOINT`, and `TRACEPARENT` from `runs.AnnTraceparent` when it matches the factory's W3C pattern (reuse `internal/factory/runs`' validator, exported). The bridge's `MCP_READ_ONLY_TOOLS` gains the two knowledge tools here, ahead of PR F (EX11);
  - `internal/runctl/netpol.go` and `internal/runctl/cnp.go`: an egress rule to the collector's port, with namespace and labels;
  - `internal/runctl/config_test.go`, `internal/runctl/objects_test.go`, `internal/runctl/netpol_test.go`, `internal/runctl/render_test.go`.

- [ ] **Step 1: failing tests:**
  - with no `tracing`, the pod, the NetworkPolicy and the CNP are byte-identical to today's fixtures;
  - with an endpoint, the harness has `OTEL_EXPORTER_OTLP_ENDPOINT` and, for an AgentRun annotated `00-<32 hex>-<16 hex>-01`, `TRACEPARENT`;
  - an all-zero or malformed annotation gives no `TRACEPARENT`, and the run still starts;
  - the run's NetworkPolicy (kubernetes mode) and CNP (cilium mode) allow the collector's port to its selector and nothing more;
  - an endpoint without `collector` fails `Validate` with `tracing.collector: name the collector's pods (namespace, labels) so the run's policy can reach it`;
  - `MCP_READ_ONLY_TOOLS` holds `room-broker__kb_search,room-broker__kb_get`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/runctl/ -run 'Trac|Pod|NetPol|CNP|Config'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): runs export their span to the configured OTLP endpoint, under the task's trace`.

### Task 2: the chart, the stub sink and the k3d proof (S)

**Files:**
- Modify:
  - `charts/maku/values.yaml`, `values.schema.json`, `templates/run-controller-config.yaml`, `tests/render.sh`: EX15's `runController.tracing`;
  - `charts/maku/templates/agents-namespace.yaml` (the runs namespace's kubernetes-mode egress);
  - `hack/e2e-stub/` (`otlp.go`: `POST /v1/traces`, OTLP/HTTP protobuf, records trace ids at `/test/traces`) and its test;
  - `charts/agent-factory/tests/install.sh` (with `TRACING=1`, set the stub as the endpoint and assert one run's trace id equals its task's);
  - `taskfile.yaml` (`chart:tracing:k3d`).

- [ ] **Step 1: failing render cases:**
  - default and quickstart renders are byte-identical;
  - `runController.tracing.otlpEndpoint` without `collector` fails with Task 1's message;
  - with both, the controller config carries them, and the runs namespace's policy opens the port to the selector.

  Run `bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement**, then run `task chart:tracing:k3d`. Expected: the stub records the harness span with the task's trace id.
- [ ] **Step 3: commit** `feat(charts): the runs' OTLP endpoint and its egress, proved on k3d`.

### Task 3: docs (S)

**Files:**
- Create: `docs/observability.md`. It holds traces (EX15: who emits, the two endpoints, the egress, what a run can send), and is the home that PRs B and C extend.
- Modify:
  - `docs/README.md`;
  - `docs/operations.md` (a pointer);
  - `docs/upgrading.md` (`runController.tracing`);
  - `docs/superpowers/specs/2026-11-12-v4-production-path-design.md` (EX17's D6 line).

- [ ] **Step 1: write them.** Run `task docs`. Expected: PASS.
- [ ] **Step 2: commit** `docs: traces end to end`. Then `task check`, and open PR A with EX15 and the nightly note in the body.

## PR B: scrape objects and alerts (D21)

### Task 4: `monitoring`: ServiceMonitors and PodMonitors (S)

**Files:**
- Create: `charts/agent-factory/templates/monitoring-scrape.yaml`.
- Modify:
  - `charts/agent-factory/values.yaml` (EX13's `monitoring` block, all off);
  - `values.schema.json`;
  - `templates/_helpers.tpl` (`afumbrella.monitoring`: EX12's `metricsFrom` rule);
  - `templates/NOTES.txt` (the agentgateway note);
  - `tests/render.sh`.

- [ ] **Step 1: failing render cases:**
  - off renders nothing (the demo golden is unchanged);
  - on, there are ServiceMonitors `room-broker` and `agent-factory`, and PodMonitors `maku-run-controller` and, in kubernetes mode, `maku-egress-proxy`, each on `port: metrics` (or the pod's `metrics` port) and `path: /metrics`, with `monitoring.serviceMonitor.labels` added;
  - `maku-gateway.enabled` adds the model-gateway PodMonitor on port 15020;
  - kubernetes mode with an empty `metricsFrom` fails with EX12's message.

  Run `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): opt-in ServiceMonitors and PodMonitors for every component`.

### Task 5: the PrometheusRule (M)

**Files:**
- Create:
  - `charts/agent-factory/files/rules/{rooms,factory,run-path,slack,gateway}.yaml` (EX13's groups, ported from ANR's VMRules at `1529b3b`, plus the step 3 alerts from `docs/operations.md`);
  - `charts/agent-factory/templates/monitoring-rules.yaml` (one `PrometheusRule`, `maku`, its groups read with `.Files.Get` and `replace`, each group gated on its component, then `disabled` applied by name).

  Every rule carries `runbook_url` (`__DOCS__/operations.md#<anchor>`) and `dashboard` (`/d/<uid>`).
- Modify: `charts/agent-factory/tests/render.sh`.

- [ ] **Step 1: failing render cases:**
  - off renders nothing;
  - on with the demo values, the groups are `maku-rooms`, `maku-factory` and `maku-run-path`;
  - `slack.enabled` (once PR E lands; until then, a fixture flag) adds `maku-slack`, and `maku-gateway.enabled` adds `maku-gateway`;
  - no rendered expression contains `agent-system`, `xplane-` or `${`;
  - `disabled: [AgentFactoryClassPausedLong]` removes exactly that rule;
  - every rule has both annotations;
  - `{{ $labels.room }}` survives the render byte for byte.

  Run `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the product alerts as an opt-in PrometheusRule`.

### Task 6: promtool tests and the alerts docs (M)

**Files:**
- Create:
  - `charts/agent-factory/tests/monitoring.sh`: it renders the rule with production-shaped values, extracts `.spec` to a rules file and runs `promtool check rules`, then `promtool test rules` on `tests/monitoring/*.test.yaml`;
  - `tests/monitoring/*.test.yaml`, ported from ANR's `test-room-webhook-alerts.sh`, `test-agent-factory-slack-alerts.sh`, `test-agent-coordination-alerts.sh`, `test-agent-factory-budget-alerts.sh`, `test-agent-merge-regression-alert.sh`, `test-agent-class-paused-alert.sh` and `test-agent-cost-crosscheck-alerts.sh`, with the same synthetic series and boundaries, plus one case per new rule;
  - `charts/agent-factory/tests/promql-only.sh`: it fails on any MetricsQL-only function in `files/`.
- Modify:
  - `mise.toml` (`promtool = "3.15.0"`);
  - `taskfile.yaml` (`chart:check` runs both scripts);
  - `docs/operations.md`: `## Alerts` and `### Factory alerts` describe the chart's groups, each alert's anchor, `monitoring.prometheusRule.*`, and "the VictoriaMetrics operator converts it";
  - `docs/observability.md` (scrape and rules);
  - `docs/integration.md:57`, `docs/architecture.md:464` (the observability row: the chart, not cnref);
  - `docs/upgrading.md`.

- [ ] **Step 1: run** `bash charts/agent-factory/tests/monitoring.sh`. Expected: FAIL until the tests are ported.
- [ ] **Step 2: port and fix.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `test(charts): every product alert evaluated by promtool`. Open PR B with EX12 and EX13 in the body.

## PR C: dashboards (D21)

### Task 7: the dashboards as ConfigMaps (L)

**Files:**
- Create:
  - `charts/agent-factory/files/dashboards/{agents-overview,agent-factory,agent-room,agent-run,agent-identity,agent-fleet,agent-gateway}.json`: ANR's JSON at `1529b3b`, translated per EX14;
  - `charts/agent-factory/templates/monitoring-dashboards.yaml`.
- Modify: `charts/agent-factory/tests/render.sh`.

- [ ] **Step 1: failing render cases:**
  - off renders nothing;
  - on, there are six ConfigMaps (seven with `maku-gateway.enabled`), each holding `<uid>.json`, with `grafana_dashboard: "1"` by default and `annotations` passed;
  - each JSON parses (`yq -p json`), with uids equal to ANR's;
  - no `$${`, `agent-system`, `xplane-`, `VictoriaLogs` or `VictoriaTraces` remains;
  - each ConfigMap is under 900 KiB.

  Run `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the product dashboards as Grafana sidecar ConfigMaps`.

### Task 8: the dashboard queries under promtool, and the docs (M)

**Files:**
- Create: `charts/agent-factory/tests/monitoring/dashboards.sh`. It extracts every `expr` from the rendered dashboards, checks each parses (`promtool query`-free: a rule file of records, then `promtool check rules`), and evaluates the queries ANR's `test-agent-dashboard-queries.sh`, `test-agent-identity-queries.sh` and `test-agent-identity-dashboard.py` cover, on their synthetic series.
- Modify:
  - `taskfile.yaml`;
  - `docs/observability.md` (each dashboard, the folder annotation, grafana-operator's `configMapRef`, and what was dropped and why);
  - `docs/web-ui.md` (`human.dashboards` links by uid).

- [ ] **Step 1: run it.** Expected: FAIL until the translations hold.
- [ ] **Step 2: fix**, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `test(charts): the dashboards' queries evaluated by promtool`. Open PR C with EX14 and the list of dropped panels in the body.

## PR D: exposure and the web front door (D19)

### Task 9: the exposure probe (S)

**Files:**
- Create: `charts/agent-factory/tests/expose-probe/probe.sh`, `README.md`. It runs on a throwaway k3d with Traefik enabled and agentgateway as the Gateway API implementation (the vendored controller), with oauth2-proxy pointed at the e2e stub issuer.

- [ ] **Step 1: write the probe as assertions**, each printing `PASS`/`FAIL <what>`:
  - **X1:** an HTTPRoute `Exact /slack/events` + `POST` answers 404 to `GET` and to `/slack/eventsx`;
  - **X2:** `RequestHeaderModifier` removes a client's `X-Forwarded-Access-Token`;
  - **X3:** a WebSocket upgrade (`/v1/ws` on an echo backend) survives 6 minutes through agentgateway and through Traefik, with and without `timeouts`;
  - **X4:** oauth2-proxy (the pinned version) with `skip-jwt-bearer-tokens`, given a valid CLI bearer plus a forged `X-Forwarded-Access-Token`, forwards no `X-Forwarded-Access-Token`;
  - **X5:** `oidc-groups-claim` with a nested claim (`realm_access.roles`): read or not, recorded;
  - **X6:** whether `HTTPRoute.spec.rules[].timeouts` is accepted by the standard CRDs at v1.2.
- [ ] **Step 2: run it,** and record the results in the PR D body and `docs/exposure.md`.
  - **Stop rule:** if X4 fails, the chart sets the header-removal flag oauth2-proxy has, and `ingress` mode refuses `expose.web` until a controller-specific strip is configured (`expose.web.annotations` named in the message).
  - **Stop rule:** if X6 fails, `timeouts` renders only when set, which is already EX2's rule.
- [ ] **Step 3: commit** `test(charts): a reproducible probe of the exposure guarantees`.

### Task 10: `expose` values, schema and rules (S)

**Files:**
- Modify:
  - `charts/agent-factory/values.yaml` (EX2, `mode: none`);
  - `values.schema.json` (`mode` enum; hostnames as RFC 1123 names; parentRefs objects);
  - `templates/_helpers.tpl` (`afumbrella.expose`, called from `afumbrella.profile`);
  - `tests/schema.sh`, `tests/render.sh`.

- [ ] **Step 1: failing cases:**
  - `mode: bogus` is refused by the schema;
  - `mode: httproute` with `web.enabled` and no hostnames fails with `expose.web.hostnames is empty`;
  - `mode: httproute` with no parentRefs at either level fails;
  - `mode: ingress` with no `className` at either level fails;
  - `maku.broker.human.mode: local` with `expose.web.enabled` fails with `the broker serves humans on a port-forward in local mode`;
  - oidc without `webProxy.enabled` fails with EX2's reason;
  - kubernetes mode with an empty `expose.networkPolicy.from` fails with EX6's message;
  - the demo render is unchanged.

  Run `bash charts/agent-factory/tests/schema.sh && bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): expose.mode, httproute, ingress or none`.

### Task 11: the routes (M)

**Files:**
- Create: `charts/agent-factory/templates/expose.yaml`.
- Modify: `tests/render.sh`, `tests/golden/expose-httproute.yaml`, `tests/golden/expose-ingress.yaml`.

- [ ] **Step 1: failing cases:**
  - **httproute:** the web route has `PathPrefix /`, the header removal, the backend `rooms-web-proxy:80`, and `timeouts` only when set;
  - per-surface `parentRefs` replace the shared ones;
  - **ingress:** `Prefix /` and `Exact /slack/events`, `ingressClassName`, merged annotations, `tls` passed through, and no Certificate object anywhere in the render (EX5);
  - the `slack` surface renders nothing without `slack.enabled`;
  - the goldens match.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the web and Slack routes, by HTTPRoute or Ingress`.

### Task 12: the web front door, its policies, the k3d proof and `docs/exposure.md` (L)

**Files:**
- Create:
  - `charts/agent-factory/templates/web-proxy.yaml` (Deployment `rooms-web-proxy`, Service with `http` 80 and `metrics` 44180, PDB, ServiceAccount with no token automount, a restricted securityContext);
  - `templates/web-proxy-netpol.yaml` (kubernetes NetworkPolicy and cilium CNP: ingress per EX6, egress to DNS, the broker's `:8080` and the IdP's 443);
  - `charts/agent-factory/tests/expose.sh`;
  - `docs/exposure.md`.
- Modify:
  - `values.yaml` and `values.schema.json` (EX4's `webProxy`);
  - `_helpers.tpl` (`afumbrella.webProxyArgs`, from `maku.broker.human` per preset; `extraArgs` may not override a derived key);
  - `charts/maku/templates/broker-netpol.yaml` and `broker-cnp.yaml` (ingress on `:8080` from `app.kubernetes.io/name: rooms-web-proxy` when `broker.webProxy: true`, a new maku value the umbrella requires to equal `webProxy.enabled`);
  - `charts/maku/values.yaml`;
  - `taskfile.yaml` (`chart:expose:k3d`);
  - `charts/agent-factory/tests/install.sh` (`EXPOSE=httproute|ingress`; Traefik on for `ingress`);
  - `docs/identity.md` (step 5's oauth2-proxy section becomes "rendered by the chart");
  - `docs/README.md`, `docs/upgrading.md`;
  - the spec's D19 line (EX17).

- [ ] **Step 1: failing cases:**
  - **render, zitadel preset:**
    - the args carry the project scope through `$(PROJECT_ID)`;
    - `EXTRA_JWT_ISSUERS` is built from the CLI Secret;
    - `skip-auth-strip-headers=true`, `cookie-samesite=strict`, and `skip-auth-route` for roomctl.
  - **render, generic preset:** the scopes are `human.scopes`; `allowed-group` is present only for a top-level claim (X5).
  - **render, overrides:** `extraArgs.upstream` fails, naming it.
  - **render, the broker:** its kubernetes-mode policy admits the proxy on `:8080`.
  - **k3d, httproute** (`task chart:expose:k3d`):
    - `GET /` through the Gateway redirects to the stub issuer;
    - a stub ID token cookie session lists rooms;
    - `GET /.well-known/roomctl` answers without a session;
    - a CLI bearer reads `/api/rooms`;
    - the same bearer plus a forged `X-Forwarded-Access-Token` is refused a decision (P18);
    - `/v1/ws` stays open 6 minutes.
  - **k3d, ingress:** the same through Traefik.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the web front door, rendered from the broker's identity keys`. Open PR D with EX2 to EX6, Task 9's results and the spec amendment in the body.

## PR E: the Slack subchart, broker parity and component egress (D20, D6)

### Task 13: the broker's parity keys (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml`, `values.schema.json`: EX9's `broker.webhooks`, `broker.suspend`, `broker.policyManagement`, `broker.coordination`;
  - `templates/broker-config.yaml` (verbatim, with `secretFile` filled);
  - `templates/broker-deployment.yaml` (one Secret volume per webhook with `existingSecret`);
  - `templates/broker-netpol.yaml`, `templates/broker-cnp.yaml` (each subscriber's egress);
  - `tests/render.sh`;
  - `charts/maku/tests/loadconfig/main.go` (loads the rendered config with the broker's strict decoder; extend the fixture).

- [ ] **Step 1: failing cases:**
  - unset, the render is byte-identical;
  - a `webhooks` entry with `existingSecret: room-broker-webhook-slack-app` mounts it at `/etc/room-broker/webhooks/slack-app/` and writes that `secretFile`;
  - a set `secretFile` wins;
  - a `cluster` entry with `networkPolicy.to` opens its port to those peers only;
  - an `internet` entry opens 443 to public addresses minus `privateCIDRs`;
  - cnref's gcp-0 `suspend`, `policyManagement` and `webhooks` blocks load through `loadconfig`.

  Run `bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the broker's webhooks, suspend, policy management and coordination as values`.

### Task 14: `charts/maku-slack` (M)

**Files:**
- Create:
  - `charts/maku-slack/{Chart.yaml (kubeVersion ">=1.30.0-0"), values.yaml, values.schema.json, README.md, .helmignore}`;
  - `templates/{_helpers.tpl, config.yaml, deployment.yaml, service.yaml, serviceaccount.yaml, rbac.yaml, threads-configmap.yaml}`;
  - `tests/render.sh`.

  The values are:
  - `image` (`ghcr.io/smana/maku-slack`, tag required by digest unless `e2e.unpinnedImages`);
  - `config` (verbatim: `teamID`, `channels`, `proposals`);
  - `brokerURL` and `roomsURL`;
  - `issuerURL`, plus step 5's generic keys when merged;
  - `slackSecret`, `principalSecret`, `webhookSecret` (EX7);
  - `principal: slack-app`;
  - `resources` and `clusterDomain`.
- Modify:
  - `taskfile.yaml` (`chart:check` lints and renders it);
  - `.github/workflows/release.yaml` (`publish charts/maku-slack maku-slack`, and the umbrella's copy step).

- [ ] **Step 1: failing cases:**
  - one replica;
  - whole-Secret mounts, mode 0440;
  - `runAsUser: 65532`, a read-only root filesystem, all capabilities dropped;
  - probes on `metrics`;
  - the config holds every file path where the mounts put it, and the three addresses;
  - the Role is `get`/`update` on `factory-slack-threads` only;
  - the thread ConfigMap has no `data` key and carries `helm.sh/resource-policy: keep` (EX10);
  - the config loads with `slackapp`'s strict decoder (a `loadconfig` mode for the App);
  - an unpinned tag fails.

  Run `bash charts/maku-slack/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `helm lint --strict charts/maku-slack --set image.tag=v0.0.0@sha256:0000`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): maku-slack, the Slack App as a chart`.

### Task 15: the umbrella wiring: alias, agreements, route and alerts (M)

**Files:**
- Modify:
  - `charts/agent-factory/Chart.yaml` (`- name: maku-slack, alias: slack, condition: slack.enabled, repository: file://../maku-slack`);
  - `values.yaml` (`slack: {enabled: false}`);
  - `values.schema.json`;
  - `templates/_helpers.tpl` (`afumbrella.slack`: EX7's refusal and EX8's agreements);
  - `templates/expose.yaml` (the `slack` surface);
  - `templates/monitoring-*.yaml` (the App's ServiceMonitor and the `maku-slack` group);
  - `tests/render.sh`;
  - `charts/agent-factory/values-slack.example.yaml` (create);
  - `internal/install/secrets.go` and `docs/install-secrets.md` (step 5's registry: the three Slack seams, `webProxy`, and `broker.webhooks[*]`). If step 5 PR E has not merged, these are a follow-up row (After this step);
  - `docs/slack.md` (an "Install" section; ANR paths become an example);
  - the spec's D20 line (EX17).

- [ ] **Step 1: failing cases:**
  - default and demo renders are byte-identical;
  - `slack.enabled` without the broker's subscriber fails, naming `maku.broker.webhooks` and the expected URL;
  - a subscriber with the wrong port fails;
  - a missing sponsored entry on either side fails, naming both keys;
  - `slack.enabled: false` with a subscriber targeting `factory-slack-app` fails (EX7);
  - `values-slack.example.yaml` renders the App, its route, its ServiceMonitor and the `maku-slack` rule group.

  Run `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the Slack App as an opt-in subchart, wired by agreement`.

### Task 16: default-deny for the factory and the Slack App, and the k3d proof (M)

**Files:**
- Create:
  - `charts/agent-factory/templates/factory-netpol.yaml`. In kubernetes mode it is a NetworkPolicy; in cilium mode, a CNP. The factory's ingress allows `:8082` from the broker, `:8080` from `networkPolicies.factory.intakeFrom` (RunLore; empty: closed), and `:9090` from `metricsFrom`. Its egress allows:
    - DNS;
    - the apiserver (`maku.networkPolicies.kubernetes.apiServerCIDRs`, `maku.apiServerPorts`);
    - the broker's `:8443`;
    - GitHub: 443 to public addresses minus `privateCIDRs`, or `networkPolicies.factory.github.to` for GHES;
    - `meter.to`, `otlp.to`, `extraEgress`.
  - `charts/maku-slack/templates/networkpolicy.yaml` and `cnp.yaml`. Ingress: `:8080` from EX6's peers, `:8081` from the broker, `:9090` from the scraper. Egress:
    - DNS;
    - the apiserver;
    - the broker's `:8080`;
    - `slack.com`. NetworkPolicy has no FQDN rule, so it is public 443 in kubernetes mode, and `toFQDNs: slack.com` in cilium mode;
    - the IdP, through `networkPolicy.idp.to`, or public 443.
- Modify:
  - `charts/agent-factory/values.yaml`, `values.schema.json` (`networkPolicies.factory`; `networkPolicies.plain` keeps the demo's ingress-only policy and is refused alongside kubernetes or cilium mode);
  - `charts/maku-slack/values.yaml` (`networkPolicy.mode`, which the umbrella requires to equal `maku.networkPolicies.mode`; `cilium.extraEgress` for cnref's gcp-0 patch);
  - `charts/agent-factory/tests/install.sh` (`SLACK=1`: the App against the e2e stub's Slack signer and its `chat.postMessage` sink);
  - `taskfile.yaml` (`chart:slack:k3d`).

- [ ] **Step 1: failing cases:**
  - **render:** each policy, per mode, holds exactly the flows above;
  - **k3d kubernetes mode, enforced:**
    - a stub-signed `app_mention` through the Gateway reaches the App;
    - the App calls the broker's `/mcp` with its client-credentials token;
    - the broker's webhook reaches `:8081`;
    - a thread line posts to the stub;
    - from inside the factory pod, a connection to a private address outside its peers is refused;
    - a badly signed `POST /slack/events` gets 401, and a `GET` gets 404 (httproute) or 405 (ingress).

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): default-deny for the factory and the Slack App`. Open PR E with EX7 to EX10, the step 2 PR13 note, and the nightly note in the body.

## PR F: RunLore's MCP endpoint (D20)

### Task 17: the broker's knowledge tools (M)

**Files:**
- Create: `internal/mcp/knowledge.go` (a `KnowledgeTools(cfg, hc, rec) []Tool` that forwards `tools/call` through a minimal Streamable HTTP client), `internal/mcp/knowledge_test.go`.
- Modify:
  - `internal/config/config.go`: `RoomToolsConfig.Knowledge {URL, Tools, Roles, Timeout, MaxBytes}`. `Validate` checks:
    - the tools are a subset of `kb_search` and `kb_get`;
    - the roles are known roles;
    - the URL is https unless the host is an in-cluster Service;
    - the timeout is 1 to 30 s;
    - `maxBytes` is at most 256 KiB;
  - `internal/config/config_test.go`;
  - `internal/app/broker.go` (register when set);
  - `internal/metrics/metrics.go` (`rooms_knowledge_calls_total`, served as `maku_rooms_knowledge_calls_total`).

- [ ] **Step 1: failing tests** against an `httptest` MCP server:
  - a triager's `kb_search` returns the upstream text, and the call carries no `Authorization`;
  - an implementer does not see the tool in `tools/list`, and is refused on call (`refused`);
  - a slow upstream gives a tool error and `timeout`;
  - an answer over `maxBytes` is clipped with a marker (`clipped`);
  - a JSON-RPC error answer gives `upstream_error`, and its message is not echoed past 200 bytes;
  - `tools: [kb_delete]` and `url: http://runlore.example.com/mcp` are refused by `Validate`, each with its key named;
  - unset, `tools/list` is unchanged.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/mcp/ ./internal/config/ ./internal/app/ -run 'Knowledge|RoomTools'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker): a configured knowledge MCP endpoint, kb_search and kb_get for triagers`.

### Task 18: the chart, the egress, the alert and the docs (S)

**Files:**
- Modify:
  - `charts/maku/values.yaml`, `values.schema.json`, `templates/broker-netpol.yaml`, `templates/broker-cnp.yaml` (the broker's egress to `knowledge.networkPolicy.to` on the URL's port), `tests/render.sh`;
  - `charts/agent-factory/files/rules/rooms.yaml` (`RoomKnowledgeFailing`: `upstream_error` or `timeout` over 50% for 30 min, with `knowledge.url` set) and its promtool case;
  - `docs/api.md` (the two tools on `:8090`);
  - `docs/integration.md` (RunLore: the three optional integrations, EX1's row);
  - `docs/factory.md` (investigate tasks);
  - `docs/upgrading.md`;
  - the spec's "RunLore stays its own product" sentence names the key.

- [ ] **Step 1: failing render cases:**
  - unset, byte-identical;
  - set, the config carries it, and kubernetes mode opens the port to the peers only.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): RunLore's MCP endpoint as one broker value`. Open PR F with EX11 in the body.

## PR G: `profile: production` renders

### Task 19: the production gate (M)

**Files:**
- Modify:
  - `charts/agent-factory/templates/_helpers.tpl`: `afumbrella.profile` accepts `production`; `afumbrella.production` implements EX16, collecting failures into one `fail` with a line each;
  - `values.schema.json` (the `profile` description);
  - `tests/render.sh`, `tests/schema.sh`.

- [ ] **Step 1: failing cases:**
  - `profile: production` with `demoValues` fails;
  - `postgres.enabled` fails;
  - `networkPolicies.plain` fails;
  - an empty production values file with `maku.enabled` lists every missing maku seam in one message: the human mode, the issuer, the broker DSN, the public URL, the sandbox and the images;
  - `maku.enabled: false`, `maku-factory.enabled: true` with its seams renders the factory only;
  - `sandbox.unsafeAllowRunc: true` renders, and the broker's `install.unsafe` lists it (PR9);
  - `--set profile=demo` without `values-demo.yaml` still fails as today.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): profile production renders, refusing every missing seam`.

### Task 20: example values and the cnref-shaped golden (S)

**Files:**
- Create:
  - `charts/agent-factory/values-production.example.yaml` (generic: Postgres DSN Secrets, the generic OIDC preset, kubernetes mode, httproute exposure, monitoring on);
  - `charts/agent-factory/tests/values-cnref-like.yaml`: ANR's shape at `1529b3b`. It has:
    - the zitadel preset;
    - cilium mode;
    - `expose.web` on `platform-tailscale-general` and `expose.slack` on `platform-public`/`factory-slack`;
    - `webProxy.existingSecret: rooms-proxy`;
    - `slack.enabled` with cnref's Secret names;
    - the broker's `webhooks`, `suspend` and `policyManagement`;
    - `maku-gateway`;
    - `monitoring` with no sidecar label;
  - `tests/golden/production-cnref-like.yaml`.
- Modify: `tests/render.sh`, `charts/agent-factory/README.md`.

- [ ] **Step 1: failing cases:** both files render, and the cnref-like render equals its golden. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `test(charts): a production render shaped like cloud-native-ref`.

### Task 21: the production-shaped k3d install and the docs (M)

**Files:**
- Modify:
  - `charts/agent-factory/tests/install.sh` (`PROFILE=production`: the stub as generic OIDC issuer, step 5's external Postgres container, kubernetes mode, httproute through agentgateway, monitoring objects with the prometheus-operator CRDs applied only);
  - `taskfile.yaml` (`chart:production:k3d`);
  - `docs/upgrading.md` ("from the demo to production; adopting the umbrella from `maku-factory`": the same release name, `maku.enabled: false` first);
  - `docs/quickstart.md` (a pointer);
  - `docs/README.md`.

- [ ] **Step 1: run** `task chart:production:k3d`. Expected, once implemented:
  - the install succeeds;
  - a stub-issuer session lists rooms through the Gateway;
  - the factory's ops `/readyz` is 200;
  - the PrometheusRule and the ServiceMonitors exist;
  - `helm upgrade` from a release of `maku-factory` alone to the umbrella with `maku.enabled: false` keeps the factory's Deployment UID.
- [ ] **Step 2: fix**, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `test(charts): a production-shaped install on k3d, and the adoption path`. Open PR G with EX16 in the body. After it merges, cut the step's v0.4x minor.

## After this step (not in these PRs)

- **Owner decisions:**
  - **`profile: production` in step 6, not step 7:** recommended (EX16), because D22's shrink needs cnref on the umbrella, and step 1 named steps 2 to 6.
  - **The web front door as an umbrella template** rather than step 5's "subchart" (EX4).
  - **RunLore through the broker** rather than gateway MCP federation (EX11).
  - **The three spec amendments of EX17**, in particular D6's collector egress and D20's Deployment count.
  - **Renaming `factory_slack_*` to `maku_slack_app_*`:** a breaking metric rename, so before v4.0.0 or never.
- **ANR follow-ups** (D22; each its own PR after the step's Maku minor, each recorded with a gcp-0 live check in the session ledger, aws-0 after). They adopt the umbrella one component at a time:
  1. **The factory under the umbrella.**
     - Repoint `flux/sources/ocirepo-agent-factory.yaml` at `oci://ghcr.io/smana/charts/agent-factory` (pinned digest, same cosign identity).
     - Keep `releaseName: agent-factory`, and set `install.crds`/`upgrade.crds: Skip` (the CRDs come from `agent-factory-crds`, step 1's follow-up).
     - Values: `profile: production`, `maku.enabled: false`, and today's `helm-values-configmap.yaml` and sponsored overlays re-nested under `maku-factory:`. Flux's `valuesFrom` cannot prefix a whole file.
     - Live check: `helm history` shows an upgrade, not an install; the Deployment UID is unchanged; Tasks are untouched.
  2. **The broker from the chart.**
     - Set `maku.enabled: true`, with the broker values taken from `infrastructure/{base,gcp-0,aws-0}/room-broker/config.yaml`: the human block, `webhooks` with `existingSecret`, `suspend` and `policyManagement`.
     - The DSN Secrets are CNPG's (step 5's follow-up 4). `ROOMS_MCP_KEY` and `ROOMS_GITHUB_APP_DIR` go through `extraEnv`/`extraVolumes`.
     - Set `networkPolicies.mode: cilium`. Whatever step 2's follow-up runs for the run-controller moves under this `maku`.
     - Cut-over: delete the `App` XR `room-broker`, then reconcile. The bridges buffer through the gap (`RoomBrokerDown`'s note). **Stop rule:** if the XR's Deployment selector equals the chart's, orphan and adopt with Helm's ownership labels instead, with no gap.
     - Delete:
       - `infrastructure/base/room-broker/{app,rbac,tls,network-policy,network-policy-webhooks,kustomizeconfig}.yaml`, the configMapGenerator and `config.yaml` (now values);
       - `infrastructure/{aws-0,gcp-0}/room-broker/network-policy-webhooks-patch.yaml`;
       - `scripts/ci/gen-webhook-egress.py` and its tests (the chart renders that egress, EX9).

       Keep `sqlinstance.yaml`, `network-policy-cnpg.yaml`, `suspend/` and the ExternalSecrets.
     - Live check: rooms list, a run's bridge reconnects, a Slack line arrives through the webhook.
  3. **The routes and the web front door (D19).**
     - Values:
       - `expose.mode: httproute`;
       - `expose.web: {hostnames: [rooms.${private_domain_name}], parentRefs: [{name: platform-tailscale-general, namespace: infrastructure, sectionName: https}]}`;
       - `expose.httproute.timeouts.request: 0s`;
       - `webProxy: {enabled: true, existingSecret: rooms-proxy}`.
     - Delete `infrastructure/base/room-broker/{httproute,oauth2-proxy,network-policy-oauth2-proxy}.yaml`.
     - Delete `tooling/base/agent-factory/httproute.yaml`, after a week of the gateway's access log shows no `POST /v1/runs` caller (EX3).
     - Live check: a browser sign-in, `maku login` and `maku mcp`, and a CLI bearer with a forged `X-Forwarded-Access-Token` cannot decide.
  4. **The Slack App from the subchart (D20).**
     - Values: `slack.enabled: true`, with `config` from `slack-app/config.yaml`. Retarget `gen-slack-bindings.py` to write `slack.config.teamID`/`channels` in the values file, and keep its test.
     - `expose.slack: {hostnames: [factory-slack.${public_domain_name}], parentRefs: [{name: platform-public, namespace: infrastructure, sectionName: factory-slack}]}`.
     - gcp-0's ZITADEL egress patch becomes `slack.networkPolicy.cilium.extraEgress`.
     - Hand `factory-slack-threads` to Helm first: add `meta.helm.sh/release-name`, `meta.helm.sh/release-namespace` and `app.kubernetes.io/managed-by: Helm`, and `kustomize.toolkit.fluxcd.io/prune: disabled`. Add EX10's `driftDetection.ignore`.
     - Then delete `tooling/base/agent-factory/slack-app/{deployment,service,serviceaccount,rbac,network-policy,httproute,vmservicescrape,config,configmap-threads}.yaml` and the configMapGenerator. Keep the four ExternalSecrets and the Password generator: ESO generators are not chart objects.
     - Live check: an existing thread still answers (the map survived), a new mention gets its proposal, a card decides.
  5. **Monitoring from the chart (D21).**
     - First confirm that cnref's VictoriaMetrics operator converts `ServiceMonitor`, `PodMonitor` and `PrometheusRule`. **Stop rule:** if it does not, a cnref PR enables the converter first.
     - Values: `monitoring.serviceMonitor`, `prometheusRule` and `dashboards` enabled, with `dashboards.labels: {}`. The seven `GrafanaDashboard`s become `configMapRef`s to `maku-dashboard-<uid>` in `agent-system` (`allowCrossNamespaceImport: true`, `folderRef: agents`).
     - In the same PR, delete:
       - `observability/base/agent-platform/vmrule-rooms.yaml` and `vmrule-agent-factory.yaml`;
       - the rules EX13 moves out of `vmrule.yaml`;
       - `infrastructure/base/room-broker/vmservicescrape.yaml` and `tooling/base/agent-factory/vmservicescrape.yaml`;
       - the dashboards' inline JSON;
       - the ANR tests whose rules or queries moved (Tasks 6 and 8 list them).
     - Keep:
       - EX13's "stays" list, in a trimmed `vmrule.yaml` and `vmrule-logs.yaml`;
       - one cnref dashboard holding the dropped logs, traces and Karpenter panels;
       - `test-agent-alert-annotations.sh`, retargeted.
     - Live check: vmalert lists `maku-rooms`, `maku-factory`, `maku-run-path`, `maku-slack` and `maku-gateway`; a synthetic `RoomApprovalPendingTooLong` reaches Slack through Alertmanager; each dashboard shows data; the room page's dashboard links resolve.
  6. **Traces and RunLore.**
     - Set `maku.runController.tracing` to `http://agent-traces-collector.observability.svc.cluster.local:4318` with its pods' selector, and `maku.broker.roomTools.knowledge.url: http://runlore.runlore.svc.cluster.local:8080/mcp`. The collector and `referencegrant-agent-traces.yaml` stay ANR's.
     - Live check: a run's span sits under its task's span, and an investigate task's triager calls `kb_search`.
     - The `runlore-kb` target in `infrastructure/base/agent-gateway/mcp.yaml` stays until step 8 retires the XR.
  7. **ANR's CI renders the chart with cnref's values (D22).** `scripts/ci/render-agent-factory.sh` pulls the pinned chart and runs `helm template` and kubeconform with the HelmRelease's values. It replaces the hand-written manifests' schema checks.
  8. **The ExternalSecrets move into `externalSecrets.secrets`** (step 5's follow-up 5), the Slack App's and `rooms-proxy` included, and their files are deleted.
- **Later Maku steps:**
  - **Step 7:**
    - `maku preflight` checks that each exposed hostname resolves and answers through its route, that the prometheus-operator CRDs exist when monitoring is on, and that the knowledge endpoint answers `tools/list`;
    - `maku install --print` asks for the exposure, Slack and monitoring;
    - the k3s job reuses `EXPOSE=ingress` on Traefik (D23).
  - **Step 8:**
    - deletes `networkPolicies.plain` and the `profile` switch (`profile: production` becomes the only render, the demo a values file);
    - deletes the knowledge tools' entry in `mcp.yaml`'s federation docs once no XR runs.

## Self-review

- **D19:**
  - `expose.mode` httproute, ingress or none, with per-surface parentRefs or ingress class and hostnames (Tasks 10–11, EX2);
  - the user's TLS, with no certificate rendered (EX5, Task 11's assertion);
  - plain HTTP in-cluster inside default-deny (EX6, Tasks 12 and 16);
  - only `/slack/events` must be public, GitHub needs no path (EX3), and the docs show how to expose just that (Task 12);
  - the web front door rendered from the identity keys, with the identity headers stripped twice and proved (EX4, Tasks 9 and 12).
- **D20:**
  - the core is always installed;
  - the Slack App is a subchart, off by default, wired by agreement, with what off removes stated and enforced (EX7, EX8, Tasks 14–15);
  - RunLore is one MCP endpoint value, with D6 intact (EX11, Tasks 17–18);
  - the minimal install's count is amended (EX17).
- **D21:**
  - `/metrics` on every component, inventoried (EX12);
  - OTLP traces from the factory and now from runs, off when unset (EX15, Tasks 1–2);
  - opt-in ServiceMonitors and PodMonitors (Task 4), the PrometheusRule with ANR's product alerts plus the step 3 ones (EX13, Tasks 5–6), and dashboard ConfigMaps with the sidecar label (EX14, Tasks 7–8);
  - all in PromQL, tested by promtool.
- **D22:** the chart now renders everything ANR's broker, Slack App, routes, front door, VMRules, scrapes and dashboards did. The `profile: production` gate allows staged adoption (EX16). ANR follow-ups 1–8 delete the duplicates in order.
- **D6:**
  - default-deny for the factory, the Slack App and the web proxy (Task 16, closing step 2's PR13);
  - the collector egress is the one configured widening (EX15).
- **D14:** every new credential is a named Secret with documented keys, registered in step 5's registry (Task 15).
- **D25:**
  - every switch defaults off;
  - default and demo renders are diffed byte-identical (Tasks 2, 4, 10, 13, 15, 18);
  - the factory's tracing key is unchanged;
  - no data migration (the thread map is adopted, not recreated);
  - no v4.
- **D26:** `kubeVersion` on `maku-slack`, Gateway API v1.2, Helm 3.15.
- **Risks named with a stop rule:**
  - **oauth2-proxy forwarding a forged access header:** Task 9's X4. The web route strips it, and Ingress mode refuses `expose.web` without a strip.
  - **Nested roles claims in oauth2-proxy:** X5. `allowed-group` is omitted, and the broker stays the authority.
  - **The thread map reset by drift detection:** EX10's ignore rule, plus adoption before the files are deleted (follow-up 4).
  - **cnref's VictoriaMetrics operator not converting:** checked before follow-up 5.
  - **The broker cut-over gap:** an orphan-and-adopt alternative (follow-up 2).
  - **The dashboards' MetricsQL semantics:** translated and evaluated by promtool on ANR's series (Task 8).
  - **The nightly** goes red until the release (strict decoding).

---

