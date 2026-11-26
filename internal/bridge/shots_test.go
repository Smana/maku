// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/wire"
)

// fakeGate is the browser gate's shots API: GET /shots?after=n and GET /shots/{n}.
type fakeGate struct {
	mu    sync.Mutex
	shots []Shot
	down  bool
	lists int
}

func (g *fakeGate) add(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.shots = append(g.shots, Shot{N: n, Mime: "image/jpeg", Bytes: 4, Width: 1280, Height: 800,
		URL: "http://127.0.0.1:8080/p" + strconv.Itoa(n), Vision: n%2 == 1})
}

func (g *fakeGate) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shots", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.lists++
		if g.down {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		out := []Shot{}
		for _, s := range g.shots {
			if s.N > after {
				out = append(out, s)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"shots": out})
	})
	mux.HandleFunc("GET /shots/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = fmt.Fprintf(w, "img%s", r.PathValue("n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeUploader is the broker's screenshot route: scripted replies per n, then 201.
type fakeUploader struct {
	mu       sync.Mutex
	uploaded []string // "n:body:url:vision"
	script   map[int][]Reply
}

func (u *fakeUploader) UploadScreenshot(_ context.Context, run string, s Shot, img []byte) (Reply, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if run != "7f3cq2xz" {
		return Reply{}, fmt.Errorf("run %q", run)
	}
	if rs := u.script[s.N]; len(rs) > 0 {
		u.script[s.N] = rs[1:]
		if rs[0].Code != http.StatusCreated {
			return rs[0], nil
		}
	}
	u.uploaded = append(u.uploaded, fmt.Sprintf("%d:%s:%s:%v", s.N, img, s.URL, s.Vision))
	return Reply{Code: http.StatusCreated}, nil
}

func (u *fakeUploader) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.uploaded)
}

func newRelay(gate string, up *fakeUploader) *ShotRelay {
	return &ShotRelay{Gate: gate, Broker: up, RunID: "7f3cq2xz", Every: 5 * time.Millisecond,
		MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, Logger: slog.New(slog.DiscardHandler)}
}

func waitShot(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Three shots are uploaded once each, in order, with their page and vision.
func TestTheRelayUploadsEachShotOnce(t *testing.T) {
	g := &fakeGate{}
	for n := 1; n <= 3; n++ {
		g.add(n)
	}
	up := &fakeUploader{}
	r := newRelay(g.start(t).URL, up)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitShot(t, "three uploads", func() bool { return len(up.got()) == 3 })
	// More polls upload nothing again.
	g.mu.Lock()
	lists := g.lists
	g.mu.Unlock()
	waitShot(t, "two more polls", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.lists >= lists+2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{"1:img1:http://127.0.0.1:8080/p1:true", "2:img2:http://127.0.0.1:8080/p2:false", "3:img3:http://127.0.0.1:8080/p3:true"}
	if got := up.got(); !slices.Equal(got, want) {
		t.Fatalf("uploaded %q, want %q", got, want)
	}
}

// A permanent refusal of shot 2 is dropped and shot 3 still goes; a retryable one is retried.
func TestTheRelayDropsAPermanentRefusalAndRetriesTheRest(t *testing.T) {
	g := &fakeGate{}
	for n := 1; n <= 3; n++ {
		g.add(n)
	}
	up := &fakeUploader{script: map[int][]Reply{
		1: {{Code: http.StatusServiceUnavailable}, {Code: http.StatusTooManyRequests, RetryAfter: "0"}},
		2: {{Code: http.StatusUnprocessableEntity, Reason: "not_an_image"}},
	}}
	var logs bytes.Buffer
	r := newRelay(g.start(t).URL, up)
	r.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"1:img1:http://127.0.0.1:8080/p1:true", "3:img3:http://127.0.0.1:8080/p3:true"}
	if got := up.got(); !slices.Equal(got, want) {
		t.Fatalf("uploaded %q, want %q", got, want)
	}
	if n := strings.Count(logs.String(), "screenshot dropped"); n != 1 {
		t.Fatalf("%d drop lines, want 1:\n%s", n, logs.String())
	}
}

// Flush after the harness ends uploads the shot taken since the last poll.
func TestFlushUploadsTheLastShot(t *testing.T) {
	g := &fakeGate{}
	g.add(1)
	up := &fakeUploader{}
	r := newRelay(g.start(t).URL, up)
	r.Every = time.Hour // the loop polls once, at start
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitShot(t, "the first upload", func() bool { return len(up.got()) == 1 })
	g.add(2)
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := up.got(); len(got) != 2 || !strings.HasPrefix(got[1], "2:") {
		t.Fatalf("uploaded %q", got)
	}
	cancel()
	<-done
}

// A gate not up yet is retried, not given up on.
func TestTheRelayWaitsForTheGate(t *testing.T) {
	g := &fakeGate{down: true}
	g.add(1)
	up := &fakeUploader{}
	r := newRelay(g.start(t).URL, up)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitShot(t, "a refused poll", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.lists >= 2 })
	g.mu.Lock()
	g.down = false
	g.mu.Unlock()
	waitShot(t, "the upload", func() bool { return len(up.got()) == 1 })
	cancel()
	<-done
}

