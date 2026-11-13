# Reusable merge policies: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a `MergePolicy` becomes one reusable class definition, and a `Repository` binds the policies it uses, each with its own mode and parameters. Every resolved `config.Class` stays what the gate reads today:
- `MergePolicy` is one class: its name is the class name (B1). Its spec is `config.Class` without `live`/`shadow`, plus `requires` (B7).
- `Repository.spec.merge` holds the repository settings, plus `fluxSource` (B2, Q14). `Factory.spec.mergeDefaults` holds the shared ones, and a Repository overrides them key by key (Q8).
- `Repository.spec.mergePolicies[]` holds the bindings: name, mode and parameters (B3, B4). One function, `config.Resolve(policy, binding, guardrails)`, serves central bindings and the stage-4 file alike. The guardrails cap every binding (B14, Q10).
- Each binding resolves, fails and keeps its last good version on its own (B9). The refusal reasons are `MissingRef`, `MissingParameter`, `ParameterTwice`, `OverPolicy`, `GuardrailMismatch`, `AmbiguousAdoption` (B6, Q11) and `Invalid`.
- Status sits where the key is (B10):
  - `Repository.status.classes[]`, with `BindingsValid`, `RepositoryFileValid`, `InSync`, `authority` and the resolved `merge`;
  - `MergePolicy.status.consumers[]`, with the columns `CONSUMERS CURRENT LIVE ACCEPTED AGE`.
- The decision record gains `repogen=` and `resolved=` (B12). Approval grants bind to the resolved class's hash (B13, Q9). The Evidence card splits shadow decisions by policy generation, and promotion still counts them all (Q6).
- `maku config migrate` converts today's objects and proves that no resolved class changes (B17). `maku config validate --base` prints the blast radius and the bindings that exceed the guardrails (B11, Q5, Q10).
- The stage-4 file takes the binding shape (B15). The settings pages follow the split (B16).
- **Two releases (B8).** Release N reads both shapes. Release N+1 refuses the old one: the per-repository `MergePolicy`, the file's `classes:` grammar and its `conditions.any` (Q13).

Nothing the gate decides changes: on aws-0 and gcp-0 the resolved classes before and after are equal, which ai-native-ref's CI proves on every migration PR.

**Architecture:**
- **`internal/factory/config`** (new files, pure):
  - `Binding`, `PolicySpec` and `PolicyContext`;
  - `Resolve`, with its closed reason set;
  - `ValidatePolicy`;
  - `ResolvedHash`;
  - `ApplyMergeDefaults`.
  
  `config.Class` gains `requiresApproval`. `oracle.workflow` and `monitor.flux.source` become optional in the type and stay required on a resolved class.
- **`api/factory/v1alpha1`** serves both shapes of `MergePolicy` in release N, told apart by `spec.repositoryRef` and held apart by CEL. `Repository` gains `merge`, `mergePolicies` and the new status. `Factory` gains `mergeDefaults`.
- **`internal/factory/resources.Assemble`** resolves the bindings before `config.Parse`, under a `PolicyContext` read from the merged catalogue. A binding that does not resolve never reaches Parse:
  - it is reported in `Result.Bindings`;
  - its last resolved class is put back from `Options.LastResolved`.
  
  `configsource` keeps that map in memory (D5; none after a restart, D14), and lists the kinds unstructured so an omitted key stays omitted.
- **`statuswriter`** computes `Repository.status.classes[]` and `MergePolicy.status.consumers[]` from the per-binding outcomes.
- **The gate itself is unchanged**: triage, the adopter and the monitor read `config.Repository.Classes`, which resolution fills. Only these move:
  - the decision record and `Task.status.configGenerations`;
  - the grant lookup and the central approval subset;
  - the catalogue's generations.
- **`internal/factory/mergepolicy`** parses the file's `mergePolicies:` through the same `config.Resolve`.
- **`resources.Split` and `resources.Impact`** back the CLI and the settings editor's preview.

