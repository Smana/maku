// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func startFreshPostgres(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	c, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start fresh postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return dsn
}

func assertRolesWorkEndToEnd(t *testing.T, adminURL, dbName string, res BootstrapResult) {
	t.Helper()
	ownerDSN, err := BuildRoleDSN(adminURL, "", 0, dbName, RoleOwner, res.Passwords[RoleOwner], "disable", "")
	if err != nil {
		t.Fatalf("build owner DSN: %v", err)
	}
	brokerDSN, err := BuildRoleDSN(adminURL, "", 0, dbName, RoleBroker, res.Passwords[RoleBroker], "disable", "")
	if err != nil {
		t.Fatalf("build broker DSN: %v", err)
	}
	retentionDSN, err := BuildRoleDSN(adminURL, "", 0, dbName, RoleRetention, res.Passwords[RoleRetention], "disable", "")
	if err != nil {
		t.Fatalf("build retention DSN: %v", err)
	}

	if _, err := Migrate(t.Context(), ownerDSN, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrate as rooms_owner failed: %v", err)
	}
	for _, tc := range []struct {
		role Role
		dsn  string
	}{
		{RoleBroker, brokerDSN},
		{RoleRetention, retentionDSN},
	} {
		s, err := Open(t.Context(), tc.dsn, tc.role)
		if err != nil {
			t.Fatalf("Open(%s) failed: %v", tc.role, err)
		}
		if tc.role == RoleBroker {
			if err := s.SchemaCurrent(t.Context()); err != nil {
				s.Close()
				t.Fatalf("SchemaCurrent as %s failed: %v", tc.role, err)
			}
		}
		s.Close()
	}
}

func TestBootstrapSuperuser(t *testing.T) {
	adminURL := startFreshPostgres(t)

	res, err := Bootstrap(t.Context(), adminURL, BootstrapOptions{})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !res.DatabaseCreated {
		t.Fatalf("DatabaseCreated = false, want true")
	}
	if res.CloudAdminGrant {
		t.Fatalf("CloudAdminGrant = true for superuser, want false")
	}
	if res.Database != "rooms" {
		t.Fatalf("Database = %q, want rooms", res.Database)
	}
	if !slices.Equal(res.RolesCreated, fixedRoles) {
		t.Fatalf("RolesCreated = %v, want %v", res.RolesCreated, fixedRoles)
	}
	if len(res.RolesRotated) != 0 {
		t.Fatalf("RolesRotated = %v, want empty", res.RolesRotated)
	}
	for _, r := range fixedRoles {
		if len(res.Passwords[r]) != 32 {
			t.Fatalf("password length for %s = %d, want 32", r, len(res.Passwords[r]))
		}
	}

	assertRolesWorkEndToEnd(t, adminURL, "rooms", res)
}

// Simulates RDS / Cloud SQL / Azure Flexible Server where the admin user has CREATEROLE and
// CREATEDB but is NOT a superuser. On Postgres 16+, CREATE DATABASE ... OWNER rooms_owner requires
// GRANT rooms_owner TO CURRENT_USER first.
func TestBootstrapCloudAdmin(t *testing.T) {
	superURL := startFreshPostgres(t)
	if err := run(t.Context(), superURL, `CREATE ROLE cloud_admin LOGIN NOSUPERUSER CREATEROLE CREATEDB NOBYPASSRLS PASSWORD 'cloudadminpw'`); err != nil {
		t.Fatalf("create cloud_admin: %v", err)
	}
	cloudAdminURL := as(superURL, "cloud_admin", "cloudadminpw")

	res, err := Bootstrap(t.Context(), cloudAdminURL, BootstrapOptions{Database: "maku_rooms"})
	if err != nil {
		t.Fatalf("Bootstrap as cloud_admin: %v", err)
	}
	if !res.CloudAdminGrant {
		t.Fatalf("CloudAdminGrant = false for non-superuser admin, want true")
	}
	if !res.DatabaseCreated {
		t.Fatalf("DatabaseCreated = false, want true")
	}
	if res.Database != "maku_rooms" {
		t.Fatalf("Database = %q, want maku_rooms", res.Database)
	}

	assertRolesWorkEndToEnd(t, cloudAdminURL, "maku_rooms", res)
}

