// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// memShots is the screenshot store in memory: idempotent on (room, run, n), a per-run quota.
type memShots struct {
	mu    sync.Mutex
	shots map[string]store.Screenshot
	quota int
	err   error
}

func (m *memShots) PutScreenshot(_ context.Context, room string, sh store.Screenshot) (store.ScreenshotMeta, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return store.ScreenshotMeta{}, false, m.err
	}
	sh.Digest, sh.Size = digest(sh.Bytes), len(sh.Bytes)
	k := fmt.Sprintf("%s/%s/%d", room, sh.RunID, sh.N)
	if old, ok := m.shots[k]; ok {
		if old.Digest != sh.Digest {
			return store.ScreenshotMeta{}, false, store.ErrScreenshotConflict
		}
		return old.ScreenshotMeta, false, nil
	}
	if m.quota > 0 && len(m.shots) >= m.quota {
		return store.ScreenshotMeta{}, false, store.ErrScreenshotQuota
	}
	m.shots[k] = sh
	return sh.ScreenshotMeta, true, nil
}

func shotRun(id, roomID string, screenshots string) *unstructured.Unstructured {
	u := agentRun(id, roomID, "Running")
	if screenshots != "" {
		u.SetAnnotations(map[string]string{runwatch.ScreenshotsAnnotation: screenshots})
	}
	return u
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newShotServer(t *testing.T) (*Server, *memLog, *runwatch.Watcher, *memShots) {
	t.Helper()
	s, log, w := newServer(t)
	shots := &memShots{shots: map[string]store.Screenshot{}}
	s.Screenshots = shots
	return s, log, w, shots
}

func shotPath(run string, n int) string {
	return fmt.Sprintf("/v1/bridge/screenshots?run=%s&n=%d&url=%s&vision=1", run, n, "http%3A%2F%2F127.0.0.1%3A8080%2Fapp%3Ftoken%3Dx")
}

// Phase B: the bridge uploads a screenshot; the room gains one screenshot event with the meta and
// no pixels, stamped as the run's agent; a repeat is 200 and adds nothing.
func TestABridgeUploadsAScreenshot(t *testing.T) {
	s, log, w, shots := newShotServer(t)
	w.Upsert(t.Context(), shotRun(runA, room, "10"))
	h := s.Routes()
	hello(t, h, runA)
	img := pngOf(t, 64, 32)
	rec := call(t, h, http.MethodPost, shotPath(runA, 1), "run:"+runA, img)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var meta envelope.ScreenshotPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil || meta.N != 1 || meta.Width != 64 || meta.Height != 32 ||
		meta.Mime != "image/png" || len(meta.Digest) != 64 || !meta.Vision || meta.PageURL != "http://127.0.0.1:8080/app" {
		t.Fatalf("meta %+v %v", meta, err)
	}
	evs := log.stored()
	if len(evs) != 1 || evs[0].Type != envelope.Screenshot || evs[0].Actor.ID != "agent:"+runA || evs[0].RunID != runA {
		t.Fatalf("events %+v", evs)
	}
	var p envelope.ScreenshotPayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p != meta {
		t.Fatalf("payload %s, want %+v", evs[0].Payload, meta)
	}
	if len(evs[0].Payload) > 512 || bytes.Contains(evs[0].Payload, []byte("PNG")) {
		t.Fatalf("the event carries more than the meta: %d bytes", len(evs[0].Payload))
	}
	if rec := call(t, h, http.MethodPost, shotPath(runA, 1), "run:"+runA, img); rec.Code != http.StatusOK {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body)
	}
	if n := len(log.stored()); n != 1 {
		t.Fatalf("a repeat appended: %d events", n)
	}
	if len(shots.shots) != 1 {
		t.Fatalf("%d stored", len(shots.shots))
	}
}

