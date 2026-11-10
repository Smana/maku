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
	if err := waitServable(t.Context(), slog.New(slog.DiscardHandler), gate, sync, nil, time.Millisecond, 4*time.Millisecond); err != nil {
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
		func(context.Context) error { calls++; return boom }, nil, time.Hour, time.Hour)
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
		}, func() []string { return []string{"Repository a: Invalid"} }, time.Hour, time.Hour)
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
	err := waitServable(ctx, slog.New(slog.DiscardHandler), &startupGate{}, sync, refusals(src), time.Millisecond, 2*time.Millisecond)
	if !errors.Is(err, errStoppedWaiting) || calls < 2 {
		t.Fatalf("err %v after %d Syncs, want a wait ended by the deadline", err, calls)
	}
}
