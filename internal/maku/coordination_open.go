// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/mcpwire"
)

// coordination_open's tools/list entry: a plan from coordination_plan stored as the parent's draft,
// for the developer to confirm on the parent's room page. Nothing in its input confirms, files,
// merges or recovers.
var coordinationOpenEntry = map[string]any{
	"name": toolCoordinationOpen,
	"description": "Open a coordination plan from coordination_plan as its parent's draft, as the developer, and return the " +
		"parent room's URL. It confirms nothing: the parent and each child are confirmed by their maintainers on that " +
		"room page only. Pass coordination_plan's plan, its hash as planHash, and its planning record.",
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["confirmation"],"properties":{` +
		`"confirmation":{"type":"object","additionalProperties":false,"required":["plan","planHash","planning"],"properties":{` +
		`"plan":{"type":"object"},"planHash":{"type":"string"},"planning":{"type":"object"}}}}}`),
}

// coordinationOpen is coordination_open: the draft only, through the broker as the developer.
func (r *Relay) coordinationOpen(ctx context.Context, c localCall) any {
	var a struct {
		Confirmation json.RawMessage `json:"confirmation"`
	}
	if mcpwire.DecodeArgs(c.Args, &a) != nil {
		return mcpwire.ToolError("invalid_arguments: confirmation is coordination_plan's {plan, planHash, planning}")
	}
	var in struct {
		Plan     v1alpha1.CoordinationPlan           `json:"plan"`
		PlanHash string                              `json:"planHash"`
		Planning coordination.PlanningReconciliation `json:"planning"`
	}
	dec := json.NewDecoder(bytes.NewReader(a.Confirmation))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || !coordRoomRE.MatchString(in.Plan.ID) {
		return mcpwire.ToolError("invalid_arguments: confirmation is coordination_plan's {plan, planHash, planning}, and nothing else")
	}
	cl := Client{URL: r.URL, HC: r.HC, Token: r.Token}
	if _, err := cl.CoordinationDraft(ctx, in.Plan.ID, coordination.CoordinationConfirmation{Plan: in.Plan, PlanHash: in.PlanHash, Planning: in.Planning}); err != nil {
		return mcpwire.ToolError("not_opened: " + OneLine(err.Error()))
	}
	out, _ := mcpwire.ToolResult(map[string]string{"parentTask": in.Plan.ID, "roomURL": strings.TrimRight(r.URL, "/") + "/r/" + in.Plan.ID})
	return out
}
