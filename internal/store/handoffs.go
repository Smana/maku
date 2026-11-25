// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// PruneHandoffs deletes every stored handoff and web start record whose expiry is at or before
// before, and returns how many were deleted.
func (s *Store) PruneHandoffs(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM handoffs WHERE expires_at <= $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("store: prune handoffs: %w", err)
	}
	starts, err := s.pool.Exec(ctx, `DELETE FROM handoff_starts WHERE expires_at <= $1`, before.UTC())
	if err != nil {
		return tag.RowsAffected(), fmt.Errorf("store: prune handoff starts: %w", err)
	}
	return tag.RowsAffected() + starts.RowsAffected(), nil
}

// HandoffStartTTL is how long a web start's record is kept: the factory reads it within minutes of
// the filing; the rest is the audit trail.
const HandoffStartTTL = 7 * 24 * time.Hour

// HandoffStart is the broker's record of a web New task start (broker-confirmed handoff): handoff
// Handoff filed issue Issue on Repository through the factory App, for Principal (ZITADEL Sub),
// whose linked GitHub login was Login, with Labels, at StartedAt. Repository is lower-cased.
type HandoffStart struct {
	Repository           string
	Issue                int
	Handoff, Principal   string
	Sub, Login           string
	Labels               []string
	StartedAt, ExpiresAt time.Time
}

// RecordHandoffStart inserts h; a second record for the same issue is refused, never replaced.
func (s *Store) RecordHandoffStart(ctx context.Context, h HandoffStart) error {
	labels := h.Labels
	if labels == nil {
		labels = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO handoff_starts (repository, issue, handoff, principal, sub, login, labels, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		strings.ToLower(h.Repository), h.Issue, h.Handoff, h.Principal, h.Sub, h.Login, labels, h.StartedAt.UTC(), h.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("store: record handoff start: %w", err)
	}
	return nil
}

// GetHandoffStart is the unexpired record of the start that filed issue on repository; found is
// false when there is none.
func (s *Store) GetHandoffStart(ctx context.Context, repository string, issue int, now time.Time) (HandoffStart, bool, error) {
	var h HandoffStart
	err := s.pool.QueryRow(ctx, `SELECT repository, issue, handoff, principal, sub, login, labels, started_at, expires_at
		FROM handoff_starts WHERE repository = $1 AND issue = $2 AND expires_at > $3`, strings.ToLower(repository), issue, now.UTC()).
		Scan(&h.Repository, &h.Issue, &h.Handoff, &h.Principal, &h.Sub, &h.Login, &h.Labels, &h.StartedAt, &h.ExpiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return HandoffStart{}, false, nil
	case err != nil:
		return HandoffStart{}, false, fmt.Errorf("store: get handoff start: %w", err)
	}
	h.StartedAt, h.ExpiresAt = h.StartedAt.UTC(), h.ExpiresAt.UTC()
	return h, true, nil
}
