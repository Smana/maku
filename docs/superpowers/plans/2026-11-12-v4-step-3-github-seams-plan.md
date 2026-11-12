# v4 rollout step 3, the GitHub seams: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the factory's GitHub access becomes a set of seams a platform team can wire with two GitHub Apps and no component from cloud-native-ref (cnref):
- **D10.** The factory mints a short-lived installation token for each run. The token is scoped to the run's one repository and carries the permissions its role gets from one role table, written into that run's own Secret. Long runs get it refreshed, and the token is revoked when the run ends. The H2 publisher gets its token from the same minter. octo-sts is still supported but no longer needed.
- **D11.** `maku/merge-gate` is the gate a production repository can depend on. It is allowed without policy-bot outside the demo, it posts `success` only when the factory arms, and branch protection can require it from the factory App. A documented ruleset and a protection observer back it. policy-bot remains an optional integration.
- **D12.** Two Apps: the agents App and the factory App. The merger can fold into the factory App. The config and chart seams (`apps.*`) exist here, and each App's permission set is Go data that `maku install` (step 7) will create the Apps from. An agent token never holds `statuses`, `workflows` or `administration`, and the reviewer's token is read-only.

Every existing install keeps working (D25):
- `runTokens.enabled` without `perRun` keeps step 2's shared writer;
- `githubSecret`, `mergerSecret` and `runTokens.appSecret` keep their meaning;
- `merge.policyBot` unset still means policy-bot;
- the XR plus octo-sts path is untouched.

**Architecture:**
- **A new package, `internal/factory/minter`, holds three things:**
  - `Roles`, the one role table (PR2);
  - `Minter`, which wraps the App-key calls that `forge` exports: the installation lookup, the downscoped mint, revoke, and the granted-permissions read;
  - two consumers: `RunTokens`, a leader runnable that keeps one Secret `<run>-git` per live run, and `PublisherTokens`, the second `applier.PublisherTokens` adapter.
- **The run-controller** gains a `gitTokens: perRun` mode. It mounts the run's own Secret, waits for it, and checks its owner and expiry.
- **The factory writes per-run Secrets with server-side apply.** Its RBAC is `create`, `patch` and `delete` with no `get`. A required ValidatingAdmissionPolicy bounds it to `<prefix><id>-git` Secrets that it owns, so the factory can never read the runs namespace's other Secrets (PR3).
- **The merge gate** keeps `reconciler/gatestatus.go`. It adds:
  - the production `policyBot: false`;
  - a stricter `success`;
  - a branch-rules observer;
  - an `agent_merged` outcome.

**Tech Stack:**
- Go: controller-runtime (server-side apply), go-github v92, golang-jwt;
- the GitHub REST endpoints `GET /repos/{o}/{r}/installation`, `POST /app/installations/{id}/access_tokens` (`repositories`, `permissions`), `GET /app/installations/{id}`, `DELETE /installation/token` and `GET /repos/{o}/{r}/rules/branches/{b}`;
- ValidatingAdmissionPolicy v1, proved on envtest 1.36;
- Helm 3.14+ templates, JSON Schema, the bash render tests (`yq`);
- Python for the harness credential helper;
- k3d (demo-e2e) and the nightly real-App demo.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D10 (tokens), D11 (merge gate), D12 (two Apps), D25 (additive), D26 (floor);
- "Rollout", step 3.

It interacts with the hardening plan `docs/superpowers/plans/2026-11-12-hardening-h1-h2-plan.md`, rulings HR11, HR12 and HR13 (pending the owner).

**Read against:** Maku `origin/main` `b07ab767`. Step 2's PR A to C and hardening H1, H2a and H2b are merged.

**Branch:** five PRs, each from `origin/main` once the previous one merges (E may run in parallel with B to D).

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, the minter core (D10) | `feat/v4-github-minter` | 1–3 | M | first; unreachable from config |
| B, per-run tokens on the run-controller (D10) | `feat/v4-github-run-tokens` | 4–8 | L | second |
| C, the publisher, readiness and HR13 (D10, HR12) | `feat/v4-github-publisher` | 9–11 | M | third: needs A and B |
| D, two Apps (D12) | `feat/v4-github-apps` | 12–14 | M | fourth |
| E, the merge gate (D11) | `feat/v4-github-merge-gate` | 15–19 | M | any time after A; last to merge |

The step's v0.4x minor is cut after PR E. A to D may also ship in interim minors.

## Global Constraints

- **No v4.** This step ships v0.4x minors only: no v4 tag, release or chart version.
- **Additive (D25).**
  - Every new behaviour is opt-in: `runTokens.perRun`, `runController.gitTokens: perRun`, `hardening.publisher.minter`, `apps.merger.mode: factory`, `merge.policyBot: false` outside the demo.
  - Defaults keep today's render. Prove it with a render diff against `origin/main` for the umbrella demo, the maku quickstart and `runController.enabled=false`.
  - Old keys keep working. Where an old and a new key conflict, the render or the config fails with a message naming both.
- **Fail closed.**
  - A run without its minted Secret waits Pending, and `caps.maxPendingMinutes` escalates it.
  - A mint that answers different permissions than it asked for is refused and revoked.
  - With `perRun`, the factory's Secret RBAC never renders without its admission policy.
- **One role table.** Every permission a run or the publisher is minted with comes from `minter.Roles`. No other code builds a run's permission map. A test greps `internal/` for `"pull_requests": "write"` outside `minter` and the existing factory and merger sets.
- **No token in a log, a metric, an error or an event.** Errors name the HTTP status only, as `forge.appCaller.call` does.
- **Bounded vocabularies.** Mint results, binding refusals and outcomes are closed sets.
- **Strict decoding.** The factory and run-controller configs are decoded strictly, so chart and binary change in the same PR. The demo-e2e nightly goes red until the release; name this in each PR body.
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
  - `task crd:gen && task crd:check` after an API type change;
  - `task harness:test` after a harness change;
  - `task check` before the last commit of each PR.

## Rulings made while planning

