// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
)

// HR35: the broker posts the run and target to the factory with its projected token, re-read at
// each call, and a 409 is the tool error that names the filter off.
func TestForgeClient(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/7f3cq2xz/forge-read" || string(b) != `{"target":"checks"}` ||
			r.Header.Get("Authorization") != "Bearer projected-token" {
			t.Errorf("%s %s %s %q", r.Method, r.URL.Path, b, r.Header.Get("Authorization"))
		}
		w.WriteHeader(code)
		if code == http.StatusOK {
			_ = json.NewEncoder(w).Encode(hardening.ForgeReadResult{Items: []hardening.ForgeItem{{Kind: "check", Name: "lint", Trust: hardening.Approved}},
				InputTrust: hardening.InputTrust{Min: hardening.Approved}})
		} else {
			_, _ = io.WriteString(w, `{"error":"read_filter_off"}`)
		}
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := ForgeClient{FactoryURL: srv.URL + "/", TokenFile: tok, HTTP: func() (*http.Client, error) { return srv.Client(), nil }}
	res, err := c.ForgeRead(t.Context(), "7f3cq2xz", "checks")
	if err != nil || len(res.Items) != 1 || res.Items[0].Name != "lint" || res.InputTrust.Min != hardening.Approved {
		t.Fatalf("%+v %v", res, err)
	}
	code = http.StatusConflict
	if _, err := c.ForgeRead(t.Context(), "7f3cq2xz", "checks"); !errors.Is(err, ErrReadFilterOff) ||
		err.Error() != "read_filter_off: forge_read is not active for this repository" {
		t.Fatalf("409: %v", err)
	}
	code = http.StatusForbidden
	if _, err := c.ForgeRead(t.Context(), "7f3cq2xz", "checks"); err == nil || errors.Is(err, ErrReadFilterOff) {
		t.Fatalf("403: %v", err)
	}
	c.TokenFile = filepath.Join(t.TempDir(), "absent")
	if _, err := c.ForgeRead(t.Context(), "7f3cq2xz", "checks"); err == nil {
		t.Fatal("no token, yet a read")
	}
}

// REL-H5: ForgeRead wired to runrequest.Factory.HTTPClient reuses the cached TLS transport and
// keep-alive connection across calls instead of leaking a fresh http.Transport per read.
func TestForgeClientReusesFactoryTransport(t *testing.T) {
	dir := t.TempDir()
	tlsreloadtest.WritePair(t, dir, 1, time.Now().Add(-time.Minute))
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	remotes := map[string]struct{}{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(hardening.ForgeReadResult{})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := runrequest.Factory{URL: srv.URL, CAFile: filepath.Join(dir, "tls.crt")}
	c := ForgeClient{FactoryURL: srv.URL, TokenFile: tok, HTTP: f.HTTPClient}
	for i := range 5 {
		if _, err := c.ForgeRead(t.Context(), "7f3cq2xz", "checks"); err != nil {
			t.Fatalf("ForgeRead %d: %v", i, err)
		}
	}
	mu.Lock()
	conns := len(remotes)
	mu.Unlock()
	if conns != 1 {
		t.Fatalf("5 sequential ForgeRead calls opened %d TCP/TLS connections, want 1 (transport reuse)", conns)
	}
}
