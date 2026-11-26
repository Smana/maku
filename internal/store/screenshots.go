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

// MaxScreenshot is one stored image's cap, after the broker's re-encode (phase B caps).
const MaxScreenshot = 1 << 20

// Per-run and per-room screenshot quotas (phase B caps): 25 per run is the gate's ceiling.
const (
	MaxScreenshotsPerRun      = 25
	MaxScreenshotsPerRoom     = 100
	MaxScreenshotBytesPerRoom = 64 << 20
)

// A screenshot's source: a run's browser, or an issue's request image, which has no run.
const (
	SourceBrowser = "browser"
	SourceRequest = "request"
)

var (
	// ErrBadScreenshot is a screenshot the table cannot hold: no bytes or over MaxScreenshot, a
	// type other than PNG or JPEG, n under 1, or a run id that does not fit its source.
	ErrBadScreenshot = errors.New("not a screenshot the room can keep")
	// ErrScreenshotQuota is a run or room that has reached its screenshot count or byte quota.
	ErrScreenshotQuota = errors.New("screenshot storage quota exceeded")
	// ErrScreenshotConflict is a second, different image under a (run, n) the room already holds.
	ErrScreenshotConflict = errors.New("another screenshot is stored under that run and number")
	// ErrNoScreenshot is a digest the room holds no screenshot for.
	ErrNoScreenshot = errors.New("no such screenshot")
)

// ScreenshotMeta is a stored screenshot without its bytes. RunID is "" for a request image.
// Digest and Size are the store's, computed from the bytes.
type ScreenshotMeta struct {
	RunID         string
	N             int
	Digest, Mime  string
	Width, Height int
	Size          int
	Source        string
	// PageURL is the loopback page the browser showed, "" when unknown.
	PageURL string
	// Vision is whether the run's model could see the image (C15).
	Vision      bool
	PublishedAt time.Time // zero until published
	CreatedAt   time.Time
}

// Screenshot is a stored screenshot with its bytes, already re-encoded by the caller (shots).
type Screenshot struct {
	ScreenshotMeta
	Bytes []byte
}

func (sh Screenshot) valid() bool {
	runOK := (sh.Source == SourceBrowser && envelope.ValidID(sh.RunID)) || (sh.Source == SourceRequest && sh.RunID == "")
	return runOK && len(sh.Bytes) > 0 && len(sh.Bytes) <= MaxScreenshot && sh.N >= 1 && sh.N <= MaxScreenshotsPerRoom &&
		(sh.Mime == "image/png" || sh.Mime == "image/jpeg") && sh.Width > 0 && sh.Height > 0
}

// PutScreenshot stores sh in room under (sh.RunID, sh.N), with its digest and size computed from
// its bytes, and reports whether it was new. A repeat of the same image is the stored row; another
// image under the same key is ErrScreenshotConflict. A sealed room takes none.
func (s *Store) PutScreenshot(ctx context.Context, room string, sh Screenshot) (ScreenshotMeta, bool, error) {
	if !sh.valid() {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot: %w", ErrBadScreenshot)
	}
	sum := sha256.Sum256(sh.Bytes)
	sh.Digest, sh.Size = hex.EncodeToString(sum[:]), len(sh.Bytes)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The room's row lock orders the put against a seal and against another put's quota check.
	switch sealed, _, err := lockRoom(ctx, tx, room); {
	case err != nil:
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, err)
	case sealed:
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, ErrSealed)
	}
	existing, err := scanShot(tx.QueryRow(ctx, `SELECT `+shotCols+` FROM run_screenshots
		WHERE room_id = $1 AND run_id = $2 AND n = $3`, room, sh.RunID, sh.N))
	switch {
	case err == nil && existing.Digest == sh.Digest:
		return existing, false, nil
	case err == nil:
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, ErrScreenshotConflict)
	case !errors.Is(err, pgx.ErrNoRows):
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, err)
	}
	var roomCount, roomBytes, runCount int64
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(size), 0), count(*) FILTER (WHERE run_id = $2)
		FROM run_screenshots WHERE room_id = $1`, room, sh.RunID).Scan(&roomCount, &roomBytes, &runCount); err != nil {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, err)
	}
	if runCount >= MaxScreenshotsPerRun || roomCount >= MaxScreenshotsPerRoom || roomBytes+int64(sh.Size) > MaxScreenshotBytesPerRoom {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, ErrScreenshotQuota)
	}
	m, err := scanShot(tx.QueryRow(ctx, `INSERT INTO run_screenshots
		(room_id, run_id, n, digest, mime, width, height, size, bytes, source, page_url, vision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING `+shotCols,
		room, sh.RunID, sh.N, sh.Digest, sh.Mime, sh.Width, sh.Height, sh.Size, sh.Bytes, sh.Source, sh.PageURL, sh.Vision))
	if err != nil {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ScreenshotMeta{}, false, fmt.Errorf("store: put screenshot in room %s: %w", room, err)
	}
	return m, true, nil
}

// Screenshots lists room's screenshots without their bytes, request images first, then by run and
// number.
func (s *Store) Screenshots(ctx context.Context, room string) ([]ScreenshotMeta, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+shotCols+` FROM run_screenshots WHERE room_id = $1
		ORDER BY run_id, n LIMIT $2`, room, MaxScreenshotsPerRoom)
	if err != nil {
		return nil, fmt.Errorf("store: screenshots of room %s: %w", room, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ScreenshotMeta, error) { return scanShot(r) })
	if err != nil {
		return nil, fmt.Errorf("store: screenshots of room %s: %w", room, err)
	}
	return out, nil
}

// Screenshot returns the image room stores under digest, or ErrNoScreenshot. Two rows with one
// digest hold the same bytes; the first stored is returned.
func (s *Store) Screenshot(ctx context.Context, room, digest string) (Screenshot, error) {
	var sh Screenshot
	m, err := scanShot(s.pool.QueryRow(ctx, `SELECT `+shotCols+`, bytes FROM run_screenshots WHERE room_id = $1 AND digest = $2
		ORDER BY created_at, run_id, n LIMIT 1`, room, digest), &sh.Bytes)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Screenshot{}, fmt.Errorf("store: read screenshot: %w", ErrNoScreenshot)
	case err != nil:
		return Screenshot{}, fmt.Errorf("store: read screenshot: %w", err)
	}
	sh.ScreenshotMeta = m
	return sh, nil
}

const shotCols = `run_id, n, digest, mime, width, height, size, source, page_url, vision, published_at, created_at`

// scanShot reads shotCols, then extra.
func scanShot(row pgx.Row, extra ...any) (ScreenshotMeta, error) {
	var m ScreenshotMeta
	var published *time.Time
	dest := append([]any{&m.RunID, &m.N, &m.Digest, &m.Mime, &m.Width, &m.Height, &m.Size, &m.Source, &m.PageURL,
		&m.Vision, &published, &m.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return ScreenshotMeta{}, err
	}
	if published != nil {
		m.PublishedAt = *published
	}
	return m, nil
}
