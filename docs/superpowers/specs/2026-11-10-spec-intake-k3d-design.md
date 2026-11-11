# Spec-intake k3d functional test: design

**Status:** approved approach (2026-11-10). **Scope:** a throwaway, self-contained k3d scenario for the
spec-intake workflow. It adds test infrastructure only, plus one deployment configuration seam for the
GitHub API root. No real GitHub writes, real model spend, or production credentials.

## Goal

Prove the merged spec-intake behavior in a real cluster, rather than only proving that the chart installs:

1. `intake_plan` returns a ranked gap with a marker and hash.
2. A candidate answer is accepted through the revision envelope and receives a new hash.
3. `plan_check` finishes deterministically, including accept-the-rest, with no unresolved marker filed.
4. A scoper bounce exposes its question, and `need_answer` edits only the issue body without relabeling.
5. One status comment is created and edited across task transitions and generations.

The scenario must clean up its cluster and all credentials, pass locally with Docker and k3d, and be
suitable for later CI execution.

## Decisions

| # | Decision | Rejected |
|---|---|---|
| D1 | **One purpose-built Go `e2e-stub` service** supplies every external dependency: JWKS, ZITADEL user/link reads, GitHub App installation/repository/user/permission APIs and issue comments, and model responses. | Real GitHub credentials/repositories: external writes and secret management. A real model: cost and nondeterminism. |
| D2 | **`ROOMS_GITHUB_API_URL` configures the GitHub API root**, defaulting to `https://api.github.com/`. Both broker and factory read it through the existing app wiring. | Hard-coded API root: prevents an in-cluster stub. A second production config file: unnecessary for one endpoint. |
| D3 | **OIDC mode, not local demo mode.** Local mode deliberately refuses `/mcp` and `human.intake.plan`. The test uses an RS256-signed access token and the stub's JWKS. | Weakening local-mode restrictions to make the test easier. |
| D4 | **No new production behavior beyond D2.** The functional scenario lives in test scripts/manifests and the stub. Existing chart defaults and production behavior remain unchanged. | Adding spec-intake behavior to the generic chart install test or changing the demo's security model. |
| D5 | **The test is opt-in**, exposed as `task chart:intake:k3d`, not added to the already long `task check`. It can later join the demo-e2e workflow. | Making every local `task check` pay for a multi-image cluster test. |

## Components

### `hack/e2e-stub`

A small Go HTTP server with bounded request/response sizes and table-driven tests. Its responses are
static or deterministic from the request; counters expose model calls for assertions.

- **OIDC:** `/oauth/v2/keys` serves the stub public JWKS. A test helper signs an access token with subject
  `dev`, client id `roomctl`, project audience, and the `agents-admin` group.
- **ZITADEL:** `GET /v2/users/dev` returns `USER_STATE_ACTIVE`; `POST /v2/users/dev/links/_search`
  returns the GitHub IdP link with numeric user id `123`.
- **GitHub App:** installation lookup and token mint return installation `1` and a throwaway token.
- **GitHub reads:** `GET /repos/owner/repo` returns `visibility: public`; `GET /user/123` returns login
  `dev`; `GET /repos/owner/repo/collaborators/dev/permission` returns `write`.
- **GitHub issues/comments:** a small in-memory issue store supports label events, labels, body reads,
  comment creation, full/recent comment reads, and edits. Every created comment has an ID and factory
  author. GraphQL is implemented only for the queries the scenario uses; unsupported queries fail closed.
- **Planner model:** returns one valid `plan/v1` draft with a single `g1` clarification marker and a
  recommended answer. It records calls; deterministic `plan_check` paths must not increase the count.
- **Scoper model:** returns a valid `room_handoff` scope in `question` state, followed by a valid `ready`
  scope on the next generation. Other roles receive the existing finish-tool response.

The server reads its TLS key pair from mounted files. The broker and factory trust the throwaway CA
through `SSL_CERT_FILE`, injected only by test values.

### `charts/agent-factory/tests/intake.sh`

A bash scenario following `install.sh` conventions: unique cluster name, private temporary kubeconfig,
image build/import, diagnostics on failure, and unconditional cluster deletion.

Test values configure:

- `maku.broker.human.mode=oidc`, issuer/JWKS/client/project/Origin/groups and `access.readerFile`;
- `maku.broker.human.mcp.enabled=true` and `human.intake.plan` with the stub route, API key file,
  `owner/repo` allowlist, one class, and `maxRounds: 2`;
- `ROOMS_GITHUB_API_URL=https://e2e-stub.e2e.svc.cluster.local/` for broker and factory;
- throwaway App ID/private key, images built from this checkout, and the stub service.

The script drives `/mcp` directly with the signed token. `issue_file` confirmation uses a scripted MCP
elicitation response, not a human or a real GitHub write.

## Scenario

### A. Ranked gap and deterministic revision

1. Start the cluster, stub, broker, run controller, and factory. Wait for all required readiness.
2. Call `intake_plan` with `text` and `repository: owner/repo`. Assert `g1`, exactly one marker, a
   recommendation, `round: 1`, and a `sha256:` hash.
3. Replace the marker with `task check`, append `D1 [g1]: run task check, because it verifies the change`,
   and call `intake_plan` with the original text, base plan+hash, and candidate. Assert a different valid
   hash and no remaining gaps.
4. Resolve a fresh plan through accept-the-rest using `plan_check`. Assert no additional model call, a new
   hash, and `canStart` consistent with the stub's write permission.
5. Attempt to file a body still containing a clarification marker through `issue_draft`; assert
   `unresolved_marker`. Confirm the final plan passes `plan_check`.

### B. Scoper question and `need_answer`

1. Create the issue through the stub and apply `factory/ready` as maintainer `dev`. The scoper returns a
   question. Assert `factory/needs-author` and a room `question` scope.
2. Call `room_brief` through `/mcp` and assert the structured question and recommendation.
3. Call `need_answer` with the issue and a scripted confirmation. Assert the appended `D<n>` record, no
   `factory/ready` added by the tool, and an explicit maintainer-next result.
4. Change the body or label state before confirmation in a second case; assert no write.

### C. One live status comment

1. Assert exactly one factory status comment on the issue across scoper/question and later ready states.
2. Assert its ID is stable while phase/waiting content changes, and event comments remain separate.
3. Start generation 2 with the ready scope. Assert the same comment is adopted and generation 1 cannot
   overwrite generation 2's content.

## Verification

- `go test -race -count=1 ./hack/e2e-stub` covers every stub route, static response, unsupported-query
  refusal, and model-call count.
- App config tests prove the API root defaults to `https://api.github.com/` and honors
  `ROOMS_GITHUB_API_URL` without logging credentials.
- Chart render tests prove the API-root and TLS environment are present only in the test values.
- `task chart:intake:k3d` must print `spec-intake k3d OK` and delete the cluster on success or failure.
- `task check` runs after all unit/render changes and must exit 0.

## Not in scope

- Real GitHub repositories, Apps, comments, labels, or pull requests.
- Real model calls or a new production model provider.
- Slack intake, factory metrics, or the deferred self-improvement loop.
- A general GraphQL implementation or a general-purpose GitHub/OIDC emulator.
