// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/policy"
)

type fakeReadiness struct {
	calls int
	fail  string
}

func (f *fakeReadiness) Hardened(_ context.Context, repo string) (hardening.Report, error) {
	f.calls++
	if repo == f.fail {
		return hardening.Report{}, errors.New("read publisher policy: 502")
	}
	return hardening.Report{Missing: []hardening.Missing{hardening.MissingDrill}}, nil
}

func readinessServer(t *testing.T) (*Server, *fakeReadiness) {
	t.Helper()
	cfg := sponsoredCfg()
	cfg.Repositories = append(cfg.Repositories, config.Repository{Name: "Smana/off"}, config.Repository{Name: "Smana/broken"})
	cfg.Hardening = config.Hardening{Mode: hardening.ModeOff, Repositories: map[string]config.HardeningRepository{
		"Smana/cloud-native-ref": {Mode: hardening.ModeAudit}, "Smana/broken": {Mode: hardening.ModeAudit}}}
	rd := &fakeReadiness{fail: "Smana/broken"}
	return &Server{Cfg: cfg, Readiness: rd, Groups: policy.Groups{Admin: "agents-admins", Member: "agents"},
		Auth: byToken{
			"op":  {Kind: envelope.ActorHuman, ID: "human:1", Sub: "1", Groups: []string{"agents-admins"}},
			"dev": {Kind: envelope.ActorHuman, ID: "human:2", Sub: "2", Groups: []string{"agents"}},
			"sys": {Kind: envelope.ActorSystem, ID: "system:broker"},
		}, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}, rd
}

func getReadiness(s *Server, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/hardening/readiness", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// HR41: an operator (a human in the admin group) gets every repository at mode audit or above:
// hardened, missing and drill, at most 60 s old; an error reads missing error. Anyone else is 403.
func TestReadinessRoute(t *testing.T) {
	s, rd := readinessServer(t)
	w := getReadiness(s, "op")
	var got struct {
		Repositories []struct {
			Repository string   `json:"repository"`
			Hardened   bool     `json:"hardened"`
			Missing    []string `json:"missing"`
		} `json:"repositories"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Repositories) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if r := got.Repositories[0]; r.Repository != "Smana/cloud-native-ref" || r.Hardened || len(r.Missing) != 1 || r.Missing[0] != "drill_stale_or_failed" {
		t.Fatalf("%+v", r)
	}
	if r := got.Repositories[1]; r.Repository != "Smana/broken" || r.Hardened || len(r.Missing) != 1 || r.Missing[0] != "error" {
		t.Fatalf("%+v", r)
	}
	getReadiness(s, "op")
	if rd.calls != 2 {
		t.Fatalf("%d evaluations: a second view inside the TTL reads nothing", rd.calls)
	}
	for _, tok := range []string{"dev", "sys"} {
		if w := getReadiness(s, tok); w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", tok, w.Code)
		}
	}
	if w := getReadiness(s, "nobody"); w.Code != http.StatusUnauthorized {
		t.Fatalf("%d", w.Code)
	}
	s.Readiness = nil
	if w := getReadiness(s, "op"); w.Code != http.StatusNotFound {
		t.Fatalf("no readiness port: %d", w.Code)
	}
}

// HR41: the Evidence page carries the repository's readiness row for a repository at mode audit or
// above, none below audit; it says ready, not ready or error, and the missing facts stay on the
// admin-only route.
func TestReadinessEvidenceRow(t *testing.T) {
	s, _ := evidenceServer(true)
	rd := &fakeReadiness{}
	s.Readiness = rd
	s.Cfg.Current().Hardening = config.Hardening{Repositories: map[string]config.HardeningRepository{coordPrimary: {Mode: hardening.ModeAudit}}}
	w := getAs(s.Handler(), "human", "/v1/policy/evidence?repository="+coordPrimary)
	var r evidence.Report
	// #531's review: a reader sees ready or not ready, never which facts are missing.
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Hardening == nil || r.Hardening.Hardened ||
		len(r.Hardening.Missing) != 0 || r.Hardening.Drill != "" {
		t.Fatalf("%s %v", w.Body, err)
	}
	s.Cfg.Current().Hardening = config.Hardening{}
	w = getAs(s.Handler(), "human", "/v1/policy/evidence?repository="+coordPrimary)
	var below evidence.Report
	if err := json.Unmarshal(w.Body.Bytes(), &below); err != nil || below.Hardening != nil {
		t.Fatalf("below audit: %s %v", w.Body, err)
	}
}

// blockingReadiness counts its reads and holds each until release is closed.
type blockingReadiness struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
	fail    bool
}

func (b *blockingReadiness) Hardened(context.Context, string) (hardening.Report, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-b.release
	if b.fail {
		return hardening.Report{}, errors.New("read publisher policy: 502")
	}
	return hardening.Report{Missing: []hardening.Missing{hardening.MissingDrill}}, nil
}

// #531's review: the Evidence page and the readiness route share one read per repository for 60
// seconds, so N concurrent views cost one evaluation; past the TTL it is read again, and an error
// reads error. (Admission keeps reading fresh: it calls the Evaluator, not this.)
//
// Deterministic: the read is held until every view has missed the cache, and the views that reach
// singleflight only after the first read has finished must find its answer, not read again.
func TestReadinessCacheSharesOneRead(t *testing.T) {
	s, _ := readinessServer(t)
	b := &blockingReadiness{release: make(chan struct{})}
	s.Readiness = b
	clock := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	s.Handler() // the cache is built with the handler
	const n = 8
	var misses atomic.Int32
	allMissed := make(chan struct{})
	s.readinessCache.afterMiss = func() {
		if misses.Add(1) == n {
			close(allMissed)
		}
	}
	reads := func() int { b.mu.Lock(); defer b.mu.Unlock(); return b.calls }
	var done sync.WaitGroup
	got := make([]evidence.Readiness, n)
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			got[i] = s.readinessOf(t.Context(), "Smana/cloud-native-ref")
		}()
	}
	<-allMissed // every view missed the cache before the one read may finish
	close(b.release)
	done.Wait()
	if r := reads(); r != 1 {
		t.Fatalf("%d reads for %d concurrent views, want 1", r, n)
	}
	for _, v := range got {
		if v.Hardened || len(v.Missing) != 1 || v.Missing[0] != "drill_stale_or_failed" {
			t.Fatalf("%+v", got)
		}
	}
	s.readinessOf(t.Context(), "Smana/cloud-native-ref")
	if r := reads(); r != 1 {
		t.Fatalf("a view inside the TTL read again: %d", r)
	}
	clock = clock.Add(61 * time.Second)
	b.mu.Lock()
	b.fail = true
	b.mu.Unlock()
	if v := s.readinessOf(t.Context(), "Smana/cloud-native-ref"); reads() != 2 || len(v.Missing) != 1 || v.Missing[0] != "error" {
		t.Fatalf("past the TTL: %d reads, %+v", reads(), v)
	}
}
