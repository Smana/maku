---
title: "The database"
weight: 20
description: "Maku keeps every room's log in PostgreSQL 16 or later: the broker refuses an older server at start. Two adoption tiers share the exact same schema, migrations and three T12."
diataxis: reference
aliases:
  - /docs/database/
---

Maku keeps every room's log in PostgreSQL 16 or later: the broker refuses an older server at start.
Two adoption tiers share the exact same schema, migrations and three T12 database roles:

- **Tier-1 Pilot (bundled PostgreSQL):** enable the umbrella chart's hardened single-replica
  PostgreSQL StatefulSet (`postgres.enabled: true`, `postgres.acknowledgeSingleReplica: true`,
  `postgres.tls.mode: helm | cert-manager | existing`) for a zero-external-database install with
  PVC storage, mandatory TLS 1.3 (`sslmode=verify-full`), default-deny network policies, and
  optional scheduled S3/GCS backups (`charts/agent-factory/values-pilot.example.yaml`). Restarts
  or node drains briefly interrupt rooms until the pod returns; the install banner shows
  `db_single_replica` (and `db_backups_off` when backups are off).
- **Tier-2 Hardened (external HA PostgreSQL):** bring your own CloudNativePG, Amazon RDS, Google
  Cloud SQL or Azure Flexible Server (`postgres.enabled: false`, the chart's default) for
  multi-replica high availability and continuous WAL backups.
- **Laptop demo (`-f values-demo.yaml`):** admits `postgres.enabled: true` with `postgres.tls.mode:
  disabled` and `1Gi` storage for throwaway local clusters only.

## Three roles, one DSN Secret each

The migrations grant to three fixed role names, so an install cannot rename them. Each role has its
own Secret, holding its DSN under one key, `broker.database.secretKey` (default `uri`):

| Role | Value (maku chart) | Read as | By |
|---|---|---|---|
| `rooms_owner` | `broker.database.ownerSecret` | `ROOMS_MIGRATION_DATABASE_URL` | the broker's `migrate` init container |
| `rooms_broker` | `broker.database.brokerSecret` | `ROOMS_DATABASE_URL` | the broker |
| `rooms_retention` | `broker.database.retentionSecret` | `ROOMS_DATABASE_URL` | the CronJob `room-broker-retention`, daily at 03:17 (`broker.retention.schedule`) |

The factory has no database: only the broker and its retention job read these Secrets.

**Migrations run at start-up.** With `ownerSecret` set, the broker's pod runs `maku-broker migrate` as
an init container before the broker starts. It waits for its Secret, works with any GitOps
controller, and an advisory lock applies each file once across replicas. Without `ownerSecret`
nothing migrates, and the broker refuses to serve a schema older than its binary.

**What each command checks before it touches the log.** `migrate`, the broker and the retention job
each refuse to start, with one message per failure, when:

| Check | Message |
|---|---|
| The server is below 16 | `PostgreSQL <version>: Maku needs 16 or later (D26)` |
| The DSN is another role's | `connected as "<role>": the broker runs as rooms_broker` (likewise for the others) |
| The role bypasses row-level security: a superuser, `BYPASSRLS`, a member of a role that is either, or, for the broker and the retention job, a member of `rooms_owner` or the owner of a table with row-level security and no `FORCE ROW LEVEL SECURITY` | `<role> bypasses row-level security (<why>): the room log would be writable` |
| `migrate` only: `rooms_broker` or `rooms_retention` does not exist | `create the login roles rooms_broker and rooms_retention first (docs/database.md)` |
| The broker only: the schema is behind its binary | `the schema is at <version>, this broker needs <version>: run migrate (broker.database.ownerSecret)` |
| The broker only: `rooms_broker` cannot read the migration ledger | `rooms_broker cannot read the migration ledger: set broker.database.ownerSecret, whose migrate grants SELECT (version) ON public.room_broker_revisions TO rooms_broker (docs/database.md)` |

A database the command cannot reach is not a refusal. The retention job retries it every 5 s for two
minutes: a CNI that enforces NetworkPolicy admits a new pod's address a few seconds after it starts.
A wrong password, a missing database or a certificate the client cannot verify fails at once.

### A ledger the broker cannot read

The broker refuses to start when `rooms_broker` cannot read the migration ledger, because it then
cannot tell the schema's version. This happens on a database the Atlas operator migrated, whose
ledger only its owner reads, and on a `public.room_broker_revisions` whose grant was revoked. The
preflight's `database` check fails with `db_ledger_unreadable`.

Set `ownerSecret`: the first `migrate` adopts the operator's ledger into
`public.room_broker_revisions` and grants `rooms_broker` `SELECT (version)` on it, every run. Until
v0.49.0 this was a warning, the gauge `maku_rooms_schema_unverified` and the alert
`RoomBrokerSchemaUnverified`; both are gone ([upgrading]({{< relref "/docs/operate/upgrading.md#an-unreadable-ledger-is-refused" >}})).

## Bootstrap

### Automated bootstrap (`maku db bootstrap`)

On an existing PostgreSQL 16 or later server (managed RDS, Cloud SQL, Azure Flexible Server or
self-hosted), `maku db bootstrap` creates the three fixed roles with random 32-character passwords,
creates the `rooms` database owned by `rooms_owner` (automatically granting `rooms_owner` to
`CURRENT_USER` when the admin role is not a superuser), grants `CREATE, USAGE` on schema `public` to
`rooms_owner`, verifies that none of the roles bypasses row-level security, and writes the three
Kubernetes `Secret` manifests to a mode `0600` file. Pass the admin DSN through `MAKU_DB_ADMIN_URL`
(never as a CLI flag):

```bash
export MAKU_DB_ADMIN_URL='postgresql://<admin>:<password>@<host>:5432/postgres?sslmode=require'
maku db bootstrap --out secrets/database.yaml --namespace maku --ca-secret rooms-db-ca
kubectl apply -n maku -f secrets/database.yaml
```

If the three roles already exist, `maku db bootstrap` refuses to overwrite live credentials unless
you pass `--rotate-passwords`. Pass `--out -` to write the manifests to stdout.

### Manual SQL

Run once, as a role that may create roles and databases, then put each role's password in its DSN
Secret. None of the three roles needs `SUPERUSER`, `CREATEROLE`, `CREATEDB` or `BYPASSRLS`, and none
may have them.

```sql
CREATE ROLE rooms_owner LOGIN PASSWORD 'change-me-owner';
CREATE ROLE rooms_broker LOGIN PASSWORD 'change-me-broker';
CREATE ROLE rooms_retention LOGIN PASSWORD 'change-me-retention';
CREATE DATABASE rooms OWNER rooms_owner;
\connect rooms
GRANT CREATE, USAGE ON SCHEMA public TO rooms_owner;
```

These are the bundled Postgres's own statements (`rooms-db-init`), and the external-database test
(`task chart:install:k3d:external-db`) runs this block as written.

The DSNs, with TLS verified against the database's CA:

```text
postgresql://rooms_owner:<password>@<host>:5432/rooms?sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt
```

`broker.database.caSecret` names a Secret with the key `ca.crt`. The chart mounts it at
`/etc/room-broker/db-ca/ca.crt` in the `migrate` init container, the broker and the retention job.

```bash
kubectl create secret generic rooms-owner-dsn -n maku --from-literal=uri='postgresql://rooms_owner:...'
kubectl create secret generic rooms-broker-dsn -n maku --from-literal=uri='postgresql://rooms_broker:...'
kubectl create secret generic rooms-retention-dsn -n maku --from-literal=uri='postgresql://rooms_retention:...'
```

```yaml
# agent-factory values, beside -f values-demo.yaml or your own profile
postgres:
  enabled: false
maku:
  broker:
    database:
      ownerSecret: rooms-owner-dsn
      brokerSecret: rooms-broker-dsn
      retentionSecret: rooms-retention-dsn
      caSecret: rooms-db-ca
      # a database outside the cluster: the broker's and the job's egress on 5432
      egress:
        cidrs: [10.20.0.0/16]
```

## Managed services and operators

| Where | How |
|---|---|
| Amazon RDS, Google Cloud SQL, Azure Database for PostgreSQL flexible server | The admin user is not a superuser. On Postgres 16 it holds `ADMIN` on the roles it creates but cannot yet act as them: run `GRANT rooms_owner TO CURRENT_USER;` between the `CREATE ROLE` lines and `CREATE DATABASE`. Never grant `rds_superuser`, `cloudsqlsuperuser` or `azure_pg_admin` to the three roles. Use the provider's CA bundle as `caSecret` |
| CloudNativePG | Declare the three roles under `spec.managed.roles` (`login: true`, a `passwordSecret` each, nothing else), and the database with a `Database` resource owned by `rooms_owner`. Build each DSN Secret from the role's password Secret, with an ExternalSecret template or by hand. The cluster's CA Secret (`<cluster>-ca`, key `ca.crt`) is the `caSecret` |
| A database the Atlas operator migrated (cloud-native-ref's `SQLInstance` with `atlasSchema`) | Give the broker `ownerSecret`. Its first `migrate` adopts the operator's ledger, the table `atlas_schema_revisions` in the schema of the same name, in `public` or in the connection's current schema, when its revisions are exactly the first shipped files with their `atlas.sum` hashes, and logs `adopted <n> revisions from the Atlas operator`. Nothing is applied twice; any other ledger, two of them, or an operator ledger that later runs past `migrate`'s own, is refused with nothing written. `rooms_owner` must be able to read that ledger. Then drop `atlasSchema` from the claim |

## Tier-1 Pilot: hardened bundled PostgreSQL

To run a pilot without provisioning an external database, layer
`charts/agent-factory/values-pilot.example.yaml` over your values file. The chart generates and
keeps the database passwords, three DSN Secrets (`rooms-owner-dsn`, `rooms-broker-dsn`,
`rooms-retention-dsn`) with `sslmode=verify-full`, a `20Gi` PVC, default-deny `NetworkPolicy` or
`CiliumNetworkPolicy`, and TLS 1.3 (`hostssl` + `scram-sha-256`):

- **`postgres.tls.mode: helm`** generates and keeps `rooms-db-tls` and `rooms-db-ca` across
  upgrades without requiring cert-manager.
- **`postgres.tls.mode: cert-manager`** renders a `Certificate` from `postgres.tls.issuerRef`.
- **`postgres.tls.mode: existing`** uses operator-supplied `postgres.tls.secretName` and
  `postgres.tls.caSecretName`.

The bundled instance works out of the box on its PVC alone (take CSI `VolumeSnapshot`s if your
StorageClass supports them). To push daily custom-format `pg_dump` archives to S3 or GCS, enable
`postgres.backup.enabled: true` with a digest-pinned `postgres.backup.uploader.image`, upload
`command`, `destination` and egress allowlist; the dump init container holds `rooms-owner-dsn` and
the upload container mounts only the dump directory read-only.

### Graduating from Tier-1 Pilot to Tier-2 HA PostgreSQL

Both tiers use identical PostgreSQL 16+ schemas and roles:

1. Scale `room-broker` and `agent-factory` to `0`.
2. Export the database with `pg_dump --format=custom --no-owner` against `rooms-db`.
3. Bootstrap the Tier-2 target (below) and restore with `pg_restore` as `rooms_owner`.
4. Switch `postgres.enabled: false`, point `maku.broker.database` at the Tier-2 DSN and CA Secrets,
   set `singleReplica: false` and `backupsOff: false`, scale back up, and run `maku preflight`.

## Backups and high availability

In Tier-2, both are the operator's: back up the `rooms` database the way your Postgres is backed
up (CNPG's Barman Cloud plugin, RDS snapshots, Cloud SQL backups), and restore it into a server of
the same or a later major version. The broker reconnects after a failover;
[Operations]({{< relref "/docs/operate/rooms.md#backups-and-rebuilds" >}}) has cloud-native-ref's settings.
