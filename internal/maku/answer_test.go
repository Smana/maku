// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/mcpwire"
)

type answerBroker struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []toolCall
	rooms    []map[string]any
	taskData map[string]any
}

func newAnswerBroker(t *testing.T, rooms []map[string]any, taskData map[string]any) *answerBroker {
	t.Helper()
	ab := &answerBroker{rooms: rooms, taskData: taskData}
	ab.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		if json.NewDecoder(r.Body).Decode(&m) != nil || len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var res any
		switch m.Method {
		case "initialize":
			res = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/call":
			var c struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &c)
			ab.mu.Lock()
			if c.Name != "rooms_list" {
				ab.calls = append(ab.calls, toolCall{Name: c.Name, Args: c.Arguments})
			}
			switch c.Name {
			case "rooms_list":
				res = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": "rooms"}},
					"structuredContent": map[string]any{"rooms": ab.rooms},
				}
			case mcpwire.Task:
				res = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": "task card"}},
					"structuredContent": ab.taskData,
				}
			case "room_queue":
				res = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": `{"queued":4,"room":"3kq7x2ma"}` + "\n\nDeprecated: use answer; this tool is removed in the next release."}},
					"structuredContent": map[string]any{
						"room":   c.Arguments["room"],
						"queued": 4,
					},
				}
			default:
				res = map[string]any{"content": []any{map[string]any{"type": "text", "text": "{}"}}, "structuredContent": map[string]any{}}
			}
			ab.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
	}))
	t.Cleanup(ab.srv.Close)
	return ab
}

func (ab *answerBroker) seenCalls() []toolCall {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	return append([]toolCall(nil), ab.calls...)
}

func answerSession(t *testing.T, ab *answerBroker, issues IssueEditor, l, unl *labeling) session {
	t.Helper()
	b := &mcpBroker{srv: ab.srv}
	s := relayTo(t, b, tok, func(r *Relay) {
		r.Issues = issues
		if l != nil {
			r.Label = l.label
		}
		if unl != nil {
			r.Unlabel = unl.label
		}
		r.After = func(time.Duration) <-chan time.Time { return time.After(5 * time.Millisecond) }
	})
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
	return s
}

func openQuestionTask(canStart bool) map[string]any {
	return map[string]any{
		"apiVersion": "task/v1",
		"room":       "3kq7x2ma",
		"status": map[string]any{
			"phase": "Active",
			"issue": map[string]any{
				"number": 7,
				"url":    "https://github.com/Smana/a/issues/7",
			},
		},
		"question": map[string]any{
			"text":        "Which OS should the test target?",
			"recommended": "Target Linux amd64 only",
		},
		"canStart": canStart,
	}
}

func TestAnswerOpenQuestionWritesDecision(t *testing.T) {
	ab := newAnswerBroker(t, []map[string]any{{"id": "3kq7x2ma", "repository": "Smana/a", "issue": 7}}, openQuestionTask(true))
	issues := &fakeIssueEditor{
		snapshots: []IssueSnapshot{
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
		},
	}
	l := &labeling{}
	s := answerSession(t, ab, issues, l, nil)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Answer, "arguments": map[string]any{"task": "3kq7x2ma"}}})
	ask := s.next(t)
	if ask["method"] != "elicitation/create" {
		t.Fatalf("expected elicitation/create, got %v", ask)
	}
	msg := ask["params"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "Which OS should the test target?") ||
		!strings.Contains(msg, "Target Linux amd64 only") ||
		!strings.Contains(msg, "+ D1: <answer>") {
		t.Fatalf("form message missing question, recommendation, or diff:\n%s", msg)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"answer": "Use Linux arm64", "send": true, "restart": false},
	}})
	res := s.next(t)
	if res["result"].(map[string]any)["isError"] == true {
		t.Fatalf("answer failed: %v", res)
	}
	wantBody := needAnswerBody + "\n\n## Decisions\n\nD1: Use Linux arm64"
	if len(issues.edits) != 1 || issues.edits[0].body != wantBody {
		t.Fatalf("edits = %+v, want body %q", issues.edits, wantBody)
	}
}

func TestAnswerRestartDefaultsYes(t *testing.T) {
	ab := newAnswerBroker(t, []map[string]any{{"id": "3kq7x2ma", "repository": "Smana/a", "issue": 7}}, openQuestionTask(true))
	issues := &fakeIssueEditor{
		snapshots: []IssueSnapshot{
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
		},
	}
	l, unl := &labeling{}, &labeling{}
	s := answerSession(t, ab, issues, l, unl)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Answer, "arguments": map[string]any{"task": "3kq7x2ma"}}})
	ask := s.next(t)
	props := ask["params"].(map[string]any)["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	restartProp, ok := props["restart"].(map[string]any)
	if !ok || restartProp["title"] != "Restart the task" || restartProp["default"] != true {
		t.Fatalf("restart prop = %v", props["restart"])
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"answer": "", "send": true, "restart": true},
	}})
	res := s.next(t)
	if res["result"].(map[string]any)["isError"] == true {
		t.Fatalf("answer failed: %v", res)
	}
	if len(unl.calls) != 1 || unl.calls[0] != "Smana/a#7+factory/ready" {
		t.Fatalf("Unlabel calls = %v, want [Smana/a#7+factory/ready]", unl.calls)
	}
	if len(l.calls) != 1 || l.calls[0] != "Smana/a#7+factory/ready" {
		t.Fatalf("Label calls = %v, want [Smana/a#7+factory/ready]", l.calls)
	}
}