- **PR1 (what already exists; this step builds only what is missing).**

  | Spec item | Already on `origin/main` | Missing, built here |
  |---|---|---|
  | **D10: the factory App's own auth** | `forge.Connect` → `tokens` (`internal/factory/forge/app.go`). It signs an RS256 App JWT from the mounted `app_id` and `private_key`, read at each mint, backdated 1 min with a 9 min life. It finds the installation with `GET repos/{o}/{r}/installation` and mints `POST app/installations/{id}/access_tokens` with `repositories: [name]` and the fixed `permissions()` (contents read, issues write, pull_requests write, metadata read). It caches the token until 5 min before expiry, and a 401 invalidates it. `forge.Mint` is the same, uncached | An installation cache shared across repositories, a check of the reply's permissions, revoke, and the granted-permissions read (Task 1) |
  | **D10: run tokens** | `internal/factory/runtoken` (step 2): two shared Secrets, `agent-git` (`contents`/`pull_requests: write`, worn by implementer **and tester**) and `agent-git-read`. They come from one App (`runTokens.appIDFile`), cover every served repository of **one owner** (`oneOwner`) and are renewed every 30 min. The run-controller mounts them by role (`GitSecretFor`, `readOnlyRoles`). On the XR, runs exchange at octo-sts under `.github/chainguard/agent-<role>.sts.yaml` in cnref and ANR, through identity-proxy (`images/maku-harness/git_credential_agent.py`) | A token per run, per repository and per role from one table, refreshed and revoked (Tasks 2–8) |
  | **D10: publisher** | `applier.PublisherTokens` with `OctoSTSTokens` (`internal/factory/applier/publisher.go`); `hardening.publisher.octoSTS` is required by `validateEnforce` | `minter.PublisherTokens` and `hardening.publisher.minter` (Task 9) |
  | **D11: `maku/merge-gate`** | **It exists:** `reconciler/gatestatus.go` (`GateStatusContext`, `statusFor`, `postGateStatus`) is called from `awaitingCI` when `merge.gateStatus`. It is posted by the merger connection (`MergerPermissions(…, status)` asks `statuses: write`), skipped when unchanged and truncated to 140 characters. The demo requires `gateStatus: true`, and demo-e2e asserts it | `policyBot: false` outside the demo (today refused: "production gates through policy-bot"); `success` only on arm; branch-protection docs and the observer; agent merges (Tasks 15–19) |
  | **D11: policy-bot** | `arm.go:495–510` and `merge.go:506` read `policy-bot: main` from `merge.policyBotLogin` (ERROR → `gate_path`, otherwise `policy_pending`/`policy_absent`). `trust.go` trusts `policyBotLogin`. Without it, every live class needs `allowedFiles`. A class's `conditions.deny.protectedFiles` is Maku's own gate-path list | Nothing new in the gate rows; config and docs only |
  | **D12: Apps** | Three key pairs in the config: `github.appIDFile` (factory), `github.mergerAppIDFile` (merger, R16; `mergerLogin` is "the arming actor, so the merge actor", used to tell the factory's merges from human ones), and `runTokens.appIDFile` (agents, step 2). The merger may already name the factory App's files (the demo does). `factoryLogin` and `agentsLogin` exist. The broker's verdict poster (`ROOMS_GITHUB_APP_DIR`) is rendered by no chart | The `apps.*` chart seam, the agents App config key, the login rules for the folded merger, and the permission sets as data (Tasks 12–14) |

- **PR2 (the role table: one data structure, two variants, the owner's ruling pending, HR12).**
  - **The structure:** `internal/factory/minter/roles.go` declares:

    ```go
    type Permissions map[string]string                   // e.g. {"contents": "write"}
    type Change struct{ When []hardening.OutputType; Permission, To string } // applies when any When type is enforced
    type Rule struct{ Base Permissions; Changes []Change }
    type Table map[string]Rule // implementer, tester, reviewer, triager, publisher
    var TableD10, TableHR12 Table
    var Roles = TableHR12 // set by the owner's ruling on HR12; the only line that changes
    func (t Table) For(role string, enforced []hardening.OutputType) (Permissions, error)
    ```

  - **`For`'s input** is the run's own `agents.ogenki.io/safe-outputs` annotation, written at CREATE (HR14). It is never the live config, so a mid-run publish never widens a token. A role absent from the table is an error, never "read".
  - **Shared reads (R):** `issues: read`, `checks: read`, `actions: read`, `metadata: read`.

  **Variant HR12 (recommended):**

  | Role | Base | `pr`, `comment` or `labels` enforced | `push` enforced (H3; config still refuses `push`, so this column is tested but unreachable) |
  |---|---|---|---|
  | implementer | `contents: write`, `pull_requests: write`, R | `pull_requests: read` | `contents: read` |
  | tester | `contents: write`, `pull_requests: read`, R | none | `contents: read` |
  | reviewer | `contents: read`, `pull_requests: read`, R | none | none |
  | triager (scoper and integration included) | as reviewer | none | none |
  | publisher | `contents: read`, `pull_requests: write`, `issues: write`, `metadata: read` | none | `contents: write` |

  **Variant D10 (the spec as written):**

  | Role | Permissions, whatever is enforced |
  |---|---|
  | implementer | `contents: write`, `pull_requests: write`, R |
  | tester | `contents: write`, `pull_requests: read`, R |
  | reviewer | `contents: read`, `pull_requests: write` ("read plus review"), R |
  | triager | as HR12's reviewer |
  | publisher | as HR12 (D10 is silent, and H2 needs it) |

  - **Recommendation: HR12.**
    1. It matches today's cnref octo-sts policies exactly, so nothing widens. It also narrows step 2's shared writer, which gives the tester `pull_requests: write`.
    2. No code posts a GitHub review with a reviewer's token: verdicts go to the room and are posted by system components (`verdictpost`). D10's "plus review" has no consumer and would let a reviewer comment, label, or approve a human's pull request.
    3. D12's "an agent token can never approve" holds only while no agent token holds `pull_requests: write` on a pull request it did not author. Under HR12 that holds for the reviewer always, and for the implementer once `pr` is enforced.
    4. R2 can be computed from the table (PR8).
  - **Rejected: a config knob** (`runTokens.roles: d10|hr12`). A token's scope is a security property, not a setting. The owner's ruling is one reviewed line, and its docs table is generated from it (Task 2).
  - **Invariants, tested on every variant:**
    - keys come only from `{contents, pull_requests, issues, checks, actions, metadata}`;
    - access is only `read` or `write`;
    - `metadata: read` is always present;
    - only the publisher may hold `issues: write`;
    - no role holds `statuses`, `workflows`, `administration`, `checks: write` or `actions: write`.

- **PR3 (where the token lives: a per-run Secret the factory can write and never read).**
  - **The Secret:** the factory server-side-applies `<run name>-git` in the runs namespace.
    - Data: `{token}`.
    - Labels: `app.kubernetes.io/managed-by: maku-factory` and `maku.ogenki.io/run: <name>`.
    - Annotation: `maku.ogenki.io/expires-at`.
    - An `ownerReference` to the AgentRun (`blockOwnerDeletion: false`), so garbage collection removes the Secret with the run.
  - **The factory's Role** in the runs namespace gains `secrets: create, patch, delete`. It gets no `get`, `list` or `watch`, so the minter never reads a Secret back.
  - **Why the admission policy is required:** a patch or delete response returns the object, so `patch` on `agent-model` would leak the model key. A ValidatingAdmissionPolicy is therefore **rendered with the RBAC, never optional**. For the factory's ServiceAccount it allows Secret CREATE, UPDATE and DELETE only when:
    - the name matches `^<namePrefix>[a-z2-7]{8}-git$`;
    - the label is present (on DELETE: on `oldObject`);
    - an `AgentRun` ownerReference names `<name minus -git>`.

    A denied request returns no object.
  - **The run-controller** gets `secrets: get` in the runs namespace without `resourceNames` in `perRun` mode. It already creates pods that mount any Secret there, so this adds nothing.
  - **Alternatives weighed:**
    - **A token endpoint on the factory that runs call with their projected ServiceAccount token** (octo-sts-like): this makes the most privileged component reachable from every sandbox. Rejected.
    - **The run-controller mints:** the spec says the factory mints, and the publisher needs the same key in the factory. Rejected.
    - **`get` on all Secrets:** exposes `agent-model` until step 4 moves model keys to agentgateway. Rejected.

- **PR4 (which runs get a token: the binding).** `RunTokens` mints only for a non-terminal AgentRun of the portable kind (`maku.ogenki.io`) that the factory can account for:
  - a run carrying `agents.ogenki.io/task` must name a Task in the factory's namespace that is not terminal, whose `spec.repository` equals the run's `spec.repository` (case-insensitive);
  - a run with no Task label (an API run, `api/server.go:392`) must hold a reservation under its id in today's or yesterday's day ledger (R48), and carry `agents.ogenki.io/request-hash`.

  Anything else is refused with a bounded reason (`unbound_task`, `unbound_api`, `repository_mismatch`, `role_unknown`), logged once per run, and left Pending.

  The chart's `agentRunPolicies.creators` (step 2 Task 5) stays the outer guard. The umbrella fails a render that sets `maku-factory.runTokens.perRun` without it.
- **PR5 (expiry, refresh and revoke).**
  - **Lifetime:** installation tokens live one hour, and GitHub offers no shorter. Runs live up to 480 minutes (`budget.maxMinutes`).
  - **When the minter writes:**
    - `runs.Client` gains an optional `Created` hook, so the first token is applied right after the claim is created (the reconciler's and the API's path alike);
    - the leader's `RunTokens` loop, every 30 s from the cache, rewrites any token with less than 10 minutes left. The kubelet updates a Secret volume in place (no `subPath`), and the harness reads `GIT_TOKEN_FILE` at each call, so a long run keeps a live token;
    - a new leader keeps nothing in memory and mints a fresh token for every live run at start. That is a burst bounded by `caps.concurrentRuns`.
  - **Suspended runs:** a `Suspended` run is not refreshed. It is re-minted when its phase leaves `Suspended`.
  - **The run-controller's checks:** it refuses to start or restart a pod while the Secret is missing, carries no `token`, has the wrong owner UID or label, or expires within 5 minutes. The run stays Pending with reason `SecretMissing` or `SecretExpiring`.
  - **Revoke:**
    - the harness revokes the run's token on exit when `GIT_TOKEN_REVOCABLE=1` (set in `perRun` mode), after the checkpoint push;
    - at a terminal phase, the minter revokes the token it holds in memory (`DELETE /installation/token`, best effort) and deletes the Secret (NotFound is success).
  - **Residual:**
    - a replaced token is not revoked and lives at most 10 more minutes;
    - after a factory restart, the token of a run that ended meanwhile is not revoked and expires within the hour.

    Both go in `docs/security.md`.
- **PR6 (installation lookup and downscoping).**
  - **The lookup** is per repository: `GET /repos/{o}/{r}/installation` with the App JWT. The result is cached per lowercased `owner/repo` for one hour and evicted on a 404 from either call. Step 2's one-owner limit goes away in `perRun` mode.
  - **The mint:** `POST /app/installations/{id}/access_tokens` with `{"repositories": ["<name>"], "permissions": For(role, enforced)}`. The reply's `permissions` must equal the request, and its `repositories` must be exactly the one name. Otherwise the token is revoked and the mint fails with `scope_mismatch`.
  - **Failures:**
    - a 422 is `permission_not_granted` (the App's installation lacks a permission the table asks);
    - a 404 is `not_installed`.
  - **The metric:** `agent_factory_run_token_mints_total{role, result}`, with result one of `ok`, `not_installed`, `permission_not_granted`, `scope_mismatch`, `error`.
  - **GHES:** every URL is built on `githubAPIURL()`.
- **PR7 (the publisher from the minter, HR12).**
  - `minter.PublisherTokens` implements `applier.PublisherTokens`. Its `For(repo)` returns a cached `forge.TokenSource` that mints `Roles.For("publisher", EnforcedFor(repo))` from the agents App. It refreshes 10 minutes before expiry and `Invalidate`s on a 401.
  - **Config:** `hardening.publisher.minter: true` selects it. It needs `runTokens.enabled` (the agents App's key) and is refused together with `hardening.publisher.octoSTS.url`. `validateEnforce` accepts either source.
  - The publisher's login stays `agentsLogin` (HR11), since it is the same App.
- **PR8 (readiness R2 and R3 read the real token source).** `hardening.Settings` gains `TokenSource`:
  - `xr` (`runs.XR()`): R2 and R3 read the octo-sts policy files, as today.
  - `minter` (`runTokens.perRun`): R2 and R3 read `minter.Roles.For(role, EnforcedFor(repo))` for each sandbox role, and for the publisher when `hardening.publisher.minter`.
  - `shared` (anything else): R2 is missing, because the shared write token grants writes. R3 reads the publisher's source.

  `sandboxPermissionObserver` sets the `agent_factory_hardening_sandbox_permission` gauge from the same source. Under HR12, R2 still fails until H3, because the implementer and tester keep `contents: write`. That is the truth: R2 has always been H3-era.
- **PR9 (two Apps).**
  - **Config:**
    - `github.agentsAppIDFile` and `github.agentsKeyFile` name the agents App (both or neither). `runTokens.appIDFile` and `privateKeyFile` stay as aliases; both set and different is refused. The minter, `RunTokens` and the publisher read only the resolved pair.
    - When `github.mergerAppIDFile` equals `github.appIDFile`, the merger is folded into the factory App, and every repository's `merge.mergerLogin` must equal `factoryLogin`. Otherwise the gate reads its own merges as human merges (`merge.go:38`). The demo already satisfies this.
    - Under `runTokens.perRun`, outside the demo, `agentsLogin` must differ from `factoryLogin`, and the agents pair from the factory pair. That is D12 itself, and the push-identity binding (T14) needs it. It is refused only under the new key, so no existing config breaks.
  - **Chart** (`maku-factory`, mapped like step 2's PR8):

    | New key | Old key, still honoured | Rule |
    |---|---|---|
    | `apps.factory.existingSecret` | `githubSecret` | the new key wins when set |
    | `apps.agents.existingSecret` | `runTokens.appSecret` | the new key wins; both set and different fails the render |
    | `apps.merger.mode: separate` (default) or `factory` | `mergerSecret` | `factory`: no merger volume, and the chart writes `github.mergerAppIDFile` and `mergerKeyFile` as the factory App's files. Setting `mergerSecret` explicitly with `factory` fails |

    Each key that writes config fails when the user's `config:` already sets the same key, as `runTokens.enabled` does today.
  - **Permissions as data:** `internal/githubapps` exports `Factory(merge MergerOptions)` and `Agents()`, each App's required permission set:
    - the factory App is the union of `forge`'s factory set and `MergerPermissions(…)` in two-App mode;
    - the agents App is the union of `minter.Roles` over every role and every enforced combination, the publisher included.

    The docs table is tested against it, and step 7's manifest flow reads it.
  - **Security:** the factory pod now holds the agents App's key, which octo-sts alone held. T15's row says so.
- **PR10 (the merge gate's states).**
  - `statusFor` posts `success` only for `arm`, which is posted before the merge in the same pass, as today.
  - `shadow` becomes `pending` ("`<class>`, shadow: would merge; a maintainer merges"). With branch protection requiring the status, a `success` on a shadow pull request would let any writer, an agent token with `contents: write` included, merge what the gate handed to a human.
  - `merge.policyBot: false` is accepted outside the demo only with `merge.gateStatus: true`, and `policyBotLogin` may then be empty. The `allowedFiles` rule for live classes stays. A repository retiring policy-bot moves its `.policy.yml` gate list into each class's `conditions.deny.protectedFiles`.
  - **Defaults are unchanged (D25):** `gateStatus` false, and `policyBot` unset meaning true. A merger App without `statuses: write` would stop minting if the default flipped. `maku install` (step 7) writes `gateStatus: true, policyBot: false`, and step 8 flips the defaults.
- **PR11 (branch protection and pull requests that are not the factory's).**
  - **The documented ruleset** applies to the default branch:
    - `pull_request` with 0 required approvals;
    - `required_status_checks`: the repository's CI checks plus `{context: maku/merge-gate, integration_id: <the App that posts it>}`. That is the factory App in two-App mode; pinning the integration means no other App's status counts;
    - `non_fast_forward` and `deletion`;
    - bypass for repository admins only.
  - **Pull requests that are not tasks** get no status, so a maintainer bypasses, as `docs/factory.md` ("AwaitingHuman") already requires under today's ruleset. Posting the status on human-authored pull requests (policy-bot's `human-authored` rule) is an owner option listed under After this step, not built.
  - **The observer:** a fence-pass observer reads `GET /repos/{o}/{r}/rules/branches/{default}` with the factory App's token (metadata read). It sets `agent_factory_merge_gate_required{repository}` to 0 or 1, and logs a warning on each change while `gateStatus` is on. It does not refuse: the factory merges by its own decision, and the rule protects against everyone else. Classic branch protection is not read, since that needs `administration: read`; the docs say to use a ruleset.
- **PR12 (an agent merge is recorded).** Until H3 the implementer and tester tokens keep `contents: write`, which can merge a pull request whose required checks are green. That leaves a seconds-long window between the gate's `success` and the factory's own merge. In `awaitingCI`, a `MERGED` pull request whose `mergedBy` is `agentsLogin`:
  - is counted as outcome `agent_merged`;
  - is logged at error level;
  - otherwise ends like a human merge.

  This is a residual named in `docs/security.md`, closed at H3 under HR12.
- **PR13 (HR13: how a repository with `enforceOutputs` leaves the XR).** HR13 keeps such repositories on XR plus octo-sts until the minter applies HR12. After this step the exit is:
  1. Maku carries PR A to C, with `minter.Roles = TableHR12` per the owner's ruling.
  2. The values set `runTokens.enabled`, `runTokens.perRun`, `apps.agents`, `runController.gitTokens: perRun` and `hardening.publisher.minter: true`.
  3. The factory's HR13 startup warning is gone. It now fires only for `Enforcing() && !XR && !perRun`.
  4. The `sandbox_permission` gauge shows the implementer at `pull_requests: read` and the publisher at `pull_requests: write` for that repository.
  5. The step 2 cutover runbook flips `runs.group` (stop, drain, flip, lift). The flip is global, so every repository moves together.

  Under the D10 variant the implementer keeps `pull_requests: write`. Enforced repositories must then stay on the XR, and the plan's docs say so. The warning stays a warning: HR13 ruled out a refusal.
- **PR14 (octo-sts stays for the XR).** Nothing in `forge.OctoSTS`, `OctoSTSTokens`, the harness exchange or the readiness policy-file path is deleted. Step 8 deletes them once cnref runs only the product path.

---

## PR A: the minter core (D10)

### Task 1: `forge`'s App calls, exported and cached (M)

**Files:**
- Modify: `internal/factory/forge/app.go`:
  - `Installations`, a cache keyed by lowercased `owner/repo` with a TTL and an `Evict`;
  - `AppClient{API, HTTP, IDFile, KeyFile, Now}` with `Installation(ctx, owner, name)`, `AccessToken(ctx, id, repos, perms) (Token, granted map[string]string, repos []string, error)`, `Revoke(ctx, token)` and `Granted(ctx, id) (map[string]string, error)`;
  - `Mint` rebuilt on these, with the same behaviour;
  - the errors `ErrNotInstalled`, `ErrPermissionNotGranted` and `ErrScopeMismatch`.
- Modify: `internal/factory/forge/mint_test.go`.

- [ ] **Step 1: failing tests**, table-driven, against `httptest.NewTLSServer`:
  - **`AccessToken`:**
    - the request body is exactly `{"permissions":{…},"repositories":["r"]}` with `Bearer <jwt>`;
    - a reply whose `permissions` differ (say `contents: write` asked, `read` granted) gives `ErrScopeMismatch`, and the token is revoked: assert one `DELETE /installation/token` carrying it;
    - a reply with two repositories gives `ErrScopeMismatch`;
    - a 422 gives `ErrPermissionNotGranted`.
  - **`Installation`:**
    - a second call within the TTL makes no request;
    - a 404 gives `ErrNotInstalled` and evicts;
    - `Evict` forces a lookup.
  - **`Revoke`:** a 204 and a 401 (already gone) are both nil; a 500 is an error naming the status only.
  - **`Granted`** returns `GET /app/installations/{id}`'s `permissions`.
  - **GHES:** with the base `https://ghe.example/api/v3/`, every path is joined under it.
  - **No token text:** a body quoting the token never appears in any error (assert with `strings.Contains`).

  Run `go test -race -count=1 ./internal/factory/forge/ -run 'TestAppClient|TestInstallations|TestMint'`. Expected: FAIL (`undefined: AppClient`).
- [ ] **Step 2: implement.** `tokens` and `Mint` call the new methods. Existing `TestMint*` cases pass unchanged.
- [ ] **Step 3:** rerun. Expected: PASS.
- [ ] **Step 4: commit** `feat(forge): App installation lookup, downscoped mint checked against its reply, revoke and granted permissions`.

### Task 2: the role table (S)

**Files:**
- Create: `internal/factory/minter/roles.go`, `internal/factory/minter/roles_test.go`, `internal/factory/minter/doc.go` (the package comment cites D10, HR12 and PR2).
- Modify: `docs/factory.md` (a section "Run tokens: the role table", between `<!-- roles:begin -->` and `<!-- roles:end -->`).

- [ ] **Step 1: failing tests.**
  - **`TestTableHR12`**, one row per (role, enforced):
    - implementer with none → `contents: write`, `pull_requests: write`;
    - implementer with `[pr]`, `[comment]` or `[labels]` → `pull_requests: read`;
    - implementer with `[issue]` alone → unchanged;
    - implementer with `[push]` → `contents: read`;
    - tester with `[push]` → `contents: read`;
    - reviewer and triager have no `write` under any combination;
    - publisher with `[pr]` → `pull_requests: write`, `issues: write`, `contents: read`; with `[push]` → `contents: write`.
  - **`TestTableD10`:** the implementer is unchanged by enforcement, and the reviewer has `pull_requests: write`.
  - **`TestInvariants`**, over both tables and every subset of the five output types:
    - only the allowed keys and `read`/`write`;
    - `metadata: read` always;
    - `issues: write` only for the publisher;
    - never `statuses`, `workflows` or `administration`.
  - **`TestUnknownRole`:** `For("scoper", nil)` errors.
  - **`TestRolesIsAVariant`:** `Roles` deep-equals `TableD10` or `TableHR12`.
  - **`TestDocsTable`:** the markdown between the markers equals `Render(Roles)`; `-update` rewrites it.

  Run `go test -race -count=1 ./internal/factory/minter/`. Expected: FAIL (no package).
- [ ] **Step 2: implement** PR2's structure and both literals. `Roles = TableHR12` carries the comment `// The owner's ruling on HR12 (hardening plan) sets this line; TableD10 is the spec's D10 as written.`
- [ ] **Step 3:** rerun, with `-update` once for the docs section, then review the diff. Expected: PASS.
- [ ] **Step 4: commit** `feat(minter): one role table for run and publisher tokens, with the D10 and HR12 variants`.

### Task 3: the `Minter` (M)

**Files:**
- Create: `internal/factory/minter/minter.go`, `internal/factory/minter/minter_test.go`.
- Modify: `internal/factory/fmetrics/fmetrics.go` and its test (`RunTokenMint(ctx, role, result)`, the closed result set of PR6; `RunTokenRevoke(ctx, result)` with `ok` or `error`).

- [ ] **Step 1: failing tests** (a fake `AppClient` interface):
  - `Mint(ctx, "o/r", "implementer", [pr])` asks for exactly `Roles.For("implementer", [pr])`, on `["r"]`, through `Installation("o","r")`;
  - `ErrNotInstalled` evicts the installation, and the next mint looks it up again;
  - each result increments `agent_factory_run_token_mints_total` with the right `result`;
  - two owners mint through two installations;
  - `Revoke` counts `ok` or `error` and never returns the token in its error;
  - `CheckGranted(ctx, repos)` returns, per repository, the permissions `Roles` may ask that the installation does not grant (empty when complete).

  Run `go test -race -count=1 ./internal/factory/minter/ ./internal/factory/fmetrics/`. Expected: FAIL.
- [ ] **Step 2: implement.** `type Minter struct{ Client AppClient; Table Table; Metrics; Log }`, where `Table` is nil-defaulted to `Roles`.
- [ ] **Step 3:** rerun, then `task check`. Expected: PASS.
- [ ] **Step 4: commit** `feat(minter): mint a run's token for one repository and role`. Then open PR A, noting that nothing is reachable from config and that HR12 is pending the owner (PR2's recommendation).

## PR B: per-run tokens on the run-controller (D10)

### Task 4: the run-controller's `perRun` mode (M)

**Files:**
- Create: `internal/runctl/testdata/pod-perrun.golden.yaml`.
- Modify:
  - `internal/runctl/config.go`: `GitTokens string` (`shared` default, `perRun`); `GitSecretFor(r *v1alpha1.AgentRun)`; `perRun` with `gitReadSecret` or `unsafeSharedGitToken` is refused with "choose one";
  - `internal/runctl/pod.go`: the `github` volume from `GitSecretFor(r)`; `GIT_TOKEN_REVOCABLE=1` in `perRun`;
  - `internal/runctl/reconciler.go`: `inputs` checks the per-run Secret's `token`, its label, owner UID and `expires-at`;
  - `internal/runctl/config_test.go`, `internal/runctl/render_test.go`, `internal/runctl/envtest_test.go`;
  - `charts/maku/values.yaml` (`runController.gitTokens: shared`), `charts/maku/templates/run-controller-config.yaml`, `charts/maku/templates/run-controller-rbac.yaml` (`perRun`: `secrets: get` with no `resourceNames`; otherwise unchanged), `charts/maku/values.schema.json` (`gitTokens` enum), `charts/maku/tests/render.sh`.

- [ ] **Step 1: failing tests.**
  - **Golden:** an implementer pod in `perRun` mounts `<run>-git` and has `GIT_TOKEN_REVOCABLE=1`. The existing goldens are byte-identical in `shared` mode.
  - **envtest:**
    - with no Secret: Pending `SecretMissing`;
    - a Secret owned by another UID: Pending `SecretMissing` with the message `the run's token Secret is not the factory's`;
    - `expires-at` within 5 minutes: Pending `SecretExpiring`;
    - a fresh Secret: the pod is created;
    - a running pod is never deleted for a later expiry.
  - **Config:** `gitTokens: perRun` with `gitReadSecret` is refused.
  - **Render:** `--set runController.gitTokens=perRun` gives the Role with `secrets: [get]` and no `resourceNames`, and the config `gitTokens: perRun`. The default render is byte-identical to `origin/main`.

  Run `go test -race -count=1 ./internal/runctl/` (envtest with `KUBEBUILDER_ASSETS="$(go tool setup-envtest use -p path 1.36.x)"`) and `bash charts/maku/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun with `-update` for the new golden only, and review the diff. Expected: PASS.
- [ ] **Step 3: commit** `feat(runctl): a perRun token mode, each run mounting its own Secret, checked for owner and expiry`.

### Task 5: the harness revokes a per-run token on exit (S)

**Files:**
- Modify: `images/maku-harness/git_credential_agent.py` (`revoke()` revokes the file's token when `GIT_TOKEN_REVOCABLE=1`, under the same lock), `images/maku-harness/tests/test_git_credential_agent.py`.

- [ ] **Step 1: failing tests:**
  - with `GIT_TOKEN_FILE` and `GIT_TOKEN_REVOCABLE=1`, `revoke` sends one `DELETE /installation/token` with the file's token;
  - without the variable it returns without a call (the PAT case, unchanged);
  - a 401 is logged, not raised.

  Run `task harness:test`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(harness): revoke the run's minted token on the way out`.

### Task 6: the factory's per-run token writer (L)

**Files:**
- Create: `internal/factory/minter/runs.go` (`RunTokens`: a leader `manager.Runnable`, `Ensure(ctx, run)`, the binding of PR4, the server-side apply with field manager `maku-factory-minter`, revoke and delete at terminal), `internal/factory/minter/runs_test.go`.
- Modify:
  - `internal/factory/runs/runs.go` (`Client.Created func(context.Context, Run)`, called after a successful create and on a replay that finds the claim);
  - `internal/factory/runs/runs_test.go`;
  - `internal/app/factory.go` (`runTokens`: with `cfg.RunTokens.PerRun`, build `minter.RunTokens` instead of `runtoken.Writers`, and set `runs.Client.Created` to `Ensure`; the `oneOwner` check applies to `shared` only);
  - `internal/app/factory_test.go`.

- [ ] **Step 1: failing tests** (a fake client with an interceptor that fails the test on any `Get`/`List` of a Secret):
  - **A Task-bound implementer run** with `safe-outputs: pr`:
    - is minted with `Roles.For("implementer", [pr])`;
    - and applied as `<run>-git` with the token, the labels, `expires-at` and an ownerReference to the run's UID.
  - **An API run** with a ledger reservation is minted. Refused, each counted with its reason and never minted:
    - one without a reservation;
    - one whose Task names another repository;
    - one whose Task is terminal.
  - **Refresh:**
    - a token with 9 minutes left is re-minted and re-applied at the next tick;
    - a `Suspended` run is skipped and re-minted when it leaves `Suspended`.
  - **Terminal:** revoke is called with the in-memory token, and the Secret is deleted. A NotFound on delete is success. After a simulated restart (an empty memory), a live run gets a fresh mint and a terminal one only a delete.
  - **A mint failure** leaves no Secret change and retries the next tick.
  - **`Created` hook:** `runs.Client.Create` calls `Created` once per create, and once on a replay.

  Run `go test -race -count=1 ./internal/factory/minter/ ./internal/factory/runs/ ./internal/app/ -run 'RunTokens|Created|Token'`. Expected: FAIL.
- [ ] **Step 2: implement.**
  - The tick lists the cached AgentRuns of `cfg.Runs` (the manager already caches them).
  - `Ensure` and the tick share one per-run state map under a mutex.
  - `NeedLeaderElection` is true for the loop. `Ensure` may run on any replica: the apply is idempotent.
- [ ] **Step 3:** rerun, then `go vet ./...`. Expected: PASS.
- [ ] **Step 4: commit** `feat(minter): keep each live run's token Secret fresh, and revoke it when the run ends`.

### Task 7: `runTokens.perRun` in the config and the factory chart, with its admission policy (M)

**Files:**
- Create: `charts/maku-factory/templates/run-token-policy.yaml` (the ValidatingAdmissionPolicy and its binding, PR3), `charts/maku-factory/tests/admission/runtoken_test.go` (envtest, on the `charts/maku/tests/admission` pattern).
- Modify:
  - `internal/factory/config/config.go` (`RunTokens.PerRun`: needs `enabled` and the portable group; under it, outside the demo, `agentsLogin` must differ from `factoryLogin`; PR9) and `internal/factory/config/config_test.go`;
  - `charts/maku-factory/values.yaml` (`runTokens.perRun: false`), `charts/maku-factory/templates/configmap.yaml` (writes `perRun`), `charts/maku-factory/templates/rbac.yaml` (`perRun`: `secrets: [create, patch, delete]` with no `get`; otherwise today's rules), `charts/maku-factory/tests/render.sh`;
  - `charts/agent-factory/templates/_helpers.tpl` (umbrella rules: `maku-factory.runTokens.perRun` and `maku.runController.gitTokens: perRun` both or neither; `perRun` needs `maku.agentRunPolicies.creators`).

- [ ] **Step 1: failing tests.**
  - **Config:**
    - `perRun` without `enabled`, or with `runs.group: cloud.ogenki.io`, is refused;
    - `perRun` with `agentsLogin == factoryLogin` outside the demo is refused with `runTokens.perRun: agentsLogin and factoryLogin name one App; D12 needs two`.
  - **Render:**
    - `perRun` gives the Role verbs exactly `[create, patch, delete]`, no `resourceNames` rule, and the policy plus binding scoped by `namespaceSelector` to the runs namespace;
    - no `perRun` gives today's Role, and neither policy nor binding;
    - the umbrella fails `perRun` without `gitTokens: perRun`, and without `agentRunPolicies.creators`.
  - **envtest**, as `system:serviceaccount:<ns>:agent-factory`:
    - the apply of `maku-run-abcdefgh-git` with the label and an AgentRun ownerReference is allowed;
    - each of these is denied, with no object in the response: a patch of `agent-model`, a create without the label, a create owned by another name, a delete of `agent-model`;
    - a `get` is forbidden by RBAC.

  Run `go test -race -count=1 ./internal/factory/config/ -run PerRun`, `bash charts/maku-factory/tests/render.sh` and the envtest. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts,config): runTokens.perRun, with a Secret role that cannot read and the policy that bounds its writes`.

### Task 8: the demo on per-run tokens, the proof, the docs (M)

**Files:**
- Modify:
  - `charts/agent-factory/values-demo.yaml` (`maku-factory.runTokens.perRun: true`, `maku.runController.gitTokens: perRun`, `maku.agentRunPolicies.creators` and `factoryServiceAccount`);
  - `internal/factory/config/config.go` (the demo implies `runTokens.enabled` with the factory App, as `RunTokenApp` does; `perRun` accepted there);
  - `charts/agent-factory/tests/render.sh` (assert exactly the render changes: the Role verbs, the policy and binding, the two config keys);
  - `charts/agent-factory/tests/install.sh`;
  - `.github/workflows/demo-e2e.yaml` (the nightly);
  - `docs/factory.md` (run tokens: per run, replacing "interim"), `docs/security.md`, `charts/maku/README.md`, `charts/maku-factory/README.md`, `docs/upgrading.md`.

- [ ] **Step 1: write the assertions first.**
  - **`install.sh` k3d** (a dummy App): the first run stays Pending `SecretMissing`, and the factory logs `run token: mint failed` with `result=error` or `not_installed`.
  - **The nightly** (the real App): during the implementer run, `kubectl -n agents get secret <run>-git` exists with `maku.ogenki.io/expires-at`. The pull request is opened as today. Within 2 minutes of the run's end, the Secret is gone.

  Run `bash charts/agent-factory/tests/install.sh k3d` locally. Expected: FAIL (the demo still uses `agent-git`).
- [ ] **Step 2: implement and document.**
  - `docs/security.md` gains PR3's no-read design, PR5's residuals, PR12's residual, and the T15 row (the factory pod holds the agents App key).
  - `docs/upgrading.md` lists `runTokens.perRun` and `runController.gitTokens`, and that `shared` stays.
- [ ] **Step 3:** run `task check` and `task chart:install:k3d`. Expected: PASS.
- [ ] **Step 4: commit** `feat(demo): runs get per-run tokens; docs for the minter`. Then open PR B with PR3 to PR6 in the body and the strict-decoding note.

## PR C: the publisher, readiness and HR13 (D10, HR12)

### Task 9: `PublisherTokens` from the minter (S)

**Files:**
- Create: `internal/factory/minter/publisher.go`, `internal/factory/minter/publisher_test.go`.
- Modify:
  - `internal/factory/config/hardening.go` (`Publisher.Minter bool`; `validateEnforce` accepts `minter` or `octoSTS.url`, refuses both, and requires `runTokens.enabled` with `minter`) and `internal/factory/config/hardening_test.go`;
  - `internal/app/factory_hardening.go` (`safeOutputApplier`: `d.Tokens` is `minter.PublisherTokens` when `Publisher.Minter`, and the projected-token read is skipped);
  - `internal/app/factory_hardening_test.go`;
  - `charts/maku-factory/values.yaml` (comment only: `hardening.publisher.audience` is unused with the minter).

- [ ] **Step 1: failing tests.**
  - `For("o/r")` returns one cached source per repository, case-insensitive.
  - `Token` mints `Roles.For("publisher", EnforcedFor("o/r"))`, is cached until 10 minutes before expiry, and `Invalidate` forces a mint.
  - `TestEnforceOutputs` gains these cases:
    - `minter: true` without `octoSTS.url` validates;
    - both set is refused;
    - `minter` without `runTokens.enabled` is refused.
  - `safeOutputApplier` with `minter` builds without a token file.

  Run `go test -race -count=1 ./internal/factory/minter/ ./internal/factory/config/ ./internal/app/ -run 'Publisher|Enforce|Applier'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(minter): the publisher's token from the agents App, a second PublisherTokens beside octo-sts`.

### Task 10: readiness R2 and R3, and the permission gauge, from the token source (M)

**Files:**
- Modify:
  - `internal/hardening/readiness.go` (`Settings.TokenSource`; `Evaluator.Grants func(repo string) (roles map[string]map[string]string, publisher map[string]string)`, used for `minter`; `shared` makes R2 missing) and `internal/hardening/readiness_test.go`;
  - `internal/app/factory_hardening.go` (`sandboxPermissionObserver` and the readiness settings choose the source per PR8), `internal/app/factory_hardening_test.go`.

- [ ] **Step 1: failing tests.**
  - **XR:** the policy files are read exactly as today. The existing cases are unchanged.
  - **`minter` with HR12 and `[pr, comment, labels]`:**
    - R3 holds (the publisher has `pull_requests: write`);
    - R2 is missing (`sandbox_write_token`: `contents: write`);
    - the gauge shows implementer `pull_requests=read` and `contents=write`.
  - **`minter` with D10:** R2 is missing, and the gauge shows implementer `pull_requests=write`.
  - **`shared`:** R2 is missing whatever the policy files say.
  - No policy file is read under `minter` or `shared` (the fake counts reads).

  Run `go test -race -count=1 ./internal/hardening/ ./internal/app/ -run 'Readiness|Hardened|SandboxPermission'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(hardening): readiness reads the token source runs actually get`.

### Task 11: HR13's warning follows the minter; the exit runbook (S)

**Files:**
- Modify: `internal/factory/config/config.go` (the HR13 warning condition `Enforcing() && !Runs.XR() && !RunTokens.PerRun`; under `perRun` with `Roles` set to `TableD10`, a second warning: the implementer keeps `pull_requests: write`), `internal/factory/config/hardening_test.go` (`TestUnscopedTokenWarning` gains the `perRun` cases), `docs/operations.md` (PR13's sequence as a runbook), `docs/factory.md`.

- [ ] **Step 1: failing tests:**
  - `perRun` with HR12 gives no HR13 warning;
  - `shared` gives the warning as today;
  - `perRun` with D10 gives the D10 warning (swap `Roles` in the test through an unexported hook).

  Run `go test -race -count=1 ./internal/factory/config/ -run Unscoped`. Expected: FAIL.
- [ ] **Step 2:** implement and write the runbook. Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): an enforced repository may leave the XR once runs get per-run tokens (HR13)`. Then open PR C. Its body names HR12 and HR13 as the owner's decisions and PR2's recommendation.

## PR D: two Apps (D12)

### Task 12: the agents App in the config; the folded merger's logins (M)

**Files:**
- Modify: `internal/factory/config/config.go` (`GitHub.AgentsAppIDFile` and `AgentsKeyFile`; `RunTokenApp` resolves new over old; old and new differing is refused; a merger pair equal to the factory pair requires every `merge.mergerLogin == factoryLogin`), `internal/factory/config/config_test.go`, `internal/factory/config/demo_test.go` (asserting the demo still parses), `internal/app/factory.go` (the minter and the shared writer read `RunTokenApp`).

- [ ] **Step 1: failing tests.**
  - The agents pair alone works for `runTokens`.
  - The pair and `runTokens.appIDFile` both set and different is refused with `github.agentsAppIDFile and runTokens.appIDFile name different Apps`.
  - A merger pair equal to the factory pair with `mergerLogin: other[bot]` is refused with `merge.mergerLogin must be factoryLogin when the merger signs with the factory App`.
  - The demo parses unchanged.

  Run `go test -race -count=1 ./internal/factory/config/ -run 'Agents|Merger|Demo'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): name the agents App, and hold a folded merger to the factory App's login`.

### Task 13: the `apps` chart seam (M)

**Files:**
- Create: `charts/maku-factory/values.schema.json` (strict for `apps`, `runTokens` and `hardening`; `additionalProperties: true` at the top, per step 1's PR4).
- Modify: `charts/maku-factory/values.yaml` (`apps` with PR9's comment table), `charts/maku-factory/templates/_helpers.tpl` (`af.apps`: the resolved Secret names and the merger mode), `charts/maku-factory/templates/deployment.yaml` (mounts the agents App at `/etc/agent-factory-agents`; no merger volume in `factory` mode), `charts/maku-factory/templates/configmap.yaml` (writes `github.agents*`, and the merger files in `factory` mode; fails on a user-set duplicate), `charts/maku-factory/tests/render.sh`, `charts/agent-factory/values-demo.yaml` (`apps.merger.mode: factory` replaces the explicit `mergerAppIDFile`/`mergerKeyFile` lines), `charts/agent-factory/tests/render.sh`.

- [ ] **Step 1: failing render cases:**
  - `apps.agents.existingSecret=x` mounts `x`, and the config has `github.agentsAppIDFile: /etc/agent-factory-agents/app_id`;
  - `apps.agents.existingSecret` together with a different `runTokens.appSecret` fails;
  - `apps.merger.mode=factory` gives no `merger` volume, and the config's merger files are the factory's;
  - `factory` mode with `mergerSecret` set fails;
  - with no `apps` keys, the render is byte-identical to `origin/main`;
  - the demo's factory config is byte-identical to `origin/main`'s;
  - the schema rejects `apps.merger.mode: both`.

  Run `bash charts/maku-factory/tests/render.sh` and `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): apps.factory, apps.agents and apps.merger.mode, mapped onto the existing Secrets`.

### Task 14: the two Apps' permissions as data, the startup check, the docs (M)

**Files:**
- Create: `internal/githubapps/apps.go`, `internal/githubapps/apps_test.go`, `docs/github-apps.md`.
- Modify: `internal/app/factory.go` (at wiring, under `perRun`: `Minter.CheckGranted` over the served repositories; each missing permission is logged once and set on the gauge `agent_factory_github_app_permission_missing{app, permission}`; the factory App's installation is checked the same way against `githubapps.Factory`), `internal/factory/fmetrics/fmetrics.go` and its test, `docs/factory.md` (links), `charts/agent-factory/README.md`.

- [ ] **Step 1: failing tests.**
  - `Agents()` equals the union of `minter.Roles` over every role and every enforced subset. Under HR12 that is `contents: write`, `pull_requests: write`, `issues: write`, `checks: read`, `actions: read`, `metadata: read`.
  - `Factory(MergerOptions{Folded: true, Reruns: true, Labels: true, Status: true})` is `contents: write`, `issues: write`, `pull_requests: write`, `statuses: write`, `checks: read`, `actions: write`, `metadata: read`.
  - Neither App holds `workflows` or `administration`.
  - `TestDocs`: the two permission tables in `docs/github-apps.md` equal these.
  - The startup check with a fake installation lacking `issues: write` logs once and sets the gauge to 1.

  Run `go test -race -count=1 ./internal/githubapps/ ./internal/app/ -run 'Apps|Granted'`. Expected: FAIL.
- [ ] **Step 2: write `docs/github-apps.md`.**
  - Each App's role, its permissions (from the data), and where its key goes (`apps.*`).
  - Why the agents App has no `workflows`: GitHub then refuses an agent's push that touches `.github/workflows/`.
  - The optional third merger App (`apps.merger.mode: separate`) and what it buys: the merge key separated from the issue writer's.
  - policy-bot as an optional fourth App.
  - The factory App's webhook events are today's (link `docs/factory.md`).
  - "Created by `maku install` in rollout step 7".
- [ ] **Step 3:** rerun, then `task check`. Expected: PASS.
- [ ] **Step 4: commit** `feat(githubapps): the agents and factory Apps' permissions as data, checked against each installation`. Then open PR D with PR9 in the body.

## PR E: the merge gate (D11)

### Task 15: `policyBot: false` in production, with the gate's status (S)

**Files:**
- Modify: `internal/factory/config/config.go` (`:2020–2041`: `policyBot: false` outside the demo requires `gateStatus: true`; `policyBotLogin` may then be empty, in any profile), `internal/factory/config/demo_test.go` (named edit: the `"policyBot false"` case's message changes from "production gates through policy-bot" to the new rule), `internal/factory/config/config_test.go`, `docs/factory.md` (the `demo` rule table).

- [ ] **Step 1: failing tests:**
  - outside the demo, `policyBot: false` with `gateStatus: true` and `allowedFiles` on every live class validates, with no `policyBotLogin`;
  - the same without `gateStatus` is refused with `merge.policyBot: false needs merge.gateStatus: true: without policy-bot, maku/merge-gate is the status branch protection requires`;
  - a live class without `allowedFiles` is still refused.

  Run `go test -race -count=1 ./internal/factory/config/ -run 'PolicyBot|Demo'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(config): a repository may gate on maku/merge-gate without policy-bot`.

### Task 16: `success` only when the factory arms (S)

**Files:**
- Modify: `internal/factory/reconciler/gatestatus.go` (`shadow` posts `pending`, `<class>, shadow: would merge; a maintainer merges`), `internal/factory/reconciler/gatestatus_test.go` (named edit: the shadow row's state), `docs/quickstart.md` (step 4's wording), `docs/factory.md`.

- [ ] **Step 1: failing test.** `TestStatusFor`'s shadow row expects `pending`. A new case asserts that the arm row's status is posted before `Merge` is called, using the fake's call order.

  Run `go test -race -count=1 ./internal/factory/reconciler/ -run 'StatusFor|GateStatus'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun, then `go test -race -count=1 ./internal/factory/reconciler/`. Expected: PASS.
- [ ] **Step 3: commit** `fix(gate): maku/merge-gate is success only when the factory merges`.

### Task 17: the branch-rules observer (M)

**Files:**
- Create: `internal/factory/forge/rules.go`, `internal/factory/forge/rules_test.go` (`(*GitHub).BranchRules(ctx, branch)`: `GET repos/{o}/{r}/rules/branches/{b}`, keeping `required_status_checks` entries `{context, integration_id}`), `internal/app/gateguard.go`, `internal/app/gateguard_test.go` (a `fenceObserver`: for a repository with `gateStatus`, `GateRequired(rules, appID)`; it sets the gauge and logs a warning on change).
- Modify: `internal/factory/forge/forgetest/fake.go` (`BranchRules`), `internal/factory/fmetrics/fmetrics.go` and its test (`agent_factory_merge_gate_required{repository}`), `internal/app/factory.go` (chain the observer; the App id from the merger's id file, which is the factory App's when folded).

- [ ] **Step 1: failing tests.**
  - **`BranchRules`:** parses the rule list; a 404 (no rules) is an empty list.
  - **`GateRequired`:**
    - true for `{context: maku/merge-gate, integration_id: <id>}`;
    - false for the context with another or no `integration_id` (any App's status would count);
    - false for no rule;
    - false for `policy-bot: main` alone.
  - **The observer:**
    - a repository without `gateStatus` makes no call;
    - a flip from 1 to 0 logs `maku/merge-gate is not required from the App that posts it on <branch>: anyone with write access can merge an agents pull request` once;
    - an API error keeps the last value.

  Run `go test -race -count=1 ./internal/factory/forge/ ./internal/app/ -run 'BranchRules|GateRequired|GateGuard'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(gate): report whether branch protection requires maku/merge-gate from the factory App`.

### Task 18: an agent's merge is recorded (S)

**Files:**
- Modify: `internal/factory/reconciler/merge.go` (`awaitingCI`'s `MERGED` branch: `mergedBy == agentsLogin` gives outcome `agent_merged` and an error log), `internal/factory/fmetrics/fmetrics.go` (the outcome set gains `agent_merged`), `internal/factory/fmetrics/fmetrics_test.go`, `internal/factory/reconciler/merge_test.go`.

- [ ] **Step 1: failing tests:**
  - a pull request merged by `agentsLogin` counts `agent_merged`, not `human_merged`, and the task ends `Done`/`merged` as a human merge does;
  - a merge by a maintainer still counts `human_merged`.

  Run `go test -race -count=1 ./internal/factory/reconciler/ ./internal/factory/fmetrics/ -run 'Merged|Outcome'`. Expected: FAIL.
- [ ] **Step 2:** implement. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(gate): a merge by the agents App is counted and logged as agent_merged`.

### Task 19: branch protection docs, the gate (S)

**Files:**
- Modify: `docs/factory.md` (a new section, "Branch protection for the merge gate"), `docs/security.md`, `docs/architecture.md` (the Factory → GitHub row: two Apps), `docs/operations.md` (the gauge and the warning), `docs/roadmap.md` (rollout step 3 built), `README.md` (the merge-gate link, if its anchor moves).

- [ ] **Step 1: write.**
  - **The branch protection section** in `docs/factory.md`:
    - PR11's ruleset as JSON for `gh api repos/{o}/{r}/rulesets --input -`, with `integration_id` taken from the factory App's id;
    - what each rule is for, and what admins bypass;
    - why classic protection is not read.
  - **Retiring policy-bot, in order:**
    1. `gateStatus: true` with the App granted `statuses: write`;
    2. watch `maku/merge-gate` beside `policy-bot: main` for a week (`agent_factory_merge_decisions_total` per verdict);
    3. move the `.policy.yml` gate list into each class's `deny.protectedFiles`;
    4. add `maku/merge-gate` to the ruleset;
    5. remove `policy-bot: main` from it;
    6. set `policyBot: false`.
  - **policy-bot as an optional integration:** what it adds, namely rules for human-authored pull requests.
  - **`docs/security.md`:**
    - T15 (the factory pod holds the factory and agents keys);
    - D12's invariants, and the tests that hold them (Task 2's invariants);
    - PR12's residual.
- [ ] **Step 2:** run `task check`, then `task chart:install:k3d`. Expected: PASS.
- [ ] **Step 3: commit** `docs: the merge gate as a required status, and the two Apps`. Then open PR E. Its body lists PR10 to PR12 and says this is a v0.4x minor, not v4.

## After this step (not in these PRs)

- **Owner decisions:**
  - **HR12:** confirm `minter.Roles` (PR2 recommends `TableHR12`). Whichever variant is chosen, amend D10's text in the v4 spec and in ANR's pointer to it.
  - **D11's pull requests that are not tasks:** should the factory post `maku/merge-gate` on human-authored pull requests, policy-bot's `human-authored` rule (PR11)? That would be a later Maku plan.
  - **The demo's ruleset:** under per-run tokens no run holds `statuses: write`, so the demo could require `maku/merge-gate` (`hack/demo/lib.sh`, today "never maku/merge-gate", owner decision D2). The owner decides; nothing changes here.
- **ANR follow-ups** (each its own PR, after the matching Maku release; each recorded with a gcp-0 live check in the session ledger):
  1. **Per-run tokens for cnref, merged with step 2's switch off the XR** (HR13, PR13). This step's part:
     - bump the pin;
     - `apps.agents.existingSecret` from an ExternalSecret on the OpenBao `agents` mount's `github-app` key (the one octo-sts reads today);
     - `runTokens: {enabled: true, perRun: true}`, `runController.gitTokens: perRun`, `agentRunPolicies.creators` and `factoryServiceAccount`;
     - `hardening.publisher.minter: true`, and `hardening.publisher.audience` emptied.

     The repositories with `enforceOutputs` move with the rest once the HR12 table is confirmed. Live check:
     - an implementer's `<run>-git` exists and is gone after the run;
     - a reviewer token gets 403 on push;
     - on an enforced repository, `gh pr create` with the implementer token gets 403 and `output_pr` opens the PR as `ogenki-agents[bot]`;
     - the gauge shows `pull_requests=read`.
  2. **Switch cnref off octo-sts for the factory**, once no `cloud.ogenki.io` run is live and the publisher is on the minter. Delete:
     - `security/{aws-0,gcp-0}/octo-sts` and `security/base/octo-sts`;
     - `clusters/*-agent-platform/security-octo-sts.yaml` and their kustomization entries;
     - the agentgateway `sts` listener (`infrastructure/base/agent-gateway/sts.yaml`), the octo-sts audiences in `policies-identity.yaml` and the octo-sts rule in `network-policy.yaml`;
     - `.github/chainguard/agent-{implementer,reviewer,tester,triager}.sts.yaml` in cnref, ANR and the runlore repositories, after the last XR run;
     - the `oidc_*` octo-sts mention in `HOST-CONTRACT.md`.

     Step 2's "revisit the octo-sts audience" policies go with them.
  3. **Retire policy-bot per repository,** in Task 19's order:
     - `gateStatus: true` in `tooling/base/agent-factory/resources/mergepolicy-*.yaml`;
     - the merger App granted `statuses: write`;
     - the `.policy.yml` gate list moved into class `deny.protectedFiles` (cnref's list above);
     - the rulesets require `maku/merge-gate` with the posting App's `integration_id`, then drop `policy-bot: main`;
     - `policyBot: false`, and `policyBotLogin` removed.

     Then delete `tooling/base/policy-bot`, `security/*/merge-gate-secrets`, `.policy.yml`, `scripts/ci/check-policy-gate-coverage.sh` and the policy entries in the OpenBao policies, and uninstall `ogenki-merge-gate`.
  4. **Optionally fold the merger into the factory App:**
     - grant `ogenki-agent-factory` the union from `docs/github-apps.md`;
     - set `apps.merger.mode: factory` and `mergerLogin: ogenki-agent-factory[bot]` in every MergePolicy;
     - change the rulesets' `integration_id`;
     - uninstall `ogenki-agent-merger`.
- **Later Maku steps:**
  - **Step 4** removes `agent-model` from the runs namespace, which shrinks PR3's residual further.
  - **Step 7** creates both Apps from `internal/githubapps` through the manifest flow, and `maku preflight` reuses `CheckGranted`, the protection observer and the run-controller's Secret checks.
  - **Step 8:**
    - deletes `internal/factory/runtoken`, `runController.gitTokens: shared`, `gitReadSecret`, `unsafeSharedGitToken`, the `runTokens.appIDFile` alias, `githubSecret`, `mergerSecret` and `runTokens.appSecret`;
    - deletes octo-sts in `forge`, `applier` and the harness exchange, and the readiness policy-file path;
    - flips the defaults to `gateStatus: true`, `policyBot: false` and `apps.merger.mode: factory`.

## Self-review

- **D10:**
  - per run, per repository, per role:
    - the installation lookup, the downscoped mint checked against its reply, and revoke (Task 1, PR6);
    - one role table (Task 2, PR2);
    - the minter (Task 3);
  - the run's own Secret, written without read access and bounded by admission (Tasks 6 and 7, PR3), mounted and checked by the run-controller (Task 4);
  - expiry, refresh for long runs, suspended runs, leader change and revoke (Tasks 4–6, PR5);
  - only runs the factory started (PR4);
  - the publisher from the same minter (Task 9, PR7);
  - octo-sts optional and untouched for the XR (PR14).
- **HR12:**
  - one data structure with both variants and a recommendation (PR2);
  - R2 and R3 re-read against the table (Task 10, PR8).
- **HR13:**
  - the warning follows the real token source (Task 11);
  - the exit for enforced repositories (PR13), and the ANR follow-up 1.
- **D11:**
  - what exists is named (PR1);
  - `policyBot: false` in production (Task 15);
  - `success` only on arm (Task 16, PR10);
  - the ruleset, and the observer that it is in place (Tasks 17 and 19, PR11);
  - policy-bot optional, and its retirement order (Task 19, ANR follow-up 3).
- **D12:**
  - the two Apps' config and chart seams (Tasks 12 and 13, PR9);
  - the permissions as data for step 7, checked at startup (Task 14);
  - no agent token holds `statuses`, `workflows` or `administration` (Task 2's invariants);
  - the reviewer is read-only (HR12);
  - an agent merge is recorded, and its residual is named (Task 18, PR12).
- **D25:**
  - every new behaviour is opt-in, and old keys are honoured, with conflict errors;
  - default renders are byte-identical (Tasks 4, 7, 13);
  - the demo's render changes are listed exactly (Task 8);
  - no v4.
- **D26:** VAP v1, envtest 1.36; the GitHub endpoints exist on GHES 3.14.
- **Risks named with a stop rule:**
  - **A patch or delete response returning a Secret:** the policy always renders with the RBAC (Task 7). The envtest proves a denied patch returns nothing.
  - **Secret propagation lag:** a 10-minute refresh margin against the kubelet's sync of a minute or two, plus the run-controller's 5-minute `SecretExpiring`.
  - **Leader-change mint burst:** bounded by `caps.concurrentRuns`.
  - **The App missing a permission:** the startup check and the `permission_not_granted` result name it, and runs wait Pending.
  - **The nightly** goes red until the release (strict decoding).

