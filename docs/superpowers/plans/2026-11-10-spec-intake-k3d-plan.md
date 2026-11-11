# Spec-Intake k3d Functional Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove ranked intake revisions, `need_answer`, and generation-safe status comments in a self-contained k3d cluster with no real credentials or external writes.

**Architecture:** A purpose-built Go stub supplies OIDC, ZITADEL, GitHub, and model APIs. One deployment environment variable redirects existing GitHub clients to it. A scripted MCP client drives the broker and local relay in k3d, with assertions on model calls, issue bodies/labels, and comment IDs.

**Tech Stack:** Go, bash, k3d, Helm, Docker, MCP JSON-RPC, existing chart test conventions.

**Spec:** `docs/superpowers/specs/2026-11-10-spec-intake-k3d-design.md`

## Global Constraints

- Default GitHub API root remains `https://api.github.com/`; override only with `ROOMS_GITHUB_API_URL`.
- No real GitHub writes, production credentials, or real model calls. All keys/tokens are throwaway.
- Local mode remains unchanged and refuses MCP/planner; the test uses OIDC with RS256.
- `plan_check` and accept-the-rest make no additional planner model call.
- `need_answer` edits only the body and never labels; conflicts produce no write.
- Status comment IDs remain stable across transitions and generations; old generations cannot overwrite new ones.
- Test is opt-in as `task chart:intake:k3d`, not part of `task check`; cleanup is unconditional.
- Stdlib Go tests, `shellcheck`, no new production dependencies.

## Review Focus

- An unset or malformed API root must not redirect production silently or leak a credential: `TestGitHubAPIURL` in Task 1.
- A forged newer public comment from a non-factory author must not be accepted: `TestPublicBounceAuthor` in Task 3 and the `need_answer` conflict case in Task 6.
- A deterministic final check must leave the model-call counter unchanged: `TestDeterministicRevision` in Task 5.
- A body/label change between form and write must produce zero edits: `TestNeedAnswerConflict` in Task 6.
- Generation 2 must retain one comment ID while generation 1 stops writing: `TestStatusCommentGeneration` in Task 6.

---

### Task 1: GitHub API root and chart injection seams

**Files:**
- Modify: `internal/app/factory.go`, `internal/app/access.go`, `internal/app/verdicts.go`, `internal/app/factory_repos.go`
- Test: `internal/app/access_test.go`, `internal/app/factory_test.go`
- Modify: `charts/maku-factory/values.yaml`, `charts/maku-factory/templates/deployment.yaml`, `charts/maku-factory/tests/render.sh`

**Interfaces:**
- Produces: `func githubAPIURL() string`, returning `getenv("ROOMS_GITHUB_API_URL")` when non-empty, else `https://api.github.com/`.
- Factory chart gains optional `extraEnv`, `extraVolumes`, `extraVolumeMounts` with the same shapes as the maku broker chart; defaults render no new objects/env/volumes.

- [ ] **Step 1: Write failing configuration and render tests**

Add `TestGitHubAPIURL`: default, `https://e2e-stub.e2e.svc.cluster.local/` override, and an explicit invalid URL passed to the existing client validation. Add render cases proving the factory accepts test-only env/volumes and emits none by default.

```go
t.Setenv("ROOMS_GITHUB_API_URL", "https://e2e-stub.e2e.svc.cluster.local/")
if got := githubAPIURL(); got != "https://e2e-stub.e2e.svc.cluster.local/" { t.Fatalf("API root = %q", got) }
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/app -run TestGitHubAPIURL -count=1 && bash charts/maku-factory/tests/render.sh`
Expected: FAIL because the helper and factory injection values do not exist.

- [ ] **Step 3: Implement the minimal seam**

Replace app-wide constant reads with `githubAPIURL()` and validate through existing GitHub/forge clients. Add the optional chart values and render them exactly as the broker's existing injection pattern; do not alter default behavior.

- [ ] **Step 4: Run affected tests**

Run: `go test -race -count=1 ./internal/app && bash charts/maku-factory/tests/render.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app charts/maku-factory
git commit -m "test(factory): allow in-cluster GitHub and TLS stubs"
```

### Task 2: Stub OIDC and GitHub read APIs

**Files:**
- Create: `hack/e2e-stub/main.go`, `server.go`, `oidc.go`, `github.go`, `token.go`, `Dockerfile`
- Create: corresponding `_test.go` files

