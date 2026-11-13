# Identity: any OIDC issuer

The broker, the factory, `maku` and the Slack App sign humans in through any OpenID Connect
issuer. ZITADEL is one preset among others. This page covers:

- the keys each component reads, and how the presets set them;
- what a person's token must carry;
- the proxy in front of the broker;
- what each IdP was found to issue;
- moving an install off ZITADEL.

## The presets

`preset` picks the token rules. An empty preset is `zitadel`: every install configured before v4
keeps its behaviour with no edit.

| Preset | `tokenUse` | Audience a token must hold | Roles | Client claim | Extra scopes `maku login` asks for |
|---|---|---|---|---|---|
| `zitadel` (and empty) | `access` | the project id, read from `projectIDFile` (required) | `groups`, else the keys of `urn:zitadel:iam:org:project:roles` | `azp` on an ID token, `client_id` on an access token | `urn:zitadel:iam:org:project:id:<project>:aud` |
| `generic`, `tokenUse: id` (the default) | `id` | the client ids themselves: an ID token's `aud` holds its client (OIDC Core) | `rolesClaim`, required | `azp`, else `client_id`, else a one-element `aud` | `scopes` |
| `generic`, `tokenUse: access` | `access` | `audience` or `audienceFile`, required | `rolesClaim`, required | the same | `scopes`, plus `audience` sent as a parameter (Auth0) |

`rolesClaim` is a claim name, or an [RFC 6901](https://www.rfc-editor.org/rfc/rfc6901) pointer when
it starts with `/`. `https://ns.example/roles` has no leading `/`, so it is a name.

The claim's value is accepted in three shapes:

- an array of strings;
- an object, whose keys are the names;
- one string.

Under `generic`, the claim gives no group in these cases:

- it is missing;
- it has another shape;
- it holds more than 256 names, or a name over 256 bytes.

The principal then holds no group, and the policy refuses it as not in an agents group. The broker
counts this in `maku_rooms_human_roles_claim_refused_total{reason}` and the factory in
`maku_factory_human_roles_claim_refused_total{reason}`, where `reason` is `missing`, `type` or
`bounds`. The `zitadel` preset keeps its old rule: a malformed `groups` claim refuses the whole
token, and the group list is not bounded.

## The keys

### The broker: `human`

```yaml
human:
  mode: oidc
  preset: generic
  issuer: https://idp.example.com
  jwksURL: ""              # optional with an https issuer: read from its discovery document
  clientIDFile: /etc/room-broker/oidc/web-client-id
  roomctlClientIDFile: /etc/room-broker/oidc/cli-client-id
  tokenUse: id
  audience: ""             # or audienceFile; zitadel's is projectIDFile
  rolesClaim: groups
  scopes: [groups]         # added to openid profile email offline_access by maku login
  groups: {admin: agents-admin, member: agents-member}
  mcp:
    sponsoredClientClaim: ""   # generic only: cid (Okta), appid (Entra), azp or client_id
```

The loader refuses a mix of keys from the two presets, and names both keys:

- `projectIDFile` under `generic` names `audienceFile`;
- `audience` under `zitadel` names `projectIDFile`;
- `generic` without `rolesClaim` is refused;
- `tokenUse: access` under `generic` needs exactly one of `audience` and `audienceFile`;
- a sponsored principal under `generic` needs an audience too.

Without `jwksURL`, the broker reads `jwks_uri` from `<issuer>/.well-known/openid-configuration` at
its first token. The document's `issuer` must equal the configured one exactly. An IdP that is
down does not stop the broker: until it answers, humans are refused.

### The maku chart: `broker.oidc`

```yaml
broker:
  oidc:
    existingSecret: room-broker-oidc
    keys: {web: web-client-id, cli: cli-client-id, project: project-id}
```

The chart mounts the Secret at `/etc/room-broker/oidc`. It fills `human.clientIDFile`,
`human.roomctlClientIDFile` and, under `zitadel`, `human.projectIDFile` with their paths, wherever
the human block leaves them unset. A path the block sets wins. The ids are read at use, so a
rotated Secret needs no restart.

### The factory: `config.api`

The factory has the same keys: `preset`, `humanIssuer`, `tokenUse`, `audience`/`audienceFile`,
`rolesClaim`, `groups` and `sponsoredClientClaim`. `humanJWKS` is optional with an https
`humanIssuer`.

The broker forwards each human's token to the factory, which verifies it again, so the two must
agree. The umbrella chart fails a render where the following differ, naming both keys:

| Broker key | Factory key |
|---|---|
| `maku.broker.human.preset` | `maku-factory.config.api.preset` |
| `maku.broker.human.issuer` | `maku-factory.config.api.humanIssuer` |
| `maku.broker.human.tokenUse` | `maku-factory.config.api.tokenUse` |
| `maku.broker.human.rolesClaim` | `maku-factory.config.api.rolesClaim` |
| `maku.broker.human.groups` | `maku-factory.config.api.groups` |
| `maku.broker.human.audience` | `maku-factory.config.api.audience` |
| `maku.broker.human.mcp.sponsoredClientClaim` | `maku-factory.config.api.sponsoredClientClaim` |

The check applies once the factory takes human tokens, that is when `humanIssuer` is set. It
cannot cover the client ids, nor an `audienceFile`: they are files mounted from Secrets and read
at use, so the render never sees their content. Keep the two sides' Secrets in step yourself.

### `maku`

`maku configure` reads `preset`, `tokenUse`, `scopes` and `audience` from the broker's
`/.well-known/roomctl`. With `--from`, it shows each of them before it writes, beside the value it
replaces, with the hosts of the two endpoints: a broker cannot redirect a sign-in or change the
token's audience unseen. The issuer is kept exactly as given, a trailing `/` included (Auth0's). Under `generic` it also reads the issuer's device and token endpoints from
its discovery document, and stores them. `maku login` then runs the device grant there.

