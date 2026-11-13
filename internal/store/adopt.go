// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Smana/maku/internal/store/migrations"
)

// atlasCloudIdentifier is the row Atlas keeps in its ledger to name the target database: a
// bookkeeping row, never a migration.
const atlasCloudIdentifier = ".atlas_cloud_identifier"

// atlasLedgerSQL finds the Atlas operator's ledger through the catalog, which any role reads, in
// each schema Atlas may have put it: atlas_schema_revisions (its default), or the schema its URL
// named, public or the connection's current one. It says whether the connected role may read it.
const atlasLedgerSQL = `SELECT n.nspname, has_schema_privilege(n.oid, 'USAGE') AND has_table_privilege(c.oid, 'SELECT')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE c.relname = 'atlas_schema_revisions' AND c.relkind IN ('r', 'p')
	AND n.nspname IN ('atlas_schema_revisions', 'public', current_schema())
	ORDER BY n.nspname`

// atlasLedger is the operator's ledger: table is its quoted, qualified name, "" when there is none.
type atlasLedger struct {
	table    string
	readable bool
}

// findAtlasLedger locates the operator's ledger. Two of them is ambiguous, and refused as Atlas
// refuses it: which one recorded the database's history cannot be told.
func findAtlasLedger(ctx context.Context, q querier) (atlasLedger, error) {
	rows, err := q.Query(ctx, atlasLedgerSQL)
	if err != nil {
		return atlasLedger{}, fmt.Errorf("find the Atlas operator's ledger: %w", err)
	}
	defer rows.Close()
	var (
		found    []string
		readable bool
	)
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema, &readable); err != nil {
			return atlasLedger{}, fmt.Errorf("find the Atlas operator's ledger: %w", err)
		}
		found = append(found, schema)
	}
	if err := rows.Err(); err != nil {
		return atlasLedger{}, fmt.Errorf("find the Atlas operator's ledger: %w", err)
	}
	switch len(found) {
	case 0:
		return atlasLedger{}, nil
	case 1:
		return atlasLedger{table: pgx.Identifier{found[0], "atlas_schema_revisions"}.Sanitize(), readable: readable}, nil
	}
	return atlasLedger{}, fmt.Errorf("the Atlas operator's ledger is ambiguous: atlas_schema_revisions exists in schemas %s; "+
		"keep the one Atlas writes and drop the others", strings.Join(found, ", "))
}

type atlasRevision struct {
	version, hash, err string
	applied, total     int64
}

// adopt takes over a database the Atlas operator migrated (cnref's SQLInstance) when it holds the
// operator's ledger and not room_broker_revisions yet. Its complete revisions must be exactly a
// prefix of the shipped files, each with the file's atlas.sum hash, and none may be partial or
// failed. It then records that prefix in room_broker_revisions with the shipped checksums, in one
// transaction, so migrate applies only what is pending and nothing twice (D25). Anything else is
// refused, naming the version, with nothing written. The Atlas ledger is left as it is.
func adopt(ctx context.Context, conn *pgx.Conn, files []migrations.File, log *slog.Logger) error {
	l, err := findAtlasLedger(ctx, conn)
	if err != nil {
		return fmt.Errorf("migrate: %w; nothing was adopted", err)
	}
	if l.table == "" {
		return nil
	}
	if !l.readable {
		return fmt.Errorf("migrate: the Atlas operator's ledger %s is not readable by %s: "+
			"grant it USAGE on the schema and SELECT on the table, then re-run; nothing was adopted", l.table, ownerRole)
	}
	revs, err := atlasRevisions(ctx, conn, l.table)
	if err != nil {
		return err
	}
	// An empty ledger of ours is no history: a first run that failed on its first file leaves one
	// behind, and must not hide the operator's ledger from the next run.
	ours, err := recorded(ctx, conn)
	if err != nil {
		return err
	}
	if len(ours) > 0 {
		// Our ledger wins, unless the operator went on migrating past it: those files ran outside
		// migrate's record, and applying them again would fail halfway or apply them twice.
		return notAhead(revs, ours)
	}
	if err := adoptable(revs, files); err != nil {
		return err
	}
	if len(revs) == 0 {
		return nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate: adopt: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, stmt := range []string{createRevisions, grantRevisions} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: adopt: %w", err)
		}
	}
	for _, f := range files[:len(revs)] {
		if _, err := tx.Exec(ctx, recordSQL, f.Version, checksum(f.Body)); err != nil {
			return fmt.Errorf("migrate: adopt: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: adopt: %w", err)
	}
	log.Info(fmt.Sprintf("adopted %d revisions from the Atlas operator", len(revs)), "revisions", len(revs))
	return nil
}

func atlasRevisions(ctx context.Context, conn *pgx.Conn, table string) ([]atlasRevision, error) {
	rows, err := conn.Query(ctx, `SELECT version, applied, total, coalesce(error, ''), hash
		FROM `+table+` WHERE version <> $1 ORDER BY version`, atlasCloudIdentifier)
	if err != nil {
		return nil, fmt.Errorf("migrate: adopt: read the Atlas operator's ledger: %w", err)
	}
	defer rows.Close()
	var revs []atlasRevision
	for rows.Next() {
		var r atlasRevision
		if err := rows.Scan(&r.version, &r.applied, &r.total, &r.err, &r.hash); err != nil {
			return nil, fmt.Errorf("migrate: adopt: %w", err)
		}
		revs = append(revs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: adopt: %w", err)
	}
	return revs, nil
}

// notAhead refuses an operator's ledger holding a revision, finished or not, past the newest one
// migrate recorded.
func notAhead(revs []atlasRevision, ours map[string]string) error {
	newest := ""
	for v := range ours {
		newest = max(newest, v)
	}
	for _, r := range revs {
		if r.version > newest {
			return fmt.Errorf("migrate: the Atlas operator applied version %s after migrate's last revision %s: "+
				"the two ledgers disagree, so nothing was applied; stop the operator (drop atlasSchema from the claim) "+
				"and reconcile room_broker_revisions by hand", r.version, newest)
		}
	}
	return nil
}

// adoptable accepts the operator's revisions only when they are, in order, the first len(revs)
// shipped files, each complete and with its file's hash.
func adoptable(revs []atlasRevision, files []migrations.File) error {
	shipped := map[string]bool{}
	for _, f := range files {
		shipped[f.Version] = true
	}
	for i, r := range revs {
		switch {
		case r.applied != r.total || r.err != "":
			return fmt.Errorf("migrate: the Atlas operator's revision %s is partial or failed (%d of %d statements applied): "+
				"repair it with Atlas first; nothing was adopted", r.version, r.applied, r.total)
		case !shipped[r.version]:
			return fmt.Errorf("migrate: the Atlas operator applied version %s, which this binary does not ship: "+
				"it is newer than the code; nothing was adopted", r.version)
		case files[i].Version != r.version:
			return fmt.Errorf("migrate: the Atlas operator's ledger skips version %s: its revisions must be the first "+
				"shipped files, in order; nothing was adopted", files[i].Version)
		case files[i].AtlasHash != r.hash:
			return fmt.Errorf("migrate: the Atlas operator's revision %s has hash %s, the shipped file %s: "+
				"the database ran another file; nothing was adopted", r.version, r.hash, files[i].AtlasHash)
		}
	}
	return nil
}
