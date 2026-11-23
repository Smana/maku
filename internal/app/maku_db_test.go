// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/store"
)

func fakeBootstrap(t *testing.T, wantDB string, wantRotate bool) func(context.Context, string, store.BootstrapOptions) (store.BootstrapResult, error) {
	t.Helper()
	return func(_ context.Context, _ string, opts store.BootstrapOptions) (store.BootstrapResult, error) {
		if opts.Database != wantDB {
			t.Errorf("opts.Database = %q, want %q", opts.Database, wantDB)
		}
		if opts.RotatePasswords != wantRotate {
			t.Errorf("opts.RotatePasswords = %v, want %v", opts.RotatePasswords, wantRotate)
		}
		return store.BootstrapResult{
			ServerVersion:   "16.4",
			Database:        opts.Database,
			DatabaseCreated: !opts.RotatePasswords,
			CloudAdminGrant: true,
			RolesCreated:    []store.Role{store.RoleOwner, store.RoleBroker, store.RoleRetention},
			Passwords: map[store.Role]string{
				store.RoleOwner:     "OwnerPass1234567890",
				store.RoleBroker:    "BrokerPass1234567890",
				store.RoleRetention: "RetentionPass1234567890",
			},
		}, nil
	}
}

func TestDBBootstrapWrites0600SecretsWithoutPrintingPasswords(t *testing.T) {
	t.Setenv("MAKU_DB_ADMIN_URL", "postgresql://admin:adminpw@db.example.internal:5432/postgres?sslmode=require")
	outFile := filepath.Join(t.TempDir(), "secrets", "database.yaml")
	var stdout, stderr bytes.Buffer
	m := Maku{
		Dir:         t.TempDir(),
		Out:         &stdout,
		Err:         &stderr,
		DBBootstrap: fakeBootstrap(t, "rooms", false),
	}

	err := m.Run(context.Background(), []string{
		"db", "bootstrap",
		"--out", outFile,
		"--namespace", "maku-prod",
		"--ca-secret", "rooms-db-ca",
	})
	if err != nil {
		t.Fatalf("db bootstrap: %v", err)
	}

	info, err := os.Stat(outFile)
	if err != nil {
		t.Fatalf("stat %s: %v", outFile, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 0600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"name: rooms-owner-dsn",
		"name: rooms-broker-dsn",
		"name: rooms-retention-dsn",
		"namespace: maku-prod",
		`uri: "postgresql://rooms_owner:OwnerPass1234567890@db.example.internal:5432/rooms?sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt"`,
		`uri: "postgresql://rooms_broker:BrokerPass1234567890@db.example.internal:5432/rooms?sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt"`,
		`uri: "postgresql://rooms_retention:RetentionPass1234567890@db.example.internal:5432/rooms?sslmode=verify-full&sslrootcert=/etc/room-broker/db-ca/ca.crt"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %q:\n%s", want, body)
		}
	}

	// Summary on stdout must never echo generated passwords or the admin password.
	summary := stdout.String()
	for _, secret := range []string{"OwnerPass1234567890", "BrokerPass1234567890", "RetentionPass1234567890", "adminpw"} {
		if strings.Contains(summary, secret) {
			t.Errorf("summary leaked secret %q:\n%s", secret, summary)
		}
	}
	if !strings.Contains(summary, "T12 invariants verified") || !strings.Contains(summary, "rooms-db-ca") {
		t.Errorf("unexpected summary:\n%s", summary)
	}

	// Refuses overwriting existing file without --force.
	if err := m.Run(context.Background(), []string{"db", "bootstrap", "--out", outFile}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite without --force = %v, want already exists error", err)
	}

	// Succeeds with --force and --rotate-passwords.
	m.DBBootstrap = fakeBootstrap(t, "rooms", true)
	if err := m.Run(context.Background(), []string{"db", "bootstrap", "--out", outFile, "--rotate-passwords", "--force"}); err != nil {
		t.Fatalf("db bootstrap --force --rotate-passwords: %v", err)
	}
}

func TestDBBootstrapStdoutAndCustomEnv(t *testing.T) {
	t.Setenv("CUSTOM_PG_ADMIN", "postgresql://admin:pw@127.0.0.1:5432/postgres")
	var stdout, stderr bytes.Buffer
	m := Maku{
		Dir:         t.TempDir(),
		Out:         &stdout,
		Err:         &stderr,
		DBBootstrap: fakeBootstrap(t, "custom_rooms", false),
	}

	err := m.Run(context.Background(), []string{
		"db", "bootstrap",
		"--out", "-",
		"--admin-dsn-env", "CUSTOM_PG_ADMIN",
		"--host", "pg.internal",
		"--port", "5433",
		"--database", "custom_rooms",
		"--sslmode", "require",
	})
	if err != nil {
		t.Fatalf("db bootstrap --out -: %v", err)
	}
	if !strings.Contains(stdout.String(), "postgresql://rooms_broker:BrokerPass1234567890@pg.internal:5433/custom_rooms?sslmode=require") {
		t.Errorf("stdout missing expected DSN:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "to stdout") {
		t.Errorf("stderr missing summary:\n%s", stderr.String())
	}
}

func TestDBBootstrapRefusesMissingEnvAndUnknownSubcommand(t *testing.T) {
	t.Setenv("MAKU_DB_ADMIN_URL", "")
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}

	if err := m.Run(context.Background(), []string{"db"}); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("bare `maku db`: %v", err)
	}
	if err := m.Run(context.Background(), []string{"db", "bootstrap", "--out", "-"}); err == nil || !strings.Contains(err.Error(), "MAKU_DB_ADMIN_URL is empty") {
		t.Fatalf("missing env: %v", err)
	}
	if err := m.Run(context.Background(), []string{"db", "bootstrap"}); err == nil || !strings.Contains(err.Error(), "--out FILE") {
		t.Fatalf("missing --out: %v", err)
	}
}
