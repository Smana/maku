// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// The :8443 listener's bounds (Ruling AC).
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 16 << 10
)

// httpServer is the :8443 listener with its bounds. WriteTimeout is the one
// exception, 0: the SSE stream is a response that never ends, so each
// response gets a write deadline in Routes, which the stream replaces with one per
// write (streamWriteWait), disarmed while the stream is idle.
func (s *Server) httpServer(addr string, cfg *tls.Config) *http.Server {
	srv := &http.Server{Addr: addr, Handler: s.Routes(), TLSConfig: cfg,
		ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: 0,
		IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderBytes,
		ErrorLog: slog.NewLogLogger(s.log().Handler(), slog.LevelWarn)}
	srv.RegisterOnShutdown(s.closeStreams)
	return srv
}

// ListenAndServeTLS serves the API on addr with cfg (tlsreload.Config's) until ctx ends,
// then drains for at most drain. Streams end at once on shutdown: their bridges
// re-dial another replica.
func (s *Server) ListenAndServeTLS(ctx context.Context, addr string, cfg *tls.Config, drain time.Duration) error {
	if s.Log == nil || s.Redactor == nil || s.Runs == nil || s.Systems == nil || s.Watch == nil {
		// Unchecked, each would panic on every request instead (review B4).
		return errors.New("bridgeapi: Log, Redactor, Runs, Systems and Watch are required")
	}
	srv := s.httpServer(addr, cfg)
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServeTLS("", "") }()
	select {
	case err := <-served:
		return fmt.Errorf("bridgeapi: serve %s: %w", addr, err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shut := srv.Shutdown(dctx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		shut = errors.Join(shut, err)
	}
	if shut != nil {
		return fmt.Errorf("bridgeapi: shut down %s: %w", addr, shut)
	}
	return nil
}
