# v4 rollout step 4, the gateway: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agentgateway becomes the only model gateway on the product path (D7), shipped with the umbrella chart. It serves three kinds of caller:
- **The agents.** Each run-controller run calls agentgateway with a token the run-controller projects for that run. No provider key enters the runs namespace any more. The run's subject is `system:serviceaccount:<runs ns>:<run>`, which the meter already matches, so the meter prices the run.
- **The planner.** The broker calls `tier-frontier` through the gateway with its own projected token. It no longer uses a static key held by the Envoy AI gateway.
- **The classifier.** The broker calls `tier-light` the same way, as a separate client.

Routes, providers and aliases are values, and the keys are Secrets named in those values (D14). The quirks of each provider live in the chart as presets: Mistral's `reasoning_effort`, Kimi's fixed temperature and its constant reasoning. Once runs carry a gateway identity, step 2 PR B's refusals are lifted: `loops.<class>.models` and `split` on the portable `maku.ogenki.io` kind, and the run-controller's refusal of `dataClass: internal`. The Envoy AI gateway leaves Maku's docs, fixtures and defaults. ANR then drops it and moves the suspended Kimi and Mistral overrides into the gateway's values.

Every existing install keeps working (D25):
- `runController.modelSecret` (`agent-model` with `base_url`, `model`, `api_key`, `judge_model`) stays the default model mode;
- `human.intake.plan.url`/`apiKeyFile` and `human.slackIntents.url`/`apiKeyFile` keep their meaning and win over anything the chart derives;
- `meter.logsURL` with `meter.throttleQuery` keeps working;
- `maku-gateway.enabled` and `agentgateway.enabled` default to false, so the umbrella's default and demo renders do not change.

**Architecture:**
- **A new chart, `charts/maku-gateway`,** renders Maku's objects on an agentgateway controller:
  - the Gateway, with three listeners: `public`, `internal` and `clients`;
  - its `AgentgatewayParameters`;
  - one `AgentgatewayBackend` per provider and alias;
  - the HTTPRoutes, routed by path, one per alias;
  - the identity and authorization policies;
  - the model-catalog ConfigMap;
  - its NetworkPolicy.

  It is released like `maku-factory`, so ANR can consume it on its own before cnref moves to the umbrella. The umbrella includes it as an optional subchart (`maku-gateway.enabled`). Beside it sits the upstream controller chart, `oci://cr.agentgateway.dev/charts/agentgateway`, as a second optional subchart (`agentgateway.enabled`). The upstream `agentgateway-crds` chart becomes an optional dependency of `agent-factory-crds`, which is installed first.
- **The run-controller gains `model.mode: gateway`.** Each run gets a projected ServiceAccount token whose audience is `<prefix>.<dataClass>.<alias>`, and a second one for its judge alias. The other inputs follow from that:
  - the base URL is the run's data-class listener, under `/m/<alias>/v1`;
  - `MODEL` is the run's `spec.model`;
  - the judge gets its own alias path and token.

  The gateway verifies the token offline against the cluster's issuer. It checks the data-class listener and the alias the path names, and records the token's `sub` as `ar_agent`/`x_ar_agent`.
- **The broker** mounts one projected token per client, with audiences `<prefix>.client.planner` and `<prefix>.client.classifier`. The chart writes the `clients` listener URL and the token file into `human.intake.plan` and `human.slackIntents` unless the verbatim block sets them already. The broker code does not change: it already reads a bearer file at each call. `PlanModel` and `ClassifyModel` stay pinned constants.
- **The meter** keeps its query and its subject match:
  - it learns agentgateway's own token-type names (`input_cache_read`, `input_cache_write`), so a Prometheus that does not relabel still prices runs;
  - it gains an optional PromQL throttle source beside VictoriaLogs.
- **The factory** gains `runs.gatewayIdentity`, which is what lifts the refusals on the portable kind.

