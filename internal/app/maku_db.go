// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/store"
)

const (
	defaultAdminDSNEnv = "MAKU_DB_ADMIN_URL"
	dbCACertMountPath  = "/etc/room-broker/db-ca/ca.crt"
)

var (
	dbDNSLabelRE     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dbDNSSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// db runs `maku db <subcommand>`. Currently ships `bootstrap` (v5 DB5).
func (r Maku) db(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return errors.New("db: expected subcommand `bootstrap`: maku db bootstrap --out FILE [--namespace NS] [--admin-dsn-env MAKU_DB_ADMIN_URL] [--host HOST] [--port PORT] [--database rooms] [--sslmode verify-full] [--ca-secret NAME] [--rotate-passwords] [--force]")
	}
	return r.dbBootstrap(ctx, args[1:])
}

// dbBootstrap creates Maku's three fixed PostgreSQL roles and database against an external
// Postgres 16+ server using an admin DSN read from the environment (never argv), verifies the
// T12 role invariants, and writes Kubernetes Secret manifests for the three DSNs to --out (0600).
func (r Maku) dbBootstrap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("db bootstrap", flag.ContinueOnError)
	outPath := fs.String("out", "", "path to write the three DSN Secret manifests (mode 0600), or - for stdout")
	ns := fs.String("namespace", "maku", "namespace for the generated Secret manifests")
	adminEnv := fs.String("admin-dsn-env", defaultAdminDSNEnv, "environment variable holding the PostgreSQL admin DSN (never pass the DSN on argv)")
	host := fs.String("host", "", "database host for the rendered DSNs (default: the admin DSN's host)")
	port := fs.Uint("port", 0, "database port for the rendered DSNs (default: the admin DSN's port, or 5432)")
	database := fs.String("database", "rooms", "database name to create or verify")
	sslmode := fs.String("sslmode", "verify-full", "sslmode written into the rendered DSNs (disable, require, verify-ca, verify-full)")
	caSecret := fs.String("ca-secret", "", "CA Secret name (broker.database.caSecret): when set, adds sslrootcert=/etc/room-broker/db-ca/ca.crt")
	brokerSecret := fs.String("broker-secret", "rooms-broker-dsn", "Secret name for rooms_broker")
	ownerSecret := fs.String("owner-secret", "rooms-owner-dsn", "Secret name for rooms_owner")
	retentionSecret := fs.String("retention-secret", "rooms-retention-dsn", "Secret name for rooms_retention")
	secretKey := fs.String("key", "uri", "key name inside each Secret")
	rotate := fs.Bool("rotate-passwords", false, "rotate passwords if Maku's roles already exist")
	force := fs.Bool("force", false, "overwrite --out if it already exists")

	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("db bootstrap: unexpected positional argument(s) %q", strings.Join(pos, " "))
	}
	if *outPath == "" {
		return errors.New("db bootstrap: --out FILE (or --out -) is required")
	}
	if !dbDNSLabelRE.MatchString(*ns) || len(*ns) > 63 {
		return fmt.Errorf("db bootstrap: --namespace %q is not a DNS label", *ns)
	}
	for name, val := range map[string]string{
		"--broker-secret":    *brokerSecret,
		"--owner-secret":     *ownerSecret,
		"--retention-secret": *retentionSecret,
	} {
		if !dbDNSSubdomainRE.MatchString(val) || len(val) > 253 {
			return fmt.Errorf("db bootstrap: %s %q is not a valid Secret name", name, val)
		}
	}
	if strings.TrimSpace(*secretKey) == "" {
		return errors.New("db bootstrap: --key must not be empty")
	}
	if *port > 65535 {
		return fmt.Errorf("db bootstrap: --port %d is out of range (1-65535)", *port)
	}
	switch *sslmode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("db bootstrap: --sslmode %q is not one of disable, require, verify-ca, verify-full", *sslmode)
	}
	if *outPath != "-" && !*force {
		if _, err := os.Stat(*outPath); err == nil {
			return fmt.Errorf("db bootstrap: %s already exists: move it away or pass --force", *outPath)
		}
	}
	if strings.TrimSpace(*adminEnv) == "" {
		return errors.New("db bootstrap: --admin-dsn-env must not be empty")
	}
	adminDSN := strings.TrimSpace(os.Getenv(*adminEnv))
	if adminDSN == "" {
		return fmt.Errorf("db bootstrap: environment variable %s is empty: set it to a PostgreSQL admin DSN (never pass credentials as CLI flags)", *adminEnv)
	}

	fn := r.DBBootstrap
	if fn == nil {
		fn = store.Bootstrap
	}
	res, err := fn(ctx, adminDSN, store.BootstrapOptions{
		Database:        *database,
		RotatePasswords: *rotate,
		Rand:            r.Rand,
	})
	if err != nil {
		return fmt.Errorf("db bootstrap: %w", err)
	}

	sslRootCert := ""
	if *caSecret != "" {
		sslRootCert = dbCACertMountPath
	}
	type roleEntry struct {
		role   store.Role
		secret string
	}
	entries := []roleEntry{
		{store.RoleOwner, *ownerSecret},
		{store.RoleBroker, *brokerSecret},
		{store.RoleRetention, *retentionSecret},
	}
	var doc bytes.Buffer
	fmt.Fprintf(&doc, "# Generated by maku db bootstrap for database %s (PostgreSQL %s).\n", res.Database, res.ServerVersion)
	doc.WriteString("# Keep this file out of git; apply it with kubectl apply -f or import into your secret store.\n")
	for i, e := range entries {
		dsn, err := store.BuildRoleDSN(adminDSN, *host, uint16(*port), res.Database, e.role, res.Passwords[e.role], *sslmode, sslRootCert)
		if err != nil {
			return fmt.Errorf("db bootstrap: render DSN for %s: %w", e.role, err)
		}
		if i > 0 {
			doc.WriteString("---\n")
		}
		fmt.Fprintf(&doc, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\nstringData:\n  %s: %s\n",
			e.secret, *ns, *secretKey, strconv.Quote(dsn))
	}

	if *outPath == "-" {
		if _, err := r.Out.Write(doc.Bytes()); err != nil {
			return err
		}
		summaryOut := r.Err
		if summaryOut == nil {
			summaryOut = os.Stderr
		}
		writeBootstrapSummary(summaryOut, res, "stdout", *ns, *caSecret)
		return nil
	}
	if err := writeSecretFile(*outPath, doc.Bytes()); err != nil {
		return fmt.Errorf("db bootstrap: write %s: %w", *outPath, err)
	}
	writeBootstrapSummary(r.Out, res, *outPath, *ns, *caSecret)
	return nil
}

