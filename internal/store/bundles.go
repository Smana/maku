// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Smana/maku/internal/envelope"
)

// MaxBundle is a push bundle's cap, the design's 8 MiB (hardening.MaxBundle).
const MaxBundle = 8 << 20

// BundleRetention is how long a bundle is kept once stored, unless a pending approval binds it.
const BundleRetention = 14 * 24 * time.Hour

var (
	// ErrDigestMismatch is a bundle whose bytes do not hash to the digest it was stored under.
	ErrDigestMismatch = errors.New("the bundle's sha256 is not its digest")
	// ErrBundleTooLarge is an empty bundle or one over MaxBundle.
	ErrBundleTooLarge = errors.New("a bundle is 1 byte to 8 MiB")
	// ErrNoBundle is a bundle the store does not hold for that room, run and digest.
	ErrNoBundle = errors.New("no such bundle")
)

// PutBundle stores b, a run's push bundle, under (run, digest) in room (HR22). digest must be b's
// hex sha256. A second put of the same run and digest is a no-op. A sealed room takes none.
func (s *Store) PutBundle(ctx context.Context, room, run, digest string, b []byte) error {
	if len(b) == 0 || len(b) > MaxBundle {
		return fmt.Errorf("store: put bundle: %w", ErrBundleTooLarge)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("store: put bundle: %w", ErrDigestMismatch)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: put bundle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The room's row lock orders the put against a seal, as an append is.
	switch sealed, _, err := lockRoom(ctx, tx, room); {
	case err != nil:
		return fmt.Errorf("store: put bundle in room %s: %w", room, err)
	case sealed:
		return fmt.Errorf("store: put bundle in room %s: %w", room, ErrSealed)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO output_bundles (room_id, run_id, digest, size, bundle)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (run_id, digest) DO NOTHING`, room, run, digest, len(b), b); err != nil {
		return fmt.Errorf("store: put bundle in room %s: %w", room, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: put bundle in room %s: %w", room, err)
	}
	return nil
}

// Bundle returns the bundle run stored in room under digest, or ErrNoBundle.
func (s *Store) Bundle(ctx context.Context, room, run, digest string) ([]byte, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT bundle FROM output_bundles WHERE room_id = $1 AND run_id = $2 AND digest = $3`,
		room, run, digest).Scan(&b)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("store: read bundle: %w", ErrNoBundle)
	case err != nil:
		return nil, fmt.Errorf("store: read bundle: %w", err)
	}
	return b, nil
}

// maxOutputEvents bounds OutputEvents: a request, a wait and an answer, with room for replays.
const maxOutputEvents = 16

// OutputEvents returns run's message events in room whose output record names outputID, oldest
// first, at most maxOutputEvents: what output_status reads. The caller still reads each through
// envelope.ReadOutput, which trusts the stamp, never the payload.
func (s *Store) OutputEvents(ctx context.Context, room, run, outputID string) ([]envelope.Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM events WHERE room_id = $1 AND run_id = $2
		AND type = 'message' AND payload->'output'->>'id' = $3 ORDER BY seq LIMIT $4`, room, run, outputID, maxOutputEvents)
	if err != nil {
		return nil, fmt.Errorf("store: output events of room %s: %w", room, err)
	}
	defer rows.Close()
	var out []envelope.Event
	for rows.Next() {
		ev, err := scan(rows, room)
		if err != nil {
			return nil, fmt.Errorf("store: output events of room %s: %w", room, err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// SweepBundles deletes the bundles stored more than BundleRetention before now, unless a pending
// approval binds one, and returns how many. It runs as rooms_retention, through the table's one
// function; the broker's role holds no DELETE.
func (s *Store) SweepBundles(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT sweep_output_bundles($1)`, now.Add(-BundleRetention)).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: sweep bundles: %w", err)
	}
	return n, nil
}
