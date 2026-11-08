// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Saved approval grants (merge policy stage 5): the current state in approval_grants, moved only
// along its transitions, and each move's event in approval_grant_events, insert-only. The
// migration's triggers refuse any other move; the methods here refuse them first, by name.

// The grant refusals callers branch on.
var (
	ErrNoGrant = errors.New("no such approval grant")
	// ErrGrantState is a move the grant's status does not allow: revoking a revoked grant,
	// reconfirming an active one, using a suspended one.
	ErrGrantState = errors.New("the approval grant does not move that way from its status")
	// ErrGrantVersion is a move bound to a version the grant has since left (MP12).
	ErrGrantVersion = errors.New("the approval grant moved on since that version was read")
)

// MaxGrantList bounds one listing of grants, and MaxGrantEvents one grant's history.
const (
	MaxGrantList   = 500
	MaxGrantEvents = 200
)

// Grant is one saved approval grant as stored. LastUsedAt is zero and LastUsedPR 0 until it is used.
type Grant struct {
	ID, Repository, Class string
	Version               int
	Files                 []string
	DiffShape, PolicyHash string
	OriginPR              int
	CreatedBy             string
	CreatedAt, RenewedAt  time.Time
	ExpiresAt             time.Time
	Status                string
	StatusAt, LastUsedAt  time.Time
	LastUsedPR            int
}

// GrantEvent is one entry of a grant's audit.
type GrantEvent struct {
	ID                   int64
	GrantID              string
	Version              int
	Event, Actor, Reason string
	PolicyHash, Head     string
	PR                   int
	At                   time.Time
}

// NewGrant is a grant to create: its predicate (validated by the caller, checked again by the
// table), its origin, its creator and its lifetime from now.
type NewGrant struct {
	ID, Repository, Class string
	Files                 []string
	DiffShape, PolicyHash string
	OriginPR              int
	CreatedBy             string
	Lifetime              time.Duration
}

// GrantMove is one move of a grant: its event, the version it was read at (0 for whichever is
// current), who made it and why. PolicyHash is the policy a reconfirmation binds to, or the one a
// suspension was found under; Lifetime is a renewal's, from now; PR and Head are a use's.
type GrantMove struct {
	Event, Actor, Reason string
	Version              int
	PolicyHash           string
	Lifetime             time.Duration
	PR                   int
	Head                 string
}

// GrantFilter narrows a listing: by repository (lowercase owner/name), class and status; "" is any.
type GrantFilter struct {
	Repository, Class, Status string
	Limit                     int
}

const grantColumns = `grant_id, version, repository, class, files, diff_shape, policy_hash, origin_pr, created_by,
	created_at, renewed_at, expires_at, status, status_at, last_used_at, last_used_pr`

func scanGrant(r pgx.Row) (Grant, error) {
	var g Grant
	var used *time.Time
	var usedPR *int
	err := r.Scan(&g.ID, &g.Version, &g.Repository, &g.Class, &g.Files, &g.DiffShape, &g.PolicyHash, &g.OriginPR,
		&g.CreatedBy, &g.CreatedAt, &g.RenewedAt, &g.ExpiresAt, &g.Status, &g.StatusAt, &used, &usedPR)
	if used != nil {
		g.LastUsedAt = *used
	}
	if usedPR != nil {
		g.LastUsedPR = *usedPR
	}
	return g, err
}

func seconds(d time.Duration) float64 { return d.Seconds() }

