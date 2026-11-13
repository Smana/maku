// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/Smana/maku/internal/store/migrations"
)

// atlasCloudIdentifier is the row Atlas keeps in its ledger to name the target database: a
// bookkeeping row, never a migration.
const atlasCloudIdentifier = ".atlas_cloud_identifier"

// atlasLedgerSQL finds the Atlas operator's ledger through the catalog, which any role reads, and
// says whether the connected role may read it.
const atlasLedgerSQL = `SELECT count(*) > 0,
	coalesce(bool_and(has_schema_privilege(n.oid, 'USAGE') AND has_table_privilege(c.oid, 'SELECT')), false)
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = 'atlas_schema_revisions' AND c.relname = 'atlas_schema_revisions'`

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
	// An empty ledger of ours is no history: a first run that failed on its first file leaves one
	// behind, and must not hide the operator's ledger from the next run.
	ours, err := recorded(ctx, conn)
	if err != nil {
		return err
	}
	if len(ours) > 0 {
		return nil // our own ledger wins; the operator's, if any, is history
	}
	var present, readable bool
	if err := conn.QueryRow(ctx, atlasLedgerSQL).Scan(&present, &readable); err != nil {
		return fmt.Errorf("migrate: adopt: %w", err)
	}
	if !present {
		return nil
	}
	if !readable {
		return fmt.Errorf("migrate: the Atlas operator's ledger atlas_schema_revisions is not readable by %s: "+
			"grant it USAGE on the schema and SELECT on the table, then re-run; nothing was adopted", ownerRole)
	}
	revs, err := atlasRevisions(ctx, conn)
	if err != nil {
		return err
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

func atlasRevisions(ctx context.Context, conn *pgx.Conn) ([]atlasRevision, error) {
	rows, err := conn.Query(ctx, `SELECT version, applied, total, coalesce(error, ''), hash
		FROM atlas_schema_revisions.atlas_schema_revisions WHERE version <> $1 ORDER BY version`, atlasCloudIdentifier)
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
