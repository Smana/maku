// SPDX-License-Identifier: Apache-2.0

package rooms

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/wire"
)

// The client implements readiness's drill port (R6).
var _ hardening.Drills = (*Client)(nil)

// HR38: the factory posts a scored report and reads the newest pass back; a version or hash the
// broker would refuse is never sent, and no_drill is no pass, not an error.
func TestRoomsDrills(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var queries []string
	missing := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/drills", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		if n > 1 {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(wire.Error{Reason: wire.ReasonDrillExists})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"drill-1"}`)
	})
	mux.HandleFunc("GET /v1/drills/last-pass", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		gone := missing
		mu.Unlock()
		if gone {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(wire.Error{Reason: wire.ReasonNoDrill})
			return
		}
		_, _ = io.WriteString(w, `{"id":"drill-1","at":"2026-11-13T06:00:00Z"}`)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("t1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c, err := New(srv.URL, tok, httpx.New(5*time.Second, roots), nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	at := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	rep := hardening.DrillReport{DrillMeta: hardening.DrillMeta{ID: "drill-1", Version: "v0.45.0", ConfigHash: hash, At: at},
		Pass: true, Runs: 2, Failures: []hardening.Failure{}, Detections: map[hardening.Layer]int{hardening.LayerPaths: 1}}
	if err := c.PutDrillReport(t.Context(), rep); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil || sent["id"] != "drill-1" || sent["pass"] != true ||
		sent["configHash"] != hash || sent["version"] != "v0.45.0" || sent["report"].(map[string]any)["runs"] != float64(2) {
		t.Fatalf("body %s", bodies[0])
	}
	if err := c.PutDrillReport(t.Context(), rep); !errors.Is(err, ErrDrillExists) {
		t.Fatalf("a second report: %v", err)
	}
	got, ok, err := c.LastPass(t.Context(), hardening.DrillKey{Version: "v0.45.0", ConfigHash: hash})
	if err != nil || !ok || got.ID != "drill-1" || !got.At.Equal(at) || len(queries) != 1 || queries[0] != "configHash="+hash+"&version=v0.45.0" {
		t.Fatalf("%+v %v %v (queries %q)", got, ok, err, queries)
	}
	missing = true
	if _, ok, err := c.LastPass(t.Context(), hardening.DrillKey{Version: "v0.45.0", ConfigHash: hash}); err != nil || ok {
		t.Fatalf("no pass: %v %v", ok, err)
	}
	sentBefore := len(bodies) + len(queries)
	for name, err := range map[string]error{
		"a bad hash": func() error {
			_, _, err := c.LastPass(t.Context(), hardening.DrillKey{Version: "v1", ConfigHash: "x"})
			return err
		}(),
		"a bad version": func() error {
			_, _, err := c.LastPass(t.Context(), hardening.DrillKey{Version: "v 1", ConfigHash: hash})
			return err
		}(),
		"a bad id": func() error {
			r := rep
			r.ID = "Drill 1"
			return c.PutDrillReport(t.Context(), r)
		}(),
	} {
		if err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if len(bodies)+len(queries) != sentBefore {
		t.Fatal("a refused call reached the broker")
	}
}