func TestBootstrapIdempotentAndRotate(t *testing.T) {
	adminURL := startFreshPostgres(t)

	first, err := Bootstrap(t.Context(), adminURL, BootstrapOptions{})
	if err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}

	// Second call without RotatePasswords must refuse to clobber existing credentials.
	if _, err := Bootstrap(t.Context(), adminURL, BootstrapOptions{}); !errors.Is(err, ErrRolesExist) {
		t.Fatalf("second Bootstrap without RotatePasswords = %v, want ErrRolesExist", err)
	}

	// With RotatePasswords, roles rotate cleanly and the existing database is preserved.
	second, err := Bootstrap(t.Context(), adminURL, BootstrapOptions{RotatePasswords: true})
	if err != nil {
		t.Fatalf("rotate Bootstrap: %v", err)
	}
	if second.DatabaseCreated {
		t.Fatalf("DatabaseCreated = true on rotate, want false")
	}
	if len(second.RolesCreated) != 0 {
		t.Fatalf("RolesCreated = %v on rotate, want empty", second.RolesCreated)
	}
	if !slices.Equal(second.RolesRotated, fixedRoles) {
		t.Fatalf("RolesRotated = %v, want %v", second.RolesRotated, fixedRoles)
	}
	for _, r := range fixedRoles {
		if second.Passwords[r] == first.Passwords[r] {
			t.Fatalf("password for %s did not change on rotation", r)
		}
	}

	// Old broker password must fail authentication; new passwords must work end-to-end.
	oldBrokerDSN, err := BuildRoleDSN(adminURL, "", 0, "rooms", RoleBroker, first.Passwords[RoleBroker], "disable", "")
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := pgx.Connect(t.Context(), oldBrokerDSN); err == nil {
		_ = conn.Close(t.Context())
		t.Fatalf("old password still authenticated after rotation")
	}

	assertRolesWorkEndToEnd(t, adminURL, "rooms", second)
}

func TestBootstrapValidationAndBuildRoleDSN(t *testing.T) {
	if _, err := Bootstrap(t.Context(), "", BootstrapOptions{}); err == nil || !strings.Contains(err.Error(), "admin DSN is required") {
		t.Fatalf("empty admin DSN: %v", err)
	}
	if _, err := Bootstrap(t.Context(), "postgres://postgres:pw@127.0.0.1:5432/postgres", BootstrapOptions{Database: "Bad-DB"}); err == nil || !strings.Contains(err.Error(), "database name") {
		t.Fatalf("invalid db name: %v", err)
	}

	dsn, err := BuildRoleDSN("postgres://admin:secret@db.internal:5433/postgres?sslmode=require", "", 0, "rooms", RoleBroker, "pw123", "verify-full", "/etc/room-broker/db-ca/ca.crt")
	if err != nil {
		t.Fatalf("BuildRoleDSN: %v", err)
	}
	want := "postgresql://rooms_broker:pw123@db.internal:5433/rooms?sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt"
	if dsn != want {
		t.Fatalf("BuildRoleDSN = %q, want %q", dsn, want)
	}

	override, err := BuildRoleDSN("postgres://admin:secret@127.0.0.1:5432/postgres", "rooms-db.maku.svc", 5432, "rooms", RoleOwner, "pw456", "require", "")
	if err != nil {
		t.Fatalf("BuildRoleDSN override: %v", err)
	}
	wantOverride := "postgresql://rooms_owner:pw456@rooms-db.maku.svc:5432/rooms?sslmode=require"
	if override != wantOverride {
		t.Fatalf("BuildRoleDSN override = %q, want %q", override, wantOverride)
	}
}
