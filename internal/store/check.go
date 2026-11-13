// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Smana/maku/internal/store/migrations"
)

// Role is one of the three fixed database roles. The migrations GRANT to them by name, so an
// install cannot rename them (v4 D9).
type Role string

// The roles: the schema's owner (migrate), the broker (serve, intent-samples) and the retention
// job (retention).
const (
	RoleOwner     Role = ownerRole
	RoleBroker    Role = "rooms_broker"
	RoleRetention Role = "rooms_retention"
)

// minServerVersion is Postgres 16 (D26), as server_version_num.
const minServerVersion = 160000

// Server is what Check read about the connection: the server's version, the role connected and
// whether the connection is encrypted. TLS is reported, never required: the demo's hop is plain.
type Server struct {
	Version string
	User    string
	TLS     bool
}

// querier is a *pgx.Conn or a *pgxpool.Pool.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkSQL reads every fact Check judges in one statement, so a pool answers it on one backend.
// pg_has_role is strict: with no rooms_owner role it gives NULL, read as "not a member".
const checkSQL = `SELECT current_setting('server_version_num')::int, current_setting('server_version'),
	current_user::text, r.rolsuper, r.rolbypassrls,
	coalesce(pg_has_role(current_user, to_regrole('rooms_owner'), 'MEMBER'), false),
	to_regrole('rooms_broker') IS NOT NULL, to_regrole('rooms_retention') IS NOT NULL,
	coalesce((SELECT s.ssl FROM pg_stat_ssl s WHERE s.pid = pg_backend_pid()), false)
	FROM pg_roles r WHERE r.rolname = current_user`

// Check refuses a connection the room log must not run on, with one message per failure: a
// server below Postgres 16, a role other than the one expected, a role that bypasses row-level
// security (superuser, BYPASSRLS, or, for the broker and the retention job, membership in the
// owner role), and, for the owner, a missing login role the migrations GRANT to. The room log's
// immutability rests on the grants and the policies (T12, SC-10), so none of these is a warning.
func Check(ctx context.Context, q querier, role Role) (Server, error) {
	var (
		s                                       Server
		num                                     int
		super, bypass, member, broker, retainer bool
	)
	if err := q.QueryRow(ctx, checkSQL).Scan(&num, &s.Version, &s.User, &super, &bypass, &member,
		&broker, &retainer, &s.TLS); err != nil {
		return s, fmt.Errorf("database check: %w", err)
	}
	var errs []error
	if num < minServerVersion {
		errs = append(errs, fmt.Errorf("PostgreSQL %s: Maku needs 16 or later (D26)", s.Version))
	}
	if s.User != string(role) {
		errs = append(errs, wrongRole(s.User, role))
	}
	if super || bypass || (role != RoleOwner && member) {
		errs = append(errs, fmt.Errorf("%s bypasses row-level security: the room log would be writable", s.User))
	}
	if role == RoleOwner && (!broker || !retainer) {
		errs = append(errs, errors.New("create the login roles rooms_broker and rooms_retention first (docs/database.md)"))
	}
	return s, errors.Join(errs...)
}

// wrongRole names the role connected and the one this command runs as. The owner's message is
// migrate's own: every object the files create belongs to rooms_owner.
func wrongRole(user string, role Role) error {
	switch role {
	case RoleOwner:
		return fmt.Errorf("connected as %q: the migrations run as %s, which owns every table, "+
			"policy and index they create, so give the migrate DSN %s's credentials", user, ownerRole, ownerRole)
	case RoleRetention:
		return fmt.Errorf("connected as %q: the retention job runs as %s", user, role)
	default:
		return fmt.Errorf("connected as %q: the broker runs as %s", user, role)
	}
}

// ErrSchemaUnverified is a schema version serve could not read: its ledger exists but this role
// cannot read it. The broker serves, and says so once.
var ErrSchemaUnverified = errors.New("the schema version is unverified: this role cannot read the migration ledger")

// newestVersionSQL reads the ledger's newest version, granted to rooms_broker on its version
// column alone (Migrate): the broker reads which files ran, never writes the ledger.
const newestVersionSQL = `SELECT coalesce(max(version), '') FROM room_broker_revisions`

// SchemaCurrent is serve's check that the schema is the one this binary was built for: the newest
// recorded revision must be the newest embedded file. A ledger behind names migrate; a ledger
// ahead is migrate's own refusal. A ledger the role cannot read is ErrSchemaUnverified.
func (s *Store) SchemaCurrent(ctx context.Context) error {
	files, err := migrations.Files()
	if err != nil {
		return fmt.Errorf("schema check: %w", err)
	}
	return schemaCurrent(ctx, s.pool, files)
}

func schemaCurrent(ctx context.Context, q querier, files []migrations.File) error {
	want := files[len(files)-1].Version
	have, err := newestApplied(ctx, q)
	if err != nil {
		return err
	}
	shipped := false
	for _, f := range files {
		shipped = shipped || f.Version == have
	}
	switch {
	case have == "":
		return fmt.Errorf("the schema is at none, this broker needs %s: run migrate (broker.database.ownerSecret)", want)
	case !shipped || have > want:
		return fmt.Errorf("the database has version %s, which this binary does not ship: it is newer than the code", have)
	case have < want:
		return fmt.Errorf("the schema is at %s, this broker needs %s: run migrate (broker.database.ownerSecret)", have, want)
	}
	return nil
}

// newestApplied reads the newest applied version from room_broker_revisions, else from the Atlas
// operator's ledger (a cnref database before its first migrate): "" when the database holds
// neither ledger, ErrSchemaUnverified when the one it holds is not readable by this role.
func newestApplied(ctx context.Context, q querier) (string, error) {
	var exists, readable bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.room_broker_revisions') IS NOT NULL,
		coalesce(has_column_privilege(to_regclass('public.room_broker_revisions'), 'version', 'SELECT'), false)`).Scan(&exists, &readable); err != nil {
		return "", fmt.Errorf("schema check: %w", err)
	}
	sql, args := newestVersionSQL, []any{}
	if !exists {
		if err := q.QueryRow(ctx, atlasLedgerSQL).Scan(&exists, &readable); err != nil {
			return "", fmt.Errorf("schema check: %w", err)
		}
		sql, args = newestAtlasVersionSQL, []any{atlasCloudIdentifier}
	}
	if !exists {
		return "", nil
	}
	if !readable {
		return "", ErrSchemaUnverified
	}
	var v string
	if err := q.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		return "", fmt.Errorf("schema check: %w", err)
	}
	return v, nil
}

// newestAtlasVersionSQL is the operator's newest complete revision: a partial or failed one has
// not finished its file.
const newestAtlasVersionSQL = `SELECT coalesce(max(version), '') FROM atlas_schema_revisions.atlas_schema_revisions
	WHERE version <> $1 AND applied = total AND coalesce(error, '') = ''`
