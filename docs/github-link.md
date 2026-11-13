# GitHub account link

Each human links their GitHub account to their rooms account once, in the broker, through GitHub's
OAuth web flow. The broker stores the numeric GitHub id GitHub proved, never a token. Rooms, the
factory and the Slack App resolve that id to the current login at each use, through the factory
App, so a renamed account keeps working. The link replaces ZITADEL's GitHub IdP link and the
`human.access.readerFile` / `api.access.readerFile` reader that read it.

## Turning it on

1. **Pick the OAuth client.** The factory App's own (a GitHub App has a client id and a client
   secret: *Settings → Developer settings → GitHub Apps → the App → Client secrets*), or a dedicated
   OAuth App. Register `<publicURL>/link/github/callback` as its callback URL.
2. **Store it in a Secret**, keys `client_id` and `client_secret`, in the broker's namespace.
3. **Set the chart values**:

   ```yaml
   broker:
     githubLink:
       existingSecret: room-broker-github-link
       # webURL: https://ghes.example.com   # a GHES; its API is <webURL>/api/v3 (ROOMS_GITHUB_API_URL)
       # activeWindow: 168h
     human:
       access: {ttl: 5m}   # the room-access check; readerFile is optional once the link is set
   ```

   The chart mounts the Secret at `/etc/room-broker/github-link` and fills `human.githubLink` (keys
   the human block already sets win). The broker's egress gains GitHub on 443: `github.com` and
   `api.github.com` by name in `networkPolicies.mode: cilium`, any public address in mode
   `kubernetes`, where a NetworkPolicy cannot name a host.
4. **Let the callback through the front door.** oauth2-proxy needs
   `skip-auth-route: GET=^/link/github/callback$`: its session cookie is `SameSite=Strict`, so the
   redirect from github.com arrives without it. The callback links nothing (below).
5. **The factory** reads the links from the broker: set `maku-factory.config.api.access.links: broker`
   (it needs `broker.url`).
6. **Each human opens `/link/github`** once and presses *Link GitHub*.

`human.githubLink` is refused in `human.mode: local` (the loopback user is an admin with no GitHub
account) and needs an `https` `publicURL`.

| Key | Default | Meaning |
|---|---|---|
| `human.githubLink.clientIDFile`, `clientSecretFile` | (the chart's mount) | The OAuth client, read at each use: a rotated Secret needs no restart |
| `human.githubLink.webURL` | `https://github.com` | GitHub's site: the browser's authorize page and the code exchange |
| `human.githubLink.activeWindow` | `168h` (at most `720h`) | How long a link stays active without its human signing in |

## The flow

```mermaid
sequenceDiagram
  participant B as Browser (web session)
  participant R as Broker
  participant G as GitHub
  B->>R: POST /link/github/start (same origin)
  R->>R: state, nonce; store sha256(state), sha256(nonce), sub
  R-->>B: 303 to GitHub's authorize, cookie __Host-maku-ghlink=nonce
  B->>G: authorize (state, S256 challenge)
  G-->>B: 302 to /link/github/callback?code&state
  B->>R: GET /link/github/callback (cookie, no session)
  R->>G: exchange code + PKCE verifier, GET /user, revoke the token
  R-->>B: "GitHub says you are @login: link?" (nothing linked yet)
  B->>R: POST /link/github/confirm (same origin, web session, cookie)
  R->>R: link sub to the GitHub id, append the event
```

- **Start** needs the web UI's session (the CLI and MCP bearers are refused `not_web`) and the
  broker's own `Origin`. It makes a 32-byte `state` and a 32-byte nonce, stores only their SHA-256
  with the `sub`, at most 3 flows per `sub`, 10 minutes each, and sets the nonce as the cookie
  `__Host-maku-ghlink` (`Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`). The `__Host-`
  prefix keeps a sibling subdomain from planting its own nonce in the browser (cookie tossing).
- **The PKCE verifier** is derived from the nonce and the state, and stored nowhere: only the browser
  holding the cookie, with the state GitHub sends back, lets the broker compute it.
- **The callback** is the one unauthenticated route. It refuses a state whose nonce is not the
  cookie's (`cookie_mismatch`) before any exchange. It exchanges the code, reads `GET /user` (`id`,
  `login`), revokes the user token at once, records the user on the flow, and renders a confirmation.