func TestAScreenshotUploadIsRefused(t *testing.T) {
	img := func(t *testing.T) []byte { return pngOf(t, 8, 8) }
	for _, tc := range []struct {
		name        string
		annotation  string
		token, path string
		body        func(t *testing.T) []byte
		setup       func(*memShots, *memLog)
		code        int
		reason      string
	}{
		{name: "a harness room-tools token", annotation: "10", token: "tools:" + runA, path: shotPath(runA, 1), body: img,
			code: http.StatusUnauthorized, reason: wire.ReasonUnauthenticated},
		{name: "another run's upload", annotation: "10", token: "run:" + runA, path: shotPath(runB, 1), body: img,
			code: http.StatusForbidden, reason: wire.ReasonNotPermitted},
		{name: "a run without the annotation", token: "run:" + runA, path: shotPath(runA, 1), body: img,
			code: http.StatusForbidden, reason: wire.ReasonScreenshotsOff},
		{name: "n past the run's cap", annotation: "2", token: "run:" + runA, path: shotPath(runA, 3), body: img,
			code: http.StatusBadRequest, reason: wire.ReasonBadScreenshot},
		{name: "no n", annotation: "2", token: "run:" + runA, path: "/v1/bridge/screenshots?run=" + runA, body: img,
			code: http.StatusBadRequest, reason: wire.ReasonBadScreenshot},
		{name: "a bad vision", annotation: "2", token: "run:" + runA, path: "/v1/bridge/screenshots?run=" + runA + "&n=1&vision=yes",
			body: img, code: http.StatusBadRequest, reason: wire.ReasonBadScreenshot},
		{name: "html", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1),
			body: func(*testing.T) []byte { return []byte("<html><script>x</script></html>") },
			code: http.StatusUnprocessableEntity, reason: wire.ReasonNotAnImage},
		{name: "over the viewport", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1),
			body: func(t *testing.T) []byte { return pngOf(t, 1921, 1) },
			code: http.StatusUnprocessableEntity, reason: wire.ReasonDimensions},
		{name: "over 1 MiB", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1),
			body: func(*testing.T) []byte { return bytes.Repeat([]byte("x"), store.MaxScreenshot+1) },
			code: http.StatusRequestEntityTooLarge, reason: wire.ReasonScreenshotTooLarge},
		{name: "over the quota", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1), body: img,
			setup: func(m *memShots, _ *memLog) { m.err = store.ErrScreenshotQuota },
			code:  http.StatusTooManyRequests, reason: wire.ReasonScreenshotQuota},
		{name: "another image under the same n", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1), body: img,
			setup: func(m *memShots, _ *memLog) { m.err = store.ErrScreenshotConflict },
			code:  http.StatusConflict, reason: wire.ReasonScreenshotConflict},
		{name: "a sealed room", annotation: "10", token: "run:" + runA, path: shotPath(runA, 1), body: img,
			setup: func(m *memShots, _ *memLog) { m.err = fmt.Errorf("put: %w", store.ErrSealed) },
			code:  http.StatusGone, reason: wire.ReasonSealed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, log, w, shots := newShotServer(t)
			w.Upsert(t.Context(), shotRun(runA, room, tc.annotation))
			w.Upsert(t.Context(), shotRun(runB, room, tc.annotation))
			h := s.Routes()
			hello(t, h, runA)
			if tc.setup != nil {
				tc.setup(shots, log)
			}
			rec := call(t, h, http.MethodPost, tc.path, tc.token, tc.body(t))
			if rec.Code != tc.code || reason(t, rec) != tc.reason {
				t.Fatalf("%d %q, want %d %q", rec.Code, reason(t, rec), tc.code, tc.reason)
			}
			if n := len(log.stored()); n != 0 {
				t.Fatalf("a refusal appended %d events", n)
			}
		})
	}
}

// A bridge cannot forge a screenshot event through its event batches: only the upload route
// writes one, from what the broker decoded.
func TestABridgeCannotPushAScreenshotEvent(t *testing.T) {
	s, log, w, _ := newShotServer(t)
	w.Upsert(t.Context(), shotRun(runA, room, "10"))
	h := s.Routes()
	hello(t, h, runA)
	it := wire.Item{Stream: wire.StreamEvents, Seq: 1, Type: envelope.Screenshot,
		Payload: envelope.Must(envelope.ScreenshotPayload{Run: runA, N: 1, Digest: digest([]byte("x")), Mime: "image/png"})}
	rec := call(t, h, http.MethodPost, "/v1/bridge/events", "run:"+runA, batch(it))
	if rec.Code != http.StatusBadRequest || reason(t, rec) != wire.ReasonBadItem {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	log.untouched(t)
}

// A broker without a screenshot store refuses the route.
func TestTheScreenshotRouteNeedsAStore(t *testing.T) {
	s, _, w := newServer(t)
	w.Upsert(t.Context(), shotRun(runA, room, "10"))
	h := s.Routes()
	hello(t, h, runA)
	if rec := call(t, h, http.MethodPost, shotPath(runA, 1), "run:"+runA, pngOf(t, 8, 8)); rec.Code != http.StatusNotImplemented {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The factory, and only the factory, stores an issue's images as the room's request images (v5
// Q16 B3): scaled down, re-encoded, numbered 1 to MaxRequestImages, with no run.
func TestRoomRequestImage(t *testing.T) {
	s, _, _ := outputServer(t)
	shots := &memShots{shots: map[string]store.Screenshot{}}
	s.Screenshots = shots
	h := s.Routes()
	img := func(w, ht int) []byte {
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, ht)))
		return b.Bytes()
	}
	path := func(room, n string) string { return "/v1/rooms/" + room + "/images?n=" + n }
	rec := call(t, h, http.MethodPost, path(room, "1"), "sys:"+factory, img(3136, 1000))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := shots.shots[room+"//1"]
	if got.Source != store.SourceRequest || got.RunID != "" || got.Width != 1568 || got.Height != 500 {
		t.Fatalf("stored %+v", got.ScreenshotMeta)
	}
	if rec := call(t, h, http.MethodPost, path(room, "1"), "sys:"+factory, img(3136, 1000)); rec.Code != http.StatusOK {
		t.Fatalf("a repeat is 200: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		path, token string
		body        []byte
		code        int
		reason      string
	}{
		"a run token":     {path(room, "2"), "run:" + runA, img(10, 10), http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"another system":  {path(room, "2"), "sys:system:triage", img(10, 10), http.StatusForbidden, wire.ReasonNotPermitted},
		"n out of range":  {path(room, "5"), "sys:" + factory, img(10, 10), http.StatusBadRequest, wire.ReasonBadScreenshot},
		"no n":            {"/v1/rooms/" + room + "/images", "sys:" + factory, img(10, 10), http.StatusBadRequest, wire.ReasonBadScreenshot},
		"a bad room":      {path("NOPE", "2"), "sys:" + factory, img(10, 10), http.StatusBadRequest, wire.ReasonBadRoom},
		"not an image":    {path(room, "2"), "sys:" + factory, []byte("<svg/>"), http.StatusUnprocessableEntity, wire.ReasonNotAnImage},
		"a bomb's header": {path(room, "2"), "sys:" + factory, img(9000, 10), http.StatusUnprocessableEntity, wire.ReasonDimensions},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, http.MethodPost, c.path, c.token, c.body)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.reason)
			}
		})
	}
}
