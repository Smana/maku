// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/mcpwire"
)

const startDescription = "Use when the plan is ready. Shows the developer a form; only their answer files the issue and starts the work."

var startEntry = map[string]any{
	"name":        mcpwire.Start,
	"description": startDescription,
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],` +
		`"properties":{"id":{"type":"string","minLength":1,"maxLength":128}}}`),
}

type startArgs struct {
	ID string `json:"id"`
}

type handoffCheckShape struct {
	planShape
	Workflows  []string `json:"workflows"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Repository string   `json:"repository"`
	view       planView
	workflow   string
}

func (h *handoffCheckShape) governed() *governed {
	return &governed{planShape: h.planShape, view: h.view}
}

func (h *handoffCheckShape) draft() draftShape {
	repo := h.Repository
	if repo == "" {
		repo = h.view.Repository
	}
	title := h.Title
	if title == "" && len(h.view.Issues) > 0 {
		title = h.view.Issues[0].Title
	}
	body := h.Body
	if body == "" && len(h.view.Issues) > 0 {
		body = h.view.Issues[0].Body
	}
	return draftShape{
		Repo:        repo,
		Title:       title,
		Body:        body,
		CanStart:    h.CanStart,
		CanStartWhy: h.CanStartWhy,
	}
}

func parseRenderedWorkflow(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "Workflow "); ok {
			if wf := strings.TrimSpace(after); wf != "" {
				return wf
			}
		}
	}
	return ""
}

func (r *Relay) checkHandoff(ctx context.Context, id, workflow string) (*handoffCheckShape, any) {
	callArgs := map[string]any{"id": id}
	if workflow != "" {
		callArgs["workflow"] = workflow
	}
	text, sc, isErr, err := r.callBroker(ctx, "handoff_check", callArgs)
	if err != nil {
		return nil, mcpwire.ToolError(explainRelay(err))
	}
	if isErr {
		return nil, mcpwire.ToolError(text)
	}
	var h handoffCheckShape
	if json.Unmarshal(sc, &h) != nil || !present(h.Plan) || h.Hash == "" {
		return nil, mcpwire.ToolError("unavailable: the broker's handoff_check answer is unreadable")
	}
	if json.Unmarshal(h.Plan, &h.view) != nil {
		return nil, mcpwire.ToolError("unavailable: the plan handoff_check validated is unreadable")
	}
	d := h.draft()
	if !repoRE.MatchString(d.Repo) || len(h.view.Issues) == 0 || !titleRE.MatchString(d.Title) || strings.TrimSpace(d.Body) == "" {
		return nil, mcpwire.ToolError("unavailable: the plan handoff_check validated is not an issue this tool can file")
	}
	switch {
	case h.view.Issues[0].Profile != "":
		h.workflow = h.view.Issues[0].Profile
	case parseRenderedWorkflow(text) != "":
		h.workflow = parseRenderedWorkflow(text)
	case h.view.Issues[0].Suggestion != nil && h.view.Issues[0].Suggestion.Profile != "":
		h.workflow = h.view.Issues[0].Suggestion.Profile
	case len(h.Workflows) > 0:
		h.workflow = h.Workflows[0]
	default:
		h.workflow = "default"
	}
	return &h, nil
}

