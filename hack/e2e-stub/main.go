// SPDX-License-Identifier: Apache-2.0

// Command e2e-stub emulates only the external APIs used by the spec-intake k3d test.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func newHTTPServer() *http.Server {
	return newServer(":8443", New().Handler())
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       time.Minute,
		WriteTimeout:      0, // Task 3's model route streams SSE.
		MaxHeaderBytes:    64 << 10,
	}
}

// newPlainServer is the same stub over plain http on E2E_STUB_HTTP_ADDR, or nil when it is unset: the
// model gateway reaches the stub as a provider over http (plan v4 step 4, Task 12), as it reaches the
// probe's echo provider, so the gateway needs no copy of the throwaway CA.
func newPlainServer(h http.Handler) *http.Server {
	addr := os.Getenv("E2E_STUB_HTTP_ADDR")
	if addr == "" {
		return nil
	}
	return newServer(addr, h)
}

func tlsFiles() (cert, key string, err error) {
	cert, key = os.Getenv("E2E_STUB_TLS_CERT_FILE"), os.Getenv("E2E_STUB_TLS_KEY_FILE")
	if cert == "" || key == "" {
		return "", "", errors.New("E2E_STUB_TLS_CERT_FILE and E2E_STUB_TLS_KEY_FILE are required")
	}
	return cert, key, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cert, key, err := tlsFiles()
	if err != nil {
		log.Error("e2e-stub TLS configuration", "err", err)
		os.Exit(1)
	}
	srv := newHTTPServer()
	if plain := newPlainServer(srv.Handler); plain != nil {
		go func() {
			if err := plain.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("e2e-stub plain http server", "err", err)
				os.Exit(1)
			}
		}()
	}
	if err := srv.ListenAndServeTLS(cert, key); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("e2e-stub server", "err", err)
		os.Exit(1)
	}
}
