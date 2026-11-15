// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoHandoff is an unknown or expired handoff id.
var ErrNoHandoff = errors.New("no such handoff")

// Handoff is one stored plan held on the broker (UX2), bound to the principal that asked for it
// until ExpiresAt.
type Handoff struct {
	ID, Principal string
	Args, Plan    json.RawMessage
	Hash          string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// PutHandoff inserts or replaces h by its ID.
func (s *Store) PutHandoff(ctx context.Context, h Handoff) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO handoffs (id, principal, args, plan, hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			principal = EXCLUDED.principal,
			args = EXCLUDED.args,
			plan = EXCLUDED.plan,
			hash = EXCLUDED.hash,
			created_at = EXCLUDED.created_at,
			expires_at = EXCLUDED.expires_at`,
		h.ID, h.Principal, string(h.Args), string(h.Plan), h.Hash, h.CreatedAt.UTC(), h.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("store: put handoff: %w", err)
	}
	return nil
}

// GetHandoff returns the stored handoff for id when its expiry is after now, or ErrNoHandoff when
// unknown or expired.
func (s *Store) GetHandoff(ctx context.Context, id string, now time.Time) (Handoff, error) {
	var h Handoff
	err := s.pool.QueryRow(ctx, `SELECT id, principal, args, plan, hash, created_at, expires_at
		FROM handoffs WHERE id = $1 AND expires_at > $2`, id, now.UTC()).
		Scan(&h.ID, &h.Principal, &h.Args, &h.Plan, &h.Hash, &h.CreatedAt, &h.ExpiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Handoff{}, fmt.Errorf("store: get handoff: %w", ErrNoHandoff)
	case err != nil:
		return Handoff{}, fmt.Errorf("store: get handoff: %w", err)
	}
	h.CreatedAt = h.CreatedAt.UTC()
	h.ExpiresAt = h.ExpiresAt.UTC()
	return h, nil
}

// PruneHandoffs deletes every stored handoff whose expiry is at or before before, and returns how
// many were deleted.
func (s *Store) PruneHandoffs(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM handoffs WHERE expires_at <= $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("store: prune handoffs: %w", err)
	}
	return tag.RowsAffected(), nil
}