// startTool serves start {id}: it re-validates the stored handoff plan on the broker, shows the
// developer a confirmation form (UX3, UX14, UX15), re-validates on workflow change, and files and
// starts the work with the developer's gh only on their explicit yes.
func (r *Relay) startTool(ctx context.Context, args json.RawMessage) any {
	var a startArgs
	if mcpwire.DecodeArgs(args, &a) != nil || strings.TrimSpace(a.ID) == "" || len(a.ID) > 128 {
		return mcpwire.ToolError("invalid_arguments: id is required, and no field outside {id} is taken")
	}
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()

	checked, refused := r.checkHandoff(ctx, a.ID, "")
	if refused != nil {
		return refused
	}
	draft := checked.draft()
	if !elicit || r.File == nil {
		msg := needsConfirmation + ": start asks the developer to confirm, and this client cannot ask (no elicitation); " +
			"show them the issue and let them file it with gh issue create\n" + startFallbackNext(draft)
		return mcpwire.ToolError(msg)
	}

	offer := draft.CanStart && r.Label != nil
	props := map[string]any{
		"file": map[string]any{
			"type":        "boolean",
			"title":       "File this issue",
			"default":     false,
			"description": "Filed with your gh, as you",
		},
	}
	tail := "It gets no label: label it " + readyLabel + " yourself to start the work."
	if offer {
		props["start"] = map[string]any{
			"type":        "boolean",
			"title":       "Start now",
			"default":     true,
			"description": "Also labels it " + readyLabel + " with your gh, which starts the work",
		}
		tail = "Keep \"Start now\" on to also label it " + readyLabel + " with your gh, which starts the work."
	} else if why := OneLine(draft.CanStartWhy); why != "" {
		tail = "Work cannot be started from here (" + why + "). It gets no label: label it " + readyLabel + " yourself to start the work."
	}

	defaultWF := OneLine(checked.workflow)
	if len(checked.Workflows) > 1 {
		choices := make([]string, 0, len(checked.Workflows)+1)
		if defaultWF != "" && !slices.Contains(checked.Workflows, defaultWF) {
			choices = append(choices, defaultWF)
		}
		choices = append(choices, checked.Workflows...)
		props["workflow"] = map[string]any{
			"type":    "string",
			"title":   "Workflow",
			"enum":    choices,
			"default": defaultWF,
		}
	}

	budgetLine := "Budget none"
	if checked.view.Budget.CeilingUSD > 0 {
		budgetLine = "Budget up to " + dollars(checked.view.Budget.CeilingUSD)
	}
	meta := fmt.Sprintf("Workflow %s\n%s\n\n", defaultWF, budgetLine)
	if n := len(checked.view.Issues); n > 1 {
		meta += fmt.Sprintf("The plan holds %d issues: this files the first.\n\n", n)
	}

	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message":         fmt.Sprintf("File this issue on %s as you, with gh? %s\n\n%s%s\n\n%s", draft.Repo, tail, meta, draft.Title, draft.Body),
		"requestedSchema": map[string]any{"type": "object", "required": []string{"file"}, "properties": props},
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return mcpwire.ToolError(needsConfirmation + "; the client did not show the confirmation form (no answer within " +
			elicitWait.String() + "), so show them the issue and let them file it with gh issue create\n" + startFallbackNext(draft))
	}
	if err != nil {
		return mcpwire.ToolError("not_filed: the developer was not asked: " + err.Error() + "\n" + startFallbackNext(draft))
	}
	var res struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &res) != nil {
		return mcpwire.ToolError("not_filed: the client could not ask the developer\n" + startFallbackNext(draft))
	}
	file, _ := formBool(res.Content["file"])
	if res.Action != "accept" || !file {
		out, _ := mcpwire.ToolResult(map[string]any{"filed": false, "why": "the developer did not confirm (" + res.Action + ")"})
		return out
	}

	if len(checked.Workflows) > 1 {
		if chosenWF := formString(res.Content["workflow"]); chosenWF != "" && chosenWF != defaultWF {
			rechecked, ref := r.checkHandoff(ctx, a.ID, chosenWF)
			if ref != nil {
				return ref
			}
			checked = rechecked
			draft = checked.draft()
			offer = draft.CanStart && r.Label != nil
		}
	}

	startNow, chosen := formBool(res.Content["start"])
	if offer && !chosen {
		r.log().Warn("start: the form answer has no usable start; treated as not started", "start", string(res.Content["start"]))
	}
	url, err := r.File(ctx, draft.Repo, draft.Title, draft.Body)
	if err != nil {
		return mcpwire.ToolError("not_filed: " + err.Error())
	}
	if !offer || !startNow {
		next := "label it " + readyLabel + " to start the work: your decision"
		if offer && !chosen {
			next = "start was not chosen (the form came back without it), so the work is not started: ask the developer whether to start it, and label it " + readyLabel + " only on their yes"
		}
		out, _ := mcpwire.ToolResult(map[string]any{"filed": true, "url": url, "started": false, "next": next})
		return out
	}
	return r.startFromHandoff(ctx, draft.Repo, url, checked.governed().planLabels())
}

func formString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func startFallbackNext(d draftShape) string {
	start := "to start the work they add the label " + readyLabel + " themselves, or you call start again and they choose Start now in its form"
	if !d.CanStart {
		start = "work cannot be started from here; they label the issue " + readyLabel + " themselves when it can run"
	}
	return "next: show the developer this one command and let them run it; it only files the issue, and nothing is filed until they do. " +
		"Never add a label yourself: " + start + ":\n" + fallbackCommand(d)
}

func (r *Relay) startFromHandoff(ctx context.Context, repo, url string, labels []string) any {
	result := map[string]any{"filed": true, "url": url, "started": false}
	n, err := issueNumber(url, repo)
	for _, label := range labels {
		if err != nil || r.ClassLabel == nil {
			break
		}
		if cerr := r.ClassLabel(ctx, repo, n, label); cerr != nil {
			result["why"] = "filed without labels, so not started: " + label + ": " + OneLine(cerr.Error()) +
				"; label it " + strings.Join(labels, ", ") + ", then " + readyLabel + ", yourself"
			out, _ := mcpwire.ToolResult(result)
			return out
		}
	}
	if err == nil {
		err = r.Label(ctx, repo, n, readyLabel)
	}
	if err != nil {
		result["why"] = "filed without the label, so not started: " + OneLine(err.Error()) + "; label it " + readyLabel + " yourself"
		out, _ := mcpwire.ToolResult(result)
		return out
	}
	result["started"] = true
	id := r.awaitRoom(ctx, repo, n)
	if id == "" {
		result["why"] = "not started yet: the factory's comment on the issue says why"
		out, _ := mcpwire.ToolResult(result)
		return out
	}
	result["task"] = id
	result["room"] = id
	out, _ := mcpwire.ToolResult(result)
	if text, sc, isErr, err := r.callBroker(ctx, mcpwire.Task, map[string]string{"task": id}); err == nil && !isErr && text != "" {
		var meta struct {
			APIVersion string `json:"apiVersion"`
		}
		if json.Unmarshal(sc, &meta) == nil && meta.APIVersion == "task/v1" {
			out["content"] = []map[string]string{{"type": "text", "text": "Filed " + url + " · task " + id + "\n\n" + text}}
		}
	}
	return out
}
