# v4 rollout step 2, the run path: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Maku's run-controller becomes a run path a production cluster can trust, with nothing from cloud-native-ref (cnref) under it:
- the scoper and the loop policies work on `maku.ogenki.io` runs; the Crossplane AgentRun XR is no longer required (D5);
- a new `kubernetes` network mode uses plain `networking.k8s.io/v1` policies plus an in-cluster egress proxy that enforces each run's FQDN allow-list. A preflight refuses runs when the CNI does not enforce NetworkPolicy (D6);
- the sandbox RuntimeClass is a value with a preflight. `unsafeAllowRunc` is the demo's escape hatch, and the install says so wherever people look (D18);
- the chart renders a ResourceQuota on the runs namespace (D16);
- ValidatingAdmissionPolicies hold the run pod's shape: the sandbox class, a restricted securityContext, no hostPath, limits set, the image allow-list (D17).

Every existing install keeps working (D25):
- `networkPolicies.enabled`, `runController.runtimeClassName` and `runController.allowUnsandboxed` keep their meaning;
- the demo keeps rendering `networkPolicy: none`;
- ANR's XR install (`runController.enabled: false`) renders no new object.

**Architecture:**
- Everything new lives in the `maku` chart and the run-controller image. That is where the run path already lives, and it is the chart ANR consumes.
- The run-controller gains three things:
  - a preflight runnable, which checks the RuntimeClass and, in `kubernetes` mode, runs a probe pod that proves a denied connection is denied;
  - a per-role git Secret: reviewer and triager get a read-only token;
  - a `kubernetes` mode. Each run gets a NetworkPolicy, plus `HTTPS_PROXY` and an egress annotation the controller alone writes.
- The same binary gains two subcommands:
  - `egress-proxy`, a CONNECT-only forward proxy. It maps a client's pod IP to its run's annotation;
  - `netprobe`, which the preflight's probe pod runs.
- The factory's config stops requiring the XR. The demo's git token writer becomes a run token writer that keeps a write Secret and a read Secret fresh. That writer is a bridge until the per-run minter (D10, step 3).

**Tech Stack:**
- Go: controller-runtime, client-go, net/http's CONNECT handling;
- Helm 3.14+ templates and a JSON Schema for the maku chart's new blocks;
- ValidatingAdmissionPolicy v1 (Kubernetes ≥1.30), proved on envtest 1.36;
- the bash render tests (`yq`);
- TypeScript and vitest for the banner;
- kind with Cilium plus gVisor (quickstart-e2e), and k3d with kube-router (demo-e2e).

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D5 (run path), D6 (network), D16 (quota), D17 (admission), D18 (sandbox);
- D25 (additive rollout), D26 (Kubernetes ≥1.30);
- "Rollout", step 2.

**Branch:** three PRs, each from `origin/main` once the previous one merges. Step 1 (`feat/v4-chart-contract`) must be merged first.

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, sandbox and admission (D18, D16, D17) | `feat/v4-run-path-sandbox` | 1–6 | L | first: every later pod must pass its policy |
| B, scoper and loops (D5) | `feat/v4-run-path-scoper` | 7–10 | M | second |
| C, network (D6) | `feat/v4-run-path-network` | 11–16 | L | third: its probe pod and proxy use A's preflight and policy |

The step's v0.4x minor is cut after PR C. A and B are additive and may also ship in interim minors.

## Global Constraints

- **No v4.** This step ships as a v0.4x minor. No v4 tag, release or chart version is created.
- **Additive (D25).**
  - Every new object in the `maku` chart renders only when `runController.enabled` is true (PR11), so ANR's XR install renders exactly what it renders today. Prove this with a render case using `runController.enabled=false`, diffed against `origin/main`'s render.
  - Old keys keep working: `networkPolicies.enabled`, `runController.runtimeClassName`, `runController.allowUnsandboxed`, `runController.createRuntimeClass`. Where an old and a new key conflict, the render fails with a message naming both.
  - `-f values-demo.yaml` keeps network mode `none` and the unsandboxed runtime. Its render changes only by the new objects listed in Task 6 Step 4.
- **Fail closed.** A new safety value defaults to the safe side:
  - `sandbox.unsafeAllowRunc: false`;
  - `runController.unsafeSharedGitToken: false`;
  - `runsQuota.enabled: true`;
  - `runPodPolicy.enabled: true`.
  
  Each unsafe setting shows on the install banner (Task 6).
- **Support floor (D26):** the VAPs use `admissionregistration.k8s.io/v1` only, and nothing needs anything newer than 1.30.
- **Strict decoding.** The run-controller and broker configs are decoded strictly, so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until the release (its header already says so). Name this in each PR body.
- `task check` before the last commit of each PR. Never `HOME=…` or `env -i`. No attribution lines. Conventional commits in English.

## Rulings made while planning

- **PR1 (what the XR gives that the run-controller lacks).** The two refusals in `internal/factory/config/config.go` (`:2283–2287`, `:2307–2314`) rest on three gaps:
  1. **A read-only token.** cnref maps the triager role to octo-sts policy `agent-triager.sts.yaml`. The same holds for the reviewer: `contents: read`, while tester and implementer write. The run-controller mounts one `gitSecret` for every role (`internal/runctl/pod.go`, volume `github`), so a "read-only" scoper could push. `rules.md` only *asks* it not to.
  2. **A path for the handoff.** The scoper ends with `room_handoff` (`images/maku-harness/agent_run.py:109`), a room tool. On the XR it goes through the gateway. On the run-controller it exists only when `roomToolsURL` is set (`broker.roomTools.auth: tokenreview`).
  3. **Resolving model aliases.** On the XR the gateway resolves the judge alias (`tier-light`) and `loops.<class>.models` provider aliases. The run-controller ignores `spec.model` (3a R7) and points the judge at the run's own model.

  `spec.loop` itself is not a gap: the portable CRD carries it, and `runctl.Pod` turns it into `AGENT_LOOP` (the demo already uses this). The message "this runtime's has no spec.loop" is stale.

  Step 2 closes gaps 1 and 2 in the run-controller (Tasks 7, 10). It narrows gap 3 with an optional `judge_model` key in the model Secret (Task 8). It refuses `loops.<class>.models` on the portable kind until agentgateway arrives in step 4 (Task 9, PR4).
- **PR2 (read-only is enforced where the token is chosen).** The run-controller picks the git Secret, so it enforces the role:
  - reviewer and triager mount `gitReadSecret`;
  - implementer and tester mount `gitSecret`.
  
  With no `gitReadSecret` and `unsafeSharedGitToken: false`, a reviewer or triager run is refused. A scoper run (a triager whose task starts with `runs.ScoperMarkerPrefix`) is refused without `roomToolsURL`. The factory's config-time refusal is deleted: it guarded a runtime it cannot see. One check now sits where the guarantee is made. `internal/app/runkind_test.go` drops its `config.go` exemption, because `config.go` no longer compares against `runwatch.DefaultKind`.