// CreateGrant stores a grant, active at version 1 until now plus its lifetime, with its created event.
func (s *Store) CreateGrant(ctx context.Context, n NewGrant) (Grant, error) {
	if n.Lifetime <= 0 {
		return Grant{}, errors.New("a grant's lifetime is positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	g, err := scanGrant(tx.QueryRow(ctx, `INSERT INTO approval_grants (grant_id, version, repository, class, files, diff_shape,
		policy_hash, origin_pr, created_by, created_at, renewed_at, expires_at, status, status_at)
		VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, now(), now(), now() + make_interval(secs => $9), 'active', now())
		RETURNING `+grantColumns, n.ID, n.Repository, n.Class, n.Files, n.DiffShape, n.PolicyHash, n.OriginPR, n.CreatedBy, seconds(n.Lifetime)))
	if err != nil {
		return Grant{}, err
	}
	if err := grantEvent(ctx, tx, g, GrantMove{Event: "created", Actor: n.CreatedBy, PR: n.OriginPR, PolicyHash: n.PolicyHash}); err != nil {
		return Grant{}, err
	}
	return g, tx.Commit(ctx)
}

// Grant reads one grant.
func (s *Store) Grant(ctx context.Context, id string) (Grant, error) {
	g, err := scanGrant(s.pool.QueryRow(ctx, `SELECT `+grantColumns+` FROM approval_grants WHERE grant_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNoGrant
	}
	return g, err
}

// Grants lists grants, newest first, at most f.Limit (MaxGrantList when 0 or more).
func (s *Store) Grants(ctx context.Context, f GrantFilter) ([]Grant, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxGrantList {
		limit = MaxGrantList
	}
	rows, err := s.pool.Query(ctx, `SELECT `+grantColumns+` FROM approval_grants
		WHERE ($1 = '' OR repository = $1) AND ($2 = '' OR class = $2) AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC, grant_id LIMIT $4`, f.Repository, f.Class, f.Status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) { return scanGrant(r) })
}

// GrantEvents is a grant's audit, oldest first, at most MaxGrantEvents.
func (s *Store) GrantEvents(ctx context.Context, id string) ([]GrantEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT event_id, grant_id, version, event, actor, reason, policy_hash, head, pr, at
		FROM approval_grant_events WHERE grant_id = $1 ORDER BY event_id LIMIT $2`, id, MaxGrantEvents)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (GrantEvent, error) {
		var e GrantEvent
		var pr *int
		err := r.Scan(&e.ID, &e.GrantID, &e.Version, &e.Event, &e.Actor, &e.Reason, &e.PolicyHash, &e.Head, &pr, &e.At)
		if pr != nil {
			e.PR = *pr
		}
		return e, err
	})
}

// MoveGrant makes one move of grant id, under the grant's row lock: revoked, disabled, suspended,
// expired, reconfirmed, renewed or used. A move its status does not allow is ErrGrantState, one
// bound to a version since left ErrGrantVersion, an unknown grant ErrNoGrant.
func (s *Store) MoveGrant(ctx context.Context, id string, m GrantMove) (Grant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	g, err := s.moveGrant(ctx, tx, id, m)
	if err != nil {
		return Grant{}, err
	}
	return g, tx.Commit(ctx)
}

// moves are where each event may start from.
var moves = map[string][]string{
	"revoked":     {"active", "suspended", "expired"},
	"disabled":    {"active", "suspended", "expired"},
	"suspended":   {"active"},
	"expired":     {"active", "suspended"},
	"reconfirmed": {"suspended"},
	"renewed":     {"active", "expired"},
	"used":        {"active"},
}

func (s *Store) moveGrant(ctx context.Context, tx pgx.Tx, id string, m GrantMove) (Grant, error) {
	from, ok := moves[m.Event]
	if !ok {
		return Grant{}, fmt.Errorf("%q is not a grant move", m.Event)
	}
	g, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM approval_grants WHERE grant_id = $1 FOR UPDATE`, id))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Grant{}, ErrNoGrant
	case err != nil:
		return Grant{}, err
	case m.Version != 0 && m.Version != g.Version:
		return Grant{}, ErrGrantVersion
	case !slices.Contains(from, g.Status):
		return Grant{}, ErrGrantState
	}
	var q string
	args := []any{id}
	switch m.Event {
	case "revoked", "disabled", "suspended", "expired":
		q = `UPDATE approval_grants SET status = $2, status_at = now() WHERE grant_id = $1 RETURNING ` + grantColumns
		args = append(args, m.Event)
	case "reconfirmed":
		q = `UPDATE approval_grants SET status = 'active', status_at = now(), version = version + 1, policy_hash = $2
			WHERE grant_id = $1 RETURNING ` + grantColumns
		args = append(args, m.PolicyHash)
	case "renewed":
		if m.Lifetime <= 0 {
			return Grant{}, errors.New("a renewal's lifetime is positive")
		}
		q = `UPDATE approval_grants SET status = 'active', status_at = now(), version = version + 1, renewed_at = now(),
			expires_at = now() + make_interval(secs => $2) WHERE grant_id = $1 RETURNING ` + grantColumns
		args = append(args, seconds(m.Lifetime))
	case "used":
		q = `UPDATE approval_grants SET last_used_at = now(), last_used_pr = $2 WHERE grant_id = $1 RETURNING ` + grantColumns
		args = append(args, m.PR)
	}
	if g, err = scanGrant(tx.QueryRow(ctx, q, args...)); err != nil {
		return Grant{}, err
	}
	return g, grantEvent(ctx, tx, g, m)
}

