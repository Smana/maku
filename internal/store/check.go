// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// ErrUnavailable is a database Check could not ask: unreachable, not answering, starting up or out
// of connections. Unlike a refusal (a wrong password, a missing database, a certificate the client
// cannot verify), it may pass on a retry.
var ErrUnavailable = errors.New("database unavailable")

// What a Check or a schema check refused, for a caller that maps it to a code (the preflight's
// database check): each is matched with errors.Is, and leaves the message as it was.
var (
	ErrServerTooOld = errors.New("the server is below Postgres 16")
	ErrWrongRole    = errors.New("connected as another role")
	ErrBypassesRLS  = errors.New("the role bypasses row-level security")
	ErrMissingRoles = errors.New("the login roles are missing")
	ErrSchemaBehind = errors.New("the schema is behind the binary")
	ErrSchemaAhead  = errors.New("the schema is ahead of the binary")
)

// tagged is err, also matching kind under errors.Is: the message stays err's.
type tagged struct {
	kind error
	err  error
}

func (t tagged) Error() string        { return t.err.Error() }
func (t tagged) Unwrap() error        { return t.err }
func (t tagged) Is(target error) bool { return target == t.kind }

func tag(kind, err error) error { return tagged{kind: kind, err: err} }

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
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// checkSQL reads every fact Check judges in one statement, so a pool answers it on one backend.
// pg_has_role is strict: with no rooms_owner role it gives NULL, read as "not a member". The two
// lists are the other roles this one may SET ROLE to that are superusers or BYPASSRLS, and the
// tables it owns (or may act as the owner of) whose policies their owner skips (no FORCE).
const checkSQL = `SELECT current_setting('server_version_num')::int, current_setting('server_version'),
	current_user::text, r.rolsuper, r.rolbypassrls,
	coalesce(pg_has_role(current_user, to_regrole('rooms_owner'), 'MEMBER'), false),
	to_regrole('rooms_broker') IS NOT NULL, to_regrole('rooms_retention') IS NOT NULL,
	coalesce((SELECT s.ssl FROM pg_stat_ssl s WHERE s.pid = pg_backend_pid()), false),
	coalesce((SELECT string_agg(x.rolname, ', ' ORDER BY x.rolname) FROM pg_roles x
		WHERE (x.rolsuper OR x.rolbypassrls) AND x.oid <> r.oid AND pg_has_role(current_user, x.oid, 'MEMBER')), ''),
	coalesce((SELECT string_agg(c.oid::regclass::text, ', ' ORDER BY c.oid::regclass::text) FROM pg_class c
		WHERE c.relkind IN ('r', 'p') AND c.relrowsecurity AND NOT c.relforcerowsecurity
		AND pg_has_role(current_user, c.relowner, 'MEMBER')), '')
	FROM pg_roles r WHERE r.rolname = current_user`

// Check refuses a connection the room log must not run on, with one message per failure: a
// server below Postgres 16, a role other than the one expected, a role that bypasses row-level
// security (superuser, BYPASSRLS, membership in a role that is either, and, for the broker and the
// retention job, membership in the owner role or owning a table without FORCE ROW LEVEL SECURITY),
// and, for the owner, a missing login role the migrations GRANT to. The owner owns every table
// the files create, so owning one is its job, not a bypass. The room log's
// immutability rests on the grants and the policies (T12, SC-10), so none of these is a warning.
func Check(ctx context.Context, q querier, role Role) (Server, error) {
	var (
		s                                       Server
		num                                     int
		super, bypass, member, broker, retainer bool
		bypassRoles, unforced                   string
	)
	if err := q.QueryRow(ctx, checkSQL).Scan(&num, &s.Version, &s.User, &super, &bypass, &member,
		&broker, &retainer, &s.TLS, &bypassRoles, &unforced); err != nil {
		if unavailable(err) {
			return s, fmt.Errorf("database check: %w: %w", ErrUnavailable, err)
		}
		return s, fmt.Errorf("database check: %w", err)
	}
	var errs []error
	if num < minServerVersion {
		errs = append(errs, tag(ErrServerTooOld, fmt.Errorf("PostgreSQL %s: Maku needs 16 or later (D26)", s.Version)))
	}
	if s.User != string(role) {
		errs = append(errs, tag(ErrWrongRole, wrongRole(s.User, role)))
	}
	var why []string
	if super {
		why = append(why, "a superuser")
	}
	if bypass {
		why = append(why, "BYPASSRLS")
	}
	if bypassRoles != "" && !super {
		why = append(why, "a member of "+bypassRoles+", superuser or BYPASSRLS")
	}
	if role != RoleOwner && member {
		why = append(why, "a member of "+ownerRole)
	}
	if role != RoleOwner && unforced != "" && !super {
		why = append(why, "the owner of "+unforced+", without FORCE ROW LEVEL SECURITY")
	}
	if len(why) > 0 {
		errs = append(errs, tag(ErrBypassesRLS, fmt.Errorf("%s bypasses row-level security (%s): the room log would be writable",
			s.User, strings.Join(why, "; "))))
	}
	if role == RoleOwner && (!broker || !retainer) {
		errs = append(errs, tag(ErrMissingRoles, errors.New("create the login roles rooms_broker and rooms_retention first (docs/database.md)")))
	}
	return s, errors.Join(errs...)
}

