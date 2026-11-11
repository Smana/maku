// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The outcomes of an intent sample (SI19): shadowed when stage 1 classified and answered as ever,
// then, from stage 3, what became of the confirm.
const (
	SampleShadowed  = "shadowed"
	SampleConfirmed = "confirmed"
	SampleCancelled = "cancelled"
	SampleExpired   = "expired"
)

// IntentSample is one reply of an opted-in channel with its label. Reply must already be redacted.
type IntentSample struct {
	ID         int64     `json:"id"`
	Channel    string    `json:"channel"`
	Reply      string    `json:"reply"`
	Intent     string    `json:"intent"`
	Confidence float64   `json:"confidence"`
	Outcome    string    `json:"outcome"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ErrBadIntentSample is a sample the table's checks refuse: a malformed channel, an empty or
// oversized reply, a label outside the nine, a retention under a day.
var ErrBadIntentSample = errors.New("store: not a valid intent sample")

// InsertIntentSample stores a shadowed sample that expires retentionDays from now, and returns its
// id. The expiry is stamped here so the retention job needs no configuration.
func (s *Store) InsertIntentSample(ctx context.Context, in IntentSample, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		return 0, fmt.Errorf("%w: retention is at least one day", ErrBadIntentSample)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO intent_samples (channel, reply, intent, confidence, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(days => $5)) RETURNING sample_id`,
		in.Channel, in.Reply, in.Intent, in.Confidence, retentionDays).Scan(&id)
	if err != nil {
		if isCheckViolation(err) {
			return 0, ErrBadIntentSample
		}
		return 0, fmt.Errorf("store: insert intent sample: %w", err)
	}
	return id, nil
}

// SetIntentSampleOutcome moves a shadowed sample to confirmed, cancelled or expired, once, through
// the table's one function: the broker holds no UPDATE. It reports whether this call moved it.
func (s *Store) SetIntentSampleOutcome(ctx context.Context, id int64, outcome string) (bool, error) {
	var moved bool
	if err := s.pool.QueryRow(ctx, `SELECT intent_sample_outcome($1, $2)`, id, outcome).Scan(&moved); err != nil {
		if isCheckViolation(err) {
			return false, ErrBadIntentSample
		}
		return false, fmt.Errorf("store: set intent sample outcome: %w", err)
	}
	return moved, nil
}

// IntentSamplesSince lists the samples of the last days days, oldest first: what `maku-broker
// intent-samples` exports for labelling.
func (s *Store) IntentSamplesSince(ctx context.Context, days int) ([]IntentSample, error) {
	rows, err := s.pool.Query(ctx, `SELECT sample_id, channel, reply, intent, confidence, outcome, created_at
		FROM intent_samples WHERE created_at >= now() - make_interval(days => $1) ORDER BY sample_id`, days)
	if err != nil {
		return nil, fmt.Errorf("store: list intent samples: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (IntentSample, error) {
		var x IntentSample
		return x, r.Scan(&x.ID, &x.Channel, &x.Reply, &x.Intent, &x.Confidence, &x.Outcome, &x.CreatedAt)
	})
}

// PurgeIntentSamples deletes every sample past its expiry and returns how many. It runs as
// rooms_retention; the broker's role holds no DELETE.
func (s *Store) PurgeIntentSamples(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM intent_samples WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("store: purge intent samples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// isCheckViolation is a table check refusing a row, which can name the row's text in its detail:
// the sample's error never carries it.
func isCheckViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514"
}
