// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/shots"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// The largest screenshot the broker keeps, in pixels: the browser gate's largest viewport.
const (
	maxShotWidth  = 1920
	maxShotHeight = 1080
)

// Screenshots stores the images a run's bridge relays from its browser gate (harness screenshots
// phase B); *store.Store implements it.
type Screenshots interface {
	PutScreenshot(ctx context.Context, room string, sh store.Screenshot) (store.ScreenshotMeta, bool, error)
}

// screenshot is POST /v1/bridge/screenshots?run=&n=&url=&vision=0|1, the body one image. Only a
// run's bridge calls it, with the run token the harness container never holds (C12). The broker
// decodes and re-encodes the image (shots.Normalize), stores the result under (run, n) and appends
// one screenshot event with its meta, never its bytes. 201 is a new image, 200 a repeat.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	p, run, ok := s.bridgeAuth(w, r)
	if !ok {
		return
	}
	if s.Screenshots == nil {
		fail(w, http.StatusNotImplemented, wire.ReasonNoScreenshots)
		return
	}
	q := r.URL.Query()
	if q.Get("run") != run.ID {
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return
	}
	if run.Screenshots == 0 {
		fail(w, http.StatusForbidden, wire.ReasonScreenshotsOff)
		return
	}
	n, err := strconv.Atoi(q.Get("n"))
	vision := q.Get("vision")
	if err != nil || n < 1 || n > run.Screenshots || (vision != "" && vision != "0" && vision != "1") {
		fail(w, http.StatusBadRequest, wire.ReasonBadScreenshot)
		return
	}
	release, ok := s.admit(w, p.ID)
	if !ok {
		return
	}
	defer release()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			fail(w, http.StatusRequestEntityTooLarge, wire.ReasonScreenshotTooLarge)
			return
		}
		fail(w, http.StatusBadRequest, wire.ReasonBadScreenshot)
		return
	}
	img, mime, width, height, err := shots.Normalize(body, maxShotWidth, maxShotHeight, store.MaxScreenshot)
	switch {
	case errors.Is(err, shots.ErrDimensions):
		fail(w, http.StatusUnprocessableEntity, wire.ReasonDimensions)
		return
	case errors.Is(err, shots.ErrTooLarge):
		fail(w, http.StatusUnprocessableEntity, wire.ReasonScreenshotTooLarge)
		return
	case err != nil:
		fail(w, http.StatusUnprocessableEntity, wire.ReasonNotAnImage)
		return
	}
	ctx := r.Context()
	// The bridge holding the room's lease is the one that may write to it (Ruling Y): checked
	// before the image is stored, and the event's append is fenced on it again.
	held, err := s.Log.TouchBridge(ctx, run.Room, run.ID)
	if err != nil {
		s.logFailure(w, err, "renew the bridge lease", "room", run.Room, "run", run.ID)
		return
	}
	if !held {
		fail(w, http.StatusConflict, wire.ReasonLeaseLost)
		return
	}
	sh := store.Screenshot{ScreenshotMeta: store.ScreenshotMeta{RunID: run.ID, N: n, Mime: mime, Width: width, Height: height,
		Source: store.SourceBrowser, PageURL: s.pageURL(ctx, q.Get("url")), Vision: vision == "1"}, Bytes: img}
	m, created, err := s.Screenshots.PutScreenshot(ctx, run.Room, sh)
	switch {
	case errors.Is(err, store.ErrScreenshotQuota):
		fail(w, http.StatusTooManyRequests, wire.ReasonScreenshotQuota)
		return
	case errors.Is(err, store.ErrScreenshotConflict):
		fail(w, http.StatusConflict, wire.ReasonScreenshotConflict)
		return
	case errors.Is(err, store.ErrBadScreenshot):
		fail(w, http.StatusUnprocessableEntity, wire.ReasonNotAnImage)
		return
	case err != nil:
		s.logFailure(w, err, "store a screenshot", "room", run.Room, "run", run.ID)
		return
	}
	meta := envelope.ScreenshotPayload{Run: m.RunID, N: m.N, Digest: m.Digest, Mime: m.Mime, Width: m.Width,
		Height: m.Height, Bytes: m.Size, Source: m.Source, PageURL: m.PageURL, Vision: m.Vision}
	// A repeat appends again under the same key, a no-op, so an image stored by a request whose
	// append failed still gets its event.
	d := envelope.Draft{RoomID: run.Room, RunID: run.ID, Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: p.ID, Role: run.Role},
		Type: envelope.Screenshot, Origin: envelope.OriginHarness, OriginClient: "agent:" + run.ID + ":screenshots",
		OriginSeq: int64(n), Payload: envelope.Must(meta)}
	if _, _, err := s.Log.AppendAsBridge(ctx, run.ID, d); err != nil {
		s.logFailure(w, err, "append a screenshot event", "room", run.Room, "run", run.ID)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	reply(w, code, meta)
}

// pageURL is the page a screenshot showed, kept only as a loopback http URL's origin and path
// (envelope.PageURL), and dropped whole if the redactor finds a secret in it.
func (s *Server) pageURL(ctx context.Context, raw string) string {
	u := envelope.PageURL(raw)
	if u == "" {
		return ""
	}
	in, err := json.Marshal(map[string]string{"pageUrl": u})
	if err != nil {
		return ""
	}
	if _, rules, err := s.Redactor.Payload(ctx, in); err != nil || len(rules) > 0 {
		return ""
	}
	return u
}
