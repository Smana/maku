# v4 rollout step 7, install UX and proof: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a platform team that has never seen Maku goes from an empty directory to a running factory on its own cluster. It does so without reading the source and without anything applied behind its back. Then CI proves on every chart PR that the same path works on more than one Kubernetes. This step builds the last three decisions of the rollout:
- **D24, the install UX.**
  - `maku install --print` asks about each seam and creates the two GitHub Apps through GitHub's manifest flow. It writes `values.yaml`, the Secret manifests (or ExternalSecret entries) and the starter `Factory` and `Repository` objects, and applies nothing.
  - `maku preflight` runs as a Helm test hook and from the CLI. It answers, seam by seam, whether the install can work, and names the fix when it cannot.
  - One "Install on your cluster" page carries the seam table.
- **D23, the proof of "any Kubernetes".**
  - On every chart PR:
    - a kind install with Calico and gVisor;
    - a k3s install with flannel, kube-router's policy, Traefik Ingress and an external Postgres.

    Both use `profile: production` values that the wizard wrote, and fake models.
  - Nightly:
    - an upgrade from the last released minor to HEAD;
    - the real-model job, which already exists.
  - The two manual installs (GKE, kubeadm) get runbooks and a ledger template here. The runs that gate v4.0 are the owner's (R8).
- **D3, private code by explicit opt-in.** A Repository that serves a private repository needs `spec.privateCode.acknowledged: true`. That field states that the code is sent to the configured models.

The step ends with the **v4.0 decision pack** (PR I). It maps each spec decision to its evidence and lists the manual runs still owed, so the owner can rule on v4.0.0. This step cuts no tag.

Every existing install keeps working (D25):
- `preflight.enabled` defaults to `false`, so the default and demo renders are byte-identical. The wizard writes `true`, and step 8 flips the default.
- `spec.privateCode` is optional. In this step a private repository without it gets a warning, a condition and a gauge, but no refusal (R1).
- `maku demo app` is unchanged. `maku install` reuses its manifest-flow code but does not replace it.

**Architecture:**
- **`internal/preflight`** (new, pure where it can be) holds the check catalog.
  - A check has an id, a seam, where it runs, and an outcome (`pass`, `warn`, `fail` or `skip`) with a bounded `code` and a one-line message.
  - It holds the report type (JSON lines, then a text summary) and the exit rule: any `fail`, or any check that could not decide, exits 1.
- **Each check runs where its traffic runs** (IN3):
  - **Cluster checks** run in the preflight pod, which talks to the API server only. These are the server version, the CRDs, the RuntimeClass, and the Secrets and their keys from step 5's registry.
  - **Component checks** run inside the component that will make the call:
    - the broker: the database, OIDC discovery, the knowledge endpoint and the planner's gateway client;
    - the factory: the App installations and their grants, the merge-gate ruleset and private code;
    - the run-controller: its existing preflight.

    Each component serves its results on its ops port at `GET /preflight` (from `internal/app/ops.go`, beside `/readyz`). The preflight pod collects them. So a check passes only through the same NetworkPolicy, DNS and credentials the component uses.
  - **Outside checks** run in the CLI, from the user's machine: each exposed hostname resolves and answers through its route.
- **The runner** is a new `preflight` subcommand of the `maku-run-controller` image. That image already carries `netprobe` and `egress-proxy`, a Kubernetes client, and the release's signature. There are two front ends to one pod template:
  - a Helm test-hook Job;
  - a suspended CronJob, `<release>-preflight`, that `maku preflight` and Argo CD users instantiate (IN4).
- **`maku install --print`** is in `internal/install`:
  - a question flow over the seams, interactive or from `--answers FILE`;
  - the App manifests built from `internal/githubapps`;
  - a writer that lays out one output tree (IN6).

  It is golden-tested, and its output must pass `helm template` under `profile: production` and kubeconform.