- **Confirm** needs the web session whose `sub` started the flow (`sub_mismatch` otherwise) and the
  cookie again. It writes the link and its event and consumes the flow.

This defeats a link CSRF either way: an attacker's flow confirmed in the victim's session is
`sub_mismatch`, and a victim's flow completed in the attacker's browser has no cookie.

**Uniqueness.** One GitHub id per `sub`, one `sub` per GitHub id. Linking an id another account holds
is refused (`github_account_taken`): an admin unlinks it first. Relinking one's own account replaces
its link. Logins are kept as GitHub names them, an Enterprise Managed User's `handle_shortcode`
included.

## Revocation

| Who | How | Event |
|---|---|---|
| The human | *Unlink* on `/link/github` | `unlinked` |
| An admin | `DELETE /api/links/github/{sub}` from a web session in the admin group | `admin_unlinked` |
| Break-glass | `maku-broker github-unlink --sub <sub>`, as `rooms_broker` (`ROOMS_DATABASE_URL`) | `admin_unlinked`, actor `break-glass` |

Every act is kept in the append-only `github_link_events`, which the broker's role cannot rewrite.
Revoking the App's authorization at GitHub changes nothing: no token is held.

## Active, without an IdP admin API

The link row carries `last_seen_at`, which each authenticated request of its human refreshes (at
most hourly). A linked account is active while `now - last_seen_at ≤ activeWindow`.

- A human presenting a token is active by construction: the request refreshes the link first, and
  the IdP stops minting tokens for a deactivated user.
- The Slack App acting for a linked human (`slackDecide`, the factory's self-serve sponsor) is refused
  `not_linked` once that human has not signed in within the window.
- The factory reads the broker's verdict (`active` in `GET /v1/links/github`), so one window rules both.

**The residual** is a deactivated person whose Slack link keeps acting up to `activeWindow` after
their last sign-in. A smaller window shortens it; an admin unlink ends it at once.

## Moving off the ZITADEL reader

With `human.githubLink` and `human.access.readerFile` both set, the link is read first and the
reader's links only for an account with no link. The reader's user state still rules every account,
linked or not, so a deactivation in ZITADEL applies at once, not after `activeWindow`. The broker
drops a ZITADEL link to a GitHub id another account holds through its account link: one GitHub id is
one account across both sources, and the account link wins. The broker logs that `readerFile` is
deprecated.

**The residual during the move.** The factory's run-request API reads the broker's link by `sub`
only, so it cannot tell that a ZITADEL link names an id another account linked in the broker. An
account whose ZITADEL link and another account's GitHub link name the same id can resolve to that
login there until the reader is removed. Moving every human onto the link, then removing both
`readerFile`s, closes it. The factory does the same with `api.access.links: broker` beside `readerFile`. Once every
active human has linked, remove both `readerFile`s; v4 step 8 deletes them.

## Signals

`maku_rooms_github_links_total{outcome}` counts each step and refusal: `started`, `linked`, `relinked`,
`unlinked`, `admin_unlinked`, `state_invalid`, `cookie_mismatch`, `sub_mismatch`,
`github_account_taken`, `github_error`, `not_web`, `too_many_pending`, `bad_origin`, `error`. No log
line, metric, error or page carries a code, a state, a nonce, a verifier or a token.

## The proof

`task chart:identity:k3d` runs the flow end to end on a throwaway k3d cluster, with the e2e stub as
the generic OIDC issuer, GitHub's OAuth web flow and the factory App's API, and no ZITADEL reader: a
member with no link sees no room, links through the stub's redirect and the confirm, reads the room
through the link, and is unlinked again by an admin. The stub counts the revoked user token and
proves no ZITADEL endpoint was called.
