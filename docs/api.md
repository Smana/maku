# API

The broker serves four listeners. Every body is JSON.

| Port | Who calls it | Authentication |
|---|---|---|
| `:8443` (TLS) | The bridge (container `room-bridge`, an identity that keeps its pre-maku name, see [naming](naming.md#technical-names)) in each run pod; system callers such as the factory | Offline JWT: run tokens (audience `room-broker`) or system tokens (audience `rooms-system`) |
| `:9090` | kubelet, `vmagent` | None: probes and metrics only |
| `:8080` | Humans, through oauth2-proxy | ZITADEL ID token and access token |
| `:8090` | Agents' `room_*` tools, through the `agent-router` Gateway only | Injected key plus the gateway's verified `x-ar-agent` |
| `:8085` (bridge) | kubelet | None |

`maku-broker serve` serves `:8443`, `:9090`, `:8080` and `:8090`.

## `:8443` — bridge and system API

Served with `ListenAndServeTLS` on the pair cert-manager writes to `/etc/room-broker/tls/`. The
broker re-reads the files when they change, so a renewal needs no restart, and a plain-HTTP request
fails. See [security](security.md#tls-on-8443) for the certificate and its CA.

Errors are JSON, `{"error": "<reason>"}`, with the status codes below. The reasons are stable
strings, the `wire.Reason*` constants: branch on them, not on the status text.

`hello`, the events endpoint and both system endpoints limit each principal to 10 requests a second (burst
20) and 10 in flight, the per-principal numbers. Over either, they answer `429 rate_limited` with
`Retry-After: 1`, and the caller retries.

### Authentication

| Caller | Presents | The broker checks |
|---|---|---|
| A run's bridge | `Authorization: Bearer <token>`: a projected ServiceAccount token, audience `room-broker`, 600 s, re-read from its file before every request | Signature against the JWKS of an issuer in `runIssuers`; audience; expiry; `sub` matches the issuer's `subPattern`, which yields the run id. Then the run must be live in the `AgentRun` watch (not terminal, not revoked) and have a `roomRef`. The room is the run's `roomRef`, never a request parameter |
| A system caller | `Authorization: Bearer <token>`, audience `rooms-system` | Signature against `systemIssuer`; audience; expiry; `sub` must be a key of `systemPrincipals`, whose value becomes the principal, e.g. `system:factory` |

`systemIssuer` is optional: only the factory calls these routes, and a broker without one still
builds a system authenticator that never authenticates, so the system routes answer `401` rather
than disappearing. It takes `mode: jwks` (the default, verifying against `issuer` and `jwksURL`) or
`mode: tokenreview`, which asks the apiserver and needs no reachable issuer. `systemPrincipals`
requires it: without an issuer no system caller could ever be vouched for, and the config is
refused ([integration](integration.md#the-brokers-configuration)).

No call reaches Kubernetes' TokenReview unless an issuer says `mode: tokenreview`: every other token
is validated offline and issuer-agnostically. A run
that ends, is revoked or is deleted has its streams cut on the watch event.

### Endpoints

| Method and path | Caller | Does |
|---|---|---|
| `POST /v1/bridge/hello` | Bridge | Claims the room's bridge lease, returns where the log is |
| `POST /v1/bridge/events` | Bridge | Appends a batch of harness items |
| `GET /v1/bridge/stream` | Bridge | One SSE stream down: pings; `deliver`, `interrupt` and `decision` |
| `POST /v1/bridge/approvals` | Bridge | Asks for a human decision on a pending action |
| `GET /v1/rooms/{id}/events` | System | Reads a room's log |
| `POST /v1/rooms/{id}/messages` | System | Appends `message{kind: task_state}` |
| `POST /v1/rooms/{id}/task` | System | Appends the task's facts, `state_changed{kind: task}` |
| `POST /v1/rooms/{id}/queue` | System | Queues a chat message for the room's next run |
| `GET /v1/rooms/{id}/queue` | System | Lists the messages still queued |
| `POST /v1/rooms/{id}/queue/consume` | System | Marks queued messages consumed by a run |

### `POST /v1/bridge/hello`

No body. Response `200`:

```json
{"roomId": "3kq7x2ma", "afterHarnessSeq": 36, "afterStatusSeq": 4, "approvals": {"profile": ""}}
```

`afterHarnessSeq` and `afterStatusSeq` are the highest keys already stored for this run's two
streams; the bridge resumes after them. `approvals` carries the room's approval profile
and is empty when no approval profile is set.

| Status | `error` | When |
|---|---|---|
| `401` | `unauthenticated` | No token, a bad signature, a wrong audience or issuer, an expired token, a `sub` that names no run |
| `403` | `run_not_live` | The run is terminal, revoked, deleted, or not yet in the watch |
| `403` | `run_has_no_room` | The run has no `roomRef` |
| `429` | `rate_limited` | Over the run's limits, shared with its batches |
| `409` | `room_busy` | Another run holds the room's lease, and its run is live. The broker also appends `state_changed{kind: limit, reason: concurrent_run}` |
| `410` | `sealed` | The room is closed: its log takes no more events. The bridge never releases its harness, and the pod fails |
| `503` | `no_room` | The room's row does not exist yet: its `Room` has not been reconciled |
| `503` | `log_unavailable` | The database is unreachable |

### `POST /v1/bridge/events`

Request, at most 2 MiB; the bridge sends up to 100 items:

```json
{"items": [
  {"stream": "events", "seq": 36, "type": "tool_call",
   "payload": {"callId": "call_42", "tool": "terminal", "args": {"command": "ls docs"}, "risk": "LOW", "decidedBy": null}},
  {"stream": "status", "seq": 5, "type": "state_changed",
   "payload": {"kind": "harness_status", "status": "finished", "previous": "running"}}
]}
```

`stream` is `events` (harness events, idempotency scope `agent:<runId>`) or `status` (status
transitions, scope `agent:<runId>:status`); `seq` is the item's key in that scope, greater than 0.
Each payload is redacted, object keys included, then checked against the bridge allowlist: the
check reads the redacted payload, the one stored. The whole batch is checked before anything is
written, so a refused item leaves nothing behind. A payload's keys must each have one spelling:
Go readers fold case (`Delivery`, `ſtatus` and a Kelvin-sign `K` all match), jsonb readers do not, so
a key that folds onto another or onto a field of the type's envelope struct without being spelled as
it is refused. A `message` is stored as its envelope struct re-marshals it; a chat's `verdict` and
`commit` are dropped. An item whose keys are one once redacted (two tokens as keys of an env dump)
cannot keep either value: that item alone is stored as a `{"refused": true, "type": …, "reason": "key_collision"}` stub and the
rest of the batch is appended.

Response `200`: `{"afterHarnessSeq": 36, "afterStatusSeq": 5}`, the highest key of the batch on each
stream, or `0` for a stream the batch did not carry. A replayed key is acknowledged without
appending again. An empty batch, `{"items":[]}`, is the bridge's heartbeat: it renews the lease
and nothing else, and the bridge sends one when it has pushed nothing for 30 s.

| Status | `error` | When | The bridge then |
|---|---|---|---|
| `400` | `bad_batch` | Not JSON, an unknown field, or data after the batch | Never drops it: halves the batch until the refused item is alone, then keeps its slot with a `state_changed{harness_event, harnessKind: refused}` stub naming the broker's reason. If even the stub is refused, it retries with backoff and logs at Error |
| `400` | `bad_item` | Unknown type or stream, `seq` ≤ 0, a type or `state_changed` kind a bridge may not push ([allowlist](event-envelope.md#state_changed-kinds)), or a key spelled two ways | As for `bad_batch` |
| `400` | `bad_payload` | The payload is not a JSON object | As for `bad_batch` |
| `413` | `batch_too_large` | Over 2 MiB or over 500 items | Halves the batch; a lone item still too large keeps its slot with a size stub |
| `401` | `unauthenticated` | As for `hello` | Re-reads its token and retries |
| `403` | `run_not_live`, `run_has_no_room` | As for `hello` | Retries on the next tick |
| `409` | `lease_lost` | This run no longer holds the room's lease, seen by the lease renewal or by the append's fence. Nothing is appended | Must not drop the batch: the events are not in the log. Keeps it and says hello again |
| `410` | `sealed` | The room is sealed | Stops mirroring |
| `429` | `rate_limited` | Over the run's request rate or requests in flight | Must not drop the batch: retries after `Retry-After` |
| `503` | `log_unavailable`, `timed_out` | The database refused the append, or the request's 30 s ran out before the batch was redacted | Retries; keeps buffering |

A payload Postgres refuses outright (SQLSTATE class 22) is not an error: it is stored as a
`{"refused": true, "type": …, "reason": "invalid_value"}` stub so the cursor moves on.

### `GET /v1/bridge/stream`

A Server-Sent Events stream the bridge opens and holds (sandbox-initiated, no WebSocket).
Response `200`, `Content-Type: text/event-stream`. The broker writes a comment line, `: ping`, every
30 s. The stream ends at the token's expiry, and the bridge re-dials with a fresh token. A stream
carries only the events addressed to its own run.

Deliveries are derived from the log, never held in a replica's memory. On every connect the broker
subscribes to the room, reads the run's last acknowledgement (the highest `ref` of its `delivered`,
`interrupted` and `undeliverable` acks that is a delivery of that run), and sends the run's
deliveries after it before anything else, in the stream's first flush; then it follows the room live.
Both reads go through partial indexes, never the room's whole log. A stream whose acknowledgement it
cannot read is refused `503`, rather than replay every delivery from the start. An event's `data`
lines are bounded to 256 KiB together.

| Outcome of an injection | The bridge |
|---|---|
| Accepted by the harness | Acknowledges `delivered` or `interrupted` |
| Refused for good: steering with a 4xx other than 404, 408, 409, 425, 429; an interrupt with any 4xx | Acknowledges `undeliverable` with the harness's `code`, and goes on: one refusal must not block every later `ref`. An interrupt of an idle conversation is moot, and a late one would land on a turn the driver never meant |
| Failed for now (no answer, 5xx, or steering refused with one of those 4xx) | Ends the stream; the replay hands the same `ref` over again before any later one |

**Delivery is at least once** (a crashed bridge re-delivers). A bridge injects each `ref`
once per process, but one that restarts, or shuts down while a `deliver` is in flight, after
injecting a `ref` and before its acknowledgement reaches the log, injects it again: the transcript
shows a second user message. A replayed `interrupt` lands on whatever turn is current then, which can
be a later turn than the one the driver meant.

Acknowledgements are the bridge's own claims: the broker cannot see inside the
sandbox. A `ref` counts only if it is a delivery of that run, so a forged one past every delivery
skips nothing; a compromised sandbox can still hide steering from its own run, as it could by ignoring
the harness. The log may then show `delivered` for a message never injected.

| SSE `event` | `data` | Meaning |
|---|---|---|
| `deliver` | `{"ref": 1846, "text": "…"}` | A steering message; the bridge injects it into the harness and acknowledges it as `state_changed{delivered, ref}` |
| `interrupt` | `{"ref": 1847}` | The driver interrupted the run; acknowledged as `state_changed{interrupted, ref}` |
| `decision` | `{"approvalId": "…", "allow": true, "reason": "…", "ref": 1851}` | An approval was decided; acknowledged as `state_changed{decision_applied, ref}` |

Errors before the stream opens are those of `hello`.

### `POST /v1/bridge/approvals`

Request, at most 64 KiB: `{"eventId": "e42", "callId": "call_97", "class": "forge.pr", "action": {…}}`.
`eventId` is the harness ActionEvent's id. `class` is one of `forge.push`, `forge.pr`, `forge.other`,
`mcp.write`, `shell.high`. The action is redacted and stored as the approval card shows it.
Response `200`: `{"approvalId": "…", "expiresAt": "…"}`, with `expiresAt` always set: 30 minutes for
an `attended` room, the room's `approvals.ttl` (default `4h`) for an `unattended` one. The request is
idempotent per `(room, run, eventId)`, never per `callId`: a model provider may reuse a tool call id,
and the bridge rejects a step whose ack names an approval it already spent. The request is fenced on
the room's bridge lease, like the run's other appends, and recorded as `approval_requested`. It names
the run's prompters for four-eyes: whoever requested the run (and its `spec.principal`), steered it,
or wrote a queued message its brief consumed or that was promoted to it.

An approval whose `callId` already has a `tool_result` in the log after its request (the harness ran
or rejected the call), or whose run has left the room since (the broker's `participant{left}`), is
closed as `superseded`, never left for an approver: by the leader's sweep every 30 s, or at once when
an approver decides it. A superseded decision is never sent to the
bridge. An expiry is decided `allow: false` with `reason: "expired"`: at the first decision attempt past `expiresAt`, else by the sweep within 30 s of it.

| Status | `error` | When |
|---|---|---|
| `400` | `bad_approval` | Not JSON, an unknown field, no `eventId` or `callId` (or one over 256 bytes), or an unknown class |
| `400` | `bad_action` | The action is not a JSON object, its keys collide once redacted, or it is over 63 KiB redacted |
| `401`, `403` | as for `hello` | |
| `409` | `lease_lost` | Another run holds the room's bridge lease |
| `410` | `sealed` | The room is sealed |
| `429` | `rate_limited` | Over the run's limits |
| `503` | `log_unavailable`, `timed_out` | The log or the run's prompters could not be read or written |

### `GET /v1/rooms/{id}/events`

For system callers. Query: `afterSeq` (default `0`), `limit` (1 to 500, default `100`). Response `200`:

```json
{"events": [ {"v": 1, "seq": 1, "type": "state_changed", "…": "…"} ], "lastSeq": 1842}
```

`events` are full [C4 envelopes](event-envelope.md) with `seq > afterSeq`, in order; `lastSeq` is
the room's current high-water mark. Page by passing the last `seq` you received as `afterSeq`.

| Status | `error` | When |
|---|---|---|
| `400` | `bad_room` | `{id}` is not a C2 id |
| `401` | `unauthenticated` | Bad, expired or wrong-audience token |
| `403` | `not_permitted` | A valid token whose `sub` is not in `systemPrincipals` |
| `404` | `no_room` | No such room in the log |
| `429` | `rate_limited` | Over the principal's limits |
| `503` | `log_unavailable` | The database is unreachable. Never a `lastSeq` of 0 in its place |

### `POST /v1/rooms/{id}/messages`

For system callers; appends the reserved kind the factory owns. Request, at most 32 KiB:

```json
{"kind": "task_state", "text": "Reviewing", "clientSeq": 1}
```

`kind` must be `task_state`, `clientSeq` greater than 0, `text` not blank and at most 16 KiB; the text is redacted.
Response `201`: `{"seq": 1843}`.

The idempotency key is `(principal, clientSeq)` alone: a replay answers `200` with the original
`seq`, **even when its body differs**. The new body is not stored and no error says so, so a caller
never reuses a `clientSeq` for another message.

| Status | `error` | When |
|---|---|---|
| `400` | `bad_room`, `bad_message` | A bad id; any other kind, `clientSeq` ≤ 0, text over 16 KiB, or a malformed body |
| `401`, `403` | `unauthenticated`, `not_permitted` | As for reads |
| `404` | `no_room` | No such room |
| `410` | `sealed` | The room is sealed |
| `429` | `rate_limited` | Over the principal's limits |
| `503` | `log_unavailable`, `timed_out` | The database refused it, or the request ran out of time |

### `POST /v1/rooms/{id}/task`

For system callers: the factory's facts about the room's task, stored as
[`state_changed{kind: task}`](event-envelope.md#state_changed-kinds) so the
[summary](#get-apiroomsidsummary) reads the room log alone. Request, at most 32 KiB:

```json
{"clientSeq": 7, "facts": {"phase": "Implementing", "run": {"id": "cf4ato2x", "role": "implementer"},
  "budget": {"usedTokens": 189093, "limitTokens": 1500000},
  "issue": {"number": 42, "url": "https://github.com/Smana/cloud-native-ref/issues/42"}}}
```

`facts.phase` is required, token counts are not negative, and `issue.url` and `pr.url` are
github.com issue and pull request URLs. The facts are redacted. Response `201`: `{"seq": 1845}`.
Replays are keyed as for `/messages`, `(principal, clientSeq)`, but under the origin
`<principal>:task`: the two `clientSeq` spaces never collide. Refusals as for `/messages`; facts
that do not validate are `400 bad_message`.

A broker older than the route (v0.7) answers a plain-text `404`. The factory takes that as a wait,
warns once, and keeps the facts due until the broker serves the route: upgrade the broker first.

### The queue routes

For system callers: the factory turns a maintainer's GitHub review into a queued
message, and consumes it once a run's brief quotes it. Every route answers `501 no_queue` on a broker
built without a queue store, and otherwise refuses as the routes above do: `400 bad_room`,
`401`, `403`, `404 no_room`, `410 sealed`, `429`, `503 log_unavailable`.

`POST /v1/rooms/{id}/queue`, at most 32 KiB:

```json
{"text": "GitHub review by @Smana: use the relative link", "clientSeq": 901, "stream": "review"}
```

The text is redacted and stored as `message{kind: chat, delivery: queued}`, the caller its actor and
`<principal>:queue:<stream>` its origin. `stream` is `[a-z]{1,16}`, default `default`: each source
keeps its own `clientSeq` space, apart from the caller's `task_state` messages. Response `201`:
`{"seq": 1844}`. Replaying the stream's highest `clientSeq` answers `200 {"duplicate": true}`.
A lower `clientSeq` need not be a replay, since GitHub review ids follow creation order, not
submission order. It is stored as a new message, unless its exact key is already stored: then the
answer is `201` with the stored `seq`. Two identical requests racing can both answer `201` with
the same `seq`. Either way, nothing is stored twice.

`GET /v1/rooms/{id}/queue` answers `200 {"queued": [{"ref": 1844, "author": "system:factory",
"text": "…"}]}`, oldest first; `ref` is the queued message's `seq`.

`POST /v1/rooms/{id}/queue/consume` `{"refs": [1844], "runId": "7f3cq2xz"}` moves each ref still
queued to `consumed` by that run and answers `200 {"consumed": n}`, the refs this call moved. A ref
already consumed, promoted, removed or never queued is skipped, so a retry is safe.

| Status | `error` | When |
|---|---|---|
| `400` | `bad_message` | No text, text over 16 KiB, `clientSeq` ≤ 0, or a malformed body |
| `400` | `bad_stream` | A `stream` that is not `[a-z]{1,16}` |
| `400` | `bad_consume` | A `runId` that is not a C2 id, more than 100 refs, or a malformed body |
| `501` | `no_queue` | No queue store wired |

## `:9090` — probes and metrics

| Path | Answers `200` when | Used as |
|---|---|---|
| `GET /healthz` | The process is up. Body: `ok <version>` | Liveness |
| `GET /readyz` | Postgres answers a ping and the Kubernetes caches are synced | Readiness |
| `GET /startupz` | The schema is migrated (`events` exists) | Startup: the first deploy waits for CNPG and the Atlas migration |
| `GET /metrics` | Always | Prometheus metrics ([operations](operations.md#metrics)) |

The bridge serves `GET /healthz` on `:8085`, for kubelet only, and no metrics. It reports unhealthy only when the
harness answered once and has been unreachable for more than 60 s: as a native sidecar its startup
probe gates the harness container, so it must never wait for the harness. It never
checks the broker, so a broker outage cannot mark sandboxes unready.

It also serves `GET /admission`, read by `maku-bridge gate` on loopback:

| Status | Body | When |
|---|---|---|
| `503` | `pending` | No hello has been decided yet: the broker is unreachable, or another run's live run has held the room for less than 3 minutes |
| `200` | `admitted` | The bridge holds the room's lease. The gate exits 0 and the harness starts |
| `409` | `room_busy` or `sealed` | The run will never hold the room. The gate exits 1, which fails the pod before the harness runs |
| `403` | `loopback only` | The request came from outside the pod |

## `:8080` — human API

Reached only through oauth2-proxy on `rooms.<private domain>`. Every request carries the human's
ZITADEL **ID token** in `Authorization` and their **JWT access token** in `X-Forwarded-Access-Token`,
both from oauth2-proxy.

| Check | Rule |
|---|---|
| ID token | Issuer is the identity provider, audience holds the `rooms-proxy` client id, not expired |
| Access token | Same `sub`; issued for `rooms-proxy`. A web session requires it to differ from the ID token |
| Groups | `agents-admin` or `agents-member`, else `403` |
| User state | The ZITADEL user must be active, admins included: an inactive, locked, deleted or initial user is refused `401 user_inactive` on every route, and an open WebSocket closes `1008 user_inactive`. Read with the room-access reader, cached with its answers for at most 5 minutes; past that, a state ZITADEL cannot give fails closed (`503 access_unverified`). Counted in `maku_rooms_rejected_actions_total{reason="user_inactive"}` |
| Room access | `agents-admin` sees every room. Anyone else sees a room only if GitHub lets the login linked to their ZITADEL user read the room's `repository`; a room with none is admins-only. A closed room, whose `Room` the factory deleted when its task ended, stays readable for its retention under the same check, on the repository its factory task facts name; it is not listed in `GET /api/rooms`, and its owner and members are gone with the `Room`. Answers are cached for at most 5 minutes; past that, a check ZITADEL or GitHub cannot answer fails closed. A room you may not see answers exactly like a missing one |
| `Origin` | Must match the room host: no cross-site WebSocket |
| Lifetime | A connection lasts `min(token expiry, 1 h)`, then closes for re-authentication |

`maku` presents a bearer token from its own native ZITADEL client; oauth2-proxy lets it
through with `skip-jwt-bearer-tokens`. Such a token can read, post, queue and fork, but the broker
refuses `decide`, `message{steering}`, `interrupt` and every `driver_*` action from it. Its text is
[machine-sent](#machine-sent-text).

Errors before a WebSocket upgrade are plain-text HTTP errors.

| Method and path | Does |
|---|---|
| `GET /`, `GET /r/{id}`, `GET /assets/{file...}` | The embedded UI, under a strict Content Security Policy. `/assets/` serves nested files (`/assets/svg/…`, `/assets/fonts/…`); a directory, `/assets` or `/assets/` is a `404`, never a listing. A room's page keeps its newest 5 000 events; older ones leave the page, never the log. A `401` from the API, or one behind a refused WebSocket upgrade, sends the page to `/oauth2/start?rd=<the page>` |
| `GET /api/rooms` | One row per room the caller may read (room access, then their role): id, `url` (the room's page), phase, `state` (the [coarse state](#the-coarse-state)), owner, driver, data class, last `seq`, `lastEventAt` (when the log last moved; omitted when the room has no events or the time could not be read), `repository`, the task (`issue`, `title`, `task` phase, `pr`; absent when the room has none), `reason` (the task's wait code from its last facts, while its phase is `Queued`, `AwaitingCI`, `AwaitingHuman` or `Escalated`; absent otherwise or when it cannot be read), `needsYou` (how many things wait for the caller), `needsMe` (`needsYou` > 0: an approval is pending that the caller could decide in the web UI, or the factory's task is held on the room and the caller is its driver or owner — the summary's `kind: hold`, or a PR waits for the caller's merge — `kind: merge`, or an Escalated task waits for its issue's author and the caller owns the room — `kind: answer`; an approval past its deadline that the sweep has not decided yet counts for neither `needsYou` nor `need`, as the summary drops it), `need` (the first of those things, in the summary's order: the pending approval with the lowest id, then a hold, then a merge, then an answer; the same `kind`, `id`, `what`, `deadline?` and `url` as a `needsYou` item; absent when nothing waits for the caller), `status` (`{kind, icon, text}`: the room page's [status line](web-ui.md#the-room-list) for the caller, from the same `summary.StatusLine` and the same cases as the room page and `maku status`, fed from the row's own data: the run from the broker's run watch, a failed row's reason code from one store read for every failed row listed, and the approvals the caller may decide (an approver who prompted the turn under four-eyes may not) against those only others may; fixed phrases and numbers, never agent text; `kind` is one of `running`, `queued`, `waiting-you`, `waiting-other`, `waiting-pr`, `waiting`, `finished`, `done`, `failed`, `stopped`; absent when the state is one this broker does not know), and the caller's own role. A room whose access cannot be verified is left out, with no error. The `X-Rooms-Access` header says why a list may be short, about the caller only: `unlinked` (not an admin, and no GitHub link on their ZITADEL user), `unverified` (a check failed past the cache, or none is configured), else `ok`. Filters, applied after the access and role checks so none reveals an unreadable room: `?repo=owner/name`; `?mine=1`, rooms whose task's issue the caller filed or labelled or whose PR they authored or review, matched on their linked GitHub login; `?needs_me=1`, rows with `needsMe`. `429 rate_limited` past the caller's read budget: 10/s, burst 20, per replica, shared with the summary and apart from acts |
| `GET /api/rooms/{id}/summary` | The room's [`summary/v1`](#get-apiroomsidsummary): status, what needs the caller, what they can do, the agents' notes |
| `GET /api/rooms/{id}/brief` | The room's resume brief: [`brief/v1`](#get-apiroomsidbrief), the text `room_brief` returns and the cursor to ask from next |
| `GET /api/rooms/{id}/events` | A page of the room's log for the run view: [`events`](#get-apiroomsidevents) |
| `GET /api/needs` | What needs the caller, across every room they may read: [`needs/v1`](#get-apineeds), in the one order, with the cursor to ask from next. Served while `human.needs.enabled` (the default); off, `404`, and `needs_list` is not listed either |
| `GET /v1/ws?room=<id>` | The live room, over WebSocket |
| `POST /mcp` | The [MCP server](#post-mcp) `maku mcp` relays, when `human.mcp.enabled`; else `404` |
| `POST /api/slack/decisions` | The Slack App's one deciding route, when `human.slackDecide`: `{team, user, channel, messageTS, approvalId, branch, decision}` → `{"result": "<code>"}`. Bearer: the App's sponsored principal only. 200 with a closed code for every refusal, `400` for shape, size or content type, `429` over 10 a minute (burst 5, per `(team,user)`, per replica), `503` when the store, the room or the log cannot be read. `forge.push` only; the human is resolved from `slack_links`, and must be an approver or owner of the room *and* write on its repository |
| `POST /api/rooms` | `{"dataClass": "public", "repository": "Smana/cloud-native-ref"}` → `201 {"id": "…"}`: a new room owned and driven by the caller, any agents member, on a `repository` they can read (room access). `400` for another data class, a missing or malformed repository, an unknown field or a principal the Room CRD would refuse, `404` for a repository the caller cannot read or the factory App cannot see (missing, or the App not installed), `409` when a Room of the generated id exists, `429` past the caller's action budget (10/s, burst 20, shared with acts), `500` when the Room is refused for any other reason, such as the broker lacking `create` on rooms (logged at WARN), `503` when the API server is unreachable, overloaded or times out, or `access_unverified` when the caller's access cannot be checked. The `SameSite=Strict` cookie and the `Origin` check stop a cross-site post |
| `GET /.well-known/roomctl` | The same `{url, issuer, clientID, projectID, version, humanMode}` with no sign-in, for `maku configure --url`; none of it is a secret. `humanMode` is `oidc` or `local` (`local`: no IdP, no token, and the `maku` values are empty). The proxy in front must let this path through unauthenticated |
| `GET /api/roomctl` | `{url, issuer, clientID, projectID, version, humanMode}` for the room list's CLI setup view, any agents member: the values of `maku configure`, and the broker's version, whose maku release the view's install block downloads (the latest when it is not a `v*` build). `clientID` is `""` while the broker has no maku client. maku asks for the project's audience scope with `projectID`. `dashboards?` (`[{label, url}]`): the room page's link templates from [`human.dashboards`](integration.md#the-brokers-configuration), unfilled; the page fills `{room}`, `{run}` and `{task}` per room. Absent when none is configured. They are not secrets, so any member gets them; `/.well-known/roomctl` leaves them out, since `maku configure` has no use for them |

### `GET /api/rooms/{id}/summary`

The room's top layer, folded from its log, which the room page and `maku status` render. It is a
room read: the same room access and role check as `GET /v1/ws`. Query: `after=N` (default `0`)
keeps only the notes after `seq` N; pass back the number of the previous answer's `cursor`.

| Status | When |
|---|---|
| `200` | The summary below |
| `400` | `after` is not a non-negative integer |
| `404` | No such room, or one the caller may not read: the same `no such room` |
| `429` | `rate_limited`: over the caller's read budget (10/s, burst 20, per replica), shared with `GET /api/rooms` |
| `503` | `access_unverified`: ZITADEL or GitHub cannot confirm the caller's access past the cache; or the rooms or the log are unavailable. Retry |

| Field | Holds |
|---|---|
| `apiVersion` | `summary/v1`, the contract both renderers check |
| `room`, `url` | The room id and its page |
| `status` | `state`: the [coarse state](#the-coarse-state), from the log's task phase, else the room's. `reason` and `next` (absent unless the task waits: `Queued`, `AwaitingCI`, `AwaitingHuman`, `Escalated`; absent in a sealed room): why, and what happens or who acts next. `issue.title` is the issue's title, bounded. `phase`: the task's, else the room's, and `Closed` or `Sealed` once the log is sealed. `task`: the room's Task, which the factory names as the room, so the room id; absent in a room without task facts. `run`, `budget`, `pr`, `issue`, `lastVerdict`: the factory's latest facts and the latest review verdict, each `null` when absent. `spend` (`usedMicroUSD`, `limitMicroUSD?`): the task's dollars, in millionths of a dollar, beside `budget`'s tokens; absent until the factory reports it. `spendFinal`: the factory's claim (CD23) that `spend` is the task's last figure — its runs priced again after the settle window, and the figure never bucketed by the facts key — so a surface may print it; absent while the dollars may still move, and for a task whose runs the meter could not price. The card, the brief and the web panel then say `spend settling` — or `spend unpriced` when the task spent tokens the meter could price none of, the case where no final fact is coming — and a sealed room prints the figure its log froze, since it takes no other. Empty fields inside them are left out, such as a PR's `reviewers` before anyone reviews. `reasonCode`: the factory's raw reason code from the last task facts, bounded, kept in a sealed room; the key the status line reads, beside `reason`'s prose. `approvals` (`{mine, others}`): the live approvals the caller may decide in the web UI (not one they prompted under four-eyes) and those only others may, which the status line reads; absent when none is live, and in a sealed room. `runModel` and `lastRunModel` (`{run, model?, effort?}`): what a run runs on. `model` is its `AgentRun` `spec.model`, from the broker's `run_phase{Running, model}`; `effort` is the reasoning effort its harness reported on `harness_status`, the run's claim, bounded to seven values. `runModel` is the run in the room now, `lastRunModel` the run before it, or the last run when none is in the room. Each absent until the log says. The model that served the run is the gateway's `gen_ai_request_model` |
| `runs` | The task's run history, oldest first, at most 20, from the factory's latest facts: `id`, `role`, `trigger?`, `startedAt?`, `endedAt?`, `outcome?` (`succeeded`, `failed`, `budget_exhausted`, `revoked`, `lost` or `ended`; absent while the run goes on), `verdict?` (at most 32 bytes), `microUSD?` (the run's dollars). Absent when the task has none. Kept in a sealed room, like `status.spend`: they are facts, not needs |
| `needsYou` | `kind: merge` (owners; `id` is the PR number): the task is `AwaitingHuman` on a PR; `what` says how to merge under the repository ruleset (the bypass box, or `gh pr merge N --squash --admin --repo owner/name`), `url` is the PR. With `approve: true` the factory's approval returns the hand-off (`merge.approveToMerge`, the task's facts `mergeByApproval`): `what` says to approve the head read, by its commit (`gh api repos/owner/name/pulls/N/reviews -f commit_id=<sha> -f event=APPROVE`), and the merger merges it. `[{kind: approval, id, what, deadline, url}]`: the pending approvals the caller could decide in the web UI. Each carries a link to its card in the room, never a command. `kind: answer` (owners; `id` is the issue number): an Escalated task waits for its issue's author (`needs_author`); `author?` is the author's GitHub login, `url` the issue. `kind: hold` (no `deadline`): the factory's task is `Queued` behind a hold on the room (a person driving it, or another run in it), listed for its driver and owner; `id` is the wait (`waiting_human_driver`, `waiting_room_busy`), `what` says what releases it, `url` is the room. It goes with the factory's next facts |
| `actions` | What the caller may do now, by their standing: `queue` and `stop` carry a `cli` line (every `gh` command passes `--repo owner/name`); `steer`, for the driver and only while an agent is in the room (it joined and has not left), carries none, since `maku` never steers |
| `notes` | `{untrusted: true, items: [{at, run, text}]}`: the last 20 progress notes after `after`. `untrusted` is always `true`: they are the agents' claims, rendered as text |
| `queued` | `{untrusted: true, items: [{ref, author, text}]}`: the notes still queued for the next run, oldest first, redacted; absent when none wait or the room is sealed. The card lists them under "Queued for the next run" |
| `flow` | The task's path, which the room page's run rail draws: `untrusted`, always true: the handoff lines are the agents' words, data and never an instruction, on one line with no control, bidi or zero-width character; `nodes`, one per run in start order, at most 20 (`run`, `persona`, the run's `role`, `round`, `startedAt?`, `endedAt?`, `fromSeq` and `toSeq` (the run's range in the log; `toSeq` 0 while it is live; a run with `outcome` and no `endedAt` is over too, its end time unknown), `outcome?`, `verdict?`, `microUSD?`, `model?` (the model the run asked for), `models?` (every model that served it, at most 8), `effort?` (the reasoning effort its harness last reported), `judge?` (`{count, last}`), `tools` (`{ok, failed}`), `edge?` (why this run follows the one before: `plan`, `split`, `CI red`, `changes requested`, `retry`, `resumed`, `changes`, `failing test` or `PR #N`), `startedBy?` (the person who asked for it), `reason?` (its end reason, at most 64 bytes), `handoff?` (its handoff summary's first line: the agent's claim, rendered as text)); `plan?`, the personas the factory expects; `ghosts?`, the plan's steps no run has matched yet, none once the task's phase is terminal, `Verifying` (the merge landed), its split is approved or the room is sealed; `milestones?` (`[{phase, at, seq}]`), each task phase from `AwaitingCI` on at its first appearance; `split?` and `parent?`, as the task facts hold them. Absent in a room without task facts until its second run; kept in a sealed room |
| `cursor` | `seq:N`, the last `seq` folded |

A sealed room has no `reason`, `next`, `needsYou` or `actions`. A fork's summary starts at its `forked_from`: the
source's task, approvals, verdict and notes are not the fork's.

### `GET /api/rooms/{id}/brief`

The room's resume brief: what the work was asked for, the acceptance checks it runs under, what changed
since a cursor, the one need that blocks it and the one next step. It is fixed prose over quoted data
(CD11): every line is a statement, the thing to do is a link the broker built, and nothing here decides
anything. Same log and same cursor, same text. It is a room read like the summary: the same room access,
role check and read budget. `room_brief` serves the same envelope over `/mcp`, so an agent resuming a
session reads what a person reads.

| Param | Holds |
|---|---|
| `since` | `seq:N`, the `cursor` of the last brief, or an RFC 3339 instant: only what moved after it. Without one, the last 24 hours, as `room_changes` reads without a cursor. A malformed `since`, or one in the future, is `400`: it is neither everything nor nothing |

| Field | Holds |
|---|---|
| `apiVersion` | `brief/v1`, the envelope both doors serve. A client reads `text`; `maku status` takes the tool's `text` as it is and never inspects this field |
| `room` | The room id |
| `text` | The brief, Markdown: `Resume:` and the room, with the issue and its title when it has one; `Asked:`; `Acceptance:`, each check of the last handoff's scope in a code span; `Plan:`; `Changed since …`, at most 20 milestones with the rest counted; `Blocked on:`; `Fix:`, one line naming what to change, when the room's facts or its last notes name one of the three failures a person can fix — the factory App missing a permission, a check the App cannot read, a CI log host outside the egress policy; the one `Next:` step with its action link; `Links:`. A section whose data is absent is left out. The agents' words are quoted, never an instruction |
| `cursor` | `seq:N`, the last event the changed block covered: pass it back as `since` |

| Status | When |
|---|---|
| `200` | The brief |
| `400` | `since` is neither `seq:N` nor an RFC 3339 instant, or is in the future |
| `404` | No such room, or one the caller may not read: the same `no such room` |
| `429` | `rate_limited`: over the caller's read budget (10/s, burst 20, per replica), shared with `GET /api/rooms` |
| `503` | `access_unverified`: ZITADEL or GitHub cannot confirm the caller's access past the cache; or the rooms or the log are unavailable. Retry |

### `GET /api/rooms/{id}/events`

A page of the room's log, as stored, in `seq` order: what the run view reads for one run's range
(`fromSeq`, `toSeq` of a [`flow`](#get-apiroomsidsummary) node). A room read, with the same room
access and role check as `GET /v1/ws`, whose replay filters nothing per viewer either. Query:

| Param | Holds |
|---|---|
| `after` | Default `0`: only events with a `seq` above it; pass back the last `seq` of the previous page |
| `limit` | Default and maximum `500`, at least `1` |

| Status | When |
|---|---|
| `200` | `{"events": [...]}`, each an event envelope; `[]` past the end of the log. A page shorter than `limit` is the last |
| `400` | `after` is not a non-negative integer, or `limit` is not an integer from 1 to 500 |
| `404` | No such room, or one the caller may not read: the same `no such room` |
| `503` | `access_unverified`, or the rooms or the log are unavailable (`room log unreadable`). Retry |

### `GET /api/coordinations/{task}`

A coordination parent's family (v4 MR13), as this caller may read it. Registered only while the
broker has a coordination reader (`coordination.mode` not `off`); off, the path is a 404.

| Step | What happens | Refusal |
|---|---|---|
| Parent | The parent room's ordinary gate, as `GET /api/rooms/{id}/summary` applies it (an admin passes) | the room's 404 or 503 |
| Core view | Read live, uncached, never served as read | 404 not a parent; 503 `coordination view unavailable` |
| Each child | A human's own linked login and GitHub read of **that child's** repository; a sponsored principal's `human.mcp.sponsored[].repositories`. No admin path; a missing link or any error withholds the child | — |

The answer is the core `CoordinationView` with `restrictedChildren` added. A withheld child is only
counted: its plan entry, authorization, live state, artifacts, merge-order entry, incidents, any
recovery naming it, every integration attempt with it as a subject and every reference to it are
removed, and with one withheld the family budget, the primary rationale, the integration acceptance
and every child's integration acceptance and release are cleared too. The room summary carries the
same value as `coordination`, and its flow as `flow.coordination`; `needs/v1` adds the parent's own
`plan_confirmation` and `recovery` items, anchored on `<parent room>#coordination`.

### `GET /api/coordinations/{task}/checkout`

The family's `coordination-checkout/v1` document (MR14), all or nothing: the core
`CheckoutManifest` (each child's repository, room, issue and PR URL, dependencies, produced and
consumed landed artifacts, and exactly one of `headSHA` or `mergeCommitSHA`) plus the integration
attempts. `worktree` is the child id until `maku` fills its path. A caller missing any child gets
the 404 an unreadable room gets; a family not at one valid commit per child is a 409
`checkout_not_ready`.

### `GET /api/needs`

What needs the caller, across every room they may read: one list, one order. `needs/v1` is one
derivation, and the room rows, the room summary, this route, `needs_list` and the web panel all read it,
so no two surfaces can disagree. It is a human token's route, as every route but `POST /mcp`: a sponsored
principal reads its own rooms' needs through `needs_list`, and is `401` here. Served unless
`human.needs.enabled` is off ([the broker's configuration](integration.md#the-brokers-configuration));
off, it is a `404` and `needs_list` is not listed either.

| Param | Holds |
|---|---|
| `since` | An RFC 3339 instant, the `cursor` of a previous answer: only the items whose cause is newer. Never in the future, and never another format: both are `400`, so a mistyped cursor is neither everything nor nothing |
| `room` | One room's items |
| `repo` | One repository's, matched as the room list matches it |

| Field | Holds |
|---|---|
| `apiVersion` | `needs/v1` |
| `items` | One item per thing to do, `[]` when nothing does: `kind` (`plan_confirmation`, `approval`, `question`, `merge`, `review`, `failed_check`, `recovery` or `hold`), `id`, `what` (the move, in prose), `why?` (the factory's reason), `room`, `url`, `action` (`{kind, label, url}`: the one thing to do, and where), `deadline?` (an approval's), `at?` (when the cause landed), `issue?`, `pr?`, `author?` (a question's issue author) and `approve?` (a merge an approval settles: the step says approve the head, never `--admin`). It carries no command: an approval is decided on the room page, every other action on GitHub or the issue, and `action.url` is the link. In the one order, deterministic and never scored: a deadline ascending first, then the kind order above, then the room |
| `cursor` | This answer's instant, truncated to the second: ask with it as `since` |
| `access` | `X-Rooms-Access`' value for the caller: why the list may be short, about the caller alone |
| `unread?` | The rooms whose state could not be read: they contribute no item, and one line of the text says so rather than passing them off as quiet. A room the caller may not read is never named |

| Status | When |
|---|---|
| `200` | The list |
| `400` | `since` is not an RFC 3339 instant, or is in the future |
| `401` | Not a human's token; a sponsored principal's included |
| `404` | `human.needs.enabled` is off |
| `429` | `rate_limited`: over the caller's read budget (10/s, burst 20, per replica), shared with `GET /api/rooms` |
| `503` | `access_unverified`, or the rooms are unavailable. Retry |

An answer counts in `maku_needs_list_total{surface="api", principal, outcome}` (`ok`, `unread`,
`refused` or `error`), and what it held in `maku_needs_items{kind}`. The same list over `POST /mcp`
counts under `surface="tool"`, so one query covers both doors.

### The coarse state

One vocabulary for the room list, `summary/v1` and `POST /mcp` (factory interfaces F7): the task's
phase decides, else the room's. A closed or sealed room stops a task it left unfinished. An idle room
waits for its owner unless a run is on its way. A task phase this broker does not know reads as its
room's. A room with no task reads its own phase: `Closed` is `stopped`, and a room not yet started (no
phase, no task) is `queued`. Each state maps onto one A2A task state, so a later agent card is a mapping.

| `state` | Task phases | Rooms without a task | A2A |
|---|---|---|---|
| `queued` | `Received`, `Triaged`, `Queued` | `Open`, or no phase yet | `SUBMITTED` |
| `running` | `Implementing`, `Reviewing`, `AwaitingCI`, `AutoMerging`, `Merged`, `Verifying` | `Active`; `Idle` with a run requested, pending or suspended | `WORKING` |
| `waiting_for_human` | `AwaitingHuman`, `Escalated` | `AwaitingHuman`; `Idle` with no run on its way | `INPUT_REQUIRED` |
| `done` | `Done`, `NoOp` | — | `COMPLETED` |
| `failed` | `Rejected`, `Reverted` | — | `FAILED`; `REJECTED` for `Rejected` |
| `stopped` | `Stopped`, `Closed`, and any unfinished phase in a closed room | `Closed` | `CANCELED` |

### `GET /v1/ws`

| Status before upgrade | When |
|---|---|
| `401` | Not authenticated, or the token is already past its expiry; `user_inactive`: the ZITADEL user is no longer active |
| `403` | A foreign `Origin`; not in an agents group |
| `404` | No such room, or one the caller may not read: the same `no such room` |
| `429` | More than 10 connections for this person, or more than 20 people in this room (per replica) |
| `503` | The log is unavailable; `access_unverified`: ZITADEL or GitHub cannot confirm the caller's access past the cache |

One JSON object per text frame.

| Direction | Frame | Fields |
|---|---|---|
| client → broker | `hello` | `roomId`, `afterSeq?` (clamped to the mark: the `sync` frame sets the baseline) or `tail?` (default: the last 500 events). Must be the first frame, else the socket closes `1008 hello first` |
| client → broker | `act` | `clientSeq`, `action`, `driverEpoch?` |
| client → broker | `ping` | Every 30 s |
| broker → client | `state` | `throughSeq`, `snapshot: {roomId, phase, driver, driverEpoch, dataClass, you, runs, queue, sealed}`. `runs` is `[{id, role, phase, unsandboxed}]`; `unsandboxed` is true for a run whose pod has no RuntimeClass. `queue` is the messages still queued for the next run, `[{ref, author, text}]` (redacted), read just after the mark: apply events past `throughSeq` over it. `sealed` is the log's seal at the mark (`phase` follows the Room and lags it). `approvals` is the pending approvals, `[{approvalId, runId, callId, class, action, expiresAt, seq}]`, read like `queue`: a card per approval, however far behind the tail its request is |
| broker → client | `sync` | `fromSeq`, `throughSeq`: the range that follows from the log |
| broker → client | `event` | One C4 envelope |
| broker → client | `ack` | `clientSeq`, then `seq` or `rejected`, and `result` for actions that return data |

Replay is lossless: the broker subscribes to the room's fan-out (the hub), reads the high-water
mark, pages the log up to it, then streams live events, dropping any at or below the mark. A gap in
live `seq` triggers a range read.

| Close code | Reason | Client should |
|---|---|---|
| `4001` | `reauth` | Reconnect: the connection reached `min(token expiry, 1 h)` |
| `1008` | `slow_consumer` | Reconnect with `afterSeq`: over 2 MiB was pending |
| `1008` | `hello first` | Send `hello` first, within 10 s |
| `1009` | — | Keep a frame under 32 KiB |
| `1007` | `failed to unmarshal JSON` | Send each frame as one JSON object |
| `1001` | `shutdown` | Reconnect: the replica is stopping |
| `1013` | `log_unavailable` | Reconnect with `afterSeq` after a backoff |
| `1008` | `no such room` | Stop: the caller may no longer read the room (room access, re-checked on every ping against the access cache, so within the TTL plus 30 s); a re-dial is refused `404` |
| `1013` | `access_unverified` | Reconnect after a backoff: ZITADEL or GitHub could not confirm the caller's access past the cache |
| `1008` | `user_inactive` | Stop: the caller's ZITADEL user is no longer active (re-checked on every ping, admins included, so within the TTL plus 30 s); a re-dial is refused `401` |

The broker pings every 30 s; a peer that does not answer within 10 s is disconnected without a
close frame. So is a peer that does not take a frame within 10 s (`write_timeout`): reconnect with
`afterSeq`. So is a client that sends no `hello` within 10 s.

### Actions

| Action | Fields | Who (see [authorization](security.md#authorization)) |
|---|---|---|
| `message` | `text`, `delivery: none \| queued \| steering`, `to?` | Collaborator and up; `steering` driver only |
| `remove_queued`, `promote_queued` | `ref` | The author or the driver; promote: driver |
| `interrupt` | — | Driver |
| `driver_request`, `driver_give`, `driver_take` | `to?`, `reason?` | Request: collaborator; give: driver, to a collaborator or better (from the Room's members; an `agents-admin` who is not a member takes instead) or the room's system holder; take: owner or `agents-admin`, with a reason of at most 256 bytes |
| `start_run` | `role` (`implementer`, `reviewer`, `triager`), `prUrl?` (reviewer only, a pull request of the room's repository), `egressProfiles?` (at most 8 names) | Driver, owner. A reviewer's task is its PR, else the one the latest agent handoff or review verdict names; any other role's is the fenced brief of the latest agent handoff and verdict, which consumes the queued messages it quotes. Recorded as `state_changed{run_requested}`, with the reviewer's `taskUrl`. Without a factory requester (`via: manifest`) the ack's `result` is the rendered `AgentRun` for the owner to apply. A replayed `clientSeq` acks the record without it; one retried after a failed record (`log_unavailable`) asks for the same run under the same idempotency key, so it never creates a second one ([integration](integration.md)) |
| `invite` | `principal`, `memberRole`, `approver` | Owner. At most 20 members; never demotes the driver-token holder below collaborator (`bad_action`): the holder hands the token over first |
| `close` | `reason?` | Owner |
| `decide` | `approvalId`, `decision: approved \| denied`, `reason?` (at most 1 KiB, redacted; the agent reads it) | Approver, owner, from the web UI. The first valid decision wins (`already_decided` for every later one, a supersede included). The deadline is checked on the database clock in the transaction that would record the decision: one past it closes the approval as `expired` and is refused `approval_expired`, whether or not the sweep has run, as is any decision on an expired approval. With `approvals.fourEyes`, nobody who prompted the run decides (`four_eyes`). A replayed `clientSeq` acks the stored decision |
| `fork` | `seq`, `note?` (at most 1 KiB, redacted), `role?`, `prUrl?`, `egressProfiles?` (as `start_run`'s), `approvals?: attended \| unattended` | Watcher and up; also from `maku`. A sealed room forks only for its owner or an `agents-admin` (`sealed`). A new room owned and driven by the forker, with the source's data class, repository and retention and none of its members. Its approvals ask by default: for the source's owners (owner, owner members, `agents-admin`) `attended`, with the source's `ttl`, `fourEyes` and every override but `allow`, so an unattended factory room does not make a human's own fork run unasked; `approvals: unattended` keeps the source's approvals as they are (an attended source stays attended; `bad_action` for any other value). Anyone else, whatever they ask, gets, class by class, the stricter of the source's and a new room's (`attended`), so an `unattended` or `allow` override never carries over to a watcher: events `1..seq` copied with their `seq`, then `state_changed{forked_from}`. The source's queue as it stood at `seq` comes along: each message queued by then and not yet promoted, removed or consumed by then is queued again in the fork (one consumed through `POST /v1/rooms/{id}/queue/consume` leaves no event to date it, so it comes along only while still queued). With a `role`, the new room's first run, on `agent/<new room>` from the latest agent commit at or before `seq`, on the forker's token and budget. The ack's `seq` is the new room's `forked_from`; its `result` is `{roomId, run?, runError?}`: `run` is the `start_run` claim or `null`, `runError` its rejection. A failed run leaves the fork made. A prefix over 5,000 events or 32 MiB is `too_large`; a principal forks 3 times at once, then once a minute, per replica |

| `rejected` | Meaning |
|---|---|
| `not_permitted` | The caller's room role, flag or client does not allow it; or the factory refused the caller a run, such as a caller without `write` on the room's repository on GitHub ([factory](factory.md#who-may-start-work-on-a-repository)) |
| `stale_epoch` | `driverEpoch` no longer matches: someone else holds the token now |
| `rate_limited` | Over 10 actions per second (burst 20), or over 3 forks at once then one a minute; per replica |
| `no_running_run` | Steering or interrupt with no run `Running` |
| `room_busy` | A run is already running; or a run was requested in the last 10 minutes and has not joined yet, and the broker does not know it ended (a claim not applied, or a factory run not seen yet). Two replicas answering a `start_run` in the same instant can both pass this check until the factory refuses a second pending run per room |
| `reviewer_needs_pr` | A reviewer's `start_run` with no `prUrl`, and no pull request of the room's repository in the latest agent handoff or review verdict. Human chat, queued text and tool output never choose it |
| `over_budget` | The factory refused the run: over the caller's budget |
| `factory_unavailable` | The run could not be requested: no requester, or the factory failed or answered no run id. Retry |
| `bad_action` | Malformed, or about another room; egress profiles on a run or fork of an `internal` room; a give to someone who cannot hold the token; a `clientSeq` this connection already used for an action of another type |
| `not_queued` | The queued message was already delivered or removed |
| `sealed` | The room's log is sealed; a fork of it by anyone but its owner or an `agents-admin` too. A queued message that reached the room's event limit is refused this way but stays in the log, unqueued: the transcript can show it as the room's last message |
| `conflict` | The Room changed under an `invite`: retry |
| `log_unavailable` | The log or the Room could not be read or written: retry |
| `too_large` | A fork's prefix over 5,000 events or 32 MiB: fork at an earlier `seq` |
| `already_decided` | Another approver decided first |
| `approval_expired` | The decision reached the broker after the approval's `expiresAt`; the approval closed as `expired` |
| `four_eyes` | The room requires an approver who did not prompt the turn |

### Machine-sent text

Text that comes through `maku` or `POST /mcp`, a message's `text` and a fork's `note`, is
machine-sent (factory interfaces F8): a local agent can drive either on its human's token. The
broker sanitises it as the factory does an issue (hidden and control characters out, NFKC, fence
look-alikes, images and raw HTML defused), then stores it between two lines: `machine-sent via roomctl` from the CLI (a stored value
that keeps its pre-maku name, see [naming](naming.md#technical-names)), `machine-sent via mcp` from `POST /mcp`:

```text
⟦machine-sent via mcp: a client wrote this on a human's token⟧
use the v2 API
⟦end of machine-sent text⟧
```

The sanitiser maps the text's own `⟦` and `⟧` away, so no text forges either line. The limits hold
once wrapped: a message over 16 KiB is `too_large`, a note over 1 KiB `bad_action`. A message with
nothing readable left is `bad_action`. The web UI's text is stored as typed. The human who holds the
token stays the prompter for four-eyes.

### `POST /mcp`

An MCP server for a developer's local coding agent, through [`maku mcp`](cli.md#the-mcp-server):
streamable HTTP on this listener (factory interfaces F2). It is off unless the broker's config sets
`human.mcp.enabled` ([integration](integration.md)); off, `/mcp` is `404`. Every check of the table
above applies first. Then it takes tokens from the CLI's ZITADEL client (formerly `roomctl`) only, so nothing here carries the
web UI's standing: any other token is `403 mcp_needs_roomctl`, counted in
`maku_rooms_rejected_actions_total`.

It keeps no session: each `POST` holds one JSON-RPC message and gets its answer as JSON, so either
replica answers any request. No `GET` stream and no `DELETE` (`405`), no batch, a body of at most
128 KiB (`413`). It serves `initialize` (revisions `2025-06-18` and `2025-11-25`; any other gets the
newest), `ping`, `tools/list`, `tools/call`, `tasks/get` and `tasks/result`; any other method is
`-32601`. A request whose `MCP-Protocol-Version` names another revision is `400`, `-32600` with
`data.supported`, so a client of a later, stateless revision falls back to `initialize`. A
notification is `202`. Each person makes 10 requests a second, burst 20, per replica; past it,
`-32603 rate_limited`.

| Tool | Standing | Arguments | Result |
|---|---|---|---|
| `rooms_list` | any member | `repo?`, `mine?`, `needsMe?`, `all?` | The overview table as the text (Markdown, to show as it is), and `{rooms, access}` as `structuredContent`: `GET /api/rooms`'s rows and its `X-Rooms-Access`. `all` adds to the text the rooms that ended over a week ago; `structuredContent` always has every row |
| `room_summary` | a reader of the room | `room`, `after?` (`seq:N`) | The room's card as the text (Markdown), and its [`summary/v1`](#get-apiroomsidsummary) as `structuredContent` |
| `room_tail` | a reader of the room | `room`, `cursor?` (`seq:N`), `kinds?` (at most 10 event types) | `{room, state, events: {untrusted: true, items}, cursor, more}`: at most 100 events and 256 KiB of payload past the first, at most 2,000 scanned for `kinds`; without a cursor, the last 50. Pass `cursor` back; `more` says the log held more when it was read |
| `room_changes` | a reader of the room | `room`, `cursor?` (`seq:N`) | What changed since a cursor, as a list of milestones (run started and ended, notes, handoffs, approvals asked and decided, verdicts, the PR opened or merged, holds, the budget) in the text, and `{room, url, since, items, more, cursor, untrusted: true}` as `structuredContent`. Without a cursor, the last 24 hours. A call scans at most 2,000 events: `more > 0` says there is more to read (milestones left out, or events past that bound), and `cursor` is the last event it covered: pass it back. A cursor past the room's last event is clamped to it, so the cursor returned can be lower. A fork's copied events are not its own milestones |
| `room_queue` | collaborator | `room`, `text` | `{room, queued}`: the message's `seq`. Its text is machine-sent |
| `room_fork` | as the `fork` action | `room`, `at`, `role?`, `prUrl?`, `egress?`, `note?` | `{room, url, run?, runError?}`. The note is machine-sent |
| `issue_draft` | any member | `repo`, `title`, `body` | `{repo, title, body, stripped, canStart, canStartWhy?}`: the draft with hidden and control characters stripped (`stripped` counts them). `canStart` is true when `repo` is in `human.mcp.sessionStart` and the caller writes to it on GitHub; else `canStartWhy` is `off` (not listed, no GitHub check wired, or a sponsored caller), `not_linked`, `no_write` (read only, or the GitHub App cannot see the repository) or `unverified` (the check failed). Refused unless the title reads `<area>: <what is wrong>` (at most 256 characters), the body holds `## What is wrong`, `## What it should be` and `## Acceptance check` in that order (at most 16 KiB), and the redactor finds no secret. It files nothing: `maku mcp`'s `issue_file` does |
| `needs_list` | any member; a sponsored principal, the rooms it watches | `since?`, `room?`, `repo?` | The caller's needs across the rooms they may read, as `needs/v1`, as the text and as `structuredContent`: [`GET /api/needs`](#get-apineeds)'s `{apiVersion, items, cursor, access, unread?}`, item for item and in the same order. Listed only while `human.needs.enabled` serves the route. It decides nothing: each item's `action` is a link — a merge on GitHub, an approval on the room page |
| `intake_plan` | any caller, when `human.intake.plan.enabled` | `text`, `repository?`, `candidates?`, `context?`, `evidence?`, and a sponsored principal's `on_behalf_of`, `ceiling` | `plan/v1` and its hash, the one question to ask first, `canStart`. One public-text-only model call; it files nothing ([the planner](#the-planner-on-mcp)) |
| `plan_check` | any caller, when `human.intake.plan.enabled` | `plan`, `hash`, `on_behalf_of?` | The plan re-validated with the hash recomputed, and `canStart`: the door every filing re-validates through ([the planner](#the-planner-on-mcp)) |
| `coordination_plan` | any caller, when `human.intake.plan.enabled` and `coordination.mode` is not `off` | `text`, `candidates` (2 to 20), `primary`, `context?`, `id?`, `version?`, and a sponsored principal's `on_behalf_of` | A validated two-repository `CoordinationPlan`, its hash, diagnostics and the attempt's planning record. One call on the planner's route; it files nothing ([coordination](#coordination-on-mcp)) |
| `room_brief` | a reader of the room; never a sponsored principal | `room`, `since?` (`seq:N` or RFC 3339) | The resume brief as the text, and [`brief/v1`](#get-apiroomsidbrief)'s `{apiVersion, room, text, cursor}` as `structuredContent`: what the work was asked for, its acceptance checks, what changed since the cursor (without one, 24 hours), what blocks it and the one next step. Fixed prose over quoted data: pass `cursor` back as `since`. Not on a sponsored principal's list |

The view tools (`rooms_list`, `room_summary`, `room_changes`, `needs_list`, `room_brief`) return their text as `content` and their JSON as `structuredContent`, with an `outputSchema`; the JSON is not repeated in the text. `run_create`'s and `room_tail`'s task results stay JSON text, as `tasks/result` returns them.

`prompts/list` and `prompts/get` serve the prompts `handoff` (`what`), `plan` (`what`), `status`
(`room`), `resume` (`room`) and `inbox`: canned messages the client offers as slash commands. A
sponsored principal's list is empty. The prompts start nothing and compose nothing: `handoff` passes
the developer's words and the session's context to `intake_plan` and then files through `issue_file`,
and `plan` stops after showing what the planner answered. `resume` is one call to the needs list, then
`room_brief` of the room it names first — or of the room the developer passed — so a session starts
from the brief rather than from a search of the log, and never claims a room exists before
`room_brief` shows it. `inbox` is one `needs_list` call: when its list is empty it says so in one line
and names `rooms_list` with `mine`, rather than making a second call.

No tool approves, steers or stops, and none labels or starts factory work by itself: only the developer's answer to `issue_file`'s form, which `maku` serves where `plan_check` (with a plan) or `issue_draft` (without one) says `canStart`, can apply `factory/ready` with the developer's own `gh`. The two write tools are acts through
the CLI's ZITADEL client (formerly `roomctl`): the [actions](#actions)' rules, limits and rejections apply. Arguments are
strict: an unknown field is refused. Every string an agent wrote comes back under `untrusted: true`,
as the summary's notes do. A refused call is a tool result with `isError: true` whose text starts
with its reason: `invalid_arguments`, `invalid_draft`, `rejected: <the act's rejection>`, `no such
room` (a missing room and one the caller may not read alike), `access_unverified`, `rooms
unavailable`, `log unavailable`, `room log unreadable` or `unavailable`. Each call counts in
`maku_rooms_human_mcp_calls_total{tool, outcome}`.

**`room_tail` as a task** (MCP Tasks, F5). With `2025-11-25`, `initialize` declares
`tasks.requests.tools.call` and `room_tail` lists `execution.taskSupport: optional`. A `tools/call`
with `task: {ttl?}` returns a task that waits for the room's next events: `ttl` is the wait, 10 s to
15 min (default 5 min); without a cursor, it waits for the events after the room's last `seq` now.
The broker keeps no task: the id is an unsigned cursor naming the query and its creator, not a signed
token. The broker checks that the caller is the creator, and every read re-authorizes the caller as the
room's reader, on any replica, from the log.

| Method | Answers |
|---|---|
| `tasks/get` | `completed` once an event of the task's `kinds` follows its cursor, its room is `done`, `failed` or `stopped`, or the wait is over; `failed` with `no such room` when the room cannot be read; else `working`, the room's state in `statusMessage`. `pollInterval` is 5 s |
| `tasks/result` | A terminal task's page, as `room_tail` returns it, with `_meta["io.modelcontextprotocol/related-task"]`. A `working` task is `-32600 task_working`: it does not block, since every request must end inside the proxy's 30 s. `maku mcp` polls `tasks/get` and blocks for its client |

Another person's task id, or a malformed one, is `-32602 no such task`. A room read that fails for
a while is `-32603`: poll again. There is no `tasks/list` or `tasks/cancel` (`-32601`), and no task
is `input_required`: that state belongs to [`run_create`](#sponsored-principals-on-mcp).

### The planner on `/mcp`

`intake_plan` and `plan_check` are the governed planner (CD5, CD17, CD18, CD27): a sentence becomes
`plan/v1` through one bounded call to the AI Gateway, and the plan's hash is the value every later
door re-validates before anything is filed. They are **off** unless the broker's config sets
`human.intake.plan.enabled`; off, both are unknown tools (`unknown tool: intake_plan`), as
`run_create` is to a human token. `config.Load` refuses the switch unless it also has
`human.mcp.enabled` (both tools exist on this listener alone), `human.access` (the write check the
candidate set is drawn from), a non-empty `human.intake.plan.repositories` and a non-empty
`human.intake.plan.classes` — each named by its own error, so the switch cannot land ahead of its
parts. The broker in turn refuses to start with `ROOMS_GITHUB_APP_DIR` unset (no repository's
visibility can be read, so the guard would refuse every plan) or without an `Actor` redactor
(`plan_check` scans a plan's text as `issue_draft` does).

| Tool | Arguments | Result |
|---|---|---|
| `intake_plan` | `text` (one sentence about what is wrong, required, at most 16 KiB); `repository?` and `candidates?` (owner/name, at most 20 candidates); `context?` (at most 4 KiB) and `evidence?` (at most 5 items of at most 280 characters, counted against the same 4 KiB); `on_behalf_of?` (`slack:<team>/<user>`) and `ceiling?` (USD, 0 or absent means the class's), both a sponsored principal's alone | `{plan, hash, question, canStart, canStartWhy?, degraded}`: the plan, the hash the broker computed over it, the one question to ask first, whether this caller may start the factory on it, and whether a template rather than the planner wrote it (`degraded`, `human.intake.plan.fallback: composer`) |
| `plan_check` | `plan` (the `plan/v1` object as it was shown), `hash` (`sha256:<64 hex>`), `on_behalf_of?` (a sponsored principal's, as above) | `{plan, hash, canStart, canStartWhy?}`: the plan re-validated with the hash recomputed over it. It files nothing, calls no model, spends no daily plan and writes no record |

`plan/v1` is `{apiVersion: "plan/v1", rev, planner: {model, surface, at}, repository,
repositoryEvidence, alternatives, issues, budget: {ceilingUSD, source}, question, hash}`; each issue
is `{index, title, body, class, roles, dependsOn}`, numbered 1..n in order, one to three per plan,
with `sanitize`'s draft bounds. `repositoryEvidence` (at most 5 items of at most 280 characters),
`alternatives` and `planner` are display and untrusted; the hashed set is
`{apiVersion, repository, budget, issues}` (CD27), so `hash` — computed by the broker, never by a
caller — is what a plan *is*.

**What the model decides, and what it does not.** The reply names the class and drafts the issues;
roles, the ceiling and the hash are the config's and the broker's. A reply naming a role or a budget
has it dropped and logged, a class the config does not define is refused (`invalid_class`), and a
repository outside the candidate set refuses the plan whole (`repository_not_in_candidates`) without
being named back. The candidate set is `human.intake.plan.repositories` ∩ what was asked ∩ what the
requester may write ∩ what GitHub reports **public**: a private repository is refused
(`private_repository_unsupported`) before any call, and a repository GitHub cannot answer for is
`repository_unverified`, never planned on. Visibility is read once per repository and cached for
`human.access`'s TTL, the same window a revoked permission lapses in.

Each refusal is a tool error whose text starts with one reason of a closed list:

| Reason | One line |
|---|---|
| `private_repository_unsupported` | the planner reads public text only, and this repository is not public; file the draft with `issue_draft` and `issue_file` using your own model, or wait for the internal route |
| `repository_not_allowed` | no repository the allowlist serves and you may write in matches this request; name one of the repositories listed |
| `no_write` | you cannot write to this repository; ask for one you may write in, or show the draft to someone who can file it |
| `invalid_draft` | the draft is not the issue template `<area>: <what is wrong>`, then the three headings in order |
| `planner_invalid` | the plan is not the shape the contract asks for (`<field>: <value>, the bound is …`); ask again |
| `planner_unavailable` | the plan service is unavailable; try again later |
| `planner_timeout` | the plan service did not answer inside its deadline; try again |
| `plan_changed` | the plan was superseded; the newer one is the one to confirm |
| `repository_unverified` | GitHub could not confirm this repository, so the planner will not guess; try again later |
| `repository_not_in_candidates` | the plan names a repository outside the ones this request may use; name one of them |
| `invalid_class` | the class is not one this broker's config defines; name one of the classes it serves |
| `not_linked` | your account is not linked to GitHub, so the broker cannot tell what you may write; link it first |
| `daily_plans_reached` | you have used today's plans; the cap resets at 00:00 UTC |
| `plan_too_large` | the plan is longer than a bound the contract sets; nothing was truncated |

The cap is `human.intake.plan.dailyPlans` per **effective requester** — a human's GitHub login, a
Slack user's `slack:<team>/<user>` — per UTC day, counted on this replica, and one plan costs one
call: there is no retry loop. Neither tool files, stores or approves anything: the plan is shown,
and the coding agent's `issue_file` (or the Slack proposal, later) re-validates it through
`plan_check` first. Both count in `maku_rooms_human_mcp_calls_total{tool, outcome}`, and
`intake_plan` also feeds `maku_intake_plan_total`, `maku_intake_plan_seconds` and
`maku_intake_plan_tokens`.

**For the operator:** the planner's model call is the only place the broker reaches a model; it sends
public text only, refuses a private repository before the call, and logs the hash, never the text.
`human.intake.plan.reasoningEffort` (`low`, `medium` or `high`; unset by default, which sends
nothing) is sent as the call's `reasoning_effort`, and any other value fails startup. Set it for a
model that always reasons: `glm-5.3` behind `tier-frontier` spends the whole 20 s deadline
reasoning at its default, and answers in about 4 s at `low`. Whether a value is accepted is the
provider's: Z.ai names `low` and `high` for `glm-5.3`, not `medium`.

### Coordination on `/mcp`

`coordination_plan` (v4 MR4) plans one change across two public repositories on the planner's own
route: the same call, the same requester resolution, the same guard and the same daily counter as
`intake_plan`. It is listed only while `human.intake.plan.enabled` and the top-level
`coordination.mode` is `plan` or `execute`; `off`, the default, lists nothing. The block is the
factory's (`{mode, publicOnly: true, maxChildren: 2}`), read with the same rules, and `config.Load`
refuses a mode other than `off` without the planner.

| Step | What happens | Refusal |
|---|---|---|
| Arguments | Closed: no file, confirm, merge, role or budget argument | `invalid_arguments` |
| Requester and primary | Resolved as `intake_plan` does; the requester must write `primary` | `not_linked`, `no_write`, `repository_not_allowed` |
| Every other candidate | Served by `human.intake.plan.repositories` and public, before any text leaves | `private_repository_unsupported`, `repository_not_allowed`, `repository_unverified` |
| Daily slot | One of `human.intake.plan.dailyPlans`, shared with `intake_plan` | `daily_plans_reached` |
| One call | The model proposes the reading only; it may not name roles, a tier, a maintainer or money | `planner_invalid`, `planner_timeout`, `planner_unavailable` |
| Composition | Children's roles (the class's, without `scoper`) and allocation (its ceiling) from the class table; one run is a tenth of it; planning and each of 4 integration runs cost one run of the primary's class; `coordination.Validate` in plan mode | a coordination reason, such as `policy_unresolved` or `invalid_plan`, with its field |

The result is `{plan, hash, diagnostics, planning}`: `hash` is the plan's canonical SHA-256 (64 hex,
no prefix), `diagnostics` notes an incomplete one-child draft and each maintainer proposed as the
requester, and `planning` is `{attemptID, effectiveRequester}`. It writes nothing: no issue, Task,
run, room, label or record. The factory re-resolves every child at its own repository's policy when
the plan is confirmed; that, and filing, are not on `/mcp`.

### Sponsored principals on `/mcp`

A sponsored principal (a ZITADEL machine user declared in the broker's `human.mcp.sponsored` and the
factory's `api.sponsored`, [factory](factory.md#sponsored-principals)) reaches `/mcp` with its own
client-credentials token, whose audience must hold the project (`human.projectIDFile`). Nothing
else serves it: every other route refuses it, and it never reaches `/v1/runs`. It sees six tools, plus
the planner's two when `human.intake.plan.enabled` is on: `run_create`, `issue_draft`, `room_summary`,
`room_tail`, `room_changes`, `needs_list`, `intake_plan` and `plan_check`; `room_queue`, `room_fork` and
`rooms_list` are unknown tools to it, as is `needs_list` while `human.needs.enabled` is off. It reads a
room only as one of its `watcher` members, which the factory adds when it creates its task's room, with no
GitHub check.

`needs_list` is its one room-scoped read (CD28): the room's needs, not a login's, so the merge it cannot
perform is listed with a link to the pull request and no field that could carry a decision — the schema
takes `since`, `room` and `repo` only, and refuses anything else. The two legacy room reads stay need-free
(B1): `room_summary` and the room's row say nothing needs it, as they did before.

| Tool | Arguments | Result |
|---|---|---|
| `run_create` | `repo`, `text`, `dataClass` (`public`), `max_cost` (USD, above 0), all required; `title?` (files the issue, with the `issue_draft` rules), `on_behalf_of?` (`slack:<team>/<user>`), `ref?`, `idempotency_key?` (at most 128 characters; default a hash of the arguments, so a retried call makes one task); `plan?` (the `plan/v1` the task was confirmed from) with `plan_hash?` (the `sha256:` value the broker answered beside it) — both, or neither | `{task, room, issue, duplicate, url, sponsor?}`: the factory's answer, the room's page, and the GitHub login that sponsors a new task |
| `slack_link_code` | `team`, `user` (Slack ids), `handle?`, `name?` (display only). Listed only to `human.slackLink.principal`; `team` must be `human.slackLink.teamID` (`wrong_team`) | `{url, expiresAt}`: `<publicURL>/link/slack?code=<code>`, single use, 10 minutes; it replaces the user's earlier code. `too_many_codes` past 1000 waiting ([Slack self-serve link](slack.md#self-serve-link)) |
| `slack_unlink` | `team`, `user`. Listed only to `human.slackLink.principal` | `{unlinked}`: whether the Slack user had a self-serve link |

Unknown fields are refused: class, tier, template, auto-merge and egress are never the caller's.
The text passes the broker's redactor, then [`POST /v1/tasks`](#post-v1tasks) with the caller's
own token, which the factory verifies again. A factory refusal comes back as a tool error,
`rejected: <reason>`.

A call that carries a plan is held to it before any of that, and anything it disagrees with files
nothing: the broker recomputes the [hash](#the-planner-on-mcp) over the plan (`plan_changed` when it
does not match `plan_hash`, so a plan edited after it was shown is never filed), refuses a plan that
does not carry exactly one issue (`invalid_arguments`: Slack files one issue per call, CD19),
`plan_changed` unless `repo`, `title` and `text` are the plan's own repository, title and body, and
`invalid_arguments` for a `max_cost` above the plan's ceiling. It then re-validates the plan through
the same checks `plan_check` runs — the Slack user's GitHub write access, the allowlist, the class
table and the issue template — so a plan this broker cannot hold is refused here rather than filed and
left to the factory to refuse. `on_behalf_of` is required with a plan: the plan is re-validated for the
Slack user it was made for, never for the App's own identity. A call with no plan is the tool it always
was, which is why an App that has not been updated keeps working.

**`run_create` as a task.** With `2025-11-25`, `run_create` lists `execution.taskSupport: optional`.
A `tools/call` with `task: {ttl?}` creates the factory task and returns an MCP task whose id is
`run.` plus a base64url JSON `{v, by, room, created, ttl}`: stateless like `tail.`, bound to its
creator. `ttl` defaults to 24 h, at most 72 h. `tasks/get` maps the room's coarse state:

| Room state | Task status |
|---|---|
| `queued`, `running` | `working` |
| `waiting_for_human` | `input_required`, `statusMessage` naming the room page; `tasks/result` returns the summary and never asks the caller anything: approvals stay on the web page |
| `done` | `completed` |
| `failed` | `failed` |
| `stopped` | `cancelled` |

Triage makes the room, so a task whose room does not exist yet is `working` for a 2-minute grace
from its creation, then `failed` (`no such room`). A slow triage can flip a `failed` task back to
`working` once the room appears: poll `tasks/get` until `completed` or `cancelled`, and treat
`failed` as provisional until the grace is well past.

### `POST /api/slack/decisions`

The one route the Slack App decides an approval through ([Slack](slack.md)): the Approve and Deny
buttons of a `forge.push` card. It is on only when the broker's config sets `human.slackDecide`
([integration](integration.md)); off, the path is the mux's `404`, as `/mcp` is when
`human.mcp.enabled` is false. The class it decides is `forge.push` alone, a constant: no other class
is decided here, and no configuration widens it.

It takes the App's client-credentials token, through the sponsored authenticator `POST /mcp` uses:
a human's session or a machine's token is `403`. No cookie is read and no `Origin` is required, so
the route has no CSRF surface either. The body is strictly decoded — an unknown key is a `400` — and
capped at 4 KiB, one value per request, with `Content-Type: application/json` required:

```json
{
  "team": "T07J97G2K2P",
  "user": "U07JY4EQNDN",
  "channel": "C0CC4M8SNVD",
  "messageTS": "1730822400.123456",
  "approvalId": "01M4A1B2C3D4E5F6G7H8J9K0MN",
  "branch": "agent/26zfnuxm",
  "decision": "approved"
}
```

| Field | Shape, checked before any read |
|---|---|
| `team` | `^T[A-Z0-9]{2,20}$`, and equal to `human.slackDecide.teamID` |
| `user` | `^[UW][A-Z0-9]{2,20}$` |
| `channel` | `^[CG][A-Z0-9]{2,20}$` |
| `messageTS` | `^[0-9]{1,12}\.[0-9]{1,8}$` |
| `approvalId` | `^[0-9A-HJKMNP-TV-Z]{26}$`, a ULID: the broker's bridge API is its only producer |
| `branch` | the Room's run pattern, `^agent/[a-z0-9][a-z0-9._/-]{0,100}$` |
| `decision` | `approved` or `denied` |

Each person makes 10 decisions a minute, burst 5, per replica, keyed by `(team,user)`: a person
presses a button a few times and never in a stream, so this is not the room page's 10 a second.
Past it, `429 rate_limited`.

The answer is one key, always a closed code and never a sentence: `{"result": "approved"}`.

| HTTP | `result` | Meaning |
|---|---|---|
| `200` | `approved`, `denied` | The decision was recorded on this request |
| `200` | `already_decided` | Someone, or this same click replayed, closed it first; nothing was appended |
| `200` | `approval_expired` | The deadline had passed; the click closed it `expired` if it was still pending |
| `200` | `not_linked` | The Slack user has no `slack_links` row, or the account has no GitHub identity |
| `200` | `link_unverified` | ZITADEL or GitHub could not be asked; it fails closed, with no detail |
| `200` | `not_permitted` | Not an approver or owner in this room, or the room is admins-only |
| `200` | `repository_permission` | The resolved login cannot write the room's repository |
| `200` | `four_eyes` | This human prompted the run (or the room), and the room sets four eyes |
| `200` | `binding_mismatch` | The branch is not the run's branch, or the approval binds none |
| `200` | `not_supported` | The class is not `forge.push` |
| `200` | `not_found` | No such approval in the room log |
| `200` | `sealed` | The room is sealed |
| `400` | `bad_request` | Shape, body size or content type |
| `401`, `403` | — | Not the App's principal |
| `429` | `rate_limited` | Over the pair's budget |
| `503` | `unavailable` | The store, the room or the log could not be read: retry |

Every refusal is a `200` with a code, apart from those four transport ones, so the App never retries
a refusal and Slack never redelivers one into a loop. Each answer counts in
`maku_rooms_slack_decisions_total{result, decision}` ([operations](operations.md)): `result` is the
code, `decision` the decision it answered with, and empty when the answer carried none.

What it reads, in order, each step naming the code it refuses with. Nothing comes from the request
beyond its ids: the body cannot name a human, a room or a class.

1. **The App.** The sponsored principal, and its id must be `human.slackDecide.principal`. Not it:
   `403`.
2. **The workspace.** `team` must be `human.slackDecide.teamID`. Different: `not_permitted`, and the
   answer says nothing about which workspace it was.
3. **The link.** `slack_links` says which ZITADEL account the Slack user is ([Slack self-serve
   link](slack.md#self-serve-link)). None: `not_linked`.
4. **The room.** The approval row names it, so a card cannot be pointed at another room's approval.
   No such approval: `not_found`. The room's state seals it: `sealed`.
5. **The class.** `forge.push` only: `not_supported` for any other, before any GitHub call.
6. **Room standing.** The Room CR alone decides it: its `spec.owner`, a `spec.members` role or the
   `approver` flag. The broker holds no token for the linked human, so no ZITADEL group grant is
   read: an `agents-admin` who is neither the room's owner nor one of its approvers is
   `not_permitted` here, and decides on the room page.
7. **The account.** The current GitHub login of that ZITADEL account: no GitHub link is
   `not_linked`, and a ZITADEL or GitHub read that fails is `link_unverified`.
8. **Repository write.** `repository_permission` when the login cannot write the room's repository,
   which is the second standing a maintainer outside the room does not have.
9. **The binding.** The approval recorded its run's branch when it was requested, and the run is
   re-read now; the body's `branch` must be that branch, and an approval that binds none is
   `binding_mismatch`. A card that outlived the state it was drawn from decides nothing.
10. **Four eyes.** A room with four eyes refuses the person who prompted it, on the row's own
    prompters, exactly as the room page compares them.
11. **The decision.** Through the store's one fence, unchanged. The event names the person, never the
    App. A replayed click answers as itself, and the opposite decision is not a replay.

Merging is untouched: this route decides an approval, and the merge stays a review of the current
head on GitHub.

## `:8082` — the factory's task intake

### `POST /v1/tasks`

It serves on the factory's `api.listen`, only while `api.sponsored` or `api.internalPrincipals` is
non-empty (otherwise `404`). It takes sponsored and [internal](#an-internal-principals-request) tokens
only: a human or a system caller gets `403 sponsored_only`. (`POST /v1/runs` refuses a sponsored caller the other way round, with
`403 sponsored_only_tasks`: a sponsored principal starts tasks, never runs.) The broker's
`run_create` calls it with the caller's own token. Model and
limits: [sponsored principals](factory.md#sponsored-principals).

| | |
|---|---|
| Auth | `Authorization: Bearer`: a token from `api.humanIssuer` whose client is a sponsored principal's `clientIDFile` and whose audience holds `api.projectIDFile` |
| `Idempotency-Key` | Required, at most 255 bytes. The Task's name derives from `api:<name>:<key>`: a replay answers `200 {duplicate: true}` and creates nothing, two requests under one key make one task. A name another request owns is `409 idempotency_conflict` |
| Body | `repository`, `text` (at most 16 KiB), `dataClass` (`public`), `maxCostUSD` (at least 0.01), all required; `title?` (files the issue; `<area>: <what is wrong>` and the three template sections), `onBehalfOf?`, `ref?` (at most 256 characters, a git ref: letters, digits, `.`, `_`, `/`, `-`, no leading `-`, no `..`, no whitespace; the same pattern as `run_create`'s, since it is written into the agent's brief). At most 64 KiB; unknown fields refused |
| `201` | `{task, room, issue?, sponsor}`: the room is named as the task; `sponsor` is the GitHub login it is charged to |
| `200` | `{task, room, issue, duplicate: true}` |
| `400` | `bad_json`, `idempotency_key_required`, `bad_data_class`, `bad_ref`, `max_cost_required`, `max_cost_over_principal`, `max_cost_over_class`, `invalid_draft`, `bad_text` |
| `401`, `403` | `unauthenticated`; `not_permitted`, `sponsored_only`, `repository_not_allowed`, `not_linked`, `not_a_maintainer` |
| `503` (link) | `link_unverified`: a [self-serve link](factory.md#sponsored-principals) the broker or ZITADEL could not answer for; retry |
| `409`, `429` | `repository_not_owned`, `idempotency_conflict`; `daily_task_cap` (the factory's `caps.tasksPerDay` is spent for this UTC day, counted as the reconciler counts it: refused before any Task or issue exists), `principal_task_cap`, `principal_budget_exhausted` (advisory: the hard limit is the run-time reservation) |
| `503` | `kill_switch`, `meter_unpriced`, `tasks_unavailable`, `ledger_unavailable`, `intake_failed` |

#### An internal principal's request

An [internal principal](factory.md#internal-principals) (RunLore) sends the same route with a finding,
and gets `investigate` on internal data, never a title or a link to another user. The body is
`repository`, `text` (the finding), `dataClass: internal`, `maxCostUSD` and `incident`, all required:
`incident` is `{alert, resource, severity, cluster, verdict, confidence}` (`alert` and `resource`
required). `Idempotency-Key` is the incident's trigger key.

| | |
|---|---|
| `201` | `{task, room, issue}`: an `investigate` task and a public stub issue labelled `factory/proposed`; it names the alert, resource and room, never the finding |
| `200` | `{task, room, issue, duplicate: true}`: a task of the key is still open. Once it ended, the same key is a new generation |
| `202` | `{accepted: false, reason: below_threshold}`: the verdict is not `action_required` or `action_suggested`, or `confidence` is under `minConfidence` |
| `400` | `bad_json`, `idempotency_key_required`, `title_not_allowed`, `on_behalf_of_not_allowed`, `bad_ref`, `bad_data_class` (anything but `internal`), `incident_required`, `max_cost_required`, `max_cost_over_principal`, `max_cost_over_class` |
| `403`, `409` | `repository_not_allowed`, `repository_not_owned` |
| `429` | `daily_task_cap` (as above: no Task and no stub issue, so no stub claims an investigation the reconciler would reject; two racing requests can still pass, and the second's rejection is narrated on its stub), `principal_daily_tasks` (the principal's tasks created this UTC day, whatever their kind), `principal_task_cap`, `principal_budget_exhausted` |
| `503` | as above |

A sponsored principal's request refuses an `incident` (`bad_json`), and an internal principal's refuses
everything a sponsored one's does beyond its own keys.

### `POST /v1/tasks/{task}/verification`

An internal principal with `verification: true` says whether the fix of its own proposal held. It serves
on the same port, only while some internal principal has `verification: true` (otherwise `404`). `{task}`
is the **fix** task, whose `spec.origin` names a task the caller created. The body is
`verdict` (`resolved` or `persisting`), `alert` (`name`, `firingAtMerge`, `firingNow`), `checkedAt`
(RFC 3339) and `evidence?` (at most 16 KiB, scanned for secrets); at most 32 KiB, unknown fields refused.

| | |
|---|---|
| `201` | `{task, verdict, recordedAt}`: the verdict is `status.monitor.verification` of the fix task, which the [post-merge monitor](factory.md#the-post-merge-monitor) reads; the evidence is `status.published.evidence` of the origin task (internal: the origin room is closed by then) |
| `200` | `{…, duplicate: true}`: the same verdict was already recorded |
| `400` | `bad_json`, `bad_verdict`, `bad_evidence` |
| `401`, `403` | `unauthenticated`; `not_permitted` (not an internal principal), `verification_not_allowed`, `not_own_task` (no origin, another principal's origin, or a class with no `monitor.verification`) |
| `404`, `409` | `no_such_task`; `not_merged`, `window_closed` (past `monitor.verification.within` after the merge), `verdict_already_recorded` (another verdict) |
| `503` | `tasks_unavailable` |

`maku_factory_api_intake_total{principal, outcome}` counts the requests of an authenticated sponsored
or internal principal: `admitted`, `duplicate`, or the code of any refusal after that point (`kill_switch`,
`bad_*`, `max_cost_*`, `repository_*`, `invalid_draft`, the caps and the `503`s); any other value is
`other`. The `unauthenticated`, `not_permitted` and `sponsored_only` refusals come before the
principal is known and are **not counted**.

## Webhooks

The broker sends signed, id-only events to the subscribers declared in its `webhooks` config (see
[operations](operations.md#webhooks)). It is off until a subscriber is declared. A payload carries
ids and closed codes only: never room text, a title or a message — a `branch` is a code shape, on the
one approval that binds it.

| Event | Fires when | Fields beyond `type` and `room` |
|---|---|---|
| `room.task_received` | The task enters `Received` | `repo` |
| `room.task_triaged` | The task enters `Triaged` | `repo` |
| `room.run_started` | A run enters `Running` | `run` |
| `room.run_ended` | A run ends: `Succeeded`, `Failed`, `BudgetExhausted` or `Revoked` | `run` |
| `room.waiting_for_human` | The task enters `AwaitingHuman` or `Escalated`, or an approval is requested | `reason`; on an approval also `repo`, and `approvalId`, `class`, `branch`, `expiresAt` when the request carries them |
| `room.pr_opened` | The first pull request of the task is known | `pr` |
| `room.verdict_posted` | GitHub accepted the verdict comment | `pr` |
| `room.merged` | The task enters `Merged`, or its facts carry reason `merged` (a human merge), `merged_verified` or `merged_unverified`, once | `pr` |
| `room.reverted` | The task enters `Reverted` | `pr` |
| `room.monitor_verdict` | The [post-merge monitor](factory.md#the-post-merge-monitor)'s verdict reaches the task facts of a room that names an `origin`, once | `repo`, `pr`, `verdict` |
| `room.budget_reached` | A task phase or a run end names a budget reason | `limit` |
| `room.ended` | The task's first coarse end: `done`, `failed` or `stopped` | `state` |
| `room.approval_closed` | An approval is `approved`, `denied`, `expired` or `superseded` | `approvalId`, `decision` |

A task event fires on a change of phase only: the factory rewrites a phase's facts (budget, run,
pull request) without a new event. `repo` is on `task_received`, `task_triaged`, `room.monitor_verdict`
and an approval's `room.waiting_for_human`.
`verdict_posted` is sent once GitHub accepted the comment, not when it was attempted.

An approval's `room.waiting_for_human` names the approval, and `room.approval_closed` says how it
ended, so a subscriber follows one approval from request to close without reading the room.
`approvalId` is a ULID and `class` is one of the broker's five: a value outside its closed shape is
dropped from the event rather than clipped, and the reader draws the plain line instead. `branch` is
the run's branch on the approval that binds one, and `expiresAt` is its deadline, RFC 3339 in UTC to
the second. A close is one event per approval; a replayed entry emits nothing.

`origin` is on **every** event of a room whose task was started from an issue the factory published
(see [`factory/publish`](factory.md#internal-principals)): the room id of the investigate task. It is
absent on any other room's events.

`room.monitor_verdict` goes out once per room, and only for a room whose task facts name an `origin`: a verdict is final once set, so a
`regression` decided inside the window is sent at once. Delivery is at least once with a stable `webhook-id`. A subscriber receives it
only when its `events` list names it.

`reason`, `limit`, `state` and `verdict` are clipped to closed lists (`webhook.Reasons`, `webhook.Limits`,
`webhook.States`, and the monitor's three verdicts); anything else is sent as `other`. `decision` is
the fifth closed list: a close outside it is not sent at all, and a subscriber that renders one clips
it the same way (`webhook.Clip("decision", …)`). The factory's room escalations `foreign_room`,
`room_log_unreadable` and `rooms_crd_outdated` are listed.

| Field | Values |
|---|---|
| `limit` | `budget-run`, `budget-task`, `budget-principal`, `budget-fleet`, `budget-person`, `budget-family` |
| `state` | `queued`, `running`, `waiting`, `done`, `failed`, `stopped` |
| `verdict` | `healthy`, `regression`, `inconclusive` |
| `decision` | `approved`, `denied`, `expired`, `superseded` |
| `reason` | The wait reasons of [the summary](#get-apiroomsidsummary), plus `approval`, `manual`, `revoked`, `deleted`, `principal_unknown`, `budget-run`, `budget-principal`, `budget-fleet`, `budget-person`, `foreign_room`, `room_log_unreadable`, `rooms_crd_outdated` |

Example body:

```json
{"type":"room.pr_opened","room":"room-7f3a","pr":42}
```

### Delivery

A `POST` of `application/json` with three headers, as [Standard Webhooks](https://www.standardwebhooks.com/) defines them:

| Header | Value |
|---|---|
| `webhook-id` | `evt_…`: the same for every retry of one event, distinct per subscriber |
| `webhook-timestamp` | Unix seconds |
| `webhook-signature` | `v1,<base64>` per key, space-separated: two while a key is rotated |

The signed string is `webhook-id.webhook-timestamp.body`, HMAC-SHA256 under the subscriber's key,
over the raw body bytes.

A subscriber must:

1. verify the signature against the raw body, with a constant-time comparison;
2. reject a timestamp more than 5 minutes from its clock, either way;
3. deduplicate on `webhook-id`: a delivered event can arrive twice (a restart replays from the last cursor);
4. answer `2xx` quickly, and fetch the room through the MCP server or the human API if it needs more
   than the ids.

**At-least-once holds for delivered events only.** A delivery that does not get a `2xx` is retried after
10 s, 1 min and 5 min, then given up: the event is dropped and the room's cursor moves on, even when
the subscriber stays enabled (three given up in a row disable it). The exception is a signing key the
broker cannot read: the room is held, nothing is dropped, and each sweep tries again until the key is
fixed.

Events are ordered within a room, except for the replays after a broker restart, and not across
rooms. A subscriber's response body is never read beyond 64 KiB. No redirect is followed and no proxy
is used.

Where a subscriber starts: every room that exists when the subscriber is first declared starts at that
room's last event, so history is not replayed; a room created later starts at its first event.

A room's events are read from its log. A room forked from another replays the parent's copied history
as its own events, so a subscriber of the fork sees the parent's phases again, under the fork's room id.

A room's repository (the `repo` field and the `repositories` filter) comes only from the factory's
task facts, or else from the Room. If the two disagree, the room is held and counted `mismatch`.

Verifying with Go, standard library only. `keys` are the base64-decoded bytes of the key file's lines:

```go
func verify(keys [][]byte, h http.Header, body []byte, now time.Time) (id string, ok bool) {
	id, ts := h.Get("webhook-id"), h.Get("webhook-timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if id == "" || err != nil {
		return "", false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return "", false
	}
	for _, k := range keys {
		m := hmac.New(sha256.New, k)
		fmt.Fprintf(m, "%s.%s.", id, ts)
		m.Write(body)
		want := m.Sum(nil)
		for _, s := range strings.Fields(h.Get("webhook-signature")) {
			v, found := strings.CutPrefix(s, "v1,")
			if !found {
				continue
			}
			if got, err := base64.StdEncoding.DecodeString(v); err == nil && hmac.Equal(got, want) {
				return id, true // dedupe on id, then act
			}
		}
	}
	return "", false
}
```

Verifying with Python. `keys` are the base64-decoded bytes of the key file's lines:

```python
import base64, hashlib, hmac, time

def verify(keys, headers, body: bytes) -> bool:
    i, ts = headers["webhook-id"], headers["webhook-timestamp"]
    try:
        if abs(time.time() - int(ts)) > 300:
            return False
    except ValueError:
        return False
    for key in keys:
        want = base64.b64encode(hmac.new(key, f"{i}.{ts}.".encode() + body, hashlib.sha256).digest()).decode()
        if any(hmac.compare_digest(s[3:], want) for s in headers["webhook-signature"].split() if s.startswith("v1,")):
            return True
    return False
```

A key line prefixed with `whsec_` is the secret the Standard Webhooks libraries expect.

## `:8090` — room tools

An MCP server on `POST /mcp`, reached only through an `agent-router` `MCPRoute`, which authenticates
the run first and injects a generated key in `X-Room-Mcp-Key` (`ROOMS_MCP_KEY`).

The listener is the config's top-level `roomTools` block, apart from `human.mcp`: absent, or with no
`enabled`, it is on, as every deployed broker runs it; an explicit `roomTools.enabled: false` binds
no `:8090` at all and starts no server, so `ROOMS_MCP_KEY` is not even read. On, `ROOMS_MCP_KEY`
alone decides whether a call is accepted, and without it every call is refused (`401`).

The broker derives the run from `X-Ar-Agent`, the gateway's verified `sub`, matched whole against each
run issuer's `subPattern` in turn, and reads the run's room and role from its `AgentRun`, never from a
header or an argument. It exposes `initialize`, `ping`, `tools/list` and `tools/call` only: no
`resources` or `prompts`, which the gateway would not authorize by role. It answers `POST` only and
never opens a stream or sends a request of its own (agent-router#2715).

| HTTP | When |
|---|---|
| `401` | `X-Room-Mcp-Key` wrong, missing or sent twice, or `ROOMS_MCP_KEY` unset: every call is refused |
| `403` | `X-Ar-Agent` missing, sent twice or naming no run, or the run is not live or in no room |
| `405` | Anything but `POST` |
| `413` | A body over 128 KiB |
| `503` | A call over 15 s |

`tools/list` lists the tools of the run's role. A call needs the role and the permission matrix's action (see [what each standing may do](web-ui.md#what-each-standing-may-do))
(`read` for `room_read`, `chat` for the three writes), then spends the run's one call per second.
The limit is per broker replica: with two replicas, a run whose calls the gateway
spreads across both can make two a second. Arguments are strict: an unknown field is refused, and text holds
no control character but tab, newline and carriage return. Every write is redacted before it is
appended, attributed to `agent:<runId>` with the run's role, origin `client`.

| Tool | Roles | Arguments | Result | Appends |
|---|---|---|---|---|
| `room_read` | all | `sinceSeq` ≥ 0, `limit` 1–100 (default and cap 100) | `{events, lastSeq}`: the room's `message` and `handoff` events, redacted, at most 1 MiB of payload a reply and 500 events scanned. Pass `lastSeq` as the next `sinceSeq` | nothing |
| `room_post` | all | `text`, 1–16 384 bytes | `{seq}` | `message{kind: chat}`, delivered to nobody |
| `room_progress` | all | `text`, one line of 1–280 characters; one note a minute per run per broker replica, on top of the call limit | `{seq}` | `message{kind: progress}`, delivered to nobody; a refused note over the minute answers `rate_limited: one progress note a minute`. The harness calls it too (#60), through the same identity proxy, so nothing is bypassed: a fixed `Harness (<role>): …` line at the first step and after a commit, push, check run or PR that exited 0, each once, 60 s apart, at most 8. The prefix is a text convention, not an attribution. The minute is shared with the agent's own notes, so the harness's are the guaranteed floor and the model's are best effort. The bounds are per process: the first note of a resumed run starts `(resumed)` |
| `room_handoff` | implementer, tester, triager | `toRole`, `summary` (1–8 192 bytes), `commit` (the full 40-hex sha, `git rev-parse HEAD`; an abbreviation is refused); `scope` (the triager only, listed to it alone: `{state: ready or question, task, touches, leave, plan, assumptions, acceptance: [{check, proves}], question}`, at most 4096 bytes as JSON; any other role's is refused. It is listed to every triager, but only a scoper run's is used: an investigate triager leaves it empty, as a malformed one refuses the handoff); `split` (the triager only, listed to it alone, and only beside a `scope` in state `ready`: `{why, children: [2 to 5 scopes in state ready, in the order they merge]}`, at most 21 KiB as JSON) | `{seq}` | `handoff{fromRole, toRole, summary, commit, branch, scope?, split?}`; `fromRole` and `branch` from the `AgentRun` |
| `room_verdict` | reviewer, tester | `verdict: approve \| changes`, `summary`, `commit` (as `room_handoff`) | `{seq}` | `message{kind: review_verdict, verdict, commit, pullRequest}`: `pullRequest` is the run's `spec.task.url` when it is a pull request of `spec.repository`, else absent. The leader then posts it on the PR |
| `room_judge` | implementer | `verdict: met \| not_yet \| impossible`, `reason` (1–1000 bytes), `evidence` (1–20 ids, one line each) | `{seq}` | `message{kind: judge_verdict, verdict, text, evidence}`. The harness calls it (O15) after each completion-judge round, once its own override is applied (a failing check is never `met`); the summary's `status.lastJudge` and `room_changes`' `judge` milestone show it, the reason as untrusted text. It is the judge's reading, not a decision. It is the harness's alone: the tool is listed and callable only on a request carrying the harness client's `X-Agent-Harness: 1` marker, which the model's own MCP client is configured without, so it is not on the model's tool list and its call is refused `not_permitted`. The marker is not a secret: a command the model runs could send it if it can reach the route with the run's credentials |

A refused call is a tool result with `isError: true`, whose text starts with its reason:

| Reason | Meaning |
|---|---|
| `not_permitted` | The tool is unknown, or not one of the run's role |
| `rate_limited` | A second call within the second |
| `invalid_arguments` | The arguments break the tool's schema; the text says how |
| `room_sealed` | The room takes no more events |
| `no_room` | The run's room has no log |
| `log_unavailable` | The log failed; try again later |

Tool calls carry no idempotency key (MCP request ids are per session), so a retried call can append
twice; both copies are attributed and visible.
