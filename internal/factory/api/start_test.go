// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
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

// probed is what a probe keeps of a response whose body it has drained and closed.
type probed struct {
	status int
	tls    *tls.ConnectionState
}

// postStatus POSTs an empty JSON body to url and returns the status and TLS state it got.
func postStatus(t *testing.T, c *http.Client, url string) (probed, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return probed{}, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return probed{resp.StatusCode, resp.TLS}, nil
}

// startOn opens s's listener and serves until the test ends; it returns the bound address.
func startOn(t *testing.T, s *Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ln, err := s.listen(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, s.httpServer(), ln, time.Second) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("drain: %v", err)
		}
	})
	return ln.Addr().String()
}

// S-1, D6: with api.tls the API speaks TLS 1.3 only, from the mounted pair, and a plain request
// never reaches a handler.
func TestTheAPIServesTLSWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	cert := tlsreloadtest.WritePair(t, dir, 1, time.Now())
	addr := startOn(t, &Server{Auth: byToken{}, Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0",
		TLS: &config.ServerTLS{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}}}})
	roots := x509.NewCertPool()
	roots.AddCert(cert)

	resp, err := postStatus(t, httpx.New(5*time.Second, roots), "https://"+addr+"/v1/runs")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	if resp.status != http.StatusUnauthorized || resp.tls == nil || resp.tls.Version != tls.VersionTLS13 {
		t.Fatalf("https: %d, TLS %+v", resp.status, resp.tls)
	}
	// The TLS listener answers a plain request with 400 and never reaches a handler (a 401
	// would mean it did); a connection error proves nothing, so it fails too.
	plain, err := postStatus(t, httpx.New(5*time.Second, nil), "http://"+addr+"/v1/runs")
	if err != nil || plain.status != http.StatusBadRequest {
		t.Fatalf("plain http: %v, %v: want the 400 answer", plain, err)
	}
	d := tls.Dialer{Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}}
	if c, err := d.DialContext(t.Context(), "tcp", addr); err == nil {
		_ = c.Close()
		t.Fatal("a TLS 1.2 handshake succeeded: the API's minimum is 1.3")
	}
}

// Without api.tls the API serves plaintext, as every deployment before S-1 (D6: TLS is optional).
func TestTheAPIServesPlaintextWithoutTLS(t *testing.T) {
	addr := startOn(t, &Server{Auth: byToken{}, Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0"}}})
	resp, err := postStatus(t, httpx.New(5*time.Second, nil), "http://"+addr+"/v1/runs")
	if err != nil || resp.status != http.StatusUnauthorized {
		t.Fatalf("plaintext: %+v %v", resp, err)
	}
}

// A pair that does not load fails the start, naming api.tls: the pod crash-loops with the reason
// rather than serving plaintext.
func TestAMissingAPIPairFailsTheStart(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0",
		TLS: &config.ServerTLS{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}}}}
	if ln, err := s.listen(t.Context()); err == nil || !strings.Contains(err.Error(), "api.tls") {
		if ln != nil {
			_ = ln.Close()
		}
		t.Fatalf("err = %v, want one naming api.tls", err)
	}
}
