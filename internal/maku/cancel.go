// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"

	"github.com/Smana/maku/internal/mcpwire"
)

// toolTaskCancel is the broker's unlisted cancel, which only this relay calls, after its form.
const toolTaskCancel = "task_cancel"

const cancelDescription = "Use when the developer wants to stop a task now. Shows a form before anything is stopped; " +
	"discard also closes the task's pull request, deletes its agent/<task> branch and closes its issue as not planned " +
	"(a GitHub App cannot delete an issue). Works when the task's issue was deleted."

var cancelEntry = map[string]any{
	"name":        mcpwire.Cancel,
	"description": cancelDescription,
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["task"],` +
		`"properties":{"task":{"type":"string","minLength":1,"description":"` + refHint + `"},` +
		`"discard":{"type":"boolean","description":"also close the pull request, delete the branch and close the issue as not planned"}}}`),
}

type cancelArgs struct {
	Task    string `json:"task"`
	Discard bool   `json:"discard"`
}

// cancelTool asks the developer, in a form, to cancel or discard the task, and only on their yes
// calls the broker's task_cancel (#644). The form is the confirmation: with no form, nothing stops.
func (r *Relay) cancelTool(ctx context.Context, args json.RawMessage) any {
	var in cancelArgs
	if mcpwire.DecodeArgs(args, &in) != nil || in.Task == "" {
		return mcpwire.ToolError("invalid_arguments: task is required; discard is true or false")
	}
	roomID, refusal := r.resolveRef(ctx, in.Task)
	if refusal != nil {
		return refusal
	}
	if !roomIDRE.MatchString(roomID) {
		return mcpwire.ToolError("invalid_arguments: task is a task id or reference")
	}
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()
	if !elicit {
		return mcpwire.ToolError("needs_confirmation: cancel asks the developer in a form, and this client cannot ask (no elicitation); " +
			"nothing was stopped: run maku cancel " + roomID + " in a terminal, or use the room page")
	}
	what := "Cancel task " + roomID + "? Its runs stop now and its room ends."
	if in.Discard {
		what = "Discard task " + roomID + "? Its runs stop now, its pull request is closed, its agent/" + roomID +
			" branch deleted and its issue closed as not planned (a GitHub App cannot delete an issue)."
	}
	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message": what,
		"requestedSchema": map[string]any{
			"type":     "object",
			"required": []string{"confirm"},
			"properties": map[string]any{
				"confirm": map[string]any{"type": "boolean", "title": "Yes, stop this task", "default": false},
				"discard": map[string]any{"type": "boolean", "title": "Also discard its pull request, branch and issue", "default": in.Discard},
			},
		},
	})
	if err != nil {
		return mcpwire.ToolError("needs_confirmation: the developer was not asked: " + OneLine(err.Error()) + "; nothing was stopped")
	}
	var form struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &form) != nil {
		return mcpwire.ToolError("needs_confirmation: the client could not ask the developer; nothing was stopped")
	}
	confirm, _ := formBool(form.Content["confirm"])
	if form.Action != "accept" || !confirm {
		out, _ := mcpwire.ToolResult(map[string]any{"task": roomID, "result": "not_cancelled", "why": "the developer did not confirm (" + form.Action + ")"})
		return out
	}
	discard, ok := formBool(form.Content["discard"])
	if !ok {
		discard = in.Discard
	}
	text, sc, isErr, err := r.callBroker(ctx, toolTaskCancel, map[string]any{"room": roomID, "discard": discard})
	if err != nil {
		return mcpwire.ToolError(explainRelay(err))
	}
	if isErr {
		return mcpwire.ToolError(text)
	}
	var payload map[string]any
	if json.Unmarshal(sc, &payload) != nil || payload == nil {
		payload = map[string]any{"task": roomID}
	}
	if discard {
		payload["note"] = "GitHub Apps cannot delete issues: the issue is closed as not planned; a repository admin can delete it"
	}
	out, _ := mcpwire.ToolResult(payload)
	return out
}
