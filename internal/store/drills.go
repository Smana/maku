// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxDrillReport bounds one stored drill report's JSON (HR38).
const MaxDrillReport = 256 << 10

var (
	// ErrDrillExists is a second report under a drill id: each drill is scored once.
	ErrDrillExists = errors.New("a report for this drill is already stored")
	// ErrBadDrillReport is a report the table would refuse: a bad id, version, hash or time, or a
	// report that is not one JSON value of at most MaxDrillReport bytes.
	ErrBadDrillReport = errors.New("a drill report has an id, a version, a 64-hex hash, a time and a JSON report of at most 256 KiB")

	drillIDRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	drillVersionRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	drillHashRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// DrillReport is one scored drill as the store keeps it (HR38): what it proved (a Maku version under
// one hardening configuration hash), when, whether it passed, and the scorer's report.
type DrillReport struct {
	ID         string
	Version    string
	ConfigHash string
	At         time.Time
	Pass       bool
	Report     json.RawMessage
}

// ValidDrillID reports whether id is a drill id the table takes.
func ValidDrillID(id string) bool { return drillIDRE.MatchString(id) }

// Validate holds r to what the table takes.
func (r DrillReport) Validate() error {
	switch {
	case !drillIDRE.MatchString(r.ID), !drillVersionRE.MatchString(r.Version), !drillHashRE.MatchString(r.ConfigHash),
		r.At.IsZero(), len(r.Report) == 0, len(r.Report) > MaxDrillReport, !json.Valid(r.Report):
		return ErrBadDrillReport
	}
	return nil
}

// PutDrillReport stores r once: a second report under its id is ErrDrillExists, whatever it holds.
func (s *Store) PutDrillReport(ctx context.Context, r DrillReport) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("store: put drill report: %w", err)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO drill_reports (drill_id, version, config_hash, at, pass, report)
		VALUES ($1, $2, $3, $4, $5, $6)`, r.ID, r.Version, r.ConfigHash, r.At.UTC(), r.Pass, string(r.Report))
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &pg) && pg.Code == "23505": // unique_violation
		return fmt.Errorf("store: put drill report %s: %w", r.ID, ErrDrillExists)
	case errors.As(err, &pg) && pg.Code == "23514": // check_violation: a jsonb over the bound
		return fmt.Errorf("store: put drill report %s: %w", r.ID, ErrBadDrillReport)
	case err != nil:
		return fmt.Errorf("store: put drill report %s: %w", r.ID, err)
	}
	return nil
}

// LatestDrill returns the newest report for version and configHash, passing or not, and whether
// there is one (R6, #531's review): a fail newer than a pass makes R6 false until a newer pass.
func (s *Store) LatestDrill(ctx context.Context, version, configHash string) (id string, at time.Time, pass, found bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT drill_id, at, pass FROM drill_reports WHERE version = $1 AND config_hash = $2
		ORDER BY at DESC, drill_id DESC LIMIT 1`, version, configHash).Scan(&id, &at, &pass)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", time.Time{}, false, false, nil
	case err != nil:
		return "", time.Time{}, false, false, fmt.Errorf("store: latest drill: %w", err)
	}
	return id, at.UTC(), pass, true, nil
}

// DrillPassed is whether the stored report of drill id passed, and whether one is stored: what the
// factory marks a manifest with when its report was stored by an earlier pass (#520's review).
func (s *Store) DrillPassed(ctx context.Context, id string) (pass, found bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT pass FROM drill_reports WHERE drill_id = $1`, id).Scan(&pass)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("store: read drill %s: %w", id, err)
	}
	return pass, true, nil
}