- **D3** lives in the factory's fence pass. It reads each repository's `visibility` with the factory App's token, which the factory already reads for the demo's `requirePublic`. It sets the Repository condition `PrivateCode` and the gauge `maku_factory_private_code_unacknowledged{repository}`, and adds `private_code_unacknowledged` to the install banner (PR9's list).
- **CI** gains `install-e2e.yaml`, with one job per platform. Each job:
  1. runs `maku install --print --answers hack/install-e2e/<platform>.answers.yaml`;
  2. applies the output with plain Helm (per PR) or through Flux or Argo CD (nightly, R6);
  3. runs `helm test`;
  4. drives one task to a merged pull request against the e2e stub's fake GitHub.

**Tech Stack:**
- Go: `client-go`/controller-runtime (already used by the run-controller), `internal/githubapps`, `internal/runctl`'s preflight, `internal/store`'s check (step 5 PR D), `internal/authn` discovery (step 5 PR B), `internal/mcpwire` (the knowledge `tools/list`);
- the GitHub App manifest flow (`/settings/apps/new`, `POST /app-manifests/{code}/conversions`) on github.com and GHES ≥3.14;
- Helm 3.15 (test hooks), JSON Schema, kubeconform;
- kind with Calico (the tigera operator, pinned by digest) and gVisor (`hack/quickstart/fetch-runsc.sh`);
- k3d 5.9.0 (k3s with flannel, kube-router and Traefik);
- Flux ≥2.4 and Argo CD ≥2.12 (the floor, D26) on the nightly;
- the e2e stub (`hack/e2e-stub`): fake GitHub, OIDC issuer, model and OTLP sink.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D2 (one factory per install, several per cluster);
- D3 (private code opt-in);
- D14 (Secrets by name; "the preflight fails the install");
- D18 (the RuntimeClass and its preflight);
- D23 (the proofs);
- D24 (the install UX);
- D25 (additive);
- D26 (support floor);
- "Rollout", step 7;
- the owner rulings PR9 (the unsafe banner), Merge gate (a separate ruleset on agent PRs only), A11 (agentgateway's CRDs outside Helm) and step 6 R1 to R6.

**Read against:**
- Maku `origin/main` `2dd10422`:
  - **Merged:**
    - steps 1 to 4;
    - step 5 PR A (#524);
    - step 6 PR A (#526), B (#523) and B2 (#529);
    - reusable merge policies PR A to D (#517, #519, #522, #528);
    - hardening H1 to H4b (#520).
  - **Open:**
    - step 5 PR B (#530) and PR D (#521);
    - step 6 PR F (#527);
    - hardening H5 (#531).
  - **Not started:**
    - step 5 PR C and E;
    - step 6 PR C, D, E and G;
    - reusable merge policies PR E to K.

  Each PR below names the unmerged PRs it reads.
- ANR `origin/main` `fbaf995`. None of the cnref flips of steps 2 to 6 has landed: Kueue, octo-sts, policy-bot, the Envoy AI gateway, the XR and the hand-written broker are all still live.

**Branch:** ten PRs, each from `origin/main` once its dependencies merge.

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, the preflight runner and the cluster checks (D24, D14, D18) | `feat/v4-preflight-runner` | 1–3 | L | after step 5 PR E (Task 3 reads its Secret registry); Tasks 1–2 may merge before it |
| B, component checks (D24) | `feat/v4-preflight-components` | 4–6 | L | after A, step 5 PR B (#530, discovery) and PR D (#521, the database check), and step 6 PR F (#527, knowledge) |
| C, `maku preflight` and the outside checks (D24) | `feat/v4-preflight-cli` | 7–8 | M | after B and step 6 PR D (exposure) |
| D, private code by opt-in (D3) | `feat/v4-private-code` | 9–10 | M | any time; it touches `RepositorySpec`, so rebase across reusable merge policies PR E to K |
| E1, the two Apps through the manifest flow (D24, D12) | `feat/v4-install-apps` | 11–12 | M | any time |
| E2, `maku install --print` (D24) | `feat/v4-install-print` | 13–15 | L | after E1, D, step 5 PR E (the registry) and step 6 PR G (`profile: production`) |
| F, the install jobs on every chart PR (D23, D2) | `feat/v4-install-e2e` | 16–18 | L | after C and E2 |
| G, the nightly upgrade and GitOps legs (D23, D26) | `feat/v4-install-nightly` | 19–20 | M | after F |
| H, "Install on your cluster" (D24, D3, D18, D26) | `docs/v4-install-page` | 21 | M | after C and E2 |
| I, the v4.0 decision pack | `docs/v4-decision-pack` | 22 | S | last |

The step's v0.4x minor is cut after PR H. A to E2 may also ship in interim minors. PR I is documentation for the owner's decision and needs no release.

## Owner rulings (settled 2026-11-13)

The owner answered yes to each, as the plan recommended (2026-11-13). The spec records them in its owner rulings table, prefixed "Step 7".

| # | Ruling | Answer and reason | Used by |
|---|---|---|---|
| R1 | Should a private repository without `privateCode.acknowledged` get a warning in step 7 (the condition, the gauge, the banner) and be refused only from step 8? | **Yes.** D3's refusal is the decision, and D25 makes each step additive. A refusal in a v0.4x minor would stop cnref's private repositories, if any, with no notice. A warning first lets ANR add the field and prove it live before step 8 flips it | Task 9 |
| R2 | May D14's "the preflight fails the install when one is missing" be met by two things, with no pre-install hook? <ul><li>The components refuse to start, naming the Secret and key (step 5 Task 19).</li><li>The preflight's test hook fails.</li></ul> And may Argo CD users, whose tool does not run Helm test hooks, run `maku preflight` by hand or through the suspended CronJob? | **Yes.** A pre-install hook runs before the release's ServiceAccounts and policies exist, so it would need its own copies of them. Flux and Argo CD each handle hook failures differently, and a pre-install hook would block a GitOps reconcile on a check that is only advisory. The start-up refusal already makes the install fail in the sense that matters: nothing becomes ready | Tasks 2, 7 |
| R3 | May `maku install --print` write the credentials it generates to a `secrets/` directory, mode 0600, with its own `.gitignore`? These are the two Apps' private keys, the factory App's OAuth client secret and a random oauth2-proxy cookie secret. | **Yes.** The manifest flow returns the private key once, so it has to land somewhere. The alternative is to print `kubectl create secret` commands with the key inline, which puts it in shell history. The wizard never asks for a third-party secret (model keys, DSNs, IdP client secrets). It writes those Secrets as templates with empty keys | Task 14 |
| R4 | Should `values-demo.yaml` default to `model.mode: gateway` from this step, layering what `values-demo-gateway.yaml` does today? This is carried over from step 4's open decision. | **Yes**, as step 4 recommended. The demo then shows the product path, the provider key leaves the sandbox, and step 8 can make `model.mode: secret` demo-only rather than the demo's default. The cost is agentgateway on the laptop: its memory goes in `docs/quickstart.md` | Task 15 (the demo answers file) |
| R5 | Should the GitHub account link use the factory App's OAuth client, with the wizard setting its callback URL in the App manifest? This is carried over from step 5's open decision. | **Yes**, as step 5 recommended: no third App or OAuth App, and the manifest flow sets `callback_urls` at creation | Tasks 11, 13 |
| R6 | Should the nightly run the kind and k3s installs through Flux 2.4 and Argo CD 2.12, the floor (D26), while the per-PR jobs use plain Helm? | **Yes.** D1's audience installs through GitOps. The CRD chart's ordering, A11's agentgateway CRDs outside Helm and `crds: Skip` are GitOps-only failure modes. Running both tools on every PR would roughly double the jobs' time | Task 20 |
| R7 | Should the nightly also test Kubernetes 1.30, the floor, on top of the three latest minors that D26 tests? | **Yes**, on the kind job only. 1.30 is where the ValidatingAdmissionPolicies went GA, which is the reason for the floor. A floor that nothing tests drifts | Task 20 |
| R8 | Should the recorded GKE and kubeadm installs that gate v4.0 (D23) run on step 8's last minor, with one kubeadm dry run after step 7's minor to catch install-UX defects early? | **Yes.** Step 8 flips the defaults (`gateStatus`, `preflight.enabled`, the `profile` switch, the sandbox class). A proof run on step 7's minor would prove a chart that v4.0 does not ship | Task 22 |
| R9 | Should the GKE proof be a fresh, second install on gcp-0 in its own namespace, from `maku install --print` output with test-organization Apps, rather than cnref's own release? | **Yes.** cnref's values are not what a new user writes. A second install in the same cluster also proves D2 ("several teams means several installs"), which R10 makes possible | Task 22 |
| R10 | Should the three ClusterRoles with fixed names (`maku-factory-admin`, `maku-mergepolicy-editor`, `maku-viewer`) move into `agent-factory-crds`, and every other cluster-scoped object get a release-prefixed name, so that two installs can share a cluster (D2)? | **Yes.** Today a second release fails on Helm ownership of those three ClusterRoles and of `agent-factory-flux-reader` (IN1). The three aggregated roles describe the CRDs' types, which the CRD chart installs once per cluster | Task 18 |
| R11 | Does k3d, which runs the k3s binary in Docker and is what CI already uses, count as D23's "k3s install"? | **Yes.** It is the same k3s, with its flannel, kube-router and Traefik. A k3s installed on the runner would add root setup and a VM-level teardown for no difference the product can see | Task 17 |

## Global Constraints

- **No v4.** This step ships v0.4x minors only: no v4 tag, release or chart version. PR I prepares the owner's decision and does not take it.
- **Additive (D25).**
  - Every new behaviour is opt-in:
    - the chart: `preflight.enabled` (default `false`);
    - the factory: `spec.privateCode` (warn only, R1);
    - the CLI: `maku install` and `maku preflight` are new commands.
  - Default renders stay byte-identical. Prove it with a render diff against `origin/main` for:
    - the umbrella demo;
    - the maku quickstart;
    - `runController.enabled=false`;
    - `maku-factory`'s default;
    - `maku-gateway`'s minimal values;
    - `agent-factory-crds`.

    R10's move changes `agent-factory-crds` and `maku-factory` on purpose. Task 18 lists that diff, and `docs/upgrading.md` gives the adoption commands.
  - `maku demo app`'s flags and output are unchanged.
- **Fail closed.**
  - A check that cannot decide (a timeout, a 5xx, an RBAC denial on its own read) is `fail` with code `undecided`, never `pass`.
  - A component that does not serve `/preflight` (an older image) is `fail` with code `component_too_old`, naming the image.
  - The wizard refuses to write into a non-empty `--out` directory unless `--force` is given, and never overwrites `secrets/`.
  - An answers file with an unknown key is refused (strict decoding), so CI cannot drift from the wizard.
- **No credential in a report, a log, a metric, an error or a rendered value.**
  - A preflight message names a Secret and key, a host, an HTTP status or a SQLSTATE. It never names a value, a DSN or a token.
  - The wizard prints no key material. The App's PEM and client secret go to `secrets/` only (R3).
- **Bounded vocabularies.**
  - The preflight `code`s are a closed set, tested.
  - `maku_factory_private_code_unacknowledged{repository}` is a 0/1 gauge.
- **Strict decoding.** The broker, factory and run-controller configs gain `preflight` keys (the ops route is always on, but the knobs are config), so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until the release. Name this in each PR body.
- **Maku house rules:**
  - the SPDX line on every Go file;
  - stdlib `testing`, table-driven;
  - conventional commits in English, with no attribution lines or trailers;
  - golangci-lint v2's full set;
  - no error string ends in punctuation.
- **This machine:**
  - wrap every test run in `systemd-run --user --scope -q -p MemoryMax=6G`;
  - never set `HOME=` or use `env -i`. The wizard's tests redirect `XDG_CONFIG_HOME` and use `t.TempDir()`;
  - `internal/store` needs Docker;
  - the kind and k3s jobs run in CI, not here: this machine is memory-bound (as #521 did for `external-db`).
- **Task commands:**
  - `task chart:check` after a chart change;
  - `task crd:gen && task crd:check` after an API type change (Task 9);
  - `task check` before the last commit of each PR.

## Drift this plan builds on

What shipped differs from the earlier plans in ways step 7 must respect. Each line names its source.

| # | What shipped | Source | What step 7 does with it |
|---|---|---|---|
| F1 | The broker cannot read the Atlas operator's ledger as `rooms_broker`. It serves anyway, logs a warning and sets `maku_rooms_schema_unverified` to 1, and `RoomBrokerSchemaUnverified` alerts. A readable ledger that is behind or ahead is refused | #521 deviations | The broker's `database` check reports `warn`/`schema_unverified`, not `fail`, while the bridge exists. Step 8 turns it into a refusal |
| F2 | cnref's ledger holds 14 revisions, not 16. The live check reads `adopted 14 revisions…, applied 2` | #521 plan errors | The decision pack's ANR checklist quotes the corrected line |
| F3 | The external database proof is a CI leg (`external-db`) in `demo-e2e.yaml` | #521 deviations | The k3s job reuses its Postgres container setup |
| F4 | Dex refuses client credentials, so a Slack App cannot run on Dex. Keycloak's realm roles are in the access token only. `tokenUse: id` works on both | #524 probe | The wizard asks for the IdP's family only to warn. It refuses Slack with Dex, and under `tokenUse: id` with Keycloak it suggests `rolesClaim: groups` and a groups mapper |
| F5 | Under `tokenUse: id` the broker never parses `X-Forwarded-Access-Token`. The design is safe only if no route reaches the broker's human port around oauth2-proxy | #524 hand-off | The factory's exposure check (Task 5) fails `broker_exposed_around_proxy` when an HTTPRoute or Ingress in the namespace targets the broker's `human` port |
| F6 | Step 5 built no front door. `docs/identity.md` gives oauth2-proxy's arguments per preset, and step 6 PR D builds it | #530 deviations | The wizard writes `webProxy` values and never asks for oauth2-proxy arguments |
| F7 | `maku configure` caches the issuer's endpoints under `generic` only, and `/.well-known/roomctl` serves `preset` and `tokenUse` | #530 deviations | The CLI check reads `/.well-known/roomctl` through the web hostname |
| F8 | The run's tracing endpoint must be `http(s)://<svc>.<ns>.svc.<domain>:<port>`, with `collector.namespace` equal to the endpoint's | #526 deviation 2 | The wizard validates the endpoint with the chart's own rule |
| F9 | `chart:tracing:k3d` runs with `NETPOL=none`, because the gateway leg's `gw-caller` probe pod fails under the runs namespace's default-deny | #526 deviation 4 | Task 16 fixes `gw-caller` (its own policy) before the kind job runs the gateway in `kubernetes` mode |
| F10 | The knowledge timeout is 1 s to 12 s, `networkPolicy.to` takes the mode's own shape, and an `isError` result counts as `upstream_error` | #527 deviations | The broker's `knowledge` check calls `tools/list` under the configured timeout, and the wizard writes `to` in the chosen mode's shape |
| F11 | The cost cross-check needs `maku-factory.enabled`, its `meter.url` and a priced catalog | #529 deviations | The wizard turns monitoring's cost group on only with a priced catalog |
| F12 | `slack.enabled` already exists as an umbrella fixture flag, `disabled` alert names are validated, and `docs/observability.md` exists | #523 deviations | The wizard writes `monitoring.prometheusRule.disabled` only from the chart's own list |
| F13 | Most of H5's readiness was built in step 3 (`Settings.TokenSource`, `Evaluator.Grants`). On the run-controller, readiness R2 reads the minter's role table | #531 | No preflight check duplicates readiness. The decision pack links the Evidence page's readiness row |
| F14 | Each legacy per-repository `MergePolicy` logs a warning that it is removed in the next minor | #522 | The wizard writes class-shape MergePolicies and bindings only |

## Rulings made while planning

- **IN1 (what exists today; this step builds only what is missing).**

  | Spec item | Today on `origin/main` | Missing, built here |
  |---|---|---|
  | **D24: the preflight** | <ul><li>The run-controller's `Preflight` runnable (`internal/runctl/preflight.go`) checks the RuntimeClass, the git Secrets, the CNI with a netprobe pod (`preflight_network.go`), and the gateway's JWKS, aliases and `gateway-token-ttl` (`preflight_gateway.go`). It refuses runs and sets `maku_run_controller_preflight_passed`.</li><li>The factory checks each App installation's grants at start (`internal/app/factory_apps.go:93`, `minter.CheckGranted`).</li><li>The merge-gate observer sets `agent_factory_merge_gate_required{repository}`.</li><li>`maku doctor` checks the CLI's own setup only.</li><li>Every component's ops mux is `internal/app/ops.go` (`/metrics`, `/healthz`, `/readyz`, `/startupz`).</li><li>No chart renders a Helm test hook</li></ul> | The catalog, the runner, `/preflight` on each ops mux, the chart objects and the CLI (PRs A to C) |
  | **D24: the wizard** | <ul><li>`maku demo app` (`internal/app/maku_demo_app.go`) runs GitHub's manifest flow with a loopback callback, the demo's single App and a fixed permission map. It writes id, key and slug to `--dir`.</li><li>`internal/githubapps` holds each App's required set as data (`Agents()`, `Factory(MergerOptions)`, `Merger`), and its doc comment names `maku install` as its consumer.</li><li>No `maku install` exists</li></ul> | The two Apps from `githubapps` (E1), the seam flow and the writer (E2) |
  | **D24: the install page** | `docs/quickstart.md` (the laptop), `docs/upgrading.md` (per step), `docs/identity.md` (#530), `docs/database.md` (#521), `docs/observability.md` (#523), `docs/github-apps.md`, `docs/gateway.md`, `docs/security.md`. There is no page for a cluster install and no seam table outside the spec | `docs/install.md` (PR H) |
  | **D23: per-PR installs** | `demo-e2e.yaml` runs k3d (k3s) legs `none`, `kubernetes`, `unenforced` and `gateway` with the demo profile, plus `external-db` once #521 merges. `quickstart-e2e.yaml` runs kind with Cilium, gVisor, cert-manager and CNPG. Neither installs `profile: production`, Calico, or Traefik Ingress | `install-e2e.yaml` (PR F) |
  | **D23: nightly** | The demo nightly (kind, real model, test App), and `migrate-e2e.sh` (from v0.30.0, the group move). No upgrade from the last minor | The upgrade and GitOps legs (PR G) |
  | **D3** | The demo refuses a private repository (`internal/app/factory.go:352`, `requirePublic`). Outside the demo a private repository is served silently. `Coordination.PublicOnly` refuses private families | `spec.privateCode` (PR D) |
  | **D2: several installs per cluster** | Fixed-name cluster objects: `maku-factory-admin`, `maku-mergepolicy-editor` and `maku-viewer` (`charts/maku-factory/templates/clusterroles.yaml`), and `agent-factory-flux-reader` (`rbac.yaml:101`). A second release fails on their ownership. The VAPs, the RuntimeClass ClusterRole and the run-pod policies are already named from values | Task 18 (R10) |

- **IN2 (the check catalog).** One table in `internal/preflight/catalog.go`. `docs/install.md` renders it between markers, and a test keeps the two equal.

  | Id | Seam | Runs in | Check | `fail` codes (`warn` in italics) | From |
  |---|---|---|---|---|---|
  | `kubernetes` | cluster | pod | server version ≥1.30 | `kubernetes_too_old` | D26 |
  | `crds` | cluster | pod | the six maku CRDs exist at the chart's served versions; agentgateway's four when `maku-gateway.enabled`; Gateway API standard ≥v1.2 when `expose.mode: httproute` or `maku-gateway.enabled`; prometheus-operator's when any `monitoring.*.enabled`; `external-secrets.io/v1` when `externalSecrets.enabled` | `crd_missing`, `crd_version` | D13, A11, step 4, step 6 |
  | `runtimeclass` | sandbox | pod | the RuntimeClass named by `sandbox.runtimeClassName` exists | `runtimeclass_missing`, *`unsafe_allow_runc`* | D18 |
  | `secrets` | secrets | pod | each Secret in step 5's registry that the values enable exists with its documented keys (Role `get` by `resourceNames`) | `secret_missing`, `secret_key_missing` | D14 |
  | `run-path` | network, sandbox | run-controller | its `Preflight` result: RuntimeClass, git Secrets, the CNI netprobe, the gateway (JWKS, aliases, token TTL) | the existing reasons, mapped one to one; *`network_policy_none`* | D6, D18, step 4 |
  | `database` | Postgres | broker | step 5's database check over its three DSNs: reachable, version ≥16, roles without `BYPASSRLS` or superuser, and the schema current | `db_unreachable`, `db_too_old`, `db_role_bypasses_rls`, `db_schema_behind`, `db_schema_ahead`, *`schema_unverified`* (F1) | D9 |
  | `oidc` | identity | broker, factory | discovery answers, `issuer` matches, the JWKS fetches, and the roles claim is configured | `oidc_discovery`, `oidc_issuer_mismatch`, `oidc_jwks` | D8 |
  | `github-link` | identity | broker | when `human.githubLink` is set: the OAuth client authorizes, and its callback is the web hostname's `/link/github/callback` (R5) | `link_client`, `link_callback_mismatch` | D8 |
  | `apps` | GitHub | factory | each served repository has both App installations, and `CheckGranted` holds for each | `app_not_installed`, `app_permission_missing` | D10, D12 |
  | `merge-gate` | GitHub | factory | per repository with `gateStatus`: the default-branch ruleset requires `maku/merge-gate` with the posting App's `integration_id` (the merge-gate ruling) | `merge_gate_not_required`, *`merge_gate_rules_unread`* | D11 |
  | `private-code` | GitHub | factory | each private or `internal` repository has `privateCode.acknowledged` | *`private_code_unacknowledged`* (R1; `fail` from step 8) | D3 |
  | `models` | models | broker, run-controller | the planner and the classifier clients, and one run alias per listener, answer a one-token completion through the gateway | `model_unreachable`, `model_refused` | D7 |
  | `knowledge` | RunLore | broker | `tools/list` lists `kb_search` and `kb_get` within the configured timeout (F10) | `knowledge_unreachable`, `knowledge_tools_missing` | D20 |
  | `exposure` | exposure | factory (in-cluster), CLI (outside) | in-cluster: no route targets the broker's `human` port (F5). Outside: each hostname resolves; the web host answers oauth2-proxy's `/ping`; the Slack host answers an unsigned `POST /slack/events` with 401 and a `GET` with 404 or 405 | `broker_exposed_around_proxy`, `host_unresolved`, `route_unanswered`, `slack_route_too_wide` | D19 |
  | `tracing` | observability | run-controller | when `runController.tracing` is set, the collector's OTLP port accepts a TCP connection from a pod labelled as a run | `collector_unreachable` | D21, step 6 R1 |

  **Not checks:** hardening readiness (it has its own row on the Evidence page, F13), alert delivery (Alertmanager is the user's), and backups (D9: the operator's job).

- **IN3 (where each check runs; rejected alternatives).**
  - **Chosen: each component checks its own path** and serves the result. The preflight pod's own NetworkPolicy then needs egress to only two places: the API server, and the components' ops port in its namespace. The components' policies gain ingress on `:9090` from the preflight pod's labels, beside `networkPolicies.kubernetes.metricsFrom`.
  - **Rejected: the preflight pod probes everything itself.** It would need its own egress to the database, the IdP, GitHub, the gateway and RunLore. It would then prove that the preflight pod can reach them, not that the broker can: a missing broker egress rule would pass.
  - **Rejected: the CLI checks from the laptop.** The laptop's network is not the cluster's. Database and gateway reachability from outside means nothing, and it would need port-forwards.
  - **The component results are computed, not stored.** `GET /preflight` runs the component's checks on demand, each bounded to 10 s and all within 30 s. It caches the result for 30 s, so a scrape storm costs nothing. It answers `{"component","version","checks":[…]}` with bounded codes. It is unauthenticated, like `/metrics` on the same port, so it carries no value a metric would not.

- **IN4 (the runner and its objects).** These render only with `preflight.enabled`:
  - `ServiceAccount` `<release>-preflight`;
  - a `Role` holding:
    - `get` on the registry's Secrets by `resourceNames`;
    - `get`/`list` on HTTPRoutes and Ingresses in the namespace;
  - a `ClusterRole` holding:
    - `get` on the named RuntimeClass;
    - `get` on the named CRDs;
    - the version endpoint;
  - a `NetworkPolicy` (`kubernetes` or `cilium` mode) whose egress is only the API server and `:9090` on the components;
  - **the test hook:** a `Job` annotated `helm.sh/hook: test` and `helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`, with `backoffLimit: 0`;
  - **the template:** a `CronJob` `<release>-preflight` with `suspend: true` and the same pod template. `maku preflight` and Argo CD users create a Job from it (R2).

  The pod runs `maku-run-controller preflight` under the restricted security context, with requests and limits and no service links. Both probes are N/A because it is a Job. It writes the JSON-lines report to stdout and a one-line summary to its termination message (under 4 KiB). The pod template lives in a named template, so the Job and the CronJob cannot drift (a render test compares them).
  - **Rejected: a new image.** It would add a seventh signed image to `release.yaml` for one subcommand.
  - **Rejected: the `maku` CLI as an image.** It does not exist, and it holds no Kubernetes client.

- **IN5 (the report).**
  - Lines are `{"check","component","outcome","code","message","seconds"}`, sorted by seam and then by id. The text summary is a table grouped by the seams of the spec's seam table, so the user reads it in the same order as `docs/install.md`.
  - Exit status 0 means no `fail`. `--strict` also fails on `warn`, and the CI jobs use it.

- **IN6 (the wizard's output tree).**

  ```text
  <out>/
    README.md                     # what was written, the commands to run next, in order
    values.yaml                   # profile: production, every seam answered; no secret value
    crds/values.yaml              # agent-factory-crds (and agentgateway-crds when chosen, A11)
    factory/
      factory.yaml                # Factory, with mergeDefaults {gateStatus: true, policyBot: false}
      repository-<name>.yaml      # one per repository, privateCode set from the visibility read
      mergepolicy-<class>.yaml    # class-shape policies the answers enable (F14); none by default
    github/
      ruleset-merge-gate.json     # the merge-gate ruleset (agent PRs, bypass for humans and the merger)
      apply-ruleset.sh            # the gh api commands; the wizard never runs them
    secrets/                      # 0700; .gitignore '*'
      agents-app.yaml             # Secret manifest: app-id, private-key (R3)
      factory-app.yaml            # Secret manifest: app-id, private-key, client-id, client-secret
      web-proxy.yaml              # Secret manifest: a generated cookie secret, client-id; client-secret empty
      templates/*.yaml            # Secret manifests with empty keys: model keys, DSNs, IdP clients, Slack
    externalsecrets.yaml          # instead of secrets/templates when Secrets are ESO-delivered (D14)
    gitops/
      flux.yaml                   # HelmRepository/OCIRepository + HelmReleases: CRDs first, crds: Skip
      argocd.yaml                 # Applications with sync waves: CRDs, agentgateway CRDs (Prune=false), the umbrella
  ```

  - **`--print` is the only mode.** A bare `maku install` refuses with `maku install only writes files: run maku install --print --out DIR`. That reserves the bare command and keeps D24's "never applies" visible in the command line.
  - **`--out -`** prints `values.yaml` alone to stdout and writes nothing else. It refuses when Apps would be created, because their keys need `secrets/`.
  - **`--answers FILE`** answers every question non-interactively, with strict decoding. Each question's answers-file key is its id in `internal/install/questions.go`. The README's command list is generated from the same table.

- **IN7 (the seam questions).** Each question has an id, a default, the value keys it writes and a validator shared with the chart's schema. The validator is read from `values.schema.json` at build time (`go:embed`), so the wizard cannot accept what the chart refuses.

  | Seam | Asks | Writes |
  |---|---|---|
  | GitHub | github.com or GHES (`apiURL`, `webURL`, ≥3.14); the owner (organization or user); the repositories to serve | `github.*`; one `Repository` each, with visibility read through the App once it exists (D3) |
  | Apps | create both through the manifest flow (E1), or reuse existing ones (ids and key paths) | `apps.agents`, `apps.factory`, `apps.merger.mode: factory` |
  | Identity | the issuer; `preset` (`generic` or `zitadel`); `tokenUse`; the roles claim; the agents and admin group values; the web, CLI and (with Slack) Slack client ids; the IdP family, only to warn (F4) | `maku.broker.human.*`, `maku-factory.config.api.*`, `webProxy` |
  | Database | the three DSN Secret names and their key; the CA Secret; `sslmode` | `maku.broker.database.*` |
  | Sandbox | the RuntimeClass name (`gvisor`, GKE Sandbox's included; `kata`; other) | `maku.sandbox.runtimeClassName` |
  | Network | `kubernetes` or `cilium`; the gateway's or ingress controller's pod peers | `maku.networkPolicies.mode`, `expose.networkPolicy` |
  | Models | the providers (the presets `maku-gateway` knows), their Secret names and keys, the tier aliases, and prices (optional; F11) | `maku-gateway.*`, `maku.runController.model.mode: gateway` |
  | Exposure | `httproute`, `ingress` or `none`; the web hostnames; the parentRefs or ingress class; TLS Secret names (Ingress) | `expose.*` |
  | Slack | on or off; the Secret names | `slack.*`, `expose.slack` |
  | Monitoring | ServiceMonitors, PrometheusRule, dashboards; the sidecar label | `monitoring.*` |
  | Tracing | the OTLP endpoint (F8) | `maku.runController.tracing`, `maku-factory.config.tracing` |
  | RunLore | the knowledge MCP URL and its peers (F10) | `maku.broker.roomTools.knowledge` |
  | Secrets | plain Secrets, or ExternalSecrets against a named store | `externalSecrets.*` or `secrets/templates/` |

- **IN8 (the two Apps).**
  - Two manifests, built from `githubapps`:
    - **agents App:** `Agents()`;
    - **factory App:** `Factory(MergerOptions{Folded: true, Reruns, Labels, Status: true})`. It folds the merger (`apps.merger.mode: factory`, the default step 8 flips to). The options come from the answers: `Labels` when any class is live or monitored, `Reruns` when `checkReruns` is set.
  - Each manifest has:
    - `hook_attributes.active: false` and `default_events: []`: the factory polls, so no webhook (step 6 R2);
    - `public: false`;
    - `callback_urls: [<web>/link/github/callback]` on the factory App only, with R5;
    - `request_oauth_on_install: false`.
  - The flow runs the two Apps one after the other on one loopback listener. Each has its own `state`, checked in constant time, as `maku demo app` does. The conversion's `pem`, `client_id` and `client_secret` go to `secrets/` and nowhere else. The wizard then prints each App's install URL, and waits until the user confirms both installations, or skips with `--no-wait`.
  - **Rejected: three Apps.** D12 says two, and step 3 PR D made the fold the recommended mode.
  - **Reused:** `maku demo app`'s listener, form page and conversion code move to `internal/githubapps/manifest.go`, where both commands call them. `maku demo app` keeps its permission map, because the demo has one App (`TestDemoManifestUnchanged`).

- **IN9 (private code, D3).**
  - **The API:** `RepositorySpec.PrivateCode *PrivateCode` with `{acknowledged bool}`. A CEL rule says `acknowledged` may only be `true` when set, so an explicit `false` is refused as meaningless.
  - **The read:** the fence pass already reads each repository through the factory App. It reads `visibility` (`public`, `private` or `internal`), and treats `internal` as private. A failed read is "unknown", which counts as private for the warning (fail closed). On GHES, `internal` exists only with Enterprise; the same rule holds.
  - **R1, step 7:**
    - the condition `PrivateCode` is `False` with reason `Unacknowledged` and the D3 sentence as its message;
    - the gauge is 1;
    - the broker's `install.unsafe` gains `private_code_unacknowledged`, through the factory's existing push to the banner;
    - a warning is logged once per change of visibility.

    Work continues.
  - **Step 8** turns the same condition into a refusal: no new task for that repository. The message is the D3 sentence plus the fix.
  - **The demo** keeps `requirePublic`, which is stricter: an acknowledged private repository is still refused in the demo, because the demo's agents can read its secrets.
  - **Coordination's `publicOnly`** is unchanged: a private family stays refused whatever the acknowledgement says.

- **IN10 (the install jobs).**
  - **`kind-calico`:**
    - a kind cluster with `disableDefaultCNI`, Calico through the tigera operator (pinned), and gVisor's `runsc` with a RuntimeClass `gvisor`;
    - the e2e stub as the OIDC issuer, GitHub, model and OTLP sink;
    - Postgres 16 as a plain StatefulSet in another namespace: a BYO database the chart does not render;
    - `networkPolicies.mode: kubernetes`, `expose.mode: httproute` through agentgateway, and `model.mode: gateway`.
  - **`k3s` (k3d, R11):**
    - k3s's flannel and kube-router policy, and its bundled Traefik;
    - `expose.mode: ingress`;
    - Postgres as an external container on the Docker network, `external-db`'s setup (F3);
    - `sandbox.runtimeClassName: gvisor` names a test-only RuntimeClass with handler `runc`, step 6 Task 21's device. Production values refuse `unsafeAllowRunc`, and the `runtimeclass` check passes because it does not inspect the handler. The docs say plainly that this leg proves the network, Ingress and database seams, not the sandbox; `kind-calico` proves the sandbox.
  - **Both:**
    1. write their values with `maku install --print --answers hack/install-e2e/<leg>.answers.yaml` (built from the checkout);
    2. install the CRD chart and then the umbrella with plain Helm;
    3. run `helm test` (the preflight, `--strict`);
    4. file a docs-links issue on the fake GitHub, label it, and wait for a pull request whose `maku/merge-gate` status is `success`, then for the merge.
  - **The Kubernetes minors:** the three latest kind node images, pinned by digest in `hack/install-e2e/versions.env` and bumped by Renovate, on `kind-calico` per PR. k3s uses k3d's default image, pinned the same way.

- **IN11 (the nightly).**
  - **`upgrade`:**
    1. on `kind-calico`, install the last released minor's charts (`oci://ghcr.io/smana/charts/agent-factory-crds` and `agent-factory`, cosign-verified) with the answers file's values, as rendered by that release's own `maku install` binary;
    2. create a room and a task and wait for the run;
    3. load HEAD's images into kind (built in the job; the demo nightly cannot do this, `demo-e2e.yaml`'s header);
    4. `helm upgrade` the CRD chart and then the umbrella to HEAD.

    It asserts:
    - every Deployment UID is unchanged;
    - the room, the task and the merged pull request's record survive;
    - the broker's `migrate` applied only the new revisions;
    - `helm test` passes.

    The first release this can start from is step 6's minor, the first with `profile: production`. Until then, the job skips and says why in its summary.
  - **`gitops`:** the same two legs, installed by Flux (2.4) and Argo CD (2.12) from the wizard's `gitops/` files (R6), on kind 1.30 (R7).
  - **The existing real-model nightly** is unchanged.

- **IN12 (the install page's outline).** `docs/install.md`, "Install on your cluster":
  1. who it is for (D1) and what it is not (the quickstart);
  2. the seam table (the spec's, as amended by step 6 R1 to R3), with each seam's default and what is lost without it;
  3. `maku install --print`;
  4. Flux, Argo CD and plain Helm. The CRD chart goes first, then agentgateway's CRDs outside Helm (A11), then the umbrella with `crds: Skip`;
  5. `maku preflight` and the check table (IN2);
  6. **private code (D3)**, with the sentence verbatim. The Repository reference in `docs/factory.md` carries the same sentence;
  7. **sandbox nodes (D18):** GKE Sandbox (its class is `gvisor`), k3s with `runsc`, kubeadm with `runsc` or Kata, and EKS through Bottlerocket or a custom AMI;
  8. the support floor (D26);
  9. several installs in one cluster (D2, R10);
  10. upgrading, which links `docs/upgrading.md`.

- **IN13 (the v4.0 decision pack).** `docs/v4-readiness.md` is a reader page in `docs/README.md`'s index, so `hack/docs.sh` checks it. It holds:
  - one row per spec decision D1 to D26: what proves it, and a link to the test, the CI job or the ledger entry;
  - the ANR shrink checklist (D22) with each follow-up's status;
  - the two manual install runbooks (GKE per R9, kubeadm), each with a ledger template;
  - the step 8 removals still to land;
  - the open owner rulings that touch v4.0.

  The page states facts and links, never a recommendation to release. The release decision is the owner's.

---

## PR A: the preflight runner and the cluster checks (D24, D14, D18)

### Task 1: `internal/preflight`, the catalog and the report (M)

**Files:**
- Create:
  - `internal/preflight/catalog.go` (IN2: ids, seams, where each runs, its codes);
  - `internal/preflight/report.go` (IN5);
  - `internal/preflight/component.go` (the `/preflight` JSON shape, shared by the components and the collector);
  - the tests of each.

- [ ] **Step 1: failing tests:**
  - every code in a result is in its check's set (an unknown code panics in tests and maps to `undecided` in production);
  - the report sorts by seam and then by id;
  - exit 1 on any `fail`, and on `warn` with `--strict`;
  - a message that holds `postgres://`, `-----BEGIN`, `ghs_`, `ghp_` or `Bearer ` is refused by `report.Add` (a guard, tested);
  - the summary fits in 4096 bytes with every check failing.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/preflight/`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(preflight): the check catalog and the report`.

### Task 2: the runner and its chart objects (M)

**Files:**
- Create:
  - `internal/preflight/runner.go` (it runs the pod's checks, then collects each component's `/preflight` from the Service names the config lists, merges and prints);
  - `cmd/maku-run-controller` case `preflight`;
  - `charts/maku/templates/preflight.yaml` (IN4: SA, Role, ClusterRole and bindings, NetworkPolicy or CNP, the hook Job, the suspended CronJob, from one named pod template);
  - `charts/maku/templates/preflight-config.yaml`.
- Modify:
  - `charts/maku/values.yaml` and `values.schema.json` (`preflight.enabled: false`, `preflight.image` defaulting to the run-controller's, `preflight.timeout`);
  - the components' policies (ingress on `:9090` from the preflight labels, only with `preflight.enabled`);
  - `charts/maku/tests/render.sh`.

- [ ] **Step 1: failing cases:**
  - off renders nothing;
  - on renders the eight objects;
  - the Job's and the CronJob's pod templates are equal (yq);
  - the Role's Secret `resourceNames` equal the registry's for the values;
  - the pod passes the run-pod VAP's restricted rules (rendered through `kubeconform` and the chart's VAP test);
  - in `cilium` mode a CNP renders in place of the NetworkPolicy;
  - the six default renders are byte-identical with `origin/main`.

  Run `task chart:check`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the preflight runner, as a test hook and a suspended CronJob`.

### Task 3: the cluster checks (M)

**Files:**
- Create: `internal/preflight/cluster.go` (`kubernetes`, `crds`, `runtimeclass`, `secrets`), with its tests on a fake client.
- Modify: `internal/runctl/preflight.go`, which exports its RuntimeClass refusal as a function the cluster check calls, so the two cannot disagree.

- [ ] **Step 1: failing tests**, one per code in IN2's rows:
  - 1.29 is refused;
  - a CRD at an old served version is refused;
  - a missing `agentgateway` CRD is refused only with `maku-gateway.enabled`;
  - `unsafeAllowRunc` gives `warn`;
  - a Secret without one documented key names the key and not its value;
  - an RBAC denial is `undecided` (`fail`), not `secret_missing`.
  
  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(preflight): the cluster checks`. Open PR A. Its body states the strict-decoding note and that `preflight.enabled` is off by default.

## PR B: component checks (D24)

### Task 4: `/preflight` on the ops mux, and the broker's checks (L)

**Files:**
- Modify:
  - `internal/app/ops.go` (`GET /preflight` from a `preflight.Source`, cached 30 s, bounded per IN3);
  - `internal/app/broker.go` (its source: `database` over step 5's check, F1's `schema_unverified` as `warn`; `oidc` over step 5 PR B's discovery; `github-link`; `knowledge` over #527's client with `tools/list`; `models` for the planner and the classifier clients);
  - the broker config's `preflight` keys (`timeout`), strict.
- Create: `internal/app/broker_preflight_test.go`.

- [ ] **Step 1: failing tests:**
  - each code is reached by a fake: a Postgres 15 container, a role with `BYPASSRLS`, an unreadable ledger (`warn`), an issuer mismatch, a JWKS 500, a knowledge endpoint missing `kb_get`, and a gateway 401 for the planner;
  - a 30 s cache serves a second call without a database round trip;
  - no answer holds the DSN, even with a malformed one.

  `internal/store` needs Docker. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker): its own preflight, served on the ops port`.

### Task 5: the factory's checks (M)

**Files:**
- Modify:
  - `internal/app/factory.go`, `internal/app/factory_apps.go` (`apps` from `CheckGranted`; `merge-gate` from the observer's last read; `private-code` from Task 9's state, or `skip` before PR D; `exposure` in-cluster, F5);
  - the factory chart's Role (`get`/`list` on HTTPRoutes and Ingresses in its namespace, only with `preflight.enabled`).

- [ ] **Step 1: failing tests:**
  - an App missing `statuses: write` names it;
  - a repository without the second installation is refused;
  - a ruleset without `integration_id` gives `merge_gate_not_required`;
  - an HTTPRoute whose backend is the broker's Service on port `human` gives `broker_exposed_around_proxy`, while one to `rooms-web-proxy` passes;
  - the demo (one App) reports `apps` against the demo's map.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): its own preflight: Apps, the merge gate and exposure`.

### Task 6: the run-controller's report and the collector (S)

**Files:**
- Modify:
  - `internal/runctl/preflight.go` (a `Source` that serves `Result()` as `run-path`, mapping each refusal reason one to one; `tracing`; `models` per listener alias);
  - `internal/preflight/runner.go` (collect, `component_too_old` on 404);
  - `charts/maku/tests/install.sh` (with `PREFLIGHT=1`, `helm test` on the k3d demo).

- [ ] **Step 1: failing cases:**
  - the k3d `kubernetes` leg passes `helm test`;
  - the `unenforced` leg fails `run-path` with the netprobe's code;
  - an image without `/preflight` gives `component_too_old`.

  Run `task chart:install:k3d:netpol` with `PREFLIGHT=1`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): the run path's preflight, collected with the others`. Open PR B.

## PR C: `maku preflight` and the outside checks (D24)

### Task 7: the command (M)

**Files:**
- Create: `internal/maku/preflight.go`. It does the following:
  - find the release's CronJob by label;
  - create a Job from it, with the owner reference to the CronJob and a `maku-preflight-<rand>` name;
  - wait;
  - read the pod's log;
  - print the IN5 summary;
  - delete the Job unless `--keep`;
  - exit as the report says.
- Modify: `internal/app/maku.go` (the `preflight` command, `--namespace`, `--release`, `--kubeconfig`, `--strict`, `--json`), `docs/cli.md`.

- [ ] **Step 1: failing tests** on a fake client:
  - no CronJob prints `preflight.enabled is off in this release: set it, or run helm test`;
  - a failed pod's report is printed and the exit is 1;
  - `--json` prints the lines verbatim;
  - the Job is deleted on success and on failure.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(maku): maku preflight runs the release's preflight and prints it`.

### Task 8: the outside checks (S)

**Files:**
- Create: `internal/maku/preflight_outside.go`, which reads the release's `expose` hostnames from its HTTPRoutes and Ingresses and checks IN2's `exposure` row from the user's machine, with `/.well-known/roomctl` through the web host (F7).

- [ ] **Step 1: failing tests** on `httptest` servers and a fake resolver:
  - an unresolved host;
  - a Slack host that answers `GET /` with 200 (`slack_route_too_wide`);
  - a web host whose `/ping` is not oauth2-proxy's;
  - `--no-outside` skips the row.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(maku): the outside checks, from where the user stands`. Open PR C.

## PR D: private code by opt-in (D3)

### Task 9: the field, the read and the warning (M)

**Files:**
- Modify:
  - `api/factory/v1alpha1/factoryconfig_types.go` (`PrivateCode`, IN9's CEL);
  - `internal/factory/config` (`Repository.PrivateCode`);
  - the fence pass's repository read (`visibility`);
  - `internal/factory/statuswriter` (the `PrivateCode` condition);
  - `internal/factory/fmetrics` (the gauge);
  - the factory's push into the broker's `install.unsafe` (the code `private_code_unacknowledged`);
  - `web/src` (the banner's text for the new code);
  - the generated CRDs, `charts/agent-factory-crds/tests/render.sh`.

- [ ] **Step 1: failing tests:**
  - `acknowledged: false` is refused by CEL;
  - private without the field is condition False, gauge 1, banner code set, and the work continues (R1);
  - private with the field is condition True and gauge 0;
  - `internal` counts as private;
  - a failed read counts as private;
  - the demo still refuses private even when acknowledged;
  - the banner test shows the new code's sentence.

  Run the `config`, `statuswriter`, `fmetrics` and `app` packages, envtest for the CRD, and `task ui:test`. Expected: FAIL.
- [ ] **Step 2: implement**, then `task crd:gen && task crd:check`. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(api): private code served only on an explicit acknowledgement (D3), warned in this release`.

### Task 10: docs and the alert (S)

**Files:**
- Modify:
  - `charts/agent-factory/files/rules/factory.yaml` (`AgentFactoryPrivateCodeUnacknowledged`, 15 min, `runbook_url` to `factory.md#private-code`) and its promtool test;
  - `docs/factory.md` (the Repository reference's `privateCode`, with D3's sentence);
  - `docs/upgrading.md` (this release warns, step 8 refuses);
  - `docs/security.md`.

- [ ] **Step 1:** promtool cases: the alert fires after 15 minutes at 1, and is silent at 0. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `hack/docs.sh` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `docs: private code and its acknowledgement`. Open PR D.

## PR E1: the two Apps through the manifest flow (D24, D12)

### Task 11: `githubapps` builds the manifests (S)

**Files:**
- Create: `internal/githubapps/manifest.go` (`Manifest(kind, name, opts)`, IN8's fields, the `callback_urls` with R5).
- Modify: `internal/githubapps/apps_test.go`.

- [ ] **Step 1: failing tests:**
  - the agents manifest's permissions equal `Agents()`;
  - the factory manifest's equal `Factory(folded)` for each option set;
  - the agents manifest has no `statuses`, `workflows` or `administration` (the role table's invariants);
  - `hook_attributes.active` is false and `default_events` is empty;
  - `callback_urls` is present only on the factory App.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(githubapps): the two Apps' manifests, from their permission sets`.

### Task 12: the flow, shared with `maku demo app` (M)

**Files:**
- Create: `internal/githubapps/flow.go`: the loopback listener, the form page, the state check and the conversion, moved from `internal/app/maku_demo_app.go`, with a `Flow.Run(ctx, manifests...)` that creates several Apps on one listener.
- Modify: `internal/app/maku_demo_app.go` (it calls `Flow` with its one manifest), `internal/app/maku_demo_app_test.go`.

- [ ] **Step 1: failing tests:**
  - `maku demo app`'s existing tests pass unchanged, and its manifest bytes are unchanged (`TestDemoManifestUnchanged`);
  - two Apps are created in order on one listener, each with its own state;
  - a replayed or crossed `state` is refused;
  - a GHES base posts to `<webURL>/organizations/<org>/settings/apps/new`;
  - the conversion's `pem` never reaches stdout or stderr (captured).

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `refactor(githubapps): one manifest flow for the demo and the install`. Open PR E1.

## PR E2: `maku install --print` (D24)

### Task 13: the questions and the answers file (M)

**Files:**
- Create:
  - `internal/install/questions.go` (IN7's table: id, prompt, default, validator, value keys);
  - `internal/install/answers.go` (strict decode);
  - `internal/install/schema.go` (the umbrella's `values.schema.json` embedded through a generated copy, checked by `task chart:check`);
  - their tests.

- [ ] **Step 1: failing tests:**
  - every question writes only keys the schema knows;
  - every validator refuses what the schema refuses, with one table of cases per question;
  - an unknown answers key is refused;
  - F4's warnings: Slack with Dex is refused, and Keycloak with `tokenUse: id` gives a suggestion;
  - F8's endpoint rule;
  - F10's `to` shape per mode.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(install): the seam questions, from the chart's own schema`.

### Task 14: the writer (M)

**Files:**
- Create:
  - `internal/install/write.go` (IN6's tree; 0700 `secrets/` with `.gitignore`, 0600 files, R3; refuses a non-empty `--out` without `--force`, and never overwrites `secrets/`);
  - `internal/install/gitops.go` (`flux.yaml`, `argocd.yaml`);
  - `internal/install/factory.go` (the `Factory`, `Repository` and ruleset files);
  - golden tests under `internal/install/testdata/`.

- [ ] **Step 1: failing tests:**
  - three answers files give their goldens: generic with plain Secrets, zitadel with ESO, and GHES;
  - every written `values.yaml` passes `helm template` under `profile: production` and kubeconform (the test shells out to `helm`, skipped when absent, and always run by `task chart:check`);
  - the README's command order is CRDs, then agentgateway CRDs, then Secrets, then the umbrella, then the ruleset, then `maku preflight`;
  - no golden file outside `secrets/` holds a PEM, a client secret or a cookie secret;
  - `secrets/` is mode 0700, and its files are 0600.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(install): write values, Secrets, the factory objects and the GitOps files`.

### Task 15: the command, and the demo's answers (M)

**Files:**
- Modify:
  - `internal/app/maku.go` (the `install` command: `--print`, `--out`, `--answers`, `--force`, `--no-wait`, and the bare-command refusal, IN6);
  - `docs/cli.md`.
- Create:
  - `hack/install-e2e/kind-calico.answers.yaml`, `hack/install-e2e/k3s.answers.yaml`;
  - `charts/agent-factory/values-demo.yaml`'s gateway default (R4: `values-demo-gateway.yaml`'s content folded in, the file kept as an empty pointer for one minor);
  - `charts/agent-factory/tests/golden/` updates.

- [ ] **Step 1: failing tests:**
  - a scripted interactive run (stdin) gives the generic golden;
  - bare `maku install` is refused with IN6's message;
  - `--out -` with App creation is refused;
  - with R4, the demo golden shows `model.mode: gateway` and `task chart:install:k3d` still passes. This is the one intended change to the demo render. It is listed in the PR body.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(maku): maku install --print`. Open PR E2 with IN6 to IN8 in the body.

## PR F: the install jobs on every chart PR (D23, D2)

### Task 16: the shared harness and `gw-caller` (M)

**Files:**
- Create:
  - `hack/install-e2e/lib.sh` (it builds the checkout's images and loads them; runs the wizard from the answers; installs the CRD chart, agentgateway's CRDs server-side, the Secrets and the umbrella; runs `helm test`; drives one task through the stub; collects diagnostics);
  - `hack/install-e2e/versions.env`;
  - `hack/install-e2e/lib_test.sh`.
- Modify: the gateway leg's `gw-caller` probe in `charts/agent-factory/tests/install.sh`, which gets its own NetworkPolicy so it passes under `kubernetes` mode (F9).

- [ ] **Step 1: failing cases:**
  - `NETPOL=kubernetes GATEWAY=1 task chart:install:k3d` passes, where today it fails at `gw-caller`;
  - `lib_test.sh` covers the wizard call and the order of the installs.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `test(install): the shared install harness; gw-caller under default-deny`.

### Task 17: the two legs and the workflow (M)

**Files:**
- Create:
  - `hack/install-e2e/kind-calico.sh`, `hack/install-e2e/k3s.sh` (IN10);
  - `.github/workflows/install-e2e.yaml`. It triggers on PRs touching `charts/**`, `internal/install/**`, `internal/preflight/**` and `hack/install-e2e/**`. Its matrix is `kind-calico` × the three kind minors, plus `k3s`. Its permissions are `contents: read`, it has a 45 min timeout, and diagnostics always run.
- Modify: `taskfile.yaml` (`install:kind-calico`, `install:k3s`), `docs/development.md`.

- [ ] **Step 1: run** both legs in CI on the PR. Expected, once implemented:
  - the wizard writes its tree;
  - `helm test` passes `--strict`;
  - a docs-links task's pull request gets `maku/merge-gate` `success` and is merged by the factory App;
  - on `kind-calico`, a run pod's `runtimeClassName` is `gvisor` and `runsc` is its handler;
  - on `k3s`, Traefik serves the web host and `POST /slack/events` only.
- [ ] **Step 2: fix** until green.
- [ ] **Step 3: commit** `ci: install on kind with Calico and gVisor, and on k3s, on every chart PR (D23)`.

### Task 18: two installs in one cluster (D2, R10) (M)

**Files:**
- Modify:
  - `charts/maku-factory/templates/clusterroles.yaml` (deleted). The three aggregated ClusterRoles move to `charts/agent-factory-crds/templates/clusterroles.yaml`;
  - `charts/maku-factory/templates/rbac.yaml` (`agent-factory-flux-reader` becomes `<release>-flux-reader`);
  - an audit of every cluster-scoped object in the four charts, each one release-prefixed or moved, with a test that lists them;
  - `docs/upgrading.md` (the adoption commands for the three ClusterRoles: label and annotate them to `af-crds` before upgrading, as step 1's CRD adoption);
  - `hack/install-e2e/kind-calico.sh` (a second release `team-b` in its own namespace, `runController.createRuntimeClass: false`).

- [ ] **Step 1: failing cases:**
  - the render test lists every cluster-scoped object, and none has a fixed name outside `agent-factory-crds`;
  - the `kind-calico` leg installs `team-b` and both releases pass `helm test`;
  - a run in `team-b` cannot reach `team-a`'s broker (the netprobe's negative control, across namespaces);
  - the CRD chart's adoption test (step 1's) covers the ClusterRoles.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts)!: several installs in one cluster (D2): shared roles in the CRD chart`. It is a breaking change for the three ClusterRoles' ownership, with adoption documented. Open PR F.

## PR G: the nightly upgrade and GitOps legs (D23, D26)

### Task 19: the upgrade leg (M)

**Files:**
- Create: `hack/install-e2e/upgrade.sh` (IN11).
- Modify: `.github/workflows/install-e2e.yaml` (a `schedule` trigger and an `upgrade` job; it skips with a summary line while the last release has no `profile: production`).

- [ ] **Step 1: run** it with `workflow_dispatch`. Expected, once implemented: every IN11 assertion passes, from the newest release with `profile: production` to HEAD.
- [ ] **Step 2: fix** until green.
- [ ] **Step 3: commit** `ci: a nightly upgrade from the last minor to HEAD (D23)`.

### Task 20: the GitOps legs and the floor (S)

**Files:**
- Create: `hack/install-e2e/flux.sh`, `hack/install-e2e/argocd.sh` (the wizard's `gitops/` files applied to a kind 1.30 cluster with Flux 2.4 and Argo CD 2.12; the Argo CD leg runs the preflight through `maku preflight`, R2).
- Modify: `.github/workflows/install-e2e.yaml` (a `gitops` nightly job, with R6 and R7).

- [ ] **Step 1: run** it with `workflow_dispatch`. Expected, once implemented:
  - both tools install the CRDs first and then the umbrella;
  - agentgateway's CRDs survive a prune (A11);
  - a second reconcile changes nothing;
  - the preflight passes.
- [ ] **Step 2: fix** until green, then `task check`.
- [ ] **Step 3: commit** `ci: nightly installs through Flux and Argo CD at the floor (D26)`. Open PR G.

## PR H: "Install on your cluster" (D24, D3, D18, D26)

### Task 21: the page (M)

**Files:**
- Create: `docs/install.md` (IN12; the check table between `<!-- preflight:begin -->` markers, generated by a Go test from IN2's catalog, as `githubapps.DocTables` does for `docs/github-apps.md`).
- Modify:
  - `docs/README.md`, `docs/quickstart.md` (a pointer, and R4's memory note);
  - `README.md` (the install pointer);
  - `docs/roadmap.md` (step 7 built);
  - `charts/agent-factory/README.md`.

- [ ] **Step 1: failing test:** the marker test fails until the table is generated. `hack/docs.sh` is clean. Expected: FAIL.
- [ ] **Step 2: write** per `docs/docs-style.md`, for a reader who was not there. Run `hack/docs.sh`, `hack/names.sh` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `docs: install on your cluster`. Open PR H. After it merges, cut the step's v0.4x minor.

## PR I: the v4.0 decision pack

### Task 22: `docs/v4-readiness.md` (S)

**Files:**
- Create: `docs/v4-readiness.md` (IN13). The GKE runbook follows R9, and the kubeadm runbook gives its dry-run line after step 7's minor and its recorded run on step 8's last minor (R8). Each runbook ends with a ledger template:
  - the date;
  - the Maku version and digests;
  - the cluster and Kubernetes version;
  - the CNI and sandbox;
  - the wizard's answers file, with no secrets;
  - the `maku preflight --json` output;
  - the task, the pull request and the merge;
  - the defects found, each with its issue.
- Modify: `docs/README.md`, `docs/roadmap.md`.

- [ ] **Step 1: write.** Every D1 to D26 row links an existing test, job or doc anchor. A row with no evidence yet says so, with its step. Run `hack/docs.sh` and `task check`. Expected: PASS.
- [ ] **Step 2: commit** `docs: the v4.0 decision pack`. Open PR I. Its body lists the open owner rulings that touch v4.0 and states that this PR cuts nothing.

## After this step (not in these PRs)

- **Owner decisions:** R1 to R11 are settled (above). Step 6's open one, renaming `factory_slack_*` to `maku_slack_app_*`, is settled by step 8's R5 (yes, in step 8).
- **ANR follow-ups** (each its own PR after the step's Maku minor, each recorded with a gcp-0 live check in the session ledger, aws-0 after):
  1. **`privateCode` on each private repository**, if cnref serves any. Live check: the gauge reads 0 on both clouds before step 8.
  2. **`preflight.enabled: true`** in the umbrella values once step 6's follow-up 1 runs the umbrella. Flux's `test.enable: true` on the HelmRelease. Live check: `flux get helmreleases` shows the test passed, and the report has no `fail`.
  3. **The ClusterRoles' adoption** (R10) before the bump that carries Task 18: label and annotate `maku-factory-admin`, `maku-mergepolicy-editor` and `maku-viewer` to the CRD release.
- **The kubeadm dry run** (R8): after the step's minor, in a throwaway kubeadm cluster, from `maku install --print`. The defects go in issues, not in the ledger's proof slot.
- **Step 8** gets its own plan (`2026-11-13-v4-step-8-cleanup-plan.md`). From this step it takes:
  - flipping `preflight.enabled` to `true`;
  - turning `private_code_unacknowledged` into a refusal (R1);
  - turning `schema_unverified` into a refusal (F1);
  - deleting `values-demo-gateway.yaml` (R4).

## Self-review

- **D24:**
  - **the wizard:** it asks about each seam (IN7, Task 13), creates the two Apps through the manifest flow (IN8, Tasks 11–12), and writes values, Secrets or ExternalSecrets, the factory objects and GitOps files without applying anything (IN6, Task 14). The bare command refuses (Task 15);
  - **the preflight:** a Helm test hook and the CLI (IN4, Tasks 2 and 7). It checks the CNI, the RuntimeClass, the database's reachability and version, OIDC discovery, the App permissions and the gateway's models, every check named in steps 2 to 6, each where its traffic runs (IN2, IN3, Tasks 3–6 and 8);
  - **the install page** has the seam table (IN12, Task 21).
- **D23:**
  - per PR: kind with Calico and gVisor, and k3s with kube-router, Traefik and external Postgres, with fake models (IN10, Tasks 16–17);
  - nightly: the upgrade from the last minor and the GitOps legs (IN11, Tasks 19–20). The real-model job is unchanged;
  - the manual GKE and kubeadm installs have their runbooks and ledger templates (Task 22). The runs are the owner's (R8, R9);
  - managed clouds are not automated.
- **D3:** the field, the read, the warning in this step and the refusal in step 8 (R1, IN9, Tasks 9–10), with the sentence on the install page and the Repository reference (Tasks 10 and 21).
- **D2:** two installs in one cluster are proved (R10, Task 18).
- **D14:** a missing Secret or key fails the preflight by name (Task 3), and start-up refuses it (step 5, R2).
- **D25:**
  - `preflight.enabled` is off by default;
  - the private-code warning does not refuse;
  - the default renders are byte-identical, except R4's demo default and R10's ownership move, each listed;
  - no data migration;
  - no v4.
- **D26:** 1.30 is refused below and tested on the nightly (R7); the three latest minors run per PR; Helm 3.15; Flux 2.4 and Argo CD 2.12 on the nightly (R6).
- **The drift (F1 to F14)** each has a consumer above.
- **Risks named with a stop rule:**
  - **Argo CD does not run test hooks:** the suspended CronJob and `maku preflight` (R2), proved by the Argo CD leg (Task 20).
  - **A preflight that passes falsely:** each check runs in the component that will make the call (IN3), and "cannot decide" is `fail`.
  - **The App key on disk:** 0600 in a git-ignored directory, never printed (R3, Task 12's capture test).
  - **The wizard and the chart drifting apart:** the validators are read from the chart's schema, and every golden is rendered with `helm template` (Tasks 13–14).
  - **Fixed-name ClusterRoles on an upgrade:** adoption before the bump (follow-up 3).
  - **The nightly** goes red until the release (strict decoding).

---
