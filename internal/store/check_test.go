// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Smana/maku/internal/store/migrations"
)

// Postgres 16 is the floor (D26): 15 is refused by migrate and by serve before a statement runs,
// and 16, 17 and 18 (the suite's own server) migrate from empty and serve.
func TestCheckServerVersions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts three Postgres servers")
	}
	log := slog.New(slog.DiscardHandler)
	for _, c := range []struct {
		image  string
		refuse bool
	}{
		{"postgres:15-alpine", true},
		{"postgres:16-alpine", false},
		{"postgres:17-alpine", false},
	} {
		t.Run(c.image, func(t *testing.T) {
			ctx := t.Context()
			super := startPostgres(t, c.image)
			exec(t, super, `CREATE ROLE rooms_owner LOGIN PASSWORD 'owner'; -- pragma: allowlist secret
				CREATE ROLE rooms_broker LOGIN PASSWORD 'broker'; -- pragma: allowlist secret
				CREATE ROLE rooms_retention LOGIN PASSWORD 'retention'; -- pragma: allowlist secret`)
			exec(t, super, "CREATE DATABASE rooms OWNER rooms_owner")
			db := inDB(super, "rooms")
			exec(t, db, "GRANT CREATE, USAGE ON SCHEMA public TO rooms_owner")
			_, migErr := Migrate(ctx, as(db, "rooms_owner", "owner"), log)
			st, openErr := Open(ctx, as(db, "rooms_broker", "broker"), RoleBroker)
			if st != nil {
				defer st.Close()
			}
			if c.refuse {
				for name, err := range map[string]error{"migrate": migErr, "serve": openErr} {
					if err == nil || !strings.Contains(err.Error(), "Maku needs 16 or later (D26)") || !strings.Contains(err.Error(), "PostgreSQL 15.") {
						t.Errorf("%s on %s: err = %v, want the version refusal", name, c.image, err)
					}
				}
				if got := query(t, db, "SELECT (to_regclass('public.room_broker_revisions') IS NULL)::text"); got != "true" {
					t.Error("migrate on 15 wrote the ledger")
				}
				return
			}
			if migErr != nil || openErr != nil {
				t.Fatalf("%s: migrate %v, open %v", c.image, migErr, openErr)
			}
			if err := st.SchemaCurrent(ctx); err != nil {
				t.Fatalf("%s: SchemaCurrent = %v", c.image, err)
			}
		})
	}
	t.Run("postgres:18-alpine", func(t *testing.T) {
		_, broker, _, _ := testDB(t)
		st, err := Open(t.Context(), broker, RoleBroker)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if !strings.HasPrefix(st.Server.Version, "18.") {
			t.Fatalf("the suite's server is %s, want 18", st.Server.Version)
		}
	})
}

// startPostgres runs image for this test alone and returns its superuser's DSN.
func startPostgres(t *testing.T, image string) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	super, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return super
}

// Each command connects as its own role and no other: the broker's DSN pointing at the owner, or
// the retention job's at the broker, is refused by name before the log is touched.
func TestCheckRole(t *testing.T) {
	owner, broker, _, _ := testDB(t)
	for _, c := range []struct {
		name, dsn string
		role      Role
		want      string
	}{
		{"serve as the owner", owner, RoleBroker, `connected as "rooms_owner": the broker runs as rooms_broker`},
		{"retention as the broker", broker, RoleRetention, `connected as "rooms_broker": the retention job runs as rooms_retention`},
		{"migrate as the broker", broker, RoleOwner, `connected as "rooms_broker": the migrations run as rooms_owner`},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := Open(t.Context(), c.dsn, c.role)
			if err == nil {
				st.Close()
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Open = %v, want %q", err, c.want)
			}
		})
	}
}

