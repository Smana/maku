// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// appRig is an App key on disk and an httptest TLS server whose handlers each test sets.
type appRig struct {
	mux     *http.ServeMux
	srv     *httptest.Server
	hc      *http.Client
	id, key string
}

func newAppRig(t *testing.T) *appRig {
	t.Helper()
	_, pemKey := newKey(t)
	dir := t.TempDir()
	r := &appRig{mux: http.NewServeMux(), id: filepath.Join(dir, "app_id"), key: filepath.Join(dir, "private_key")}
	if err := os.WriteFile(r.id, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.key, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewTLSServer(r.mux)
	t.Cleanup(r.srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(r.srv.Certificate())
	r.hc = httpx.New(5*time.Second, roots)
	return r
}

func (r *appRig) client(base string) *AppClient {
	return &AppClient{API: r.srv.URL + base, HTTP: r.hc, IDFile: r.id, KeyFile: r.key, Now: time.Now,
		Installations: &Installations{}}
}

// leaked is a token the fake quotes in every body it can: no error may carry it.
const leaked = "ghs_SECRETVALUE"

// tokenReply answers a mint with the given permissions and repositories, quoting the token.
func tokenReply(perms map[string]string, repos ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		rs := make([]map[string]string, 0, len(repos))
		for _, n := range repos {
			rs = append(rs, map[string]string{"name": n, "full_name": "o/" + n})
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": leaked,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": perms, "repositories": rs})
	}
}

