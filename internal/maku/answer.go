// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/mcpwire"
)

const answerDescription = "Use when the developer answers a task's question or wants to leave a note for its next run. Shows a form before anything is written."

var answerEntry = map[string]any{
	"name":        mcpwire.Answer,
	"description": answerDescription,
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["task"],` +
		`"properties":{"task":{"type":"string","minLength":1,"description":"` + refHint + `"},` +
		`"text":{"type":"string","maxLength":16384}}}`),
}

type answerArgs struct {
	Task string `json:"task"`
	Text string `json:"text"`
}

type answerTaskShape struct {
	Room   string `json:"room"`
	Status struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
		Issue  *struct {
			Number int    `json:"number"`
			URL    string `json:"url"`
		} `json:"issue"`
	} `json:"status"`
	Question    *scopeQuestion `json:"question"`
	CanStart    bool           `json:"canStart"`
	CanStartWhy string         `json:"canStartWhy"`
}

func (r *Relay) resolveTaskIssue(ctx context.Context, roomID string, ts answerTaskShape) (string, int, error) {
	if ts.Status.Issue != nil && ts.Status.Issue.URL != "" {
		if m := issueURL.FindStringSubmatch(strings.TrimSpace(ts.Status.Issue.URL)); m != nil {
			n, err := strconv.Atoi(m[2])
			if err == nil && repoRE.MatchString(m[1]) && n >= 1 {
				return m[1], n, nil
			}
		}
	}
	rows, err := r.listRows(ctx, map[string]any{"all": true})
	if err != nil {
		return "", 0, err
	}
	for _, row := range rows {
		if row.ID == roomID && repoRE.MatchString(row.Repository) && row.Issue >= 1 {
			return row.Repository, row.Issue, nil
		}
	}
	return "", 0, fmt.Errorf("task %s has no GitHub issue", roomID)
}

// answerTool serves answer {task, text?}: when the task has an open question it confirms the
// developer's one-line answer and writes it under ## Decisions (re-applying factory/ready on yes
// when canStart); otherwise it confirms text and queues it as a note for the next run.
func (r *Relay) answerTool(ctx context.Context, args json.RawMessage) any {
	var in answerArgs
	if mcpwire.DecodeArgs(args, &in) != nil || strings.TrimSpace(in.Task) == "" || len(in.Text) > 16384 {
		return mcpwire.ToolError("invalid_arguments: task is required, and text is at most 16384 bytes")
	}
	roomID, refusal := r.resolveRef(ctx, in.Task)
	if refusal != nil {
		return refusal
	}
	if !roomIDRE.MatchString(roomID) {
		return mcpwire.ToolError("invalid_arguments: task is a task id or reference")
	}

	tText, tSC, tIsErr, err := r.callBroker(ctx, mcpwire.Task, map[string]any{"task": roomID})
	if err != nil {
		return mcpwire.ToolError(explainRelay(err))
	}
	if tIsErr {
		return mcpwire.ToolError(tText)
	}
	var ts answerTaskShape
	if json.Unmarshal(tSC, &ts) != nil {
		return mcpwire.ToolError("unavailable: the broker's task answer is unreadable")
	}

	question := ts.Question
	if question != nil && strings.TrimSpace(question.Text) == "" {
		question = nil
	}
	if question == nil && ts.Status.Reason == "needs_author" && r.Issues != nil {
		if repo, issueNum, rerr := r.resolveTaskIssue(ctx, roomID, ts); rerr == nil {
			if snap, verr := r.Issues.View(ctx, repo, issueNum); verr == nil {
				if q, ok := publicBounceQuestion(snap.Comments, r.FactoryLogin); ok {
					question = &q
				}
			}
		}
	}

	if question == nil {
		return r.answerNote(ctx, roomID, in.Text)
	}
	return r.answerOpenQuestion(ctx, roomID, in.Text, ts, *question)
}

func (r *Relay) answerNote(ctx context.Context, roomID, rawText string) any {
	note := strings.TrimSpace(rawText)
	if note == "" {
		return mcpwire.ToolError("invalid_arguments: text is the note to leave")
	}
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()
	if !elicit {
		return mcpwire.ToolError("needs_confirmation: answer asks the developer to confirm before leaving a note, and this client cannot ask (no elicitation); nothing was written")
	}
	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message": "Leave this note for task " + roomID + "'s next run?\n\n" + note,
		"requestedSchema": map[string]any{
			"type":     "object",
			"required": []string{"send"},
			"properties": map[string]any{
				"send": map[string]any{
					"type":    "boolean",
					"title":   "Leave this note for the next run",
					"default": false,
				},
			},
		},
	})
	if err != nil {
		return mcpwire.ToolError("needs_confirmation: the developer was not asked: " + OneLine(err.Error()) + "; nothing was written")
	}
	var form struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &form) != nil {
		return mcpwire.ToolError("needs_confirmation: the client could not ask the developer; nothing was written")
	}
	send, _ := formBool(form.Content["send"])
	if form.Action != "accept" || !send {
		out, _ := mcpwire.ToolResult(map[string]any{"queued": false, "task": roomID, "why": "the developer did not confirm (" + form.Action + ")"})
		return out
	}

	qText, qSC, qIsErr, err := r.callBroker(ctx, "room_queue", map[string]any{"room": roomID, "text": note})
	if err != nil {
		return mcpwire.ToolError(explainRelay(err))
	}
	if qIsErr {
		return mcpwire.ToolError(qText)
	}
	if before, _, ok := strings.Cut(qText, "\n\nDeprecated: "); ok {
		qText = before
	}
	var payload map[string]any
	if json.Unmarshal(qSC, &payload) != nil || payload == nil {
		payload = map[string]any{"task": roomID, "queued": true}
	}
	payload["task"] = roomID
	out, _ := mcpwire.ToolResult(payload)
	if strings.TrimSpace(qText) != "" {
		out["content"] = []map[string]string{{"type": "text", "text": qText}}
	}
	return out
}

func (r *Relay) answerOpenQuestion(ctx context.Context, roomID, draftText string, ts answerTaskShape, question scopeQuestion) any {
	if r.Issues == nil {
		return mcpwire.ToolError("unavailable: answer has no GitHub issue editor")
	}
	repo, issueNum, err := r.resolveTaskIssue(ctx, roomID, ts)
	if err != nil {
		return mcpwire.ToolError("unavailable: resolve the task's issue: " + OneLine(err.Error()))
	}
	link := fmt.Sprintf("https://github.com/%s/issues/%d", repo, issueNum)

	initial, err := r.Issues.View(ctx, repo, issueNum)
	if err != nil {
		return mcpwire.ToolError("unavailable: read the issue before answering: " + OneLine(err.Error()))
	}
	acceptance := IsAcceptanceQuestion(question.Text)
	previewBody, err := AnswerBody(initial.Body, "<answer>", acceptance)
	if err != nil {
		return mcpwire.ToolError("invalid_decisions: " + OneLine(err.Error()) + "; nothing was written")
	}
	preview := bodyDiff(initial.Body, previewBody)
	recommended := strings.TrimSpace(question.Recommended)
	if acceptance {
		recommended = AcceptanceFromRecommendation(question.Recommended)
	}
	blankNote := ""
	if recommended != "" {
		blankNote = " Leave the answer blank to accept the recommendation."
	}

	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()
	if !elicit {
		return mcpwire.ToolError("needs_confirmation: answer asks the developer for an answer and explicit confirmation, and this client cannot ask (no elicitation); nothing was written")
	}

	offerRestart := ts.CanStart && r.Label != nil
	answerProp := map[string]any{
		"type":  "string",
		"title": "Your one-line answer (blank accepts the recommendation)",
	}
	if trimmed := strings.TrimSpace(draftText); trimmed != "" {
		answerProp["default"] = OneLine(trimmed)
	}
	props := map[string]any{
		"answer": answerProp,
		"send":   map[string]any{"type": "boolean", "title": "Append this answer to the issue", "default": false},
	}
	restartNote := " A maintainer must restart the task afterwards."
	if offerRestart {
		props["restart"] = map[string]any{
			"type":    "boolean",
			"title":   "Restart the task",
			"default": true,
		}
		restartNote = " Keep \"Restart the task\" on to also re-apply " + readyLabel + " with your gh."
	}

	message := "Answer the question on " + link + ".\n\nQuestion:\n" + question.Text +
		"\n\nRecommendation:\n" + question.Recommended +
		"\n\nThe full body diff, with <answer> replaced by your trimmed one-line answer:\n" + preview +
		"\n\n" + strings.TrimSpace(blankNote+restartNote)
	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message":         message,
		"requestedSchema": map[string]any{"type": "object", "required": []string{"answer", "send"}, "properties": props},
	})
	if err != nil {
		return mcpwire.ToolError("needs_confirmation: the developer was not asked: " + OneLine(err.Error()) + "; nothing was written")
	}
	var form struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &form) != nil {
		return mcpwire.ToolError("needs_confirmation: the client could not ask the developer; nothing was written")
	}
	send, _ := formBool(form.Content["send"])
	if form.Action != "accept" || !send {
		out, _ := mcpwire.ToolResult(map[string]any{
			"answered":  false,
			"issue":     link,
			"task":      roomID,
			"restarted": false,
			"next":      "the question is unanswered and nothing was written",
		})
		return out
	}

	var answer string
	if len(form.Content["answer"]) > 0 && string(form.Content["answer"]) != "null" {
		if json.Unmarshal(form.Content["answer"], &answer) != nil {
			return mcpwire.ToolError("invalid_answer: answer is one line of text; nothing was written")
		}
	}
	if strings.TrimSpace(answer) == "" && recommended != "" {
		answer = recommended
	}
	newBody, err := AnswerBody(initial.Body, answer, acceptance)
	if err != nil {
		return mcpwire.ToolError(OneLine(err.Error()) + "; nothing was written")
	}

	current, err := r.Issues.View(ctx, repo, issueNum)
	if err != nil {
		return mcpwire.ToolError("unavailable: re-read the issue before answering: " + OneLine(err.Error()) + "; nothing was written")
	}
	if current.Body != initial.Body || !slices.Equal(current.Labels, initial.Labels) || !current.UpdatedAt.Equal(initial.UpdatedAt) {
		return mcpwire.ToolError("issue_changed: the issue body, labels, or updatedAt changed while the answer was open; nothing was written")
	}
	if err := r.Issues.EditBody(ctx, repo, issueNum, newBody); err != nil {
		return mcpwire.ToolError("not_answered: edit the issue body: " + OneLine(err.Error()))
	}

	if !offerRestart {
		out, _ := mcpwire.ToolResult(map[string]any{
			"answered":  true,
			"issue":     link,
			"task":      roomID,
			"restarted": false,
			"next":      "a maintainer must restart the task",
		})
		return out
	}

	restart, chosen := formBool(form.Content["restart"])
	if !chosen || !restart {
		out, _ := mcpwire.ToolResult(map[string]any{
			"answered":  true,
			"issue":     link,
			"task":      roomID,
			"restarted": false,
			"next":      "re-apply " + readyLabel + " to restart the task",
		})
		return out
	}

	if r.Unlabel != nil {
		_ = r.Unlabel(ctx, repo, issueNum, readyLabel)
	}
	if err := r.Label(ctx, repo, issueNum, readyLabel); err != nil {
		out, _ := mcpwire.ToolResult(map[string]any{
			"answered":  true,
			"issue":     link,
			"task":      roomID,
			"restarted": false,
			"next":      "answered, but could not re-apply " + readyLabel + ": " + OneLine(err.Error()) + "; re-apply it yourself",
		})
		return out
	}
	out, _ := mcpwire.ToolResult(map[string]any{
		"answered":  true,
		"issue":     link,
		"task":      roomID,
		"restarted": true,
	})
	return out
}
