// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The promotion workflow's states (merge policy stage 6).
const (
	PromotionRequested = "requested"
	PromotionPROpened  = "pr_opened"
	PromotionMerged    = "merged"
	PromotionAbandoned = "abandoned"

	ExceptionRequested = "requested"
	ExceptionApproved  = "approved"
	ExceptionRefused   = "refused"
)

// The workflow's refusals, each one a reason the API names.
var (
	ErrNoPolicyRecord = errors.New("store: no such promotion or exception")
	// ErrPolicyMoved is a move the record's state does not allow: it was decided or closed already,
	// or the move skips what it needs (a pull request number).
	ErrPolicyMoved = errors.New("store: the record cannot move so")
	// ErrPromotionOpen is a second open promotion of one class.
	ErrPromotionOpen = errors.New("store: the class has an open promotion")
	// ErrExceptionUnusable is a promotion naming an exception that is not approved, has expired, or
	// is another class's or minimum's.
	ErrExceptionUnusable = errors.New("store: the exception is not an approved one of this class")
)

// Exception is one operator exception below the evidence minimum (MP7).
type Exception struct {
	ID            string     `json:"id"`
	Repository    string     `json:"repository"`
	Class         string     `json:"class"`
	Minimum       int        `json:"minimum"`
	Justification string     `json:"justification"`
	RequestedBy   string     `json:"requestedBy"`
	RequestedAt   time.Time  `json:"requestedAt"`
	State         string     `json:"state"`
	DecidedBy     string     `json:"decidedBy,omitempty"`
	DecidedAt     *time.Time `json:"decidedAt,omitempty"`
	Note          string     `json:"note,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

// Promotion is one request to promote a class to live, on the evidence it was granted on.
type Promotion struct {
	ID          string          `json:"id"`
	Repository  string          `json:"repository"`
	Class       string          `json:"class"`
	Threshold   int             `json:"threshold"`
	Evidence    json.RawMessage `json:"evidence"`
	ExceptionID string          `json:"exceptionId,omitempty"`
	RequestedBy string          `json:"requestedBy"`
	RequestedAt time.Time       `json:"requestedAt"`
	State       string          `json:"state"`
	PullRequest int             `json:"pullRequest,omitempty"`
	ClosedBy    string          `json:"closedBy,omitempty"`
	ClosedAt    *time.Time      `json:"closedAt,omitempty"`
}

const (
	exceptionCols = `exception_id, repository, class, minimum, justification, requested_by, requested_at, state,
		coalesce(decided_by, ''), decided_at, coalesce(note, ''), expires_at`
	promotionCols = `promotion_id, repository, class, threshold, evidence, coalesce(exception_id, ''), requested_by,
		requested_at, state, coalesce(pull_request, 0), coalesce(closed_by, ''), closed_at`
)

func scanException(row pgx.Row) (Exception, error) {
	var e Exception
	err := row.Scan(&e.ID, &e.Repository, &e.Class, &e.Minimum, &e.Justification, &e.RequestedBy, &e.RequestedAt, &e.State,
		&e.DecidedBy, &e.DecidedAt, &e.Note, &e.ExpiresAt)
	return e, err
}

func scanPromotion(row pgx.Row) (Promotion, error) {
	var p Promotion
	err := row.Scan(&p.ID, &p.Repository, &p.Class, &p.Threshold, &p.Evidence, &p.ExceptionID, &p.RequestedBy,
		&p.RequestedAt, &p.State, &p.PullRequest, &p.ClosedBy, &p.ClosedAt)
	return p, err
}

// policyErr maps the tables' refusals: a trigger's check_violation is a move the state does not
// allow, a unique violation the class's open promotion, no row an unknown id.
func policyErr(err error, check error) error {
	var pg *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNoPolicyRecord
	case errors.As(err, &pg) && pg.Code == "23505":
		return ErrPromotionOpen
	case errors.As(err, &pg) && pg.Code == "23514":
		return check
	}
	return fmt.Errorf("store: policy record: %w", err)
}

// RequestException records e as requested, now.
func (s *Store) RequestException(ctx context.Context, e Exception) (Exception, error) {
	row := s.pool.QueryRow(ctx, `INSERT INTO operator_exceptions (exception_id, repository, class, minimum, justification,
		requested_by, state) VALUES ($1, $2, $3, $4, $5, $6, 'requested') RETURNING `+exceptionCols,
		e.ID, e.Repository, e.Class, e.Minimum, e.Justification, e.RequestedBy)
	out, err := scanException(row)
	return out, policyErr(err, ErrPolicyMoved)
}

// DecideException approves (until expires) or refuses a requested exception, once.
func (s *Store) DecideException(ctx context.Context, id, state, by, note string, expires time.Time) (Exception, error) {
	var exp *time.Time
	if state == ExceptionApproved {
		exp = &expires
	}
	row := s.pool.QueryRow(ctx, `UPDATE operator_exceptions SET state = $2, decided_by = $3, decided_at = now(),
		note = nullif($4, ''), expires_at = $5 WHERE exception_id = $1 RETURNING `+exceptionCols, id, state, by, note, exp)
	out, err := scanException(row)
	return out, policyErr(err, ErrPolicyMoved)
}

// Exception reads one exception.
func (s *Store) Exception(ctx context.Context, id string) (Exception, error) {
	out, err := scanException(s.pool.QueryRow(ctx, `SELECT `+exceptionCols+` FROM operator_exceptions WHERE exception_id = $1`, id))
	return out, policyErr(err, ErrPolicyMoved)
}

// RecordPromotion records p as requested, now: ErrPromotionOpen beside an open one of its class,
// ErrExceptionUnusable when its exception cannot carry it.
func (s *Store) RecordPromotion(ctx context.Context, p Promotion) (Promotion, error) {
	var exc *string
	if p.ExceptionID != "" {
		exc = &p.ExceptionID
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO policy_promotions (promotion_id, repository, class, threshold, evidence,
		exception_id, requested_by, state) VALUES ($1, $2, $3, $4, $5, $6, $7, 'requested') RETURNING `+promotionCols,
		p.ID, p.Repository, p.Class, p.Threshold, p.Evidence, exc, p.RequestedBy)
	out, err := scanPromotion(row)
	return out, policyErr(err, ErrExceptionUnusable)
}