func TestAppClientAccessToken(t *testing.T) {
	asked := map[string]string{"contents": "write", "metadata": "read"}
	cases := []struct {
		name    string
		reply   http.HandlerFunc
		want    error
		revokes int
	}{
		{"the reply matches the request", tokenReply(asked, "r"), nil, 0},
		{"a narrower grant is a scope mismatch, revoked", tokenReply(map[string]string{"contents": "read", "metadata": "read"}, "r"), ErrScopeMismatch, 1},
		{"a wider grant is a scope mismatch, revoked", tokenReply(map[string]string{"contents": "write", "metadata": "read", "issues": "write"}, "r"), ErrScopeMismatch, 1},
		{"two repositories are a scope mismatch, revoked", tokenReply(asked, "r", "s"), ErrScopeMismatch, 1},
		{"another repository is a scope mismatch, revoked", tokenReply(asked, "s"), ErrScopeMismatch, 1},
		{"no repositories are a scope mismatch, revoked", tokenReply(asked), ErrScopeMismatch, 1},
		{"a 422 is a permission the installation lacks", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"`+leaked+`"}`, http.StatusUnprocessableEntity)
		}, ErrPermissionNotGranted, 0},
		{"a 404 is an App not installed", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leaked, http.StatusNotFound)
		}, ErrNotInstalled, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newAppRig(t)
			var mu sync.Mutex
			var body []byte
			var auth string
			revoked := 0
			r.mux.HandleFunc("POST /app/installations/9/access_tokens", func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				body, _ = io.ReadAll(req.Body)
				auth = req.Header.Get("Authorization")
				mu.Unlock()
				c.reply(w, req)
			})
			r.mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				if req.Header.Get("Authorization") == "Bearer "+leaked {
					revoked++
				}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			})
			tok, granted, repos, err := r.client("/").AccessToken(t.Context(), 9, []string{"r"}, asked)
			mu.Lock()
			defer mu.Unlock()
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if revoked != c.revokes {
				t.Fatalf("revokes = %d, want %d", revoked, c.revokes)
			}
			if err != nil {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error quotes the token: %v", err)
				}
				if tok.Value != "" {
					t.Fatal("a failed mint returns no token")
				}
				return
			}
			if got := string(bytes.TrimSpace(body)); got != `{"permissions":{"contents":"write","metadata":"read"},"repositories":["r"]}` {
				t.Fatalf("body %s", got)
			}
			if !strings.HasPrefix(auth, "Bearer ey") {
				t.Fatalf("auth %q: want the App JWT", auth)
			}
			if tok.Value != leaked || tok.ExpiresAt.IsZero() || !maps.Equal(granted, asked) || !slices.Equal(repos, []string{"r"}) {
				t.Fatalf("token %v granted %v repos %v", tok.ExpiresAt, granted, repos)
			}
		})
	}
}

func TestInstallations(t *testing.T) {
	r := newAppRig(t)
	var mu sync.Mutex
	lookups, missing := 0, false
	r.mux.HandleFunc("GET /repos/{o}/{n}/installation", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		if missing {
			http.Error(w, leaked, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return lookups }
	c := r.client("/")
	now := time.Now()
	c.Installations.Now = func() time.Time { return now }
	get := func() (int64, error) { return c.Installation(t.Context(), "Smana", "Repo") }

	if id, err := get(); err != nil || id != 9 || count() != 1 {
		t.Fatalf("id %d err %v lookups %d", id, err, count())
	}
	if _, err := c.Installation(t.Context(), "smana", "repo"); err != nil || count() != 1 {
		t.Fatalf("a second call within the TTL, in any case, makes no request: lookups %d err %v", count(), err)
	}
	c.Installations.Evict("SMANA", "repo")
	if _, err := get(); err != nil || count() != 2 {
		t.Fatalf("Evict forces a lookup: lookups %d", count())
	}
	now = now.Add(61 * time.Minute)
	if _, err := get(); err != nil || count() != 3 {
		t.Fatalf("after an hour the entry is stale: lookups %d", count())
	}
	mu.Lock()
	missing = true
	mu.Unlock()
	now = now.Add(61 * time.Minute)
	if _, err := get(); !errors.Is(err, ErrNotInstalled) || strings.Contains(err.Error(), leaked) {
		t.Fatalf("a 404 is ErrNotInstalled, naming no body: %v", err)
	}
	mu.Lock()
	missing = false
	mu.Unlock()
	if _, err := get(); err != nil || count() != 5 {
		t.Fatalf("a 404 caches nothing: lookups %d", count())
	}
	if _, err := get(); err != nil || count() != 5 {
		t.Fatalf("the found id is cached again: lookups %d", count())
	}
}

func TestAppClientRevoke(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		ok     bool
	}{{"204 revoked", http.StatusNoContent, true}, {"401 already gone", http.StatusUnauthorized, true}, {"500 an error", http.StatusInternalServerError, false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newAppRig(t)
			r.mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				if c.status != http.StatusNoContent {
					_, _ = w.Write([]byte(leaked))
				}
			})
			err := r.client("/").Revoke(t.Context(), leaked)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), leaked) || !strings.Contains(err.Error(), "500")) {
				t.Fatalf("the error names the status only: %v", err)
			}
		})
	}
}

func TestAppClientGranted(t *testing.T) {
	r := newAppRig(t)
	r.mux.HandleFunc("GET /app/installations/9", func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ey") {
			http.Error(w, "jwt", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "permissions": map[string]string{"contents": "write", "metadata": "read"}})
	})
	got, err := r.client("/").Granted(t.Context(), 9)
	if err != nil || !maps.Equal(got, map[string]string{"contents": "write", "metadata": "read"}) {
		t.Fatalf("granted %v err %v", got, err)
	}
}

// Every call is joined under a GHES base path.
func TestAppClientGHES(t *testing.T) {
	r := newAppRig(t)
	var mu sync.Mutex
	var paths []string
	r.mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(req.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
		case strings.HasSuffix(req.URL.Path, "/access_tokens"):
			tokenReply(map[string]string{"metadata": "read"}, "r")(w, req)
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": map[string]string{}})
		}
	})
	c := r.client("/api/v3/")
	if _, err := c.Installation(t.Context(), "o", "r"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.AccessToken(t.Context(), 9, []string{"r"}, map[string]string{"metadata": "read"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Revoke(t.Context(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Granted(t.Context(), 9); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/v3/repos/o/r/installation", "POST /api/v3/app/installations/9/access_tokens",
		"DELETE /api/v3/installation/token", "GET /api/v3/app/installations/9"}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, want) {
		t.Fatalf("paths %v", paths)
	}
}

// An http API, or no client, is refused before any call.
func TestAppClientRefusesAPlainAPI(t *testing.T) {
	r := newAppRig(t)
	for _, c := range []*AppClient{
		{API: "http://x/", HTTP: r.hc, IDFile: r.id, KeyFile: r.key},
		{API: r.srv.URL + "/", IDFile: r.id, KeyFile: r.key},
	} {
		if _, err := c.Installation(t.Context(), "o", "r"); err == nil {
			t.Fatalf("%q: refused", c.API)
		}
	}
}
