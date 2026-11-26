// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Smana/maku/internal/store"
)

// ScreenshotReader is the store's screenshot reads (harness screenshots phase B); *store.Store
// implements it.
type ScreenshotReader interface {
	Screenshots(ctx context.Context, room string) ([]store.ScreenshotMeta, error)
	Screenshot(ctx context.Context, room, digest string) (store.Screenshot, error)
}

// screenshotRow is one row of GET /api/rooms/{id}/screenshots: a stored image's meta, never its
// bytes. Its keys are the screenshot event's, so the page reads one shape.
type screenshotRow struct {
	Run         string    `json:"run"`
	N           int       `json:"n"`
	Digest      string    `json:"digest"`
	Mime        string    `json:"mime"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Bytes       int       `json:"bytes"`
	Source      string    `json:"source"`
	PageURL     string    `json:"pageUrl,omitempty"`
	Vision      bool      `json:"vision"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	CreatedAt   time.Time `json:"createdAt,omitzero"`
}

var errShotsUnreadable = errors.New("screenshots unreadable")

// screenshotList is GET /api/rooms/{id}/screenshots: the room's images, by run then n. It is a room
// read like roomEvents, under the same gate: an unreadable room answers as a missing one.
func (s *Server) screenshotList(w http.ResponseWriter, r *http.Request) {
	ctx, id, ok := s.shotGate(w, r)
	if !ok {
		return
	}
	defer ctx.cancel()
	metas, err := s.Screenshots.Screenshots(ctx, id)
	if err != nil {
		s.log().Warn("screenshots: unreadable", "room", id, "err", err)
		http.Error(w, errShotsUnreadable.Error(), http.StatusServiceUnavailable)
		return
	}
	rows := make([]screenshotRow, 0, len(metas))
	for _, m := range metas {
		rows = append(rows, screenshotRow{Run: m.RunID, N: m.N, Digest: m.Digest, Mime: m.Mime, Width: m.Width,
			Height: m.Height, Bytes: m.Size, Source: m.Source, PageURL: m.PageURL, Vision: m.Vision,
			PublishedAt: m.PublishedAt, CreatedAt: m.CreatedAt})
	}
	writeJSON(w, rows)
}

// screenshotImage is GET /api/rooms/{id}/screenshots/{digest}: the stored bytes, which the broker
// re-encoded, under the type the broker set. The headers keep the browser from reading them as
// anything but an image: no sniffing, and a sandboxed, sourceless document if opened on its own.
func (s *Server) screenshotImage(w http.ResponseWriter, r *http.Request) {
	ctx, id, ok := s.shotGate(w, r)
	if !ok {
		return
	}
	defer ctx.cancel()
	digest := r.PathValue("digest")
	if !validDigest(digest) {
		http.NotFound(w, r)
		return
	}
	sh, err := s.Screenshots.Screenshot(ctx, id, digest)
	if errors.Is(err, store.ErrNoScreenshot) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log().Warn("screenshot: unreadable", "room", id, "err", err)
		http.Error(w, errShotsUnreadable.Error(), http.StatusServiceUnavailable)
		return
	}
	ext := "png"
	if sh.Mime == "image/jpeg" {
		ext = "jpg"
	}
	run := sh.RunID
	if run == "" {
		run = "request"
	}
	h := w.Header()
	h.Set("Content-Type", sh.Mime)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", `inline; filename="`+id+"-"+run+"-"+strconv.Itoa(sh.N)+"."+ext+`"`)
	h.Set("Cache-Control", "private, max-age=3600, immutable")
	h.Set("Content-Length", strconv.Itoa(len(sh.Bytes)))
	_, _ = w.Write(sh.Bytes)
}

// shotCtx is a route's bounded context with its cancel, so a handler defers one call.
type shotCtx struct {
	context.Context
	cancel context.CancelFunc
}

// shotGate is roomEvents' gate: an authenticated, admitted principal within its read rate, who may
// read the room (D7). It answers the refusal itself.
func (s *Server) shotGate(w http.ResponseWriter, r *http.Request) (shotCtx, string, bool) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowed(w, r, p) {
		return shotCtx{}, "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	id := r.PathValue("id")
	if _, _, _, err := s.readRoom(ctx, id, p); err != nil {
		cancel()
		http.Error(w, err.Error(), roomStatus(err))
		return shotCtx{}, "", false
	}
	return shotCtx{ctx, cancel}, id, true
}

// validDigest is a lowercase hex SHA-256, the store's digest form; anything else names no image.
func validDigest(d string) bool {
	if len(d) != 64 {
		return false
	}
	for _, c := range d {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