// A role that bypasses row-level security would make the append-only log writable: BYPASSRLS, a
// superuser, membership in a role that is either (SET ROLE reaches it), the broker or the
// retention job holding the owner's role, or owning a table whose RLS its owner skips (no FORCE).
// Roles are cluster-wide, so each case undoes itself; the suite's tests run one at a time.
func TestCheckRefusesRLSBypass(t *testing.T) {
	owner, broker, retention, super := testDB(t)
	for _, c := range []struct {
		name, grant, revoke, dsn string
		db                       bool // grant runs in the test's database, which is dropped after
		role                     Role
	}{
		{"BYPASSRLS", "ALTER ROLE rooms_broker BYPASSRLS", "ALTER ROLE rooms_broker NOBYPASSRLS", broker, false, RoleBroker},
		{"a member of rooms_owner", "GRANT rooms_owner TO rooms_broker", "REVOKE rooms_owner FROM rooms_broker", broker, false, RoleBroker},
		{"a superuser", "ALTER ROLE rooms_retention SUPERUSER", "ALTER ROLE rooms_retention NOSUPERUSER", retention, false, RoleRetention},
		{"retention in rooms_owner", "GRANT rooms_owner TO rooms_retention", "REVOKE rooms_owner FROM rooms_retention", retention, false, RoleRetention},
		{"a member of a BYPASSRLS role", "CREATE ROLE t_bypass NOLOGIN BYPASSRLS; GRANT t_bypass TO rooms_broker",
			"DROP ROLE t_bypass", broker, false, RoleBroker},
		{"a member of a superuser role", "CREATE ROLE t_super NOLOGIN SUPERUSER; GRANT t_super TO rooms_retention",
			"DROP ROLE t_super", retention, false, RoleRetention},
		{"the owner in a BYPASSRLS role", "CREATE ROLE t_owner_bypass NOLOGIN BYPASSRLS; GRANT t_owner_bypass TO rooms_owner",
			"DROP ROLE t_owner_bypass", owner, false, RoleOwner},
		{"the broker owns an RLS table without FORCE", "CREATE TABLE t_owned (x int); ALTER TABLE t_owned ENABLE ROW LEVEL SECURITY; " +
			"ALTER TABLE t_owned OWNER TO rooms_broker", "DROP TABLE t_owned", broker, true, RoleBroker},
	} {
		t.Run(c.name, func(t *testing.T) {
			at := shared.super
			if c.db {
				at = super
			}
			exec(t, at, c.grant)
			t.Cleanup(func() { exec(t, at, c.revoke) })
			st, err := Open(t.Context(), c.dsn, c.role)
			if err == nil {
				st.Close()
			}
			want := string(c.role) + " bypasses row-level security"
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "the room log would be writable") {
				t.Fatalf("Open = %v, want %q", err, want)
			}
		})
	}
	// The owner owns every RLS table by design, without FORCE: that alone is not a refusal.
	st, err := Open(t.Context(), owner, RoleOwner)
	if err != nil {
		t.Fatalf("the owner, owning the schema: %v", err)
	}
	st.Close()
}

// A database Check cannot reach is ErrUnavailable, which the retention job retries. A wrong
// password (SQLSTATE class 28) or a missing database (3D) is a refusal: no retry fixes it.
func TestCheckUnavailableIsOnlyAConnectionFailure(t *testing.T) {
	_, broker, _, _ := testDB(t)
	missing, _ := url.Parse(broker)
	missing.Path = "/no_such_database"
	for _, c := range []struct {
		name, dsn   string
		unavailable bool
	}{
		{"a wrong password", as(broker, "rooms_broker", "wrong"), false}, // pragma: allowlist secret
		{"a missing database", missing.String(), false},
		{"nothing listening", "postgres://rooms_broker@127.0.0.1:1/rooms?connect_timeout=1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := Open(t.Context(), c.dsn, RoleBroker)
			if err == nil {
				st.Close()
				t.Fatal("Open succeeded")
			}
			if got := errors.Is(err, ErrUnavailable); got != c.unavailable {
				t.Fatalf("Open = %v: unavailable %v, want %v", err, got, c.unavailable)
			}
		})
	}
}

// A certificate the client cannot verify is a refusal too: retrying does not change the CA.
func TestUnavailableClassifiesTLS(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"unknown authority", fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}), false},
		{"wrong host", fmt.Errorf("tls: %w", x509.HostnameError{Host: "db"}), false},
		{"verification", &tls.CertificateVerificationError{Err: errors.New("expired")}, false},
		{"auth", &pgconn.PgError{Code: "28P01"}, false},
		{"starting up", &pgconn.PgError{Code: "57P03"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"syntax", &pgconn.PgError{Code: "42601"}, false},
		{"refused", errors.New("dial tcp: connection refused"), true},
	} {
		if got := unavailable(c.err); got != c.want {
			t.Errorf("%s: unavailable = %v, want %v", c.name, got, c.want)
		}
	}
}

// migrate checks that the roles its files GRANT to exist before any statement runs: a missing
// rooms_retention names docs/database.md, and the ledger is never created.
func TestMigrateRefusesMissingRoles(t *testing.T) {
	owner, super := emptyOwnedDBWithSuper(t)
	exec(t, shared.super, "ALTER ROLE rooms_retention RENAME TO rooms_retention_away")
	t.Cleanup(func() { exec(t, shared.super, "ALTER ROLE rooms_retention_away RENAME TO rooms_retention") })
	n, err := Migrate(t.Context(), owner, slog.New(slog.DiscardHandler))
	if err == nil || n != 0 || !strings.Contains(err.Error(), "create the login roles rooms_broker and rooms_retention first (docs/database.md)") {
		t.Fatalf("Migrate = %d, %v, want the missing-role refusal", n, err)
	}
	if got := query(t, super, "SELECT (to_regclass('public.room_broker_revisions') IS NULL AND to_regclass('public.events') IS NULL)::text"); got != "true" {
		t.Fatal("migrate ran a statement with a role missing")
	}
}

