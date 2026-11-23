// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

// VerdictsClient is the origin of the leader's verdict_posted and
// verdict_not_posted records. Their origin_seq is the verdict's seq, so the
// idempotency key is the record, and a new leader writes nothing twice.
const VerdictsClient = "broker:verdicts"

// agentVerdicts selects room_verdict's writes with no outcome yet, in rooms that
// can still record one (review M5): origin client, the run's own tools scope,
// and a role that holds the tool. $1 is VerdictsClient.
const agentVerdicts = ` FROM events e JOIN rooms r USING (room_id)
		WHERE e.type = 'message' AND e.actor_kind = 'agent' AND e.payload->>'kind' = 'review_verdict'
		  AND e.origin = 'client' AND e.origin_client = 'agent:' || e.run_id || ':tools'
		  AND e.actor_role = 'reviewer' AND NOT r.sealed
		  AND NOT EXISTS (SELECT 1 FROM events p
		                  WHERE p.room_id = e.room_id AND p.origin_client = $1 AND p.origin_seq = e.seq)`

// UnpostedVerdicts are agents' review verdicts after since, oldest first, with
// no outcome yet (SP2 design §3), leaving out the event ids in exclude: the
// verdicts the poster holds in backoff, so they never starve the next ones.
func (s *Store) UnpostedVerdicts(ctx context.Context, since time.Time, limit int, exclude []string) ([]envelope.Event, error) {
	if exclude == nil {
		exclude = []string{} // NULL would make ANY unknown and hide every row
	}
	return s.verdicts(ctx, "unposted verdicts", `SELECT e.room_id, `+verdictCols+agentVerdicts+`
		  AND e.ts > $2 AND NOT (e.id = ANY($3))
		ORDER BY e.ts, e.room_id, e.seq LIMIT $4`, VerdictsClient, since, exclude, limit)
}

// ExpiredVerdicts are the verdicts UnpostedVerdicts no longer returns: stamped
// at or before before, oldest first, with no outcome yet (review 3.5 m2b).
func (s *Store) ExpiredVerdicts(ctx context.Context, before time.Time, limit int) ([]envelope.Event, error) {
	return s.verdicts(ctx, "expired verdicts", `SELECT e.room_id, `+verdictCols+agentVerdicts+`
		  AND e.ts <= $2
		ORDER BY e.ts, e.room_id, e.seq LIMIT $3`, VerdictsClient, before, limit)
}

const verdictCols = `e.seq, e.id, coalesce(e.run_id, ''), e.actor_kind, e.actor_id, coalesce(e.actor_role, ''), e.type,
	e.caused_by, e.origin, e.ts, e.redactions, e.payload`

// verdicts runs one SELECT returning full event rows (PERF-M8, replacing the N+1 Range loop).
func (s *Store) verdicts(ctx context.Context, what, query string, args ...any) ([]envelope.Event, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()
	var out []envelope.Event
	for rows.Next() {
		var roomID string
		ev, err := scan(withRoom{rows, &roomID}, "")
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		ev.RoomID = roomID
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}
