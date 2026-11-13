// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Smana/maku/internal/store/migrations"
)

// atlasFixture is the Atlas operator's ledger over v0.45.0's fourteen files, as cnref holds it.
//
//go:embed testdata/atlas_revisions.sql
var atlasFixture string

// atlasFiles is how many files the fixture's ledger records.
const atlasFiles = 14

// atlasMigrated builds what cnref's SQLInstance leaves: the first fourteen files applied by the
// operator as rooms_owner, its ledger beside them, and no room_broker_revisions. It returns the
// owner's and the superuser's DSNs.
func atlasMigrated(t *testing.T) (owner, super string) {
	t.Helper()
	owner, super = emptyOwnedDBWithSuper(t)
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	for _, f := range files[:atlasFiles] {
		stmts := []string{f.Body}
		if f.NoTx {
			stmts = splitTxModeNoneStatements(f.Body)
		}
		for _, s := range stmts {
			if _, err := conn.Exec(t.Context(), s); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
	}
	if _, err := conn.Exec(t.Context(), atlasFixture); err != nil {
		t.Fatalf("the Atlas fixture: %v", err)
	}
	return owner, super
}

func logTo(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

// A database the Atlas operator migrated is adopted: its fourteen revisions are recorded with the
// shipped checksums and applied 0 times again; one more shipped file is applied alone, and then
// the rest. The operator's ledger is left as it was.
func TestAdoptAtlasLedger(t *testing.T) {
	ctx := t.Context()
	owner, super := atlasMigrated(t)
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	before := query(t, super, "SELECT md5(string_agg(t::text, ',' ORDER BY version)) FROM atlas_schema_revisions.atlas_schema_revisions t")
	var buf bytes.Buffer
	if n, err := migrate(ctx, owner, files[:atlasFiles], logTo(&buf)); err != nil || n != 0 {
		t.Fatalf("migrate over the operator's fourteen files = %d, %v, want 0 applied", n, err)
	}
	if !strings.Contains(buf.String(), "adopted 14 revisions from the Atlas operator") {
		t.Fatalf("the adoption is not logged:\n%s", buf.String())
	}
	for _, f := range files[:atlasFiles] {
		if got := query(t, owner, "SELECT checksum FROM room_broker_revisions WHERE version = $1", f.Version); got != checksum(f.Body) {
			t.Fatalf("%s: adopted checksum %q, want the shipped %q", f.Name, got, checksum(f.Body))
		}
	}
	if n, err := migrate(ctx, owner, files[:atlasFiles+1], logTo(&buf)); err != nil || n != 1 {
		t.Fatalf("one more shipped file: applied %d, %v, want exactly 1", n, err)
	}
	if n, err := Migrate(ctx, owner, logTo(&buf)); err != nil || n != len(files)-atlasFiles-1 {
		t.Fatalf("the rest: applied %d, %v, want %d", n, err, len(files)-atlasFiles-1)
	}
	if got := strings.Count(buf.String(), "adopted"); got != 1 {
		t.Fatalf("adopted %d times, want once", got)
	}
	if after := query(t, super, "SELECT md5(string_agg(t::text, ',' ORDER BY version)) FROM atlas_schema_revisions.atlas_schema_revisions t"); after != before {
		t.Fatal("migrate wrote the operator's ledger")
	}
}

// A ledger that is not exactly a prefix of the shipped files, complete and with their hashes, is
// refused by version with nothing written: room_broker_revisions does not exist afterwards.
func TestAdoptRefuses(t *testing.T) {
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	second, unknown := files[1].Version, "20261231000000"
	for _, c := range []struct{ name, forge, want string }{
		{"a gap", "DELETE FROM atlas_schema_revisions.atlas_schema_revisions WHERE version = '" + second + "'",
			"skips version " + second},
		{"an unknown version", "INSERT INTO atlas_schema_revisions.atlas_schema_revisions SELECT '" + unknown +
			"', description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version" +
			" FROM atlas_schema_revisions.atlas_schema_revisions WHERE version = '" + second + "'",
			"applied version " + unknown + ", which this binary does not ship"},
		{"a partial revision", "UPDATE atlas_schema_revisions.atlas_schema_revisions SET applied = total - 1 WHERE version = '" + second + "'",
			"revision " + second + " is partial or failed"},
		{"a failed revision", "UPDATE atlas_schema_revisions.atlas_schema_revisions SET error = 'boom' WHERE version = '" + second + "'",
			"revision " + second + " is partial or failed"},
		{"another file's hash", "UPDATE atlas_schema_revisions.atlas_schema_revisions SET hash = 'x' WHERE version = '" + second + "'",
			"revision " + second + " has hash x"},
		{"a ledger rooms_owner cannot read", "ALTER SCHEMA atlas_schema_revisions OWNER TO postgres; " +
			"ALTER TABLE atlas_schema_revisions.atlas_schema_revisions OWNER TO postgres",
			"is not readable by rooms_owner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			owner, super := atlasMigrated(t)
			exec(t, super, c.forge)
			n, err := Migrate(t.Context(), owner, slog.New(slog.DiscardHandler))
			if err == nil || n != 0 || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing was adopted") {
				t.Fatalf("Migrate = %d, %v, want a refusal naming %q", n, err, c.want)
			}
			if got := query(t, super, "SELECT (to_regclass('public.room_broker_revisions') IS NULL)::text"); got != "true" {
				t.Fatal("a refused adoption left room_broker_revisions behind")
			}
		})
	}
}