**Interfaces:**
- `New() *Stub`, `(*Stub).Handler() http.Handler`, `(*Stub).ModelCalls() int`.
- `/oauth/v2/keys`, `/token`, `/v2/users/dev`, `/v2/users/dev/links/_search`.
- GitHub installation/token routes plus `GET /repos/owner/repo`, `GET /user/123`, and collaborator permission.
- `/token` returns a signed token with `sub=dev`, `client_id=roomctl`, `aud=[project,roomctl]`, `groups=[agents-admin]`.

- [ ] **Step 1: Write failing route tests**

Use `httptest.NewTLSServer(Handler())` to assert JWKS/token claims, active user, GitHub link `123`, public visibility, login `dev`, and `write` permission. Assert wrong repositories and unsupported routes fail closed, and responses contain no private key material.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./hack/e2e-stub -run 'TestOIDC|TestGitHubReads' -count=1`
Expected: FAIL because the server/routes do not exist.

- [ ] **Step 3: Implement the bounded stub**

Generate or load one RSA key, serve JWKS, sign tokens, and return deterministic JSON. Add bounded bodies, ReadHeaderTimeout, and TLS startup from `E2E_STUB_TLS_CERT_FILE`/`E2E_STUB_TLS_KEY_FILE`. Keep the Dockerfile minimal and nonroot.

- [ ] **Step 4: Run stub tests**

Run: `go test -race -count=1 ./hack/e2e-stub`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add hack/e2e-stub
git commit -m "test(e2e): add OIDC and GitHub read stubs"
```

### Task 3: Issue store and planner/scoper model protocols

**Files:**
- Create: `hack/e2e-stub/issues.go`, `issues_test.go`, `model.go`, `model_test.go`

**Interfaces:**
- Concurrency-safe in-memory issue store with `Issue(number)`, `SetBody(number, body)`, `Comments(number)`, and label events.
- GitHub comment create/edit/list endpoints return stable IDs and factory author `maku-demo[bot]`.
- GraphQL supports only the scenario's issue snapshot/labels timeline queries and returns a descriptive error otherwise.
- `POST /v1/chat/completions`: planner returns one valid g1 plan; scoper alternates question then ready handoff; other roles receive finish. Separate planner/scoper call counters.

- [ ] **Step 1: Write failing store/model tests**

Cover labels/events, body edits, comment ID stability, recent-vs-all comments, factory-author filtering helper `TestPublicBounceAuthor`, GraphQL issue shape, valid planner marker, scoper question/ready sequence, and unchanged counter on non-model paths.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./hack/e2e-stub -run 'TestIssueStore|TestModel|TestPublicBounceAuthor' -count=1`
Expected: FAIL because the store/model routes are absent.

- [ ] **Step 3: Implement the deterministic protocols**

Protect all mutable store state with a mutex. Implement only required REST/GraphQL shapes and return errors for anything else. Use the existing finish-tool streaming shape for non-scoper roles and valid scope envelopes for question/ready handoffs.

- [ ] **Step 4: Run the complete stub suite**

Run: `go test -race -count=1 ./hack/e2e-stub`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add hack/e2e-stub
git commit -m "test(e2e): model issues and scoped model responses"
```

### Task 4: Scripted MCP client

**Files:**
- Create: `hack/e2e-stub/client/main.go`, `client.go`, `client_test.go`
- Create: `hack/e2e-stub/fakegh/main.go`, `fakegh_test.go`

**Interfaces:**
- `Call(ctx, name string, args any) (json.RawMessage, error)` supports broker HTTP `/mcp` and local `maku mcp` stdio.
- Subcommands: `plan`, `revise`, `accept-rest`, `answer`, `status`.
- `fakegh` implements only the test's `gh issue create/view/edit` forms, forwards them to the stub, prints the issue URL on create and GitHub-shaped JSON on view, and never calls GitHub.
- Elicitation responses are scripted: final plan accepts file but not start; need_answer accepts `answer`/`send`; conflict cases answer the same way after the fixture changes state.

- [ ] **Step 1: Write failing client and fake-GH tests**

