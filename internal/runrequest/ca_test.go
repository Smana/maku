// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
)

// S-1: a run request trusts the CA in factoryCAFile, read at each call: a factory it signs is
// reached, one another CA signs is refused, and a rotated file applies with no restart.
func TestFactoryTrustsTheCAFileReadAtUse(t *testing.T) {
	served, other := t.TempDir(), t.TempDir()
	tlsreloadtest.WritePair(t, served, 1, time.Now())
	tlsreloadtest.WritePair(t, other, 2, time.Now())
	pair, err := tls.LoadX509KeyPair(filepath.Join(served, "tls.crt"), filepath.Join(served, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		if r.URL.Path == "/v1/tasks" {
			_, _ = w.Write([]byte(`{"task":"abcd2345","room":"abcd2345","issue":42,"duplicate":false,"sponsor":"smana"}`))
			return
		}
		_, _ = w.Write([]byte(`{"runId":"3kq7x2ma"}`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	ca := filepath.Join(t.TempDir(), "ca.crt")
	f := Factory{URL: srv.URL, CAFile: ca}
	req := Request{Role: "implementer", AccessToken: "human-token", IdempotencyKey: "3kq7x2ma:web:1"}
	copyCA := func(from string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(from, "tls.crt"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ca, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Request(t.Context(), req); err == nil {
		t.Fatal("a missing factoryCAFile reached the factory")
	}
	copyCA(other)
	if _, err := f.Request(t.Context(), req); err == nil {
		t.Fatal("a factory signed by another CA was trusted")
	}
	copyCA(served) // the rotation: same path, new content, same Factory value
	if res, err := f.Request(t.Context(), req); err != nil || res.RunID != "3kq7x2ma" || res.Via != "factory" {
		t.Fatalf("the factory's own CA: %+v %v", res, err)
	}
	task, err := f.CreateTask(t.Context(), TaskRequest{Repository: "Smana/x", Text: "t", DataClass: "public", MaxCostUSD: 2,
		AccessToken: "tok", IdempotencyKey: "k1"})
	if err != nil || task.Task != "abcd2345" {
		t.Fatalf("a sponsored task over the same CA: %+v %v", task, err)
	}
}
