// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BootstrapOptions configures Bootstrap against an existing PostgreSQL 16+ server.
type BootstrapOptions struct {
	// Database is the database to create or verify (default "rooms").
	Database string
	// RotatePasswords resets the passwords of existing Maku roles instead of refusing them.
	RotatePasswords bool
	// Rand is the source of generated passwords (default crypto/rand.Reader).
	Rand io.Reader
}

// BootstrapResult reports what Bootstrap created, rotated, or verified.
type BootstrapResult struct {
	ServerVersion   string
	Database        string
	DatabaseCreated bool
	CloudAdminGrant bool
	RolesCreated    []Role
	RolesRotated    []Role
	Passwords       map[Role]string
}

var (
	// ErrRolesExist is returned when any of the three Maku roles already exists and
	// BootstrapOptions.RotatePasswords is false.
	ErrRolesExist = errors.New("database roles already exist")

	// dbIdentRE matches a safe lowercase PostgreSQL identifier (1-63 chars).
	dbIdentRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// fixedRoles is the deterministic order in which Maku's three fixed roles are created and verified.
var fixedRoles = []Role{RoleOwner, RoleBroker, RoleRetention}

// Constant DDL blocks per fixed role: passwords are supplied via transaction-local
// set_config('maku.pw', $1, true) and quoted inside PostgreSQL with format('%L', ...),
// so Go never interpolates values into SQL.
var (
	createRoleSQL = map[Role]string{
		RoleOwner:     `DO $$ BEGIN EXECUTE format('CREATE ROLE rooms_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
		RoleBroker:    `DO $$ BEGIN EXECUTE format('CREATE ROLE rooms_broker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
		RoleRetention: `DO $$ BEGIN EXECUTE format('CREATE ROLE rooms_retention LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
	}
	alterRoleSQL = map[Role]string{
		RoleOwner:     `DO $$ BEGIN EXECUTE format('ALTER ROLE rooms_owner WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
		RoleBroker:    `DO $$ BEGIN EXECUTE format('ALTER ROLE rooms_broker WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
		RoleRetention: `DO $$ BEGIN EXECUTE format('ALTER ROLE rooms_retention WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', current_setting('maku.pw')); END $$;`,
	}
)

const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Bootstrap prepares an external PostgreSQL 16+ server for Maku: creates (or rotates, with
// RotatePasswords) the three fixed non-privileged roles (rooms_owner, rooms_broker,
// rooms_retention), creates the database owned by rooms_owner (granting rooms_owner to
// CURRENT_USER first when the admin is not a superuser, as on RDS, Cloud SQL and Azure),
// grants CREATE and USAGE on schema public to rooms_owner, and verifies that none of the
// roles can bypass row-level security.
func Bootstrap(ctx context.Context, adminURL string, opts BootstrapOptions) (BootstrapResult, error) {
	if strings.TrimSpace(adminURL) == "" {
		return BootstrapResult{}, errors.New("bootstrap: admin DSN is required")
	}
	dbName := opts.Database
	if dbName == "" {
		dbName = "rooms"
	}
	if !dbIdentRE.MatchString(dbName) {
		return BootstrapResult{}, fmt.Errorf("bootstrap: database name %q must match %s", dbName, dbIdentRE.String())
	}
	cfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: parse admin DSN: %w", err)
	}
	rng := opts.Rand
	if rng == nil {
		rng = rand.Reader
	}

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	var (
		versionNum int
		versionStr string
		adminUser  string
		isSuper    bool
	)
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int, current_setting('server_version'), current_user::text, r.rolsuper FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&versionNum, &versionStr, &adminUser, &isSuper); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: inspect server: %w", err)
	}
	if versionNum < minServerVersion {
		return BootstrapResult{}, tag(ErrServerTooOld, fmt.Errorf("bootstrap: PostgreSQL %s: Maku needs 16 or later (D26)", versionStr))
	}

	existing, err := existingMakuRoles(ctx, conn)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: check roles: %w", err)
	}
	if len(existing) > 0 && !opts.RotatePasswords {
		names := make([]string, 0, len(existing))
		for _, r := range fixedRoles {
			if existing[r] {
				names = append(names, string(r))
			}
		}
		return BootstrapResult{}, tag(ErrRolesExist, fmt.Errorf("bootstrap: roles already exist (%s): pass --rotate-passwords to rotate their passwords and keep the database", strings.Join(names, ", ")))
	}

	passwords := make(map[Role]string, len(fixedRoles))
	for _, role := range fixedRoles {
		pw, err := generatePassword(rng, 32)
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("bootstrap: generate password for %s: %w", role, err)
		}
		passwords[role] = pw
	}

	res := BootstrapResult{
		ServerVersion: versionStr,
		Database:      dbName,
		Passwords:     passwords,
	}

	if err := applyRoles(ctx, conn, existing, passwords, &res); err != nil {
		return BootstrapResult{}, err
	}

	var dbOwner string
	err = conn.QueryRow(ctx, `SELECT pg_get_userbyid(datdba)::text FROM pg_database WHERE datname = $1`, dbName).Scan(&dbOwner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if !isSuper {
			// On Postgres 16+ managed services (RDS, Cloud SQL, Azure Flexible Server), the admin
			// role is not a superuser and holds ADMIN on roles it creates without implicit SET ROLE
			// membership; granting rooms_owner to CURRENT_USER allows CREATE DATABASE ... OWNER rooms_owner.
			if _, err := conn.Exec(ctx, `GRANT rooms_owner TO CURRENT_USER`); err != nil {
				return BootstrapResult{}, fmt.Errorf("bootstrap: grant rooms_owner to %s: %w", adminUser, err)
			}
			res.CloudAdminGrant = true
		}
		createDBSQL := "CREATE DATABASE " + pgx.Identifier{dbName}.Sanitize() + " OWNER rooms_owner"
		if _, err := conn.Exec(ctx, createDBSQL); err != nil { //nolint:gosec // dbName is validated by dbIdentRE and sanitized with pgx.Identifier
			return BootstrapResult{}, fmt.Errorf("bootstrap: create database %s: %w", dbName, err)
		}
		res.DatabaseCreated = true
	case err != nil:
		return BootstrapResult{}, fmt.Errorf("bootstrap: check database %s: %w", dbName, err)
	default:
		if dbOwner != string(RoleOwner) {
			return BootstrapResult{}, fmt.Errorf("bootstrap: database %s already exists and is owned by %q, want %s", dbName, dbOwner, RoleOwner)
		}
	}

	// Connect to the target database to grant public schema privileges to rooms_owner and verify role invariants.
	dbCfg := cfg.Copy()
	dbCfg.Database = dbName
	dbConn, err := pgx.ConnectConfig(ctx, dbCfg)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: connect to database %s: %w", dbName, err)
	}
	defer func() { _ = dbConn.Close(context.WithoutCancel(ctx)) }()

	if _, err := dbConn.Exec(ctx, `GRANT CREATE, USAGE ON SCHEMA public TO rooms_owner`); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: grant schema public to rooms_owner: %w", err)
	}
	if err := verifyBootstrappedRoles(ctx, dbConn); err != nil {
		return BootstrapResult{}, err
	}
	return res, nil
}

func existingMakuRoles(ctx context.Context, conn *pgx.Conn) (map[Role]bool, error) {
	rows, err := conn.Query(ctx, `SELECT rolname::text FROM pg_roles WHERE rolname IN ('rooms_owner', 'rooms_broker', 'rooms_retention')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Role]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[Role(name)] = true
	}
	return out, rows.Err()
}

func applyRoles(ctx context.Context, conn *pgx.Conn, existing map[Role]bool, passwords map[Role]string, res *BootstrapResult) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap: begin role transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, role := range fixedRoles {
		if _, err := tx.Exec(ctx, `SELECT set_config('maku.pw', $1, true)`, passwords[role]); err != nil {
			return fmt.Errorf("bootstrap: stage password for %s: %w", role, err)
		}
		if existing[role] {
			if _, err := tx.Exec(ctx, alterRoleSQL[role]); err != nil {
				return fmt.Errorf("bootstrap: rotate role %s: %w", role, err)
			}
			res.RolesRotated = append(res.RolesRotated, role)
		} else {
			if _, err := tx.Exec(ctx, createRoleSQL[role]); err != nil {
				return fmt.Errorf("bootstrap: create role %s: %w", role, err)
			}
			res.RolesCreated = append(res.RolesCreated, role)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bootstrap: commit roles: %w", err)
	}
	return nil
}

// verifyBootstrappedRoles checks every T12 role invariant against pg_roles before Bootstrap
// returns credentials: none of the three roles may be a superuser, BYPASSRLS, CREATEROLE, CREATEDB,
// or a member of a superuser/BYPASSRLS role, and neither rooms_broker nor rooms_retention may be a
// member of rooms_owner.
func verifyBootstrappedRoles(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `SELECT r.rolname::text, r.rolcanlogin, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb,
		coalesce(pg_has_role(r.oid, to_regrole('rooms_owner'), 'MEMBER'), false),
		coalesce((SELECT string_agg(x.rolname, ', ' ORDER BY x.rolname) FROM pg_roles x
			WHERE (x.rolsuper OR x.rolbypassrls) AND x.oid <> r.oid AND pg_has_role(r.oid, x.oid, 'MEMBER')), '')
		FROM pg_roles r WHERE r.rolname IN ('rooms_owner', 'rooms_broker', 'rooms_retention')`)
	if err != nil {
		return fmt.Errorf("bootstrap: verify roles: %w", err)
	}
	defer rows.Close()

	seen := map[Role]bool{}
	var errs []error
	for rows.Next() {
		var (
			name                                       string
			canLogin, super, bypass, createR, createDB bool
			ownerMember                                bool
			privRoles                                  string
		)
		if err := rows.Scan(&name, &canLogin, &super, &bypass, &createR, &createDB, &ownerMember, &privRoles); err != nil {
			return fmt.Errorf("bootstrap: scan role verification: %w", err)
		}
		role := Role(name)
		seen[role] = true
		if !canLogin {
			errs = append(errs, fmt.Errorf("role %s cannot log in", role))
		}
		if super || bypass || createR || createDB {
			errs = append(errs, fmt.Errorf("role %s has forbidden privilege (super=%v bypassrls=%v createrole=%v createdb=%v)", role, super, bypass, createR, createDB))
		}
		if privRoles != "" {
			errs = append(errs, fmt.Errorf("role %s is a member of privileged role(s) %s", role, privRoles))
		}
		if role != RoleOwner && ownerMember {
			errs = append(errs, fmt.Errorf("role %s is a member of %s", role, RoleOwner))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("bootstrap: verify roles: %w", err)
	}
	for _, role := range fixedRoles {
		if !seen[role] {
			errs = append(errs, fmt.Errorf("role %s was not found after bootstrap", role))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("bootstrap: role invariant check failed: %w", errors.Join(errs...))
	}
	return nil
}

func generatePassword(r io.Reader, n int) (string, error) {
	maxIdx := big.NewInt(int64(len(passwordAlphabet)))
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(r, maxIdx)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[v.Int64()]
	}
	return string(b), nil
}

// BuildRoleDSN constructs a PostgreSQL URI DSN for one of Maku's roles. When host or port is
// empty/zero, they are read from adminURL via pgx.ParseConfig (falling back to port 5432).
func BuildRoleDSN(adminURL, host string, port uint16, database string, role Role, password, sslmode, sslrootcert string) (string, error) {
	if database == "" {
		database = "rooms"
	}
	if !dbIdentRE.MatchString(database) {
		return "", fmt.Errorf("database name %q must match %s", database, dbIdentRE.String())
	}
	if host == "" || port == 0 {
		cfg, err := pgx.ParseConfig(adminURL)
		if err != nil {
			return "", fmt.Errorf("parse admin DSN for host/port: %w", err)
		}
		if host == "" {
			host = cfg.Host
		}
		if port == 0 {
			port = cfg.Port
		}
	}
	if host == "" {
		return "", errors.New("database host is required (pass --host or include it in the admin DSN)")
	}
	if port == 0 {
		port = 5432
	}
	if sslmode == "" {
		sslmode = "verify-full"
	}
	switch sslmode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return "", fmt.Errorf("sslmode %q is not one of disable, require, verify-ca, verify-full", sslmode)
	}
	query := "sslmode=" + sslmode
	if sslrootcert != "" {
		query += "&sslrootcert=" + sslrootcert
	}
	u := &url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(string(role), password),
		Host:     net.JoinHostPort(host, strconv.Itoa(int(port))),
		Path:     "/" + database,
		RawQuery: query,
	}
	return u.String(), nil
}
