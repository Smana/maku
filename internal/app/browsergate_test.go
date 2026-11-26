// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

func TestBrowserGateConfig(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string // an error substring, "" for success
	}{
		{"the defaults", nil, nil, ""},
		{"an IPv6 loopback listener", []string{"-listen", "[::1]:9223"}, nil, ""},
		{"a listener on every interface", []string{"-listen", ":9223"}, nil, "loopback"},
		{"a listener on a pod address", []string{"-listen", "10.0.0.4:9223"}, nil, "loopback"},
		{"a listener by name", []string{"-listen", "localhost:9223"}, nil, "loopback"},
		{"an upstream off loopback", []string{"-upstream", "http://10.0.0.4:9224/mcp"}, nil, "upstream"},
		{"an https upstream", []string{"-upstream", "https://127.0.0.1:9224/mcp"}, nil, "upstream"},
		{"a cap of 25", nil, map[string]string{"MAX_SCREENSHOTS": "25"}, ""},
		{"a cap over the ceiling", nil, map[string]string{"MAX_SCREENSHOTS": "26"}, "MAX_SCREENSHOTS"},
		{"a cap of zero", nil, map[string]string{"MAX_SCREENSHOTS": "0"}, "MAX_SCREENSHOTS"},
		{"vision on", nil, map[string]string{"VISION": "1"}, ""},
		{"vision as a word", nil, map[string]string{"VISION": "yes"}, "VISION"},
		{"an unknown flag", []string{"-token", "x"}, nil, "token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := browserGateConfig(c.args, env(c.env))
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
	cfg, _ := browserGateConfig(nil, env(nil))
	if cfg.listen != "127.0.0.1:9223" || cfg.upstream != "http://127.0.0.1:9224/mcp" || cfg.max != 10 || cfg.vision ||
		cfg.count != "/tmp/gate/count" || cfg.shots != "/tmp/gate/shots" {
		t.Errorf("defaults %+v", cfg)
	}
}

func TestServeBrowserGateStopsWithItsContext(t *testing.T) {
	dir := t.TempDir()
	cfg := browserGate{listen: "127.0.0.1:0", upstream: "http://127.0.0.1:9/mcp", count: filepath.Join(dir, "count"),
		shots: filepath.Join(dir, "shots"), max: 10}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", cfg.listen)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveBrowserGate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, ln) }()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	srv := httpx.New(5*time.Second, nil)
	resp, err := srv.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("did not stop")
	}
}