// grantEvent appends m's event for g at its current version.
func grantEvent(ctx context.Context, tx pgx.Tx, g Grant, m GrantMove) error {
	var pr *int
	if m.PR > 0 {
		pr = &m.PR
	}
	_, err := tx.Exec(ctx, `INSERT INTO approval_grant_events (grant_id, version, event, actor, reason, policy_hash, pr, head)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, g.ID, g.Version, m.Event, m.Actor, m.Reason, m.PolicyHash, pr, m.Head)
	return err
}

// boundTo is whether a grant's hash, 12 hex digits at least, is a prefix of the policy's: the
// grant was made under that policy.
func boundTo(grantHash, policyHash string) bool {
	return len(grantHash) >= 12 && strings.HasPrefix(policyHash, grantHash)
}

// LookupGrants is the factory's read of a repository class's grants, immediately before a
// decision (merge policy stage 5): under their row locks, an active or suspended grant past its
// expiry or past maxLifetime since its last renewal is expired, an active one made under another
// policy than policyHash is suspended until reconfirmed, and the grants still active are returned
// with the events the lookup recorded.
func (s *Store) LookupGrants(ctx context.Context, repository, class, policyHash string, maxLifetime time.Duration, actor string) ([]Grant, []string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT `+grantColumns+`, now() >= least(expires_at, renewed_at + make_interval(secs => $3))
		FROM approval_grants WHERE repository = $1 AND class = $2 AND status IN ('active', 'suspended')
		ORDER BY grant_id FOR UPDATE`, repository, class, seconds(maxLifetime))
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		g       Grant
		expired bool
	}
	read, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var used *time.Time
		var usedPR *int
		err := r.Scan(&x.g.ID, &x.g.Version, &x.g.Repository, &x.g.Class, &x.g.Files, &x.g.DiffShape, &x.g.PolicyHash,
			&x.g.OriginPR, &x.g.CreatedBy, &x.g.CreatedAt, &x.g.RenewedAt, &x.g.ExpiresAt, &x.g.Status, &x.g.StatusAt,
			&used, &usedPR, &x.expired)
		if used != nil {
			x.g.LastUsedAt = *used
		}
		if usedPR != nil {
			x.g.LastUsedPR = *usedPR
		}
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	var active []Grant
	var events []string
	for _, x := range read {
		m := GrantMove{Actor: actor, PolicyHash: policyHash}
		switch {
		case x.expired:
			m.Event, m.Reason = "expired", "past its expiry or the maximum grant lifetime"
		case x.g.Status == "active" && !boundTo(x.g.PolicyHash, policyHash):
			m.Event, m.Reason = "suspended", "the repository policy changed"
		case x.g.Status == "active":
			active = append(active, x.g)
			continue
		default:
			continue
		}
		if _, err := s.moveGrant(ctx, tx, x.g.ID, m); err != nil {
			return nil, nil, err
		}
		events = append(events, m.Event)
	}
	return active, events, tx.Commit(ctx)
}