// The gate must be on loopback over http: the relay refuses to start otherwise.
func TestTheRelayRefusesANonLoopbackGate(t *testing.T) {
	for _, gate := range []string{"http://10.0.0.1:9223", "https://127.0.0.1:9223", "http://example.com:9223", "", "file:///tmp/x",
		"http://user@127.0.0.1:9223"} {
		r := newRelay(gate, &fakeUploader{})
		if err := r.Run(t.Context()); err == nil {
			t.Errorf("Run(%q) started", gate)
		}
	}
	for _, gate := range []string{"http://127.0.0.1:9223", "http://[::1]:9223", "http://localhost:9223"} {
		if err := ValidGateURL(gate); err != nil {
			t.Errorf("ValidGateURL(%q): %v", gate, err)
		}
	}
}

// The broker client posts the image with the run, number, page and vision in the query.
func TestUploadScreenshotSendsTheShot(t *testing.T) {
	var got struct {
		q    url.Values
		body []byte
		ct   string
	}
	fb := &fakeBroker{}
	srv, ca := fb.startWith(t, func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/bridge/screenshots", func(w http.ResponseWriter, r *http.Request) {
			got.q, got.ct = r.URL.Query(), r.Header.Get("Content-Type")
			got.body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		})
	})
	b, err := NewBroker(srv.URL, writeToken(t, t.TempDir(), "v1"), ca)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := b.UploadScreenshot(t.Context(), "7f3cq2xz", Shot{N: 2, Mime: "image/png", URL: "http://127.0.0.1:8080/a?b=c", Vision: true}, []byte("png"))
	if err != nil || rep.Code != http.StatusCreated {
		t.Fatalf("%+v %v", rep, err)
	}
	if got.q.Get("run") != "7f3cq2xz" || got.q.Get("n") != "2" || got.q.Get("url") != "http://127.0.0.1:8080/a?b=c" ||
		got.q.Get("vision") != "1" || string(got.body) != "png" || got.ct != "image/png" {
		t.Fatalf("%+v %q %q", got.q, got.body, got.ct)
	}
}

// A final read flushes the gate's screenshots too, after the log: the shot taken just before the
// harness ended reaches the room.
func TestAFinalReadFlushesTheScreenshots(t *testing.T) {
	f := &fakeAgentServer{pageSize: 100, status: "running"}
	f.add(chatEvent("hi"))
	fb := &fakeBroker{}
	r := newRig(t, NewHarness(f.start(t, conv).URL, conv), fb)
	r.b.Interval = time.Hour
	g := &fakeGate{}
	up := &fakeUploader{}
	r.b.Shots = newRelay(g.start(t).URL, up)
	r.b.Shots.Every = time.Hour
	ctx, _ := r.run(t)
	eventually(ctx, t, "the first step mirrors the log", func() bool { return len(fb.stored(wire.StreamEvents)) == 1 })
	g.add(1)
	if _, err := r.b.FinalRead(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := up.got(); len(got) != 1 || !strings.HasPrefix(got[0], "1:") {
		t.Fatalf("uploaded %q", got)
	}
}
