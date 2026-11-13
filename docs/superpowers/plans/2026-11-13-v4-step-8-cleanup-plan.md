# v4 rollout step 8, clean-up: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** once cloud-native-ref (cnref) runs only the product path, Maku deletes the paths steps 1 to 7 kept for it. It flips the defaults that steps 2 to 7 left at their old values, and deletes the `profile` switch. After this step:
- the chart's defaults are the product path;
- the demo is a values file;
- every key that remains is one a v4.0 user may rely on.

The chart is then ready for v4.0.0. Cutting it waits for the owner's go, after the recorded GKE and kubeadm installs (D23, step 7 R8).

This step has two halves:
- **Signals first (PR A, its own minor).** Every deprecated path reports, while it is still served, that it is in use. It does so through a gauge, a start-up warning, the preflight and the install banner. cnref then shows, live, that it no longer uses any of them.
- **Removals after (PR B to J, the next minor).** Each removal names the signal that must read zero on both clouds before it merges (R1, R2). A removal is a breaking change inside v0.4x, which the spec allows before v4.0.0 (D13). Each one is recorded in one consolidated migration note (PR J).

**Not here (the reusable merge policies plan's PR K owns them; release N+1):**
- the per-repository `MergePolicy` (`spec.repositoryRef`, `classes`, `merge`, its legacy status fields, `ReasonNoMergePolicy`, `ReasonDuplicatePolicy`, `MergePolicyManifest`);
- the repository file's `classes:` grammar and `conditions.any`;
- `Generations.MergePolicy` and the legacy `Resolve`.

This step neither signals nor deletes those. PR K's own warning (`internal/factory/resources/assemble.go:257`) and its precondition (`kubectl get mergepolicy -A -o jsonpath='{..repositoryRef}'` empty) are its signal. PR D below flips merge defaults in the same types, so it merges after PR K (CL6).

**Architecture:**
- **One deprecation registry,** `internal/deprecation`, as data. Each entry has:
  - an id, the component, the old key or path, its replacement, and the step that introduced the replacement;
  - the PR here that removes it;
  - a detector: a function over the decoded config, and for runtime paths, a counter the code path increments.

  The broker, the factory and the run-controller each export `maku_deprecated_in_use{component, id}` (0/1), and log one warning per id at start. The chart reads the same registry, generated into `charts/maku/files/deprecations.yaml` and checked by a test. It lists the deprecated keys the values set in `NOTES.txt`. Step 7's preflight gains a `deprecated` check that lists every id at 1, as `warn`.
- **The removal PRs** delete code, keys and tests by area, in the order of the steps that made them obsolete. Each one deletes its registry entries too, so the registry is empty when the step ends and is then deleted itself (PR J).
- **The flips:** `networkPolicies.mode` defaults to `kubernetes`; `sandbox.runtimeClassName` to `gvisor`; `runController.gitTokens` and `model.mode` lose their old values; `merge.gateStatus` defaults to true and `merge.policyBot` to false; `apps.merger.mode` to `factory`; `preflight.enabled` to true; and the factory's run kind to the run-controller's. Each flip lands in the PR of its area, with a render test and a migration-note line.
- **The demo marker:** with `profile` gone, `values-demo.yaml`'s `demoValues: true` is the only switch that admits the demo-only settings. Those are `postgres.enabled`, `sandbox.unsafeAllowRunc`, `model.mode: secret` (R4) and the demo's single App (CL4).

**Tech Stack:**
- Go (deletions, and one small package `internal/deprecation`);
- Helm 3.15, JSON Schema, the bash render tests (`yq`);
- the Python harness (`images/maku-harness`, where its octo-sts exchange is deleted);
- promtool 3.15.0 (the alert on `maku_deprecated_in_use`, and R5's metric rename);
- step 7's install jobs and nightly upgrade as the proof that nothing the product path needs went with the deletions.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D5 (the XR goes);
- D6 (NetworkPolicy by default, Cilium optional);
- D7 (agentgateway only);
- D8 (the `readerFile` PAT goes);
- D10 (octo-sts optional);
- D11 (policy-bot optional);
- D12 (two Apps);
- D13 (the `profile` switch is deleted at the end of the rollout; breaking changes only in a major after v4.0.0);
- D16 (Kueue dropped);
- D18 (the RuntimeClass default);
- D22 (the ANR shrink);
- D25 (the room database is kept throughout);
- "Rollout", step 8;
- the owner rulings HR13 and Release;
- the "After this step" lists of steps 1 to 7, which name every item below.

**Read against:**
- Maku `origin/main` `2dd10422`. This plan assumes steps 5 to 7 and the reusable merge policies plan through PR K have merged. Every path below is checked against `2dd10422` and named where it is today. A PR that finds a path moved updates this plan's inventory in its body.
- ANR `origin/main` `fbaf995`: none of the cnref flips of steps 2 to 6 has landed. Step 8 cannot start before they have (the precondition gate).

**Branch:** ten PRs.

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, the deprecation signals | `feat/v4-deprecation-signals` | 1–3 | M | first, after step 7's minor; **its own minor (S8.1)** |
| B, the run path's old keys and Kueue (steps 2, D16) | `feat/v4-cleanup-run-keys` | 4–5 | M | after S8.1 and the gate for its ids |
| C, the XR run kind and octo-sts (D5, D10, R3) | `feat/v4-cleanup-xr` | 6–8 | L | after S8.1 and the gate; before D |
| D, the GitHub seams' old shapes and the merge defaults (D10–D12) | `feat/v4-cleanup-github` | 9–10 | M | after C (both edit `internal/app/factory.go`'s token wiring) and **after the reusable merge policies PR K** (CL6) |
| E, the gateway's old modes (D7, R4) | `feat/v4-cleanup-gateway` | 11 | S | after S8.1 and the gate |
| F, identity's old reader (D8) | `feat/v4-cleanup-identity` | 12–13 | M | after S8.1 and the gate (every human linked) |
| G, the chart contract: `profile`, `plain`, the subcharts' `crds/`, `preflight` on (D13) | `feat/v4-cleanup-chart` | 14–16 | M | after B and E (they edit the same values and the demo file) |
| H, warnings become refusals (D3, D9) | `feat/v4-cleanup-refusals` | 17–18 | S | after S8.1 and the gate |
| I, older deprecations (R5–R7) | `feat/v4-cleanup-older` | 19–21 | M | after S8.1 and the gate for its ids |
| J, the values contract and the migration note (D13) | `docs/v4-cleanup-contract` | 22–23 | M | last |

S8.1 is cut after PR A. S8.2, the step's last v0.4x minor, is cut after PR J. Step 7's recorded GKE and kubeadm installs then run on S8.2 (step 7 R8). The owner's v4.0 decision follows. This step cuts no v4 tag, release or chart.

## The precondition gate

A removal PR merges only when both are true:
1. Its ids read `maku_deprecated_in_use == 0` on gcp-0 and on aws-0 (R2) for seven consecutive days on S8.1, recorded in the session ledger as a query and its result.
2. The ANR follow-ups that retire the path are merged and live-checked.

| ANR follow-up (plan) | Retires | Gates |
|---|---|---|
| Step 1: `agent-factory-crds` ahead of the factory; `crds: CreateReplace` becomes `Skip` | the subcharts' `crds/` | G |
| Step 2: off the XR, Kueue and Kyverno duplicates deleted | `cloud.ogenki.io`, `queueName`, `networkPolicies.enabled`, the old sandbox keys | B, C |
| Step 3, follow-ups 1–4: off octo-sts, policy-bot retired per repository, the merger folded | octo-sts, `gitTokens: shared`, `githubSecret`, `mergerSecret`, the merge defaults | C, D |
| Step 4, follow-ups 1–5: the gateway as values, runs on the gateway, the planner moved, the Envoy AI gateway deleted | `model.mode: secret` outside the demo | E |
| Step 5, follow-ups 1–4: the ZITADEL preset explicit, the link, the reader gone, CNPG behind DSN Secrets | `readerFile`, `zitadelReaderSecret`; `schema_unverified` reads 0 | F, H |
| Step 6, follow-ups 1–8: the umbrella adopted component by component | `profile: production`'s role as a gate; `networkPolicies.plain` | G |
| Step 7, follow-ups 1–3: `privateCode`, `preflight.enabled`, the ClusterRoles' adoption | `private_code_unacknowledged` reads 0 | G, H |
| Reusable merge policies: ANR-1 to ANR-6, cnref-1, PR K released | the per-repository `MergePolicy` | D |

**If a path cannot reach zero** (a live dependency nobody planned for), its removal PR waits. The rest proceed. The decision pack (step 7 PR I) lists it as owed before v4.0.

## Owner rulings (settled 2026-11-13)

The owner answered yes to each, as the plan recommended (2026-11-13). The spec records them in its owner rulings table, prefixed "Step 8".

| # | Ruling | Answer and reason | Used by |
|---|---|---|---|
| R1 | Should step 8 ship as two minors, signals (S8.1) and then removals (S8.2), with each removal gated on its signal reading zero for seven days? | **Yes.** A removal inside v0.4x breaks whoever still uses the path. The signals turn "cnref runs only the product path" (the spec's precondition) into a query instead of a belief, and cost one extra minor | every PR |
| R2 | Must both clouds read zero before a removal merges, unless the owner has decommissioned aws-0? | **Yes.** aws-0 runs the same ANR tree a session later. A removal that only gcp-0 has proved breaks aws-0 at its next pin bump | B to I |
| R3 | Should step 8 delete octo-sts support entirely (in `forge`, the applier, the harness exchange and readiness's policy-file path), reading D10's "octo-sts becomes optional" as met once the XR, its only user, is gone? | **Yes.** octo-sts was never wired to the run-controller path. Keeping it "optional" would keep an unexercised token source. D10's guarantee (per-run, per-repository tokens) is the minter's, and that stays | C |
| R4 | Should `model.mode: secret` stay as a demo-only mode, refused without `demoValues` and flagged on the banner as `model_key_in_sandbox`, rather than be deleted? | **Yes**, given step 7 R4 (the demo's default became `gateway`). A laptop without agentgateway is still a useful first run, and the banner states what it costs. | E |
| R5 | Should step 8 rename `factory_slack_*` to `maku_slack_app_*` (step 6's "before v4.0.0 or never"), in one PR with the chart's rules and dashboards and no dual export? | **Yes.** After step 6's follow-up 5 the rules and dashboards ship in the chart, so they move in the same commit. Only queries ANR keeps by hand need edits, which `hack/names.sh` can list. Dual export doubles series for one minor and changes nothing for the chart's users | Task 21 |
| R6 | Should step 8 delete `maku-broker migrate-group`, `internal/migrategroup` and the v0.30.0 upgrade e2e, so that an install older than v0.31 upgrades through a v0.4x release first? | **Yes.** cnref migrated long ago. A new v4 user starts on `maku.ogenki.io`, and the migration costs a CI job and a code path nobody runs | Task 20 |
| R7 | Should step 8 delete the factory config's legacy single-repository shape (v2 D1: `repository`, `maintainers`, `triggerLabel`, `controlIssue` at the top level), documented as read "for one minor release"? | **Yes.** It is long overdue, and the Factory and Repository objects (Maku v0.41) replaced the file's repositories altogether. The factory resources cutover leaves no live user | Task 19 |
| R8 | Should the values schema-diff gate (V17) ship in step 8 report-only, armed to fail only by the owner's v4.0.0 release PR? | **Yes.** Before v4.0.0 breaking value changes are allowed (D13), and step 8 makes many. Arming the gate is part of cutting v4, which is the owner's go | Task 22 |
| R9 | Should the private-code warning become a refusal in step 8 (step 7 R1's second half)? | **Yes**, once ANR's follow-up shows the gauge at 0 on both clouds. D3 is the decision; step 7 only delayed it | Task 17 |
| R10 | Should the broker's "unreadable ledger" bridge (#521) become a refusal in step 8? | **Yes**, once `maku_rooms_schema_unverified` reads 0 on both clouds (ANR step 5 follow-up 4). #521 left it as a TODO for "a later minor", with no date in the binary. This is that minor | Task 18 |

## Global Constraints

- **No v4.** S8.1 and S8.2 are v0.4x minors. There is no v4 tag, release or chart version, and the schema-diff gate is not armed (R8).
- **Breaking, but announced.**
  - Each removal commit is `feat(<area>)!:` or `refactor(<area>)!:`, naming what is removed and its replacement.
  - S8.1 announces every removal (CL3).
  - A key that is removed is refused, never silently ignored. The values schema rejects it, and the template `fail`s with a message naming the replacement and the anchor in `docs/upgrading.md`. This keeps a GitOps tool from applying a values file that silently means something else. A config key the binaries decode strictly is refused the same way, by strict decoding, with a message naming the replacement.
- **The room database is kept (D25).** No migration drops a table or a column in this step. Rows written by a removed path stay readable, and the broker never refuses an old row.
- **The product path's proofs stay green.** Every removal PR runs `task check` and the chart's k3d legs. Step 7's `install-e2e` (kind-calico, k3s and two installs per cluster) runs on each PR, and the nightly upgrade from S8.1 to HEAD runs before S8.2 is cut. A removal that turns one red is wrong, not the test.
- **Default renders change on purpose.** Each flip lists its render diff in the PR body: the umbrella demo, the production example and `values-cnref-like.yaml` (step 6 Task 20). The cnref-like golden changes only where cnref has not already set the new value.
- **No credential in a log, a metric, an error or a rendered value.** The deprecation messages name keys and paths, never values.
- **Bounded vocabularies.** `maku_deprecated_in_use{component, id}` takes its ids from the registry, which a test closes.
- **Strict decoding.** The broker, factory, run-controller and Slack App configs lose keys, so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until S8.2. Name this in each PR body.
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
  - `task crd:gen && task crd:check` after an API type change (Tasks 6, 9);
  - `task chart:check` after a chart change;
  - `task harness:test` after a harness change (Task 7);
  - `task check` before the last commit of each PR.

## Rulings made while planning

- **CL1 (the inventory: everything earlier steps left for step 8).** Every row was named by an earlier plan's "After this step" or by a merged PR, and is checked against `2dd10422`.

  | # | Item (where it is today) | Kept by | Replacement | PR |
  |---|---|---|---|---|
  | 1 | `networkPolicies.enabled` (`charts/maku/values.yaml`; `""` mode reads it) | step 2 PR8 | `networkPolicies.mode`, **default `kubernetes`** (D6) | B |
  | 2 | `runController.runtimeClassName` (default `gvisor`), `runController.allowUnsandboxed` | step 2 PR8 | `sandbox.runtimeClassName`, **default `gvisor`**, and `sandbox.unsafeAllowRunc` | B |
  | 3 | The factory's `spec.queueName` writes (`internal/factory/runs/runs.go:254`), and the Kueue settings behind them | step 2 (D16) | the factory's slots and budgets, and the chart's ResourceQuota | B |
  | 4 | The `cloud.ogenki.io` run kind: `runwatch.DefaultKind` (`internal/runwatch/kind.go:24`, still the default), the XR branches in the broker and factory | step 2 (D5) | the run-controller's kind (`maku.ogenki.io`, already the chart's `runs.group`), **now the Go default too** | C |
  | 5 | octo-sts: `internal/factory/forge/octosts.go`, `OctoSTSTokens`, the applier's and publisher's octo-sts path, the harness's exchange (`images/maku-harness/git_credential_agent.py`), readiness's policy-file path (`internal/hardening/readiness.go`, H5's `PolicyFiles` over the XR) | step 3 PR14, H5 (#531) | the per-run minter (D10), and readiness from the role table (HR42) | C |
  | 6 | HR13's fix text naming "the XR and octo-sts" | HR13 | "per-run tokens" only | C |
  | 7 | The portable-run refusals kept for runs without `gatewayIdentity` (step 4 PR D), and the knowledge tools' mention of `mcp.yaml` federation (step 6) | steps 4, 6 | gateway identity and the broker pass-through (step 6 R5) | C |
  | 8 | `internal/factory/runtoken`, `runController.gitTokens: shared` (`charts/maku/values.yaml:210`, **today's default**), `gitReadSecret`, `unsafeSharedGitToken` (and PR9's `shared_git_token` banner code) | step 3 | `gitTokens: perRun`, the only mode | D |
  | 9 | `runTokens.appIDFile` and `privateKeyFile` aliases, `runTokens.appSecret`, `githubSecret`, `mergerSecret` (`charts/maku-factory`, `internal/factory/config/config.go:327–410`) | step 3 | `apps.agents`, `apps.factory`, `apps.merger` | D |
  | 10 | The merge defaults `gateStatus: false`, `policyBot` unset meaning true, `apps.merger.mode` unset meaning separate | step 3 PR10 | **`gateStatus: true`, `policyBot: false`, `apps.merger.mode: factory`**. policy-bot stays an opt-in integration (D11), and a separate merger App stays possible | D |
  | 11 | `model.mode: secret` (`charts/maku/values.yaml:185`, **today's default**) and `agent-model` in the runs namespace | step 4 | `model.mode: gateway`; `secret` demo-only under R4 | E |
  | 12 | `meter.logsURL`'s Envoy defaults in the docs | step 4 | the PromQL throttle (step 4 PR D) | E |
  | 13 | `values-demo-gateway.yaml` (an empty pointer since step 7 R4) | step 7 | `values-demo.yaml` | E |
  | 14 | `human.access.readerFile`, `api.access.readerFile`, `internal/ghidentity/zitadel.go`, `zitadelReaderSecret` (`charts/maku-factory/values.yaml:72`), the old `NewHumans` signature, the `SlackLinkReader` alias (`internal/bridgeapi`) | step 5 PR5 | the GitHub account link (D8) | F |
  | 15 | The `profile` switch (`charts/agent-factory/templates/_helpers.tpl`, `afumbrella.profile`) | step 1, step 6 R6 | the defaults are production; `demoValues` marks the demo (CL4) | G |
  | 16 | `networkPolicies.plain` (`charts/agent-factory/values-demo.yaml:186`) | step 6 | the components' own default-deny policies (step 2 PR13, step 6 Task 16) | G |
  | 17 | The `crds/` directories of `charts/maku` and `charts/maku-factory` | step 1 | `agent-factory-crds` alone | G |
  | 18 | `preflight.enabled: false` | step 7 | **default `true`** | G |
  | 19 | The private-code warning | step 7 R1 | a refusal (R9) | H |
  | 20 | The unreadable-ledger bridge (`internal/app/broker.go`, `schemaCurrent`'s TODO) | #521 | a refusal (R10) | H |
  | 21 | The legacy single-repository factory config (`internal/factory/config/config.go:123`, `normalize`) | v2 D1 | `Repository` objects | I (R7) |
  | 22 | `maku-broker migrate-group`, `internal/migrategroup`, `hack/quickstart/migrate-e2e.sh`, the `migrate-group` job | v0.30 | none: upgrade through a v0.4x first | I (R6) |
  | 23 | `factory_slack_*` metric names | step 6 | `maku_slack_app_*` | I (R5) |

- **CL2 (kept on purpose; not deprecated).**
  - `networkPolicies.mode: cilium`: optional (D6).
  - `networkPolicies.mode: none`: kept, on the banner (step 2).
  - `sandbox.unsafeAllowRunc`: demo and kind only (D18).
  - The ZITADEL preset (D8).
  - `postgres.enabled`: demo-only (D9).
  - `merge.policyBot: true` as an opt-in (D11).
  - `apps.merger.mode: separate` (D12).
  - `maku demo app`, which has its own App (step 7 IN8).
  - `maku`'s import of roomctl's files (`internal/maku/legacy.go`): it reads a user's own disk, removes nothing from the product, and is cheap.
  - The `roomctl` wire names (`/.well-known/roomctl`, `hack/names.sh`'s legacy set): they are identities, not shapes, and renaming them would break every configured CLI.
  - The room database's tables and columns (D25).

- **CL3 (the signals).**
  - **`internal/deprecation`** holds `Entry{ID, Component, Old, New, Since, RemovedBy, Detect}`. `Detect` reads the decoded config. For runtime paths (an XR AgentRun reconciled, an octo-sts exchange served, a reader-file lookup made), a counter is incremented where the path runs, and the gauge is 1 while the counter moved in the last 24 h.
  - **The gauge** `maku_deprecated_in_use{component, id}` is always exported, 0 or 1. The ids are closed by a test against the registry.
  - **The alert** `MakuDeprecatedPathInUse` is in the chart's PrometheusRule, a group rendered with any component. It is `info`, fires after 1 h, and its `runbook_url` is `upgrading.md#v4-clean-up`.
  - **Start-up** logs one `warn` line per id, with the old key, the new one and the removing release ("the next minor").
  - **The preflight** gains a `deprecated` check (`warn`, code `deprecated_in_use`, the ids in its message), so `maku preflight` shows the list.
  - **The chart:** `NOTES.txt` lists the deprecated keys the values set, from `charts/maku/files/deprecations.yaml`, which a Go test generates from the registry and compares.
  - **The banner:** a deprecated path is not unsafe, so `install.unsafe` is unchanged. Only the admin Settings page shows the list.

- **CL4 (the demo marker after `profile`).**
  - `profile` is removed. The schema rejects it, and the template fails with `profile was removed: the defaults are production; the demo is -f values-demo.yaml (upgrading.md#v4-clean-up)`.
  - `demoValues: true`, which `values-demo.yaml` already carries for step 1's check, becomes the only gate for the demo-only settings: `postgres.enabled`, `sandbox.unsafeAllowRunc`, `model.mode: secret` (R4), and the demo's single-App factory config. `networkPolicies.mode: none` is not demo-only: it stays allowed everywhere, on the banner (CL2). Each is refused without it, naming `values-demo.yaml`.
  - `afumbrella.production`'s seam checks (step 6 PR G) become the umbrella's only gate, renamed `afumbrella.seams`, and run on every render without `demoValues`.
  - **Rejected: keeping `profile` as an accepted no-op.** A key that does nothing is a key a user tunes for nothing.

- **CL5 (the order inside S8.2).**
  - B, C, E, F, H and I are independent once their gates hold.
  - C precedes D: both rewire the factory's token sources in `internal/app/factory.go`, and C removes the octo-sts branch D's code would otherwise still see.
  - G follows B and E, which edit the same `values.yaml` keys and `values-demo.yaml`.
  - J is last: it reads every removal's line.

- **CL6 (coordination with the reusable merge policies plan's PR K).**
  - **PR K deletes, in release N+1:**
    - the per-repository `MergePolicy` and its CEL, columns and status fields;
    - `Decode`'s legacy path;
    - `MergePolicyManifest`;
    - the file's `classes:` and `conditions.any`;
    - `Generations.MergePolicy`.
  - **Step 8 deletes none of these.** It also adds no deprecation entry for them, because PR K's warning already exists.
  - **PR D below flips `config.Merge`'s defaults** (`gateStatus`, `policyBot`). Those are the same struct `Factory.spec.mergeDefaults` and `Repository.spec.merge` carry. So PR D merges after PR K, on PR K's shapes: no rebase across a deleted shape, and one `task crd:gen` diff, not two.
  - **If PR K has not merged by the time D's gate holds,** D waits. B, C and E to I do not touch those types and proceed.
  - **The migration note (PR J)** links PR K's section of `docs/upgrading.md` ("N+1") instead of restating it.

- **CL7 (the values contract for v4.0, V17 and D13).**
  - `hack/values-contract.sh` compares the umbrella's and the subcharts' `values.schema.json` with the last released minor's (pulled from the OCI registry, cosign-verified). It classifies each change:
    - a removed or renamed key;
    - a narrowed type, enum or pattern;
    - a new required key;
    - a changed default.

    It prints them as a table. A removed or narrowed key is "breaking", and a changed default is "behaviour".
  - **Report-only (R8):** CI prints it on chart PRs and never fails. `docs/upgrading.md` says that from v4.0.0 a breaking line needs a major and a migration note, and that arming the gate is part of cutting v4.0.0.
  - **Rejected: a hand-kept list of the contract's keys.** It drifts. The schema is the contract.

---

## PR A: the deprecation signals (S8.1)

### Task 1: `internal/deprecation` and the gauge (M)

**Files:**
- Create: `internal/deprecation/registry.go` (CL1's rows 1–14 and 16–23 that a binary can detect, with their detectors), `internal/deprecation/registry_test.go`.
- Modify:
  - `internal/metrics`, `internal/factory/fmetrics` (`maku_deprecated_in_use`);
  - the start-up of the broker, the factory, the run-controller and the Slack App (`internal/app/*.go`);
  - the runtime counters at the XR reconcile (`internal/runwatch`), the octo-sts exchange (`forge/octosts.go`), the reader lookup (`ghidentity/zitadel.go`) and the shared git token (`internal/factory/runtoken`).

- [ ] **Step 1: failing tests:**
  - each detectable id is reached by a config fixture, or by a counter increment for the runtime ones;
  - a clean product-path config gives every id 0;
  - one warning line per id at start;
  - the ids are closed (an unknown id panics in tests);
  - no message holds a config value.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(deprecation): every path step 8 removes reports that it is in use`.

### Task 2: the chart's list, the alert and the preflight check (S)

**Files:**
- Create: `charts/maku/files/deprecations.yaml` (generated by `go test ./internal/deprecation -run TestChartList -update`).
- Modify:
  - `charts/maku/templates/NOTES.txt`, `charts/maku-factory/templates/NOTES.txt`, `charts/agent-factory/templates/NOTES.txt`;
  - `charts/agent-factory/files/rules/maku.yaml` (`MakuDeprecatedPathInUse`) and its promtool test;
  - `internal/preflight/catalog.go` (`deprecated`);
  - `web/src` (the admin Settings list);
  - `tests/render.sh`.

- [ ] **Step 1: failing cases:**
  - the cnref-like golden's NOTES list the keys cnref sets today;
  - the production example lists none;
  - promtool: the alert fires after 1 h at 1 and is silent at 0;
  - the preflight reports `deprecated_in_use` with the ids.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): list the deprecated keys a release sets, and alert while a path is in use`.

### Task 3: the announcement (S)

**Files:**
- Modify:
  - `docs/upgrading.md` (`## v4 clean-up`: the table of CL1 with each row's replacement, the release that removes it, and the query that shows it unused);
  - `docs/operations.md` (the alert);
  - `docs/roadmap.md`.

- [ ] **Step 1: write.** Run `hack/docs.sh` and `task check`. Expected: PASS.
- [ ] **Step 2: commit** `docs: what the next minor removes, and how to see you no longer use it`. Open PR A. After it merges, cut S8.1. The gate's seven days start when both clouds run it.

## PR B: the run path's old keys and Kueue (CL1 rows 1–3)

### Task 4: the network and sandbox keys (S)

**Files:**
- Modify:
  - `charts/maku/values.yaml` and `values.schema.json` (delete `networkPolicies.enabled`, `runController.runtimeClassName` and `runController.allowUnsandboxed`; `networkPolicies.mode` defaults to `kubernetes`; `sandbox.runtimeClassName` defaults to `gvisor`);
  - `charts/maku/templates/_helpers.tpl` (the resolution helpers lose their old-key branches; a removed key fails, naming its replacement);
  - `charts/maku/tests/render.sh`;
  - the deprecation registry (rows 1–2 deleted).

- [ ] **Step 1: failing cases:**
  - each removed key fails the render with its replacement named;
  - the default render is `kubernetes` mode with `gvisor`;
  - `cilium` still renders the CNPs;
  - the demo (`demoValues`) with `unsafeAllowRunc` still renders;
  - the cnref-like golden changes nowhere, since cnref sets `mode` and `sandbox`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts)!: the run path's old network and sandbox keys are removed`.

### Task 5: Kueue's `queueName` (S)

**Files:**
- Modify:
  - `internal/factory/runs/runs.go` (no `queueName` written);
  - the factory config's queue keys (removed; strict decoding refuses them, naming D16);
  - their tests;
  - the deprecation registry (row 3).

- [ ] **Step 1: failing tests:**
  - an AgentRun spec carries no `queueName`;
  - a config with the queue key is refused with D16's message.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory)!: no Kueue queue on runs (D16)`. Open PR B.

## PR C: the XR run kind and octo-sts (CL1 rows 4–7)

### Task 6: the run kind (M)

**Files:**
- Modify:
  - `internal/runwatch/kind.go` (`DefaultKind` becomes the run-controller's `maku.ogenki.io` kind; the `cloud.ogenki.io` kind and `xplane-run-` prefix go);
  - the broker's and factory's kind configuration (one kind; an XR group is refused, naming D5);
  - the XR-only branches they reach (`internal/factory/runs`, `internal/bridge/classify.go`);
  - the AgentRun CRD's XR-only fields, if any (`task crd:gen`);
  - `docs/architecture.md`;
  - the deprecation registry (row 4).

- [ ] **Step 1: failing tests:**
  - a config naming `cloud.ogenki.io` is refused;
  - the factory with no kind configured watches `maku.ogenki.io`;
  - every reconciler test that used the XR fixture runs on the run-controller fixture with the same outcome (the fixtures move, the expectations do not).

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task crd:gen && task crd:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat!: one run kind, the run-controller's (D5)`.

### Task 7: octo-sts (M)

**Files:**
- Delete: `internal/factory/forge/octosts.go` and its tests.
- Modify:
  - `internal/factory/forge/github.go`, `internal/factory/applier/applier.go`, `applier/publisher.go`, `internal/factory/minter/publisher.go` (the minter is the only token source);
  - `internal/hardening/readiness.go` and H5's `PolicyFiles` (R2/R3 read the role table only, HR42);
  - `internal/factory/config/hardening.go`;
  - `images/maku-harness/git_credential_agent.py`, `agent_checks.py` and their tests (the exchange goes; the per-run token file stays);
  - `images/maku-harness/README.md`;
  - `docs/security.md`, `docs/github-apps.md`;
  - the deprecation registry (row 5).

- [ ] **Step 1: failing tests:**
  - readiness on a repository reads the role table and never a policy file;
  - the harness with no octo-sts settings uses the mounted token;
  - a harness given an octo-sts setting refuses it by name;
  - the publisher mints through the minter only.

  Run `task harness:test` and the Go packages. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `refactor!: octo-sts is removed; the per-run minter is the token source (D10, R3)`.

### Task 8: the leftovers of the XR (S)

**Files:**
- Modify:
  - HR13's message in `internal/factory/config/config.go` (row 6);
  - the gateway-identity refusals kept for portable runs (row 7), now unreachable;
  - `docs/gateway.md` and the knowledge tools' docs (the `mcp.yaml` federation mention goes);
  - the deprecation registry.

- [ ] **Step 1: failing tests:**
  - HR13's message names per-run tokens only;
  - a config that only the removed refusals caught is now refused by the general rule, with the same key named.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `refactor: the XR's leftovers in messages and docs`. Open PR C.

## PR D: the GitHub seams' old shapes and the merge defaults (CL1 rows 8–10)

### Task 9: the shared token and the old Secret keys (M)

**Files:**
- Delete: `internal/factory/runtoken`.
- Modify:
  - `charts/maku/values.yaml` (`gitTokens` removed: per-run only; `gitReadSecret` and `unsafeSharedGitToken` removed);
  - the run-controller's config;
  - `internal/runctl/pod.go`, `internal/runctl/config.go`;
  - the broker's `install.unsafe` (the `shared_git_token` code goes) and `web/src`'s banner text;
  - `charts/maku-factory/values.yaml` and its helpers (`runTokens.appIDFile`, `privateKeyFile` and `appSecret`, `githubSecret` and `mergerSecret` removed; `apps.*` only);
  - `internal/factory/config/config.go:327–410` (the aliases);
  - the deprecation registry (rows 8–9).

- [ ] **Step 1: failing cases:**
  - each removed chart key fails, naming its `apps.*` replacement;
  - each removed config key is refused by strict decoding with the same message;
  - a reviewer run mounts its per-run read token;
  - the banner code list has no `shared_git_token`;
  - step 7's `install-e2e` legs still pass.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat!: per-run tokens only; the shared token and the old App keys are removed (D10, D12)`.

### Task 10: the merge defaults (S)

**Precondition:** the reusable merge policies PR K is merged (CL6).

**Files:**
- Modify:
  - `internal/factory/config/config.go` (`Merge.GateStatus` defaults to true when unset, so it becomes a `*bool`; `UsesPolicyBot` is true only when `policyBot: true`; `apps.merger.mode` defaults to `factory`);
  - `api/factory/v1alpha1/factoryconfig_types.go` (the doc comments on `mergeDefaults`; `task crd:gen`);
  - `charts/maku-factory/values.yaml`;
  - `docs/factory.md`, `docs/merge-policies.md`;
  - the demo (step 3's ruling: the demo requires no gate, so `values-demo.yaml` sets `gateStatus: false` explicitly).

- [ ] **Step 1: failing tests:**
  - an unset `merge` posts `maku/merge-gate` and does not consult policy-bot;
  - `policyBot: true` still routes through policy-bot (D11's opt-in);
  - an unset merger mode signs with the factory App;
  - `apps.merger.mode: separate` still works;
  - the demo render posts no gate;
  - the cnref-like golden changes nowhere, since cnref sets these after step 3's follow-ups.

  Expected: FAIL.
- [ ] **Step 2: implement**, then `task crd:gen && task crd:check`. Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory)!: the merge gate status on, policy-bot opt-in, the merger folded, by default (D11, D12)`. Open PR D.

## PR E: the gateway's old modes (CL1 rows 11–13)

### Task 11: `model.mode`, the docs and the demo file (S)

**Files:**
- Modify:
  - `charts/maku/values.yaml` (`model.mode` defaults to `gateway`; `secret` is refused without `demoValues`, R4);
  - the broker's `install.unsafe` (`model_key_in_sandbox` when `secret`) and `web/src`;
  - `charts/maku/templates/_helpers.tpl`;
  - `docs/gateway.md` (`meter.logsURL`'s Envoy defaults go, row 12).
- Delete: `charts/agent-factory/values-demo-gateway.yaml` (row 13).
- Also: the deprecation registry.

- [ ] **Step 1: failing cases:**
  - the default render is gateway mode;
  - `secret` without `demoValues` fails, naming R4's rule;
  - `secret` with `demoValues` renders and sets `model_key_in_sandbox`;
  - `-f values-demo-gateway.yaml` fails as a missing file, which `docs/upgrading.md` names.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts)!: the gateway is the model path; a key in the sandbox is demo-only (D7)`. Open PR E.

## PR F: identity's old reader (CL1 row 14)

**Precondition:** ANR step 5 follow-up 3 has run. Every active human is linked, and the reader's ids read 0.

### Task 12: the reader (M)

**Files:**
- Delete: `internal/ghidentity/zitadel.go` and its tests.
- Modify:
  - `internal/ghidentity` (the link is the only source);
  - the broker's `human.access` and the factory's `api.access` (`readerFile` removed; strict decoding names the link);
  - `charts/maku-factory/values.yaml` (`zitadelReaderSecret` removed) and its templates;
  - `charts/maku` (its reader mount);
  - the deprecation registry.

- [ ] **Step 1: failing tests:**
  - a config with `readerFile` is refused, naming `human.githubLink` and `api.access.links: broker`;
  - an unlinked human is refused as `link_missing` (step 5's code), never through a fallback;
  - the chart fails on `zitadelReaderSecret`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat!: the GitHub link is the only identity source; the ZITADEL reader is removed (D8)`.

### Task 13: the old signatures (S)

**Files:**
- Modify:
  - `internal/authn` (the old `NewHumans` signature goes; callers use the options form);
  - `internal/bridgeapi` (the `SlackLinkReader` alias goes);
  - their callers and tests.

- [ ] **Step 1:** `go build ./...` and the packages' tests after the deletion. Expected: PASS with no caller of the old names (`git grep` is empty).
- [ ] **Step 2: commit** `refactor(authn): drop the pre-preset constructor and the link reader alias`. Open PR F.

## PR G: the chart contract (CL1 rows 15–18)

### Task 14: `profile` goes, `demoValues` gates (M)

**Files:**
- Modify:
  - `charts/agent-factory/templates/_helpers.tpl` (`afumbrella.profile` deleted; `afumbrella.production` renamed `afumbrella.seams` and run without `demoValues`; the demo-only settings gated by `demoValues`, CL4);
  - `values.yaml`, `values.schema.json` (`profile` removed), `values-demo.yaml` (no `profile`), `values-production.example.yaml`, `tests/values-cnref-like.yaml`;
  - every template's first include;
  - `tests/render.sh`, `tests/schema.sh`, the goldens.

- [ ] **Step 1: failing cases:**
  - `profile: production` and `profile: demo` each fail with CL4's message;
  - a values file with no seams lists every missing seam in one message (step 6's gate, now the default);
  - `-f values-demo.yaml` renders the demo;
  - each demo-only setting fails without `demoValues`, naming `values-demo.yaml`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts)!: the profile switch is removed; the defaults are production (D13)`.

### Task 15: `networkPolicies.plain` and the subcharts' `crds/` (S)

**Precondition:** ANR's factory and broker HelmReleases have `crds: Skip` (step 1's follow-up), and `agent-factory-crds` manages the CRDs on both clouds (`kubectl get crd -l app.kubernetes.io/managed-by=Helm` shows release `af-crds`).

**Files:**
- Delete: `charts/maku/crds/`, `charts/maku-factory/crds/`.
- Modify:
  - `charts/agent-factory` (`networkPolicies.plain` and its template removed, row 16);
  - `task crd:gen` (it writes only into `charts/agent-factory-crds/files/`);
  - `task crd:check`;
  - `docs/upgrading.md`'s "Two charts, one set of CRDs";
  - the README of each chart (install `agent-factory-crds` first, also for `maku` or `maku-factory` used alone).

- [ ] **Step 1: failing cases:**
  - `crd:check` passes with one copy;
  - `networkPolicies.plain` fails, naming the components' policies;
  - step 7's install legs pass;
  - a `helm upgrade` of the nightly upgrade leg from S8.1 keeps every CRD and every object (`helm.sh/resource-policy: keep` on the CRD chart, and no subchart CRD left to delete).

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts)!: the CRDs live in agent-factory-crds only; plain policies are removed`.

### Task 16: `preflight` on by default (S)

**Files:**
- Modify: `charts/maku/values.yaml` (`preflight.enabled: true`), the goldens, and `docs/install.md`.

- [ ] **Step 1: failing cases:**
  - the default render holds the hook Job and the CronJob;
  - `values-demo.yaml` keeps them, and the k3d demo's `helm test` passes.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the preflight is on by default`. Open PR G.

## PR H: warnings become refusals (CL1 rows 19–20)

### Task 17: private code (D3, R9) (S)

**Precondition:** `maku_factory_private_code_unacknowledged` reads 0 on both clouds.

**Files:**
- Modify:
  - the factory's fence pass (an unacknowledged private repository gets no new task; in-flight tasks finish);
  - `internal/factory/statuswriter` (`PrivateCode=False` now also sets `Served=False`);
  - the preflight's `private-code` row (now `fail`);
  - `charts/agent-factory/files/rules/factory.yaml` (the alert's severity);
  - `docs/factory.md`, `docs/install.md`.

- [ ] **Step 1: failing tests:**
  - an unacknowledged private repository's labelled issue gets no task;
  - a task started before the flip finishes;
  - an acknowledged one is served;
  - the preflight fails `private_code_unacknowledged`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory)!: a private repository is served only on its acknowledgement (D3)`.

### Task 18: the unreadable ledger (D9, R10) (S)

**Precondition:** `maku_rooms_schema_unverified` reads 0 on both clouds (ANR step 5 follow-up 4).

**Files:**
- Modify:
  - `internal/app/broker.go` (`schemaCurrent`: an unreadable ledger refuses to serve, as a behind or ahead one does; the TODO and the gauge go);
  - `charts/agent-factory/files/rules/rooms.yaml` (`RoomBrokerSchemaUnverified` goes) and its tests;
  - the preflight's `schema_unverified` code (now `fail`, renamed `db_ledger_unreadable`);
  - `docs/database.md`.

- [ ] **Step 1: failing tests:**
  - `TestServeSchemaCurrent` refuses an unreadable ledger, naming the grant to add;
  - the gauge is gone from the metrics exposure test;
  - promtool has no rule left on it.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker)!: a ledger the broker cannot read is refused (D9)`. Open PR H.

## PR I: older deprecations (CL1 rows 21–23)

R5, R6 and R7 are settled yes, so all three tasks land.

### Task 19: the legacy single-repository config (R7) (S)

**Files:**
- Modify:
  - `internal/factory/config/config.go` (the `Repository`, `Maintainers`, `TriggerLabel` and `ControlIssue` top-level fields and `normalize`'s legacy branch go; strict decoding names `Repository` objects);
  - `internal/factory/config/mergedefaults.go:62–87`;
  - `internal/factory/resources/migrate.go:29`;
  - `internal/factory/intake/issues.go:190`;
  - their tests.

- [ ] **Step 1: failing tests:**
  - a legacy file is refused, naming the Repository object;
  - `maku config migrate` still reads a legacy file from the previous release's fixture and writes objects. The migration tool keeps reading what the factory no longer serves, as PR K does for its shape.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config)!: the single-repository shape is removed (v2 D1)`.

### Task 20: the group migration (R6) (S)

**Files:**
- Delete: `internal/migrategroup`, `internal/app/migrategroup_test.go`, `hack/quickstart/migrate-e2e.sh`, the `migrate-group` job in `.github/workflows/quickstart-e2e.yaml`, and the `maku-broker migrate-group` command.
- Modify:
  - `hack/names.sh` (its `group_allow` entries go, so the old group is refused everywhere outside `docs/superpowers` and `docs/maku-group-migration.md`);
  - `docs/maku-group-migration.md` (it becomes history: upgrade to the last v0.4x first);
  - `docs/upgrading.md`.

- [ ] **Step 1:** `hack/names.sh`, `go build ./...` and `task check`. Expected: PASS, with `git grep agents.ogenki.io` showing only key prefixes and history.
- [ ] **Step 2: commit** `refactor!: the agents.ogenki.io group migration is removed; upgrade through v0.4x first`.

### Task 21: the Slack App's metric names (R5) (M)

**Files:**
- Modify:
  - `internal/slackapp` (each `factory_slack_*` instrument is renamed `maku_slack_app_*`);
  - `charts/agent-factory/files/rules/slack.yaml` and its promtool tests;
  - `charts/agent-factory/files/dashboards/*.json`;
  - `docs/slack.md`, `docs/operations.md`;
  - `hack/names.sh` (the old names join the refused metric list).

- [ ] **Step 1: failing tests:**
  - the exposure test lists only `maku_slack_app_*`;
  - promtool runs the rules on the new names;
  - the dashboards' queries hold no `factory_slack_`;
  - `hack/names.sh` refuses an old name outside `docs/superpowers`.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(slackapp)!: the Slack App's metrics are maku_slack_app_*`. Open PR I. Its body lists ANR's hand-kept queries on the old names (`git grep factory_slack_` in ANR at the PR's date).

## PR J: the values contract and the migration note (D13)

### Task 22: the schema-diff report (S)

**Files:**
- Create: `hack/values-contract.sh` and `hack/values-contract_test.sh` (CL7).
- Modify:
  - `.github/workflows/ci.yaml` (a report-only step on chart PRs that writes the table to the job summary, R8);
  - `taskfile.yaml` (`chart:contract`);
  - `internal/deprecation` (deleted: the registry is empty), with its gauge, alert, NOTES list and preflight row.

- [ ] **Step 1: failing cases:**
  - fixtures of two schemas give each classification;
  - a removed key is "breaking";
  - a changed default is "behaviour";
  - a widened enum is neither;
  - the step's own diff against S8.1 is printed, and the job stays green.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `ci: report the chart values' breaking changes against the last release (V17)`.

### Task 23: the migration note and the decision pack (S)

**Files:**
- Modify:
  - `docs/upgrading.md` (`## v4 clean-up` becomes the consolidated note from v0.4x to the v4.0 candidate: every CL1 row removed, its replacement, its query, and a link to the reusable merge policies plan's "N+1" section for its shapes);
  - `docs/v4-readiness.md` (step 7 PR I: the step 8 rows filled, the open rulings updated, and the manual installs marked "to run on S8.2");
  - `docs/roadmap.md` (step 8 built);
  - `README.md`.

- [ ] **Step 1: write.** Run `hack/docs.sh`, `hack/names.sh` and `task check`. Expected: PASS.
- [ ] **Step 2: commit** `docs: the clean-up's migration note; the chart is a v4.0 candidate`. Open PR J. After it merges, cut S8.2: a v0.4x minor, not v4.

## After this step (not in these PRs)

- **Owner decisions:**
  - R1 to R10 are settled (above);
  - step 7's R8 and R9: the recorded GKE and kubeadm installs, on S8.2;
  - **the v4.0.0 go:** the tag, the release, the 4.0 chart version and arming CL7's gate. Nothing in this step does any of these.
- **ANR follow-ups** (each its own PR after the matching minor, each recorded with a gcp-0 live check in the session ledger, aws-0 after):
  1. **S8.1:** bump the pin. Record `maku_deprecated_in_use` per id on both clouds. Each id at 1 points at the earlier follow-up still owed.
  2. **S8.2:** bump the pin, which needs no values change if the gate held. The live check:
     - the umbrella reconciles;
     - `helm test` passes;
     - a docs-links task merges through `maku/merge-gate`;
     - the Slack App answers (on `maku_slack_app_*` with R5).
  3. **ANR's shrink checklist (D22) closed:** the spec's pointer in ANR lists each deleted directory against its follow-up.
- **Later, outside the v4 rollout:**
  - merging the factory and broker binaries (D20, out of scope);
  - automated EKS or AKS tests (D23, out of scope).

## Self-review

- **The spec's step 8:**
  - the old paths are deleted once cnref runs only the product path, which the gate makes a measured fact (R1, R2, CL3, PR A);
  - the `profile` switch is deleted (CL4, Task 14);
  - the chart is a v4.0 candidate (PR J), and cutting it waits for the owner's go after the manual installs (After this step).
- **Every earlier step's "Step 8" list** has a row in CL1 and a task:
  - step 1: the subcharts' `crds/` (Task 15);
  - step 2: `networkPolicies.enabled`, the sandbox keys, `queueName`, the XR kind (Tasks 4–6);
  - step 3: `runtoken`, the shared token, the aliases, octo-sts, readiness's policy-file path, the defaults (Tasks 7, 9, 10);
  - step 4: `meter.logsURL`'s docs, `model.mode`, the portable refusals (Tasks 8, 11);
  - step 5: the readers, `zitadel.go`, the old signatures (Tasks 12–13);
  - step 6: `networkPolicies.plain`, `profile`, the federation docs, the metric rename (Tasks 8, 14, 15, 21);
  - step 7: `preflight.enabled`, private code, `values-demo-gateway.yaml` (Tasks 11, 16, 17);
  - #521: the ledger bridge (Task 18).
- **The reusable merge policies plan's PR K** is not duplicated: its shapes are named only to be excluded (CL6), and PR D waits for it.
- **What stays, and why,** is listed (CL2), so a later reader does not "finish" this step by deleting an optional seam.
- **D25:**
  - the room database is untouched;
  - every removal is announced a minor ahead and refused loudly, never ignored;
  - no v4.
- **D13:** breaking value changes before v4.0.0 are each in the migration note, and the contract's gate is ready but not armed (CL7, R8).
- **Risks named with a stop rule:**
  - **A path cnref still uses:** its gauge stays at 1, and its PR waits (the gate).
  - **aws-0 lagging gcp-0:** both must read zero (R2).
  - **A flipped default changing cnref silently:** the cnref-like golden's diff is in each PR body, and cnref sets each value explicitly before the flip.
  - **The CRDs on the subcharts' removal:** `crds: Skip` and the CRD chart's ownership are checked before Task 15, and the nightly upgrade proves it.
  - **PR K late:** PR D waits, and the rest proceeds (CL6).
  - **The nightly** goes red until S8.2 (strict decoding).

---
