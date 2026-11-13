// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

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
// superuser, or the broker or the retention job holding the owner's role are each refused.
// Roles are cluster-wide, so each case undoes itself; the suite's tests run one at a time.
func TestCheckRefusesRLSBypass(t *testing.T) {
	_, broker, retention, _ := testDB(t)
	for _, c := range []struct {
		name, grant, revoke, dsn string
		role                     Role
	}{
		{"BYPASSRLS", "ALTER ROLE rooms_broker BYPASSRLS", "ALTER ROLE rooms_broker NOBYPASSRLS", broker, RoleBroker},
		{"a member of rooms_owner", "GRANT rooms_owner TO rooms_broker", "REVOKE rooms_owner FROM rooms_broker", broker, RoleBroker},
		{"a superuser", "ALTER ROLE rooms_retention SUPERUSER", "ALTER ROLE rooms_retention NOSUPERUSER", retention, RoleRetention},
		{"retention in rooms_owner", "GRANT rooms_owner TO rooms_retention", "REVOKE rooms_owner FROM rooms_retention", retention, RoleRetention},
	} {
		t.Run(c.name, func(t *testing.T) {
			exec(t, shared.super, c.grant)
			t.Cleanup(func() { exec(t, shared.super, c.revoke) })
			st, err := Open(t.Context(), c.dsn, c.role)
			if err == nil {
				st.Close()
			}
			want := string(c.role) + " bypasses row-level security: the room log would be writable"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Open = %v, want %q", err, want)
			}
		})
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