// An empty ledger of ours, left by a first run that failed on its first file, does not hide the
// operator's: the next run still adopts.
func TestAdoptBesideAnEmptyLedgerOfOurs(t *testing.T) {
	owner, _ := atlasMigrated(t)
	exec(t, owner, createRevisions)
	var buf bytes.Buffer
	if n, err := Migrate(t.Context(), owner, logTo(&buf)); err != nil || n != migrationCount(t)-atlasFiles {
		t.Fatalf("Migrate = %d, %v, want the %d files past the operator's", n, err, migrationCount(t)-atlasFiles)
	}
	if !strings.Contains(buf.String(), "adopted 14 revisions") {
		t.Fatalf("not adopted:\n%s", buf.String())
	}
}

// Our own ledger wins: with both present, the operator's is never adopted from, whatever its
// rows below ours hold (TestAdoptRefusesAnAtlasLedgerAheadOfOurs covers a row past them).
func TestAdoptIgnoresAtlasBesideOurLedger(t *testing.T) {
	owner, _, _, _ := testDB(t)
	exec(t, owner, atlasFixture)
	exec(t, owner, "UPDATE atlas_schema_revisions.atlas_schema_revisions SET error = 'boom'")
	var buf bytes.Buffer
	if n, err := Migrate(t.Context(), owner, logTo(&buf)); err != nil || n != 0 {
		t.Fatalf("Migrate = %d, %v, want 0 applied and the Atlas ledger ignored", n, err)
	}
	if strings.Contains(buf.String(), "adopted") {
		t.Fatalf("adopted beside our own ledger:\n%s", buf.String())
	}
}

// serve on a database only the operator migrated reads its ledger: current passes, behind names
// migrate; a ledger rooms_broker cannot read (cnref's, unless granted) is served unverified.
func TestSchemaCurrentReadsTheAtlasLedger(t *testing.T) {
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		grant bool
		files int
		want  error
	}{
		{"current", true, atlasFiles, nil},
		{"behind", true, atlasFiles + 1, errors.New("the schema is at " + files[atlasFiles-1].Version + ", this broker needs " + files[atlasFiles].Version)},
		{"unreadable", false, atlasFiles, ErrSchemaUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			owner, super := atlasMigrated(t)
			if c.grant {
				exec(t, super, "GRANT USAGE ON SCHEMA atlas_schema_revisions TO rooms_broker; "+
					"GRANT SELECT ON atlas_schema_revisions.atlas_schema_revisions TO rooms_broker")
			}
			st, err := Open(t.Context(), as(owner, "rooms_broker", "broker"), RoleBroker)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			err = schemaCurrent(t.Context(), st.pool, files[:c.files])
			switch {
			case c.want == nil && err != nil, c.want != nil && err == nil:
				t.Fatalf("schemaCurrent = %v, want %v", err, c.want)
			case errors.Is(c.want, ErrSchemaUnverified) && !errors.Is(err, ErrSchemaUnverified):
				t.Fatalf("schemaCurrent = %v, want ErrSchemaUnverified", err)
			case c.want != nil && !errors.Is(c.want, ErrSchemaUnverified) && !strings.Contains(err.Error(), c.want.Error()):
				t.Fatalf("schemaCurrent = %v, want %q", err, c.want)
			}
		})
	}
}

