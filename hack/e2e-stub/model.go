// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	plannerRole    = "planner"
	classifierRole = "classifier"
	scoperRole     = "scoper"
	otherRole      = "other"

	handoffTool = "room-broker__room_handoff"
	finishTool  = "finish"
	fullCommit  = "4be1c9d0e8a7f6b5c4d3e2f1a0b9c8d7e6f5a4b3"
)

type modelMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []modelToolCall `json:"tool_calls"`
}

// UnmarshalJSON accepts OpenAI's string content and OpenHands SDK's list of text parts.
func (m *modelMessage) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []modelToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	m.Role, m.ToolCalls, m.Content = wire.Role, wire.ToolCalls, ""
	if len(wire.Content) == 0 || string(wire.Content) == "null" {
		return nil
	}
	if err := json.Unmarshal(wire.Content, &m.Content); err == nil {
		return nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(wire.Content, &parts); err != nil {
		return err
	}
	for _, part := range parts {
		m.Content += part.Text
	}
	return nil
}

type modelToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    *int   `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type completionRequest struct {
	Stream         bool           `json:"stream"`
	Messages       []modelMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

func (r completionRequest) role() string {
	if len(r.Messages) == 0 {
		return otherRole
	}
	system := r.Messages[0]
	if system.Role == "system" && r.ResponseFormat.Type == "json_object" && strings.Contains(system.Content, "You plan the issues a repository's maintainers will work from") {
		return plannerRole
	}
	if system.Role == "system" && r.ResponseFormat.Type == "json_schema" && strings.Contains(system.Content, "You label one message from a person in a chat thread") {
		return classifierRole
	}
	// OpenHands carries the factory brief in its user turn, not in the SDK's system prompt.
	for _, message := range r.Messages {
		if strings.Contains(message.Content, "agent-factory scoper") && strings.Contains(message.Content, "You are the scoper") {
			return scoperRole
		}
	}
	return otherRole
}

func (r completionRequest) handedOff() bool {
	for _, message := range r.Messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == handoffTool || call.Function.Name == "room_handoff" {
				return true
			}
		}
	}
	return false
}

func newToolCall(name, arguments string) modelToolCall {
	call := modelToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call
}

func finishCall() modelToolCall {
	return newToolCall(finishTool, `{"message":"e2e-stub: role finished"}`)
}

func scoperCall(ready bool) modelToolCall {
	scope := map[string]any{
		"state":       "question",
		"task":        "Document the requested change and its verification, changing nothing outside the documentation.",
		"touches":     []string{"docs/"},
		"leave":       []string{"docs/superpowers/"},
		"plan":        []string{"Read the requested change.", "Document it and run the agreed verification."},
		"assumptions": []string{"Run task check to prove the change."},
		"acceptance": []map[string]string{
			{"check": "task check", "proves": "the repository's verification succeeds"},
		},
		"question": "Which command or observation proves the change is done?",
	}
	if ready {
		scope["state"] = "ready"
		delete(scope, "question")
	}
	arguments, err := json.Marshal(map[string]any{
		"toRole": "implementer", "summary": "The e2e scoper bounded the task.", "commit": fullCommit, "scope": scope,
	})
	if err != nil {
		panic("e2e-stub: marshal fixed scope: " + err.Error())
	}
	return newToolCall("room_handoff", string(arguments))
}

func plannerDraft() string {
	draft := map[string]any{
		"repository": "owner/repo",
		"issues": []map[string]any{{
			"title": "docs: document the verification command",
			"body": "## What is wrong\nThe requested change has no agreed verification command.\n" +
				"[NEEDS CLARIFICATION: g1 which command verifies the change?]\n" +
				"## What it should be\nThe README MUST name the command that verifies the change.\n" +
				"## Acceptance check\nThe documented verification command MUST exit 0.",
			"class": "docs",
		}},
		"gaps": []map[string]any{{
			"id": "g1", "section": "what-is-wrong", "text": "which command verifies the change?",
			"recommended": map[string]string{"answer": "task check", "why": "it runs the repository's complete verification"},
		}},
	}
	content, err := json.Marshal(draft)
	if err != nil {
		panic("e2e-stub: marshal fixed planner draft: " + err.Error())
	}
	return string(content)
}

func writeCompletion(w http.ResponseWriter, stream bool, call *modelToolCall, content string) {
	created := time.Now().Unix()
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		var delta map[string]any
		finishReason := "stop"
		if call != nil {
			streamCall := *call
			index := 0
			streamCall.Index = &index
			delta = map[string]any{"role": "assistant", "content": "", "tool_calls": []modelToolCall{streamCall}}
			finishReason = "tool_calls"
		} else {
			delta = map[string]any{"role": "assistant", "content": content}
		}
		chunks := []map[string]any{
			{"index": 0, "delta": delta, "finish_reason": nil},
			{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason},
		}
		for _, choice := range chunks {
			raw, err := json.Marshal(map[string]any{
				"id": "e2e-stub", "object": "chat.completion.chunk", "created": created, "model": "e2e-stub", "choices": []any{choice},
			})
			if err != nil {
				panic("e2e-stub: marshal fixed stream: " + err.Error())
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}

	finishReason := "stop"
	message := map[string]any{"role": "assistant", "content": content}
	if call != nil {
		finishReason = "tool_calls"
		message["content"] = ""
		message["tool_calls"] = []modelToolCall{*call}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": "e2e-stub", "object": "chat.completion", "created": created, "model": "e2e-stub",
		"choices": []any{map[string]any{"index": 0, "finish_reason": finishReason, "message": message}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

// classifierAnswer is intent_classify's closed object: a status question, confidently.
const classifierAnswer = `{"intent":"status","text":"","confidence":0.9}`

func (s *Stub) completion(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var request completionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	s.modelCalls.Add(1)
	role := request.role()
	s.record(role, r, raw)
	switch role {
	case classifierRole:
		s.classifierCalls.Add(1)
		writeCompletion(w, request.Stream, nil, classifierAnswer)
	case plannerRole:
		s.plannerCalls.Add(1)
		writeCompletion(w, request.Stream, nil, plannerDraft())
	case scoperRole:
		s.scoperCalls.Add(1)
		if request.handedOff() {
			call := finishCall()
			writeCompletion(w, request.Stream, &call, "")
			return
		}
		ready := s.scoperHandoffs.Add(1)%2 == 0
		call := scoperCall(ready)
		writeCompletion(w, request.Stream, &call, "")
	default:
		call := finishCall()
		writeCompletion(w, request.Stream, &call, "")
	}
}