func TestAnswerWithoutStandingNamesWhoRestarts(t *testing.T) {
	ab := newAnswerBroker(t, []map[string]any{{"id": "3kq7x2ma", "repository": "Smana/a", "issue": 7}}, openQuestionTask(false))
	issues := &fakeIssueEditor{
		snapshots: []IssueSnapshot{
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
			{Body: needAnswerBody, Labels: []string{needsAuthorLabel}, UpdatedAt: needAnswerUpdated},
		},
	}
	l := &labeling{}
	s := answerSession(t, ab, issues, l, nil)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Answer, "arguments": map[string]any{"task": "3kq7x2ma"}}})
	ask := s.next(t)
	props := ask["params"].(map[string]any)["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	if _, hasRestart := props["restart"]; hasRestart {
		t.Fatalf("restart field must be absent when canStart is false: %v", props)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"answer": "Use Linux amd64", "send": true},
	}})
	res := s.next(t)
	if len(issues.edits) != 1 || !strings.Contains(issues.edits[0].body, "D1: Use Linux amd64") {
		t.Fatalf("edits = %+v", issues.edits)
	}
	if len(l.calls) != 0 {
		t.Fatalf("Label calls = %v, want none", l.calls)
	}
	sc := res["result"].(map[string]any)["structuredContent"].(map[string]any)
	next, _ := sc["next"].(string)
	if !strings.Contains(strings.ToLower(next), "maintainer") || !strings.Contains(strings.ToLower(next), "restart") {
		t.Fatalf("next = %q, want mention that a maintainer must restart the task", next)
	}
}

func TestAnswerNoQuestionQueuesNote(t *testing.T) {
	noQuestion := map[string]any{
		"apiVersion": "task/v1",
		"room":       "3kq7x2ma",
		"status":     map[string]any{"phase": "Active"},
		"canStart":   true,
	}
	ab := newAnswerBroker(t, []map[string]any{{"id": "3kq7x2ma", "repository": "Smana/a", "issue": 7}}, noQuestion)
	issues := &fakeIssueEditor{}
	l := &labeling{}
	s := answerSession(t, ab, issues, l, nil)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Answer, "arguments": map[string]any{"task": "3kq7x2ma", "text": "check the edge case on empty slices"}}})
	ask := s.next(t)
	if ask["method"] != "elicitation/create" {
		t.Fatalf("expected elicitation/create before queuing note, got %v", ask)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"send": true},
	}})
	res := s.next(t)
	if res["result"].(map[string]any)["isError"] == true {
		t.Fatalf("answer note failed: %v", res)
	}
	if len(issues.edits) != 0 || issues.views != 0 || len(l.calls) != 0 {
		t.Fatalf("issue editor or labeler was touched: views=%d edits=%v labels=%v", issues.views, issues.edits, l.calls)
	}
	var queueCalls []toolCall
	for _, c := range ab.seenCalls() {
		if c.Name == "room_queue" {
			queueCalls = append(queueCalls, c)
		}
	}
	if len(queueCalls) != 1 || queueCalls[0].Args["room"] != "3kq7x2ma" || queueCalls[0].Args["text"] != "check the edge case on empty slices" {
		t.Fatalf("room_queue calls = %+v", queueCalls)
	}
}

func TestAnswerNoQuestionNoText(t *testing.T) {
	noQuestion := map[string]any{
		"apiVersion": "task/v1",
		"room":       "3kq7x2ma",
		"status":     map[string]any{"phase": "Active"},
		"canStart":   true,
	}
	ab := newAnswerBroker(t, []map[string]any{{"id": "3kq7x2ma", "repository": "Smana/a", "issue": 7}}, noQuestion)
	s := answerSession(t, ab, &fakeIssueEditor{}, &labeling{}, nil)

	res := s.request(t, 2, "tools/call", map[string]any{
		"name":      mcpwire.Answer,
		"arguments": map[string]any{"task": "3kq7x2ma"},
	})
	result := res["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if result["isError"] != true || text != "invalid_arguments: text is the note to leave" {
		t.Fatalf("got isError=%v text=%q", result["isError"], text)
	}
}
