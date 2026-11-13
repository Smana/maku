# OIDC probe

`probe.sh` starts Dex and Keycloak in throwaway containers on loopback, signs one user in to each,
and asserts the token shapes the broker's `generic` preset reads (v4 step 5, Task 1). It is a
record of what two common IdPs issue, not a test of Maku: run it again when a version changes.

```bash
bash hack/oidc-probe/probe.sh            # Dex, then Keycloak
bash hack/oidc-probe/probe.sh keycloak   # one IdP
DEX_IMAGE=ghcr.io/dexidp/dex:v2.43.1 bash hack/oidc-probe/probe.sh dex
```

Needs Docker, `curl`, `jq` and `yq`. Each line is `PASS`, `FAIL <what>`, or a `NOTE` recording a
shape that is neither right nor wrong. The exit status is the number of `FAIL` lines.

| Check | What it asserts |
|---|---|
| O1 | Discovery names `jwks_uri`, `token_endpoint` and `device_authorization_endpoint` |
| O2 | The ID token's `aud` holds the client, and the roles sit at the claim or pointer named; whether `azp` is present is noted |
| O3 | A refresh response carries a new `id_token` |
| O4 | A client-credentials access token is a JWT naming its client in `azp` or `client_id` |
| O5 | The human access token's shape: a JWT (with its `aud` and `azp`) or opaque, noted |

The fixtures:

- `dex.yaml`: static passwords for the password grant, and the `mockCallback` connector, whose
  identity carries the group `authors`, for the roles claim;
- `keycloak.yaml`: the realm `agents`, converted to JSON at start. A web client and a CLI client
  each carry a `groups` mapper, the user `alice` is in the group and holds the realm role
  `agents-member`, and `maku-slack` is a client-credentials client.

## Results, 2026-11-13

| | Dex v2.41.1 | Keycloak 26.0.8 |
|---|---|---|
| O1 discovery, device endpoint | PASS | PASS |
| O2 ID token `aud` | the client alone, no `azp` | the client alone, `azp` the client |
| O2 roles | `groups` in the ID token (`groups` scope) | `groups` in the ID token (the client's mapper); `/realm_access/roles` in the access token only |
| O3 refresh returns `id_token` | PASS | PASS |
| O4 client credentials | FAIL: `unsupported_grant_type` (v2.43.1 too) | PASS: `azp` and `client_id`; `aud` is `account` |
| O5 access token | a JWT, `aud` the client, no `azp` | a JWT, no `aud`, `azp` the client |

What this means for the `generic` preset:

- **`tokenUse: id` works on both**: the ID token's `aud` is the client, and a refresh returns a new
  one, so maku's refresh needs no new login.
- **Keycloak's realm roles are in the access token only.** Under `tokenUse: id`, set
  `rolesClaim: groups` with a group-membership mapper, or add the realm roles mapper to the ID
  token; `/realm_access/roles` is for `tokenUse: access`.
- **Keycloak's access tokens carry no audience of their own** (O5, and `account` under client
  credentials). `tokenUse: access`, and a sponsored principal, need an audience mapper that adds
  the value set as `audience`.
- **Dex (v2.41.1 and v2.43.1) refuses the client-credentials grant**: no sponsored principal (the
  Slack App) on it.
