# v4 rollout step 5, identity and data: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a platform team wires identity, the database and the credentials with what it already runs, and with no component chosen by cloud-native-ref (cnref):
- **D8, identity.** The broker and the factory accept any OIDC issuer. The roles claim name and its values are configuration. Each person links their GitHub account once, in the broker, through a GitHub OAuth flow. The link replaces ZITADEL's IdP link and the `human.access.readerFile` / `api.access.readerFile` PAT that reads it. ZITADEL becomes a preset (`human.preset: zitadel`) that keeps today's token rules.
- **D9, data.** Bring your own Postgres ≥16. There is one DSN Secret per database role (`rooms_owner`, `rooms_broker`, `rooms_retention`). Maku runs its migrations at start-up, in the broker's existing `migrate` init container. The broker refuses a server below 16, and refuses a role that would bypass row-level security. A database migrated by the Atlas operator (cnref) is adopted without re-applying anything. The chart renders the retention CronJob. The bundled Postgres StatefulSet stays demo-only, behind `postgres.enabled`.
- **D14, Secrets by name.** Every credential is an `existingSecret` with documented keys, listed in one registry that the docs and step 7's `maku preflight` read. An optional `externalSecrets.enabled` renders an ExternalSecret for each listed seam against a store the user names.

Every existing install keeps working (D25):
- a `human` block with no `preset` is the ZITADEL deployment it is today, byte for byte;
- `human.access.readerFile` and `api.access.readerFile` keep working. When a link source is also set, the link is read first and the reader is the fallback, so cnref moves without an outage. Step 8 deletes the reader;
- `broker.database.brokerSecret` / `ownerSecret` keep their meaning and their key `uri`;
- `externalSecrets.enabled`, `postgres.enabled: false`, `broker.database.retentionSecret` and `broker.githubLink` are all opt-in. The umbrella's default and demo renders do not change.

