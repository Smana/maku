// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"testing"

	"github.com/Smana/maku/internal/mcpwire"
)

func cancelCalls(ab *answerBroker) []toolCall {
	var out []toolCall
	for _, c := range ab.seenCalls() {
		if c.Name == toolTaskCancel {
			out = append(out, c)
		}
	}
	return out
}

// #644: cancel asks in a form, and only a yes reaches the broker's task_cancel, with the form's discard.
func TestCancelAsksThenCancels(t *testing.T) {
	ab := newAnswerBroker(t, nil, nil)
	s := answerSession(t, ab, &fakeIssueEditor{}, nil, nil)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Cancel, "arguments": map[string]any{"task": "3kq7x2ma"}}})
	ask := s.next(t)
	if ask["method"] != "elicitation/create" {
		t.Fatalf("expected the form, got %v", ask)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "decline"}})
	if res := s.next(t)["result"].(map[string]any); res["isError"] == true || len(cancelCalls(ab)) != 0 {
		t.Fatalf("a declined form cancels nothing: %v %v", res, cancelCalls(ab))
	}

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Cancel, "arguments": map[string]any{"task": "3kq7x2ma"}}})
	ask = s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action": "accept", "content": map[string]any{"confirm": true, "discard": true}}})
	if res := s.next(t)["result"].(map[string]any); res["isError"] == true {
		t.Fatalf("%v", res)
	}
	if c := cancelCalls(ab); len(c) != 1 || c[0].Args["room"] != "3kq7x2ma" || c[0].Args["discard"] != true {
		t.Fatalf("task_cancel calls %+v", c)
	}
}

// With no form, nothing stops: the form is the confirmation.
func TestCancelNeedsAForm(t *testing.T) {
	ab := newAnswerBroker(t, nil, nil)
	s := relayTo(t, &mcpBroker{srv: ab.srv}, tok)
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{}})
	res := s.request(t, 2, "tools/call", map[string]any{"name": mcpwire.Cancel, "arguments": map[string]any{"task": "3kq7x2ma"}})["result"].(map[string]any)
	if res["isError"] != true || len(cancelCalls(ab)) != 0 {
		t.Fatalf("%v %v", res, cancelCalls(ab))
	}
}
