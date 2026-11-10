// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"
)

func TestHTTPServerBounds(t *testing.T) {
	srv := newHTTPServer()
	if srv.Addr != ":8443" || srv.Handler == nil {
		t.Fatalf("server address/handler = %q/%v", srv.Addr, srv.Handler)
	}
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 15*time.Second || srv.IdleTimeout != time.Minute {
		t.Fatalf("server timeouts = header %v, read %v, idle %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, must not cut off model streams", srv.WriteTimeout)
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}

func TestTLSFilesFromEnvironment(t *testing.T) {
	t.Setenv("E2E_STUB_TLS_CERT_FILE", "/tls/tls.crt")
	t.Setenv("E2E_STUB_TLS_KEY_FILE", "/tls/tls.key")
	cert, key, err := tlsFiles()
	if err != nil || cert != "/tls/tls.crt" || key != "/tls/tls.key" {
		t.Fatalf("tlsFiles() = %q, %q, %v", cert, key, err)
	}
}

func TestTLSFilesRequired(t *testing.T) {
	for _, tc := range []struct{ cert, key string }{{"", "/tls/tls.key"}, {"/tls/tls.crt", ""}, {"", ""}} {
		t.Setenv("E2E_STUB_TLS_CERT_FILE", tc.cert)
		t.Setenv("E2E_STUB_TLS_KEY_FILE", tc.key)
		if _, _, err := tlsFiles(); err == nil {
			t.Fatalf("tlsFiles() accepted cert %q key %q", tc.cert, tc.key)
		}
	}
}