// MovePromotion moves a promotion to state, naming its pull request (0 keeps the one it names).
func (s *Store) MovePromotion(ctx context.Context, id, state string, pullRequest int, by string) (Promotion, error) {
	var pr *int
	if pullRequest > 0 {
		pr = &pullRequest
	}
	row := s.pool.QueryRow(ctx, `UPDATE policy_promotions SET state = $2, pull_request = coalesce($3, pull_request),
		closed_by = $4, closed_at = now() WHERE promotion_id = $1 RETURNING `+promotionCols, id, state, pr, by)
	out, err := scanPromotion(row)
	return out, policyErr(err, ErrPolicyMoved)
}

// Promotion reads one promotion.
func (s *Store) Promotion(ctx context.Context, id string) (Promotion, error) {
	out, err := scanPromotion(s.pool.QueryRow(ctx, `SELECT `+promotionCols+` FROM policy_promotions WHERE promotion_id = $1`, id))
	return out, policyErr(err, ErrPolicyMoved)
}

// policyAuditLimit bounds a repository's audit read: the newest rows of each table.
const policyAuditLimit = 100

// PolicyAudit is repository's newest promotions and exceptions, newest first.
func (s *Store) PolicyAudit(ctx context.Context, repository string) ([]Promotion, []Exception, error) {
	ps, err := collectRows(ctx, s, `SELECT `+promotionCols+` FROM policy_promotions WHERE repository = $1
		ORDER BY requested_at DESC, promotion_id LIMIT $2`, scanPromotion, repository, policyAuditLimit)
	if err != nil {
		return nil, nil, err
	}
	es, err := collectRows(ctx, s, `SELECT `+exceptionCols+` FROM operator_exceptions WHERE repository = $1
		ORDER BY requested_at DESC, exception_id LIMIT $2`, scanException, repository, policyAuditLimit)
	return ps, es, err
}

func collectRows[T any](ctx context.Context, s *Store, q string, scan func(pgx.Row) (T, error), args ...any) ([]T, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: policy audit: %w", err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: policy audit: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