Use an `httptest` MCP broker and a fake stdio relay to verify initialize, strict tool calls, structuredContent decoding, elicitation responses, new-hash capture, and error propagation. Test `fakegh` create/view/edit against the stub issue store, including body-on-stdin, label state, and `updatedAt`; assert it cannot reach a real GitHub endpoint.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./hack/e2e-stub/client -count=1`
Expected: FAIL because the client does not exist.

- [ ] **Step 3: Implement the MCP client and fake `gh`**

Use bounded JSON lines and context deadlines. The relay client forwards broker calls, serves local `need_answer`, and answers elicitation deterministically. Keep subcommand output machine-readable (`key=value` lines) so the shell scenario can assert hashes, question text, comment IDs, and model counts. Install `fakegh` first on the scenario's temporary PATH so the real `issue_file` and `need_answer` run against the stub without modification.

- [ ] **Step 4: Run client and stub tests**

Run: `go test -race -count=1 ./hack/e2e-stub/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add hack/e2e-stub/client hack/e2e-stub/fakegh
git commit -m "test(e2e): script MCP flows and stub gh writes"
```

### Task 5: k3d cluster scenario and planner proof

**Files:**
- Create: `charts/agent-factory/tests/intake.sh`, `charts/agent-factory/tests/intake-values.yaml`, `charts/agent-factory/tests/e2e-stub.yaml`
- Modify: `taskfile.yaml`, `charts/agent-factory/README.md`
- Modify: `hack/e2e-stub/server.go` (test-only `GET /__counts` returning total/planner/scoper model calls)

**Interfaces:**
- `task chart:intake:k3d` builds/loads images, creates `maku-intake-k3d`, installs the stub and chart, and always deletes it.
- `TestDeterministicRevision` is the scenario's planner phase: g1/hash → candidate/new hash → accept-rest with unchanged model counter → final-marker refusal.

- [ ] **Step 1: Add the failing scenario skeleton**

Write `intake.sh` with readiness, planner calls, and assertions before implementing its supporting values/manifests. Run it against the existing chart to capture the expected failure: local mode refuses MCP/planner and no stub exists.

- [ ] **Step 2: Implement test values and stub deployment**

Configure OIDC/access/MCP/planner, API root, throwaway TLS and secrets, `SSL_CERT_FILE`, and no default production changes. Deploy the stub with readiness checks and a NetworkPolicy permitting required broker/factory traffic.

- [ ] **Step 3: Implement the planner scenario**

Run the client's `plan`, `revise`, and `accept-rest` phases through port-forward. Assert the marker, old/new hashes, no gaps, no extra planner call, and `unresolved_marker` from `issue_draft`. Read `GET /__counts` before and after accept-rest to prove the planner model-call counter did not increase.

- [ ] **Step 4: Run the planner k3d proof**

Run: `task chart:intake:k3d`
Expected: PASS through the planner phase; later factory/status phases may remain marked pending until Task 6. No cluster remains.

- [ ] **Step 5: Commit**

```bash
git add charts/agent-factory/tests taskfile.yaml
git commit -m "test(chart): prove intake revisions on k3d"
```

### Task 6: Scoper bounce, need_answer, status generations, and final gate

**Files:**
- Modify: `charts/agent-factory/tests/intake.sh`
- Modify: `charts/agent-factory/README.md`, `docs/quickstart.md`, `.github/workflows/demo-e2e.yaml`
- Modify: `hack/e2e-stub/server_test.go` (counter route unit coverage)
- Modify: `hack/names.sh` (allow the intentional legacy OIDC client ID `roomctl`)

**Interfaces:**
- `TestNeedAnswerConflict`: body/label mutation produces zero writes; success appends D<n> and does not add ready.
- `TestStatusCommentGeneration`: one stable comment ID across scoper question/ready and generation 2; old generation cannot overwrite.

- [x] **Step 1: Add failing scoper/status assertions**

Create maintainer-labelled issues through `fakegh`, wait for needs-author, call room_brief/need_answer, and track status comment IDs. Include success, conflict, and second-generation cases. Run to capture the expected failure before wiring scoper/status responses.

- [x] **Step 2: Complete the scoper/status flow**

Use the stub's question then ready handoffs. Assert structured room_brief question, body-only Decision append, no tool-applied ready label, conflict refusal, and stable status ID with separate event comments. Assert the old generation's content is not overwritten.

- [x] **Step 3: Document and optionally schedule the test**

Document `task chart:intake:k3d` prerequisites, no-external-write guarantee, and cleanup. Add it to the PR demo-e2e workflow only if runtime is acceptable; otherwise retain opt-in local coverage and state why.

- [x] **Step 4: Run the full scenario and repository gate**

Run: `task chart:intake:k3d && task check`
Expected: `spec-intake k3d OK`; `task check` exit 0; no cluster or temporary credentials remain.

- [ ] **Step 5: Commit**

```bash
git add charts/agent-factory docs .github/workflows taskfile.yaml hack/e2e-stub
git commit -m "test(chart): prove scoper answers and status generations on k3d"
```
