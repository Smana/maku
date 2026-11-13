# The database

Maku keeps every room's log in a Postgres you run. Bring Postgres 16 or later: the broker refuses an
older server at start. The bundled single-replica Postgres of the `agent-factory` chart
(`postgres.enabled`) is for the laptop demo only: it has no backup, no high availability and no TLS,
and any profile but `demo` refuses it.

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

A database the command cannot reach is not a refusal. The retention job retries it every 5 s for two
minutes: a CNI that enforces NetworkPolicy admits a new pod's address a few seconds after it starts.
A wrong password, a missing database or a certificate the client cannot verify fails at once.

### A ledger the broker cannot read

This is a bridge for a database the Atlas operator migrated: cloud-native-ref's, before its broker
has `ownerSecret`. `rooms_broker` cannot read the operator's ledger, so it cannot tell the schema's
version. The broker serves anyway, logs `schema version unverified`, and sets
`maku_rooms_schema_unverified` to 1, which the alert `RoomBrokerSchemaUnverified` fires on.

Set `ownerSecret` to end it: the first `migrate` adopts the operator's ledger into
`public.room_broker_revisions`, which the broker reads, and the gauge drops to 0. Once
cloud-native-ref has done so, a future minor release turns an unreadable ledger into a refusal, and
its release notes say so.

## Bootstrap

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

## Backups and high availability

Both are the operator's: Maku ships no backup job and no replica. Back up the `rooms` database the way
your Postgres is backed up (CNPG's Barman Cloud plugin, RDS snapshots, Cloud SQL backups), and
restore it into a server of the same or a later major version. The broker reconnects after a
failover; [Operations](operations.md#backups-and-rebuilds) has cloud-native-ref's settings.