Under `tokenUse: id`, `maku` stores the ID token and sends it, refreshing it on its own `exp`. A
refresh that returns no `id_token` (OIDC Core makes it optional) fails with
`the IdP returned no ID token on refresh: run maku login`. A refreshed ID token naming another
issuer, subject or audience than the stored one is refused (OIDC Core 12.2). Every endpoint maku
sends a token request to must be `https`, as discovered and again at use. `maku doctor` names an
issuer whose discovery document cannot be read.

### The Slack App

```yaml
preset: generic
issuerURL: https://idp.example.com
clientIDFile: /etc/slack-app/client-id
clientSecretFile: /etc/slack-app/client-secret
tokenURL: ""          # optional: the issuer's discovery document names token_endpoint
scopes: []
audience: maku-api    # sent as a parameter; the broker's and the factory's audience pin it
```

Under `zitadel`, `projectIDFile` stays required and the request is the one it always was. Under
`generic`, `projectIDFile` is refused, and the message names `audience`.

## Token use `id` and the web session

Under `tokenUse: id`, the ID token is the credential:

- **The web:** oauth2-proxy's ID token arrives in `Authorization`, with `aud` holding the web client,
  beside a non-empty `X-Forwarded-Access-Token` that differs from it. The access token is never
  parsed: it may be opaque.
- **The CLI:** maku's own ID token arrives as the bearer, with `aud` holding the CLI client.
- **The factory:** the broker forwards the verified ID token, and the factory checks that its `aud`
  holds one of `clientIDFiles`.

A web session may steer and decide, and the CLI may not (P18). A token with the RFC 9068 `typ`
`at+jwt` is never accepted as an ID token.

**oauth2-proxy must write `X-Forwarded-Access-Token` itself, on every request, overwriting whatever
the client sent, and the broker must never be reachable around it.** Under `tokenUse: id` the
broker tells a web session from a CLI bearer by that header alone:

- oauth2-proxy's `pass-access-token` sets it from the session.
- With `skip-jwt-bearer-tokens`, a bearer it lets through arrives with both headers equal, and is
  never a web session.

A client that reached the broker directly, or through a proxy that forwards a client-supplied
header, could pair the web client's ID token with a header of its own and pass as a web session.
Keep `:8080` behind the proxy: the broker's network policy admits only the proxy, and no Ingress
or Service may route around it.

The expiry is the ID token's `exp`, capped at an hour. Keycloak's ID token lives 5 minutes by
default, so set oauth2-proxy's `cookie-refresh` below the ID token's lifetime.

## oauth2-proxy's arguments

Step 6 of the v4 rollout renders the front door as a template of the umbrella chart, under
`expose.mode`. Until then, configure oauth2-proxy yourself:

