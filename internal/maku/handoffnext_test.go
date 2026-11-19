// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/mcpwire"
)

// planResult is a broker's handoff answer: its text, and the question it asks when q is set.
func planResult(id, q string) map[string]any {
	sc := map[string]any{"apiVersion": "handoff/v1", "id": id, "text": "Handoff " + id + "\nPlan", "question": nil, "ready": q == ""}
	if q != "" {
		sc["question"] = map[string]any{"text": q, "recommended": "use 5 and 2"}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "Handoff " + id + "\nPlan"}}, "structuredContent": sc}
}

func refusedResult(text string) map[string]any {
	return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": text}}}
}

// texts are a tools/call result's text blocks, in order.
func texts(t *testing.T, res map[string]any) []string {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", res)
	}
	var out []string
	for _, c := range result["content"].([]any) {
		out = append(out, fmt.Sprint(c.(map[string]any)["text"]))
	}
	return out
}

func callHandoff(t *testing.T, s session, id int, args map[string]any) map[string]any {
	t.Helper()
	return s.request(t, id, "tools/call", map[string]any{"name": mcpwire.Handoff, "arguments": args})
}

// The handoff entry a model lists says what to do first and what each argument takes: a small model
// reads this, not the skill. Only the arguments the broker has are described.
func TestHandoffEntrySaysWhatToDoFirst(t *testing.T) {
	b := newMCPBroker(t)
	b.tools = []any{map[string]any{"name": "handoff", "description": "d", "inputSchema": map[string]any{"type": "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"},
			"answer": map[string]any{"type": "string"}, "acceptRest": map[string]any{"type": "boolean"},
			"repository": map[string]any{"type": "string"}, "ceiling": map[string]any{"type": "number"}}}}}
	s := startSession(t, b, &filing{}, &labeling{}, time.Second)
	tools := s.request(t, 2, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
	var handoff, start map[string]any
	for _, raw := range tools {
		switch tool := raw.(map[string]any); tool["name"] {
		case "handoff":
			handoff = tool
		case "start":
			start = tool
		}
	}
	desc := fmt.Sprint(handoff["description"])
	for _, want := range []string{"at once", "no other argument", "read no file", "run no git", "result's next says"} {
		if !strings.Contains(desc, want) {
			t.Errorf("handoff's description lacks %q: %s", want, desc)
		}
	}
	props := handoff["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for arg, want := range map[string]string{
		"text": "word for word", "answer": "exactly as they typed it", "acceptRest": "accept the rest",
		"repository": "only when the developer named a repository", "id": "nothing else",
	} {
		if got := fmt.Sprint(props[arg].(map[string]any)["description"]); !strings.Contains(got, want) {
			t.Errorf("%s's description %q lacks %q", arg, got, want)
		}
	}
	if _, ok := props["context"]; ok {
		t.Errorf("an argument the broker does not take was added: %v", props)
	}
	if _, ok := props["ceiling"].(map[string]any)["description"]; ok {
		t.Errorf("ceiling is not the model's to fill: %v", props["ceiling"])
	}
	if d := fmt.Sprint(start["description"]); !strings.Contains(d, "without asking the developer first") || !strings.Contains(d, "result's next says") {
		t.Errorf("start's description: %s", d)
	}
}

// Each handoff result names the one call to make, with the id in it: in structuredContent and as a
// last text block, after the broker's text, which stays as it came.
func TestHandoffResultNamesTheNextCall(t *testing.T) {
	for name, tc := range map[string]struct {
		question string
		want     []string
		never    []string
	}{
		"a question": {"Which value?", []string{"ask the developer this one question", "show nothing else of this draft", "two options",
			`{"id":"h1","answer": their words exactly as typed}`, `{"id":"h1","acceptRest":true}`}, []string{"call start"}},
		"a ready plan": {"", []string{"show the plan above as it is", `call start with {"id":"h1"} in this same turn`,
			"Do not ask the developer whether to start"}, []string{"acceptRest"}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			b.handoffFn = func(map[string]any) map[string]any { return planResult("h1", tc.question) }
			s, _ := locals(t, b)
			res := callHandoff(t, s, 2, map[string]any{"text": "fix the budget"})
			got := texts(t, res)
			if len(got) != 2 || got[0] != "Handoff h1\nPlan" || !strings.HasPrefix(got[1], "next: ") {
				t.Fatalf("text blocks = %q", got)
			}
			next := fmt.Sprint(res["result"].(map[string]any)["structuredContent"].(map[string]any)["next"])
			if "next: "+next != got[1] {
				t.Errorf("structuredContent's next %q is not the text's %q", next, got[1])
			}
			for _, want := range tc.want {
				if !strings.Contains(next, want) {
					t.Errorf("next lacks %q: %s", want, next)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(next, never) {
					t.Errorf("next names %q, another state's step: %s", never, next)
				}
			}
		})
	}
}