- **PR3 (an interim token writer).** cnref's switch off the XR (the ANR follow-up) needs the run Secrets filled without octo-sts. `internal/factory/demo/gittoken.go` becomes `internal/factory/runtoken`:
  - it keeps two Secrets fresh: `agent-git` (contents and pull_requests write) and `agent-git-read` (contents, pull_requests, issues, checks and actions read);
  - in the demo it mints from the factory App, as today;
  - with `runs.gitTokens.enabled` it mints from the App named by `runs.gitTokens.appIDFile`/`privateKeyFile`, which cnref points at its agents App.
  
  The token covers every served repository of one owner, the demo's constraint. D10's per-run, per-repository minter replaces this in step 3.
- **PR4 (`loops.models` is refused on the portable kind).** It was silently ignored there. It is now refused with `loops.<class>.models needs a model gateway (rollout step 4): this runtime runs every role on modelSecret's model`. No shipped config sets it on the portable kind. cnref's Kimi and Mistral overrides are suspended and move to agentgateway values in step 4.
- **PR5 (the egress proxy is a small Go CONNECT proxy in the run-controller image).** Alternatives weighed:
  - **Squid:** per-source ACLs mean regenerating its config and reloading it on every run start;
  - **tinyproxy:** no per-client host lists;
  - **Envoy's dynamic forward proxy:** per-run RBAC needs an xDS control plane.

  The Go proxy is about 300 lines, and the same reviewers maintain it. It adds no image: it reuses the run-controller image, its signing and its digest pin. It reads the allow-list from the run pod's `maku.ogenki.io/egress` annotation (`host:port,…`):
  - the run-controller computes the annotation at create, from the same `FQDNs` plus the model host the CNP uses;
  - the VAP makes it controller-only at CREATE and immutable on UPDATE (Task 4).
  
  The proxy identifies a client by its source pod IP, through a watch on run pods:
  - unknown, terminating or duplicate IPs are refused;
  - IP literals and resolved non-public addresses are refused;
  - only CONNECT is served.
  
  Its log lines and its 403 match the harness test proxy (`images/maku-harness/tests/checks/proxy.py`): `refuse <host:port>`.