// The broker reads the ledger's version column, and nothing else in it: serve's schema check.
func TestMigrateGrantsTheBrokerTheLedgerVersion(t *testing.T) {
	owner, _, _, _ := testDB(t)
	got := query(t, owner, `SELECT has_column_privilege('rooms_broker', 'public.room_broker_revisions', 'version', 'SELECT')::text
		|| ',' || has_column_privilege('rooms_broker', 'public.room_broker_revisions', 'checksum', 'SELECT')::text
		|| ',' || has_table_privilege('rooms_broker', 'public.room_broker_revisions', 'SELECT,INSERT,UPDATE,DELETE')::text`)
	if got != "true,false,false" {
		t.Fatalf("rooms_broker on the ledger (version, checksum, table) = %s, want true,false,false", got)
	}
}

// serve refuses a schema behind its binary, naming migrate and the owner's Secret; a schema ahead
// keeps migrate's refusal; no ledger at all is "none"; a ledger the role cannot read is served,
// unverified.
func TestSchemaCurrent(t *testing.T) {
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	newest, prev := files[len(files)-1].Version, files[len(files)-2].Version
	for _, c := range []struct {
		name, forge string
		want        string
		unverified  bool
	}{
		{"current", "", "", false},
		{"one file behind", "DELETE FROM room_broker_revisions WHERE version = '" + newest + "'",
			"the schema is at " + prev + ", this broker needs " + newest + ": run migrate (broker.database.ownerSecret)", false},
		{"ahead", "INSERT INTO room_broker_revisions (version, checksum) VALUES ('29991231000000', 'x')",
			"the database has version 29991231000000, which this binary does not ship", false},
		{"no ledger", "DROP TABLE room_broker_revisions",
			"the schema is at none, this broker needs " + newest, false},
		{"unreadable", "REVOKE SELECT (version) ON room_broker_revisions FROM rooms_broker", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, broker, _, super := testDB(t)
			if c.forge != "" {
				exec(t, super, c.forge)
			}
			st, err := Open(t.Context(), broker, RoleBroker)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			err = st.SchemaCurrent(t.Context())
			switch {
			case c.unverified:
				if !errors.Is(err, ErrSchemaUnverified) {
					t.Fatalf("SchemaCurrent = %v, want ErrSchemaUnverified", err)
				}
			case c.want == "":
				if err != nil {
					t.Fatalf("SchemaCurrent = %v, want nil", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("SchemaCurrent = %v, want %q", err, c.want)
				}
			}
		})
	}
}

// The suite's server answers in plain text: Check reports it, and Open still serves.
func TestCheckReportsPlainConnection(t *testing.T) {
	_, broker, _, _ := testDB(t)
	st, err := Open(t.Context(), broker, RoleBroker)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Server.TLS || st.Server.User != "rooms_broker" {
		t.Fatalf("Server = %+v, want rooms_broker over a plain connection", st.Server)
	}
}

// The ledger is public.room_broker_revisions whatever the role's search_path, where createRevisions
// makes it and every read looks: a decoy of the same name earlier in the path is never written,
// and the second run applies nothing.
func TestTheLedgerIsResolvedOneWay(t *testing.T) {
	owner, super := emptyOwnedDBWithSuper(t)
	db := strings.TrimPrefix(mustURL(t, super).Path, "/")
	exec(t, super, "CREATE SCHEMA decoy AUTHORIZATION rooms_owner; CREATE TABLE decoy.room_broker_revisions (version text, checksum text); "+
		"ALTER TABLE decoy.room_broker_revisions OWNER TO rooms_owner; GRANT CREATE, USAGE ON SCHEMA public TO rooms_owner")
	exec(t, super, "ALTER ROLE rooms_owner IN DATABASE "+pgx.Identifier{db}.Sanitize()+" SET search_path = decoy, public")
	files := []migrations.File{{Name: "29990101000000_noop.sql", Version: "29990101000000", Body: "SELECT 1"}}
	log := slog.New(slog.DiscardHandler)
	for run, want := range []int{1, 0} {
		if n, err := migrate(t.Context(), owner, files, log); err != nil || n != want {
			t.Fatalf("run %d: migrate = %d, %v, want %d", run, n, err, want)
		}
	}
	if got := query(t, super, "SELECT (SELECT count(*) FROM public.room_broker_revisions)::text || ',' || (SELECT count(*) FROM decoy.room_broker_revisions)::text"); got != "1,0" {
		t.Fatalf("ledger rows public,decoy = %s, want 1,0", got)
	}
	conn, err := pgx.Connect(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if err := schemaCurrent(t.Context(), conn, files); err != nil {
		t.Fatalf("schemaCurrent under search_path decoy, public = %v", err)
	}
}

func mustURL(t *testing.T, dsn string) *url.URL {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
