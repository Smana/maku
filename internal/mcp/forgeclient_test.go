// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Smana/maku/internal/hardening"
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