- **PR6 (the `kubernetes` mode's shape).**
  - **Policies:** the chart renders a default-deny NetworkPolicy for the runs namespace. The controller creates one NetworkPolicy per run: DNS to kube-dns, the egress proxy on 3128, the broker on 8443 (and the room tools port) when the run has a room, the model's namespace and port when the model is in-cluster. Ingress allows only the bridge's health port 8085, for the kubelet's probe.
  - **Proxy env:** the harness alone gets `HTTPS_PROXY`/`https_proxy` and `NO_PROXY`/`no_proxy=127.0.0.1,localhost,.svc.<clusterDomain>`. The bridge talks in-cluster only.
  - **Residual risk:** DNS to kube-dns stays open, so DNS tunnelling is possible in this mode. `cilium` mode, which filters DNS names, closes it. `docs/security.md` says so.
  - **`cilium` mode** is unchanged and uses no proxy.
  - **`none` mode** stays, and is flagged unsafe.
- **PR7 (the preflight lives in the run-controller).** A `Preflight` runnable runs at start and every 10 minutes:
  1. the RuntimeClass exists (skipped under `unsafeAllowRunc`);
  2. in `kubernetes` mode, a probe pod in the runs namespace runs `maku-run-controller netprobe`. It carries run labels, the sandbox class, its own per-run NetworkPolicy and an egress annotation of `github.com:443`. It must pass:
     - a positive control: TCP to the broker's 8443, and CONNECT `github.com:443` through the proxy;
     - a negative control: TCP to the broker's 9090 must time out or be refused, twice, 3 s apart.

  Until the first result, new runs wait Pending (`PreflightPending`, requeued after 10 s). A failed check refuses new runs: phase Failed, condition Invalid, reason `PreflightFailed`, with the check's message. Runs that already have a pod are untouched. Step 7's `maku preflight` reuses the same checks.
- **PR8 (where the `sandbox` values live).** Helm shares only `global` with subcharts, and the run path is the `maku` chart's, so the keys are `sandbox.*` in the `maku` chart: `maku.sandbox.*` in the umbrella. The mapping:

  | Spec key | `maku` chart key | Old key, still honoured | Rule |
  |---|---|---|---|
  | `sandbox.runtimeClassName` (default `gvisor`) | `sandbox.runtimeClassName` (default `""`) | `runController.runtimeClassName` (default `gvisor`) | the new key wins when set; else the old one |
  | `sandbox.unsafeAllowRunc` | `sandbox.unsafeAllowRunc` | `runController.allowUnsandboxed` | new key true: the config omits `runtimeClassName` and sets `allowUnsandboxed: true`. Setting it together with `sandbox.runtimeClassName` fails the render. The old key keeps its meaning (it matters only when the class is empty) |
  | (not in the spec) | `runController.createRuntimeClass`, `runtimeHandler` | the same | unchanged; renders the class under the resolved name |

  Step 8 deletes the old keys and moves the `gvisor` default onto `sandbox.runtimeClassName`.
- **PR9 ("the Status page").** Maku has no page by that name. Here it means the install's state, readable by every signed-in human:
  - the broker serves `GET /api/install` → `{"unsafe": [...]}` with bounded codes: `unsafe_allow_runc`, `network_policy_none`, `shared_git_token`;
  - the web UI shows a persistent banner on the Rooms list and every Settings page while the list is non-empty;
  - the chart computes the list from its values into the broker config (`install.unsafe`).
  
  Per-run `Unsandboxed` conditions and the room's "Not sandboxed" banner stay.
- **PR10 (the quota covers pods, CPU and memory only).** A quota on `requests.ephemeral-storage` would require it on every container, and the bridge and gate set none. A create refused for `exceeded quota` is Pending (`QuotaExceeded`, retried after 30 s), never Failed: the quota is a safety net behind the factory's slots (D16), not an error.
- **PR11 (new objects only with the run-controller).**
  - These render only with `runController.enabled`: the ResourceQuota, the run-pod VAP, the proxy, the NetworkPolicies, the preflight RBAC and the RuntimeClass ClusterRole.
  - The AgentRun creator and factory patch-limit VAPs (Task 5) render only when their values are set, for ANR to opt in.
- **PR12 (the pod policy uses volume-type and container allow-lists).** "No hostPath" is enforced as: volumes are only `emptyDir`, `configMap`, `secret` or `projected`. The message names hostPath. `pods/ephemeralcontainers` updates are refused, so there is no `kubectl debug` into a run.
- **PR13 (D6's "every component" in this step).** Step 2 gives `kubernetes`-mode policies to the run path:
  - the runs namespace;
  - the run-controller;
  - the egress proxy;
  - the broker (its CNP is in the same chart, and runs reach it).
  
  The factory's and the Slack App's egress policies come with step 6 (components). Until then the umbrella's existing ingress policies (`networkPolicies.plain`) stay.

---

## PR A: sandbox and admission (D18, D16, D17)

### Task 1: the `sandbox` values (S)

**Files:**
- Create: `charts/maku/values.schema.json` (strict for the new blocks only: `sandbox`, `runsQuota`, `runPodPolicy`, `agentRunPolicies`; `additionalProperties: true` at the top for the existing keys, per step 1's PR4)
- Modify: `charts/maku/values.yaml`, `charts/maku/templates/_helpers.tpl` (new `ap.sandbox`, called by `ap.validate`), `charts/maku/templates/run-controller-config.yaml`, `charts/maku/templates/run-controller-runtimeclass.yaml`, `charts/maku/tests/render.sh`

- [ ] **Step 1: failing render cases** in `render.sh`, each on `-f values-quickstart.yaml` plus the image flags:
  - `--set sandbox.runtimeClassName=kata`: the controller config has `runtimeClassName: kata`, and the RuntimeClass (createRuntimeClass) is named `kata`;
  - `--set sandbox.unsafeAllowRunc=true --set runController.createRuntimeClass=false`: no `runtimeClassName` key, and `allowUnsandboxed: true`;
  - both of the above: the render fails with `sandbox.unsafeAllowRunc and sandbox.runtimeClassName: choose one`;
  - no `sandbox` keys: the controller config is byte-identical to `origin/main`'s (`runtimeClassName: gvisor`);
  - `--set sandbox.unsafeAllowRunc=yes`: the schema rejects it.

  Run `bash charts/maku/tests/render.sh`. Expected: FAIL on the first new case.
- [ ] **Step 2: implement.**
  - `values.yaml` gets `sandbox: {runtimeClassName: "", unsafeAllowRunc: false}`. Its comment block carries PR8's table.
  - `ap.sandbox` returns `{class, allowUnsandboxed}`: class is `""` when unsafe, else `sandbox.runtimeClassName | default runController.runtimeClassName`; allowUnsandboxed is `unsafe || runController.allowUnsandboxed`.
  - The config template and the RuntimeClass template read only `ap.sandbox`.
- [ ] **Step 3:** run `bash charts/maku/tests/render.sh` and `task chart:check`. Expected: PASS.
- [ ] **Step 4: commit** `feat(charts): sandbox.runtimeClassName and sandbox.unsafeAllowRunc, mapped onto the run-controller's keys`.

### Task 2: the preflight runnable and the RuntimeClass check (M)

**Files:**
- Create: `internal/runctl/preflight.go`, `internal/runctl/preflight_test.go`
- Modify: `internal/runctl/reconciler.go` (consult the preflight before creating a pod), `internal/runctl/envtest_test.go`, `internal/app/runcontroller.go` (register the runnable; add `node.k8s.io` to the scheme), `charts/maku/templates/run-controller-rbac.yaml` (a ClusterRole and binding: `get` on `node.k8s.io/runtimeclasses`, `resourceNames: [<resolved class>]`, rendered only when the class is non-empty), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing unit tests** (`preflight_test.go`, with a fake client):
  - the state is Unknown before the first tick;
  - the class is missing: Failed, with check `runtimeClass` and the message `RuntimeClass "gvisor" not found: install the sandbox runtime on your nodes, or set sandbox.runtimeClassName`;
  - the class exists: Passed;
  - `AllowUnsandboxed` with an empty class: Passed with no API call;
  - a later tick that finds the class flips Failed to Passed.

  Then failing envtest cases in `envtest_test.go`:
  - an Unknown preflight leaves a run Pending, reason `PreflightPending`, with no pod;
  - a Failed preflight ends it Failed, condition Invalid, reason `PreflightFailed`;
  - a run that already has a pod is never refused by a later failure.

  Run `KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)" go test -race -count=1 -run 'Preflight' ./internal/runctl/`. Expected: FAIL (`undefined: Preflight`).
- [ ] **Step 2: implement.**
  - `type Preflight struct{ Client client.Reader; Cfg Config; Every time.Duration; checks []check }`, with `Result{State, Check, Message, At}` behind a mutex and `Start(ctx)` (a `manager.Runnable` that needs leader election).
  - A gauge `maku_run_controller_preflight_passed{check}` through `internal/metrics`.
  - Each state change is logged once.
  - `Reconciler` gains `Preflight interface{ Result() Result }`. It is consulted only where `Invalid` is: `pod == nil && StartedAt == ""`.
- [ ] **Step 3:** run the Step 1 command, then `bash charts/maku/tests/render.sh`. Expected: PASS. The render asserts the ClusterRole names exactly the resolved class, and no ClusterRole under `unsafeAllowRunc`.
- [ ] **Step 4: commit** `feat(runctl): a preflight refuses runs when the sandbox RuntimeClass is missing`.

### Task 3: the ResourceQuota on the runs namespace (S)

**Files:**
- Create: `charts/maku/templates/runs-quota.yaml`
- Modify: `charts/maku/values.yaml` (`runsQuota`), `internal/runctl/reconciler.go`, `internal/runctl/envtest_test.go` (or a fake-client test with an interceptor), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing tests.**
  - Render: one ResourceQuota `runs` in `runs.namespace` whose `hard` keys are exactly `pods, requests.cpu, requests.memory, limits.cpu, limits.memory`. None with `runsQuota.enabled=false`, and none with `runController.enabled=false`.
  - Go: a `client.Create` of the pod that returns `Forbidden` with `exceeded quota` leaves the run Pending, reason `QuotaExceeded`, `RequeueAfter: 30s`. Another `Forbidden` still fails the run (`CreateFailed`, today's behaviour).

  Run `go test -race -count=1 -run 'Quota' ./internal/runctl/ && bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.**
  - Defaults, sized for ten small runs with headroom, with the arithmetic in a comment (small = harness 1–2 CPU / 2–4 Gi + bridge + gate): `pods: "25"`, `requests.cpu: "16"`, `requests.memory: 32Gi`, `limits.cpu: "32"`, `limits.memory: 64Gi`.
  - In `create`, `apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota")` is checked before the terminal branch.
- [ ] **Step 3:** rerun the Step 1 command. Expected: PASS.
- [ ] **Step 4: commit** `feat(charts): a ResourceQuota on the runs namespace; a run over quota waits`.

### Task 4: the run pod's ValidatingAdmissionPolicy (M)

**Files:**
- Create: `charts/maku/templates/run-pod-policy.yaml`, `charts/maku/tests/admission/runpod_test.go`
- Modify: `charts/maku/tests/admission/policy_test.go` (`render` takes the template path and the expected object count), `charts/maku/values.yaml` (`runPodPolicy: {enabled: true, extraImages: []}`), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing envtest** `TestRunPodPolicy`.
  - **Setup:** it renders `templates/run-pod-policy.yaml` from the chart. It creates the RuntimeClass `gvisor`, the runs namespace and the policy. It acts as `system:serviceaccount:maku:run-controller` (in `system:masters`, so admission alone decides).
  - **Allowed:**
    - `runctl.Pod(...)` itself, built from Go with and without a room (the controller's own pod must pass);
  - **Denied, each with its message:**
    - a missing or different `runtimeClassName`;
    - `automountServiceAccountToken` not false;
    - `hostNetwork`, `hostPID` or `hostIPC`;
    - a `hostPath` volume;
    - a `persistentVolumeClaim` volume;
    - a container with `privileged: true`;
    - a container with `allowPrivilegeEscalation` unset;
    - capabilities without `drop: [ALL]`, or with any `add`;
    - `readOnlyRootFilesystem` unset;
    - no `runAsNonRoot`;
    - no `RuntimeDefault`/`Localhost` seccomp;
    - an init container without `limits.cpu`;
    - a container without `limits.memory`;
    - an image outside the allow-list;
    - an allow-listed repository without `@sha256:`;
    - a pod created by another user;
    - an `UPDATE` that changes `maku.ogenki.io/egress`;
    - a `pods/ephemeralcontainers` update.
  - **The unsafe render:** with `sandbox.unsafeAllowRunc=true`, a pod without a class is allowed.

  Run `KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)" go test -count=1 -run TestRunPodPolicy ./charts/maku/tests/admission/`. Expected: FAIL (the template is missing).
- [ ] **Step 2: the policy.**
  - **Object:** `maku-<release ns>-run-pods`, `failurePolicy: Fail`.
  - **Scope:** it matches `pods` CREATE and UPDATE, and `pods/ephemeralcontainers` UPDATE. The binding's `namespaceSelector` is `kubernetes.io/metadata.name: <runs.namespace>`, with `validationActions: [Deny]`.
  - **Variables:**
    - `containers` is `object.spec.containers + (has(object.spec.initContainers) ? object.spec.initContainers : [])`;
    - `images` holds the repositories of `runController.images.harness`, `runController.images.bridge` and `runController.image`, plus `runPodPolicy.extraImages`;
    - `pinned` is `!e2e.unpinnedImages`.
  - **Rules:** one validation per rule above, each with its own message. The creator rule applies to CREATE only. The RuntimeClass rule is rendered only when `ap.sandbox`'s class is non-empty.
- [ ] **Step 3:** rerun the Step 1 command, then `task test`. Expected: PASS, the broker policy's test included.
- [ ] **Step 4: commit** `feat(charts): a ValidatingAdmissionPolicy holds the run pod's sandbox, securityContext, volumes, limits and images`.

### Task 5: the AgentRun creator and factory patch-limit policies, ported from ANR (S)

**Files:**
- Create: `charts/maku/templates/agentrun-policies.yaml`
- Modify: `charts/maku/values.yaml` (`agentRunPolicies: {creators: [], factoryServiceAccount: ""}`), `charts/maku/tests/admission/runpod_test.go` (or a new `agentrun_test.go`), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing envtest cases.** With `agentRunPolicies.creators={system:serviceaccount:maku:agent-factory}`:
  - another user's AgentRun CREATE is denied with `only the factory creates AgentRuns`.

  With `factoryServiceAccount=agent-factory`, as that user:
  - a change to the spec or the labels is denied;
  - changing an annotation other than `agents.ogenki.io/usage-tokens`, `pull-request` or `revoked` is denied;
  - those three are allowed.

  With both values empty, nothing renders. Run the Task 4 command with `-run TestAgentRunPolicies`. Expected: FAIL.
- [ ] **Step 2:** write the two policies, transcribed from ANR's `security/base/agent-policies/validatingpolicies.yaml` (`agentrun-one-creator`, `agentrun-factory-patch-limit`) in the style of `broker-agentrun-policy.yaml`.
- [ ] **Step 3:** rerun. Expected: PASS.
- [ ] **Step 4: commit** `feat(charts): optional AgentRun creator and factory patch-limit policies, Maku's own copy of ANR's Kyverno rules`.

### Task 6: the install's unsafe settings, shown to every human (M)

**Files:**
- Create: `internal/humanapi/install.go`, `internal/humanapi/install_test.go`, `web/src/install-banner.ts`, `web/test/install-banner.test.ts`
- Modify: `internal/config/config.go` (broker config: `Install struct{ Unsafe []string }`, validated against the three codes), `internal/config/config_test.go`, `internal/humanapi/server.go` (route `GET /api/install`), `charts/maku/templates/broker-config.yaml`, `charts/maku/tests/render.sh`, `web/src/main.ts` and `web/src/settings.ts` (mount the banner), `internal/humanapi/ui/dist` (rebuilt), `docs/web-ui.md`

- [ ] **Step 1: failing tests.**
  - **Config:** `install.unsafe: [bogus]` is refused, and the three codes are accepted.
  - **API:** an authenticated GET returns the configured list, and `[]` when unset. An unauthenticated GET gets the API's usual 401.
  - **Web:** an empty list renders nothing. `["unsafe_allow_runc"]` renders a `role="alert"` banner reading "Runs are not sandboxed: sandbox.unsafeAllowRunc is set. Use this install for demos only." A fetch error renders nothing.
  - **Render:** the quickstart with `ALLOW_UNSANDBOXED`-style flags gives `install.unsafe: [unsafe_allow_runc]`. The default quickstart (Cilium, gVisor) gives none. `networkPolicies.enabled=false` gives `network_policy_none`.

  Run `go test -race -count=1 ./internal/config/ ./internal/humanapi/ -run 'Install'`, `task ui:test` and `bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** A helper `ap.unsafe` lists the codes:
  - `sandbox.unsafeAllowRunc` or `runController.allowUnsandboxed` with an empty class;
  - network mode `none`;
  - `runController.unsafeSharedGitToken`, which comes in Task 7 (add the code now; it stays unused until then).
- [ ] **Step 3:** run the Step 1 commands, then `task ui:check`. Expected: PASS.
- [ ] **Step 4: the demo's render.** `bash charts/agent-factory/tests/render.sh` must show, for `-f values-demo.yaml`, only these additions:
  - the ResourceQuota;
  - the run-pod VAP and its binding;
  - the RuntimeClass ClusterRole (none: the class is empty);
  - `install.unsafe: [unsafe_allow_runc, network_policy_none]`.
  
  Update the umbrella render test's assertions to name exactly these. `task check`: PASS.
- [ ] **Step 5: commit** `feat(broker,ui): the install's unsafe settings, on every page`, then open PR A. Its body lists PR8–PR12 and the strict-decoding note.

## PR B: the scoper and the loop policies on the run-controller (D5)

### Task 7: read-only tokens for reviewer and triager; the scoper needs room tools (M)

**Files:**
- Create: `internal/runctl/testdata/pod-reviewer.golden.yaml`
- Modify: `internal/runctl/config.go` (`GitReadSecret`, `UnsafeSharedGitToken`; `Invalid` gains the two refusals), `internal/runctl/pod.go` (the `github` volume's Secret by role), `internal/runctl/reconciler.go` (`inputs` checks the read Secret's `token` when configured), `internal/runctl/config_test.go`, `internal/runctl/render_test.go`, `internal/runctl/roomtools_test.go`, `charts/maku/values.yaml` (`runController.gitReadSecret: agent-git-read`, `unsafeSharedGitToken: false`), `charts/maku/values-quickstart.yaml` (`unsafeSharedGitToken: true`, commented: a laptop PAT), `charts/maku/templates/run-controller-config.yaml`, `charts/maku/templates/run-controller-rbac.yaml` (the read Secret in `resourceNames`), `charts/maku/tests/render.sh`, `hack/quickstart/up.sh` (no change expected; verify)

- [ ] **Step 1: failing tests.**
  - **Pod:** a reviewer pod mounts `agent-git-read` (new golden). An implementer pod mounts `agent-git` (the existing golden, unchanged). Tester mounts `agent-git`.
  - **`Invalid`:**
    - a triager with no `gitReadSecret` and no unsafe flag gives `role triager needs a read-only token: set runController.gitReadSecret (or unsafeSharedGitToken on a laptop)`;
    - the same run with `UnsafeSharedGitToken` is accepted and mounts `agent-git`;
    - a triager whose task text starts with `runs.ScoperMarkerPrefix`, with no `RoomToolsURL`, gives `a scoper hands off with room_handoff: set broker.roomTools.auth tokenreview`.
  - **Reconciler:** a missing `agent-git-read` leaves a reviewer run Pending with `SecretMissing`.
  - **Render:** the Role's Secret names are `agent-model,agent-git,agent-git-read`. The quickstart renders `unsafeSharedGitToken: true`, and Task 6's helper lists `shared_git_token`.

  Run `go test -race -count=1 ./internal/runctl/ && bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.**
  - Add `readOnlyRoles = []string{"reviewer", "triager"}`, matching cnref's octo-sts policies.
  - `runctl` imports `internal/factory/runs` only for `ScoperMarkerPrefix`. If that import cycles, move the constant to `internal/runwatch` and alias it from `runs`.
- [ ] **Step 3:** rerun the Step 1 command, `go test ./internal/runctl/ -run Render -update` for the new golden only, then review the diff. Expected: PASS.
- [ ] **Step 4: commit** `feat(runctl): reviewer and triager runs get a read-only token; a scoper needs the room tools`.

### Task 8: the judge's model from the model Secret (S)

**Files:**
- Modify: `internal/runctl/config.go` (`Model.Judge` from the optional key `judge_model`), `internal/runctl/pod.go` (`JUDGE_MODEL`), `internal/runctl/config_test.go`, `internal/runctl/render_test.go`, `charts/agent-factory/templates/secrets.yaml` and `values-demo.yaml` (an optional `demo.model.judgeName`), `docs/factory.md`

- [ ] **Step 1: failing tests.** With `judge_model: small` in the Secret and a run with `loop.judgeModel: tier-light`, the harness env has `JUDGE_MODEL=small`. Without the key, `JUDGE_MODEL` is the run's model (today). Run `go test -race -count=1 -run 'Judge' ./internal/runctl/`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): the completion judge may run on its own model (judge_model)`.

### Task 9: the factory config stops requiring the XR (S)

**Files:**
- Modify: `internal/factory/config/config.go` (delete the two `runwatch.DefaultKind` comparisons; add PR4's refusal, keyed on `c.Runs.Group == v1alpha1.GroupVersion.Group` of `api/v1alpha1`), `internal/factory/config/scope_test.go`, `internal/factory/config/demo_test.go`, `internal/app/runkind_test.go`, `docs/factory.md`

- [ ] **Step 1: tests first.**
  - Rewrite `TestScoperIsRefusedOnThePortableRuntime` as `TestScoperParsesOnThePortableRuntime`: `scope: scoper` and `split` parse with `runs.group: maku.ogenki.io`, demo or not.
  - In `demo_test.go`, the two cases now parse.
  - New `TestLoopModelsAreRefusedOnThePortableRuntime`: `loops.docs.models: {implementer: {frontier: kimi-frontier}}` with the portable group fails with PR4's message, and parses with `cloud.ogenki.io`.
  - `runkind_test.go`: drop the `config.go` exemption.

  Run `go test -race -count=1 ./internal/factory/config/ ./internal/app/ -run 'Scoper|LoopModels|DefaultKind|Demo'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): the scoper and the loop policies run on the run-controller`.

### Task 10: the run token writer, with read and write Secrets (M)

**Files:**
- Create: `internal/factory/runtoken/runtoken.go`, `internal/factory/runtoken/runtoken_test.go` (moved from `internal/factory/demo/gittoken.go` and its test)
- Modify: `internal/factory/demo/` (delete `gittoken.go`; keep `Permissions` as `runtoken.Write`), `internal/app/factory.go` (`demoGitToken` → `runTokens`, two writers), `internal/app/factory_test.go`, `internal/factory/config/config.go` (`runs.gitTokens: {enabled, appIDFile, privateKeyFile}`; refused with the XR kind; demo implies enabled with the factory App), `charts/maku-factory/values.yaml` (`runTokens: {enabled: false, appSecret: ""}`), `charts/maku-factory/templates/rbac.yaml` (`resourceNames: [agent-git, agent-git-read]` when demo or `runTokens.enabled`), `charts/maku-factory/templates/deployment.yaml` (mount `runTokens.appSecret` at `/etc/agent-factory-run-tokens`), `charts/maku-factory/tests/render.sh`, `docs/factory.md`

- [ ] **Step 1: failing tests.**
  - The writer writes `agent-git` with `runtoken.Write()` and `agent-git-read` with `runtoken.Read()`. The read set is `contents, pull_requests, issues, checks, actions, metadata: read`: no key is `write`, asserted.
  - A mint failure keeps the old Secret (moved test).
  - The config refuses `runs.gitTokens.enabled` without both files, and refuses it with `runs.group: cloud.ogenki.io`.
  - Render: the Role names both Secrets in demo and with `runTokens.enabled`, and neither otherwise. The App Secret is mounted only with `runTokens.enabled`.

  Run `go test -race -count=1 ./internal/factory/runtoken/ ./internal/factory/config/ ./internal/app/ -run 'Token|GitTokens' && bash charts/maku-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2:** implement, with a comment on the package: "interim: replaced by the per-run minter, v4 D10 (rollout step 3)".
- [ ] **Step 3:** rerun, then `task check` and `task chart:install:k3d`. Expected: PASS. The demo factory writes both Secrets: assert in `charts/agent-factory/tests/install.sh` that `agent-git-read` exists once the factory is up, or that its log names the App failure, since the App is a dummy.
- [ ] **Step 4: commit** `feat(factory): the run token writer keeps a write and a read token; the demo's becomes general`, then open PR B with PR1–PR4 in the body.

## PR C: the `kubernetes` network mode and the egress proxy (D6)

### Task 11: `networkPolicies.mode` and the per-run NetworkPolicy (M)

**Files:**
- Create: `internal/runctl/netpol.go`, `internal/runctl/testdata/netpol.golden.yaml`, `internal/runctl/testdata/netpol-incluster-model.golden.yaml`
- Modify: `internal/runctl/config.go` (`NetworkPolicyKubernetes`; `EgressProxy{URL, Namespace, Labels}`, required in that mode; `DNS{Namespace, Labels}`, defaulted to kube-system/`k8s-app: kube-dns`), `internal/runctl/cnp.go` (extract `Egress(r, c, m) []string` of `host:port`, shared), `internal/runctl/pod.go` (in `kubernetes` mode: the egress annotation, and the harness's proxy env per PR6), `internal/runctl/reconciler.go` (create the NetworkPolicy in that mode), `internal/runctl/config_test.go`, `internal/runctl/render_test.go`, `charts/maku/values.yaml` (`networkPolicies.mode: ""`, `networkPolicies.kubernetes.{dns, apiServerCIDRs, privateCIDRs, metricsFrom}`), `charts/maku/templates/_helpers.tpl` (`ap.netMode`: mode, or `enabled ? cilium : none`; mode `cilium` with `enabled: false` fails), `charts/maku/templates/run-controller-config.yaml`, `charts/maku/templates/run-controller-rbac.yaml` (`networking.k8s.io/networkpolicies` get/create in `kubernetes` mode), `charts/maku/templates/agents-namespace.yaml` (a NetworkPolicy default-deny in that mode), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing tests.**
  - **Golden NetworkPolicy, room and SaaS model:** podSelector on run-id; egress to kube-dns 53/UDP+TCP, the proxy's labels in its namespace on 3128, and the broker's labels on 8443 (+8090 with `RoomToolsURL`); ingress only on 8085.
  - **Golden NetworkPolicy, in-cluster model:** adds the model's namespace on its port. With no room, there is no broker rule and no ingress rule.
  - **Pod:** the annotation `maku.ogenki.io/egress` is `github.com:443,…,api.anthropic.com:443`, the same set as `Egress`. The harness env has the four proxy variables and the bridge has none. In `cilium` mode the pod is byte-identical to today's golden.
  - **Config:** `networkPolicy: kubernetes` without `egressProxy.url` is refused.
  - **Render:** `--set networkPolicies.mode=kubernetes` gives:
    - no CiliumNetworkPolicy;
    - a NetworkPolicy `default-deny` in the runs namespace with `policyTypes: [Ingress, Egress]` and no rules;
    - the controller config with `networkPolicy: kubernetes` and `egressProxy.url: http://egress-proxy.<ns>.svc.cluster.local:3128`;
    - the Role granting networkpolicies get/create.
    
    `networkPolicies.enabled=false` alone still gives `none` (old key).

  Run `go test -race -count=1 ./internal/runctl/ && bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun with `-update` for the new goldens only, and review the diffs. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): a kubernetes network mode, with a NetworkPolicy per run and the harness behind the egress proxy`.

### Task 12: the egress proxy (L)

**Files:**
- Create: `internal/egressproxy/index.go`, `internal/egressproxy/proxy.go`, `internal/egressproxy/dial.go`, `internal/egressproxy/index_test.go`, `internal/egressproxy/proxy_test.go`, `internal/egressproxy/dial_test.go`, `internal/app/egressproxy.go`
- Modify: `cmd/maku-run-controller/main.go` (the `egress-proxy` subcommand; no argument still runs the controller)

- [ ] **Step 1: failing unit tests.**
  - **Index** (fed with pods), the IP's allow-list:
    - a Running pod with `managed-by: run-controller`, the annotation and a podIP: entries from the annotation;
    - no annotation, deletionTimestamp set, phase Succeeded, or the same IP on two pods: none;
    - a pod deleted: its IP is gone.
  - **Proxy** (httptest, with an injected Index and dialer):
    - CONNECT to an allowed `host:443` from a mapped IP gives `200 Connection established`, and bytes flow both ways;
    - an unlisted host gives 403 and logs `refuse run=<id> example.com:443`;
    - an allowed host on another port gives 403;
    - an unmapped IP gives 403 `refuse run=- …`;
    - an IP literal gives 403;
    - GET gives 405;
    - an idle tunnel closes after `IdleTimeout`.
  - **Dialer:** a host resolving to 127.0.0.1, 10.0.0.1, 169.254.169.254, `privateCIDRs` or `::1` is refused, and a public address is dialled.

  Run `go test -race -count=1 ./internal/egressproxy/`. Expected: FAIL (no package).
- [ ] **Step 2: implement.**
  - The proxy reads the run-controller's config file for `namespace`; one source.
  - A controller-runtime cache watches pods in that namespace only, with the label selector `app.kubernetes.io/managed-by=run-controller`.
  - It serves `:3128` (proxy), `:9090` (metrics: `maku_egress_proxy_connections_total{decision}`, `maku_egress_proxy_bytes_total`) and `:9091` (`/healthz`; `/readyz` once the cache has synced).
  - No leader election.
- [ ] **Step 3:** rerun. Expected: PASS. Then `go vet ./...` and `task lint`. Expected: PASS.
- [ ] **Step 4: commit** `feat(egressproxy): a CONNECT proxy that holds each run to its own hosts`.

### Task 13: the proxy's chart objects, and the run-controller's and broker's NetworkPolicies (M)

**Files:**
- Create: `charts/maku/templates/egress-proxy.yaml` (Deployment of `runController.image` with args `[egress-proxy]`, 2 replicas, the restricted context, the config ConfigMap mounted, a Service on 3128, a PDB `minAvailable: 1`, a ServiceAccount, a Role and binding in `runs.namespace` with pods get/list/watch, and a NetworkPolicy), `charts/maku/templates/broker-netpol.yaml`, `charts/maku/templates/run-controller-netpol.yaml`
- Modify: `charts/maku/values.yaml` (`egressProxy: {replicas: 2, resources, extraPorts: []}`), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing render cases** for `--set networkPolicies.mode=kubernetes`:
  - **The proxy's NetworkPolicy:**
    - ingress 3128 only from namespace `runs.namespace` and pods `managed-by: run-controller`; 9090 only from `metricsFrom`;
    - egress to DNS, to the apiserver (`apiServerCIDRs` or any, on `apiServerPorts`), and to 443 plus `extraPorts` with an `ipBlock` of `0.0.0.0/0` except `privateCIDRs`.
  - **The broker's NetworkPolicy** mirrors `broker-cnp.yaml`:
    - ingress 8443 (and 8090 under tokenreview) from run pods, 8443 from the factory's labels, 9090 from `metricsFrom`;
    - egress to DNS, the apiserver, the database label on 5432, the factory port, and `extraEgress`.
  - **The run-controller's NetworkPolicy:** egress to DNS and the apiserver only; ingress none.
  - **Mode `cilium` and mode `none`:** none of these objects.
  - The proxy image is the run-controller's, by digest.

  Run `bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2:** implement. Then `bash charts/maku/tests/render.sh` and `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the egress proxy, and NetworkPolicies for the run-controller and the broker in kubernetes mode`.

### Task 14: the network preflight (M)

**Files:**
- Create: `internal/runctl/probe.go` (the probe pod, its NetworkPolicy and its egress annotation, built by the same functions as a run's), `internal/runctl/testdata/probe.golden.yaml`, `internal/app/netprobe.go` (`netprobe --allow host:port … --deny host:port … --connect host:port --proxy URL`, writing JSON to `/dev/termination-log`), `internal/app/netprobe_test.go`
- Modify: `internal/runctl/preflight.go` (the `network` check in `kubernetes` mode), `internal/runctl/preflight_test.go`, `internal/runctl/config.go` (`Images.Probe`), `cmd/maku-run-controller/main.go` (`netprobe`), `charts/maku/templates/run-controller-config.yaml` (`images.probe` = `runController.image`), `charts/maku/templates/run-pod-policy.yaml` (already allows the run-controller image: assert it), `charts/maku/tests/render.sh`

- [ ] **Step 1: failing tests.**
  - **netprobe** (local listeners):
    - the allow target is open and the deny target is closed or blackholed: exit 0;
    - the deny target accepts twice: exit 1 with `deny <addr> connected: the CNI does not enforce NetworkPolicy`;
    - the proxy answers 403 to CONNECT: exit 1 with `the egress proxy refused github.com:443`.
  - **Probe pod golden:**
    - named `<namePrefix>preflight-<8 chars>`;
    - labels `agents.ogenki.io/run-id: preflight` and `managed-by: run-controller`;
    - the sandbox class, the restricted context and limits (it must pass Task 4's policy: add it to `TestRunPodPolicy`'s allowed cases);
    - ownerReferences on its NetworkPolicy point at the pod.
  - **Preflight** (a fake client that sets the probe pod's phase):
    - Succeeded: Passed;
    - Failed: Failed with the termination message;
    - not started in 2 minutes: Failed, `the probe pod did not start under RuntimeClass "<name>": <last event>`;
    - leftover probe pods from an earlier process are deleted at start.

  Run `go test -race -count=1 ./internal/runctl/ ./internal/app/ -run 'Probe|Preflight'`. Expected: FAIL.
- [ ] **Step 2:** implement. Then rerun the Step 1 command, plus `TestRunPodPolicy` (envtest). Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): the preflight proves the CNI enforces NetworkPolicy before any run starts`.

### Task 15: the proof in CI (M)

**Files:**
- Modify: `.github/workflows/quickstart-e2e.yaml` (a matrix `netpol: [cilium, kubernetes]`), `hack/quickstart/e2e.sh` (`E2E_NETPOL=kubernetes` appends `--set networkPolicies.mode=kubernetes` to `UP_HELM_ARGS`), `.github/workflows/demo-e2e.yaml` (the k3d job gains a matrix `netpol: [none, kubernetes, unenforced]`), `charts/agent-factory/tests/install.sh` (`NETPOL` and `K3S_NO_NETPOL`), `taskfile.yaml` (`chart:install:k3d:netpol`)

- [ ] **Step 1: write the assertions first.**
  - **quickstart-e2e, `kubernetes` leg**, on Cilium, which enforces standard policies with gVisor:
    - the run reaches Succeeded, as the `cilium` leg does;
    - `kubectl -n maku logs deploy/egress-proxy` holds `allow run=<the run id> github.com:443`;
    - the run's NetworkPolicy exists;
    - `kubectl get cnp -n agents` is empty;
    - the run-controller logged `preflight passed`.
  - **demo-e2e, k3d `kubernetes` leg** (kube-router enforces): the run-controller logs `preflight passed` within 3 minutes.
  - **demo-e2e, `unenforced` leg** (`--k3s-arg '--disable-network-policy@server:0'`): it logs `preflight failed: network:` and the gauge `maku_run_controller_preflight_passed{check="network"} 0`.

  Run `NETPOL=unenforced bash charts/agent-factory/tests/install.sh k3d` locally before implementing. Expected: FAIL (`NETPOL` not handled).
- [ ] **Step 2:** implement the legs. Then run all three `install.sh` legs locally, and `hack/quickstart/e2e.sh` with `E2E_NETPOL=kubernetes` (it needs `GITHUB_TOKEN` and `REPO`). Expected: PASS. Run `actionlint` on both workflows. Expected: PASS.
- [ ] **Step 3: commit** `ci: prove the kubernetes network mode on Cilium and k3s, and the preflight's refusal`.

### Task 16: docs, the gate (S)

**Files:**
- Modify: `charts/maku/README.md`, `charts/agent-factory/README.md`, `docs/security.md`, `docs/operations.md`, `docs/factory.md`, `docs/upgrading.md`, `docs/roadmap.md`

- [ ] **Step 1: write.**
  - **`charts/maku/README.md`:**
    - PR8's mapping table;
    - `networkPolicies.mode`;
    - the proxy's values, `runsQuota`, `runPodPolicy`, `agentRunPolicies`;
    - `gitReadSecret` and `unsafeSharedGitToken`.
  - **`docs/security.md`:**
    - the three network modes, with PR6's DNS residual risk;
    - the proxy's guarantees: CONNECT-only, source-IP identity, no private addresses;
    - the pod policy's rules;
    - the read-only roles.
  - **`docs/operations.md`:**
    - the preflight: its checks, its metric and what a refused run looks like;
    - the quota and `QuotaExceeded`.
  - **`docs/factory.md`:**
    - the scoper and loops on the run-controller;
    - PR4;
    - `judge_model`;
    - `runs.gitTokens` as interim.
  - **`docs/upgrading.md`:** the new keys and the old ones still honoured.
  - **`docs/roadmap.md`:** marks rollout step 2 built.
- [ ] **Step 2:** run `task check`, then `task chart:install:k3d`. Expected: PASS.
- [ ] **Step 3: commit** `docs: the run path's network modes, sandbox, preflight, quota and admission`, then open PR C. Its body lists PR5–PR7 and PR13, and both CI legs' results, and says this is a v0.4x minor, not v4.

## After this step (not in these PRs)

- **ANR** (its own PR, after the Maku release):
  - **Switch cnref from the XR:**
    - `runs.group: maku.ogenki.io` on the broker and the factory;
    - `runController.enabled: true`, `networkPolicies.mode: kubernetes` (Cilium stays the CNI: D15);
    - `sandbox.runtimeClassName: gvisor`;
    - `runController.gitReadSecret: agent-git-read`, and `runTokens` with the agents App's key through an ExternalSecret;
    - `broker.roomTools.auth: tokenreview` for the scoper's handoff (or the gateway's route, until step 4);
    - `agentRunPolicies.creators` and `factoryServiceAccount` set.
  - **The cutover runbook:** engage the factory's stop, wait for zero active runs on `cloud.ogenki.io`, flip, then lift the stop. The factory reads one kind at a time, so a run in flight across the flip would be orphaned.
  - **Delete:**
    - cnref's Crossplane AgentRun XRD and composition usage for agents;
    - the duplicate Kyverno policies (`security/base/agent-policies/validatingpolicies.yaml`: `agents-pod-shape`, `agents-pod-creator`, `agentrun-admission`, `agentrun-one-creator`, `agentrun-factory-patch-limit`, `agentrun-broker-patch-limit`);
    - Kueue (`infrastructure/base/kueue`, `infrastructure/base/kueue-queues`, `infrastructure/{aws-0,gcp-0}/kueue`, `flux/sources/ocirepo-kueue.yaml`, `clusters/*/infrastructure-kueue.yaml` and their kustomization entries).
  - **Keep** `agents-no-secret-import` and the two audience policies as cnref platform rules (D17). Step 3 revisits the octo-sts audience.
  - **A gcp-0 live check** recorded in the session ledger:
    - a scoper run hands off;
    - a reviewer's token cannot push;
    - the proxy logs show allow and refuse lines;
    - the preflight passed;
    - the quota is present.
- **Step 3 (GitHub seams)** replaces `internal/factory/runtoken` with the per-run, per-repository minter (D10). It also revisits hardening readiness R2, which still reads octo-sts policies from `.github/chainguard`.
- **Step 4 (gateway)** lifts PR4's `loops.models` refusal and resolves the judge alias through agentgateway.
- **Step 6** adds default-deny egress policies for the factory and the Slack App (PR13).
- **Step 7** builds `maku preflight` from Task 2 and Task 14's checks, and the kind+Calico job (D23).
- **Step 8** deletes:
  - `networkPolicies.enabled`, `runController.runtimeClassName` and `runController.allowUnsandboxed` (the `gvisor` default moves to `sandbox.runtimeClassName`);
  - the factory's `spec.queueName` writes (Kueue);
  - the `cloud.ogenki.io` kind.

## Self-review

- **D5:**
  - read-only tokens (Task 7) with a token source (Task 10);
  - the scoper's handoff path (Task 7);
  - the judge's model (Task 8);
  - the XR requirement removed, and `loops.models` held to step 4 (Task 9).
  - PR1 records the evidence for each gap.
- **D6:**
  - the `kubernetes` mode (Task 11);
  - the proxy (Tasks 12, 13; PR5 records the choice);
  - default-deny for the run path's components (Tasks 11, 13; PR13 scopes the rest);
  - Cilium kept as a mode (PR6);
  - the CNI preflight (Task 14), proved on Cilium, k3s and an unenforcing k3s (Task 15).
- **D16:** the ResourceQuota (Task 3), and the over-quota run waits (PR10).
- **D17:**
  - the sandbox class, restricted securityContext, no hostPath, limits and image allow-list in one VAP (Task 4);
  - ANR's duplicates covered (Task 5), so ANR can delete them;
  - no controller needed.
- **D18:**
  - `sandbox.runtimeClassName` and `unsafeAllowRunc`, mapped onto the old keys (Task 1, PR8);
  - the RuntimeClass preflight (Task 2);
  - the flag shown to every human (Task 6, PR9).
- **D25:**
  - new objects only with `runController.enabled` (PR11), proved by a `runController.enabled=false` render diff;
  - old keys honoured;
  - the demo keeps `none` and runc, its render changes listed exactly (Task 6 Step 4);
  - no v4.
- **D26:** VAP v1 only, and envtest at 1.36 (above the floor).
- **Risks named with a stop rule:**
  - **Source-IP identity** fails where a CNI SNATs pod-to-Service traffic. The preflight's CONNECT through the proxy catches it, and runs are refused.
  - **Probe-pod start time under gVisor:** 2-minute bound, Pending until the first result.
  - **The nightly** turns red until the release (strict decoding).
  - **Kubelet probes** may be blocked by a strict CNI's NetworkPolicy; ingress on 8085 is open for that.

### Critical Files for Implementation
- /home/smana/Sources/maku/internal/runctl/pod.go
- /home/smana/Sources/maku/internal/runctl/config.go
- /home/smana/Sources/maku/internal/runctl/reconciler.go
- /home/smana/Sources/maku/internal/factory/config/config.go
- /home/smana/Sources/maku/charts/maku/templates/run-controller-config.yaml