**Tech Stack:**
- Go: controller-gen CRDs with CEL (`x-kubernetes-validations`), `listType=map`;
- controller-runtime (unstructured lists, status patches);
- envtest 1.36 for admission and the status writer;
- TypeScript and vitest for the settings pages;
- the bash and jq tally script;
- Python and PyYAML (ai-native-ref's `scripts/ci/factory_config.py`).

**Spec:** `docs/superpowers/specs/2026-11-12-reusable-merge-policies-design.md` (PR #472, branch `docs/mergepolicy-reusable-classes`):
- the model and the B4 field table;
- B1–B19, Validation and Keys;
- "The repository file (stage 4)" and "Migration of the four live repositories";
- the owner's decisions Q1–Q15.

It builds on factory resources (D3, D4, D5, D8, D10, D14, D16) and `docs/merge-policies.md` (MP2–MP17).

**Read against:** Maku `origin/main` `e43e9329`. v4 step 3 PR A (#496) is merged, PR E (#500) is open, and PRs B–D are not opened. ai-native-ref `origin/main` (Maku pin v0.45.0).

**Branch:** eleven PRs, each from `origin/main` once its dependencies merge. **None starts before v4 step 3's last PR merges** (Q15, PR16).

| PR | Branch | Tasks | Size | Depends on | Touches step-3 files |
|---|---|---|---|---|---|
| A, the one resolve path | `feat/mergepolicy-resolve` | 1–3 | M | step 3 merged | `config.go` (the `Merge` struct only), generated CRDs |
| B, the API in both shapes | `feat/mergepolicy-api` | 4–6 | L | A | generated CRDs |
| C, Assemble serves bindings | `feat/mergepolicy-assemble` | 7–10 | L | B | `config.go`, `config_test.go`, `demo_test.go` |
| D, status | `feat/mergepolicy-status` | 11–12 | M | C | none |
| E, record, generations, grants | `feat/mergepolicy-record` | 13–15 | M | C | `reconciler/merge.go` (other hunks) |
| F, the stage-4 file | `feat/mergepolicy-file` | 16–17 | M | C | none |
| G, the CLI | `feat/mergepolicy-cli` | 18–19 | L | C, F | none |
| H, evidence and the settings API | `feat/mergepolicy-settings-api` | 20–21 | M | D, E, G | `internal/app/factory.go` (other hunks) |
| I, the settings pages | `feat/mergepolicy-settings-ui` | 22–23 | L | H | none |
| J, docs; release N | `docs/mergepolicy-release-n` | 24 | S | A–I | `docs/factory.md`, `docs/operations.md`, `docs/roadmap.md` |
| K, the legacy shape removed; release N+1 | `feat/mergepolicy-legacy-removal` | 25–26 | L | J, then the ai-native-ref and cloud-native-ref follow-ups (below) | none |

D, E and F may run in parallel once C merges. Release N, a v0.4x minor, is cut after J. Release N+1, the next minor, is cut after K, before v4.0 (Q15).

## Global Constraints

- **No gate decision changes (B17, "Not in this spec").**
  - No gate row, refusal reason, metric label or class name changes.
  - A repository served from the legacy shape is decided exactly as today throughout release N.
  - Prove it two ways: the converter's equality check (Task 18), and ai-native-ref's resolved-classes snapshot test (ANR-1), kept unchanged by every migration PR.
- **Two releases (B8).** Release N reads both shapes, and nothing in N refuses a legacy object that main accepts today.
  - Legacy objects get a warning in `Result.Warnings`, logged once by `configsource`: `MergePolicy <name> is in the per-repository shape, removed in the next minor: maku config migrate`. `maku config validate` prints it too.
  - Release N+1 removes the legacy fields from the schema. No conversion webhook, no `v1alpha2`.
- **CRDs are generated, never hand-edited.**
  - `task crd:gen` writes `config/crd`, `charts/maku-factory/crds` and `charts/agent-factory-crds/files`.
  - `task crd:check` must pass in every PR that touches `api/` or `internal/factory/config/`.
  - A rebase conflict in a generated CRD is resolved by rerunning `task crd:gen` after the Go types merge, never by hand.
- **CEL rules** are written against the wiki's CEL notes: `has()` before reading an optional field, including on `oldSelf`. Every rule has an envtest case that admits and one that refuses (Task 6).
- **Bounded:** at most 32 bindings per Repository, 64 consumers per MergePolicy status, and the existing item and length bounds on every list. `TestFactoryResourceCRDsBoundEveryStringListAndMap` keeps holding.
- **Strict decoding.** `maku config validate` decodes each manifest strictly into its typed kind (`resources.Decode`). A field the old binary does not know is refused, so ai-native-ref bumps the pin before any manifest uses a new field (ANR-1).
- **Per-binding isolation (B9) is never a whole-repository refusal.** A test in each layer (Assemble, configsource, statuswriter, the file Watch) proves that one bad binding leaves the repository and its other classes serving.
- **Live impact nil until a manifest changes.** `mergeGuardrails` and `approvalMemory` are unset on aws-0 and gcp-0, and no repository is cut over (`repositoryAuthority` off). Say so in every PR body.
- **Maku house rules:**
  - the SPDX line on every Go file;
  - stdlib `testing`, table-driven, `-race`;
  - goldens only for rendered artifacts;
  - conventional commits in English, with no attribution lines or trailers;
  - golangci-lint v2 through `task lint`;
  - no error string ends in punctuation;
  - `task check` before the last commit of each PR.
- **This machine:**
  - wrap every test run in `systemd-run --user --scope -q -p MemoryMax=6G`;
  - never set `HOME=` or use `env -i`;
  - envtest runs with `KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)"`.

## Rulings made while planning

- **PR1 (B19 is overtaken).** #470's stages 1, 4, 2 and 3 are already merged (#473, #482, #483, #486), so B19's interleaving cannot be followed. What remains:
  - The catalogue amendment lands in release N (Task 14): `generations` gains `mergePolicies: {<class>: <policy generation>}`. `mergePolicy` stays in N, with the legacy object's generation, and 0 for a bound repository. N+1 removes it (Task 26). The MCP `classes_list` output schema follows.
  - #470's stage 5 touches intake only and may merge at any time.
  - Pins (#486) read the resolved class's `harnessAllowed`, which resolution keeps, so nothing re-checks them.
- **PR2 (one Go type, D16).** The policy's spec is `config.Class` inlined, plus `requires`:
  - **No type split.** `live` and `shadow` stay in the type and are refused on a class-shape policy by CEL. Splitting `Class` into an embedded definition would rewrite 89 `config.Class{…}` literals across 30 files.
  - **`requiresApproval` joins `config.Class`** as `[]ApprovalLeaf` (moved from `mergepolicy`, which keeps an alias). A resolved class then carries it, and the central gate reads it (PR12).
  - **`oracle.workflow` and `monitor.flux.source` become optional in the type** (`omitempty`, `+optional`). CEL refuses both on a class-shape policy: they come from the binding's `oracleWorkflow` and from `merge.fluxSource`. `validateClass` still requires both on a resolved class.
  - **`config.Merge` fields get `+optional`.** `Repository.spec.merge` and `Factory.spec.mergeDefaults` are then partial in the schema. Parse still requires the keys on each resolved repository, as today.
- **PR3 (`requires`).** `requires` may name the four fill-once fields (`allowedFiles`, `companionFiles`, `requiredLabel`, `oracleWorkflow`) and `holdoutChecks`. For `holdoutChecks`, "required" means the binding adds at least one check.
  - The spec's Validation table says "only fill-once fields", but its own examples (`requires: [holdoutChecks]` on `docs-links`, `[allowedFiles, holdoutChecks, oracleWorkflow]` on `dead-links`) need `holdoutChecks`.
  - Without it, `dead-links` alone fails `ValidateClass` ("an oracle needs holdoutChecks"), since every consumer supplies its own holdout.
  - **Owner ruling (2026-11-13): allowed.** `holdoutChecks` is additive *and* requirable; the spec's Validation row and B4 table are amended. A binding that only repeats the policy's own holdouts adds none and is `MissingParameter` (A-L2, PR A).
- **PR3b (review, owner ruling 2026-11-13).** Review classes keep R17's flexibility: a `review` MergePolicy may declare `checks` and `testFirst`, true included. Review still never merges on its own: it is bound `off` with no parameter. The spec is amended.
- **PR4 (Q13 reads "`author: any`").** The file grammar has no such value (`author` is `agents` or `adopt`; centrally `author.adopt`). Q13 cites B7, which is about the file's `conditions.any`. This plan reads Q13 as `conditions.any`:
  - **N:** a file using `any` still parses (legacy `classes:` only; no live file uses it). It gets a deprecation line in `RepositoryFileValid`'s message, in `maku policy check` and in the Watch's log.
  - **N+1:** it is refused together with the `classes:` grammar. `Resolved.Branches` and the variant loop of `DecideArmUnder` are deleted.
  
  **Owner to confirm.** If Q13 meant something else, only Task 26's refusal list changes.
- **PR5 (presence).** `configsource` lists `Factory`, `Repository` and `MergePolicy` as `unstructured.UnstructuredList`.
  - Today it lists typed objects and `ToUnstructured` writes every `config.Merge` field without `omitempty` as present. That breaks `resources.Object`'s own contract ("a field the author omitted stays omitted") and would make every merge key "set", defeating Q8's key-by-key override.
  - Task 7 proves the old behaviour wrong with a test before changing it.
- **PR6 (`mergeDefaults`, Q8).**
  - **Override unit:** the top-level keys of `merge`, each replaced whole. `breaker` is one key and `requiredChecks` one list: no deep merge, no list union.
  - **Where:** `config.Parse` applies the defaults at map level, before decoding, to every repository entry (file entries included). It is one place.
  - **Catalogue key:** `mergeDefaults` joins `resources.CatalogueKeys`, and ai-native-ref's `CATALOGUE` tuple with it.
  - **Status:** the resolved settings show in `Repository.status.merge`, and the keys taken from the Factory in `Repository.status.mergeDefaulted`.
  - **`fluxSource`** is a merge key, so it may be defaulted.
  - **Step 3 interaction:** step 3 PR D's rule (`merge.mergerLogin == factoryLogin` when the merger folds) must read the resolved merge. Task 9 adds a case.
- **PR7 (isolation inside Assemble, B9).**
  - **Before Parse:** Assemble resolves the bindings first, under a `config.PolicyContext{HarnessProfiles, MergeGuardrails}` decoded strictly from the merged catalogue map. Parse cannot do it, since it refuses a config without repositories. A decode failure leaves the Factory to Parse's culprit search, as today.
  - **Checks that read binding names.** Three repository checks would turn one dropped binding into a refused Repository. They read `config.Repository.Bound` (the binding names), not the resolved classes:
    - OD-8 (docs-links and revert required);
    - "schedule X: class is review or a declared class";
    - `probe: dead-links` needs an oracle class.
    
    OD-8 for bindings is also CEL.
  - **At run time** a dropped class is absent: a `class:` label naming it falls back to `review` (B9), and a schedule of it runs as `review`.
- **PR8 (`DefinedTwice`, B8).**
  - **Refusal:** a Repository generation that adds `merge` or `mergePolicies` is refused `DefinedTwice` while a legacy MergePolicy names it. The legacy MergePolicy is reported `DefinedTwice` too.
  - **Serving:** D5 keeps the previous Repository generation, with its legacy policy, serving until Flux prunes the legacy object. Flux applies, then prunes, in one reconcile.
  - **After a restart** inside that window, the repository is unserved until the prune.
- **PR9 (`AmbiguousAdoption`, B6, Q11).** It applies to two adopted bindings of one repository that share all three of:
  - the `diffShape`;
  - a login (compared case-insensitively);
  - a branch prefix, where one binding's prefix is a prefix of the other's.

  The rule:
  - the later binding in `mergePolicies[]` list order is refused, and its message names both;
  - list order, not name order, so renaming a shared policy changes nothing;
  - every mode counts, `off` included, so a later promotion cannot create the ambiguity.
  
  cloud-native-ref's `deps-patch` and `deps-minor` share logins and prefixes but differ in shape, so they pass.
- **PR10 (guardrails, B14, Q10).** Every binding, central or in the file, resolves under `mergeGuardrails`: protected paths and mandatory checks are added, an unset cap inherits the guardrail's, and a cap above it is `GuardrailMismatch`.
  - Legacy central classes keep today's path in N (no guardrails), because they disappear at N+1.
  - `maku config validate` prints one `guardrail:` line per binding over a guardrail and exits non-zero. With guardrails set, it also prints `guardrails: N bindings within`.
- **PR11 (the resolved hash, B13, Q9).**
  - **The hash:** `config.ResolvedHash(variants ...Class)`, the sha256 of the canonical JSON of the resolved variants, each with `Live` and `Shadow` false. A binding has one variant; a legacy file class with `any` has one per branch, and that case goes at N+1.
  - **Where it appears:** 64 hex digits for the grant lookup, 12 in the record's `resolved=`.
  - **No migration.** The store's `policy_hash` column and the wire key `policyHash` keep their names and carry the resolved hash: `approvalMemory` is off everywhere, and a lookup already suspends grants bound to another hash.
  - **Wording:** the broker's text says "under class definition <hash>" in place of "under policy <hash>". The approval-memory form prefills from `resolved=`.
- **PR12 (approval under central authority, B13).**
  - **Central gate:** `DecideArm` applies `withApproval` with the class's own `RequiresApproval`. A grant is read under the resolved hash, behind `approvalMemory`.
  - **Repository authority:** a binding-shape file's resolved class carries `requiresApproval` the same way.
  - **Live effect:** none of the four live repositories declares `requiresApproval` centrally, so nothing changes there.
  - **Kept:** `policyManagement.approvalMemory needs read` stays as it is. Its reason weakens, but the switch is off everywhere; revisit it when approval memory is turned on.
- **PR13 (the evidence split, Q6).** `repogen=` is printed only when the decided class comes from a binding.
  - **A record with `repogen=`:** its `policygen=` is the class policy's generation.
  - **A record without it:** decided before the binding; its `policygen` was the per-repository object's. It is bucketed `before`.
  - **What changes:** the card shows clean and changed counts per bucket. `Evaluate` and the minimum are unchanged: promotion counts every shadow decision of the window.
- **PR14 (status in release N).**
  - **Both shapes fill the new fields:** `Repository.status.classes[]`, `authority`, the file conditions and `merge` are computed for repositories served from either shape, so readers move once.
  - **Legacy policies keep their status:** a legacy `MergePolicy` keeps `classes`, `classCount` and `authority` until N+1.
  - **MergePolicy columns in N:** `Repo` (legacy only), `Consumers`, `Current`, `Live`, `Accepted`, `Age`. `Authority` and `Classes` move to `Repository`.
  - **`consumers[].repository`** is the Repository object's name, as `kubectl get repository` prints it. 64 consumers is above `MaxRepositories` (8), so no list is ever truncated.
- **PR15 (the policy page's route).** `/settings/merge-policies/policies/<name>` is matched before `/settings/merge-policies/<owner>/<name>`. A repository whose owner is literally `policies` is then reachable from the Evidence page's link only. Name this in `docs/web-ui.md`.
- **PR16 (order against v4 step 3).** The owner ruled that this is built right after step 3. Step 3's PRs B–E touch:
  - `internal/factory/config/config.go`: `Merge.PolicyBot`, `validateRepository`'s merge rules, `RunTokens`, `GitHub`;
  - `config_test.go`, `demo_test.go` and `hardening_test.go`;
  - `internal/app/factory.go`;
  - `internal/factory/fmetrics/fmetrics.go`;
  - `internal/factory/reconciler/merge.go` and `gatestatus.go`;
  - the three generated `mergepolicies.yaml` copies (#500 regenerates `Merge.PolicyBot`'s description);
  - `docs/factory.md`, `security.md`, `operations.md` and `roadmap.md`.

  This plan keeps its edits to those files small and in other hunks:
  - **`config.go`:** new code goes in new files (`binding.go`, `mergedefaults.go`, `resolvedhash.go`). `config.go` takes only struct fields, the Parse pre-pass call and `validateRepository`'s three `Bound` reads.
  - **`merge.go`:** three lines in `withRecord` and one grant read in `awaitingCI`'s diff branch, away from #500's `MERGED` and `autoMerging` hunks.
  - **`fmetrics`:** no new metric.
  
  A and B can be written while step 3 finishes, since they share only generated CRDs. They merge after it.
- **PR17 (where bindings live).**
  - **Bindings:** on `Repository` objects and in the stage-4 file only. A config file's `repositories[]` keeps `classes` and `merge`, the file-served path the demo uses (D8). `maku config migrate` turns it into class-shape manifests (Task 18).
  - **`Config.Policies`:** the accepted class-shape policies are carried on `config.Config` (`json:"-"`), set by Assemble, so the stage-4 Watch and the settings pages resolve against the same definitions.
- **PR18 (the live session).** Q15 says the four repositories migrate "in the next gcp-0 live session". The spec's Migration step 4 says aws-0. The owner's decision governs: gcp-0.
  - Both clouds apply `tooling/base/agent-factory/resources`, so each ai-native-ref PR migrates a repository on both clouds at once.
  - `validate-factory-config.sh` checks both overlays.
- **PR19 (the converter's split is deterministic, B17).**
  - **Meaning fields:** equal across consumers (compared on the decoded value, so `scripts: {checks: false}` equals `scripts: {}`), else refused with the differing fields.
  - **Fill-once fields:** always on the bindings, and named in `requires` when every consumer sets them.
  - **Additive lists:** on the policy when every consumer's list is the same set (in the first consumer's order), else each consumer's whole list on its binding. `holdoutChecks` is named in `requires` when every consumer's list is non-empty (PR3).
  - **Caps:** on the policy at the loosest consumer's value (0, no cap, is the loosest), and on a binding only below it.
  - **Other:**
    - `monitor.flux.source` becomes `merge.fluxSource`, and two sources in one repository are refused (Q14);
    - `merge` becomes `Repository.spec.merge`;
    - the mode goes on the binding.
  - **The four live repositories:** ai-native-ref's PRs commit the converter's output and edit only comments and `description`. The spec's YAML examples are hand-split illustrations; for `dead-links` the converter puts `protectedFiles` on the policy because both consumers agree.
- **PR20 (the legacy shape's last release).**
  - **In N+1** the API server prunes the legacy fields on read, so a forgotten legacy object becomes an empty class policy with `CONSUMERS 0`. It is harmless and visible.
  - **In N** every legacy object carries the warning (Global Constraints), and `docs/upgrading.md` lists `kubectl get mergepolicy -o jsonpath='{..repositoryRef}'` as the check to run before upgrading to N+1.

---

## PR A: the one resolve path

### Task 1: `config.Class` and `config.Merge` take the new fields (S)

**Files:**
- Modify:
  - `internal/factory/config/classes.go`: `Class.RequiresApproval []ApprovalLeaf` (`+kubebuilder:validation:MaxItems=16`); `ApprovalLeaf{ChangedFiles []string}` moved here; `Oracle.Workflow` and `FluxWatch.Source` gain `omitempty` and `+optional`; `validateClass` refuses an empty `requiresApproval[].changedFiles` and a pattern that does not compile, and still refuses an empty workflow or source on a declared oracle or Flux watch;
  - `internal/factory/config/config.go`: `Merge` only. `FluxSource string \`json:"fluxSource,omitempty"\`` (namespace/name, `sourceRE`), and `+optional` on every field;
  - `internal/factory/mergepolicy/policy.go`: `type ApprovalLeaf = config.ApprovalLeaf`;
  - `internal/factory/config/classes_test.go`, `internal/factory/config/config_test.go`;
  - the generated CRDs (`task crd:gen`).

- [ ] **Step 1: failing tests.**
  - `TestClassRequiresApproval`:
    - `requiresApproval: [{changedFiles: ['^infra/']}]` is accepted;
    - `[{changedFiles: []}]` is refused with `class x: a requiresApproval's changedFiles is empty: it asks about nothing`;
    - `[{changedFiles: ['(']}]` is refused.
  - `TestOracleWorkflowStillRequiredOnAClass`: a class with `oracle: {probe: dead-links, maxFindings: 10}` and no workflow is refused, with the same message as today's bad workflow.
  - `TestMergeFluxSource`:
    - `merge.fluxSource: flux-system/flux-system` parses;
    - `merge.fluxSource: nope` is refused, `merge.fluxSource "nope" is not namespace/name`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/factory/config/ -run 'RequiresApproval|OracleWorkflow|FluxSource'`. Expected: FAIL.
- [ ] **Step 2:** implement, then `task crd:gen`. Expected: every config file and manifest in the tree still parses. `go test ./internal/factory/... ./api/...` passes unchanged: the fields are additive and validation is unchanged for every existing class.
- [ ] **Step 3:** `task crd:check`. Expected: PASS.
- [ ] **Step 4: commit** `feat(config): a class carries requiresApproval; merge.fluxSource; oracle.workflow and monitor.flux.source optional in the type`.

### Task 2: `Binding`, `PolicySpec` and `Resolve` (M)

**Files:**
- Create:
  - `internal/factory/config/binding.go`:
    - `type Binding struct{ Name, Mode string; AllowedFiles, CompanionFiles []string; RequiredLabel, OracleWorkflow string; HoldoutChecks, ProtectedFiles []string; RequiresApproval []ApprovalLeaf; MaxPatchFiles, MaxModifiedLines, MaxPerDay int }`, with the same item and length bounds as `Class`;
    - `type PolicySpec struct{ Class \`json:",inline"\`; Requires []string }`;
    - `type PolicyContext struct{ HarnessProfiles map[string]HarnessProfile; Guardrails MergeGuardrails }`;
    - the reasons `ReasonMissingParameter`, `ReasonParameterTwice`, `ReasonOverPolicy`, `ReasonGuardrailMismatch`, `ReasonInvalid`;
    - `type BindingRefusal struct{ Reason, Message string }`;
    - `func Resolve(name string, p PolicySpec, b Binding, ctx PolicyContext, m Merge) (Class, *BindingRefusal)`;
    - `func ValidatePolicy(name string, p PolicySpec, ctx PolicyContext) []string`;
    - `ApplyGuardrails`, moved from `mergepolicy.guard`;
  - `internal/factory/config/binding_test.go`.
- Modify:
  - `internal/factory/config/evaluation.go`: `VariantProblemsIn(profiles, class, cl)`, which `(*Config).VariantProblems` delegates to;
  - `internal/factory/mergepolicy/resolve.go`: `guard` calls `config.ApplyGuardrails`, behaviour unchanged.

- [ ] **Step 1: failing table test `TestResolve`**, one row per B4 row:
  - **Mode:**
    - `live` gives `Live`;
    - `shadow` gives `Shadow`;
    - `off` gives neither (Q7);
    - binding `review` with mode `shadow` gives `Invalid`, `review takes only mode off`.
  - **Meaning:** the resolved description, adopt, harness, diffShape, modifyOnly, oracle probe and maxFindings, and monitor are the policy's.
  - **Fill-once:**
    - the binding's `allowedFiles` with none on the policy gives them;
    - set on both gives `ParameterTwice: allowedFiles is set by MergePolicy dead-links and by the binding`;
    - `requires: [allowedFiles]` with the binding unset gives `MissingParameter: MergePolicy dead-links requires allowedFiles`;
    - `oracleWorkflow` fills `Oracle.Workflow`;
    - `oracleWorkflow` on a policy without an oracle gives `Invalid`.
  - **Additive:**
    - `holdoutChecks`, `protectedFiles` and `requiresApproval` are the policy's followed by the binding's new entries, as written;
    - `requires: [holdoutChecks]` with no binding entry gives `MissingParameter` (PR3).
  - **Caps:**
    - binding 5 under policy 10 gives 5;
    - binding 12 over policy 10 gives `OverPolicy: maxModifiedLines 12 is over MergePolicy dead-links's 10`;
    - policy 0 and binding 5 give 5;
    - both 0 give 0.
  - **Repository fact:** a policy with `monitor.flux.within` and `merge.fluxSource` unset gives `MissingParameter: monitor.flux needs Repository.spec.merge.fluxSource`; set, `Monitor.Flux.Source` is it.
  - **Guardrails (B14):**
    - guardrail `protectedFiles` and `mandatoryChecks` are appended;
    - an unset cap inherits the guardrail's;
    - a binding cap over the guardrail gives `GuardrailMismatch`.
  - **Invalid:**
    - `semver-minor` bound `live` gives `Invalid` with `validateClass`'s message;
    - `testFirst` with `m.ContractCheck == ""` gives `Invalid: class x: testFirst needs merge.contractCheck`;
    - a `harness` not in the profiles gives `Invalid` (`VariantProblemsIn`).

  Then `TestValidatePolicy`:
  - `dead-links` with `requires: [allowedFiles, holdoutChecks, oracleWorkflow]` is valid alone (each required field gets a placeholder: `^placeholder$`, `placeholder`, `placeholder.yaml`, and `ns/name` for a Flux source);
  - the same policy without `holdoutChecks` in `requires` is refused (`an oracle needs holdoutChecks`);
  - `review` with `allowedFiles` is refused.
  
  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/factory/config/ -run 'Resolve|ValidatePolicy'`. Expected: FAIL (`undefined: Resolve`).
- [ ] **Step 2:** implement. `Resolve` copies before it appends, so the policy's slices are never aliased: assert this with a second `Resolve` on the same policy. Then rerun the command, and `go test ./internal/factory/mergepolicy/`. Expected: PASS, the mergepolicy guard tests unchanged.
- [ ] **Step 3: commit** `feat(config): one resolve path for a policy and a binding (B4, B9, B14)`.

### Task 3: the resolved hash and the merge defaults, as pure functions (S)

**Files:**
- Create:
  - `internal/factory/config/resolvedhash.go` (`ResolvedHash(variants ...Class) string`, `ShortHash(h string) string`);
  - `internal/factory/config/mergedefaults.go` (`ApplyMergeDefaults(defaults, merge map[string]any) (map[string]any, []string)`);
  - their `_test.go` files.

- [ ] **Step 1: failing tests.**
  - `TestResolvedHash`:
    - the same class in `live` and in `shadow` hashes the same;
    - a changed `holdoutChecks` order hashes differently (as written, as `Structural` compares);
    - the hash is 64 lowercase hex digits, and `ShortHash` gives its first 12.
  - `TestApplyMergeDefaults` (PR6):
    - a key absent from `merge` is taken whole and named in the returned list;
    - a present key wins, `breaker` included, even when partial (`{window: 5}` replaces the whole breaker);
    - a nil `defaults` returns `merge` unchanged and no keys;
    - `merge` is never mutated.
  
  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/factory/config/ -run 'ResolvedHash|MergeDefaults'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): the resolved class hash and key-by-key merge defaults`, then open PR A. Its body lists PR2, PR3 and PR10, says the PR is unreachable from any manifest, and names the generated CRD copies it shares with step 3 (PR16).

## PR B: the API in both shapes

### Task 4: `MergePolicy`, `Repository` and `Factory` specs (M)

**Files:**
- Modify:
  - `api/factory/v1alpha1/factoryconfig_types.go`:
    - **`MergePolicySpec`:** `RepositoryRef` becomes `+optional` (and `omitempty`); `Classes` and `Merge` (`*config.Merge`, `omitempty`) stay; `config.PolicySpec` inlined.
    - **`RepositorySpec`:** gains `Merge *config.Merge \`json:"merge,omitempty"\`` and `MergePolicies []config.Binding` (`+listType=map`, `+listMapKey=name`, `MaxItems=32`).
    - **`FactorySpec`:** gains `MergeDefaults *config.Merge \`json:"mergeDefaults,omitempty"\``.
    - **New reasons:** `ReasonMissingParameter`, `ReasonParameterTwice`, `ReasonOverPolicy`, `ReasonGuardrailMismatch` and `ReasonAmbiguousAdoption`, aliasing config's, plus `ConditionBindingsValid`.
  - `internal/factory/config/binding.go`:
    - CEL on `Binding`: the name pattern;
    - `mode` is `+kubebuilder:validation:Enum=live;shadow;off` and required;
    - `requires` items are `+kubebuilder:validation:items:Enum=allowedFiles;companionFiles;requiredLabel;oracleWorkflow;holdoutChecks`, `+listType=set`, `MaxItems=5`.

  The CEL rules (`+kubebuilder:validation:XValidation`):
  - **On `MergePolicy`** (root):
    - `has(self.spec.repositoryRef) || self.metadata.name.matches('^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$')`, "a MergePolicy's name is its class name (B1)";
    - `has(self.spec.repositoryRef) || self.metadata.name != 'review' || (<no field but description, harness, harnessAllowed, escalation>)`.
  - **On `MergePolicySpec`:**
    - `!has(self.repositoryRef) || (<no class field>)`, "the per-repository fields (repositoryRef, classes, merge) and the class fields never together (B8)";
    - `has(self.repositoryRef) || (!has(self.classes) && !has(self.merge))`;
    - `has(self.repositoryRef) || (!has(self.live) && !has(self.shadow))`, "mode is the binding's: live and shadow are refused on a MergePolicy (B4)";
    - `!has(self.oracle) || !has(self.oracle.workflow)`, "oracle.workflow is the repository's: set oracleWorkflow on the binding";
    - `!has(self.monitor) || !has(self.monitor.flux) || !has(self.monitor.flux.source)`, "the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)";
    - `has(self.repositoryRef) == has(oldSelf.repositoryRef)`, "a MergePolicy keeps its shape: delete it and create a policy". The existing `repositoryRef` immutability rule stays.
  - **On `RepositorySpec`:**
    - `!has(self.mergePolicies) || !self.mergePolicies.exists(b, b.name == 'review' && (b.mode != 'off' || <any parameter>))`, "review is bound with mode off and no parameter";
    - `!has(self.mergePolicies) || size(self.mergePolicies) == 0 || (self.mergePolicies.exists(b, b.name == 'docs-links') && self.mergePolicies.exists(b, b.name == 'revert'))`, "bind docs-links and revert when any policy is bound (OD-8)";
    - `!has(self.classAuthority) || self.classAuthority != 'repository' || !has(self.mergePolicies)`, "classAuthority repository: the repository's file holds the bindings (MP15)".

- [ ] **Step 1: failing static tests** in `api/factory/v1alpha1/factoryconfig_crd_test.go`:
  - `TestFactoryResourceCRDsCarryTheDesignRules` gains every new message;
  - `TestMergePolicyCELNamesEveryClassField`: reflects over `config.PolicySpec`'s and `config.Class`'s JSON names and asserts each one appears in the "never together" and `review` rules. This guards the explicit `has()` lists against a field added later.
  
  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./api/factory/v1alpha1/ -run 'DesignRules|CELNames|Bound'`. Expected: FAIL.
- [ ] **Step 2:** implement, then `task crd:gen`. Rerun. Expected: PASS. `TestFactoryResourceCRDsBoundEveryStringListAndMap` holds.
- [ ] **Step 3: commit** `feat(api): MergePolicy as one class definition beside the per-repository shape; Repository merge and mergePolicies; Factory mergeDefaults (B1-B4, B8, Q8)`.

### Task 5: the status types and printer columns (M)

**Files:**
- Modify:
  - `api/factory/v1alpha1/factoryconfig_types.go`:
    - **`BindingStatus{Name; Mode; PolicyGeneration int64; Accepted bool; Reason; Message; LastDecision; LastDecisionAt}`.** `Accepted` is always serialized. The `Message` is at most 1024 characters.
    - **`RepositoryStatus` gains:**
      - `Authority`;
      - `ClassCount` and `LiveCount` (always serialized);
      - `Classes []BindingStatus` (`listType=map`, key `name`, `MaxItems=32`);
      - `Merge *config.Merge`;
      - `MergeDefaulted []string`.
    - **`ConsumerStatus{Repository; Mode; ServedGeneration int64; Accepted bool; Reason}`.**
    - **`MergePolicyStatus` gains:** `ConsumerCount` and `CurrentCount` (always serialized), and `Consumers []ConsumerStatus` (`listType=map`, key `repository`, `MaxItems=64`). The legacy fields stay until N+1.
    - **`Repository` columns:** `Repo, Factory, Classes, Live, Accepted, Connected, Age`.
    - **`MergePolicy` columns:** `Repo` (`.spec.repositoryRef`), `Consumers`, `Current`, `Live`, `Accepted`, `Age`.
  - `api/factory/v1alpha1/printcolumns_test.go`, `api/factory/v1alpha1/factoryconfig_crd_test.go`.

- [ ] **Step 1: failing tests.**
  - **`TestPrinterColumnFieldsAreAlwaysPresent` gains rows:**
    - `RepositoryStatus{}` serializes `"classCount":0,"liveCount":0`;
    - `MergePolicyStatus{}` serializes `"consumerCount":0,"currentCount":0,"liveCount":0`;
    - `BindingStatus{}` serializes `"accepted":false`.
  - **The design-rules test:**
    - the repositories CRD has `name: Classes` and `name: Live`;
    - the mergepolicies CRD has `name: Consumers` and `name: Current`, and no longer `name: Authority` or `name: Classes`.
  
  Run the Task 4 command with `-run 'PrinterColumn|DesignRules'`. Expected: FAIL.
- [ ] **Step 2:** implement, then `task crd:gen`. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(api): Repository.status.classes and MergePolicy.status.consumers, with their columns (B10)`.

### Task 6: admission, proved on envtest; the CRD chart (M)

**Files:**
- Create: `api/factory/v1alpha1/mergepolicy_crd_test.go` (envtest, on `overlap_crd_test.go`'s pattern: install the three CRDs from `config/crd`, then create objects).
- Modify: `charts/agent-factory-crds/tests/render.sh` (assert the mergepolicies CRD carries the `Consumers` column and the B8 rule message), `charts/agent-factory-crds/README.md` (B8's two-release note).

- [ ] **Step 1: failing envtest `TestMergePolicyAdmission`** and `TestRepositoryAdmission`.
  - **Admitted:**
    - each of ai-native-ref's four legacy MergePolicies, verbatim from `internal/factory/resources/testdata/anr-objects.yaml` (created in Task 10; use an inline copy until then);
    - the spec's `dead-links` and `docs-links` (with `requires` per PR3);
    - the spec's `ai-native-ref` Repository;
    - a Factory with `mergeDefaults: {autoMergesPerDay: 2}`.
  - **Refused, each with its message:**
    - a policy named `Docs`;
    - legacy plus class fields (`repositoryRef` with `maxPerDay`);
    - `live: true` on a policy;
    - `oracle.workflow` on a policy;
    - `monitor.flux.source` on a policy;
    - `requires: [maxPerDay]`;
    - `review` with `allowedFiles`;
    - an update that adds `repositoryRef` to a class policy;
    - two bindings named `docs` (the API server's duplicate-key error);
    - `mode: on`;
    - a binding without `mode`;
    - `review` with mode `shadow`;
    - bindings without `revert`;
    - `classAuthority: repository` with `mergePolicies`;
    - 33 bindings.
  
  Run `KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)" systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./api/factory/v1alpha1/ -run 'Admission'`. Expected: FAIL.
- [ ] **Step 2:** fix any rule the envtest proves wrong in Task 4. A CEL cost estimate over the budget is fixed by tightening bounds, never by dropping a rule. Then run the Step 1 command, `task crd:check` and `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `test(api): every MergePolicy and Repository rule admits and refuses on envtest`, then open PR B. Its body states that no factory reads the new fields yet: a class-shape MergePolicy applied now would be ignored by Assemble (Task 8 serves it).

## PR C: Assemble serves bindings

### Task 7: list the kinds unstructured (S)

**Files:**
- Modify: `internal/factory/configsource/source.go` (`list` reads `unstructured.UnstructuredList` per kind, with the GVK from `v1alpha1.GroupVersion`; `Kinds()` unchanged for the cache), `internal/factory/configsource/source_test.go`, `internal/factory/configsource/source_envtest_test.go`.

- [ ] **Step 1: failing envtest `TestListKeepsOmittedKeysOmitted`.** It creates a legacy MergePolicy whose `merge` omits `fixRuns` and `checkReruns`, and a Repository whose `merge` sets only `autoMergesPerDay`. The listed `resources.Object.Spec` holds:
  - no `fixRuns` key;
  - for the Repository, exactly `{merge: {autoMergesPerDay: 2}, …}`.
  
  It fails on main: `fixRuns: 0` is present (PR5). Run `KUBEBUILDER_ASSETS=… systemd-run … go test -race -count=1 ./internal/factory/configsource/ -run OmittedKeys`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, plus the package's other tests. Expected: PASS, with every existing outcome unchanged: the factory's own defaults applied to a missing `fixRuns` are Parse's, as for the file.
- [ ] **Step 3: commit** `fix(configsource): list the factory kinds as stored, so an omitted key stays omitted`.

### Task 8: Assemble resolves bindings (L)

**Files:**
- Create: `internal/factory/resources/bindings.go` (`resolveBindings`, `ambiguous`, `policyContext`), `internal/factory/resources/bindings_test.go`.
- Modify: `internal/factory/resources/assemble.go`:
  - **New API:**
    - `type Options struct{ LastResolved map[BindingKey]config.Class }`;
    - `func Assemble(file []byte, objs []Object, opts ...Options) (Result, error)`;
    - `Result.Bindings []BindingOutcome{Repository, Name string; PolicyGeneration int64; Accepted bool; Reason, Message string; Served bool}`;
    - `BindingKey{Repository, Name}`.
  - **`structural`:**
    - a MergePolicy with `repositoryRef` is legacy and keeps today's rules;
    - one without is a class policy, validated alone with `config.ValidatePolicy` (refused `Invalid` per object, so D5's per-object last good keeps its consumers on the previous generation);
    - a Repository with `merge` or `mergePolicies` is a candidate alone;
    - PR8's `DefinedTwice`.
  - **`merge`:** a candidate Repository's entry gets `classes` (resolved), `merge` (spec), `bound` (names) and `bindings` (raw).
  - **Warnings:** the legacy warning.
- Modify: `internal/factory/resources/assemble_test.go`.

- [ ] **Step 1: failing tests** (`bindings_test.go`, table-driven over manifests in YAML strings):
  - **Bindings resolve:** the spec's two policies and a Repository serve `config.Repository.Classes["dead-links"]` equal to cloud-native-ref's legacy class (written out).
  - **Isolation (B9):**
    - `dead-links` at generation 4 adds `requiredLabel` to `requires`;
    - ai-native-ref's binding supplies none: the outcome is `MissingParameter`, and the class is absent from ai-native-ref's served classes;
    - with `Options.LastResolved[{ai-native-ref, dead-links}]` set, the outcome is `Accepted: false, Served: true` and the class is the last resolved one;
    - the Repository and its other classes are accepted;
    - cloud-native-ref's binding supplies the label and serves generation 4.
  - **`MissingRef`:** a binding to an absent policy. The same for a policy refused `Invalid` with no last good.
  - **`AmbiguousAdoption` (PR9):**
    - two adopted policies, both `semver-patch`, logins `renovate[bot]` and `Renovate[bot]`, prefixes `^renovate/` and `^renovate/deps-`: the second in list order is refused, naming both;
    - with shapes `semver-patch` and `semver-minor`, both are accepted;
    - swapping the list order refuses the other one.
  - **`DefinedTwice` (PR8):**
    - a Repository with bindings and a legacy MergePolicy naming it: both are refused `DefinedTwice`;
    - with the Repository's last good put back (no bindings), the legacy pair serves.
  - **Guardrails (B14):** `mergeGuardrails.maxPerDay: 1` and a binding `maxPerDay: 2` give `GuardrailMismatch`; the binding without its own cap inherits 1.
  - **Legacy untouched:** `testdata/anr.yaml` plus the four legacy policies assemble to the same `config.Config` as on main (a deep-equal against a frozen JSON in `testdata/anr-legacy-config.json`, written once from main). The four legacy warnings are in `Result.Warnings`.
  - **A bad policy never refuses a Repository:**
    - a class policy refused `Invalid` leaves every consumer on its last good generation (serving `PolicyGeneration` 3);
    - with no last good, it leaves each consumer's binding `MissingRef` and the Repository accepted.
  
  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/factory/resources/`. Expected: FAIL.
- [ ] **Step 2:** implement. `policyContext` decodes `harnessProfiles` and `mergeGuardrails` from the merged map strictly. When that fails, every binding stays unresolved this pass and Parse's culprit search refuses the Factory, as today. Then rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(resources): Assemble resolves each binding on its own, keeps its last good, and refuses AmbiguousAdoption and DefinedTwice (B6, B8, B9)`.

### Task 9: the config reads bound names, merge defaults and per-class generations (M)

**Files:**
- Create: `internal/factory/config/bound_test.go`.
- Modify:
  - **`internal/factory/config/config.go`:**
    - `Repository` gains `Bound []string \`json:"bound,omitempty"\`` and `Bindings []Binding \`json:"bindings,omitempty"\``. Parse refuses both in a config file: Assemble alone writes them;
    - `Config` gains `MergeDefaults *Merge \`json:"mergeDefaults,omitempty"\`` and `Policies map[string]ServedPolicy \`json:"-"\``, where `ServedPolicy{Spec PolicySpec; Generation int64}`;
    - `Parse` calls `ApplyMergeDefaults` on each `repositories[].merge` before decoding (PR6), and records `MergeDefaulted` per repository (`json:"-"`);
    - `validateRepository` reads `Bound` for OD-8, a schedule's class and the `dead-links` probe whenever `Bound` is non-empty (PR7).
  - **`internal/factory/config/live.go`:**
    - `RepoGenerations.Classes map[string]ClassGeneration{Policy int64; PolicyName string}`;
    - `func (c *Config) ClassGenerations(repository, class string) (policy, repo int64, bound bool)`. For a legacy-served repository it returns the legacy object's generation and `bound` false.
  - **`internal/factory/resources/assemble.go`:** `CatalogueKeys` gains `mergeDefaults`; the Factory's `mergeDefaults` maps to the top-level key.
  - `internal/factory/config/config_test.go`.

- [ ] **Step 1: failing tests.**
  - `TestBoundNamesHoldTheRepositoryRules`: a repository with `Bound: [docs-links, revert, dead-links]` and `Classes` lacking `dead-links` (dropped) parses. A schedule of class `dead-links` with `probe: dead-links` parses too.
  - `TestMergeDefaultsApplyKeyByKey`: `mergeDefaults: {policyBotLogin: x[bot], breaker: {window: 10, maxReverts: 1}}`, and an entry with `breaker: {window: 5, maxReverts: 2}` and no `policyBotLogin`, give the entry's breaker and the default login. `MergeDefaulted` is `[policyBotLogin]`.
  - `TestMergerFoldReadsResolvedMerge`: once step 3 PR D's rule exists, a `mergerLogin` set only in `mergeDefaults` satisfies it.
  - `TestConfigFileRefusesBound`: `bound` and `bindings` in a config file are refused (`set by the factory from Repository objects`).
  - `TestClassGenerations`: a bound class gives `(policy gen, repo gen, true)`; a legacy-served one gives `(object gen, repo gen, false)`.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/config/ -run 'Bound|MergeDefaults|MergerFold|ClassGenerations'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then the whole `./internal/factory/...`. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): bound names keep a dropped class's repository serving; merge defaults key by key; per-class generations (B9, B12, Q8)`.

### Task 10: configsource keeps each binding's last good (M)

**Files:**
- Create: `internal/factory/resources/testdata/anr-objects.yaml` (ai-native-ref's `tooling/base/agent-factory/resources/*.yaml` on its `origin/main`, `${cluster_name}` replaced by `aws-0`, with its source commit in the header).
- Modify:
  - **`internal/factory/configsource/source.go`:**
    - `Source.lastResolved map[resources.BindingKey]config.Class`, passed as `Options`;
    - `Outcome` gains `Bindings []resources.BindingOutcome` (on Repository outcomes) and `Consumers` (on class-policy outcomes);
    - `generations()` fills `Repos[*].Classes`, and sets `cfg.Policies` from the accepted class policies;
    - each refused binding generation is logged once, as `logRefusals` does for objects.
  - `internal/factory/configsource/source_test.go`, `internal/factory/configsource/source_envtest_test.go`, `internal/factory/reconciler/factoryresources_anr_envtest_test.go`.

- [ ] **Step 1: failing tests.**
  - `TestSyncKeepsABindingsLastGood`:
    - Sync 1 serves `dead-links` at generation 3;
    - generation 4 fails ai-native-ref's binding, so Sync 2 keeps generation 3 for it (outcome `MissingParameter`, served 3) and serves 4 to cloud-native-ref;
    - a new Source (a restart) serves none for ai-native-ref (D14).
  - `TestSyncGenerationsPerClass`: `cfg.Generations.Repos["smana/ai-native-ref"].Classes["dead-links"] == {3, "dead-links"}`.
  - **The ANR envtest:** every object of `anr-objects.yaml` is accepted on envtest, and the factory serves four repositories.
  
  Run `KUBEBUILDER_ASSETS=… systemd-run … go test -race -count=1 ./internal/factory/configsource/ ./internal/factory/reconciler/ -run 'LastGood|GenerationsPerClass|ANR'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(configsource): each binding keeps its last resolved class while the factory runs (D5, D14)`, then open PR C. Its body lists PR5–PR10 and PR17. It names the `config.go` hunks shared with step 3 (PR16): the `Repository` struct, the Parse pre-pass and three lines of `validateRepository`.

## PR D: status

### Task 11: the desired status of both shapes (M)

**Files:**
- Modify:
  - **`internal/factory/statuswriter/desired.go`:**
    - `repository()`:
      - `Classes` from `Outcome.Bindings` for a bound repository, and from the served classes for a legacy one;
      - `Authority`;
      - `ClassCount` and `LiveCount`;
      - `RepositoryFileValid` and `InSync` (moved from the legacy policy, computed by the same `fileConditions`);
      - `BindingsValid` (False while any binding is refused, the message `refused: dead-links (MissingParameter), …`);
      - `Merge` (the resolved repository's) and `MergeDefaulted`;
      - the last decision per class from `Inputs.Decisions`.
    - `mergePolicy()`:
      - a legacy policy keeps today's status;
      - a class policy gets `Consumers` from its outcome's consumers;
      - `ConsumerCount`;
      - `CurrentCount` (accepted consumers whose `ServedGeneration` equals the object's generation);
      - `LiveCount` (consumers with mode live);
      - `Accepted`, with the message `serving generation 3; generation 4 refused` when refused with a last good.
  - `internal/factory/statuswriter/desired_test.go`.

- [ ] **Step 1: failing table test `TestDesiredBindings`**, the spec's worked case ("A shared policy invalid for one repository only"):
  - ai-native-ref's `classes[dead-links]` is `accepted: false, reason: MissingParameter, policyGeneration: 3`, and `BindingsValid=False` names it;
  - cloud-native-ref's is `accepted: true, policyGeneration: 4`;
  - `mergepolicy/dead-links` has `consumerCount: 2, currentCount: 1, liveCount: 0`, and its ai-native-ref consumer carries the reason;
  - a legacy-served repository's `classes[]` lists its served classes with `policyGeneration` the legacy object's;
  - a policy refused `Invalid` with a last good is `Accepted=False` with `serving generation 3; generation 4 refused`, its consumers on 3;
  - `Repository.status.merge.autoMergesPerDay` is the Factory default when the Repository omits it, and `mergeDefaulted` names it.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/statuswriter/ -run DesiredBindings`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, plus the package's existing tests, which must pass unchanged for legacy objects. Expected: PASS.
- [ ] **Step 3: commit** `feat(statuswriter): per-binding status on the Repository, consumers on the MergePolicy (B10, Q8)`.

### Task 12: written on envtest; what `kubectl get` shows (S)

**Files:**
- Modify: `internal/factory/statuswriter/writer_envtest_test.go`, `internal/factory/statuswriter/writer.go` (only if a new field needs its own patch handling).

- [ ] **Step 1: failing envtest `TestWriterBindings`**, with the same objects as Task 11 on the API server, and the printer cells read through a `metav1.Table` request (`Accept: application/json;as=Table;g=meta.k8s.io;v=v1`):
  - `mergepolicy/dead-links` shows `2 1 0 True`;
  - `repository/cloud-native-ref` shows `Classes 11`.
  
  Run `KUBEBUILDER_ASSETS=… systemd-run … go test -race -count=1 ./internal/factory/statuswriter/ -run WriterBindings`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `test(statuswriter): the binding status and its columns, as kubectl reads them`, then open PR D with PR14 in the body.

## PR E: record, generations, grants

### Task 13: the decision record's `repogen=` and `resolved=` (S)

**Files:**
- Modify: `internal/factory/decision/decision.go` (`Rows.RepoGeneration int64`, `Rows.Resolved string`; printed after `policygen=`, before `reviewer=`, each only when set), `internal/factory/decision/decision_test.go`, `scripts/testdata/tally/prs-decisions.json` (one more record with `policygen=4 repogen=7 resolved=0123456789ab`), `scripts/shadow-merge-tally_test.sh`.

- [ ] **Step 1: failing tests.**
  - `TestMarkerBindingFields`:
    - the marker ends `… policygen=4 repogen=7 resolved=0123456789ab reviewer=glm-5.3 -->`;
    - without them, today's exact marker.
  - **The tally script:** the new record counts like its neighbours.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/decision/ ./internal/factory/evidence/ && bash scripts/shadow-merge-tally_test.sh`. Expected: FAIL on the Go test only.
- [ ] **Step 2:** implement. Rerun. Expected: PASS: the evidence regex and the script's jq read `.*reviewer=` and ignore the new tokens.
- [ ] **Step 3: commit** `feat(decision): the record names the binding's Repository generation and the resolved class's hash (B12, B13)`.

### Task 14: the task's generations, the recorder and the catalogue (S)

**Files:**
- Modify:
  - `internal/factory/reconciler/merge.go`:
    - `withRecord`: `pol, repo, bound := cfg.ClassGenerations(…)`;
    - `Rows.Generation = pol`; `RepoGeneration = repo` only when `bound` (PR13);
    - `Rows.Resolved = config.ShortHash(config.ResolvedHash(cl))` for a declared class;
  - `internal/factory/reconciler/reconciler.go`: `recordConfig` reads `ClassGenerations` with `t.Spec.PredictedClass`;
  - `internal/factory/catalogue/catalogue.go`: `Generations.MergePolicies map[string]int64 \`json:"mergePolicies,omitempty"\``; `MergePolicy` is 0 for a bound repository (PR1);
  - `internal/humanapi/classeslist.go`: the output schema gains `mergePolicies`;
  - `internal/factory/reconciler/merge_test.go`, `internal/factory/reconciler/reconciler_test.go`, `internal/factory/catalogue/catalogue_test.go`, `internal/humanapi/classeslist_test.go`.

- [ ] **Step 1: failing tests.**
  - **A bound repository:**
    - a shadow decision of `dead-links` records `policygen=4 repogen=7 resolved=<12 hex>`;
    - `Task.status.configGenerations` is `{factory: f, repository: 7, mergePolicy: 4}`;
    - the recorder's `Last.Generation` is 4.
  - **A legacy repository:** today's record plus `resolved=`, and no `repogen=`.
  - **The catalogue:** gives `generations.mergePolicies["dead-links"] == 4` and `mergePolicy == 0`.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/reconciler/ ./internal/factory/catalogue/ ./internal/humanapi/ -run 'Record|ConfigGenerations|Catalogue|ClassesList'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): a task and a decision name their class's policy generation; the catalogue's generations per class (B12, B19)`.

### Task 15: grants bind to the resolved class; approval under central authority (M)

**Files:**
- Modify:
  - **`internal/factory/reconciler/arm.go`:** `DecideArm` ends with `withApproval(d, in, approvalPaths(cl))`, where `approvalPaths` flattens `RequiresApproval[].ChangedFiles`.
  - **`internal/factory/reconciler/merge.go`:**
    - in `awaitingCI`'s diff branch, under central authority, `in.Grants = r.grantRead(ctx, cfg, t, config.ResolvedHash(cl))` when `grantsOn` and the class is live with an approval subset;
    - under repository authority, the snapshot class's resolved hash in place of `snap.Hash`.
  - **`internal/factory/reconciler/grants.go`:**
    - `needsGrants` and `grantHolds` take the resolved hash;
    - `decidingPolicy` returns the deciding class.
  - **`internal/humanapi/grants.go`:** wording per PR11 ("under class definition <hash>").
  - **`web/src/approval-memory.ts`:** the placeholder `the decision record's resolved=`, and the query key `resolved` (still accepting `policy` for one release).
  - `internal/store/grants.go`: doc comments only.
  - Tests: `internal/factory/reconciler/grants_test.go`, `grant_gate_test.go`, `authority_grants_test.go` (named edits: the bound hash), `web/test/approval-memory.test.ts`.

- [ ] **Step 1: failing tests.**
  - `TestCentralClassAsksApproval`: a central live class with `requiresApproval: [{changedFiles: ['^infra/']}]`, on a diff touching `infra/x`, gives `human:approval_required`. In shadow it gives `shadow` with `reasonShadowApproval`.
  - `TestCentralGrantArms`: with `approvalMemory` and a fake broker holding an active grant bound to `ResolvedHash(cl)`, the decision arms and its record carries `grant=`. A grant bound to the old file hash does not.
  - `TestUnrelatedClassEditKeepsGrants`: changing another class's policy leaves this class's resolved hash, and its grant, intact. Changing this policy's `holdoutChecks` suspends it, through the broker fake's lookup.
  - **The web test:** the prefilled form reads `?resolved=`.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/reconciler/ -run 'Grant|Approval' && task ui:test`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task ui:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(gate): approval grants bind to the resolved class, and a central class's requiresApproval asks (B13, Q9)`, then open PR E. Its body lists PR11–PR13 and the `merge.go` hunks shared with #500.

## PR F: the stage-4 file in the binding shape

### Task 16: the file's grammar reads both shapes (M)

**Files:**
- Modify:
  - **`internal/factory/mergepolicy/policy.go`:**
    - `File` gains `MergePolicies []config.Binding \`json:"mergePolicies,omitempty"\``;
    - `Parse` accepts `classes:` or `mergePolicies:`, never both (`the file declares classes (the old shape) or mergePolicies, never both`);
    - duplicate binding names are refused (the CRD's `listType=map`, done by hand);
    - at most 32.
  - **`internal/factory/mergepolicy/resolve.go`:**
    - `Validate` for bindings: OD-8 on names; each binding through `config.Resolve` against `cfg.Policies[name]`, the guardrails and the repository's resolved `Merge`; `MissingRef` when the policy is not served;
    - the refusals are returned apart (`BindingErrors`), not joined into the file's error: B15's isolation;
    - a legacy file using `conditions.any` returns a `Deprecations` line (PR4).
  - Tests: `policy_test.go`, `resolve_test.go`.

- [ ] **Step 1: failing tests.**
  - The spec's file (`docs-links`, `revert`, `alert-rules`) parses.
  - `classes` with `mergePolicies` is refused.
  - **Under `cfg.Policies` holding those three:**
    - the file is valid;
    - `alert-rules` resolves with the binding's `allowedFiles` and `requiresApproval`;
    - a binding `foo` with no policy gives `BindingErrors[foo] = MissingRef`, and the file is still valid;
    - a binding without `revert` makes the file invalid (OD-8).
  - A legacy file with `any` validates and carries a deprecation.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/mergepolicy/ -run 'Parse|Validate|Binding'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS, every legacy test unchanged.
- [ ] **Step 3: commit** `feat(mergepolicy): the repository file binds central policies (B15)`.

### Task 17: Watch, comparison, structure and export for bindings (M)

**Files:**
- Modify:
  - **`internal/factory/mergepolicy/watch.go`:**
    - `Snapshot.Refused map[string]config.BindingRefusal`;
    - per-binding last good (`lastBinding`), served while the process runs;
    - `evaluate` resolves each binding into `Resolved{Class: cl, Approval: paths}`.
  - **`internal/factory/mergepolicy/structural.go`:** `Compare` of a binding file against `rc.Classes` (stages 1 and 3); `Structural` between two binding files on resolved classes with `looser`; `ClassChange(old, next config.Class) Change` exported for B11.
  - **`internal/factory/mergepolicy/export.go`:** `Export` writes `mergePolicies:` from `rc.Bindings` for a bound repository, and refuses a legacy-served one (`migrate the repository first: maku config migrate`).
  - **`internal/factory/statuswriter/desired.go`:** `RepositoryFileValid`'s message names refused file bindings and deprecations.
  - **`internal/app/maku_policy.go`:** `export` and `check` print the binding errors and the deprecations.
  - Tests: `watch_test.go`, `structural_test.go`, `export_test.go`, `internal/app/maku_policy_test.go`.

- [ ] **Step 1: failing tests.**
  - **Divergence:** an exported binding file of cloud-native-ref's bound repository gives 0 divergences against the served config, and dual evaluation agrees on every fixture of `policyview/testdata` (`dual=agree`).
  - **Isolation:** under `classAuthority: repository` and the switch, one refused binding leaves the others serving through `classSource`; while the process runs, the refused one serves its last resolved class.
  - **Demotion:** a relaxation of a live binding (a wider `allowedFiles` on the policy) is demoted to shadow, as MP8 does today.
  - **The CLI:** `maku policy export` of a legacy-served repository fails with the migrate message.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/mergepolicy/ ./internal/factory/statuswriter/ ./internal/app/ -run 'Watch|Structural|Export|Policy'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(mergepolicy): observe, compare and export the binding shape`, then open PR F with PR4 in the body.

## PR G: the CLI

### Task 18: the converter proves no class changes (L)

**Files:**
- Create: `internal/factory/resources/split.go` (`Split(objs []Object, ns string) ([]Object, error)` per PR19, and `Equal(before, after *config.Config) []string`, which lists every repository and class whose resolved `config.Class` differs, field by field), `internal/factory/resources/split_test.go`.
- Modify: `internal/factory/resources/migrate.go` (`Migrate` emits the class shape through `Split`), `internal/factory/resources/migrate_test.go`, `internal/app/maku_config.go` (`config migrate --from <file>` or `--manifests <legacy objects…>`; both assemble before and after, and refuse unless `Equal` is empty), `internal/app/maku_config_test.go`.

- [ ] **Step 1: failing tests.**
  - `TestSplitANR` over `testdata/anr-objects.yaml`:
    - **12 policies:**
      - `docs-links`, `revert`, `dead-links`;
      - `docs`, `tests`, `dashboards`, `scripts`, `review`;
      - `alert-rules`, `deps-patch`, `deps-minor`;
      - `kb-entries`.
    - **4 Repositories** with `merge` and bindings.
    - **The modes** are the spec's migration table: cloud-native-ref's `docs-links` and `revert` live, the rest shadow or off.
    - **`docs-links`:** `requires: [holdoutChecks]`, and its holdouts on the bindings.
    - **`dead-links`:** `requires: [allowedFiles, oracleWorkflow]` (both consumers set them), with `holdoutChecks` and `protectedFiles` on the policy (the same set in both).
    - **`deps-patch`:** `fluxSource: flux-system/flux-system` on cloud-native-ref's `merge`.
    - **`Equal(before, after)`** is empty.
  - **Refusals:**
    - two consumers of `docs` with different `checks` are refused, naming `checks`;
    - one repository with two Flux sources is refused (Q14);
    - with `mergeGuardrails` that change a class, `Equal` names it and `config migrate` exits non-zero (PR10).
  - **The CLI:** `maku config migrate --manifests anr-objects.yaml` prints the 16 objects, and `maku config validate` accepts them.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/resources/ ./internal/app/ -run 'Split|Migrate|ConfigMigrate'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): maku config migrate splits classes into policies and bindings, and refuses unless every resolved class is equal (B17)`.

### Task 19: the blast radius, the resolved classes, the guardrails (M)

**Files:**
- Create: `internal/factory/resources/impact.go` (`Impact(base, next Result) []PolicyImpact{Policy string; Generation int64; Consumers []ConsumerImpact{Repository, Mode, Change string; Why []string; Resolves bool; Reason string}}`, using `mergepolicy.ClassChange` on each consumer's resolved class), `internal/factory/resources/impact_test.go`.
- Modify: `internal/app/maku_config.go`. `config validate` gains:
  - `--base <file…>`: the base manifests. It prints the impact, live consumers first, each relaxation line prefixed `LIVE` (Q5);
  - `--resolved`: prints each repository's resolved classes as JSON, sorted, for ai-native-ref's snapshot;
  - `--expect-unchanged`: exits non-zero when any resolved class differs from `--base`'s;
  - one `guardrail:` line per `GuardrailMismatch` (Q10);
  - the legacy-shape warnings.
  
  Also `internal/app/maku_config_test.go` and `docs/cli.md`.

- [ ] **Step 1: failing tests.** The expected output is a literal in the test:
  ```text
  MergePolicy dead-links (generation 3 -> 4):
    cloud-native-ref  shadow  tightening                                   resolves
    ai-native-ref     shadow  -                                            refused: MissingParameter: MergePolicy dead-links requires requiredLabel
  MergePolicy docs-links (generation 1 -> 2):
    LIVE cloud-native-ref  live  relaxation: allowedFiles broadened or rewritten  resolves
  ```
  Also:
  - `--expect-unchanged` fails on that change and passes on `testdata/anr-objects.yaml` against its split;
  - `--resolved` prints the frozen `testdata/anr-resolved.json`;
  - `mergeGuardrails.maxPerDay: 1` with a binding at 2 prints `guardrail: Repository/cloud-native-ref dead-links: maxPerDay 2 is over the guardrail's 1` and exits 1.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/resources/ ./internal/app/ -run 'Impact|ConfigValidate'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): maku config validate --base shows each consumer's change, and the bindings over a guardrail (B11, Q5, Q10)`, then open PR G.

## PR H: evidence and the settings API

### Task 20: the Evidence card splits by policy generation (S)

**Files:**
- Modify: `internal/factory/evidence/evidence.go` (`record` also captures `policygen=` and `repogen=`; `ShadowTally.ByGeneration []GenerationCount{Generation int64 \`json:"generation"\`; Before bool; MergedUnchanged, Changed int}`, newest first; `Evaluate` unchanged), `internal/factory/evidence/evidence_test.go`, `scripts/testdata/tally/prs-decisions.json` (records across two generations and one `before`).

- [ ] **Step 1: failing test `TestShadowByGeneration`:**
  - three clean records at `policygen=4 repogen=7`, seventeen at `policygen=3 repogen=6`, and twelve with no `repogen=` give `[{4, 3}, {3, 17}, {before, 12}]`;
  - `Evaluate`'s `Clean` is 32 (Q6: promotion counts every one).
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/evidence/ -run ByGeneration`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(evidence): shadow decisions counted per policy generation, for the card (Q6)`.

### Task 21: the factory serves bindings and policies; the broker forwards them (M)

**Files:**
- Modify:
  - **`internal/factory/evidence/service.go`:**
    - `Report` gains `Repository` (the object name), `RepositoryManifest` and `RepositoryManifestPath` (`repository-<name>.yaml`), `RepositoryStatus *RepositoryStatusView`, and `Bindings []BindingView{Name, Mode, PolicyGeneration int64, Parameters config.Binding, Accepted bool, Reason string}`;
    - the legacy fields stay for a legacy-served repository;
    - `PolicyReport{Name, Generation, Spec config.PolicySpec, Manifest, ManifestPath, Status factoryv1.MergePolicyStatus}`;
    - `(*Service).Policies` and `Policy(name)`.
  - **`internal/factory/policyview/preview.go`:** `Preview` takes `{kind: file | binding | policy}`:
    - a `binding` edit (one repository's `mergePolicies`) gives the Repository manifest and each binding's resolution;
    - a `policy` edit gives `mergepolicy-<class>.yaml` and `resources.Impact` against the served set.
  - **`internal/factory/api/server.go`:** `GET /v1/policy/policies`, `GET /v1/policy/policies/{name}`, behind `policyManagement.promotion` as their siblings.
  - **`internal/runrequest/`:** the client methods.
  - **`internal/humanapi/policy.go`:** `PolicyGateway` gains `PolicyList` and `Policy`; routes `GET /api/policy/policies` and `GET /api/policy/policies/{name}`; the page route `/settings/merge-policies/policies/{name}`.
  - **`internal/app/factory.go`:** wire `RepositoryStatus` and `PolicyStatus` from the cached client.
  - Tests: `service_test.go`, `policyview_test.go`, `manifest_test.go`, the api server test, `internal/humanapi/policy_test.go`.

- [ ] **Step 1: failing tests.**
  - **A bound repository's report:**
    - lists its bindings with policy generation and status;
    - its `RepositoryManifest` round-trips through `resources.Decode`;
    - `Manifest` and `MergePolicy` are empty.
  - **The policies:** `GET /v1/policy/policies/dead-links` shows two consumers.
  - **A policy preview** raising `maxModifiedLines` lists both consumers as `relaxation`.
  - **A binding preview** dropping `revert` is refused (OD-8).
  - **The broker:** forwards with the caller's token, and answers 404 for an unknown policy.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/evidence/ ./internal/factory/policyview/ ./internal/factory/api/ ./internal/humanapi/ -run 'Policy|Report|Preview|Binding'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(policy): the settings API serves bindings, policies and their consumers (B16)`, then open PR H. Its body names the `internal/app/factory.go` hunk (PR16).

## PR I: the settings pages

### Task 22: the repository page's cards become bindings (M)

**Files:**
- Modify:
  - `web/src/settings-api.ts`: the new report fields, and `mergePolicyCell` reading `repository` and bindings;
  - `web/src/settings.ts`:
    - each card is a binding: its policy (linked), the policy generation, the parameters this repository supplies, the mode, refused or not with the reason, and the evidence;
    - the "MergePolicy" row becomes "Repository" for a bound repository;
    - `Repository.status.merge` is shown, with each key marked "from the Factory" when it is in `mergeDefaulted`;
  - `web/src/class-card.ts`: per PR13, an evidence line per generation (`gen 4: 3 clean · gen 3: 17 clean · before the binding: 12`). The promotion text becomes "To promote, set `mode: live` on the binding in `repository-<name>.yaml` and merge the change", or "…in `.github/agent-factory.yaml`" under repository authority (B16);
  - `web/test/settings.test.ts`, `web/test/class-card.test.ts`;
  - `internal/humanapi/ui/dist` (rebuilt).

- [ ] **Step 1: failing vitest cases.**
  - **A bound repository's page:**
    - shows one card per binding;
    - ai-native-ref's refused `dead-links` card reads "Refused: MissingParameter …" and "Serving generation 3";
    - shows the split evidence line and the new promotion text.
  - **A legacy-served repository's page** is unchanged (snapshot of today's text).
  
  Run `task ui:test`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task ui:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(ui): the repository page shows bindings, their parameters, modes and evidence by generation (B16, Q6)`.

### Task 23: the policy page and the two editors (M)

**Files:**
- Create: `web/src/policy-page.ts`, `web/test/policy-page.test.ts`.
- Modify:
  - `web/src/settings.ts`: the route `/settings/merge-policies/policies/<name>` is matched first (PR15), and the "Merge policies" index gains a "Policies" table;
  - `web/src/policy-editor.ts`: two modes:
    - a binding edit outputs a Repository manifest patch, "Copy repository-<name>.yaml";
    - a policy edit outputs `mergepolicy-<class>.yaml` and the consumer impact from the preview, live consumers first.
  - `web/src/policy-fields.ts`: the binding fields per B4; meaning fields are read-only in a binding edit;
  - `web/test/policy-editor.test.ts`, `docs/web-ui.md`, `internal/humanapi/ui/dist`.

- [ ] **Step 1: failing vitest cases.**
  - **The `dead-links` page:**
    - shows the definition, `requires` and the consumers table (repository, mode, served generation, accepted, reason);
    - its header reads `2 consumers · 1 current`.
  - **The editors:**
    - the policy editor's output names `mergepolicy-dead-links.yaml` and lists a LIVE relaxation first;
    - a binding edit cannot change `diffShape`.
  - **Routing:** `/settings/merge-policies/policies/x` routes to the policy page, and `/settings/merge-policies/Smana/runlore` to the repository page.
  
  Run `task ui:test`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task ui:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(ui): a page per merge policy with its consumers; separate editors for a policy and a binding (B16)`, then open PR I.

## PR J: docs; release N

### Task 24: the docs (S)

**Files:**
- Modify:
  - **`docs/merge-policies.md`:**
    - "Factory resources" rewritten: one class per policy, bindings, the B4 table, the reasons with their fixes, status, `mergeDefaults`;
    - the file's `mergePolicies:`;
    - `conditions.any` deprecated;
    - the record's keys.
  - **`docs/factory.md`:** the record's `repogen=` and `resolved=`, `Repository.status`.
  - **`docs/api.md`:** the CRD fields and columns.
  - **`docs/cli.md`:** `config migrate --manifests`; `config validate --base`, `--resolved` and `--expect-unchanged`.
  - **`docs/upgrading.md`:**
    - N: run `maku config migrate --manifests`, apply, and check `kubectl get mergepolicy,repository`;
    - before N+1: no `repositoryRef` left (PR20), and no file in `classes:`.
  - **`docs/operations.md`:** `BindingsValid`, and reading a refused binding.
  - **`docs/roadmap.md`.**
  - **`charts/agent-factory-crds/README.md`.**
  - **The spec:** status "built (release N)", PR3's amendment and PR4's reading.
  - **TODO (B-L2):** wherever a binding is shown, quote the mode: `mode: "off"`. YAML 1.1 reads a bare `off` as `false` and `on` as `true`, so the API server refuses an unquoted one as a boolean.

- [ ] **Step 1:** write. Run `task docs` (dead links, anchors), then `task check`. Expected: PASS.
- [ ] **Step 2: commit** `docs: reusable merge policies, bindings and the two-release upgrade`, then open PR J. Release N is cut after it merges.

## PR K: the legacy shape removed; release N+1

**Precondition:** ANR-1 to ANR-5 and cnref-1 (below) are merged and proved live. `kubectl get mergepolicy -A -o jsonpath='{..repositoryRef}'` is empty on both clouds.

### Task 25: the per-repository `MergePolicy` is gone (M)

**Files:**
- Modify:
  - **`api/factory/v1alpha1/factoryconfig_types.go`:**
    - remove `RepositoryRef`, `Classes`, `Merge`, the legacy status fields, the legacy CEL and the `Repo` column;
    - the name rule applies unconditionally;
    - remove `ReasonNoMergePolicy` and `ReasonDuplicatePolicy`.
  - **`internal/factory/resources/`:**
    - `assemble.go` loses the legacy path and `DefinedTwice`'s legacy case;
    - `manifests.go`: `Decode` refuses `spec.repositoryRef` with `the per-repository MergePolicy was removed in this release: maku config migrate --manifests (from the previous release)`;
    - delete `MergePolicyManifest`.
  - **`internal/factory/configsource/source.go`:** drops legacy outcomes.
  - **`internal/factory/statuswriter/desired.go`:** drops the legacy branch.
  - **`internal/factory/evidence/service.go`:** drops the legacy report fields.
  - **`web/src/settings*.ts`:** drops the legacy rows.
  - **`internal/factory/catalogue/catalogue.go`:** drops `Generations.MergePolicy` (PR1), and `classes_list` follows.
  - Tests in every package above, the generated CRDs and `charts/agent-factory-crds/tests/render.sh`.

- [ ] **Step 1: failing tests.**
  - **`resources.Decode`** refuses a legacy manifest with the message above.
  - **The schema:** a CRD test asserts `mergepolicies` has no `repositoryRef`.
  - **Envtest:** a legacy object created under N's CRD reads back after the upgrade as an empty class policy with `CONSUMERS 0` (PR20).
  
  Run the Task 6, Task 8 and Task 10 commands. Expected: FAIL.
- [ ] **Step 2:** implement, `task crd:gen`, then rerun everything and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(api)!: the per-repository MergePolicy is removed (B8)`.

### Task 26: the file's `classes:` and `conditions.any` are refused (S)

**Files:**
- Modify: `internal/factory/mergepolicy/policy.go` (refuse `classes:` with `the classes shape was removed in this release: write mergePolicies with maku policy export`; delete `Conditions`, `Leaf`, `ClassPolicy` and their parse code), `resolve.go` (delete the legacy `Resolve`; `Resolved.Branches` and `Variants` collapse to one class), `structural.go`, `export.go`, `internal/factory/reconciler/evaluate.go` (`DecideArmUnder` takes one class; delete `rank` if unused), `internal/factory/reconciler/dual.go`, `internal/factory/policyview/` (fixtures), `web/src/policy-*.ts` (the file editor's `any` branches), the tests of each, and `docs/merge-policies.md` and `docs/upgrading.md` (N+1).

- [ ] **Step 1: failing tests.**
  - A file with `classes:` is refused with the message above.
  - The `policy_any_test.go` cases become refusals: Q13 per PR4.
  - `TestDecideArmUnder` with one class still decides every existing fixture identically.
  
  Run `systemd-run … go test -race -count=1 ./internal/factory/mergepolicy/ ./internal/factory/reconciler/ ./internal/factory/policyview/ && task ui:test`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(mergepolicy)!: the file's classes shape and conditions.any are removed (B8, B15, Q13)`, then open PR K. Release N+1 is cut after it merges.

## After this step (not in these PRs)

- **ai-native-ref, one repository per PR (B18), between releases N and N+1.** Smallest first. Each PR runs `task check`, which now runs `validate-factory-config.sh`:
  - the pinned `maku config validate` for both clouds;
  - then `--base` against `origin/main`'s `kustomize build`, printing the blast radius;
  - then `--resolved`, compared with the committed snapshot.
  - **ANR-1 (M), pin and tooling, plus runlore.**
    - Bump the Maku pin and digest to release N.
    - **`scripts/ci/factory_config.py`** learns the binding shape:
      - `CATALOGUE` gains `mergeDefaults`;
      - a class-shape `MergePolicy` and a Repository's `merge` and `mergePolicies` are resolved by the B4 table in Python, about 60 lines;
      - `config()` keeps printing `repositories: [{name, classes, merge, …}]`, so the parity, profiles, repo-files, pins, kb-class, paused-alert, steering and intake-classes suites read the same shape.
    - **New test `scripts/ci/tests/test-agent-resolved-classes.sh`:**
      - runs `maku config validate --resolved` for both clouds;
      - compares the result with `scripts/ci/tests/fixtures/resolved-classes-{aws-0,gcp-0}.json`, captured from `origin/main` before any migration;
      - fails with a diff;
      - also compares `factory_config.py`'s resolution with Maku's on the same manifests, which keeps the two honest.
    - **Migrate runlore:**
      - `maku config migrate --manifests` gives the `docs-links` and `revert` policies;
      - `repository-runlore.yaml` gains `merge` and two bindings;
      - `mergepolicy-runlore.yaml` is deleted;
      - the kustomization adds `mergepolicy-docs-links.yaml` and `mergepolicy-revert.yaml`.
  - **ANR-2 (S), runlore-kb:** adds `kb-entries`. The snapshot is unchanged.
  - **ANR-3 (S), ai-native-ref:** adds `dead-links`, `docs`, `tests`, `dashboards` and `scripts`. The snapshot is unchanged.
  - **ANR-4 (S), cloud-native-ref**, the only repository with live classes:
    - adds `review`, `alert-rules`, `deps-patch` and `deps-minor`;
    - `fluxSource: flux-system/flux-system` moves to `merge`;
    - the snapshot is unchanged.
  - **ANR-5 (S), `Factory.spec.mergeDefaults` (Q8):**
    - move the keys all four `merge` blocks share into `factory.yaml`: `policyBotLogin`, `mergerLogin`, `verifyFor`, `revertWindow`, `awaitingCIFor`, `approveToMerge` and `breaker`;
    - each Repository keeps only its overrides;
    - the snapshot is unchanged, and `kubectl get repository -o jsonpath='{.items[*].status.merge}'` equals the old blocks.
  - **ANR-6 (S):** bump the pin to release N+1 after PR K. `factory_config.py` drops the legacy shape.
- **cloud-native-ref (cnref-1, S),** between ANR-4 and N+1:
  - `.github/agent-factory.yaml` is rewritten with `maku policy export` in one PR;
  - the policy divergence gauge stays 0, and every decision keeps `dual=agree`.
- **The live proof**, in the next gcp-0 session (Q15, PR18), recorded in the session ledger:
  - `kubectl get mergepolicy,repository -n agent-system` shows twelve policies with `CONSUMERS` and `CURRENT` equal, and four Repositories with their `CLASSES` and `LIVE` counts;
  - every decision after the switch carries the same `class=` as before, plus `repogen=` and `resolved=`;
  - the Evidence page's counts for cloud-native-ref's classes equal those read before the switch, now split by generation with `before` holding the old ones;
  - `dual=agree`;
  - one deliberate bad edit on a scratch branch: a `requires` that one consumer cannot meet shows `CONSUMERS 2, CURRENT 1` and `BindingsValid=False` before it is reverted.
- **#470's stage 5** may merge at any time (PR1).
- **Later, outside this spec:**
  - bundles;
  - aliases;
  - per-binding harness (Q12);
  - per-binding `fluxSource` (Q14);
  - policies across namespaces;
  - generating policy-bot rules;
  - a refused-binding metric, should ai-native-ref want to alert on `BindingsValid` rather than read it.

## Self-review

- **B1:** the name is the class, held by CEL (Task 4). No alias anywhere. The built-in rules (`docs-links`, `revert`, `review`) are kept by `validateClass` on every resolved class (Task 2).
- **B2, Q14:** `Repository.spec.merge` is `config.Merge` plus `fluxSource` (Tasks 1, 4). `MissingParameter` when a monitored policy is bound without it (Task 2). The converter refuses two sources in one repository (Task 18).
- **B3:** `listType=map`, at most 32, `mode` required, and `review` takes only `off`, proved on envtest (Task 6).
- **B4:** one row per field kind in `TestResolve` (Task 2).
- **B5:** the record, the breaker, `maxPerDay`, `policy_promotions` and grants stay keyed by (repository, class), whose class names do not change (Task 18's equality). Promotion edits the binding (Task 22's text).
- **B6, Q11:** `AmbiguousAdoption` by list order (Task 8, PR9).
- **B7:** the flat shape plus `requires` and `requiresApproval` (PR2, Tasks 1, 4). `any` is not carried (Tasks 16, 26).
- **B8, Q1:** both shapes in N (Tasks 4, 8, 16), and the old one refused in N+1 (Tasks 25, 26). `DefinedTwice` per PR8. The legacy warning (Global Constraints, Task 19).
- **B9:**
  - per-binding isolation in Assemble (Task 8), configsource (Task 10), status (Task 11) and the file (Tasks 16, 17);
  - last good while running, none after a restart (Task 10);
  - bound names keep the repository serving (Task 9).
- **B10:** `Repository.status.classes[]`, `authority`, `RepositoryFileValid`, `InSync`, `BindingsValid`; `MergePolicy.status.consumers[]` with `CONSUMERS CURRENT LIVE ACCEPTED AGE` (Tasks 5, 11, 12).
- **B11, Q5:** `config validate --base` with LIVE consumers first (Task 19). The editor's policy preview (Tasks 21, 23). ai-native-ref's CI runs it (ANR-1).
- **B12:** `policygen=` is the class's policy, `repogen=` the Repository's, and `configGenerations.mergePolicy` the class's (Tasks 13, 14).
- **B13, Q9:** the resolved hash (Task 3), grants bound to it, and approval under central authority (Task 15, PR11, PR12).
- **B14, Q10:** guardrails in `Resolve` for every binding (Task 2). CI lines and a non-zero exit (Task 19).
- **B15, Q4:** the file's `mergePolicies:`, guardrails capping, `classAuthority: repository` without central bindings (CEL, Task 4), and per-binding isolation (Tasks 16, 17).
- **B16:** binding cards, a policy page and two editors, all read-only (Tasks 21–23).
- **B17:** `Split` plus `Equal`, refusing differing meaning (Task 18).
- **B18:** one ai-native-ref PR per repository, smallest first, `factory_config.py` in the first, and the cnref file between N and N+1 (After this step).
- **B19:** overtaken. The catalogue's per-class generations land in N (PR1, Task 14).
- **Validation:**
  - every CEL rule on envtest (Task 6);
  - the reconciler's per-binding reasons (Task 8);
  - per-policy `Invalid` with the last good serving (Tasks 8, 11);
  - offline `validate` and `--base` (Task 19).
- **Keys:** every row of the spec's Keys table is covered by Tasks 13–15 and 20. Metric labels are unchanged.
- **Q2:** no alias. **Q3:** any policy of the namespace (`MissingRef` otherwise). **Q6:** Task 20. **Q7:** run-only classes bound `off` (Tasks 2, 18). **Q8:** Tasks 3, 9, 11 and ANR-5. **Q12:** no per-binding harness, since the binding refuses meaning fields. **Q13:** PR4 and Task 26, flagged for the owner. **Q15:** after step 3 (PR16), before v4.0 (PR17), and the live session on gcp-0 (PR18).
- **Overlap with v4 step 3:** PR16 lists the shared files and keeps every edit in other hunks or new files. Generated CRDs are regenerated, never merged by hand.
- **Risks named with a stop rule:**
  - **CEL cost.** The explicit `has()` lists may exceed the CRD cost budget. Task 6 fails, and bounds are tightened, never rules dropped.
  - **Equality fails on a live repository.** If the converter's `Equal` names a difference, that migration PR stops, the difference is a bug in Split or Resolve, and the snapshot is never regenerated to pass.
  - **Python and Go resolution drift in ai-native-ref.** ANR-1's cross-check fails CI.
  - **A forgotten legacy object at N+1:** visible as `CONSUMERS 0`, and harmless (PR20).

