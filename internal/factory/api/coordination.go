// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/ghidentity"
)

// CoordinationService is the coordination flow the API serves (v4 MR4, MR5);
// *coordination.Service implements it.
type CoordinationService interface {
	Draft(ctx context.Context, actor coordination.Actor, p factoryv1.CoordinationPlan, hash string, planning coordination.PlanningReconciliation) (coordination.CoordinationConfirmation, error)
	Confirm(ctx context.Context, actor coordination.Actor, in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error)
}

// DraftRequest is PUT /v1/coordinations/{task}/draft's body: a CoordinationConfirmation's plan,
// hash and planning, and for a sponsored principal the user it drafts for. No confirmation, actor
// or AgentRun id is accepted: unknown fields are refused.
type DraftRequest struct {
	Plan       factoryv1.CoordinationPlan          `json:"plan"`
	PlanHash   string                              `json:"planHash"`
	Planning   coordination.PlanningReconciliation `json:"planning"`
	OnBehalfOf string                              `json:"onBehalfOf,omitempty"`
}

// maxCoordinationBody bounds a draft or confirmation: the plan's own bound plus its envelope.
const maxCoordinationBody = coordination.MaxPlanBytes + 8<<10

// coordinationOn is whether the switch serves the coordination routes: off, or unset, is a 404.
func (s *Server) coordinationOn() bool {
	m := s.Cfg.Coordination.Mode
	return s.Coordination != nil && m != "" && m != config.CoordinationOff
}

// draftCoordination stores a plan version on its parent (MR4). The actor is the token's: a human
// by their ZITADEL→GitHub link, holding write on the primary; a sponsored principal by its
// onBehalfOf maintainer, as POST /v1/tasks resolves one, holding write there too.
func (s *Server) draftCoordination(w http.ResponseWriter, r *http.Request) {
	p, ok := s.coordinationCaller(w, r)
	if !ok {
		return
	}
	var in DraftRequest
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Plan.ID != r.PathValue("task") {
		refuse(w, http.StatusBadRequest, "task_mismatch")
		return
	}
	primary := in.Plan.Primary.Repository
	actor := coordination.Actor{Principal: p.ID}
	switch {
	case p.Sponsored:
		sp := s.Cfg.SponsoredBy(p.ID)
		if p.Internal || sp == nil {
			refuse(w, http.StatusForbidden, reasonSponsoredOnly)
			return
		}
		if !slices.Contains(sp.Repositories, primary) {
			refuse(w, http.StatusForbidden, "repository_not_allowed")
			return
		}
		login, code, reason := s.sponsorOf(r.Context(), sp, TaskRequest{Repository: primary, OnBehalfOf: in.OnBehalfOf})
		if reason != "" {
			refuse(w, code, reason)
			return
		}
		if !s.loginWrites(r.Context(), primary, login) {
			refuse(w, http.StatusForbidden, "repository_permission")
			return
		}
		actor.LinkedGitHubLogin = login
	case in.OnBehalfOf != "":
		refuse(w, http.StatusBadRequest, "on_behalf_of_is_sponsored_only")
		return
	default:
		login, code, reason := s.linkedLogin(r.Context(), p, primary)
		if reason != "" {
			refuse(w, code, reason)
			return
		}
		if !s.mayWrite(r.Context(), p, primary) {
			refuse(w, http.StatusForbidden, "repository_permission")
			return
		}
		actor.LinkedGitHubLogin = login
	}
	out, err := s.Coordination.Draft(r.Context(), actor, in.Plan, in.PlanHash, in.Planning)
	s.answerCoordination(r.Context(), w, out, err)
}

