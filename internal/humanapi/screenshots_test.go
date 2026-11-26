// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/store"
)

// fakeShots is the store's screenshot reads: a room's rows, by room.
type fakeShots struct {
	mu    sync.Mutex
	rooms map[string][]store.Screenshot
	err   error
}

func (f *fakeShots) Screenshots(_ context.Context, room string) ([]store.ScreenshotMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := []store.ScreenshotMeta{}
	for _, sh := range f.rooms[room] {
		out = append(out, sh.ScreenshotMeta)
	}
	return out, nil
}

func (f *fakeShots) Screenshot(_ context.Context, room, digest string) (store.Screenshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.Screenshot{}, f.err
	}
	for _, sh := range f.rooms[room] {
		if sh.Digest == digest {
			return sh, nil
		}
	}
	return store.Screenshot{}, store.ErrNoScreenshot
}

const (
	shotA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shotB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func shotsWorld() *fakeShots {
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	return &fakeShots{rooms: map[string][]store.Screenshot{
		roomID: {{ScreenshotMeta: store.ScreenshotMeta{RunID: "aaaa1111", N: 1, Digest: shotA, Mime: "image/png", Width: 1280, Height: 800,
			Size: 4, Source: store.SourceBrowser, PageURL: "http://127.0.0.1:3000/login", Vision: true, CreatedAt: at},
			Bytes: []byte("\x89PNG")}},
		roomB: {{ScreenshotMeta: store.ScreenshotMeta{RunID: "bbbb2222", N: 1, Digest: shotB, Mime: "image/jpeg", Width: 640, Height: 480,
			Size: 3, Source: store.SourceBrowser, CreatedAt: at}, Bytes: []byte("\xff\xd8\xff")}},
	}}
}

func withShots(f *fakeShots) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { s.Screenshots = f }
}

// getFull is a GET that keeps the response headers.
func getFull(t *testing.T, e env, path string, h http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func TestScreenshotList(t *testing.T) {
	e := setup(t, abc(t), withShots(shotsWorld()))
	code, body := get(t, e, "/api/rooms/"+roomID+"/screenshots", admin("boss"))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(got) != 1 || got[0]["run"] != "aaaa1111" || got[0]["n"] != float64(1) || got[0]["digest"] != shotA ||
		got[0]["mime"] != "image/png" || got[0]["width"] != float64(1280) || got[0]["height"] != float64(800) ||
		got[0]["bytes"] != float64(4) || got[0]["source"] != "browser" || got[0]["pageUrl"] != "http://127.0.0.1:3000/login" ||
		got[0]["vision"] != true {
		t.Fatalf("list %s", body)
	}
	if _, ok := got[0]["publishedAt"]; ok {
		t.Fatalf("an unpublished image carries publishedAt: %s", body)
	}
	if strings.Contains(body, "PNG") {
		t.Fatalf("the list carries bytes: %s", body)
	}

	t.Run("an empty room is an empty array", func(t *testing.T) {
		code, body := get(t, e, "/api/rooms/"+roomC+"/screenshots", admin("boss"))
		if code != http.StatusOK || body != "[]\n" {
			t.Fatalf("%d %q", code, body)
		}
	})
}

func TestScreenshotImage(t *testing.T) {
	e := setup(t, abc(t), withShots(shotsWorld()))
	r, body := getFull(t, e, "/api/rooms/"+roomID+"/screenshots/"+shotA, admin("boss"))
	if r.StatusCode != http.StatusOK || body != "\x89PNG" {
		t.Fatalf("%d %q", r.StatusCode, body)
	}
	for k, want := range map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Content-Disposition":     `inline; filename="` + roomID + `-aaaa1111-1.png"`,
		"Cache-Control":           "private, max-age=3600, immutable",
	} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// The image route takes the events' gate: a room the caller cannot read, and a digest another room
// holds, answer exactly as a missing room does (404, never 403).
func TestScreenshotGate(t *testing.T) {
	w := world(map[string][]string{"Smana/a": {"dev1"}}, "nolink")
	e := setup(t, abc(t), w.option(), withShots(shotsWorld()))
	_, missing := get(t, e, "/api/rooms/6kq7x2md/screenshots", header("dev1"))
	for _, c := range []struct{ name, path string }{
		{"the list of a room the caller cannot read", "/api/rooms/" + roomB + "/screenshots"},
		{"an image of a room the caller cannot read", "/api/rooms/" + roomB + "/screenshots/" + shotB},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, body := get(t, e, c.path, header("dev1")); code != http.StatusNotFound || body != missing {
				t.Fatalf("%d %q, want 404 %q", code, body, missing)
			}
		})
	}
	for _, c := range []struct{ name, path string }{
		{"another room's digest", "/api/rooms/" + roomID + "/screenshots/" + shotB},
		{"a digest that is not one", "/api/rooms/" + roomID + "/screenshots/..%2Fsummary"},
		{"an upper-case digest", "/api/rooms/" + roomID + "/screenshots/" + strings.ToUpper(shotA)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, body := get(t, e, c.path, header("dev1")); code != http.StatusNotFound {
				t.Fatalf("%d %q, want 404", code, body)
			}
		})
	}
	t.Run("a reader", func(t *testing.T) {
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/screenshots/"+shotA, header("dev1")); code != http.StatusOK {
			t.Fatalf("%d", code)
		}
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/screenshots", header("dev1")); code != http.StatusOK {
			t.Fatalf("%d", code)
		}
	})
	t.Run("no session", func(t *testing.T) {
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/screenshots/"+shotA, http.Header{}); code != http.StatusUnauthorized {
			t.Fatalf("%d", code)
		}
	})
}

func TestScreenshotsUnavailable(t *testing.T) {
	t.Run("no store serves no route", func(t *testing.T) {
		e := setup(t, abc(t))
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/screenshots", admin("boss")); code != http.StatusNotFound {
			t.Fatalf("%d", code)
		}
	})
	t.Run("a store error is 503", func(t *testing.T) {
		f := shotsWorld()
		f.err = io.ErrUnexpectedEOF
		e := setup(t, abc(t), withShots(f))
		for _, p := range []string{"/screenshots", "/screenshots/" + shotA} {
			if code, body := get(t, e, "/api/rooms/"+roomID+p, admin("boss")); code != http.StatusServiceUnavailable || strings.Contains(body, "EOF") {
				t.Fatalf("%s: %d %q", p, code, body)
			}
		}
	})
}
