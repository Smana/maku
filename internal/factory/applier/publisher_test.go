// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/httpx"
)

// The GitHub adapter is the publisher and the filer; the fake stands for both in the tests.
var (
	_ Publisher = (*forge.GitHub)(nil)
	_ Filer     = (*forge.GitHub)(nil)
	_ Publisher = (*forgetest.Fake)(nil)
	_ Filer     = (*forgetest.Fake)(nil)
)

// staticTokens is a PublisherTokens whose sources answer one token.
type staticTokens struct{}

type staticSource string

func (s staticSource) Token(context.Context) (string, error) { return string(s), nil }

func (staticTokens) For(string) (forge.TokenSource, error) { return staticSource("ghs_publisher"), nil }

// recordingTokens is a PublisherTokens that records each repository it is asked for.
type recordingTokens struct {
	mu    sync.Mutex
	asked []string
}

func (r *recordingTokens) For(repo string) (forge.TokenSource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, repo)
	return staticSource("ghs_publisher"), nil
}

// The publisher's connection: its token from PublisherTokens for the repository (HR11), one
// connection per repository whatever its spelling, and every API call carries that token.
func TestConnectPublishers(t *testing.T) {
	var mu sync.Mutex
	var authed []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authed = append(authed, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"resources":{}}`)
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	hc := httpx.New(5*time.Second, roots)
	tokens := &recordingTokens{}
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
	if len(tokens.asked) != 1 || tokens.asked[0] != "Owner/Repo" {
		t.Errorf("asked %q", tokens.asked)
	}
	for _, h := range authed {
		if h != "Bearer ghs_publisher" {
			t.Errorf("an API call carried %q", h)
		}
	}
}

// HR24: the pusher is one mirror per repository under the cache root, its token the publisher's,
// from the same PublisherTokens; no root is no pusher.
func TestConnectPushers(t *testing.T) {
	connect := ConnectPushers(staticTokens{}, t.TempDir(), 0)
	a, err := connect(t.Context(), "Owner/Repo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := connect(t.Context(), "owner/repo")
	if err != nil || a != b {
		t.Fatalf("a second spelling made another mirror: %v", err)
	}
	if _, ok := a.(*gitbundle.Mirror); !ok {
		t.Fatalf("pusher %T", a)
	}
	if _, err := ConnectPushers(staticTokens{}, "", 0)(t.Context(), "owner/repo"); err == nil {
		t.Error("no cache root gave a pusher")
	}
}
