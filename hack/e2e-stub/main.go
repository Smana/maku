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
	return &http.Server{
		Addr:              ":8443",
		Handler:           New().Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       time.Minute,
		WriteTimeout:      0, // Task 3's model route streams SSE.
		MaxHeaderBytes:    64 << 10,
	}
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
	if err := newHTTPServer().ListenAndServeTLS(cert, key); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("e2e-stub server", "err", err)
		os.Exit(1)
	}
}
