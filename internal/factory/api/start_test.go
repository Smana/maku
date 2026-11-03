// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
)

// F-7: serve returns only once Shutdown has, so a handler between its Task's create and its issue
// finishes before the process exits. Before, Start returned on ErrServerClosed while the handler
// was still writing, and the manager let the process go.
func TestServeReturnsOnlyAfterTheInFlightHandler(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var wrote atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "filed")
		wrote.Store(true)
	}), ReadHeaderTimeout: time.Second}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			body <- "request: " + err.Error()
			return
		}
		resp, err := httpx.New(5*time.Second, nil).Do(req)
		if err != nil {
			body <- "do: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("serve returned (%v) while a handler was still writing", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !wrote.Load() {
		t.Fatal("serve returned before the handler finished")
	}
	if got := <-body; got != "filed" {
		t.Fatalf("the client read %q", got)
	}
}

// AGENTS.md: every http.Server sets its five bounds.
func TestTheAPIListenerSetsItsBounds(t *testing.T) {
	srv := (&Server{Cfg: &config.Config{}}).httpServer()
	for name, v := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if v <= 0 {
			t.Errorf("%s unset", name)
		}
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes unset")
	}
}
