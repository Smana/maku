// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// memDrills is the store's drill reports: once per id, the newest pass per version and hash.
type memDrills struct {
	mu   sync.Mutex
	rows map[string]store.DrillReport
}

func (m *memDrills) PutDrillReport(_ context.Context, r store.DrillReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := r.Validate(); err != nil {
		return err
	}
	if _, ok := m.rows[r.ID]; ok {
		return store.ErrDrillExists
	}
	m.rows[r.ID] = r
	return nil
}

func (m *memDrills) LastPass(_ context.Context, version, hash string) (string, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best store.DrillReport
	for _, r := range m.rows {
		if r.Pass && r.Version == version && r.ConfigHash == hash && r.At.After(best.At) {
			best = r
		}
	}
	return best.ID, best.At, best.ID != "", nil
}

func drillBody(mut func(map[string]any)) map[string]any {
	b := map[string]any{"id": "drill-1", "version": "v0.45.0", "configHash": strings.Repeat("c", 64),
		"at": "2026-11-13T06:00:00Z", "pass": true, "report": map[string]any{"pass": true, "runs": 30}}
	if mut != nil {
		mut(b)
	}
	return b
}

// HR38: the factory, and only the factory, stores a drill report once and reads the newest pass of
// a version and configuration hash back.
func TestDrillRoutes(t *testing.T) {
	s, _, _ := outputServer(t)
	m := &memDrills{rows: map[string]store.DrillReport{}}
	s.Drills = m
	h := s.Routes()
	if rec := call(t, h, http.MethodPost, "/v1/drills", "sys:"+factory, drillBody(nil)); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r := m.rows["drill-1"]; !r.Pass || r.ConfigHash != strings.Repeat("c", 64) || !strings.Contains(string(r.Report), `"runs":30`) {
		t.Fatalf("stored %+v", r)
	}
	last := "/v1/drills/last-pass?version=v0.45.0&configHash=" + strings.Repeat("c", 64)
	rec := call(t, h, http.MethodGet, last, "sys:"+factory, nil)
	var got struct {
		ID string    `json:"id"`
		At time.Time `json:"at"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ID != "drill-1" ||
		!got.At.Equal(time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		method, path, token string
		body                any
		code                int
		reason              string
	}{
		"a second report under one id": {http.MethodPost, "/v1/drills", "sys:" + factory, drillBody(nil), http.StatusConflict, wire.ReasonDrillExists},
		"another system posts": {http.MethodPost, "/v1/drills", "sys:system:triage", drillBody(func(b map[string]any) { b["id"] = "drill-2" }),
			http.StatusForbidden, wire.ReasonNotPermitted},
		"another system reads": {http.MethodGet, last, "sys:system:triage", nil, http.StatusForbidden, wire.ReasonNotPermitted},
		"a run posts":          {http.MethodPost, "/v1/drills", "run:" + runA, drillBody(nil), http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"a bad id": {http.MethodPost, "/v1/drills", "sys:" + factory, drillBody(func(b map[string]any) { b["id"] = "Drill 2" }),
			http.StatusBadRequest, wire.ReasonBadDrill},
		"a bad hash": {http.MethodPost, "/v1/drills", "sys:" + factory, drillBody(func(b map[string]any) { b["id"], b["configHash"] = "drill-2", "x" }),
			http.StatusBadRequest, wire.ReasonBadDrill},
		"an unknown field": {http.MethodPost, "/v1/drills", "sys:" + factory, drillBody(func(b map[string]any) { b["id"], b["by"] = "drill-2", "me" }),
			http.StatusBadRequest, wire.ReasonBadDrill},
		"a report that is not an object": {http.MethodPost, "/v1/drills", "sys:" + factory,
			drillBody(func(b map[string]any) { b["id"], b["report"] = "drill-2", []int{1} }), http.StatusBadRequest, wire.ReasonBadDrill},
		"no pass for another hash": {http.MethodGet, "/v1/drills/last-pass?version=v0.45.0&configHash=" + strings.Repeat("d", 64), "sys:" + factory, nil,
			http.StatusNotFound, wire.ReasonNoDrill},
		"a read with a bad hash": {http.MethodGet, "/v1/drills/last-pass?version=v0.45.0&configHash=x", "sys:" + factory, nil,
			http.StatusBadRequest, wire.ReasonBadDrill},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, c.method, c.path, c.token, c.body)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.reason)
			}
		})
	}
	t.Run("a broker with no drill store", func(t *testing.T) {
		s.Drills = nil
		if rec := call(t, s.Routes(), http.MethodGet, last, "sys:"+factory, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%d", rec.Code)
		}
	})
}
