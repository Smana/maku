---
title: "The Room CRD"
weight: 40
description: "Room (maku.ogenki.io/v1alpha1), namespaced, with a status subresource (api/v1alpha1, generated into config/crd/maku.ogenki.iorooms.yaml). kubectl get rooms prints Phase."
diataxis: reference
aliases:
  - /docs/room-crd/
---

`Room` (`maku.ogenki.io/v1alpha1`), namespaced, with a `status` subresource
(`api/v1alpha1`, generated into `config/crd/maku.ogenki.io_rooms.yaml`). `kubectl get rooms`
prints `Phase`, `Driver`, `Seq`, `Pending` and `Class`.

**Name.** A CEL rule requires an 8-character base32 room id: `self.metadata.name.matches('^[a-z2-7]{8}$')`, message
`a Room is named with a C2 id: 8 characters of [a-z2-7]`.

| `spec` field | Type | Required | Default | Validation | Meaning |
|---|---|---|---|---|---|
| `owner` | string | yes | — | `^(human:[A-Za-z0-9@._-]+\|system:[a-z0-9-]+)$`, at most 261 characters | Who owns the room. 261 = OIDC's 255-character `sub` plus `human:` |
| `driver` | string | yes | — | Same as `owner` | The initial driver-token holder; afterwards the log decides |
| `members` | list, map keyed by `principal` | no | — | At most 20 | Humans only; runs join through their own `spec.roomRef` |
| `members[].principal` | string | yes | — | `^human:[A-Za-z0-9@._-]+$`, at most 261 characters | |
| `members[].role` | string | yes | — | `watcher`, `collaborator`, `owner` | Cumulative room role |
| `members[].approver` | bool | no | `false` | — | The approver flag |
| `approvals` | object | no | `{}` | — | Present even when omitted, so its own defaults apply |
| `approvals.profile` | string | no | `attended` | `attended`, `unattended` | The approval mode. `attended`: `forge.pr`, `forge.other` and `mcp.write` ask a human, denied after 30 min. `unattended`: no human by default, so `forge.pr` is allowed and `forge.other`, `mcp.write` and `egress.new` are denied outright; a class overridden to `human` waits `approvals.ttl`, then is denied |
| `approvals.overrides` | map | no | — | Keys: `forge.push`, `forge.pr`, `forge.other`, `mcp.write`, `shell.high`; values: `allow`, `deny`, `human` | Per-class override of the approval mode |
| `approvals.ttl` | string | no | `4h` | `^[1-9][0-9]{0,3}(m\|h)$` (at most 9999h) | Auto-deny delay for an unattended approval |
| `approvals.fourEyes` | bool | no | `false` | — | The approver must not have prompted the turn |
| `retention` | string | no | `90d` | `^[1-9][0-9]{0,3}d$` | How long the sealed log is kept; immutable |
| `dataClass` | string | **yes** | — | `public`, `internal`; immutable | Runs requested for this room inherit it |
| `repository` | string | no | `Smana/cloud-native-ref` | `^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$` | The repository its runs work on |

| `status` field | Type | Meaning |
|---|---|---|
| `phase` | string | `Open`, `Active`, `Idle`, `AwaitingHuman` or `Closed` ([A room's phases]({{< relref "/docs/concepts/glossary.md#a-rooms-phases" >}})) |
| `lastSeq` | integer | The log's high-water mark |
| `driver` | string | The current driver-token holder |
| `driverEpoch` | integer | Bumped on every driver change |
| `pendingApprovals` | integer | Undecided approvals |
| `observedGeneration` | integer | The `spec` generation last projected |

The broker's leader writes `status` from the log. A minimal room:

```yaml
apiVersion: maku.ogenki.io/v1alpha1
kind: Room
metadata: {name: 3kq7x2ma, namespace: agent-system}
spec: {owner: "system:factory", driver: "system:factory", dataClass: public}
```
