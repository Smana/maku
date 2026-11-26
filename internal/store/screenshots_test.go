// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

const shotRun = "7f3cq2xz"

func shot(run string, n int, b []byte) Screenshot {
	return Screenshot{ScreenshotMeta: ScreenshotMeta{RunID: run, N: n, Mime: "image/png", Width: 1280, Height: 800,
		Source: SourceBrowser, PageURL: "http://127.0.0.1:8080/", Vision: true}, Bytes: b}
}

// C13: a screenshot is stored once per (room, run, n), read back by its digest, and listed without
// its bytes.
func TestPutScreenshot(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	b := []byte("png bytes")
	m, created, err := s.PutScreenshot(ctx, room, shot(shotRun, 1, b))
	if err != nil || !created {
		t.Fatalf("put: created %v, %v", created, err)
	}
	if m.Digest != digestOf(b) || m.Size != len(b) || m.N != 1 || m.RunID != shotRun || m.CreatedAt.IsZero() {
		t.Fatalf("meta %+v", m)
	}
	again, created, err := s.PutScreenshot(ctx, room, shot(shotRun, 1, b))
	if err != nil || created || again.Digest != m.Digest {
		t.Fatalf("an idempotent repeat: created %v, %+v, %v", created, again, err)
	}
	if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, 1, []byte("other"))); !errors.Is(err, ErrScreenshotConflict) {
		t.Fatalf("another image under the same n: %v, want ErrScreenshotConflict", err)
	}
	// An unchanged page shot twice is the same image under two numbers.
	if _, created, err := s.PutScreenshot(ctx, room, shot(shotRun, 2, b)); err != nil || !created {
		t.Fatalf("the same image as n=2: %v %v", created, err)
	}
	if got := query(t, super, `SELECT count(*)::text FROM run_screenshots`); got != "2" {
		t.Fatalf("%s rows, want 2", got)
	}
	got, err := s.Screenshot(ctx, room, m.Digest)
	if err != nil || !bytes.Equal(got.Bytes, b) || got.Mime != "image/png" || got.PageURL != "http://127.0.0.1:8080/" || !got.Vision {
		t.Fatalf("Screenshot = %+v, %v", got, err)
	}
	if _, err := s.Screenshot(ctx, "abcdefgh", m.Digest); !errors.Is(err, ErrNoScreenshot) {
		t.Fatalf("another room: %v", err)
	}
	list, err := s.Screenshots(ctx, room)
	if err != nil || len(list) != 2 || list[0].N != 1 || list[1].N != 2 {
		t.Fatalf("Screenshots = %+v, %v", list, err)
	}
	req := Screenshot{ScreenshotMeta: ScreenshotMeta{N: 1, Mime: "image/jpeg", Width: 1568, Height: 1000, Source: SourceRequest},
		Bytes: []byte("issue image")}
	if _, _, err := s.PutScreenshot(ctx, room, req); err != nil {
		t.Fatalf("a request image has no run: %v", err)
	}
	for name, tc := range map[string]struct {
		room string
		sh   Screenshot
		want error
	}{
		"empty":                    {room, shot(shotRun, 3, nil), ErrBadScreenshot},
		"over 1 MiB":               {room, shot(shotRun, 3, bytes.Repeat([]byte("x"), MaxScreenshot+1)), ErrBadScreenshot},
		"a gif":                    {room, func() Screenshot { x := shot(shotRun, 3, b); x.Mime = "image/gif"; return x }(), ErrBadScreenshot},
		"n zero":                   {room, shot(shotRun, 0, b), ErrBadScreenshot},
		"a browser shot of no run": {room, shot("", 3, b), ErrBadScreenshot},
		"no such room":             {"zzzzzzzz", shot(shotRun, 3, b), ErrNoRoom},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := s.PutScreenshot(ctx, tc.room, tc.sh); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("a sealed room", func(t *testing.T) {
		if err := s.CloseRoom(ctx, room, "test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, 3, []byte("late"))); !errors.Is(err, ErrSealed) {
			t.Fatalf("err = %v, want ErrSealed", err)
		}
	})
}

