// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"io"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/drills"
	"github.com/Smana/maku/internal/hardening"
)

// DrillPrincipal is the drill planter's system principal on this API (HR38): the api.systemPrincipals
// value its CronJob's ServiceAccount maps to, the one caller the drill manifest route admits.
const DrillPrincipal = config.DrillPrincipal

// DrillsRoute is the route a drill planter posts its manifest to.
const DrillsRoute = "POST /v1/hardening/drills"

// maxDrillManifest bounds a manifest's body.
const maxDrillManifest = 16 << 10

// postDrill is POST /v1/hardening/drills (HR38): the planter hands the factory a drill's manifest,
// {id, scenarios: [{scenario, task, marker}]}. Each scenario is the default suite's, and each task
// exists and carries the drill label (its issue was labelled factory/drill-injection). The factory
// scores the drill itself; the planter never writes a report. 201 accepted, 200 the same manifest
// again, 409 drill_exists for another under the same id.
func (s *Server) postDrill(w http.ResponseWriter, r *http.Request) {
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	case p.Kind != envelope.ActorSystem || p.ID != DrillPrincipal:
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	}
	suite, err := hardening.DefaultSuite()
	if err != nil {
		refuse(w, http.StatusInternalServerError, "suite_unreadable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDrillManifest))
	if err != nil {
		refuse(w, http.StatusBadRequest, "bad_request")
		return
	}
	m, err := drills.ParseManifest(body, suite)
	if err != nil {
		refuse(w, http.StatusBadRequest, "bad_manifest")
		return
	}
	ctx := r.Context()
	for _, pl := range m.Scenarios {
		var t factoryv1.Task
		switch err := s.Rooms.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: pl.Task}, &t); {
		case apierrors.IsNotFound(err):
			refuse(w, http.StatusNotFound, "unknown_task")
			return
		case err != nil:
			s.log().Warn("drill manifest: tasks unread", "drill", m.ID, "err", err)
			refuse(w, http.StatusServiceUnavailable, "unavailable")
			return
		case t.Labels[factoryv1.LabelDrillInjection] != "true" || !s.Cfg.Current().DrillTask(t.Spec.Repository):
			refuse(w, http.StatusBadRequest, "not_a_drill_task")
			return
		}
	}
	created, err := drills.Accept(ctx, s.Rooms, s.Namespace, m)
	switch {
	case errors.Is(err, drills.ErrManifestExists):
		refuse(w, http.StatusConflict, "drill_exists")
		return
	case err != nil:
		s.log().Warn("drill manifest: not stored", "drill", m.ID, "err", err)
		refuse(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
		s.log().Info("drill manifest accepted", "drill", m.ID, "scenarios", len(m.Scenarios))
	}
	reply(w, code, map[string]any{"id": m.ID, "scenarios": len(m.Scenarios)})
}