| Argument | Value |
|---|---|
| `provider` | `oidc`, with `oidc-issuer-url` = the issuer |
| `client-id` | the web client |
| `pass-authorization-header` | `true`: the ID token in `Authorization` |
| `pass-access-token` | `true`: the access token in `X-Forwarded-Access-Token`, overwriting the client's |
| `skip-jwt-bearer-tokens` | `true`, with `extra-jwt-issuers=<issuer>=<cli client id>`: maku's bearer passes |
| `oidc-groups-claim` | the roles claim (`rolesClaim`) |
| `allowed-group` | `agents-admin` and `agents-member` |
| `scope` | `openid profile email`, plus the preset's scopes. Under `zitadel`, add the project scope |
| `cookie-samesite` | `strict` |
| `cookie-refresh` | below the ID token's lifetime |
| `skip-auth-route` | `GET=^/.well-known/roomctl$` |

## The IdPs

Two IdPs were run through `hack/oidc-probe/probe.sh` on 2026-11-13. The others are documented
from their vendors' references, not run.

| IdP | Tested | `rolesClaim` | Notes |
|---|---|---|---|
| Keycloak 26 | probed | `groups`, with a group-membership mapper on each client (`tokenUse: id`); `/realm_access/roles` (`tokenUse: access` only) | Realm roles are in the access token, not the ID token. The access token has no `aud` of its own, and a client-credentials token's is `account`, so `tokenUse: access` and a sponsored principal need an audience mapper that adds `audience`. `azp` names the client |
| Dex v2.41 | probed | `groups` (from a connector that carries groups, with the `groups` scope) | The ID token's `aud` is the client, with no `azp`. Refuses the client-credentials grant (`unsupported_grant_type`, v2.43 too), so it has no sponsored principal, and the Slack App cannot sign in |
| Okta | not run | `groups` (a groups claim on the authorization server) | Client credentials name the client in `cid`: `sponsoredClientClaim: cid` |
| Microsoft Entra ID | not run | `roles` (app roles) | `groups` carries GUIDs, which also work as `groups.admin` values. Client credentials name the client in `appid`: `sponsoredClientClaim: appid` |
| Auth0 | not run | `https://<namespace>/roles` (a claim name) | `tokenUse: access` with `audience` = the API identifier, which maku and the Slack App send as a parameter |
| Authentik | not run | `groups` | A mapper that emits one group gives a single string, which is accepted |

Both probed IdPs return a new `id_token` on refresh and serve a device endpoint. Both therefore
work with `tokenUse: id`, the generic default.

**A residual on Dex.** An access token is told from an ID token only by `typ: at+jwt`. Dex's access
tokens have `aud` = the client, and the probe did not record their `typ`. If it is not `at+jwt`,
then under `tokenUse: id` on Dex a Dex access token is accepted where an ID token is expected. It
is the same person's token for the same client, so it grants no identity the person does not
already hold.

## Moving an install off ZITADEL

1. **Name the preset.** Set `human.preset: zitadel` and `api.preset: zitadel`. Nothing changes.
2. **Create the clients at the new IdP.** You need three:
   - a confidential web client, oauth2-proxy's;
   - a public CLI client with the device grant on;
   - for the Slack App, a confidential client with client credentials.

   Map the two agents groups into one claim.
3. **Put the ids in a Secret.** Point `broker.oidc.existingSecret` at it, with keys `web-client-id`
   and `cli-client-id`. Drop `projectIDFile`.
4. **Switch the keys.** Set the following on the broker and the factory together (the umbrella
   checks that they agree), and on the Slack App:
   - `preset: generic`;
   - `issuer` (`humanIssuer` on the factory);
   - `rolesClaim`;
   - `tokenUse`;
   - an `audience`, where the token use or a sponsored principal needs one.
5. **Repoint oauth2-proxy** with the arguments above.
6. **Sign everyone in again.** Each person runs `maku configure --from <rooms URL>`, then
   `maku login`: the old tokens are the old issuer's.
7. **Link GitHub accounts in the broker.** Without ZITADEL's GitHub IdP link, each person's GitHub
   login comes from the broker's [GitHub account link](github-link.md): turn on
   `broker.githubLink`, and each person opens `/link/github` once.