func writeBootstrapSummary(w interface{ Write([]byte) (int, error) }, res store.BootstrapResult, target, ns, caSecret string) {
	action := "created"
	if len(res.RolesRotated) > 0 && len(res.RolesCreated) == 0 {
		action = "rotated"
	}
	dbState := "verified existing"
	if res.DatabaseCreated {
		dbState = "created"
	}
	_, _ = fmt.Fprintf(w, "bootstrapped PostgreSQL %s: %s database %s (owner rooms_owner), %s roles rooms_owner, rooms_broker, rooms_retention (T12 invariants verified)\n",
		res.ServerVersion, dbState, res.Database, action)
	if res.CloudAdminGrant {
		_, _ = fmt.Fprintln(w, "granted rooms_owner to CURRENT_USER for non-superuser managed-service admin")
	}
	if target == "stdout" {
		_, _ = fmt.Fprintf(w, "wrote three DSN Secret manifests for namespace %s to stdout\n", ns)
	} else {
		_, _ = fmt.Fprintf(w, "wrote three DSN Secret manifests (mode 0600) to %s; apply with: kubectl apply -n %s -f %s\n", target, ns, target)
	}
	if caSecret != "" {
		_, _ = fmt.Fprintf(w, "DSNs reference %s: ensure Secret %s (key ca.crt) exists in namespace %s\n", dbCACertMountPath, caSecret, ns)
	}
}

// writeSecretFile writes data atomically at path with mode 0600, creating parent directories if needed.
func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".maku-db-bootstrap-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