**Tech Stack:**
- agentgateway v1.6.0, ANR's pin, with its `AgentgatewayBackend` and `AgentgatewayPolicy` CRDs and CEL;
- Gateway API v1 (`Gateway`, `HTTPRoute`, URLRewrite);
- Kubernetes projected ServiceAccount tokens (TokenRequest, `serviceAccountToken` volume sources);
- Go: controller-runtime and the existing meter;
- Python for one harness variable;
- Helm 3.15 with an OCI dependency and `Chart.lock`, JSON Schema, and the bash render tests (`yq`);
- k3d with the upstream controller, the e2e stub as a fake provider, and a test-only single-binary VictoriaMetrics for the metering proof;
- the nightly real-model job.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D7 (agentgateway only, in the umbrella);
- D14 (Secrets by name);
- D20 (the minimal install includes agentgateway);
- D21 (`/metrics`, Prometheus standard);
- D25 (additive);
- D26 (support floor);
- "Rollout", step 4;
- the 2026-11-12 owner rulings (Release: a minor is cut per step, after the step's last PR).

**Read against:**
- Maku `origin/main` `e43e9329`. Step 1, step 2 PR A and PR B, step 3 PR A, and HR13's refusal (#499) are merged. Step 2 PR C (the network: the `kubernetes` mode and the egress proxy) is not; PR B below names the one seam it shares with it.
- ANR `origin/main` `1529b3b`.

**Branch:** four PRs, each from `origin/main`.

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, the gateway chart (D7, D14) | `feat/v4-gateway-chart` | 1–5 | L | first: nothing calls it yet |
| B, run-controller runs through the gateway (D7) | `feat/v4-gateway-runs` | 6–10 | L | second; needs A |
| C, the planner and the classifier through the gateway (D7) | `feat/v4-gateway-clients` | 11–13 | M | after A, in parallel with B |
| D, metering on the run-controller path; lifting the refusals | `feat/v4-gateway-metering` | 14–17 | M | last; needs B |

The step's v0.4x minor is cut after PR D. A to C may also ship in interim minors.

## Global Constraints

- **No v4.** This step ships v0.4x minors only: no v4 tag, release or chart version.
- **Additive (D25).**
  - Every new behaviour is opt-in: `maku-gateway.enabled`, `agentgateway.enabled`, `agentgateway-crds.enabled` in `agent-factory-crds`, `maku.runController.model.mode: gateway`, `maku.broker.modelGateway`, `runs.gatewayIdentity` and `meter.throttle.promQuery`.
  - Default renders stay byte-identical. Prove it with a render diff against `origin/main` for the umbrella demo, the maku quickstart, `runController.enabled=false`, and `agent-factory-crds`'s default.
  - Old keys keep working. Where an old and a new key conflict, the render or the config fails with a message naming both.
- **One identity per caller, verified at the gateway.**
  - No run, broker or factory sends a provider key.
  - Under `model.mode: gateway`, no Secret holding a provider key exists in the runs namespace.
  - Every identity header the gateway logs or labels (`x_ar_agent`, `ar_agent`) is the verified `jwt.sub`. The incoming `x-ar-*` headers are stripped before routing, as ANR's `strip-identity-headers` does.
- **Fail closed.**
  - A run whose alias the run's listener does not serve is refused before its pod is created.
  - A gateway token audience the gateway does not know answers 401 or 403, never a default model.
  - A classifier mapped to a model that always reasons fails the render (GW7).
- **Secrets by name (D14).** Each provider names `existingSecret` and `key`, and the chart never renders a provider key from a plaintext value. The one exception is the demo values, as `demo.model.apiKey` does today.
- **Strict decoding.** The run-controller, broker and factory configs are decoded strictly, so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until the release. Name this in each PR body.
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
  - `task crd:gen && task crd:check` after an API type or doc-comment change (Task 7 edits `spec.model`'s description);
  - `task harness:test` after a harness change;
  - `task check` before the last commit of each PR.

## Rulings made while planning

- **GW1 (what exists today; this step builds only what is missing).**

  | Caller | Today on `origin/main` (Maku) and on cnref (ANR) | Missing, built here |
  |---|---|---|
  | **The planner** (`internal/intake/model.go`, `internal/config/config.go:126–300`) | One `POST <human.intake.plan.url>/chat/completions` with `Authorization: Bearer <apiKeyFile, read at use>`, carrying model `tier-frontier` (`PlanModel`, the only value `model` may hold), `temperature: 0`, `max_tokens: 4000`, `response_format: json_object`, and `reasoning_effort` when `reasoningEffort` is set (low, medium or high). It has a 20 s deadline and a 64 KiB reply bound. On cnref the URL is `http://ai-gateway.envoy-gateway-system.svc.cluster.local:8080/v1`: the Envoy AI gateway's human/system Gateway `ai-gateway`. There, `apiKeyAuth` (`infrastructure/base/envoy-ai-gateway/security-policy.yaml`) admits client id `planner` from `ai-gateway-planner-key`, and the AIGatewayRoute `llm-gateway` (`infrastructure/base/llm-gateway/aigatewayroute.yaml`) matches `x-ai-eg-model: tier-frontier` and sends it to Z.ai `glm-5.3` with a 600 s timeout. `reasoningEffort: low` is set because `glm-5.3` always reasons (Maku #372). The broker's key is a second copy, `room-broker-planner`, mounted at `/etc/room-broker/planner` | The planner on agentgateway, with no static key (Tasks 11–12) |
  | **The classifier** (`internal/intent/model.go`, `human.slackIntents`) | The same shape: alias `tier-light` (`ClassifyModel`), `temperature: 0`, `max_tokens: 64`, `response_format` a strict `json_schema`, a 5 s deadline, and a key file read at use. The broker makes the call for the Slack App. It runs in shadow and is not configured on cnref | The same, on its own audience (Tasks 11–12) |
  | **XR runs** (cnref) | An identity-proxy sidecar (`infrastructure/base/agent-runtime/identity-proxy-configmap.yaml`) holds the run's projected token. Its audience is `agent-router.<role>.<dataClass>`, and its TTL is the run's deadline, because gVisor does not raise the inotify event for a rotated token. The sidecar exposes `:4000` (public, sent to `agent-router` `:8080`) and `:4002` (internal, `:8081`), and pins `x-ar-model` from `MODEL` and `JUDGE_MODEL` (`/judge/v1`). The gateway is `agent-router` on agentgateway 1.6.0 (`infrastructure/base/agent-gateway`). Its `jwtAuthentication` checks the cluster issuer, requires `exp` and `sub`, requires a sub that `startsWith("system:serviceaccount:agents:")`, and sets `x-ar-agent = jwt.sub`. The public listener's Z.ai backends alias `agent-default` and `tier-frontier` to `glm-5.3`, `tier-standard` to `glm-5.3-flashx` and `tier-light` to `glm-5.3-flash`. Header rules send `kimi-frontier` to `moonshot` (`api.kimi.ai`, `/coding/v1/chat/completions`, `k3.1-pro`) and `mistral-standard` and `mistral-light` to `mistral`, whose transformation sets `reasoning_effort` to `"none"` (agentgateway parses a list-shaped `content` as an error, live run `uyqiqv6e`). The internal listener serves Anthropic. `policy-capabilities.yaml` restricts aliases by audience. `policy-frontend.yaml` adds `ar_agent` to metrics and `x_ar_agent` and `cost` to the access log. `vmpodscrape.yaml` renames `agentgateway_gen_ai_client_token_usage_*` and the token types `input_cache_read` and `input_cache_write` | Nothing: the XR keeps this until step 8 |
  | **Run-controller runs** (`internal/runctl`) | `ModelFrom` reads `agent-model`: `api_key` and `model` are required, `base_url` defaults to Anthropic, `judge_model` is optional, and http is allowed only for an in-cluster `<svc>.<ns>.svc.<domain>`. The pod mounts `api_key` into the harness (`LLM_API_KEY_FILE`). `MODEL` is the Secret's model, and `spec.model` is ignored (3a R7). The judge calls `JUDGE_BASE_URL = base_url` with `JUDGE_MODEL = judge_model` or `model`. `dataClass: internal` is refused (`config.go:197`). The CNP already admits an in-cluster model host by namespace and port (`cnp.go:21–72`). The provider key sits inside the sandbox | Gateway mode (Tasks 6–10) |
  | **Metering** (`internal/factory/meter`) | `VM` runs `meter.query` against a Prometheus-compatible `api/v1/query`. It keeps series whose `ar_agent` matches `kind.RunSubject()`, `^system:serviceaccount:<ns>:<prefix>([a-z2-7]{8})$`, which for the portable kind is the run's ServiceAccount, already named like the run (`objects.go:49`). It counts the token types `input`, `output`, `cached_input` and `cache_creation_input`, and prices by `gen_ai_request_model`. `VL` reads 429s from VictoriaLogs (`meter.logsURL`, `throttleQuery`, row key `log.x_ar_agent`). Outside the demo all four meter keys are required (`config.go:1657`). Portable runs never reach a gateway, so nothing is priced. `config.go:2353–2361` refuses `models` and `split` there ("meters no USD") | A gateway identity for those runs (B), the native type names and a PromQL throttle (D), the lifted refusals (D) |
  | **The chart** | The umbrella has `maku` and `maku-factory` (file deps) and renders `agent-model` from `demo.model`. ANR consumes `maku-factory` alone, and its room-broker is hand-written manifests | `maku-gateway` and the two upstream subcharts (A) |

- **GW2 (where the gateway lives).**
  - **`charts/maku-gateway` is a chart of its own**, released with the others (`ci.yaml` and `release.yaml` `publish charts/maku-gateway maku-gateway`). The umbrella depends on it with `condition: maku-gateway.enabled`.
  - **Why not templates in the umbrella:** ANR consumes `maku-factory` alone today and moves to the umbrella only at step 6 (D22). A gateway that lives only in the umbrella could not be flipped on cnref in step 4, which breaks the rollout rule of one seam per step.
  - **The upstream controller** (`agentgateway` 1.6.0) is a second optional dependency (`condition: agentgateway.enabled`), off where the cluster already runs one. cnref runs it from `infrastructure/base/agentgateway`.
  - **The CRDs:**
    - `agent-factory-crds` gains an optional dependency on `agentgateway-crds` 1.6.0 (`condition: agentgateway-crds.enabled`, default false), because CRDs are installed first;
    - Gateway API's standard CRDs (≥ v1.2, the floor the probe records) remain a cluster prerequisite, which step 7's preflight checks.
  - **Pins:** `Chart.lock` holds the upstream digests. `task chart:deps` runs `helm dependency build`, and CI fails when `Chart.lock` and the vendored archives disagree.
- **GW3 (the per-run identity: a projected ServiceAccount token, verified offline).**
  - **The token:** the run-controller adds a projected volume `model-token` with two `serviceAccountToken` sources:
    - `token`, audience `<prefix>.<dataClass>.<spec.model>`;
    - `judge-token`, audience `<prefix>.<dataClass>.<spec.loop.judgeModel>`, only when a judge is set.

    `<prefix>` is `gateway.audiencePrefix` (default `maku-gateway`). The ServiceAccount is the run's own, named like the run, so `jwt.sub` is the subject `RunSubject()` already matches. The meter's code needs no new key.
  - **The gateway** validates the token against the cluster's issuer (GW4) and requires `exp` and `sub`. Each listener requires:
    - a sub matching `^system:serviceaccount:<runs ns>:<namePrefix>[a-z2-7]{8}$`;
    - an audience starting `<prefix>.<listener class>.`.

    Each alias route requires the audience `<prefix>.<class>.<alias>`. The gateway sets `x-ar-agent = jwt.sub`, removes the token before the backend (`preserveToken: false`), and labels metrics `ar_agent` and the access log `x_ar_agent` with `jwt.sub`: the names the meter and ANR's dashboards read.
  - **Audiences are checked in CEL, not in the provider's `audiences` list.** ANR found a cap of eight audiences per provider. Seven aliases times two classes would leave no room.
  - **Alternatives weighed:**
    - **The identity-proxy sidecar (the XR's):** one more container and image per run, and its gVisor rotation workaround. Rejected: the harness can send the bearer itself.
    - **Per-run API keys:** the run-controller would write a key Secret per run, and the gateway's controller would have to read Secrets in the runs namespace. Rejected.
    - **extAuthz to the broker's TokenReview:** puts the broker in every model call's path. Kept as the fallback if GW4's probe fails on a cluster.
    - **A header the harness sets:** forgeable from inside the sandbox. Rejected.
- **GW4 (the JWKS source).** `maku-gateway.identity.issuer` and `identity.jwks` take two forms:
  - **`source: url`** (default): `jwks.url` is the issuer's public JWKS, as on GKE and EKS (cnref: `${oidc_issuer_url}` and `${oidc_jwks_uri}`, as `policies-identity.yaml` already uses).
  - **`source: apiserver`:** `https://kubernetes.default.svc.<clusterDomain>/openid/v1/jwks`, reached through a `backendRef` with a BackendTLSPolicy on the `kube-root-ca.crt` CA. This is for kind, k3d and kubeadm, whose issuer is not reachable from outside. When Task 1 shows agentgateway cannot authenticate its JWKS fetch, the chart also renders a ClusterRoleBinding of `system:service-account-issuer-discovery` to `system:unauthenticated`, behind `identity.jwks.allowAnonymousDiscovery: true`. This is Kubernetes' documented setting for OIDC discovery, and it exposes public keys only. The render fails when `source: apiserver` needs it and the value is false. The demo sets it; `docs/security.md` names it.
- **GW5 (aliases by path, the model forced; no forgeable header).**
  - **The route:** each alias of each listener is one HTTPRoute rule. It matches PathPrefix `/m/<alias>/`, rewrites to `/` (`URLRewrite ReplacePrefixMatch`), and sends to one backend `<provider>-<alias>`.
  - **The backend's transformation sets `model` to the alias, whatever the body says.** Its `modelAliases` maps that one alias to the provider's model.
  - **What the run-controller sets:** `LLM_BASE_URL=<listener URL>/m/<spec.model>/v1` and `JUDGE_BASE_URL=<listener URL>/m/<judge>/v1`.
  - **The pin:** a sandbox that rewrites its path to another alias holds no token with that alias's audience, so the call is refused 403. That pins each run to its claim's model and judge, as ANR's capability profiles do per role.
  - **Rejected: routing by `x-ar-model`.** Without the sidecar it is set by the harness, so it is forgeable, and agentgateway 1.6 cannot route on the body.
  - **If Task 1 finds the rewrite or the forced model broken**, the fallback is an `x-maku-model` header the harness sets from `MODEL` (litellm `extra_headers`), checked against the token's audience. That adds a harness task to PR B; the stop rule is in Task 1.
- **GW6 (data classes are listeners; `internal` lifted on the run-controller).**
  - **The listeners:** `public` (8080), `internal` (8081) and `clients` (8082). Each alias row names its provider, and each provider declares `dataClasses`.
  - **The render fails** when an `internal` alias names a provider without `internal` in `dataClasses`. The presets default `moonshot` and `mistral` to `[public]` (China, and EU until the owner approves), and `zai` to `[public]`.
  - **On the run-controller:** the audience and the listener come from `spec.dataClass`, so a public run cannot reach the internal listener, and the reverse holds too. In gateway mode, an internal run is admitted when `gateway.aliases.internal` names its alias. Without such an alias it stays refused, with a message that names the missing key.
- **GW7 (provider presets carry the quirks; values can add, never silently remove).** `providers.<name>.preset` selects the chart data in `templates/_presets.tpl`:

  | Preset | Provider object | Transformations always rendered | Flags |
  |---|---|---|---|
  | `openai` | `openai` | none | |
  | `anthropic` | `anthropic` (Completions in, Messages out) | none | `promptCache: true` |
  | `zai` | custom, `api.z.ai`, `/api/paas/v4/chat/completions` | none | `alwaysReasons: true` (glm-5.3) |
  | `mistral` | custom, `api.mistral.ai`, `/v1/chat/completions` | `reasoning_effort = "none"`: agentgateway 1.6 and 1.7 cannot parse the list `content` Mistral's reasoning answers with (502, ANR run `uyqiqv6e`), and the planner sends `low` | |
  | `moonshot` | custom, `api.kimi.ai`, `/coding/v1/chat/completions` (the Kimi Code plan's host) | `temperature = 1`, `top_p = null`: K3.1 Pro fixes both, and the planner and classifier send `temperature: 0` | `alwaysReasons: true`: `reasoning_content` spends `max_tokens` first |
  | `custom` | custom, `host`, `port`, `path`, `tls` | only the user's | the user's |

  - **The user's additions:** `providers.<name>.transformations` appends. A user field that names a preset field fails the render with `providers.<name>.transformations.<field>: the <preset> preset sets it (<why>); use preset custom to own it`.
  - **The classifier rule:** `clients.classifier` mapped to an `alwaysReasons` model fails the render, because its 64 tokens would be spent reasoning and the reply would be empty. The planner on such a model is allowed: its `reasoningEffort: low` is documented next to it.
  - **What the probe confirms:** each preset's transformation, on the stub echo upstream (Task 1). Whether `k3.1-pro` refuses `temperature: 0` or ignores it stays **unverified**; the preset sets 1 either way.
- **GW8 (the planner and the classifier are clients on their own listener).**
  - **The listener:** `clients` (8082) requires `sub == system:serviceaccount:<release ns>:<broker ServiceAccount>`. Each route requires its audience:
    - `/clients/planner/` → `<prefix>.client.planner`, with the model forced to `tier-frontier` → `clients.planner.{provider, model}`;
    - `/clients/classifier/` → `<prefix>.client.classifier`, with `tier-light` → `clients.classifier.{provider, model}`.

    The routes' timeout is 60 s, and the callers' own deadlines (20 s and 5 s) are shorter.
  - **The maku chart's `broker.modelGateway`** (`{service, namespace, port, audiencePrefix}`) adds a projected volume (two tokens, 3600 s, read at use, so the kubelet's rotation reaches them). It also fills these keys when the verbatim `human` block leaves them unset:
    - `human.intake.plan.url = http://<service>.<ns>.svc.<domain>:<port>/clients/planner/v1` and `apiKeyFile = /var/run/secrets/maku/model-gateway/planner`;
    - the same pair for `human.slackIntents`, only when that block exists.

    A block that sets `url` keeps it, so cnref's Envoy URL works until its follow-up.
  - **No broker code changes.** `PlanModel`, `ClassifyModel`, the deadlines and caps stay as they are (CD17). Spend stays attributed by the broker's `Usage` and its daily caps. The gateway attributes it by `jwt.sub` and route, which is enough for dashboards.
  - **Data:** the clients listener serves only the two clients' providers. `docs/gateway.md` says the classifier sees replies in every room it serves, so its provider must be acceptable for the workspace's data.
- **GW9 (the token's lifetime, and its residual).**
  - **TTL:** the run token's `expirationSeconds` is the pod's `activeDeadlineSeconds` plus 600. This copies the XR's ruling R2: the harness reads the key once (`build_request`), and gVisor does not raise inotify on a rotated token.
  - **The cap:** the run-controller's preflight (step 2 PR A) gains the check `gateway-token-ttl`. It makes one TokenRequest for its own ServiceAccount at the longest run (`budget.maxMinutes` 480, plus 10 minutes). When the apiserver caps it (`--service-account-max-token-expiration`), the check fails and gateway runs are refused with the cap named.
  - **Residual:**
    - a token taken from a sandbox is valid until it expires, only at the gateway, only on its alias and class;
    - its spend is metered to its run, because the meter charges late spend to an ended run (R10's pending lift);
    - this replaces today's residual, a provider key with no expiry inside the sandbox.

    `docs/security.md` says so.
- **GW10 (metering on the run-controller path, then the lifts).**
  - **The meter reads agentgateway's own names.** `input_cache_read` maps to `cached_input` and `input_cache_write` to `cache_creation_input` in `parseSeries`, so a plain Prometheus scrape prices runs without ANR's relabel. The relabelled names keep working.
  - **The documented query:**

    ```
    sum by (ar_agent, gen_ai_request_model, gen_ai_token_type, pod) (agentgateway_gen_ai_client_token_usage_sum{ar_agent=~"system:serviceaccount:<ns>:<prefix>.+"})
    ```

    It has a `gen_ai_client_token_usage_sum` variant for relabelled scrapes.
  - **The throttle:** `meter.throttle.promQuery` (new) names the runs a gateway 429'd lately from `meter.url`, grouped by `ar_agent`. Outside the demo, either it or the pair `logsURL` + `throttleQuery` is required; both set is refused. A lookup failure still costs only that tick's fleet mapping.
  - **`runs.gatewayIdentity: true`** (factory config) states that portable runs call the model gateway with their own token. It is valid only with `runs.group: maku.ogenki.io` (the XR always has the identity). Under it:
    - `loops.<class>.models` is accepted on the portable kind;
    - `loops.<class>.split` is accepted when a meter is configured: `meter.url` and `meter.query`, plus the existing `meter.prices` rule. The demo's meter stays optional, so a demo without one still refuses `split`, with `loops.<class>.split needs a meter: a family's cap is in USD`.
  - **Without the key, on the portable kind:** both refusals stay, with their messages renamed to `needs runs.gatewayIdentity (a model gateway, rollout step 4)`. A configured meter adds the startup warning `the meter prices no run of this runtime: its runs carry no gateway identity (runs.gatewayIdentity)`.
  - **The umbrella** fails a render where `maku-factory.config.runs.gatewayIdentity` and `maku.runController.model.mode == gateway` disagree. It also fails `model.mode: gateway` without `maku-gateway.enabled` or `maku.runController.model.gateway.service`, the external-gateway form ANR uses.
  - **This lifts step 2 PR B's ruling** (`split` refused on `maku.ogenki.io` because its runs meter no USD) once Task 17 proves a priced run.
- **GW11 (the Envoy AI gateway leaves Maku's product path).** No Maku code depends on Envoy: no `x-ai-eg-model`, no `x-ai-gateway-client-id`. What goes:
  - the comments "AI Gateway data-plane Service" (`internal/config/config.go`), which become "the model gateway (agentgateway, `maku-gateway`)";
  - the Envoy `RL` throttle query in the fixtures (`internal/app/testdata/policy-factory.yaml`, `internal/factory/config/testdata/sponsored.yaml`, `internal/factory/configsource/testdata/file.yaml`, `internal/factory/resources/testdata/file.yaml`), which becomes the agentgateway query from `resources/testdata/anr.yaml`;
  - the Envoy mentions in `docs/architecture.md`, `docs/integration.md` and `docs/api.md`.

  Nothing parses differently. Historical plans and specs are left as written.
- **GW12 (the model catalog from `meter.prices`).** `maku-gateway.modelCatalog.prices` takes the same rows as the factory's `meter.prices` and renders `catalog.json` by provider. Each row's provider is found through the aliases. A price row whose model no alias serves fails the render. This is ANR's `gen-prices.py` as chart logic. In the umbrella, the gateway's prices default to `maku-factory.config.meter.prices`, so there is one source.
- **GW13 (the gateway's own network).** It follows step 2 PR C's `networkPolicy` mode when merged, and the umbrella's `networkPolicies.plain` until then.
  - **Ingress:**
    - `public` and `internal` from the runs namespace's run pods (label `maku.ogenki.io/run-id`);
    - `clients` from the broker's pods;
    - metrics (15020) from `metrics.allowFrom`.
  - **Egress:**
    - DNS;
    - 443 to `providers[*].egress.cidrs`, default `0.0.0.0/0` except the cluster's own ranges (`egress.exceptCIDRs`);
    - the JWKS source.

    In `cilium` mode, `toFQDNs` names the providers' hosts.
  - **On the run's side,** in both modes, the run's policy admits the gateway namespace and port (`cnp.go` already does, by host). Under PR C's egress proxy, `NO_PROXY` already covers `.svc.<domain>`, so the model call bypasses the proxy, and the egress annotation never lists the gateway.

---

## PR A: the gateway chart (D7, D14)

### Task 1: the agentgateway probe (S)

**Files:**
- Create: `charts/maku-gateway/tests/probe/probe.sh`, `charts/maku-gateway/tests/probe/manifests.yaml` (agentgateway 1.6.0 and its CRDs on a throwaway k3d, Gateway API standard CRDs, ANR's echo upstream `infrastructure/aws-0/agentgateway-poc/echo-server.py` as a ConfigMap, and one Gateway).

- [ ] **Step 1: write the probe as assertions.** Each prints `PASS`/`FAIL <what>`:
  - **P1 (JWKS):**
    - a run-like ServiceAccount's projected token (audience `maku-gateway.public.tier-light`) is accepted with `source: apiserver`, both without and with the anonymous discovery binding;
    - a token for audience `room-broker` from the same ServiceAccount is refused.
  - **P2 (CEL on `jwt.aud`, a list):** `jwt.aud.exists(a, a.startsWith("maku-gateway.public."))` and `"maku-gateway.public.tier-light" in jwt.aud` both work.
  - **P3 (path and model):** `/m/tier-light/v1/chat/completions` reaches the echo upstream at the backend's path, and the upstream body's `model` is the mapped model even when the client sent `kimi-frontier`.
  - **P4 (presets):** the echo body shows `reasoning_effort: "none"` on a `mistral`-preset backend, and `temperature: 1` with no `top_p` on a `moonshot` one.
  - **P5 (metrics):**
    - `agentgateway_gen_ai_client_token_usage_sum` carries `ar_agent` equal to the sub, `gen_ai_request_model` equal to the mapped model, and `gen_ai_token_type` in `{input, output, input_cache_read, input_cache_write}`;
    - a 429 shows on a request counter carrying `ar_agent`; name the metric.
  - **P6:** the minimum Gateway API CRD version the controller accepts.
- [ ] **Step 2: run** `bash charts/maku-gateway/tests/probe/probe.sh`, and record each result in the PR A body.
  - **Stop rules:**
    - P1 failing even with the binding: `source: apiserver` ships as unsupported, and the demo uses the extAuthz fallback (GW3). Raise this with the owner before Task 3.
    - P3 failing: switch PR B to GW5's header fallback, adding a harness task.
    - P5's 429 counter missing `ar_agent`: Task 15 is dropped, and `logsURL` stays required.
- [ ] **Step 3: commit** `test(gateway): a reproducible agentgateway probe for identity, routing, presets and metrics`.

### Task 2: the chart skeleton, values, schema and presets (M)

**Files:**
- Create:
  - `charts/maku-gateway/Chart.yaml` (`kubeVersion >=1.30.0-0`);
  - `values.yaml`, `values.schema.json` (strict: `additionalProperties: false` everywhere);
  - `templates/_helpers.tpl` (`mgw.labels`, `mgw.validate`);
  - `templates/_presets.tpl`;
  - `README.md`;
  - `tests/render.sh`, `tests/golden/minimal.yaml`.

- [ ] **Step 1: failing render cases** in `render.sh`:
  - the default values render nothing;
  - `enabled: true` without `identity.issuer` fails with `identity.issuer is required: the gateway verifies each caller's token`;
  - an alias key outside the AgentRun enum (`agent-default`, `tier-light`, `tier-standard`, `tier-frontier`, `kimi-frontier`, `mistral-standard`, `mistral-light`) is rejected by the schema;
  - `aliases.internal.tier-light: {provider: moonshot}` fails with GW6's message;
  - `providers.mistral.transformations: [{field: reasoning_effort, …}]` fails with GW7's message;
  - `clients.classifier: {provider: zai, model: glm-5.3}` fails with GW7's classifier rule;
  - a provider without `existingSecret` fails, naming `providers.<name>.existingSecret`.

  Run `bash charts/maku-gateway/tests/render.sh`. Expected: FAIL (no chart).
- [ ] **Step 2: implement.** The values tree:

  ```yaml
  enabled: false
  gatewayClassName: agentgateway
  name: model-gateway
  image: {registry: cr.agentgateway.dev, repository: agentgateway, tag: "", digest: ""}  # required when enabled
  replicas: 2
  listeners: {public: 8080, internal: 8081, clients: 8082}
  audiencePrefix: maku-gateway
  identity: {issuer: "", jwks: {source: url, url: "", allowAnonymousDiscovery: false}}
  runs: {namespace: agents, namePrefix: run-}          # must equal maku.runs (umbrella rule)
  broker: {serviceAccount: room-broker}
  providers: {}   # <name>: {preset, existingSecret, key, host?, port?, path?, dataClasses?, transformations?, egress?}
  aliases: {public: {}, internal: {}}                  # <alias>: {provider, model}
  clients: {planner: {enabled: false}, classifier: {enabled: false}}  # {enabled, provider, model}
  modelCatalog: {enabled: true, prices: {}}
  timeouts: {runs: 600s, clients: 60s}
  networkPolicy: {mode: ""}                            # "", kubernetes, cilium, none
  metrics: {allowFrom: []}
  ```

  `_presets.tpl` holds GW7's table as a dict, and `mgw.validate` runs every rule.
- [ ] **Step 3:** rerun. Expected: PASS.
- [ ] **Step 4: commit** `feat(charts): maku-gateway, values and provider presets with their quirks`.

### Task 3: the Gateway, its parameters and the identity policies (M)

**Files:**
- Create:
  - `charts/maku-gateway/templates/gateway.yaml` (the Gateway and three listeners, `allowedRoutes` from the release namespace only);
  - `parameters.yaml` (ANR's `parameters.yaml` hardening: seccomp, liveness `/healthz/ready:15021`, limits, `shutdown {min: 10, max: 660}`, ClusterIP, PDB `minAvailable: 1`, zone and host spread, the catalog source);
  - `policies-identity.yaml` (`strip-identity-headers`, `listener-public`, `listener-internal`, `listener-clients`, `frontend`);
  - `jwks.yaml` (`source: apiserver`: the Backend, the BackendTLSPolicy, and the binding under `allowAnonymousDiscovery`).

- [ ] **Step 1: failing render cases:**
  - each listener's policy requires GW3's sub regex, built from `runs.namespace` and `runs.namePrefix` with `regexp.QuoteMeta`'s escaping;
  - the clients listener requires the broker's exact sub;
  - each listener requires its audience prefix;
  - `preserveToken` is absent or false on every listener;
  - `frontend` adds `ar_agent` to metrics and `x_ar_agent` and `cost` to the access log, all from `jwt.sub`;
  - `strip-identity-headers` removes `x-ar-agent`, `x-ar-human`, `x-ai-gateway-client-id` and `agent-session-id`;
  - `source: apiserver` without `allowAnonymousDiscovery` fails when Task 1 found the binding necessary; otherwise it renders no binding;
  - `image.digest` empty fails.
- [ ] **Step 2: implement**, copying ANR's comments where they explain a choice (the 429 body, the 600 s timeout, the 8Mi buffer).
- [ ] **Step 3:** rerun. Expected: PASS.
- [ ] **Step 4: commit** `feat(gateway): the Gateway and its listeners verify each caller's projected token`.

### Task 4: providers, alias routes, client routes and the catalog (M)

**Files:**
- Create: `charts/maku-gateway/templates/backends.yaml` (one `AgentgatewayBackend` `<provider>-<alias>` per alias row and per client, each with `auth.secretRef {name: existingSecret, key}`, its preset and user transformations, and a forced `model`), `routes.yaml` (HTTPRoutes `maku-models-public`, `maku-models-internal` and `maku-clients`, with one rule per alias: PathPrefix `/m/<alias>/`, URLRewrite, and a `models` Exact rule answered 404 as ANR does), `policies-routes.yaml` (one `Require` per rule: the alias audience), `catalog.yaml` (GW12).
- Modify: `charts/maku-gateway/tests/render.sh`, `tests/golden/cnref-like.yaml` (zai, anthropic, moonshot and mistral, with the planner on zai `glm-5.3`).

- [ ] **Step 1: failing render cases:**
  - `aliases.public.kimi-frontier: {provider: moonshot, model: k3.1-pro}` renders backend `moonshot-kimi-frontier` with `modelAliases {kimi-frontier: k3.1-pro}`, the forced-model transformation, and the preset's `temperature` and `top_p`;
  - its rule matches `/m/kimi-frontier/` and requires audience `maku-gateway.public.kimi-frontier`;
  - no route serves an alias absent from that listener's rows;
  - the planner's route forces `tier-frontier` → `glm-5.3`, and its policy requires `maku-gateway.client.planner`;
  - a price row for a model no alias serves fails;
  - the catalog's JSON equals ANR's `model-catalog.yaml` for the cnref-like values (golden).
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(gateway): one backend and one route per alias, model forced, providers' keys by name`.

### Task 5: the umbrella subcharts, the CRDs, the network, the release and the k3d proof (M)

**Files:**
- Modify:
  - `charts/agent-factory/Chart.yaml` and `Chart.lock` (dependencies `maku-gateway`, file and `condition: maku-gateway.enabled`; `agentgateway` 1.6.0, oci and `condition: agentgateway.enabled`);
  - `charts/agent-factory/values.yaml` (`maku-gateway: {enabled: false}`, `agentgateway: {enabled: false}`) and `values.schema.json`;
  - `charts/agent-factory/templates/_helpers.tpl` (rules: `maku-gateway.runs` equals `maku.runs`; `maku-gateway.broker.serviceAccount` equals the broker's);
  - `charts/agent-factory-crds/Chart.yaml`, `Chart.lock` and `values.yaml` (`agentgateway-crds`, `condition: agentgateway-crds.enabled`, default false);
  - `charts/maku-gateway/templates/networkpolicy.yaml` (create, GW13);
  - `.github/workflows/ci.yaml` and `release.yaml` (publish `maku-gateway`, and bump its version with the others);
  - `.github/workflows/ghcr-cleanup.yaml`;
  - `Taskfile.yml` (`chart:deps`; `chart:gateway:k3d`);
  - `charts/agent-factory/tests/render.sh`, `charts/agent-factory-crds/tests/`;
  - `docs/gateway.md` (create: the seam, the values, the presets table, GW3–GW9);
  - `charts/agent-factory/README.md`.

- [ ] **Step 1: failing tests:**
  - the umbrella's default and demo renders are byte-identical to `origin/main` (diff);
  - `--set maku-gateway.enabled=true` with the cnref-like values renders the gateway objects in the release namespace;
  - a `runs.namespace` mismatch fails, naming both keys;
  - `agent-factory-crds` with `agentgateway-crds.enabled=true` renders the upstream CRDs and nothing else changes;
  - **k3d** (`task chart:gateway:k3d`): the upstream CRDs and controller, the umbrella with the gateway on, the e2e stub as a `custom` provider over http. A pod with a projected token for `maku-gateway.public.tier-light` gets 200 on `/m/tier-light/v1/chat/completions`, 403 on `/m/tier-frontier/…`, and 401 with no token.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS. Run `task check`.
- [ ] **Step 3: commit** `feat(charts): the gateway as an optional umbrella subchart, with the upstream controller and CRDs`. Open PR A with GW2–GW7, GW12, GW13 and the probe results in the body.

## PR B: run-controller runs through the gateway (D7)

### Task 6: the run-controller's `model.mode` (S)

**Files:**
- Modify: `internal/runctl/config.go`:
  - `Model` gains `Mode` (`secret`, the default, or `gateway`);
  - `Gateway {URL, Ports {Public, Internal}, AudiencePrefix, Aliases {Public, Internal []string}}`;
  - `modelSecret` is required only in `secret` mode, and refused in `gateway` mode;
  - `Gateway.URL` must match `brokerHostRE`'s in-cluster form over http.
- Modify: `internal/runctl/config_test.go`.

- [ ] **Step 1: failing tests:**
  - `mode: gateway` with `modelSecret` set fails with `model.mode gateway and modelSecret: choose one (the gateway holds the keys)`;
  - without `gateway.url` it fails;
  - an https or non-`.svc` URL fails;
  - `secret` mode is unchanged (the existing cases pass).

  Run `go test -race -count=1 ./internal/runctl/ -run Config`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): model.mode gateway in the run-controller's config`.

### Task 7: the pod in gateway mode; `internal` and aliases admitted (M)

**Files:**
- Modify:
  - `internal/runctl/pod.go`: in gateway mode there is no `model` Secret volume. A projected `model-token` volume carries `token` and, with a judge, `judge-token` (GW3, GW9's TTL), mounted at `/run/agent/model`. The env becomes `MODEL=spec.model`, `LLM_BASE_URL=<url>:<port>/m/<alias>/v1`, `LLM_API_KEY_FILE=/run/agent/model/token`, and, with a judge, `JUDGE_BASE_URL`, `JUDGE_MODEL=spec.loop.judgeModel` and `JUDGE_API_KEY_FILE=/run/agent/model/judge-token`;
  - `internal/runctl/config.go` (`Invalid`): `dataClass: internal` is admitted in gateway mode when `Aliases.Internal` is set. `spec.model` or a judge alias missing from the class's aliases is refused with `spec.model <alias> is not served on the <class> listener (maku-gateway aliases.<class>)`;
  - `internal/runctl/reconciler.go` (`inputs`): gateway mode builds `Model` from config, with the host and the class's port, so `cnp.go` admits it unchanged;
  - `api/v1alpha1` (the descriptions of `spec.model` and `spec.loop.judgeModel`, then `task crd:gen`);
  - `internal/runctl/testdata/pod-gateway.golden.yaml` (create), `render_test.go`, `judge_test.go`.

- [ ] **Step 1: failing tests:**
  - **The golden:** a public implementer with `tier-standard` and judge `tier-light` gets two token sources with exactly those audiences and `expirationSeconds = deadline + 600`, no Secret volume, and the env above;
  - **an internal run:** port 8081 and audience `maku-gateway.internal.tier-standard`;
  - **refusals:** an internal run with no internal aliases is refused with the old message plus the key, and `kimi-frontier` on an internal run is refused;
  - **the CNP:** it names the gateway's namespace and the class's port;
  - **secret mode:** byte-identical to today's golden.

  Run `go test -race -count=1 ./internal/runctl/`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): runs call the gateway with their own projected token, on their claim's alias`.

### Task 8: the harness reads the judge's own token (S)

**Files:**
- Modify: `images/maku-harness/agent_run.py` (the judge's key: `JUDGE_API_KEY_FILE`, else `LLM_API_KEY_FILE`, read at the judge's call), `images/maku-harness/README.md` (the optional-environment row), `images/maku-harness/tests/` (the judge cases).

- [ ] **Step 1: failing test:** with `JUDGE_API_KEY_FILE` set, the judge's request carries that file's content as its bearer, and the run's own call carries `LLM_API_KEY_FILE`'s. Without it, the behaviour is today's. Run `task harness:test`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(harness): the completion judge may use its own key file`.

### Task 9: admission, preflight and network for gateway runs (M)

**Files:**
- Modify:
  - `charts/maku/templates/run-pod-policy.yaml`: a projected `serviceAccountToken` audience is only `room-broker`, `room-tools`, or one starting `<audiencePrefix>.`. Under gateway mode, no Secret volume names `modelSecret`;
  - `charts/maku/tests/admission/runpod_test.go` (envtest cases);
  - `internal/runctl/preflight.go` and its tests: the `gateway-token-ttl` check (GW9), with its RBAC `serviceaccounts/token: create` on the controller's own ServiceAccount (`resourceNames`), in `charts/maku/templates/run-controller-rbac.yaml`;
  - `internal/runctl/cnp.go` (unchanged logic; a test that the gateway host gets `toEndpoints` by namespace on 8080 or 8081);
  - if step 2 PR C has merged: its per-run NetworkPolicy admits the gateway namespace and port, and the egress annotation omits it.

- [ ] **Step 1: failing tests:**
  - **envtest:** a run pod whose token audience is `https://kubernetes.default.svc` is denied, and `maku-gateway.public.tier-light` is allowed;
  - **the preflight:** a fake TokenRequest that answers a shorter expiry fails `gateway-token-ttl` with `the apiserver caps ServiceAccount tokens at <d>; gateway runs need <d'>`;
  - **the CNP:** the case above.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl,charts): gateway runs pass admission, the preflight checks the token cap`.

### Task 10: the chart, the umbrella rules, the gateway demo and its proof (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml` (`runController.model: {mode: secret, gateway: {service: "", namespace: "", ports: {public: 8080, internal: 8081}, audiencePrefix: maku-gateway, aliases: {public: [], internal: []}}}`), `values.schema.json`, `templates/run-controller-config.yaml`, `tests/render.sh`, `tests/loadconfig/main.go`;
  - `charts/agent-factory/templates/_helpers.tpl` (rules: `maku.runController.model.mode: gateway` needs `maku-gateway.enabled` or `model.gateway.service`; the aliases written into the run-controller's config are `maku-gateway.aliases`' keys, with the render failing on a mismatch; GW10's `runs.gatewayIdentity` agreement);
  - `charts/agent-factory/templates/secrets.yaml` (with the gateway on, `demo.model.apiKey` renders the demo provider's Secret in the release namespace instead of `agent-model`);
  - `charts/agent-factory/values-demo-gateway.yaml` (create: layered on `values-demo.yaml`; the gateway, the controller, `source: apiserver` and `allowAnonymousDiscovery`; one `custom` provider from `demo.model.baseURL`; every alias the demo's tiers name, mapped to `demo.model.name`; the judge alias to `demo.model.judgeName`);
  - `charts/agent-factory/tests/install.sh` (a `gateway` case), `.github/workflows/demo-e2e.yaml` (a gateway leg);
  - `docs/gateway.md`, `docs/security.md` (GW3, GW9 and the removed key), `docs/upgrading.md`, `charts/maku/README.md`.

- [ ] **Step 1: write the assertions first.**
  - **Render:**
    - `-f values-demo.yaml` is byte-identical;
    - `-f values-demo.yaml -f values-demo-gateway.yaml` renders no `agent-model`, a provider Secret in the release namespace, and `model.mode: gateway`;
    - gateway mode with no gateway fails, naming both keys.
  - **`install.sh gateway` on k3d:**
    - the demo task's implementer and reviewer runs reach `Succeeded` through the gateway;
    - `kubectl -n agents get secret agent-model` is NotFound;
    - the gateway's access log shows `x_ar_agent=system:serviceaccount:agents:run-…` for each run.
  - **The nightly real-model leg:** the same, with a real key.

  Run `bash charts/agent-factory/tests/install.sh gateway`. Expected: FAIL.
- [ ] **Step 2: implement and document.**
- [ ] **Step 3:** run `task check` and `task chart:install:k3d`. Expected: PASS.
- [ ] **Step 4: commit** `feat(demo): a gateway demo where no provider key reaches the runs namespace`. Open PR B with GW3, GW5, GW6 and GW9 and the strict-decoding note.

## PR C: the planner and the classifier through the gateway (D7)

### Task 11: the broker's `modelGateway` (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml` (`broker.modelGateway: {service: "", namespace: "", port: 8082, audiencePrefix: maku-gateway}`), `values.schema.json`;
  - `templates/broker-deployment.yaml` (the projected volume `model-gateway` with `planner` and `classifier`, 3600 s, mounted read-only at `/var/run/secrets/maku/model-gateway`);
  - `templates/broker-config.yaml` (GW8's fill-when-unset into `human.intake.plan` and an existing `human.slackIntents`);
  - `templates/broker-cnp.yaml` (egress to the gateway's namespace on `port`);
  - `tests/render.sh`, `tests/loadconfig/main.go` (`broker-planner-gateway`, `broker-slackintents-gateway`).

- [ ] **Step 1: failing render cases:**
  - with `modelGateway.service` set and `human.intake.plan.enabled: true` but no `url`, the config gets the clients URL and the token path, and the loadconfig decode passes;
  - a block that sets its own `url` keeps it, and keeps its `apiKeyFile`;
  - `slackIntents` absent stays absent;
  - unset `modelGateway` renders byte-identically to `origin/main`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the broker reaches the model gateway with a projected token per client`.

### Task 12: the umbrella wiring and the quirks proved end to end (M)

**Files:**
- Modify:
  - `charts/agent-factory/templates/_helpers.tpl` (with `maku-gateway.clients.planner.enabled`, `maku.broker.modelGateway.service` must name the gateway; each client needs its provider);
  - `charts/agent-factory/tests/intake-values.yaml` and `intake.sh` (the k3d spec-intake test moves its planner from the stub's direct URL to the gateway's clients listener, with the stub as a `custom` provider);
  - `charts/agent-factory/tests/e2e-stub.yaml` (the stub echoes the received body under a test header, so the test can read what the provider got);
  - `charts/maku-gateway/tests/render.sh`.

- [ ] **Step 1: write the assertions first** (`task chart:intake:k3d`):
  - the plan is produced through `/clients/planner/v1`;
  - the stub saw `model` equal to the mapped model, and no `Authorization` of the broker's token (removed before the backend);
  - the same run with the stub declared `preset: mistral` shows `reasoning_effort: "none"` though the broker sent `low`;
  - with `preset: moonshot` the stub saw `temperature: 1` and no `top_p` though the broker sent 0;
  - the classifier (`slackIntents` on, shadow) classifies one reply through `/clients/classifier/v1`;
  - a call to `/clients/planner/` with the classifier token answers 403.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `test(intake): the planner and the classifier through the gateway, provider quirks applied`.

### Task 13: the Envoy AI gateway leaves the product path (S)

**Files:**
- Modify: `internal/config/config.go` (comments, GW11), the four config fixtures named in GW11 and the tests that read them, `docs/architecture.md`, `docs/integration.md`, `docs/api.md`, `docs/slack.md` (the classifier's route), `docs/gateway.md` (the planner and classifier section, cnref's tier-frontier mapping as the example).

- [ ] **Step 1:** `git grep -n -i -E 'envoy ai gateway|ai-gateway|x-ai-eg|response_flags:~"RL"' -- internal docs charts ':!docs/superpowers'` lists exactly the lines to change. Change them, and keep every test green: `go test -race -count=1 ./internal/config/ ./internal/app/ ./internal/factory/config/ ./internal/factory/configsource/ ./internal/factory/resources/`.
- [ ] **Step 2:** rerun the grep. Expected: no match outside `docs/superpowers`.
- [ ] **Step 3: commit** `docs: agentgateway is the one model gateway (D7); fixtures read its throttle query`. Open PR C with GW7, GW8 and GW11.

## PR D: metering on the run-controller path; lifting the refusals

### Task 14: the meter reads agentgateway's token types (S)

**Files:**
- Modify: `internal/factory/meter/vm.go` (`parseSeries` normalizes `input_cache_read` to `cached_input` and `input_cache_write` to `cache_creation_input` before `countedType`), `internal/factory/meter/series.go`, `internal/factory/meter/testdata/query_reply_agentgateway.json` (create: a recorded P5 reply), `vm_test.go`.

- [ ] **Step 1: failing tests:**
  - `TestCostFollowsTheAgentgatewaySeries` prices the recorded reply identically to the relabelled fixture;
  - a portable subject (`system:serviceaccount:agents:run-cf4ato2x`) is read under the portable kind and skipped under the XR kind;
  - `reasoning` is still not counted.

  Run `go test -race -count=1 ./internal/factory/meter/ -run 'TestCostFollows|TestVM'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(meter): read agentgateway's own cache token types`.

### Task 15: a PromQL throttle source (S)

Dropped by Task 1's stop rule when P5 found no 429 counter carrying `ar_agent`.

**Files:**
- Modify: `internal/factory/config/config.go` (`Meter.Throttle {PromQuery}`; outside the demo, one source is required, and both set is refused), `internal/factory/meter/vm.go` (`VM.RecentlyThrottled` over `promQuery`, keyed by `ar_agent`), the wiring in `internal/app`, and the tests.

- [ ] **Step 1: failing tests:**
  - **Config:** neither source outside the demo fails with `meter.throttle.promQuery, or meter.logsURL with meter.throttleQuery, is required`; both fails with `choose one`.
  - **The meter:** the reply `{ar_agent: <run subject>, value: 3}` names that run.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(meter): the gateway's 429s from PromQL, beside VictoriaLogs`.

### Task 16: `runs.gatewayIdentity`, and the refusals lifted (S)

**Files:**
- Modify:
  - `internal/factory/config/config.go`: `Runs.GatewayIdentity`. The two refusals at `:2353–2361` are conditioned on it, with GW10's messages. The `split` rule needs a configured meter on the portable kind. There is the startup warning, and `gatewayIdentity` on the XR group is refused;
  - `internal/factory/config/loops_test.go` and `scope_test.go` (the two existing tests are rewritten, and the new ones are added);
  - `charts/maku-factory/values.yaml` (documented, not defaulted), `charts/maku-factory/tests/render.sh`.

- [ ] **Step 1: failing tests:**
  - portable plus `gatewayIdentity` accepts `models: {reviewer: {frontier: kimi-frontier}}`;
  - with `meter.url`, `query` and `prices` it accepts `split`;
  - portable plus `gatewayIdentity` with no meter refuses `split` (`needs a meter`);
  - portable without the key refuses both with the renamed messages;
  - `gatewayIdentity` with `runs.group: cloud.ogenki.io` is refused;
  - the warning is present exactly when portable, without the key and with a meter set.

  Run `go test -race -count=1 ./internal/factory/config/ -run 'Loops|Scope|Split|GatewayIdentity'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): runs with a gateway identity may pin provider models and split (lifts step 2's refusal)`.

### Task 17: the metering proof and the docs (M)

**Files:**
- Create: `charts/agent-factory/tests/metrics-stub.yaml` (test-only single-binary VictoriaMetrics, pinned by digest, scraping the gateway pods' `metrics` port).
- Modify:
  - `charts/agent-factory/values-demo-gateway.yaml` (`maku-factory.config.runs.gatewayIdentity: true`; the meter commented, set by the test);
  - `charts/agent-factory/tests/install.sh` (a `metering` case);
  - `.github/workflows/demo-e2e.yaml` (the nightly asserts USD);
  - `docs/factory.md` (the `loops` row: the portable-kind sentences become "with `runs.gatewayIdentity`"; the `meter.query` row gains the agentgateway query; a new `runs.gatewayIdentity` row; `meter.throttle.promQuery`);
  - `docs/operations.md`, `docs/quickstart.md` (the "No meter and no USD caps" row names the gateway demo), `docs/upgrading.md`, `docs/gateway.md`.

- [ ] **Step 1: write the assertions first.**
  - **`install.sh metering`** (k3d, the stub's `usage` answer and a `meter.prices` row for the stub model, `split` on the `review` class with `anyTier: true`):
    - the config loads with `split` on the portable kind;
    - after the implementer run, `maku_factory_run_settled_usd{run=<id>}` > 0 on the factory's metrics;
    - the run's ledger entry exists;
    - the room's `spend.usedMicroUSD` > 0.
  - **The nightly (real model):** the same three, and `spendFinal` arrives.

  Expected: FAIL.
- [ ] **Step 2: implement and document.**
- [ ] **Step 3:** run `task check` and `task chart:install:k3d`. Expected: PASS.
- [ ] **Step 4: commit** `feat(demo): metered runs on the run-controller path, proved on k3d and nightly`. Open PR D with GW10, the lifted ruling, and the strict-decoding note. Cut the step's v0.4x minor after it merges.

## After this step (not in these PRs)

- **Owner decisions:**
  - **The demo's default:** keep `values-demo.yaml` on the direct `agent-model` Secret (D25), or make the gateway demo the default in step 7 with `maku install --print`. Recommended: step 7. Step 8 then either deletes `model.mode: secret`, or keeps it as demo-only and flags it unsafe on the install banner (PR9's list gains `model_key_in_sandbox`).
  - **`llm-platform` on cnref:** the Envoy AI gateway also fronts OpenWebUI, promptfoo and the vLLM semantic router (`clusters/*-ai-gateway`, the `B3`–`B5` shadow budgets). Dropping it either retires that lab, or moves it behind a `maku-gateway` `custom` provider and a third listener. Either is ANR's choice, outside the product.
  - **The classifier on cnref:** turn `human.slackIntents` on (shadow) once its route exists, and choose its provider (GW8's data note).
- **ANR follow-ups** (each its own PR, after the matching Maku release; each recorded with a gcp-0 live check in the session ledger):
  1. **The gateway as values.** Add `OCIRepository`/`HelmRelease` `maku-gateway`, with `agentgateway.enabled` left to `infrastructure/base/agentgateway` (the controller stays ANR's, in `agentgateway-system`). Its values carry:
     - `identity` from `${oidc_issuer_url}`/`${oidc_jwks_uri}`;
     - `runs` as the run-controller's;
     - providers `zai` (`preset: zai`, `agents-zai-api-key`), `anthropic` (internal), `moonshot` (`agents-moonshot-api-key`) and `mistral` (`agents-mistral-api-key`), whose ExternalSecrets stay ANR's (D14);
     - `aliases.public` and `aliases.internal`, as `llm.yaml`'s table;
     - `modelCatalog.prices` from the factory's `meter.prices`, retiring `scripts/ci/gen-prices.py`.

     Mistral's hand-written `reasoning_effort` transformation goes, because the preset carries it. Live check: a curl with a run-like token through each alias, and the catalog's `cost` in the access log.
  2. **Runs on the gateway, the overrides back.** Set `runController.model.mode: gateway` and `runs.gatewayIdentity: true`. Restore the suspended `loops.<class>.models` rows (Kimi on the reviewer for `docs-links`, `revert`, `dead-links` and `deps-patch`, and the Mistral rows) in `tooling/base/agent-factory/resources`. Retarget the meter:
     - the query to the `maku-gateway` pods: native names, or `vmpodscrape.yaml` retargeted to `gateway-name: model-gateway`;
     - `throttleQuery` to the new Gateway's name and namespace.

     Delete `agent-model` from the runs namespace. Live check:
     - a public docs task's reviewer runs on `kimi-frontier`;
     - the room shows `spend.usedMicroUSD` > 0;
     - `maku_factory_run_settled_usd` matches the access log's `cost` within the cross-check's tolerance;
     - one `split` proposal on a frontier task is accepted.
  3. **The planner off the Envoy AI gateway.** In `infrastructure/{base,aws-0,gcp-0}/room-broker/config.yaml`, set `human.intake.plan.url` to `http://model-gateway.<ns>.svc.cluster.local:8082/clients/planner/v1` and `apiKeyFile` to the projected token. `app.yaml` mounts the projected volume (audience `maku-gateway.client.planner`) in place of `room-broker-planner`. Retarget `network-policy.yaml`'s egress. Delete:
     - `externalsecret-planner.yaml`;
     - the envoy-ai-gateway `externalsecret-planner-key.yaml` and its `credentialRefs` entry;
     - the planner rule in `infrastructure/base/envoy-gateway/network-policy.yaml`.

     Set `clients.planner: {provider: zai, model: glm-5.3}`, and keep `reasoningEffort: low`. Live check: an `intake_plan` from Slack, and the access log's `x_ar_agent` is the broker's ServiceAccount.
  4. **The Envoy AI gateway deleted** (D22), once 3 holds. Delete:
     - `infrastructure/base/envoy-ai-gateway`, `infrastructure/aws-0/envoy-ai-gateway` and the gcp-0 overlay;
     - `infrastructure/base/llm-gateway`;
     - `clusters/{aws-0,gcp-0}-ai-gateway/infrastructure-{envoy-ai-gateway,llm-gateway}.yaml` and their kustomization entries;
     - `flux/sources/ocirepo-envoy-ai-gateway{,-crds}.yaml`;
     - `apps/base/ai/llm/ai-gateway-routes`;
     - the OpenBao `planner` entries.

     The rest of the ai-gateway umbrella follows the owner's `llm-platform` decision above.
  5. **ANR's own agentgateway objects shrink to what the chart does not render.** Delete `infrastructure/base/agent-gateway/{llm,anthropic,moonshot,mistral,model-catalog,policy-capabilities}.yaml` and their generators once no XR run is live. `mcp.yaml`, `github.yaml`, the budgets (`ratelimit.yaml`, `kvstore.yaml`, `policy-budgets.yaml`) and their Gateway stay ANR's until a later step decides on MCP federation and gateway-side budgets for the product.
- **Later Maku steps:**
  - **Step 5** brings `externalSecrets.enabled` to the providers' Secrets.
  - **Step 6** adds the gateway's ServiceMonitor, its PrometheusRule (including ANR's `AgentRouterMetricContractBroken` guard) and its dashboard.
  - **Step 7:**
    - `maku preflight` checks the Gateway API CRDs, the controller, the JWKS fetch, a token round trip per alias, and `gateway-token-ttl`;
    - `maku install --print` asks for the providers and their Secrets.
  - **Step 8:**
    - deletes `meter.logsURL`'s Envoy defaults in the docs;
    - flips `model.mode` per the owner's demo decision;
    - deletes the refusal messages kept for portable runs without `gatewayIdentity`, if the gateway becomes mandatory outside the demo.

## Self-review

- **D7:**
  - agentgateway in the umbrella as optional subcharts: Maku's objects and the upstream controller, with the CRDs in the CRD chart (Tasks 2–5, GW2);
  - it serves the agents (Tasks 6–10), the planner and the classifier (Tasks 11–12);
  - the Envoy AI gateway is gone from Maku's product path (Task 13, GW11) and from ANR (follow-ups 3–4).
- **D14:** each provider is `existingSecret` plus `key`, with no plaintext key outside the demo values (Task 2); no provider key in the runs namespace in gateway mode (Task 10).
- **Provider quirks:** Mistral's `reasoning_effort`, Kimi's temperature and `top_p`, and the always-reasoning models, held as presets that a user cannot silently undo (GW7, Task 2), proved on the wire (Tasks 1 and 12).
- **The per-run identity and metering:**
  - a projected token per run, with the subject the meter already reads (GW3, Task 7);
  - aliases pinned by audience (GW5);
  - native token types (Task 14), a throttle without VictoriaLogs (Task 15);
  - a priced run proved on k3d and nightly (Task 17).
- **The lifts:**
  - `loops.models` and `split` on the portable kind under `runs.gatewayIdentity` (Task 16, GW10);
  - `dataClass: internal` on the run-controller under an internal listener (Task 7, GW6);
  - step 2 PR B's USD refusal, lifted after Task 17's proof.
- **D25:**
  - every switch defaults off;
  - default and demo renders are diffed byte-identical (Tasks 5, 7, 10, 11);
  - the explicit `url` and `apiKeyFile` in the broker config win;
  - `logsURL` is still accepted;
  - no v4.
- **D26:**
  - Kubernetes ≥1.30 in `maku-gateway`'s `kubeVersion`;
  - the Gateway API floor is recorded by the probe (P6);
  - Helm ≥3.15 handles the OCI dependency and `Chart.lock`.
- **Risks named with a stop rule:**
  - **The JWKS cannot be fetched in-cluster:** P1 decides between the anonymous discovery binding and the extAuthz fallback before Task 3.
  - **Path rewrite or forced model broken on 1.6:** P3 switches PR B to the header fallback, with a harness task.
  - **The apiserver caps token lifetime under a run's deadline:** the preflight refuses gateway runs and names the cap (Task 9).
  - **The upstream chart's OCI dependency in offline CI:** `Chart.lock` and `task chart:deps`, with CI failing on drift (Task 5).
  - **The classifier on a reasoning model:** refused at render (GW7).
  - **The nightly** goes red until the release (strict decoding).