**Architecture:**
- **`internal/authn` gets generic.**
  - **The roles claim** becomes a configured name, or an RFC 6901 pointer when it starts with `/`. Its value may be an array of strings, an object (its keys, as ZITADEL's), or a single string.
  - **The client a token was issued to** is read from `azp`, then `client_id`, then a single-element `aud`.
  - **`Humans` serves two token uses:**
    - `access` (the ZITADEL preset, and any IdP that issues JWT access tokens with an audience): today's ID-plus-access pair for the web, and maku's access token as a bearer;
    - `id` (the generic default): the ID token is the credential. Its `aud` names the web or the CLI client. The broker forwards it to the factory as the human's proof (C4).
- **Discovery:** `jwksURL` becomes optional (it is read from `<issuer>/.well-known/openid-configuration`). `/.well-known/roomctl` gains `preset`, `tokenUse`, `scopes` and `audience`. `maku login` reads the device and token endpoints from the issuer's discovery instead of ZITADEL's fixed paths. The Slack App's client-credentials call does the same.
- **The GitHub account link** lives in the broker:
  - three tables: `github_links`, `github_link_states` and the append-only `github_link_events`;
  - a no-script page, `/link/github`, in the style of `/link/slack`;
  - an OAuth authorization-code flow with PKCE, through the factory App's OAuth client. Its callback only exchanges the code. The link is written by a same-origin confirm POST from the signed-in web session.
  - The broker stores the numeric GitHub id, never a token. `ghidentity.Resolver` reads links from the store in the broker. The factory reads them through a new system route on :8443, `GET /v1/links/github?sub=`, as it already reads `GET /v1/links/slack`.
- **`internal/store`** checks the server version, and the roles of the connection (refusing a superuser, `BYPASSRLS`, or membership in `rooms_owner`), in `migrate`, `serve` and `retention`. `migrate` adopts an Atlas operator ledger into `room_broker_revisions`.
- **The charts:**
  - the maku chart: a DSN key and a CA per role, the retention CronJob, `broker.oidc` and `broker.githubLink` mounts that fill the config's file paths;
  - the umbrella: `postgres.enabled`, `externalSecrets`, and the seam registry template;
  - `internal/install/secrets.go`: the same registry as Go data.

**Tech Stack:**
- Go: golang-jwt v5, `golang.org/x/oauth2` (device grant, client credentials, authorization code with PKCE), pgx v5 and the existing migration runner;
- the GitHub OAuth web flow (`/login/oauth/authorize`, `/login/oauth/access_token`, `GET /user`, `DELETE /applications/{client_id}/token`) on github.com and GHES ≥3.14;
- Postgres 16, 17 and 18 (testcontainers in `internal/store`, which needs Docker);
- External Secrets Operator `external-secrets.io/v1` (ESO ≥0.17);
- Helm 3.15, JSON Schema, the bash render tests (`yq`);
- k3d with the e2e stub as a generic OIDC issuer and a fake GitHub OAuth server;
- Dex and Keycloak in a throwaway k3d for the probe only.

**Spec:** `docs/superpowers/specs/2026-11-12-v4-production-path-design.md`:
- D8 (any OIDC provider, the account link);
- D9 (BYO Postgres ≥16);
- D14 (Secrets by name, optional ESO);
- D25 (additive);
- D26 (support floor: Postgres ≥16, GHES ≥3.14);
- "Rollout", step 5;
- the 2026-11-12 owner rulings (Release: a minor is cut per step, after the step's last PR).

**Read against:**
- Maku `origin/main` `f8ee9140`. Steps 1 and 2 (PR A to C) are merged, and so are step 3 PR A and PR E, and hardening H1, H2a, H2b and H3a. Step 3 PR B to D and all of step 4 are not merged. PR E below names the one seam it shares with step 4 (the gateway's provider Secrets).
- ANR `origin/main` `1529b3b`.

**Branch:** five PRs, each from `origin/main` once the previous one merges (D may run in parallel with A to C).

| PR | Branch | Tasks | Size | Order |
|---|---|---|---|---|
| A, any OIDC issuer: verification (D8) | `feat/v4-identity-oidc` | 1–5 | L | first |
| B, any OIDC issuer: the clients and the chart (D8) | `feat/v4-identity-clients` | 6–8 | M | second; needs A |
| C, the GitHub account link (D8) | `feat/v4-identity-github-link` | 9–13 | L | third; needs A (the web-session rule) |
| D, BYO Postgres (D9) | `feat/v4-data-postgres` | 14–16 | M | any time; independent of A to C |
| E, Secrets by name and ESO (D14) | `feat/v4-secrets-by-name` | 17–19 | M | last: it lists the Secrets A to D add |

The step's v0.4x minor is cut after PR E. PR A to D may also ship in interim minors.

## Global Constraints

- **No v4.** This step ships v0.4x minors only: no v4 tag, release or chart version.
- **Additive (D25).**
  - Every new behaviour is opt-in: `human.preset`, `human.tokenUse`, `human.rolesClaim`, `human.githubLink`, `api.access.links: broker`, `api.tokenUse`, `api.rolesClaim`, `broker.database.retentionSecret`, `broker.database.secretKey`, `broker.database.caSecret`, `broker.oidc`, `broker.githubLink`, `postgres.enabled: false`, `externalSecrets.enabled`.
  - Default renders stay byte-identical. Prove it with a render diff against `origin/main` for the umbrella demo, the maku quickstart, `runController.enabled=false`, and the factory chart's default.
  - Old keys keep working. Where an old and a new key conflict, the render or the config fails with a message naming both.
- **Fail closed.**
  - A token whose roles claim is missing, malformed or over the bounds holds no group, and is refused as "not in an agents group". It is never admitted as a member.
  - A link read that fails is `link_unverified` (503), never "no link" and never a guess.
  - A database role that bypasses row-level security stops the broker before it serves.
  - A migration ledger that cannot be adopted exactly is refused with nothing applied.
- **No credential in a log, a metric, an error, an event or the database:**
  - no OAuth code, `state` or PKCE verifier (stored as a SHA-256 only);
  - no GitHub user token (revoked as soon as `GET /user` has answered);
  - no client secret, DSN or password.

  Errors name the HTTP status, or the Postgres SQLSTATE, only.
- **Bounded vocabularies.** Link outcomes, roles-claim refusals and database check results are closed sets.
- **Strict decoding.** The broker, factory and Slack App configs are decoded strictly, so chart and binary change in the same PR. The demo-e2e nightly runs released images against the checkout's chart and goes red until the release. Name this in each PR body.
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
  - `task crd:gen && task crd:check` after an API type or doc-comment change (none is planned);
  - `task chart:check` after a chart change;
  - `task check` before the last commit of each PR.

## Rulings made while planning

- **PR1 (what exists today; this step builds only what is missing).**

  | Spec item | Today on `origin/main` (Maku) and on cnref (ANR) | Missing, built here |
  |---|---|---|
  | **D8: human tokens** | `authn.Humans` (`internal/authn/humans.go`) is ZITADEL-shaped. A token's `aud` must hold the project id (`human.projectIDFile`) and the client, and `azp` (ID token) or `client_id` (access token) names the client. A web session is oauth2-proxy's ID token in `Authorization` plus a distinct JWT access token in `X-Forwarded-Access-Token`, with the same `sub`. maku's own access token is a bearer, told apart by `client_id` (P18). `Claims.GroupNames()` (`jwt.go:77–90`) reads `groups`, else the keys of `urn:zitadel:iam:org:project:roles`. `human.jwksURL` is required. The factory's `api.Authenticator` (`internal/factory/api/authn.go`) calls `VerifyAuthorizedParty` per client id and has the same `GroupNames` | A configured roles claim, token use `id`, `aud`-based client resolution, discovery, and presets (Tasks 2–5) |
  | **D8: sponsored principals** | `authn.Sponsored` admits a ZITADEL service account's access token. The client is `azp`/`client_id`, and the project id is required in `aud`. The Slack App mints through `clientcredentials` at `<issuer>/oauth/v2/token` with scope `urn:zitadel:iam:org:project:id:<project>:aud` (`internal/app/slackapp.go:247–250`) | A configured client claim and audience, the token endpoint from discovery, and configured scopes and audience (Tasks 5 and 7) |
  | **D8: the CLI** | `maku login` (`internal/maku/auth.go`) runs the device grant at `<issuer>/oauth/v2/device_authorization` and `/oauth/v2/token`, with the ZITADEL project scope. It stores and sends the access token. `/.well-known/roomctl` serves `issuer`, `clientID` and `projectID` | Endpoints from discovery, the ID token in `id` mode, and the discovery fields (Task 6) |
  | **D8: human → GitHub login** | `ghidentity.Resolver` (`internal/ghidentity/ghidentity.go`). `Links` lists the ZITADEL user's IdP links through the reader PAT (`{"pat", "githubIdpId"}`, `internal/app/access.go:140–193`) and keeps the numeric GitHub id of the GitHub IdP link. `LoginOf` turns it into a login through the factory App. `Active` asks ZITADEL whether the user is active (#126). The broker (`human.access`) and the factory (`api.access`, `internal/app/factory_repos.go:268–285`) each hold a reader copy. On cnref: `externalsecret-zitadel-reader.yaml` in `infrastructure/base/room-broker` and `tooling/base/agent-factory`, written by cnref's `zitadel-idp.sh` | The link in the broker's database, its flow, the factory's read of it, and an `Active` that needs no IdP admin API (Tasks 9–13) |
  | **D8: Slack identity** | `/link/slack` links a Slack user to a `sub` (`slack_links`; "No GitHub login is stored"). The factory reads `GET /v1/links/slack` on :8443 as `system:factory`, then the same ZITADEL resolver (`internal/factory/api/tasks.go:302–331`). `slackDecide` resolves the linked human the same way | Nothing new in the Slack flow: its `sub` → login step reads the GitHub link (Task 13) |
  | **D8: the web front door** | ANR's `rooms-oauth2-proxy` (`infrastructure/base/room-broker/oauth2-proxy.yaml`) runs `provider: oidc`, `pass-access-token`, `skip-jwt-bearer-tokens` with `EXTRA_JWT_ISSUERS`, the ZITADEL project scope, `allowed-group` and `cookie-samesite: strict`. No Maku chart renders it | Documented settings per preset (Task 8). The subchart is step 6's (PR11) |
  | **D9: migrations** | `maku-broker migrate` (`internal/store/migrate.go`) applies the embedded Atlas-format files as `rooms_owner` only, under an advisory lock, and records them in `room_broker_revisions`. The maku chart runs it as the init container `migrate` when `broker.database.ownerSecret` is set. On cnref the broker has no owner Secret: the Atlas operator applies the same directory from `SQLInstance.spec.atlasSchema` (`ref: v0.45.0`) into its own ledger | Version and role checks, Atlas adoption (Tasks 14–15) |
  | **D9: roles and DSNs** | The migrations GRANT to `rooms_broker` and `rooms_retention`, which must already exist. The demo's `rooms-db-init` creates the three roles and the database. The umbrella renders `rooms-broker-dsn` and `rooms-owner-dsn` (key `uri`), with no retention DSN and no CronJob. cnref runs `retention-cronjob.yaml` against `xplane-rooms-cnpg-role-rooms-retention` | A retention DSN and the CronJob, the DSN key and CA, an external database in the umbrella, and the bootstrap docs (Task 16) |
  | **D9: "the factory reads DSN Secrets"** | The factory has no database: no `pgx` import under `internal/factory` or `cmd/maku-factory` | Nothing. PR6 corrects the spec's sentence |
  | **D14** | Secret names are scattered across values: `githubSecret`, `mergerSecret`, `oidcSecret`, `zitadelReaderSecret`, `sponsoredSecret`, `intakeSecret`, `brokerCASecret`, `broker.database.*`, `broker.tls.*`, `runController.modelSecret`, and `demo.*.existingSecret`. No registry. Every ExternalSecret is ANR's, hand-written, against the namespaced `SecretStore agents-secrets` | The registry, the `externalSecrets` rendering, and startup messages that name the Secret and the key (Tasks 17–19) |

- **PR2 (the OIDC config shape: three presets over one set of keys).** New keys on the broker's `human` block. All are optional, and the old keys keep their meaning.

  ```yaml
  human:
    mode: oidc
    preset: generic          # "" = zitadel (today's bytes) | zitadel | generic
    issuer: https://idp.example.com
    jwksURL: ""              # optional now: <issuer>/.well-known/openid-configuration's jwks_uri
    clientIDFile: /etc/room-broker/oidc/web-client-id
    roomctlClientIDFile: /etc/room-broker/oidc/cli-client-id
    tokenUse: id             # id | access (preset default: generic id, zitadel access)
    audience: ""             # tokenUse access only: an aud value every human token must hold
    audienceFile: ""         #   or read at use from a file (zitadel: projectIDFile is this)
    rolesClaim: groups       # a claim name, or an RFC 6901 pointer when it starts with "/"
    scopes: [groups]         # added to openid profile email offline_access for maku login
    groups: {admin: agents-admin, member: agents-member}   # the roles claim's values, unchanged
  ```

  | Preset | `tokenUse` | Audience | Roles | Client claim | Extra scopes |
  |---|---|---|---|---|---|
  | `zitadel` (and `""`) | `access` | `projectIDFile` (required) | `groups`, else the keys of `urn:zitadel:iam:org:project:roles` (today's `GroupNames`) | `azp`, else `client_id` | `urn:zitadel:iam:org:project:id:<project>:aud` |
  | `generic`, `tokenUse: id` (default) | `id` | the client ids themselves (OIDC Core: an ID token's `aud` holds its client) | `rolesClaim`, required | `azp`, else `client_id`, else a one-element `aud` | `scopes` |
  | `generic`, `tokenUse: access` | `access` | `audience` or `audienceFile`, required | `rolesClaim`, required | the same | `scopes`, plus `audience` sent as a parameter (Auth0) |

  - **The roles claim's value:**
    - an array of strings;
    - an object, whose keys are the names (ZITADEL);
    - or one string (Entra's single-valued `roles` is still an array; one string covers Authentik's mappers).

    At most 256 names of at most 256 bytes each. Any other shape, or a value over the bounds, gives no groups and counts `rooms_human_roles_claim_refused_total{reason}`, with `reason` one of `missing`, `type` and `bounds`.
  - **Examples in the docs** (Task 8):
    - Keycloak `/realm_access/roles`;
    - Okta `groups`;
    - Entra `roles` (app roles; `groups` carries GUIDs, which also work as `groups.admin` values);
    - Auth0 `https://<ns>/roles` (no leading `/`, so a name);
    - Dex `groups`;
    - Authentik `groups`.

    Each is marked probed (Task 1) or documented-only.
  - **The factory** gains the same four keys under `api`: `preset`, `tokenUse`, `audience`/`audienceFile` and `rolesClaim`. The umbrella fails a render where `maku.broker.human.{preset, issuer, tokenUse, rolesClaim, groups}` and `maku-factory.config.api.{preset, humanIssuer, tokenUse, rolesClaim, groups}` disagree, naming both keys.
  - **Rejected:**
    - **A JMESPath or CEL claim mapping:** a language for one lookup. A pointer reaches every IdP in the table.
    - **Fetching userinfo for opaque access tokens (Google):** an IdP call on every request. `tokenUse: id` covers those IdPs, because their ID tokens are JWTs.

- **PR3 (`tokenUse: id`: the ID token is the credential, and P18 still holds).**
  - **A web session:** oauth2-proxy's ID token in `Authorization`, with `aud` holding the web client, and a non-empty `X-Forwarded-Access-Token` that differs from it. The access token is not parsed: it may be opaque. Its only role is P18's rule that a bearer passed through `skip-jwt-bearer-tokens` (both headers equal) is never a web session.
  - **The CLI:** its ID token as the bearer, with `aud` holding the CLI client. oauth2-proxy admits it through `EXTRA_JWT_ISSUERS=<issuer>=<cli client id>`.
  - **What is forwarded to the factory:** the verified ID token, in `Principal.AccessToken` (the field's name stays). Under `api.tokenUse: id`, the factory verifies it with `aud` holding one of `clientIDFiles`.
  - **The expiry:** `min(exp, 1 h)` as today. Keycloak's 5-minute default ID token needs oauth2-proxy's `cookie-refresh` below it, which the docs say.
  - **`maku`** stores the token response. In `id` mode it sends `id_token`. A refresh that returns no `id_token` (OIDC Core makes it optional) fails with `the IdP returned no ID token on refresh: run maku login`. Task 1 records which IdPs return it.

- **PR4 (the GitHub account link: the flow).**
  - **The client:** the factory App's OAuth client (a GitHub App has a client id and secret), in `broker.githubLink.existingSecret` with keys `client_id` and `client_secret`. Its callback URL `<publicURL>/link/github/callback` is registered on the App. Step 7's manifest flow sets it (`callback_urls`). A dedicated OAuth App works too, with the same keys. The owner decides the default (After this step).
  - **The flow:**
    1. **`GET /link/github`** (a web session, P18: the CLI is refused `not_web`). The page shows the current link (login and date) and two forms, Link and Unlink.
    2. **`POST /link/github/start`** (same origin; the web session).
       - It creates `state` (32 random bytes) and a PKCE verifier.
       - It stores `{sha256(state), sub, verifier, sha256(nonce), expires: now+10 min}` in `github_link_states`, at most 3 pending per `sub`.
       - It sets the cookie `__Host-maku-ghlink=<nonce>`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/link/github`, 10 min.
       - It answers 303 to `<github.webURL>/login/oauth/authorize?client_id=…&redirect_uri=…&state=…&code_challenge=…&code_challenge_method=S256&allow_signup=false`.
    3. **`GET /link/github/callback?code&state`** is unauthenticated: oauth2-proxy lets it through with `skip-auth-route`, because its own session cookie is `SameSite=Strict` and a navigation from github.com would not carry it.
       - It requires the cookie's nonce to match the state row.
       - It exchanges the code with the verifier and calls `GET /user` (`id`, `login`).
       - It revokes the user token at once.
       - It stores `{github_id, login}` on the state row as pending, and renders a confirm page. It links nothing.
    4. **`POST /link/github/confirm`** (same origin, so the Strict session cookie is sent).
       - It requires a web session whose `sub` equals the state row's.
       - It writes the link, appends an event, and deletes the state row.
  - **What this defeats:** a link CSRF that would hand an attacker's `sub` the victim's GitHub id. The victim's confirm runs under the victim's `sub`, which is not the state's. A state without its cookie is refused at the callback.
  - **Verification:** the link is proof only at the moment of linking, by an authorization the person made at GitHub. Each use resolves the numeric id to the current login through the factory App (`LoginOf`, rename-safe), then checks permission on the repository (`repoaccess`), as today.
  - **Revocation:**
    - the person unlinks on the page;
    - an admin calls `DELETE /api/links/github/{sub}` (a web session in the admin group);
    - break-glass: `maku-broker github-unlink --sub <sub>`, as `rooms_broker`.

    Each act is an event. Revoking the App's authorization at GitHub changes nothing, because no token is held.
  - **Uniqueness:** one GitHub id per `sub`, and one `sub` per GitHub id. Linking an id held by another `sub` is refused with `github_account_taken`, and an admin unlinks first. Relinking one's own `sub` replaces the link.
  - **Outcomes** (`rooms_github_links_total{outcome}`, closed): `started`, `linked`, `relinked`, `unlinked`, `admin_unlinked`, `state_invalid`, `cookie_mismatch`, `sub_mismatch`, `github_account_taken`, `github_error`, `not_web`, `too_many_pending`, `error`.

- **PR5 (the link as the identity source, and "active" without an IdP admin API).**
  - **`ghidentity.Resolver.Links`** reads the store in the broker, and `GET /v1/links/github` (system:factory only) in the factory. The resolver keeps its numeric-id contract, so `LoginOf`, the cache and the TTL are unchanged.
  - **`Active` replaces ZITADEL's user state, generically:**
    - the link row carries `last_seen_at`, refreshed at most once an hour by each authenticated request of that `sub`;
    - a `sub` is active while it holds a link and `now - last_seen_at ≤ human.githubLink.activeWindow` (default `168h`, at most `720h`).
  - **What that means for each caller:**
    - a human presenting a token is active by construction: the request refreshes `last_seen_at` first, and the IdP stopped minting tokens for a deactivated user;
    - the Slack App acting for a linked human (`slackDecide`, the factory's `selfServeLogin`) is refused `not_linked` once that human has not signed in within the window.

    The residual is a deactivated person whose Slack link keeps acting up to the window. It is shorter with a smaller window, and zero with an admin unlink. `docs/security.md` says so. ZITADEL's live user-state read goes with the reader.
  - **The transition (D25):** with `human.githubLink` and `human.access.readerFile` both set, `Links` reads the link first, and the ZITADEL reader only for a `sub` with no link. `Active` is the reader's for those subs. A startup warning names `readerFile` as deprecated (step 8). The factory follows the same rule with `api.access.links: broker` beside `readerFile`.

- **PR6 (the database: one DSN Secret per role; migrations stay an init container).**
  - **The roles are fixed names.** The migrations GRANT to them by name, and templating SQL per install would break the checksums. Each role has its own Secret:

    | Role | Value | Read as | By |
    |---|---|---|---|
    | `rooms_owner` | `broker.database.ownerSecret` | `ROOMS_MIGRATION_DATABASE_URL` | the `migrate` init container |
    | `rooms_broker` | `broker.database.brokerSecret` | `ROOMS_DATABASE_URL` | the broker |
    | `rooms_retention` | `broker.database.retentionSecret` (new) | `ROOMS_DATABASE_URL` | the CronJob `room-broker-retention` (new, `retention.schedule` default `17 3 * * *`, ANR's spec) |

    The key is `broker.database.secretKey` (default `uri`), for every role. `broker.database.caSecret` (key `ca.crt`) mounts at `/etc/room-broker/db-ca/ca.crt` in all three containers, and the docs show `sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt`.
  - **"At start-up" is the init container, not a Job.**
    - A Helm hook Job would run before an ExternalSecret rendered in the same release has produced the owner's DSN, and Argo CD and Flux treat hooks differently.
    - The init container waits for its Secret, works with every GitOps controller, and is serialized across replicas by the existing advisory lock.
    - Without `ownerSecret` there is no migration, as today. The broker then refuses to serve a schema older than its binary (Task 14).
  - **The bootstrap is the operator's.** `docs/database.md` gives the five statements of `rooms-db-init` for any Postgres ≥16. It also covers CNPG managed roles, RDS, Cloud SQL and Azure Flexible Server: none needs superuser, and `rooms_owner` needs `CREATE` on `public`. Backups and HA stay the operator's (D9).
  - **The spec's "the broker and the factory read DSN Secrets":** only the broker and its retention job do. The spec gets a one-line amendment in PR D.

- **PR7 (the database checks).** `store.Check(ctx, conn, role)` runs in `migrate` (as `rooms_owner`), `serve` (as `rooms_broker`) and `retention` (as `rooms_retention`). It refuses with one message per failure:
  - **The version:** `server_version_num < 160000` gives `PostgreSQL <v>: Maku needs 16 or later (D26)`.
  - **The role:** `current_user` must be the expected role. Today only `migrate` checks this. `serve` and `retention` gain it, because the room log's immutability rests on the grants and RLS (T12, SC-10).
  - **RLS bypass:** `rolsuper` or `rolbypassrls` on the connected role gives `<role> bypasses row-level security: the room log would be writable`. So does `pg_has_role(current_user, 'rooms_owner', 'MEMBER')` for `rooms_broker` and `rooms_retention`.
  - **`migrate` only:** `rooms_broker` and `rooms_retention` must exist (`to_regrole`). Otherwise `create the login roles rooms_broker and rooms_retention first (docs/database.md)`, before any GRANT fails halfway.
  - **`serve` only:** the newest applied revision must equal the binary's newest file. A ledger behind the binary gives `the schema is at <v>, this broker needs <w>: run migrate (broker.database.ownerSecret)`. A ledger ahead keeps `migrate`'s existing refusal. Checked in either ledger (PR8).
  - **TLS:** `pg_stat_ssl.ssl` false is logged once as a warning, and set on `rooms_db_tls{role}`. It is not refused, because the demo's hop is plain.
  - **Additive:** both deployed Postgres are 17 (demo) and 18 (cnref), and both connect as the right roles. The demo-e2e and the cnref live check prove it.

- **PR8 (adopting the Atlas operator's ledger).**
  - **When:** `migrate` finds no `room_broker_revisions` but finds `atlas_schema_revisions.atlas_schema_revisions`.
  - **What it reads:** the rows with `applied = total` and an empty `error`.
  - **What it requires:** their versions must be exactly a prefix of the shipped files, and the database must hold no partial row.
  - **What it does:** it writes `room_broker_revisions` for that prefix with the shipped checksums, in one transaction, then applies whatever is pending. It logs `adopted N revisions from the Atlas operator`, with no data migration (D25).
  - **What it refuses**, with the version named: a gap, an unknown version, or a partial or errored row.
  - **The Atlas ledger** is left in place and never written again. `serve`'s check (PR7) reads `room_broker_revisions`, else the Atlas ledger, so cnref's broker keeps serving before its first `migrate`.
  - **Unverified:** that `rooms_owner` can read the Atlas schema on cnref (the SQLInstance comment says "Atlas migrates as it"). Task 15's ANR-shaped fixture and the live check settle it. **Stop rule:** if it cannot, the ANR follow-up grants `USAGE`/`SELECT` on that schema to `rooms_owner` before the flip.

- **PR9 (Secrets by name: one registry).**
  - **The registry:** `internal/install/secrets.go` holds `Seams []Seam{ID, Component, Namespace (release|runs), Value (the values path holding the name), Default, Keys []Key{Name, Required, Doc}, ReadAs (env|file), Optional}`.
  - **What reads it:**
    - `docs/install-secrets.md`, with a test asserting its table equals the data;
    - the umbrella's `_seams.tpl`, generated by `task chart:seams` and checked in CI;
    - step 7's `maku preflight`.
  - **What it lists:**
    - the three DSNs and the database CA;
    - the broker's OIDC id files (`broker.oidc.existingSecret`: `web-client-id`, `cli-client-id`, and `project-id` under the zitadel preset);
    - the GitHub link client;
    - the factory App, the agents App and the merger (step 3);
    - the factory's OIDC ids (`oidcSecret`), the sponsored ids, the intake key, the MCP key and the broker TLS and CAs;
    - the model gateway's providers, from step 4. If step 4 PR A has not merged when PR E opens, they are a follow-up row (After this step).
  - **Rules:**
    - a seam's required key missing from an ExternalSecret's `data` fails the render;
    - every `secretKeyRef` and `secret` volume the charts render names a registered seam, which a render test enforces;
    - the demo's generated Secrets are registered as `generatedByDemo`.

- **PR10 (`externalSecrets.enabled`: rendered, never assumed).**

  ```yaml
  externalSecrets:
    enabled: false
    apiVersion: external-secrets.io/v1      # ESO >= 0.17
    storeRef: {kind: ClusterSecretStore, name: ""}   # SecretStore allowed (ANR's agents-secrets)
    refreshInterval: 1h
    secrets: {}       # <seam id>: {data: [{secretKey, remoteRef: {key, property?}}] | dataFrom: [...], template?: {...}}
  ```

  - **What it renders:** one ExternalSecret per seam listed under `secrets`. Its `target.name` is the seam's resolved Secret name (so the subcharts keep reading one name), with `creationPolicy: Owner` and `deletionPolicy: Retain` (ANR's).
  - **`template`** passes ESO's `target.template` through, so a DSN can be assembled from CNPG's `username`/`password` (documented example).
  - **The render fails:**
    - with `enabled` and an empty `storeRef.name`;
    - with a seam id not in the registry;
    - when a required key is not produced. A `dataFrom.extract` is trusted, and the docs say so.
  - **What it never renders:** a SecretStore, or a Secret alongside an ExternalSecret of the same name. A seam listed under `externalSecrets.secrets` and also generated by the demo fails.

- **PR11 (oauth2-proxy stays out of this step).** The web front door is a component (D20) behind the exposure (D19): its subchart lands in step 6 with `expose.mode`. Step 5 ships:
  - the broker side;
  - `docs/identity.md`, with oauth2-proxy's arguments per preset: `provider: oidc`, `pass-authorization-header`, `pass-access-token`, `skip-jwt-bearer-tokens` with `EXTRA_JWT_ISSUERS=<issuer>=<cli client>`, `oidc-groups-claim` = the roles claim, `allowed-group`, `cookie-samesite: strict`, `cookie-refresh` under the ID token's lifetime, and `skip-auth-route` for `GET=^/\.well-known/roomctl$` and `GET=^/link/github/callback$`;
  - the k3d proof with the e2e stub (Task 8), which calls the broker with oauth2-proxy's headers directly.

- **PR12 (what Task 1's probe decides).** It runs Dex (v2.41, `staticPasswords` plus the `mockCallback` connector for groups) and Keycloak (26.x, a `groups` mapper) on a throwaway k3d, and records:
  - the ID and access token shapes (`aud`, `azp`, the roles claim's location);
  - whether a refresh returns `id_token`;
  - the device endpoints in discovery;
  - whether client credentials give a JWT access token with `azp`.

  **Stop rules:**
  - a probed IdP whose refresh omits `id_token` is documented as "maku login again at expiry";
  - an IdP with no device endpoint is documented as web-only.

  GitHub's PKCE on GHES 3.14 is recorded from the fake and GitHub's changelog. PKCE is sent always and is defense in depth beside the client secret, so a GHES that ignores it changes nothing.

---

## PR A: any OIDC issuer, verification (D8)

### Task 1: the IdP probe (S)

**Files:**
- Create: `hack/oidc-probe/probe.sh`, `hack/oidc-probe/dex.yaml`, `hack/oidc-probe/keycloak.yaml` (one realm import with a `groups` mapper and a device-enabled public client), `hack/oidc-probe/README.md`.

- [ ] **Step 1: write the probe as assertions**, each printing `PASS`/`FAIL <what>`, for Dex and Keycloak:
  - **O1:** discovery has `jwks_uri`, `token_endpoint` and `device_authorization_endpoint`.
  - **O2:** the ID token from the password grant has `aud` holding the client, has or lacks `azp` (recorded), and carries the roles at the documented claim or pointer.
  - **O3:** a refresh response carries `id_token`.
  - **O4:** the client-credentials access token is a JWT with `azp` or `client_id`.
  - **O5:** the access token's shape (JWT or opaque) is recorded.
- [ ] **Step 2: run** `bash hack/oidc-probe/probe.sh`, and record every result in the PR A body and in `docs/identity.md`'s IdP table (Task 8). Apply PR12's stop rules.
- [ ] **Step 3: commit** `test(identity): a reproducible probe of Dex and Keycloak token shapes`.

### Task 2: `authn`, the roles claim and the client a token was issued to (M)

**Files:**
- Modify: `internal/authn/jwt.go`:
  - `Claims` captures the raw claim set (an `UnmarshalJSON` over an alias, plus `raw map[string]json.RawMessage`);
  - `RoleNames(claim string) ([]string, error)`, where `""` is today's `GroupNames()`;
  - `issuedTo()` falls back to a one-element `aud`;
  - `ErrRolesClaim` with reasons `missing`, `type` and `bounds`.
- Modify: `internal/authn/authn_test.go`, `internal/authn/azp_test.go`.

- [ ] **Step 1: failing tests**, table-driven:
  - `groups: ["a","b"]` → `[a b]`;
  - `/realm_access/roles` over `{"realm_access":{"roles":["x"]}}` → `[x]`;
  - `https://ns.example/roles` (no leading `/`) is read as a name;
  - `urn:zitadel:iam:org:project:roles: {"agents-admin":{…}}` → `[agents-admin]`;
  - a single string → one name;
  - a number gives `type`;
  - 257 names give `bounds`;
  - an absent claim gives `missing`;
  - `~1` and `~0` unescape;
  - `""` equals `GroupNames()` on today's fixtures;
  - an ID token with `aud: [web]` and no `azp` or `client_id` is issued to `web`;
  - `aud: [web, other]` with no `azp` is issued to nobody;
  - today's ZITADEL fixtures resolve as before.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/authn/ -run 'Roles|IssuedTo|Azp'`. Expected: FAIL.
- [ ] **Step 2: implement.** No claim value reaches an error string. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(authn): a configured roles claim, and the client a token was issued to by aud`.

### Task 3: `authn.Humans` with token use `id` and `access`, and presets (M)

**Files:**
- Modify: `internal/authn/humans.go`:
  - `NewHumans` takes `HumanOptions{TokenUse, Audience func() string, RolesClaim string, Web, CLI func() string, Origin string}`;
  - the old signature becomes a ZITADEL wrapper.
- Modify: `internal/authn/humans_test.go`, `internal/authn/sponsored.go` (`ClientClaim` and `Audience` per PR2; `Sponsored` keeps a required audience), `internal/authn/sponsored_test.go`.

- [ ] **Step 1: failing tests:**
  - **`access`:** every existing `humans_test.go` case passes unchanged through the wrapper.
  - **`id`, web:** an ID token for `web` plus a distinct opaque `X-Forwarded-Access-Token` gives a human with `ClientID web` and `AccessToken` = the ID token. Equal headers are refused with "a web session needs a distinct access token". A missing access header is refused.
  - **`id`, CLI:** an ID token for `cli` as the bearer gives `ClientID cli`. The same token with `aud` `web` presented alone is refused.
  - **`id`:** a token with the right `aud` and no roles claim is admitted as a principal with no groups. The policy's "not in an agents group" then refuses it, as `humanapi` does today.
  - **The roles claim** of each preset gives the `Groups` the policy reads.
  - **Sponsored:** under `generic`/`access`, a client-credentials token with `azp` = the Slack App's id and `aud` holding `audience` is `system:slack-app`. A `cid`-only token (Okta) is admitted with `clientClaim: cid`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/authn/`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(authn): human tokens by use, id or access, over any OIDC issuer`.

### Task 4: the broker's `human` keys, discovery and wiring (M)

**Files:**
- Modify:
  - `internal/config/config.go`: `HumanConfig` gains `Preset`, `TokenUse`, `Audience`, `AudienceFile`, `RolesClaim` and `Scopes`, with `EffectivePreset()` and `EffectiveTokenUse()`. `jwksURL` becomes optional when `issuer` is https;
  - `validate`: the generic preset requires `rolesClaim`; `tokenUse: access` requires `audience` xor `audienceFile`; the zitadel preset requires `projectIDFile` and refuses `audience`; `projectIDFile` under generic is refused, naming `audienceFile`;
  - `internal/config/config_test.go`;
  - `internal/authn/discovery.go` (create: `Discover(ctx, hc, issuer) (Discovery, error)`, which checks that `issuer` matches exactly, as OIDC Discovery §4.3 requires);
  - `internal/app/jwks.go`: build the human verifier from discovery when `jwksURL` is empty, lazily, as `NewLazyVerifier` does, so a down IdP does not stop the broker. Build `Humans` and `Sponsored` from the effective options;
  - `internal/app/broker.go` (`humanServer`'s `ProjectID` becomes the effective audience reader);
  - the `internal/app` tests.

- [ ] **Step 1: failing tests:**
  - a config with no `preset` decodes to `zitadel`, and `Validate` gives today's errors;
  - `preset: generic` without `rolesClaim` gives `human.rolesClaim: is required with preset generic (the claim holding groups.admin and groups.member)`;
  - `preset: generic` with `projectIDFile` gives `human.projectIDFile: the zitadel preset's; use human.audienceFile with tokenUse access`;
  - `Discover` refuses an `issuer` mismatch and a non-https `jwks_uri`;
  - an app test with an `httptest` issuer serving discovery and keys: a generic ID token reaches `GET /api/rooms` as a member.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/config/ ./internal/authn/ ./internal/app/ -run 'Human|Preset|Discover'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker): any OIDC issuer, with presets, discovery and a configured roles claim`.

### Task 5: the factory's `api` keys and authenticator (M)

**Files:**
- Modify:
  - `internal/factory/config/config.go`: `API` gains `Preset`, `TokenUse`, `Audience`, `AudienceFile`, `RolesClaim` and `SponsoredClientClaim`; `humanJWKS` becomes optional; `projectIDFile` becomes the zitadel preset's `audienceFile`, with the same conflict messages as Task 4;
  - `internal/factory/config/config_test.go`;
  - `internal/factory/api/authn.go`: `Authenticator` takes `RolesClaim` and `TokenUse`; under `id` it accepts an ID token whose `aud` holds a client id;
  - `internal/factory/api/authn_test.go`;
  - `internal/app/factory.go`: wiring, with discovery when `humanJWKS` is empty.

- [ ] **Step 1: failing tests:**
  - today's factory fixtures parse and authenticate unchanged;
  - under `api.tokenUse: id`, the broker-forwarded ID token is a human, with groups from `api.rolesClaim`;
  - a token without an agents group is `ErrForbidden`;
  - the sponsored branch with `sponsoredClientClaim: cid` admits the Slack App.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/factory/config/ ./internal/factory/api/ -run 'Human|Authn|Sponsored'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): the run-request API over any OIDC issuer`. Open PR A with PR2, PR3 and PR12's results in the body.

## PR B: any OIDC issuer, the clients and the chart (D8)

### Task 6: the discovery document and `maku login` from discovery (M)

**Files:**
- Modify:
  - `internal/humanapi/rooms.go`: `writeSetup` adds `preset`, `tokenUse`, `scopes` and `audience`. `projectID` stays filled under zitadel. The fields are additive;
  - `internal/humanapi/server.go` (the fields);
  - `internal/maku/config.go`: `Config` gains `Preset`, `TokenUse`, `Scopes` and `Audience`; `Validate` requires `projectID` only under zitadel;
  - `internal/maku/auth.go`: `oauthConfig` reads `device_authorization_endpoint` and `token_endpoint` from `<issuer>/.well-known/openid-configuration`, cached in the config file at `maku configure`. Its scopes are `openid profile email offline_access` plus the preset's. `Token` returns `id_token` under `id`, with PR3's refresh message;
  - `internal/maku/doctor.go` (a discovery check);
  - `internal/app/maku.go` (`configure` stores the endpoints);
  - the tests of each.

- [ ] **Step 1: failing tests:**
  - against a fake issuer, `maku login` hits the discovered device endpoint, not `/oauth/v2/device_authorization`;
  - a ZITADEL-shaped `/.well-known/roomctl` with no new fields gives today's scopes and the access token;
  - `tokenUse: id` sends `id_token`;
  - a refresh with no `id_token` gives PR3's message;
  - `maku doctor` names an issuer whose discovery fails.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/maku/ ./internal/humanapi/ -run 'Login|Token|Setup|Doctor'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(maku): sign in to any OIDC issuer through its discovery document`.

### Task 7: the Slack App's client credentials, generic (S)

**Files:**
- Modify:
  - `internal/slackapp/config.go`: `tokenURL` (optional, from discovery), `scopes`, `audience`; `projectIDFile` is required only when `preset` is `zitadel` or empty;
  - `internal/app/slackapp.go:95` (the required-keys check) and `:247` (the `clientcredentials.Config` from the config, with `EndpointParams{"audience"}` when set);
  - the tests.

- [ ] **Step 1: failing tests:**
  - today's config gives today's token request: the URL and the ZITADEL scope;
  - `preset: generic, audience: maku-api, scopes: []` posts to the discovered `token_endpoint` with `audience=maku-api`;
  - generic with `projectIDFile` is refused, naming `audience`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/slackapp/ ./internal/app/ -run 'Slack'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(slack): mint the App's token at any OIDC issuer`.

### Task 8: the chart, the stub issuer, the k3d proof and `docs/identity.md` (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml`: `broker.oidc: {existingSecret: "", keys: {web: web-client-id, cli: cli-client-id, project: project-id}}`. Set, it mounts at `/etc/room-broker/oidc` and fills `human.clientIDFile`, `roomctlClientIDFile` and (zitadel) `projectIDFile` when the verbatim block leaves them unset. A set path wins;
  - `values.schema.json`, `templates/_helpers.tpl`, `templates/broker-config.yaml`, `templates/broker-deployment.yaml`, `tests/render.sh`;
  - `charts/maku-factory/templates/configmap.yaml` and `tests/render.sh` (nothing new is mounted: `oidcSecret` already is);
  - `charts/agent-factory/templates/_helpers.tpl` (PR2's agreement rule) and `tests/render.sh`;
  - `hack/e2e-stub/oidc.go`: discovery; a generic ID token mint (`/test/id-token?sub&aud&groups`); client credentials returning a JWT with `azp`;
  - `hack/e2e-stub/oidc_test.go`;
  - `charts/agent-factory/tests/intake.sh` (a generic-preset variant);
  - `Taskfile.yml` (`chart:identity:k3d`);
  - `docs/identity.md` (create: PR2's table, the IdP table from Task 1, PR3, PR11's oauth2-proxy arguments, and the migration from ZITADEL);
  - `docs/security.md` (P18 under `tokenUse: id`);
  - `docs/README.md`.

- [ ] **Step 1: failing tests:**
  - the maku chart with `broker.oidc.existingSecret=x` mounts `x` and writes the three file paths;
  - a verbatim `clientIDFile` wins;
  - the umbrella fails with `maku.broker.human.rolesClaim "groups" and maku-factory.config.api.rolesClaim "roles" must be equal`;
  - default and demo renders are byte-identical;
  - **k3d** (`task chart:identity:k3d`): the broker in `preset: generic` against the stub. A stub ID token in `groups: [agents-member]` lists rooms. One in no group answers 403. A CLI-client ID token as the bearer may read but not decide, as P18 requires.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the broker's OIDC ids by Secret, a generic issuer proved on k3d`. Open PR B.

## PR C: the GitHub account link (D8)

### Task 9: the store: links, pending states and events (M)

**Files:**
- Create: `internal/store/migrations/20261120120000_github_links.sql`. It creates:
  - `github_links (sub text PK, github_id bigint UNIQUE NOT NULL CHECK (> 0), login_at_link text, linked_at, last_seen_at)`;
  - `github_link_states (state_sha256 bytea PK, sub, verifier text, nonce_sha256 bytea, expires_at, github_id bigint NULL, login text NULL)`, with an index on `sub`;
  - `github_link_events (id bigserial, sub, github_id, act text CHECK (act IN ('linked','relinked','unlinked','admin_unlinked')), actor text, at)`;
  - GRANTs to `rooms_broker`: ALL on the first two; `SELECT, INSERT` only on events;
  - RLS on all three, with policies in `slack_links`'s style. Events are append-only: no UPDATE or DELETE grant.
- Create: `internal/store/githublinks.go`, `internal/store/githublinks_test.go`.
- Modify: `internal/store/migrations/atlas.sum` (`atlas migrate hash`).

- [ ] **Step 1: failing tests** (testcontainers Postgres 16 and 18):
  - `StartGitHubLink` refuses a 4th pending state for one `sub` (`ErrTooManyPending`);
  - `GitHubState` refuses an expired one;
  - `ConfirmGitHubLink` writes the link and an event in one transaction;
  - linking a `github_id` held by another `sub` gives `ErrGitHubTaken`, with nothing written;
  - a relink of one's own `sub` is `relinked`;
  - `Unlink` and `AdminUnlink` write their events;
  - `TouchGitHubLink` updates `last_seen_at` only when it is over 1 h old;
  - `rooms_broker` cannot `UPDATE` or `DELETE` `github_link_events` (`42501`);
  - the migration applies on top of today's schema, and `migrate`'s checksum test passes.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/store/ -run 'GitHubLink|Migrat'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(store): GitHub account links, their pending states and an append-only history`.

### Task 10: the OAuth flow and the `/link/github` pages (L)

**Files:**
- Create:
  - `internal/humanapi/githublink.go`: `GitHubLinker{Store, ClientID, ClientSecret func() string, WebURL, APIURL, HC, Rand, Now}`; the handlers `GET /link/github`, `POST /link/github/start`, `GET /link/github/callback`, `POST /link/github/confirm`, `POST /link/github/unlink`, and `DELETE /api/links/github/{sub}` (admin);
  - `internal/humanapi/githublink_test.go`;
  - `internal/github/oauth.go`: `ExchangeCode`, `User` and `RevokeUserToken`, on `webURL` and `apiURL`; no token in an error;
  - `internal/github/oauth_test.go`.
- Modify:
  - `internal/humanapi/server.go` (the routes; the callback is the one unauthenticated `/link/` route);
  - `internal/config/config.go`: `human.githubLink {clientIDFile, clientSecretFile, webURL (default https://github.com), activeWindow}`; refused in local mode, as `slackLink` is; `activeWindow` above 0 and at most `720h`;
  - `internal/config/config_test.go`;
  - `internal/metrics` (`rooms_github_links_total{outcome}`);
  - `internal/app/broker.go` (wiring).

- [ ] **Step 1: failing tests**, against a fake GitHub (`httptest`):
  - the happy path: start sets the cookie and redirects with `code_challenge`; the callback with the cookie exchanges, reads `/user` and revokes (the fake asserts the `DELETE`); confirm as the same `sub` writes the link;
  - the callback without the cookie, or with another nonce, is `cookie_mismatch` and does not exchange;
  - confirm under another `sub` is `sub_mismatch`, and nothing is written;
  - a replayed or expired `state` is `state_invalid`;
  - a CLI principal on `GET /link/github` is `not_web`;
  - a foreign `Origin` on any POST is refused;
  - an exchange error is `github_error`, and the page names no token;
  - the `github_account_taken` page says an admin must unlink;
  - `DELETE /api/links/github/{sub}` by a member is 403, by an admin `admin_unlinked`;
  - the page sets the room UI's CSP and has no script;
  - a log sink sees no `code`, `state`, verifier or token.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/humanapi/ ./internal/github/ ./internal/config/ -run 'GitHubLink|OAuth'`. Expected: FAIL.
- [ ] **Step 2: implement** PR4. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker): link a GitHub account once, through GitHub's OAuth flow`.

### Task 11: the link as the broker's identity source (M)

**Files:**
- Create: `internal/ghidentity/links.go`: `StoreLinks(store)` and `StoreActive(store, window, now)`; `Chain(primary, fallback)` for PR5's transition.
- Create: `internal/ghidentity/links_test.go`.
- Modify:
  - `internal/app/access.go` (`roomAccess` builds the resolver from `human.githubLink`, chained before `readerFile` when both are set; `human.access.readerFile` becomes optional when `githubLink` is set);
  - `internal/config/config.go` (`human.access` needs `readerFile` or `githubLink`; the https issuer rule applies only to `readerFile`);
  - `internal/humanapi` (each authenticated human request calls `TouchGitHubLink` through the throttle);
  - `internal/humanapi/slackdecide.go` (no code change; the test proves the window);
  - `internal/app/access_test.go`.

- [ ] **Step 1: failing tests:**
  - a linked `sub` resolves to the App's current login for its numeric id, after a rename too;
  - an unlinked `sub` gives `""`, so rooms are admin-only for them, as today;
  - a `sub` unseen past `activeWindow` is `ErrInactive` for `slackDecide`;
  - a request by that `sub` touches it and it is active again;
  - with both sources, a linked `sub` never calls ZITADEL (a counting fake), and an unlinked one falls back;
  - a store error is an error, never `""`.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/ghidentity/ ./internal/app/ ./internal/humanapi/ -run 'Link|Access|Active|SlackDecide'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(broker): resolve a human's GitHub login from their account link`.

### Task 12: the factory reads the links from the broker (M)

**Files:**
- Create: `internal/bridgeapi/githublinks.go`: `GET /v1/links/github?sub=` for `SlackLinkReader`'s principal (renamed `LinkReader`, with the old field kept as an alias). It answers `{"githubId": n, "lastSeenAt": t}` or 404 `not_linked`.
- Create: `internal/bridgeapi/githublinks_test.go`.
- Modify:
  - `internal/bridgeapi/server.go` (the route);
  - `internal/factory/rooms/client.go` (`GitHubLink(ctx, sub)`);
  - `internal/factory/config/config.go`: `api.access.links: broker` (requires `broker.url`); `readerFile` becomes optional with it; both set is PR5's chain;
  - `internal/app/factory_repos.go` (`access` builds `Links` and `Active` from the broker client; `activeWindow` is in the broker's answer, as `active: bool`, so one window rules);
  - `internal/factory/api/tasks.go` (no change; the tests prove `selfServeLogin` over the new source);
  - the tests of each.

- [ ] **Step 1: failing tests:**
  - only `system:factory` reads the route; any other system principal gets 403;
  - a malformed `sub` (over 128 bytes, or empty) gives 400;
  - the factory's `selfServeLogin` with a Slack link plus a GitHub link gives the login;
  - a broker 503 gives `link_unverified`;
  - an inactive link gives `not_linked`;
  - the run-request API's write check for a human goes through the broker's link.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/bridgeapi/ ./internal/factory/... ./internal/app/ -run 'GitHubLink|SelfServe|Access'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(factory): read a human's GitHub link from the broker, not the IdP`.

### Task 13: the chart, the Slack page, the fake GitHub, the proof and the docs (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml`: `broker.githubLink: {existingSecret: "", activeWindow: 168h}`, mounted at `/etc/room-broker/github-link` and filling `human.githubLink` when the verbatim block leaves it unset;
  - `values.schema.json`, `templates/broker-deployment.yaml`, `templates/broker-config.yaml`;
  - `templates/broker-netpol.yaml` and `broker-cnp.yaml` (egress 443 to `webURL`'s host, under `github.webURL`; the API host is already allowed);
  - `tests/render.sh`;
  - `charts/maku-factory/values.yaml` (a comment: `zitadelReaderSecret` deprecated, `config.api.access.links: broker`);
  - `internal/humanapi/slacklink.go` (the `not_linked_github` notice links to `/link/github`);
  - `hack/e2e-stub/github.go` (`/login/oauth/authorize` answering 302 back with a code; `/login/oauth/access_token`; `DELETE /applications/{id}/token`; `/user`) and its tests;
  - `charts/agent-factory/tests/intake.sh` (the link variant);
  - `docs/identity.md` (PR4 and PR5, revocation, the window's residual, the GitHub App callback URL);
  - `docs/slack.md`, `docs/security.md`, `docs/api.md` (the routes), `docs/upgrading.md` (moving off `readerFile`).

- [ ] **Step 1: failing tests:**
  - render: `broker.githubLink.existingSecret=x` mounts `x` and fills the files; a set `human.githubLink.clientIDFile` wins; the egress names `github.com`;
  - **k3d:** a stub human starts, follows the stub's redirect, confirms, and `/link/github` shows `dev`. A factory run request by that human passes the write check with no ZITADEL endpoint in the stub's log. An admin unlink makes the next request answer `repository_permission`.
  - default and demo renders are byte-identical.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): the GitHub link by Secret, proved end to end on k3d`. Open PR C with PR4 and PR5 in the body.

## PR D: BYO Postgres (D9)

### Task 14: the version and role checks (M)

**Files:**
- Create: `internal/store/check.go`, `internal/store/check_test.go`.
- Modify:
  - `internal/store/migrate.go` (`Check` as `rooms_owner`, plus the two roles existing, before the lock);
  - `internal/store/store.go` (`Open` takes the expected role; `serve` adds the schema-version check);
  - `internal/app/broker.go` (`serve`, `retention` and `intent-samples` pass their role);
  - `internal/metrics` (`rooms_db_tls{role}`).

- [ ] **Step 1: failing tests** (testcontainers):
  - Postgres 15 gives PR7's version message for `migrate` and `serve`;
  - 16, 17 and 18 pass;
  - `serve` connected as `rooms_owner` gives `connected as "rooms_owner": the broker runs as rooms_broker`;
  - `rooms_broker WITH BYPASSRLS` is refused, and so is `GRANT rooms_owner TO rooms_broker`;
  - `migrate` with `rooms_retention` missing names `docs/database.md` before any statement runs (the ledger is untouched);
  - `serve` on a ledger one file behind names `migrate`;
  - a non-TLS connection sets the gauge and logs once.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/store/ ./internal/app/ -run 'Check|Migrat|Open|Retention'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(store): refuse Postgres below 16 and any role that bypasses the log's row security`.

### Task 15: adopting the Atlas operator's ledger (M)

**Files:**
- Create: `internal/store/adopt.go`, `internal/store/adopt_test.go`, `internal/store/testdata/atlas_revisions.sql` (the operator's table, as it creates it, and ANR-shaped rows for v0.45.0's sixteen files).
- Modify: `internal/store/migrate.go` (adoption before `recorded`), `internal/store/check.go` (`serve` reads either ledger).

- [ ] **Step 1: failing tests:**
  - a database migrated by `atlas migrate apply` (the CLI in the test image, or the fixture applied over the real files) is adopted, and the next `migrate` applies 0;
  - with one more shipped file, it applies exactly that one;
  - a gap, an unknown version, or a row with `applied < total` or an `error` is refused, naming the version, with `room_broker_revisions` absent afterwards;
  - both ledgers present means the Atlas one is ignored;
  - `serve` on an Atlas-only database passes the version check.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/store/ -run 'Adopt|Migrat'`. Expected: FAIL.
- [ ] **Step 2: implement** PR8. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(store): adopt a database the Atlas operator migrated, applying nothing twice`.

### Task 16: the chart: a DSN per role, the CronJob, an external database, the docs (M)

**Files:**
- Modify:
  - `charts/maku/values.yaml`: `broker.database.{retentionSecret, secretKey, caSecret}`, `broker.retention.{schedule, resources}`;
  - `values.schema.json`;
  - `templates/broker-deployment.yaml` (`secretKey`; the CA mount in both containers);
  - `templates/retention-cronjob.yaml` (create: ANR's spec, the broker image, `automountServiceAccountToken: false`, `activeDeadlineSeconds: 900`);
  - `templates/broker-netpol.yaml` (the CronJob's egress to the database, by the same `podLabel(s)`, or `database.egress.cidrs` for a database outside the cluster);
  - `tests/render.sh`;
  - `charts/agent-factory/values.yaml`: `postgres.enabled: false`;
  - `charts/agent-factory/values-demo.yaml`: `postgres.enabled: true`, `retentionSecret: rooms-retention-dsn`;
  - `charts/agent-factory/templates/postgres.yaml` and `db-secrets.yaml` (render only under `postgres.enabled`; a third DSN, `rooms-retention-dsn`);
  - `charts/agent-factory/templates/_helpers.tpl` (`postgres.enabled` with a non-demo profile fails: `the bundled Postgres is demo-only (D9): bring your own and name its DSN Secrets`);
  - `values.schema.json`, `tests/render.sh`, `tests/install.sh` (an external-Postgres variant: a plain `postgres:16` container bootstrapped with `docs/database.md`'s SQL);
  - `docs/database.md` (create: PR6's table and bootstrap, managed services, CNPG, TLS, backups and HA as the operator's);
  - `docs/operations.md`, `charts/agent-factory/README.md`, `charts/maku/README.md`;
  - `docs/superpowers/specs/2026-11-12-v4-production-path-design.md` (PR6's one-line amendment to D9).

- [ ] **Step 1: failing tests:**
  - `retentionSecret=r` renders the CronJob reading `r`'s `uri` as `ROOMS_DATABASE_URL`, and nothing else;
  - `secretKey: dsn` changes all three refs;
  - `caSecret` mounts in the init container, the broker and the CronJob;
  - the demo renders the third DSN and the CronJob. These are the only demo changes, and they are listed in the PR body;
  - `postgres.enabled=false` with the demo values renders no StatefulSet, and the broker reads the named Secrets;
  - **k3d** (`task chart:install:k3d:external-db`): Postgres 16 outside the chart, the three roles from the docs. The broker migrates, serves, and the CronJob's manual run (`kubectl create job --from`) purges.

  Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check` and `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): bring your own Postgres, a DSN per role and the retention job`. Open PR D with PR6–PR8 in the body.

## PR E: Secrets by name and ESO (D14)

### Task 17: the Secret registry (S)

**Files:**
- Create: `internal/install/secrets.go` (PR9's data, every seam of steps 1–5, plus step 4's providers when merged), `internal/install/secrets_test.go`, `docs/install-secrets.md`, `hack/gen-seams.go` (writes `charts/agent-factory/templates/_seams.tpl`).
- Modify: `Taskfile.yml` (`chart:seams`; `chart:check` fails on drift).

- [ ] **Step 1: failing tests:**
  - `TestDocs`: the table in `docs/install-secrets.md` equals the data;
  - every seam's `Value` path exists in its chart's `values.yaml` (read with `yq` from the test);
  - no two seams share a default name in one namespace;
  - `TestRenderedRefs`: `helm template` of the demo and of a full production-shaped values file lists `secretKeyRef` names and `secret` volumes that are all registered seams.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/install/`. Expected: FAIL.
- [ ] **Step 2: implement.** Run `task chart:seams`. Rerun. Expected: PASS.
- [ ] **Step 3: commit** `feat(install): every Secret the install reads, as data`.

### Task 18: `externalSecrets.enabled` in the umbrella (M)

**Files:**
- Create: `charts/agent-factory/templates/externalsecrets.yaml`.
- Modify: `charts/agent-factory/values.yaml` (PR10's block, off), `values.schema.json`, `tests/render.sh`, `tests/golden/externalsecrets.yaml`, `charts/agent-factory/README.md`, `docs/install-secrets.md` (the ESO section, with an OpenBao, a CNPG-assembled DSN and an AWS Secrets Manager example).

- [ ] **Step 1: failing render cases:**
  - `enabled: false` renders nothing (byte-identical);
  - `enabled` with an empty `storeRef.name` fails;
  - `secrets.databaseBroker` renders an ExternalSecret named `rooms-broker-dsn`, the resolved name, with `target.creationPolicy: Owner`;
  - a `data` list lacking `uri` fails with `externalSecrets.secrets.databaseBroker: key uri is required (docs/install-secrets.md)`;
  - an unknown seam id fails;
  - `kind: SecretStore` is accepted;
  - a seam the demo generates fails when listed;
  - a `template` passes through;
  - the golden for the full production-shaped values.

  Run `bash charts/agent-factory/tests/render.sh`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task chart:check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(charts): optional ExternalSecrets for every Secret the install reads`.

### Task 19: startup messages that name the Secret and key, and the docs (S)

**Files:**
- Modify:
  - `internal/app/broker.go`, `internal/app/factory.go`, `internal/app/slackapp.go`: a missing or empty mounted file of a registered seam fails start-up with `<seam>: Secret <name> key <key> is missing or empty (docs/install-secrets.md)`, using the registry's lookup by mount path;
  - the tests of each;
  - `docs/README.md`, `docs/quickstart.md` (a pointer only), `docs/upgrading.md` (this step's new keys).

- [ ] **Step 1: failing tests:**
  - a broker config naming `human.githubLink.clientSecretFile` with an empty file fails with the seam's message;
  - today's broker with every file present starts.

  Run `systemd-run --user --scope -q -p MemoryMax=6G go test -race -count=1 ./internal/app/ -run 'Seam|Secret'`. Expected: FAIL.
- [ ] **Step 2: implement.** Rerun, then `task check`. Expected: PASS.
- [ ] **Step 3: commit** `feat(install): a missing credential names its Secret and key`. Open PR E with PR9 and PR10 in the body.

## After this step (not in these PRs)

- **Owner decisions:**
  - **The link's OAuth client:** the factory App's OAuth client (recommended: no extra App, and step 7's manifest sets its callback), or a dedicated OAuth App.
  - **`activeWindow`'s default:** `168h` recommended, at most `720h`. This is the residual of PR5 for Slack acting on behalf of a deactivated person.
  - **The generic preset's default `tokenUse`:** `id` recommended, because it works on every IdP Task 1 probed. `access` stays available.
  - **oauth2-proxy in the umbrella** (PR11): recommended in step 6, with `expose.mode`.
- **ANR follow-ups** (each its own PR, after the matching Maku release; each recorded with a gcp-0 live check in the session ledger). ANR still runs hand-written room-broker manifests and `maku-factory` alone until step 6, so these edit those files:
  1. **ZITADEL as a preset, explicitly.**
     - Set `human.preset: zitadel` in `infrastructure/{base,aws-0,gcp-0}/room-broker/config.yaml`, and `api.preset: zitadel` in `tooling/base/agent-factory/helm-values-configmap.yaml`. No token behaviour changes.
     - Live check: web, `maku login` and the Slack App all work unchanged.
  2. **The GitHub link, beside the reader.**
     - Register `https://rooms.${private_domain_name}/link/github/callback` on `ogenki-agent-factory`.
     - Add an ExternalSecret `room-broker-github-link` (`client_id`, `client_secret`) from the OpenBao `agents` mount.
     - Mount it, and set `human.githubLink`. Add `skip-auth-route` `GET=^/link/github/callback$` to `oauth2-proxy.yaml`.
     - Allow egress to `github.com:443` in `network-policy.yaml`.
     - Set `api.access.links: broker` in the factory values. `readerFile` stays as the fallback (PR5).
     - Runbook: each human opens `/link/github` once.
     - Live check: a linked human's room list and a Slack `slackDecide`, with ZITADEL's user API absent from the broker's egress logs for that `sub`.
  3. **The reader goes,** once every active human has linked (`github_links` count versus the agents groups):
     - remove `human.access.readerFile` and `api.access.readerFile`;
     - delete both `externalsecret-zitadel-reader.yaml` files and `zitadelReaderSecret`;
     - delete the reader machine user and its PAT in cnref's `zitadel-idp.sh` and the `bao-map.sh` entry (a cnref PR).

     ZITADEL itself, its GitHub IdP for login, and oauth2-proxy stay cnref's (D15).
  4. **CNPG behind a DSN Secret.**
     - Give the broker `ownerSecret` (the composition's `rooms_owner` role Secret with key `uri`, or an ESO template from its `username`/`password`), `retentionSecret` and, where CNPG's CA is mounted, `caSecret` with `sslmode=verify-full`.
     - Live check: the `migrate` init container logs `adopted 16 revisions from the Atlas operator, applied 0`. If PR8's stop rule fires, grant `rooms_owner` read on `atlas_schema_revisions` first.
     - Then drop `spec.atlasSchema` from `sqlinstance.yaml` and delete `retention-cronjob.yaml`.
     - Live check: the next scheduled purge runs from the chart's CronJob.

     Backups and `objectStoreRecovery` stay as they are.
  5. **OpenBao behind ESO, unchanged until step 6.** ANR's hand-written ExternalSecrets stay. Once cnref consumes the umbrella (step 6, D22), they move into `externalSecrets.secrets` with `storeRef: {kind: SecretStore, name: agents-secrets}`, and the files are deleted.
- **Later Maku steps:**
  - **Step 6:** the oauth2-proxy subchart under `expose.mode`, configured from the `human` keys of PR2; the ESO rendering extends to the Slack subchart's Secrets.
  - **Step 7:**
    - `maku preflight` checks, from the registry, that each Secret and its keys exist; that OIDC discovery works and the issuer matches; Postgres reachability, its version and the roles (`store.Check`, from a Job using the three DSNs); and the factory App's callback URL;
    - `maku install --print` asks for the preset and the roles claim, sets the callback, and writes the Secrets or ExternalSecret entries.
  - **Step 8:** deletes `human.access.readerFile`, `api.access.readerFile`, `ghidentity/zitadel.go`, `zitadelReaderSecret`, the old `NewHumans` signature and the `SlackLinkReader` alias.

## Self-review

- **D8:**
  - any OIDC issuer: a configured roles claim and token use, discovery, presets (Tasks 2–5, PR2, PR3), for the web, the CLI, the factory and the Slack App (Tasks 5–7);
  - ZITADEL is a preset whose bytes are today's (Task 4);
  - the account link, with its flow, verification, uniqueness, revocation and history (Tasks 9–10, PR4);
  - the link is the identity source in the broker and the factory (Tasks 11–12, PR5);
  - the `readerFile` PAT is optional now, and deleted by ANR follow-up 3 and step 8.
- **D9:**
  - Postgres ≥16 and the role checks (Task 14, PR7);
  - a DSN Secret per role, the retention CronJob, the CA (Task 16, PR6);
  - migrations at start-up in the init container (PR6), with Atlas adoption for cnref (Task 15, PR8);
  - the bundled StatefulSet behind `postgres.enabled`, refused outside the demo (Task 16).
- **D14:**
  - every credential is a named Secret with documented keys, in one registry the docs, the chart and step 7 read (Task 17, PR9);
  - optional ExternalSecrets against a store the user names (Task 18, PR10);
  - start-up names a missing Secret and key (Task 19).
- **D25:**
  - every new key is opt-in;
  - default renders are byte-identical (Tasks 8, 13, 16, 18), and the demo's two additions are listed (Task 16);
  - old keys are honoured with conflict errors, and the reader is chained behind the link (PR5);
  - no data migration, with the Atlas ledger adopted rather than replayed;
  - no v4.
- **D26:** Postgres 16 is refused below and tested at 16 and 18; GHES ≥3.14 for the OAuth endpoints (PKCE is defense in depth, PR12); Helm 3.15; ESO `v1`.
- **Risks named with a stop rule:**
  - **An IdP refresh without `id_token`:** a documented re-login (Task 1, PR3).
  - **`rooms_owner` cannot read the Atlas ledger:** a grant before the flip (PR8).
  - **A link CSRF:** the cookie binding plus the same-`sub` confirm (PR4, Task 10's tests).
  - **SameSite=Strict and the callback:** the callback links nothing, and the confirm is a same-origin POST (PR4).
  - **A deactivated user keeps Slack reach:** bounded by `activeWindow`, cut at once by an admin unlink (PR5).
  - **The broker under a role that bypasses RLS:** refused at start (PR7).
  - **The nightly** goes red until the release (strict decoding).

---