// Atlas puts its ledger in atlas_schema_revisions, or in the schema its URL names (public, or the
// connection's current schema): migrate finds it in any of them, and refuses to choose between two.
func TestAdoptFindsTheLedgerWhereAtlasPutsIt(t *testing.T) {
	const move = "ALTER TABLE atlas_schema_revisions.atlas_schema_revisions SET SCHEMA public; DROP SCHEMA atlas_schema_revisions"
	t.Run("public", func(t *testing.T) {
		owner, super := atlasMigrated(t)
		exec(t, super, move)
		var buf bytes.Buffer
		if n, err := migrate(t.Context(), owner, mustFiles(t)[:atlasFiles], logTo(&buf)); err != nil || n != 0 ||
			!strings.Contains(buf.String(), "adopted 14 revisions") {
			t.Fatalf("migrate = %d, %v, want the public ledger adopted:\n%s", n, err, buf.String())
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		owner, super := atlasMigrated(t)
		exec(t, super, "CREATE TABLE public.atlas_schema_revisions (LIKE atlas_schema_revisions.atlas_schema_revisions INCLUDING ALL); "+
			"ALTER TABLE public.atlas_schema_revisions OWNER TO rooms_owner")
		n, err := Migrate(t.Context(), owner, slog.New(slog.DiscardHandler))
		if err == nil || n != 0 || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("Migrate = %d, %v, want the ambiguous-ledger refusal", n, err)
		}
		if got := query(t, super, "SELECT (to_regclass('public.room_broker_revisions') IS NULL)::text"); got != "true" {
			t.Fatal("an ambiguous ledger left room_broker_revisions behind")
		}
	})
	t.Run("current schema", func(t *testing.T) {
		owner, super := atlasMigrated(t)
		exec(t, super, "CREATE SCHEMA app AUTHORIZATION rooms_owner; "+
			"ALTER TABLE atlas_schema_revisions.atlas_schema_revisions SET SCHEMA app; DROP SCHEMA atlas_schema_revisions")
		conn, err := pgx.Connect(t.Context(), owner)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
		if _, err := conn.Exec(t.Context(), "SET search_path = app, public"); err != nil {
			t.Fatal(err)
		}
		l, err := findAtlasLedger(t.Context(), conn)
		if err != nil || l.table != `"app"."atlas_schema_revisions"` || !l.readable {
			t.Fatalf("findAtlasLedger = %+v, %v, want app's, readable", l, err)
		}
	})
}

// A database our ledger already covers, which the Atlas operator then migrated further, is
// refused rather than re-applied: the operator ran files migrate has not recorded.
func TestAdoptRefusesAnAtlasLedgerAheadOfOurs(t *testing.T) {
	owner, super := atlasMigrated(t)
	files := mustFiles(t)
	if n, err := migrate(t.Context(), owner, files[:atlasFiles], slog.New(slog.DiscardHandler)); err != nil || n != 0 {
		t.Fatalf("adopt: %d, %v", n, err)
	}
	next := files[atlasFiles]
	exec(t, super, "INSERT INTO atlas_schema_revisions.atlas_schema_revisions SELECT $1, description, type, applied, total, "+
		"executed_at, execution_time, error, error_stmt, $2, partial_hashes, operator_version "+
		"FROM atlas_schema_revisions.atlas_schema_revisions WHERE version = $3", next.Version, next.AtlasHash, files[0].Version)
	n, err := Migrate(t.Context(), owner, slog.New(slog.DiscardHandler))
	if err == nil || n != 0 || !strings.Contains(err.Error(), "the Atlas operator applied version "+next.Version+" after") {
		t.Fatalf("Migrate = %d, %v, want the ahead refusal naming %s", n, err, next.Version)
	}
	if got := query(t, super, "SELECT count(*)::text FROM room_broker_revisions"); got != "14" {
		t.Fatalf("our ledger holds %s revisions after the refusal, want 14", got)
	}
}

func mustFiles(t *testing.T) []migrations.File {
	t.Helper()
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	return files
}