// A refusal has one next step. The planner's own failures are tried once more, then given up on in
// one line; anything else is shown and stops. The request is never reworded to get past one (aws-0,
// 2026-11-19: two invalid_draft in a row ended in "rephrase it yourself").
func TestHandoffRefusalHasOneNextStep(t *testing.T) {
	b := newMCPBroker(t)
	answers := []map[string]any{
		refusedResult("invalid_draft: the title is one line"),
		refusedResult("planner_invalid: the plan is not the shape the contract asks for"),
		planResult("h1", ""),
		refusedResult("planner_timeout: the plan service did not answer inside its deadline; try again"),
		refusedResult("repository_not_served_here: Smana/x is not served here; an operator adds it"),
		refusedResult("handoff_expired: start a new handoff"),
		refusedResult("invalid_arguments: pass either answer or acceptRest: true with id"),
		refusedResult("Planning is not available here; file on GitHub."),
	}
	n := 0
	b.handoffFn = func(map[string]any) map[string]any { n++; return answers[n-1] }
	s, _ := locals(t, b)
	for i, want := range []string{
		"call handoff again now with exactly the same arguments",
		"planner failed twice (planner_invalid)",
		"call start",
		"call handoff again now with exactly the same arguments", // a plan in between ends the row
		"show the developer this line as it is and stop",
		"call handoff again with {\"text\": the developer's original words}",
		"{\"id\", \"acceptRest\": true}",
		"",
	} {
		got := texts(t, callHandoff(t, s, i+2, map[string]any{"text": "fix"}))
		if want == "" {
			if len(got) != 1 {
				t.Errorf("planning off is one line to show, got %q", got)
			}
			continue
		}
		if len(got) != 2 || !strings.HasPrefix(got[1], "next: ") || !strings.Contains(got[1], want) {
			t.Errorf("answer %d: text blocks = %q, want a next with %q", i+1, got, want)
		}
		if i != 2 && !strings.Contains(got[1], "reword") && !strings.Contains(got[1], "handoff") {
			t.Errorf("answer %d: next neither names a call nor forbids rewording: %s", i+1, got[1])
		}
	}
}

// A model's continuation becomes one the broker takes: the first call's arguments sent again are
// dropped, "accept the rest" typed as the answer is acceptRest, and a real answer goes as typed.
func TestHandoffContinuationIsTidied(t *testing.T) {
	for name, tc := range map[string]struct{ args, want map[string]any }{
		"accept the rest as the answer": {map[string]any{"id": "h1", "answer": " \"Accept the rest.\" "}, map[string]any{"id": "h1", "acceptRest": true}},
		"the request sent again": {map[string]any{"id": "h1", "text": "fix", "repository": "Smana/a", "context": "c", "answer": "dailyUSD 5, maxCostUSD 2"},
			map[string]any{"id": "h1", "answer": "dailyUSD 5, maxCostUSD 2"}},
		"an answer with acceptRest": {map[string]any{"id": "h1", "answer": "Keep Kimi", "acceptRest": true}, map[string]any{"id": "h1", "answer": "Keep Kimi"}},
		"a clean answer":            {map[string]any{"id": "h1", "answer": "accept the rest of the docs too"}, map[string]any{"id": "h1", "answer": "accept the rest of the docs too"}},
		"a first call":              {map[string]any{"text": "accept the rest"}, map[string]any{"text": "accept the rest"}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			s, _ := locals(t, b)
			callHandoff(t, s, 2, tc.args)
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.calls) != 1 || !reflect.DeepEqual(b.calls[0].Args, tc.want) {
				t.Fatalf("the broker got %v, want %v", b.calls, tc.want)
			}
		})
	}
}

// A broker that cannot be reached is one plain step for the developer, never a retry loop (aws-0,
// 2026-11-19: "unavailable: … i/o timeout" was relayed with nothing to do about it).
func TestUnreachableBrokerNamesDoctor(t *testing.T) {
	b := newMCPBroker(t)
	s, _ := locals(t, b)
	b.srv.Close()
	got := texts(t, callHandoff(t, s, 2, map[string]any{"text": "fix"}))
	if len(got) != 1 || !strings.HasPrefix(got[0], "unavailable: ") ||
		!strings.Contains(got[0], "\nnext: ") || !strings.Contains(got[0], "`maku doctor`") || !strings.Contains(got[0], "Do not retry") {
		t.Fatalf("%q", got)
	}
}

// start says what follows it: wait on the task once the work started, and the question when the
// plan it was given still has one.
func TestStartNamesTheNextCall(t *testing.T) {
	t.Run("started", func(t *testing.T) {
		p := planFixture(t, "single")
		hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
			return handoffCheckedResult(t, p, true, []string{"standard"})
		})
		hb.rooms = []map[string]any{{"id": "3kq7x2ma", "repository": p.Repository, "issue": 7}}
		f := &filing{url: "https://github.com/" + p.Repository + "/issues/7"}
		s := handoffSession(t, hb, true, f, &labeling{}, nil)
		s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
		ask := s.next(t)
		s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "accept",
			"content": map[string]any{"file": true, "start": true}}})
		res := s.next(t)
		got := texts(t, res)
		want := `call wait with {"task":"3kq7x2ma"} in a background task now`
		if len(got) != 2 || !strings.HasPrefix(got[0], "Filed ") || !strings.Contains(got[1], want) {
			t.Fatalf("text blocks = %q", got)
		}
		if next := fmt.Sprint(res["result"].(map[string]any)["structuredContent"].(map[string]any)["next"]); !strings.Contains(next, want) {
			t.Fatalf("structuredContent's next = %q", next)
		}
	})
	t.Run("the plan still has its question", func(t *testing.T) {
		hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
			return refusedResult("unresolved_marker: the plan still has a clarification to answer")
		})
		s := handoffSession(t, hb, true, &filing{}, &labeling{}, nil)
		got := texts(t, s.request(t, 2, "tools/call", map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "h1"}}))
		if len(got) != 1 || !strings.Contains(got[0], "\nnext: the plan still has its question: ask the developer") ||
			!strings.Contains(got[0], `{"id":"h1","acceptRest":true}, then call start again`) {
			t.Fatalf("%q", got)
		}
	})
}
