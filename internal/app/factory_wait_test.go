// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
)

func probe(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

// While no config can be served the factory waits: alive and started, never ready, and /readyz
// says why. Once a Sync serves, the start goes on and readiness is the caches' again.
func TestWaitServableRetriesUntilServed(t *testing.T) {
	gate := &startupGate{}
	h := gate.handler(factoryOps(gate.Synced, func() bool { return false }, http.NotFoundHandler()))
	notServable := fmt.Errorf("%w: config: repository is required", configsource.ErrNotServable)
	calls := 0
	sync := func(context.Context) error {
		calls++
		if calls <= 3 {
			// Each failed attempt is probed before the next one: alive, started, not ready.
			if calls > 1 {
				for path, want := range map[string]int{"/healthz": 200, "/startupz": 200, "/readyz": 503} {
					if code, _ := probe(t, h, path); code != want {
						t.Errorf("attempt %d: GET %s = %d, want %d", calls, path, code, want)
					}
				}
				if _, body := probe(t, h, "/readyz"); !strings.Contains(body, "waiting for a servable config") ||
					!strings.Contains(body, "repository is required") {
					t.Errorf("/readyz must name the wait and its reason: %q", body)
				}
			}
			return notServable
		}
		return nil
	}
	if err := waitServable(t.Context(), slog.New(slog.DiscardHandler), gate, sync, nil, time.Millisecond, 4*time.Millisecond, time.Hour); err != nil {
		t.Fatalf("a Sync that serves ends the wait: %v", err)
	}
	if calls != 4 {
		t.Fatalf("Sync ran %d times, want 4", calls)
	}
	if code, body := probe(t, h, "/readyz"); code != 503 || strings.Contains(body, "servable") {
		t.Fatalf("served, before the caches synced: /readyz = %d %q, want 503 watch not synced", code, body)
	}
	gate.serve(func(context.Context) bool { return true })
	if code, _ := probe(t, h, "/readyz"); code != 200 {
		t.Fatalf("caches synced: /readyz = %d, want 200", code)
	}
}

// Any other error, a Forbidden list or an unreachable API server, fails the start at once.
func TestWaitServableOtherErrorFailsAtOnce(t *testing.T) {
	boom := errors.New("list Factory: forbidden")
	calls := 0
	err := waitServable(t.Context(), slog.New(slog.DiscardHandler), &startupGate{},
		func(context.Context) error { calls++; return boom }, nil, time.Hour, time.Hour, time.Hour)
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err %v after %d calls, want %v after 1", err, calls, boom)
	}
}

// SIGTERM during the wait ends it, with errStoppedWaiting so the start exits cleanly.
func TestWaitServableStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- waitServable(ctx, slog.New(slog.DiscardHandler), &startupGate{}, func(context.Context) error {
			return configsource.ErrNotServable
		}, func() waitState { return waitState{refused: []string{"Repository a: Invalid"}} }, time.Hour, time.Hour, time.Hour)
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errStoppedWaiting) {
			t.Fatalf("err %v, want errStoppedWaiting", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait ignored the cancel")
	}
}

// plumbingOnly is a file after migration with its resources not applied yet: nothing to serve.
const plumbingOnly = `factory: aws-0
broker: {url: "https://room-broker.agent-system.svc.cluster.local:8443", tokenFile: /var/run/secrets/agents/rooms/token}
github: {appIDFile: /etc/agent-factory-github/app_id, privateKeyFile: /etc/agent-factory-github/private_key}
`

// A file no resource can make servable fails the start at once, before any cluster call: the
// wait is for the resources only.
func TestFirstSyncMalformedFileFailsAtOnce(t *testing.T) {
	for name, body := range map[string]string{
		"not YAML":            "factory: [aws-0\n",
		"unknown key":         plumbingOnly + "brokr: {}\n",
		"block written twice": plumbingOnly + "hardening: {mode: off}\nhardening: {mode: off}\n",
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent")) // a cluster call would fail differently
			_, _, err := firstSync(t.Context(), slog.New(slog.DiscardHandler), path, "agent-system", &startupGate{})
			if err == nil || !strings.HasPrefix(err.Error(), "config: ") {
				t.Fatalf("err %v, want the file's config error", err)
			}
		})
	}
}

