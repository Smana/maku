// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/runrequest"
)

// CoordinationGateway is the factory's coordination routes as the broker forwards to them, with the
// caller's own token (v4 MR4, MR5). runrequest.Factory satisfies it.
type CoordinationGateway interface {
	CoordinationDraft(ctx context.Context, accessToken, parent string, in coordination.CoordinationConfirmation, onBehalfOf string) (coordination.CoordinationConfirmation, error)
	CoordinationConfirm(ctx context.Context, accessToken, parent string, in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error)
	CoordinationRecover(ctx context.Context, accessToken, parent, code string, planVersion int32) error
}

// maxCoordinationWrite bounds a draft or a confirmation body: the plan's own bound and its envelope.
const maxCoordinationWrite = 128 << 10

// coordinationDraftRoute is PUT /api/coordinations/{task}/draft: a human's plan version, forwarded
// to the factory with their own token after the parent room's gate. It opens a draft only: no
// confirmation, issue or run. The factory resolves who they are on GitHub; the broker asserts
// nothing.
func (s *Server) coordinationDraftRoute(w http.ResponseWriter, r *http.Request) {
	p, task, ok := s.coordinationWriter(w, r)
	if !ok {
		return
	}
	var in struct {
		Plan     v1alpha1.CoordinationPlan           `json:"plan"`
		PlanHash string                              `json:"planHash"`
		Planning coordination.PlanningReconciliation `json:"planning"`
	}
	if !decodeCoordination(w, r, &in) {
		return
	}
	out, err := s.CoordinationWrites.CoordinationDraft(r.Context(), p.AccessToken, task,
		coordination.CoordinationConfirmation{Plan: in.Plan, PlanHash: in.PlanHash, Planning: in.Planning}, "")
	answerFactory(w, out, err)
}

// coordinationConfirmRoute is POST /api/coordinations/{task}/confirmations: the parent's
// confirmation or one child's authorization, from the web UI alone (v4: a parent plan is confirmed
// on its room page, never from Slack, the CLI or an agent's tool), forwarded with the human's own
// token. The factory decides the maintainer's authority and answers stale_plan for an old hash.
func (s *Server) coordinationConfirmRoute(w http.ResponseWriter, r *http.Request) {
	p, task, ok := s.coordinationWriter(w, r)
	if !ok {
		return
	}
	if s.WebClient == nil || p.ClientID == "" || p.ClientID != s.WebClient() {
		refuseJSON(w, http.StatusForbidden, "web_only")
		return
	}
	var in coordination.CoordinationConfirmation
	if !decodeCoordination(w, r, &in) {
		return
	}
	if in.Plan.ID != task || (in.Parent == nil) == (in.Child == nil) {
		refuseJSON(w, http.StatusBadRequest, "invalid_confirmation")
		return
	}
	out, err := s.CoordinationWrites.CoordinationConfirm(r.Context(), p.AccessToken, task, in)
	answerFactory(w, out, err)
}