// confirmCoordination records one confirmation (MR5): the parent's or exactly one child's, by a
// human only, as the login their own token links to. Authority itself is the service's: a
// configured maintainer with current GitHub write on that repository.
func (s *Server) confirmCoordination(w http.ResponseWriter, r *http.Request) {
	p, ok := s.coordinationCaller(w, r)
	if !ok {
		return
	}
	if p.Sponsored || p.Kind != envelope.ActorHuman {
		refuse(w, http.StatusForbidden, "human_only")
		return
	}
	var in coordination.CoordinationConfirmation
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Plan.ID != r.PathValue("task") {
		refuse(w, http.StatusBadRequest, "task_mismatch")
		return
	}
	repository := in.Plan.Primary.Repository
	if in.Child != nil {
		i := slices.IndexFunc(in.Plan.Children, func(c factoryv1.CoordinationChild) bool { return c.ID == in.Child.ChildID })
		if i < 0 {
			refuse(w, http.StatusBadRequest, "invalid_confirmation")
			return
		}
		repository = in.Plan.Children[i].Repository
	}
	login, code, reason := s.linkedLogin(r.Context(), p, repository)
	if reason != "" {
		refuse(w, code, reason)
		return
	}
	out, err := s.Coordination.Confirm(r.Context(), coordination.Actor{Principal: p.ID, LinkedGitHubLogin: login}, in)
	s.answerCoordination(r.Context(), w, out, err)
}

// coordinationCaller authenticates and applies the stop object, as every intake does.
func (s *Server) coordinationCaller(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return p, false
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return p, false
	}
	if s.Stopped != nil && s.Stopped(r.Context()) {
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return p, false
	}
	return p, true
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCoordinationBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

// linkedLogin is a human's GitHub login through their ZITADEL link, resolved on repository's
// connection: never a login the caller sent.
func (s *Server) linkedLogin(ctx context.Context, p authn.Principal, repository string) (string, int, string) {
	if p.Sub == "" || s.Logins == nil {
		return "", http.StatusForbidden, "not_linked"
	}
	login, err := s.Logins.Login(ctx, p.Sub, repository)
	switch {
	case errors.Is(err, ghidentity.ErrInactive):
		return "", http.StatusForbidden, "not_linked"
	case err != nil:
		s.logLink(ctx, "the caller's GitHub login is unreadable", err)
		return "", http.StatusServiceUnavailable, reasonLinkUnverified
	case login == "":
		return "", http.StatusForbidden, "not_linked"
	}
	return login, 0, ""
}

// loginWrites is write or higher for a resolved login, through the D4 access check; without one,
// nobody writes.
func (s *Server) loginWrites(ctx context.Context, repository, login string) bool {
	lw, ok := s.Access.(interface {
		CanWriteLogin(ctx context.Context, repository, login string) (bool, error)
	})
	if !ok {
		return false
	}
	can, err := lw.CanWriteLogin(ctx, repository, login)
	return err == nil && can
}

// answerCoordination answers the service's result, mapping its bounded reasons to statuses in one
// place. A refusal names its reason only; an unreasoned failure is logged, never echoed.
func (s *Server) answerCoordination(ctx context.Context, w http.ResponseWriter, out coordination.CoordinationConfirmation, err error) {
	if err == nil {
		reply(w, http.StatusOK, out)
		return
	}
	reason := coordination.Reason(err)
	code := http.StatusConflict
	switch reason {
	case "":
		if s.Log != nil {
			s.Log.ErrorContext(ctx, "coordination: unanswered", "error", err)
		}
		refuse(w, http.StatusServiceUnavailable, "coordination_unavailable")
		return
	case coordination.ErrCoordinationOff.Error(), coordination.ErrParentNotFound.Error():
		code = http.StatusNotFound
	case coordination.ErrRepositoryPermission.Error(), coordination.ErrRepositoryNotAllowed.Error(),
		coordination.ErrPrivateRepository.Error(), coordination.ErrClassNotAllowed.Error():
		code = http.StatusForbidden
	case coordination.ErrAccessUnverified.Error(), coordination.ErrAppUnavailable.Error():
		code = http.StatusServiceUnavailable
	case coordination.ErrInvalidPlan.Error(), coordination.ErrInvalidDependency.Error(), coordination.ErrIncompletePlan.Error(),
		coordination.ErrInvalidConfirmation.Error(), coordination.ErrPlanningCostUnverified.Error(), coordination.ErrBudgetExceeded.Error(),
		coordination.ErrBudgetOverflow.Error(), coordination.ErrRolesMismatch.Error(), coordination.ErrAllocationBelowRun.Error(),
		coordination.ErrAllocationAboveCeiling.Error(), coordination.ErrPolicyMismatch.Error():
		code = http.StatusUnprocessableEntity
	}
	refuse(w, code, reason)
}