// Phase B caps: 25 per run, 100 per room, 64 MiB per room.
func TestPutScreenshotQuota(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	for n := 1; n <= MaxScreenshotsPerRun; n++ {
		if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, n, fmt.Appendf(nil, "shot %d", n))); err != nil {
			t.Fatalf("put %d: %v", n, err)
		}
	}
	// A repeat at the quota is still the idempotent answer.
	if _, created, err := s.PutScreenshot(ctx, room, shot(shotRun, 25, []byte("shot 25"))); err != nil || created {
		t.Fatalf("repeat at quota: %v %v", created, err)
	}
	if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, 26, []byte("shot 26"))); !errors.Is(err, ErrScreenshotQuota) {
		t.Fatalf("the 26th: %v, want ErrScreenshotQuota", err)
	}
	// Three more runs fill the room's 100.
	for i, run := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"} {
		for n := 1; n <= MaxScreenshotsPerRun; n++ {
			if _, _, err := s.PutScreenshot(ctx, room, shot(run, n, fmt.Appendf(nil, "run %d shot %d", i, n))); err != nil {
				t.Fatalf("run %s put %d: %v", run, n, err)
			}
		}
	}
	if _, _, err := s.PutScreenshot(ctx, room, shot("dddddddd", 1, []byte("the 101st"))); !errors.Is(err, ErrScreenshotQuota) {
		t.Fatalf("the room's 101st: %v, want ErrScreenshotQuota", err)
	}
	// The byte quota, in a room of its own: 64 images of 1 MiB fill it.
	const big = "bigshots"
	if _, err := s.EnsureRoom(ctx, NewRoom{ID: big, Driver: "system:factory", Retention: 90 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	img := bytes.Repeat([]byte("a"), MaxScreenshot)
	for i := range MaxScreenshotBytesPerRoom / MaxScreenshot {
		run := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"}[i/MaxScreenshotsPerRun]
		b := append([]byte{byte(i)}, img[1:]...)
		if _, _, err := s.PutScreenshot(ctx, big, shot(run, i%MaxScreenshotsPerRun+1, b)); err != nil {
			t.Fatalf("byte fill %d: %v", i, err)
		}
	}
	if _, _, err := s.PutScreenshot(ctx, big, shot("dddddddd", 1, []byte("x"))); !errors.Is(err, ErrScreenshotQuota) {
		t.Fatalf("over the room's bytes: %v, want ErrScreenshotQuota", err)
	}
}

// The broker can mark a screenshot published, and nothing else: no rewrite, no delete.
func TestScreenshotGrants(t *testing.T) {
	ctx := t.Context()
	s, broker, retention, _ := open(t)
	if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, 1, []byte("png"))); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, broker, `UPDATE run_screenshots SET published_at = now()`); err != nil {
		t.Fatalf("the broker could not publish: %v", err)
	}
	for name, sql := range map[string]string{
		"a DELETE":          `DELETE FROM run_screenshots`,
		"a bytes UPDATE":    `UPDATE run_screenshots SET bytes = 'x', size = 1`,
		"a page_url UPDATE": `UPDATE run_screenshots SET page_url = 'http://evil/'`,
	} {
		if err := run(ctx, broker, sql); err == nil {
			t.Fatalf("the broker role ran %s", name)
		}
	}
	if err := run(ctx, retention, `SELECT bytes FROM run_screenshots`); err == nil {
		t.Fatal("the retention role read a screenshot")
	}
}

// OQ4: screenshots go with their room, when retention purges it.
func TestPurgeExpiredDeletesScreenshots(t *testing.T) {
	ctx := t.Context()
	s, _, retention, super := open(t)
	if _, _, err := s.PutScreenshot(ctx, room, shot(shotRun, 1, []byte("png"))); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseRoom(ctx, room, "done"); err != nil {
		t.Fatal(err)
	}
	forge(t, super, `UPDATE rooms SET closed_at = now() - interval '91 days' WHERE room_id = '`+room+`'`)
	r, err := Open(ctx, retention, RoleRetention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, _, err := r.PurgeExpired(ctx); err != nil || n != 1 {
		t.Fatalf("purged %d rooms (%v), want 1", n, err)
	}
	if got := query(t, super, `SELECT count(*)::text FROM run_screenshots`); got != "0" {
		t.Fatalf("%s screenshots left, want 0", got)
	}
}
