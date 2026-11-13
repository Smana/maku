// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// Drills is the store's drill reports (hardening HR38, R6); *store.Store implements it.
type Drills interface {
	PutDrillReport(ctx context.Context, r store.DrillReport) error
	LastPass(ctx context.Context, version, configHash string) (id string, at time.Time, found bool, err error)
	DrillPassed(ctx context.Context, id string) (pass, found bool, err error)
}

// maxDrillBody bounds a report's request: the stored report's cap and its envelope.
const maxDrillBody = store.MaxDrillReport + 4<<10

var (
	drillVersionRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	drillHashRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// putDrill: POST /v1/drills, OutputClient only (HR38). The factory stores one scored drill:
// {id, version, configHash, at, pass, report}. The factory is the only writer, so a report cannot
// be forged by the drill's planter. 201 {id}; a second report under one id is 409 drill_exists.
func (s *Server) putDrill(w http.ResponseWriter, r *http.Request) {
	_, release, ok := s.outputCaller(w, r, s.Drills != nil)
	if !ok {
		return
	}
	defer release()
	var in struct {
		ID         string          `json:"id"`
		Version    string          `json:"version"`
		ConfigHash string          `json:"configHash"`
		At         time.Time       `json:"at"`
		Pass       bool            `json:"pass"`
		Report     json.RawMessage `json:"report"`
	}
	rep := store.DrillReport{}
	if err := decodeStrict(r.Body, &in); err == nil {
		rep = store.DrillReport{ID: in.ID, Version: in.Version, ConfigHash: in.ConfigHash, At: in.At, Pass: in.Pass, Report: in.Report}
	}
	if rep.Validate() != nil || !isObject(rep.Report) {
		fail(w, http.StatusBadRequest, wire.ReasonBadDrill)
		return
	}
	switch err := s.Drills.PutDrillReport(r.Context(), rep); {
	case errors.Is(err, store.ErrDrillExists):
		fail(w, http.StatusConflict, wire.ReasonDrillExists)
		return
	case errors.Is(err, store.ErrBadDrillReport):
		fail(w, http.StatusBadRequest, wire.ReasonBadDrill)
		return
	case err != nil:
		s.logFailure(w, err, "store a drill report", "drill", rep.ID)
		return
	}
	reply(w, http.StatusCreated, map[string]string{"id": rep.ID})
}

// lastPassingDrill: GET /v1/drills/last-pass?version=&configHash=, OutputClient only (R6). The
// newest passing drill of that Maku version and hardening configuration hash: 200 {id, at}, or 404
// no_drill when none passed.
func (s *Server) lastPassingDrill(w http.ResponseWriter, r *http.Request) {
	_, release, ok := s.outputCaller(w, r, s.Drills != nil)
	if !ok {
		return
	}
	defer release()
	version, hash := r.URL.Query().Get("version"), r.URL.Query().Get("configHash")
	if !drillVersionRE.MatchString(version) || !drillHashRE.MatchString(hash) {
		fail(w, http.StatusBadRequest, wire.ReasonBadDrill)
		return
	}
	id, at, found, err := s.Drills.LastPass(r.Context(), version, hash)
	switch {
	case err != nil:
		s.logFailure(w, err, "read the last passing drill")
		return
	case !found:
		fail(w, http.StatusNotFound, wire.ReasonNoDrill)
		return
	}
	reply(w, http.StatusOK, map[string]any{"id": id, "at": at.UTC()})
}

// drillResult: GET /v1/drills/{id}/result, OutputClient only: whether drill id's stored report
// passed, {id, pass}, or 404 no_drill. The factory marks a manifest from it when an earlier pass
// stored the report and its own mark was lost.
func (s *Server) drillResult(w http.ResponseWriter, r *http.Request) {
	_, release, ok := s.outputCaller(w, r, s.Drills != nil)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	if !store.ValidDrillID(id) {
		fail(w, http.StatusBadRequest, wire.ReasonBadDrill)
		return
	}
	pass, found, err := s.Drills.DrillPassed(r.Context(), id)
	switch {
	case err != nil:
		s.logFailure(w, err, "read a drill report", "drill", id)
		return
	case !found:
		fail(w, http.StatusNotFound, wire.ReasonNoDrill)
		return
	}
	reply(w, http.StatusOK, map[string]any{"id": id, "pass": pass})
}
