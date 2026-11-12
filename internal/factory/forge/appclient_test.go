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

// tokenReply answers a mint with the given permissions and repositories (full names), quoting the
// token.
func tokenReply(perms map[string]string, fullNames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		rs := make([]map[string]string, 0, len(fullNames))
		for _, fn := range fullNames {
			_, n, _ := strings.Cut(fn, "/")
			rs = append(rs, map[string]string{"name": n, "full_name": fn})
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": leaked,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": perms, "repositories": rs})
	}
}

func TestAppClientAccessToken(t *testing.T) {
	asked := map[string]string{"contents": "write", "metadata": "read"}
	noMeta := map[string]string{"contents": "write"}
	cases := []struct {
		name         string
		asked        map[string]string // asked when nil
		reply        http.HandlerFunc
		revokeStatus int // 204 when zero
		want         []error
		revokes      int
	}{
		{"the reply matches the request", nil, tokenReply(asked, "o/r"), 0, nil, 0},
		{"GitHub's added metadata: read is accepted", noMeta, tokenReply(asked, "o/r"), 0, nil, 0},
		{"the full name matches in any case", nil, tokenReply(asked, "O/R"), 0, nil, 0},
		{"a narrower grant is a scope mismatch, revoked", nil, tokenReply(map[string]string{"contents": "read", "metadata": "read"}, "o/r"), 0, []error{ErrScopeMismatch}, 1},
		{"a wider grant is a scope mismatch, revoked", nil, tokenReply(map[string]string{"contents": "write", "metadata": "read", "issues": "write"}, "o/r"), 0, []error{ErrScopeMismatch}, 1},
		{"metadata wider than read is a scope mismatch", noMeta, tokenReply(map[string]string{"contents": "write", "metadata": "write"}, "o/r"), 0, []error{ErrScopeMismatch}, 1},
		{"a requested key missing from the reply is a scope mismatch", nil, tokenReply(map[string]string{"metadata": "read"}, "o/r"), 0, []error{ErrScopeMismatch}, 1},
		{"two repositories are a scope mismatch, revoked", nil, tokenReply(asked, "o/r", "o/s"), 0, []error{ErrScopeMismatch}, 1},
		{"another repository is a scope mismatch, revoked", nil, tokenReply(asked, "o/s"), 0, []error{ErrScopeMismatch}, 1},
		{"the same name under another owner (a transfer) is a scope mismatch", nil, tokenReply(asked, "x/r"), 0, []error{ErrScopeMismatch}, 1},
		{"no repositories are a scope mismatch, revoked", nil, tokenReply(asked), 0, []error{ErrScopeMismatch}, 1},
		{"a mismatch whose revoke fails says so", nil, tokenReply(asked, "o/s"), http.StatusInternalServerError, []error{ErrScopeMismatch, ErrRevokeFailed}, 1},
		{"a 422 is a permission or repository not granted", nil, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"`+leaked+`"}`, http.StatusUnprocessableEntity)
		}, 0, []error{ErrPermissionNotGranted}, 0},
		{"a 404 is an App not installed", nil, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leaked, http.StatusNotFound)
		}, 0, []error{ErrNotInstalled}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := c.asked
			if want == nil {
				want = asked
			}
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
				status := c.revokeStatus
				if status == 0 {
					status = http.StatusNoContent
				}
				w.WriteHeader(status)
			})
			tok, granted, repos, err := r.client("/").AccessToken(t.Context(), 9, "o", []string{"r"}, want)
			mu.Lock()
			defer mu.Unlock()
			for _, w := range c.want {
				if !errors.Is(err, w) {
					t.Fatalf("err = %v, want %v", err, w)
				}
			}
			if c.want == nil && err != nil {
				t.Fatalf("err = %v", err)
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
			raw, _ := json.Marshal(map[string]any{"permissions": want, "repositories": []string{"r"}})
			if got := string(bytes.TrimSpace(body)); got != string(raw) {
				t.Fatalf("body %s, want %s", got, raw)
			}
			if !strings.HasPrefix(auth, "Bearer ey") {
				t.Fatalf("auth %q: want the App JWT", auth)
			}
			if tok.Value != leaked || tok.ExpiresAt.IsZero() || granted["contents"] != "write" || len(repos) != 1 || !strings.EqualFold(repos[0], "o/r") {
				t.Fatalf("token %v granted %v repos %v", tok.ExpiresAt, granted, repos)
			}
		})
	}
}

func TestAppClientMessages(t *testing.T) {
	if got := ErrPermissionNotGranted.Error(); !strings.Contains(got, "permission or repository not granted") {
		t.Fatalf("ErrPermissionNotGranted = %q: a 422 covers both", got)
	}
}

// Names are checked before any path is built from them.
func TestAppClientRefusesBadNames(t *testing.T) {
	r := newAppRig(t)
	calls := 0
	r.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { calls++; http.Error(w, "no", http.StatusTeapot) })
	c := r.client("/")
	for _, n := range [][2]string{{"o", "../x"}, {"o", "a..b"}, {"..", "r"}, {"o", "r/s"}, {"", "r"}, {"o", ""}, {"o", "r?x"}, {"o w", "r"}, {"o", "."}} {
		if _, err := c.Installation(t.Context(), n[0], n[1]); err == nil {
			t.Fatalf("Installation(%q, %q) is refused", n[0], n[1])
		}
		if _, _, _, err := c.AccessToken(t.Context(), 9, n[0], []string{n[1]}, map[string]string{"metadata": "read"}); err == nil {
			t.Fatalf("AccessToken(%q, %q) is refused", n[0], n[1])
		}
	}
	if calls != 0 {
		t.Fatalf("%d requests were made", calls)
	}
	if _, err := c.Installation(t.Context(), "Smana-x", "my_repo.v2"); err == nil || calls != 1 {
		t.Fatalf("a valid name reaches GitHub: calls %d err %v", calls, err)
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
			var re *RevokeError
			if err != nil && (!errors.Is(err, ErrRevokeFailed) || !errors.As(err, &re) || re.Status != c.status) {
				t.Fatalf("a failed revoke is a *RevokeError carrying its status: %v", err)
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
			tokenReply(map[string]string{"metadata": "read"}, "o/r")(w, req)
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
	if _, _, _, err := c.AccessToken(t.Context(), 9, "o", []string{"r"}, map[string]string{"metadata": "read"}); err != nil {
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
