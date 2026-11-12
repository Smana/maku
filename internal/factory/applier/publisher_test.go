// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/httpx"
)

// The GitHub adapter is the publisher and the filer; the fake stands for both in the tests.
var (
	_ Publisher = (*forge.GitHub)(nil)
	_ Filer     = (*forge.GitHub)(nil)
	_ Publisher = (*forgetest.Fake)(nil)
	_ Filer     = (*forgetest.Fake)(nil)
)

// The publisher's connection: octo-sts mints its token under the publisher identity for the
// repository (HR11), one connection per repository whatever its spelling, and every API call
// carries that token.
func TestConnectPublishers(t *testing.T) {
	var mu sync.Mutex
	var exchanges, authed []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/sts/exchange":
			exchanges = append(exchanges, r.URL.Query().Get("scope")+" "+r.URL.Query().Get("identity"))
			_, _ = io.WriteString(w, `{"token":"ghs_publisher"}`)
		default:
			authed = append(authed, r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{"resources":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	hc := httpx.New(5*time.Second, roots)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("sa"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens := &OctoSTSTokens{URL: srv.URL, TokenFile: file, HTTP: hc, Now: time.Now}
	connect := ConnectPublishers(tokens, forge.Options{APIURL: srv.URL + "/", UserAgent: "agent-factory/test", HTTP: hc, Now: time.Now})
	a, err := connect(t.Context(), "Owner/Repo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := connect(t.Context(), "owner/repo")
	if err != nil || a != b {
		t.Fatalf("a second spelling made another connection: %v", err)
	}
	if err := a.(*forge.GitHub).Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exchanges) != 1 || exchanges[0] != "Owner/Repo publisher" {
		t.Errorf("exchanges %q", exchanges)
	}
	for _, h := range authed {
		if h != "Bearer ghs_publisher" {
			t.Errorf("an API call carried %q", h)
		}
	}
	if _, err := (&OctoSTSTokens{}).For("owner/repo"); err == nil {
		t.Error("an unconfigured octo-sts gave a token source")
	}
}
