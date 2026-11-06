// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"crypto/x509"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// Mint asks for exactly the repositories and permissions it is given, finds the installation on
// the first repository, and returns the token with GitHub's expiry, uncached.
func TestMintSendsRepositoriesAndPermissions(t *testing.T) {
	_, pemKey := newKey(t)
	dir := t.TempDir()
	idFile, keyFile := filepath.Join(dir, "app_id"), filepath.Join(dir, "private_key")
	if err := os.WriteFile(idFile, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	var found string
	var got struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	mints := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/Smana/a/installation", func(w http.ResponseWriter, _ *http.Request) {
		found = "Smana/a"
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
	})
	mux.HandleFunc("POST /app/installations/9/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		mints++
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_demo", "expires_at": exp.Format(time.RFC3339)})
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	hc := httpx.New(5*time.Second, roots)
	perms := map[string]string{"contents": "write", "metadata": "read"}
	o := MintOptions{AppIDFile: idFile, KeyFile: keyFile, Owner: "Smana", Repositories: []string{"a", "b"}, Permissions: perms}

	for i := range 2 {
		tok, err := Mint(t.Context(), hc, srv.URL+"/", o)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Value != "ghs_demo" || !tok.ExpiresAt.Equal(exp) {
			t.Fatalf("token %+v", tok)
		}
		if mints != i+1 {
			t.Fatalf("mints = %d: Mint must not cache", mints)
		}
	}
	if found != "Smana/a" || !slices.Equal(got.Repositories, []string{"a", "b"}) || !maps.Equal(got.Permissions, perms) {
		t.Fatalf("found %q, body %+v", found, got)
	}
	if _, err := Mint(t.Context(), hc, "http://x/", o); err == nil {
		t.Fatal("an http API must be refused")
	}
	if _, err := Mint(t.Context(), hc, srv.URL+"/", MintOptions{AppIDFile: idFile, KeyFile: keyFile}); err == nil {
		t.Fatal("no owner, no repositories: refused")
	}
}
