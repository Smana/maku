// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Smana/maku/internal/browsergate"
	"github.com/Smana/maku/internal/httpx"
)

// The screenshot cap's default and hard ceiling (spec §5.3).
const (
	defaultMaxScreenshots = 10
	maxScreenshotsCeiling = 25
)

// browserGate is maku-browser-gate's configuration: its flags, MAX_SCREENSHOTS and VISION.
type browserGate struct {
	listen, upstream, count, shots, probe string
	max                                   int
	vision                                bool
}

// RunBrowserGate runs maku-browser-gate, the loopback MCP gate in front of the run's Playwright MCP
// server, until ctx ends. args are its flags: -listen (a loopback IP literal and port), -upstream
// (Playwright MCP's http loopback URL), -count (the screenshot count file) and -shots (the
// screenshot keep). MAX_SCREENSHOTS (1 to 25, default 10) is the run's cap and VISION (0 or 1)
// whether its model sees images. With -probe <url> it only checks that loopback URL answers 200: the
// kubelet's exec probe, since an httpGet probe dials the pod IP the gate never listens on.
func RunBrowserGate(ctx context.Context, log *slog.Logger, args []string, getenv func(string) string) error {
	cfg, err := browserGateConfig(args, getenv)
	if err != nil {
		return fmt.Errorf("maku-browser-gate: %w", err)
	}
	if cfg.probe != "" {
		return probeBrowserGate(ctx, cfg.probe)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("maku-browser-gate: %w", err)
	}
	return serveBrowserGate(ctx, log, cfg, ln)
}

func browserGateConfig(args []string, getenv func(string) string) (browserGate, error) {
	cfg := browserGate{max: defaultMaxScreenshots}
	fs := flag.NewFlagSet("maku-browser-gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.listen, "listen", "127.0.0.1:9223", "loopback address the harness reaches the gate on")
	fs.StringVar(&cfg.upstream, "upstream", "http://127.0.0.1:9224/mcp", "Playwright MCP's loopback URL")
	fs.StringVar(&cfg.count, "count", "/tmp/gate/count", "the run's screenshot count file")
	fs.StringVar(&cfg.shots, "shots", "/tmp/gate/shots", "the directory the gate keeps screenshots in")
	fs.StringVar(&cfg.probe, "probe", "", "check that this loopback URL answers 200, then exit")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.probe != "" {
		if err := browsergate.CheckURL(cfg.probe); err != nil {
			return cfg, fmt.Errorf("-probe %q: %w", cfg.probe, err)
		}
		return cfg, nil
	}
	// A literal loopback IP only: a name could resolve elsewhere, and an empty host is every interface.
	host, _, err := net.SplitHostPort(cfg.listen)
	if ip, perr := netip.ParseAddr(host); err != nil || perr != nil || !ip.IsLoopback() {
		return cfg, fmt.Errorf("-listen %q is not a loopback IP and port", cfg.listen)
	}
	if err := browsergate.CheckURL(cfg.upstream); err != nil {
		return cfg, fmt.Errorf("-upstream %q: %w", cfg.upstream, err)
	}
	if v := getenv("MAX_SCREENSHOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxScreenshotsCeiling {
			return cfg, fmt.Errorf("MAX_SCREENSHOTS %q is not between 1 and %d", v, maxScreenshotsCeiling)
		}
		cfg.max = n
	}
	switch v := getenv("VISION"); v {
	case "", "0":
	case "1":
		cfg.vision = true
	default:
		return cfg, fmt.Errorf("VISION %q is not 0 or 1", v)
	}
	return cfg, nil
}

// serveBrowserGate serves the gate on ln until ctx ends, then drains for up to 10 s.
func serveBrowserGate(ctx context.Context, log *slog.Logger, cfg browserGate, ln net.Listener) error {
	keep, err := browsergate.NewKeep(cfg.shots)
	if err != nil {
		_ = ln.Close()
		return err
	}
	counter := &browsergate.Counter{Path: cfg.count, Max: cfg.max}
	if err := mkdirFor(cfg.count); err != nil {
		_ = ln.Close()
		return err
	}
	// /mcp streams SSE, so the server has no WriteTimeout; NewProxy bounds /shots and /healthz itself.
	srv := &http.Server{
		Handler:           browsergate.NewProxy(cfg.upstream, counter, keep, cfg.vision, log),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("maku-browser-gate listening", "addr", ln.Addr().String(), "upstream", cfg.upstream, "max", cfg.max, "vision", cfg.vision)
	select {
	case err := <-errc:
		return fmt.Errorf("maku-browser-gate: %w", err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	serr := srv.Shutdown(dctx)
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return serr
}

// mkdirFor creates path's directory.
func mkdirFor(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("maku-browser-gate: %w", err)
	}
	return nil
}

// probeBrowserGate GETs url within 3 s and succeeds on 200 only.
func probeBrowserGate(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("maku-browser-gate probe: %w", err)
	}
	resp, err := httpx.New(3*time.Second, nil).Do(req)
	if err != nil {
		return fmt.Errorf("maku-browser-gate probe: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("maku-browser-gate probe: %s answered %d", url, resp.StatusCode)
	}
	return nil
}