// A plumbing-only file with no resources passes the shape check and waits.
func TestPlumbingOnlyFileWaits(t *testing.T) {
	if err := config.CheckShape([]byte(plumbingOnly)); err != nil {
		t.Fatalf("a plumbing-only file has a valid shape: %v", err)
	}
	s := runtime.NewScheme()
	if err := factoryv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	src := &configsource.Source{Reader: fake.NewClientBuilder().WithScheme(s).Build(), Namespace: "agent-system",
		File: []byte(plumbingOnly), Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	calls := 0
	sync := func(ctx context.Context) error { calls++; return src.Sync(ctx) }
	err := waitServable(ctx, slog.New(slog.DiscardHandler), &startupGate{}, sync, waitStateOf(src), time.Millisecond, 2*time.Millisecond, time.Hour)
	if !errors.Is(err, errStoppedWaiting) || calls < 2 {
		t.Fatalf("err %v after %d Syncs, want a wait ended by the deadline", err, calls)
	}
}

// syncLog is a logger writing to a buffer, to count the wait's warnings.
func syncLog() (*slog.Logger, *strings.Builder) {
	var sb strings.Builder
	return slog.New(slog.NewTextHandler(&sb, nil)), &sb
}

func notServableAfter(n int, calls *int) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		if *calls <= n {
			return configsource.ErrNotServable
		}
		return nil
	}
}

// The warning goes out once per interval however often Sync retries; a shorter interval warns again.
func TestWaitServableWarnsOncePerInterval(t *testing.T) {
	for name, tc := range map[string]struct {
		every time.Duration
		want  int
	}{"a minute apart": {time.Minute, 1}, "every attempt": {0, 6}} {
		t.Run(name, func(t *testing.T) {
			log, sb := syncLog()
			calls := 0
			if err := waitServable(t.Context(), log, &startupGate{}, notServableAfter(6, &calls), nil, time.Millisecond, time.Millisecond, tc.every); err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(sb.String(), "waiting for a servable config"); got != tc.want {
				t.Fatalf("%d warnings over %d attempts, want %d", got, calls, tc.want)
			}
		})
	}
}

// A first Sync that serves starts at once: one call, no warning, no wait left on /readyz.
func TestWaitServableStartsAtOnce(t *testing.T) {
	log, sb := syncLog()
	gate := &startupGate{}
	calls := 0
	if err := waitServable(t.Context(), log, gate, notServableAfter(0, &calls), nil, time.Hour, time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || sb.Len() != 0 || gate.waiting.Load() != nil {
		t.Fatalf("calls %d, log %q, waiting %v: want 1 call, silent, not waiting", calls, sb.String(), gate.waiting.Load())
	}
}

// With the CRDs not installed the warning is about the file, not about applying resources.
func TestWaitServableWordingFollowsCRDs(t *testing.T) {
	for name, tc := range map[string]struct{ absent, want string }{
		"CRDs installed": {"", "apply the factory's Factory, Repository and MergePolicy resources"},
		"CRDs absent":    {"y", "fix the file"},
	} {
		t.Run(name, func(t *testing.T) {
			log, sb := syncLog()
			calls := 0
			state := func() waitState { return waitState{crdsAbsent: tc.absent != ""} }
			if err := waitServable(t.Context(), log, &startupGate{}, notServableAfter(1, &calls), state, time.Millisecond, time.Millisecond, time.Minute); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sb.String(), tc.want) {
				t.Fatalf("warning %q lacks %q", sb.String(), tc.want)
			}
		})
	}
}

// A cancel while a Sync is in flight surfaces as that Sync's context error: still a clean stop.
func TestWaitServableCancelDuringSync(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	err := waitServable(ctx, slog.New(slog.DiscardHandler), &startupGate{}, func(ctx context.Context) error {
		cancel()
		return fmt.Errorf("list Factory: %w", ctx.Err())
	}, nil, time.Hour, time.Hour, time.Hour)
	if !errors.Is(err, errStoppedWaiting) {
		t.Fatalf("err %v, want errStoppedWaiting", err)
	}
}

// An API server that refuses the list fails the start at once, with the Role rule it needs.
func TestFirstSyncForbiddenNamesTheRole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"forbidden"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	body := "apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: " + srv.URL + "}}]\n" +
		"users: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n"
	if err := os.WriteFile(kubeconfig, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(plumbingOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	_, _, err := firstSync(t.Context(), slog.New(slog.DiscardHandler), cfg, "agent-system", &startupGate{})
	if err == nil || !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "Role in agent-system must grant get, list and watch") {
		t.Fatalf("err %v, want a Forbidden with the Role hint", err)
	}
}