// coordinationRecoverRoute is POST /api/coordinations/{task}/recovery: the parent's one proposed
// recovery (MR11), from the web UI alone, forwarded with the human's own token. The factory decides
// whose authority it needs and binds the current plan version; 204 on success.
func (s *Server) coordinationRecoverRoute(w http.ResponseWriter, r *http.Request) {
	p, task, ok := s.coordinationWriter(w, r)
	if !ok {
		return
	}
	if s.WebClient == nil || p.ClientID == "" || p.ClientID != s.WebClient() {
		refuseJSON(w, http.StatusForbidden, "web_only")
		return
	}
	var in struct {
		Code        string `json:"code"`
		PlanVersion int32  `json:"planVersion"`
	}
	if !decodeCoordination(w, r, &in) {
		return
	}
	if in.PlanVersion < 1 {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	err := s.CoordinationWrites.CoordinationRecover(r.Context(), p.AccessToken, task, in.Code, in.PlanVersion)
	if err != nil {
		answerFactory(w, coordination.CoordinationConfirmation{}, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// coordinationWriter is a human caller with a token, admitted to the {task} parent's room.
func (s *Server) coordinationWriter(w http.ResponseWriter, r *http.Request) (p authn.Principal, task string, ok bool) {
	p, ok = s.principal(w, r)
	if !ok {
		return p, "", false
	}
	task = r.PathValue("task")
	if p.Kind != envelope.ActorHuman || p.Sponsored || p.AccessToken == "" || !envelope.ValidID(task) {
		refuseJSON(w, http.StatusForbidden, "human_only")
		return p, "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	if _, _, _, err := s.readRoom(ctx, task, p); err != nil {
		http.Error(w, err.Error(), roomStatus(err))
		return p, "", false
	}
	return p, task, true
}

func decodeCoordination(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCoordinationWrite))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

// answerFactory writes the factory's answer: its document, or its bounded refusal at its status.
func answerFactory(w http.ResponseWriter, out coordination.CoordinationConfirmation, err error) {
	var ref runrequest.ErrRefused
	switch {
	case err == nil:
		writeJSON(w, out)
	case errors.As(err, &ref):
		refuseJSON(w, ref.Status, ref.Reason)
	case errors.Is(err, runrequest.ErrForbidden):
		refuseJSON(w, http.StatusForbidden, "human_only")
	default:
		refuseJSON(w, http.StatusServiceUnavailable, "coordination_unavailable")
	}
}

func refuseJSON(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
}

// coordinationDraftTool is coordination_draft, a sponsored principal's tool only (the Slack App's):
// coordination_plan's result stored as its parent's draft, forwarded to the factory with the App's
// own token and the user it drafts for, which the factory resolves to a linked maintainer with live
// write on the primary. It drafts only: no confirmation, issue, run or recovery is reachable here,
// and confirmations stay on the parent's room page.
func (s *Server) coordinationDraftTool() mcpTool {
	return mcpTool{name: mcpwire.CoordinationDraft,
		description: "Store a coordination_plan result as its parent's draft, for the user named in on_behalf_of. It confirms nothing: " +
			"the parent and each child are confirmed by their maintainers on the parent's room page, and merges stay on GitHub.",
		schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["plan","planHash","planning","on_behalf_of"],"properties":{` +
			`"plan":{"type":"object"},"planHash":{"type":"string","pattern":"^[0-9a-f]{64}$"},"planning":{"type":"object"},` +
			`"on_behalf_of":{"type":"string","pattern":"` + onBehalfRE.String() + `"}}}`),
		output: json.RawMessage(`{"type":"object","required":["parentTask","planHash","version"],"properties":{"parentTask":{"type":"string"},"planHash":{"type":"string"},"version":{"type":"integer"}}}`),
		call: func(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
			var a struct {
				Plan       v1alpha1.CoordinationPlan           `json:"plan"`
				PlanHash   string                              `json:"planHash"`
				Planning   coordination.PlanningReconciliation `json:"planning"`
				OnBehalfOf string                              `json:"on_behalf_of"`
			}
			if !p.Sponsored || mcpwire.DecodeArgs(args, &a) != nil || !envelope.ValidID(a.Plan.ID) || !onBehalfRE.MatchString(a.OnBehalfOf) {
				return nil, toolError("invalid_arguments: plan, planHash, planning and on_behalf_of, from coordination_plan")
			}
			out, err := s.CoordinationWrites.CoordinationDraft(ctx, p.AccessToken, a.Plan.ID,
				coordination.CoordinationConfirmation{Plan: a.Plan, PlanHash: a.PlanHash, Planning: a.Planning}, a.OnBehalfOf)
			var ref runrequest.ErrRefused
			switch {
			case errors.As(err, &ref):
				return nil, toolError(ref.Reason)
			case err != nil:
				return nil, toolError("coordination_unavailable")
			}
			return map[string]any{"parentTask": out.Plan.ID, "planHash": out.PlanHash, "version": out.Plan.Version}, nil
		}}
}
