// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/httpx"
)

// coordinationCode is the shape of every refusal the factory's coordination routes write: a bounded
// snake_case reason (coordination.Reason, or the route's own). Anything else is "refused", so the
// factory's free text never reaches a page or a model.
var coordinationCode = regexp.MustCompile(`^[a-z][a-z_]{0,47}$`)

// CoordinationDraft is PUT /v1/coordinations/{parent}/draft (v4 MR4) with the caller's own token:
// a plan version stored on its parent, never a confirmation. onBehalfOf is a sponsored principal's
// user; the factory refuses it from anyone else. The broker asserts no login or actor.
func (f Factory) CoordinationDraft(ctx context.Context, accessToken, parent string, in coordination.CoordinationConfirmation,
	onBehalfOf string) (coordination.CoordinationConfirmation, error) {
	body := struct {
		Plan       v1alpha1.CoordinationPlan           `json:"plan"`
		PlanHash   string                              `json:"planHash"`
		Planning   coordination.PlanningReconciliation `json:"planning"`
		OnBehalfOf string                              `json:"onBehalfOf,omitempty"`
	}{in.Plan, in.PlanHash, in.Planning, onBehalfOf}
	return f.coordination(ctx, http.MethodPut, accessToken, parent, "draft", body)
}

// CoordinationConfirm is POST /v1/coordinations/{parent}/confirmations (MR5) with the caller's own
// token: exactly one of the parent's confirmation or one child's authorization, decided by the
// factory against that repository's maintainer and its current write.
func (f Factory) CoordinationConfirm(ctx context.Context, accessToken, parent string,
	in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error) {
	return f.coordination(ctx, http.MethodPost, accessToken, parent, "confirmations", in)
}

func (f Factory) coordination(ctx context.Context, method, accessToken, parent, route string, payload any) (coordination.CoordinationConfirmation, error) {
	var out coordination.CoordinationConfirmation
	if accessToken == "" { // never an unauthenticated or asserted request (C4)
		return out, ErrForbidden
	}
	if !envelope.ValidID(parent) {
		return out, fmt.Errorf("runrequest: %q is no task id", parent)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("runrequest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.URL+"/v1/coordinations/"+parent+"/"+route, bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("runrequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	hc, err := f.client()
	if err != nil {
		return out, err
	}
	if f.HC == nil {
		defer hc.CloseIdleConnections()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return out, fmt.Errorf("runrequest: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxResponse)
	if err != nil {
		return out, fmt.Errorf("runrequest: factory: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			return out, fmt.Errorf("runrequest: factory answer: %w", err)
		}
		return out, nil
	}
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &refusal)
	reason := "refused"
	if coordinationCode.MatchString(refusal.Error) {
		reason = refusal.Error
	}
	return out, ErrRefused{Status: resp.StatusCode, Reason: reason}
}