// unavailable tells a database a retry may reach from one no retry fixes. A Postgres error is
// transient only in classes 08 (connection exception), 53 (insufficient resources: too many
// connections) and 57 (the server starting up or shutting down); 28 (authentication), 3D (no such
// database) and every other class are refusals, and so is a certificate the client cannot verify
// or a server that refuses TLS. Anything else (refused, reset, timed out) is the network's.
func unavailable(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code[:min(2, len(pg.Code))] {
		case "08", "53", "57":
			return true
		}
		return false
	}
	var (
		authority x509.UnknownAuthorityError
		host      x509.HostnameError
		invalid   x509.CertificateInvalidError
		verify    *tls.CertificateVerificationError
	)
	if errors.As(err, &authority) || errors.As(err, &host) || errors.As(err, &invalid) || errors.As(err, &verify) {
		return false
	}
	return !strings.Contains(err.Error(), "server refused TLS connection")
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

// ErrLedgerUnreadable is a schema version serve could not read: its ledger exists but this role
// cannot read it. The broker refuses to serve it, as it refuses a schema behind or ahead (v4 D9):
// migrate, with broker.database.ownerSecret, adopts an Atlas operator's ledger and grants the read.
var ErrLedgerUnreadable = errors.New("rooms_broker cannot read the migration ledger: set broker.database.ownerSecret, " +
	"whose migrate grants SELECT (version) ON public.room_broker_revisions TO rooms_broker (docs/database.md)")

// newestVersionSQL reads the ledger's newest version, granted to rooms_broker on its version
// column alone (Migrate): the broker reads which files ran, never writes the ledger.
const newestVersionSQL = `SELECT coalesce(max(version), '') FROM public.room_broker_revisions`

// SchemaCurrent is serve's check that the schema is the one this binary was built for: the newest
// recorded revision must be the newest embedded file. A ledger behind names migrate; a ledger
// ahead is migrate's own refusal. A ledger the role cannot read is ErrLedgerUnreadable.
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
		return tag(ErrSchemaBehind, fmt.Errorf("the schema is at none, this broker needs %s: run migrate (broker.database.ownerSecret)", want))
	case !shipped || have > want:
		return tag(ErrSchemaAhead, fmt.Errorf("the database has version %s, which this binary does not ship: it is newer than the code", have))
	case have < want:
		return tag(ErrSchemaBehind, fmt.Errorf("the schema is at %s, this broker needs %s: run migrate (broker.database.ownerSecret)", have, want))
	}
	return nil
}

// newestApplied reads the newest applied version from public.room_broker_revisions, where
// createRevisions makes it whatever the search_path, else from the Atlas
// operator's ledger (a cnref database before its first migrate): "" when the database holds
// neither ledger, ErrLedgerUnreadable when the one it holds is not readable by this role.
func newestApplied(ctx context.Context, q querier) (string, error) {
	var exists, readable bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.room_broker_revisions') IS NOT NULL,
		coalesce(has_column_privilege(to_regclass('public.room_broker_revisions'), 'version', 'SELECT'), false)`).Scan(&exists, &readable); err != nil {
		return "", fmt.Errorf("schema check: %w", err)
	}
	sql, args := newestVersionSQL, []any{}
	if !exists {
		l, err := findAtlasLedger(ctx, q)
		if err != nil {
			return "", fmt.Errorf("schema check: %w", err)
		}
		exists, readable = l.table != "", l.readable
		sql, args = fmt.Sprintf(newestAtlasVersionSQL, l.table), []any{atlasCloudIdentifier}
	}
	if !exists {
		return "", nil
	}
	if !readable {
		return "", ErrLedgerUnreadable
	}
	var v string
	if err := q.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		return "", fmt.Errorf("schema check: %w", err)
	}
	return v, nil
}

// newestAtlasVersionSQL is the operator's newest complete revision: a partial or failed one has
// not finished its file.
const newestAtlasVersionSQL = `SELECT coalesce(max(version), '') FROM %s
	WHERE version <> $1 AND applied = total AND coalesce(error, '') = ''`
