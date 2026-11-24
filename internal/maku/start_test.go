// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
)

type handoffBroker struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []toolCall
	check func(n int, args map[string]any) map[string]any
	rooms []map[string]any
	n     int
}

func newHandoffBroker(t *testing.T, check func(n int, args map[string]any) map[string]any) *handoffBroker {
	t.Helper()
	hb := &handoffBroker{check: check}
	hb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		if json.NewDecoder(r.Body).Decode(&m) != nil || len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var res any
		switch m.Method {
		case "initialize":
			res = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			res = map[string]any{"tools": []any{map[string]any{"name": mcpwire.Handoff}}}
		case "tools/call":
			var c struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &c)
			hb.mu.Lock()
			if c.Name != "rooms_list" {
				hb.calls = append(hb.calls, toolCall{Name: c.Name, Args: c.Arguments})
			}
			switch c.Name {
			case "handoff_check":
				hb.n++
				res = hb.check(hb.n, c.Arguments)
			case "rooms_list":
				res = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": "rooms"}},
					"structuredContent": map[string]any{"rooms": hb.rooms},
				}
			case mcpwire.Task:
				res = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "#7 docs: a dead link\nState     queued\nRoom      " + hb.srv.URL + "/r/3kq7x2ma"}},
					"structuredContent": map[string]any{
						"apiVersion": "task/v1",
						"room":       "3kq7x2ma",
						"text":       "#7 docs: a dead link\nState     queued\nRoom      " + hb.srv.URL + "/r/3kq7x2ma",
					},
				}
			default:
				res = map[string]any{"content": []any{map[string]any{"type": "text", "text": "{}"}}, "structuredContent": map[string]any{}}
			}
			hb.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
	}))
	t.Cleanup(hb.srv.Close)
	return hb
}

func (hb *handoffBroker) seenCalls() []toolCall {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return append([]toolCall(nil), hb.calls...)
}

func handoffCheckedResult(t *testing.T, p intake.Plan, canStart bool, workflows []string) map[string]any {
	t.Helper()
	wf := "standard"
	if len(p.Issues) > 0 && p.Issues[0].Profile != "" {
		wf = p.Issues[0].Profile
	} else if len(p.Issues) > 0 && p.Issues[0].Suggestion != nil && p.Issues[0].Suggestion.Profile != "" {
		wf = p.Issues[0].Suggestion.Profile
	}
	sc := map[string]any{
		"plan":       p,
		"hash":       intake.Hash(p),
		"canStart":   canStart,
		"maintainer": true,
		"workflows":  workflows,
		"title":      p.Issues[0].Title,
		"body":       p.Issues[0].Body,
		"repository": p.Repository,
	}
	if !canStart {
		sc["canStartWhy"] = "off"
	}
	return map[string]any{
		"isError":           false,
		"content":           []any{map[string]any{"type": "text", "text": fmt.Sprintf("Plan for %s\nWorkflow %s\nBudget up to $%.2f", p.Repository, wf, p.Budget.CeilingUSD)}},
		"structuredContent": sc,
	}
}

func handoffSession(t *testing.T, hb *handoffBroker, elicit bool, f *filing, l *labeling, cls *labeling) session {
	t.Helper()
	b := &mcpBroker{srv: hb.srv}
	s := relayTo(t, b, tok, withFile(f.file), func(r *Relay) {
		r.Label = l.label
		if cls != nil {
			r.ClassLabel = cls.label
		}
		r.RoomWait = 50 * time.Millisecond
		r.After = func(time.Duration) <-chan time.Time { return time.After(5 * time.Millisecond) }
	})
	caps := map[string]any{}
	if elicit {
		caps["elicitation"] = map[string]any{}
	}
	s.request(t, 1, "initialize", map[string]any{"capabilities": caps})
	return s
}

func TestStartFormShowsIssueWorkflowBudget(t *testing.T) {
	p := planFixture(t, "single")
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return handoffCheckedResult(t, p, true, []string{"standard"})
	})
	f, l := &filing{}, &labeling{}
	s := handoffSession(t, hb, true, f, l, nil)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	ask := s.next(t)
	if ask["method"] != "elicitation/create" {
		t.Fatalf("expected elicitation/create, got %v", ask)
	}
	params := ask["params"].(map[string]any)
	msg := params["message"].(string)
	if !strings.Contains(msg, p.Issues[0].Title) || !strings.Contains(msg, p.Issues[0].Body) {
		t.Fatalf("form message missing title or body:\n%s", msg)
	}
	hasWorkflow, hasBudget := false, false
	for line := range strings.SplitSeq(msg, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Merge") || strings.HasPrefix(strings.ToLower(trimmed), "merge ") {
			t.Fatalf("form message must carry no Merge line (UX14), found %q:\n%s", line, msg)
		}
		if strings.HasPrefix(trimmed, "Workflow ") {
			hasWorkflow = true
		}
		if strings.HasPrefix(trimmed, "Budget ") {
			hasBudget = true
		}
	}
	if !hasWorkflow || !hasBudget {
		t.Fatalf("form message missing Workflow (%v) or Budget (%v) line:\n%s", hasWorkflow, hasBudget, msg)
	}
	props := params["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	if _, hasWF := props["workflow"]; hasWF {
		t.Fatalf("workflow property must be absent when <=1 workflow may be chosen: %v", props)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "cancel"}})
	s.next(t)
}

func TestStartNowDefaultsYes(t *testing.T) {
	p := planFixture(t, "single")
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return handoffCheckedResult(t, p, true, []string{"standard", "careful"})
	})
	s := handoffSession(t, hb, true, &filing{}, &labeling{}, nil)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	ask := s.next(t)
	props := ask["params"].(map[string]any)["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	fileProp := props["file"].(map[string]any)
	startProp := props["start"].(map[string]any)
	wfProp := props["workflow"].(map[string]any)
	if fileProp["default"] != false {
		t.Fatalf("file default = %v, want false", fileProp["default"])
	}
	if startProp["default"] != true {
		t.Fatalf("start default = %v, want true", startProp["default"])
	}
	if wfProp["type"] != "string" {
		t.Fatalf("workflow property = %v", wfProp)
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "cancel"}})
	s.next(t)
}

func TestStartFilesAndLabels(t *testing.T) {
	p := planFixture(t, "single")
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return handoffCheckedResult(t, p, true, []string{"standard"})
	})
	hb.rooms = []map[string]any{{"id": "3kq7x2ma", "repository": p.Repository, "issue": 7}}
	f := &filing{url: "https://github.com/" + p.Repository + "/issues/7"}
	l, cls := &labeling{}, &labeling{}
	s := handoffSession(t, hb, true, f, l, cls)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	ask := s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"file": true, "start": true},
	}})
	res := s.next(t)
	if len(f.calls) != 1 {
		t.Fatalf("File calls = %d, want 1", len(f.calls))
	}
	wantReady := p.Repository + "#7+factory/ready"
	if len(l.calls) != 1 || l.calls[0] != wantReady {
		t.Fatalf("Label calls = %v, want [%s]", l.calls, wantReady)
	}
	result := res["result"].(map[string]any)
	sc := result["structuredContent"].(map[string]any)
	if sc["url"] != f.url || sc["task"] != "3kq7x2ma" {
		t.Fatalf("structuredContent = %v, want url=%s and task=3kq7x2ma", sc, f.url)
	}
}

func TestStartWorkflowChangeRevalidates(t *testing.T) {
	p1 := planFixture(t, "single")
	p2 := planFixture(t, "single")
	p2.Issues[0].Profile = "careful"
	p2.Issues[0].Title = "docs: careful title after workflow change"

	hb := newHandoffBroker(t, func(n int, args map[string]any) map[string]any {
		if n == 1 {
			return handoffCheckedResult(t, p1, true, []string{"standard", "careful"})
		}
		if args["workflow"] != "careful" {
			t.Errorf("second handoff_check args = %v, want workflow=careful", args)
		}
		return handoffCheckedResult(t, p2, true, []string{"standard", "careful"})
	})
	hb.rooms = []map[string]any{{"id": "3kq7x2ma", "repository": p2.Repository, "issue": 7}}
	f := &filing{url: "https://github.com/" + p2.Repository + "/issues/7"}
	l, cls := &labeling{}, &labeling{}
	s := handoffSession(t, hb, true, f, l, cls)

	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	ask := s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{
		"action":  "accept",
		"content": map[string]any{"file": true, "start": true, "workflow": "careful"},
	}})
	s.next(t)

	calls := hb.seenCalls()
	checkCalls := 0
	for _, c := range calls {
		if c.Name == "handoff_check" {
			checkCalls++
		}
	}
	if checkCalls != 2 {
		t.Fatalf("handoff_check calls = %d (%v), want 2", checkCalls, calls)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], p2.Issues[0].Title) {
		t.Fatalf("filed = %v, want revalidated title %q", f.calls, p2.Issues[0].Title)
	}
}

func TestStartWithoutElicitationFilesNothing(t *testing.T) {
	p := planFixture(t, "single")
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return handoffCheckedResult(t, p, true, []string{"standard"})
	})
	f, l := &filing{}, &labeling{}
	s := handoffSession(t, hb, false, f, l, nil)

	res := s.request(t, 2, "tools/call", map[string]any{
		"name":      mcpwire.Start,
		"arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	result := res["result"].(map[string]any)
	text := fmt.Sprint(result["content"])
	if result["isError"] != true || !strings.Contains(text, "needs_confirmation") || !strings.Contains(text, "gh issue create") {
		t.Fatalf("result = %v", res)
	}
	if len(f.calls) != 0 || len(l.calls) != 0 {
		t.Fatalf("File=%v Label=%v, want neither called", f.calls, l.calls)
	}
}

func TestStartExpiredID(t *testing.T) {
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return map[string]any{
			"isError": true,
			"content": []any{map[string]any{"type": "text", "text": "handoff_expired: start a new handoff"}},
		}
	})
	f, l := &filing{}, &labeling{}
	s := handoffSession(t, hb, true, f, l, nil)

	res := s.request(t, 2, "tools/call", map[string]any{
		"name":      mcpwire.Start,
		"arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	result := res["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if result["isError"] != true || !strings.HasPrefix(content, "handoff_expired: start a new handoff\nnext: ") ||
		!strings.Contains(content, "call handoff again with {\"text\": the developer's original words}") {
		t.Fatalf("expired result = %v", res)
	}
	if len(f.calls) != 0 || len(l.calls) != 0 {
		t.Fatalf("File=%v Label=%v, want neither called", f.calls, l.calls)
	}
}

func TestStartFormUnansweredKeepsThePlan(t *testing.T) {
	defer func(w time.Duration) { elicitWait = w }(elicitWait)
	elicitWait = 50 * time.Millisecond
	p := planFixture(t, "single")
	hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
		return handoffCheckedResult(t, p, true, []string{"standard"})
	})
	f, l := &filing{}, &labeling{}
	s := handoffSession(t, hb, true, f, l, nil)

	id := "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": id}}})
	if ask := s.next(t); ask["method"] != "elicitation/create" {
		t.Fatalf("expected elicitation/create, got %v", ask)
	}
	var res map[string]any // the form is never answered; skip notifications until start's own result
	for res == nil || fmt.Sprint(res["id"]) != "2" {
		res = s.next(t)
	}
	result := res["result"].(map[string]any)
	text := fmt.Sprint(result["content"])
	for _, want := range []string{"needs_confirmation", "The plan is kept", "call start again with the same id (" + id + ")", "gh issue create"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result lacks %q: %s", want, text)
		}
	}
	if len(f.calls) != 0 || len(l.calls) != 0 {
		t.Fatalf("File=%v Label=%v, want neither called", f.calls, l.calls)
	}
}

func TestDefaultElicitWaitLeavesTimeToRead(t *testing.T) {
	if defaultElicitWait < 5*time.Minute {
		t.Fatalf("defaultElicitWait = %v: a developer reading the form loses it under 5 min", defaultElicitWait)
	}
}

// #621: each reason start is not offered carries its one-line fix in the form text and doctor; an
// older broker's reason is shown as it is.
func TestStartWhyNamesTheFix(t *testing.T) {
	for why, want := range map[string]string{
		"repository_not_offered": "human.intake.plan.repositories",
		"access_unwired":         "human.access",
		"sponsored":              "your own session",
		"off":                    "off",
	} {
		if got := StartWhy(why); !strings.HasPrefix(got, why) || !strings.Contains(got, want) || strings.Contains(got, "\n") {
			t.Errorf("StartWhy(%q) = %q, want %q on one line", why, got, want)
		}
	}
}

// A form the developer cancels, declines or accepts without "File this issue" files nothing, and the
// plan is kept: the next step is start again with the same id, never a new handoff.
func TestStartNotConfirmedNamesStartAgain(t *testing.T) {
	for _, answer := range []map[string]any{
		{"action": "cancel"},
		{"action": "decline"},
		{"action": "accept", "content": map[string]any{"file": false}},
	} {
		t.Run(fmt.Sprint(answer["action"], answer["content"]), func(t *testing.T) {
			p := planFixture(t, "single")
			hb := newHandoffBroker(t, func(_ int, _ map[string]any) map[string]any {
				return handoffCheckedResult(t, p, true, []string{"standard"})
			})
			f := &filing{}
			s := handoffSession(t, hb, true, f, &labeling{}, nil)

			s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
				"params": map[string]any{"name": mcpwire.Start, "arguments": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
			ask := s.next(t)
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": answer})
			res := s.next(t)
			if len(f.calls) != 0 {
				t.Fatalf("File calls = %d, want 0", len(f.calls))
			}
			sc := res["result"].(map[string]any)["structuredContent"].(map[string]any)
			next, _ := sc["next"].(string)
			if sc["filed"] != false || !strings.Contains(next, "call start again with the same id (aaaaaaaaaaaaaaaaaaaaaaaaaa)") ||
				strings.Contains(next, "handoff") {
				t.Fatalf("structuredContent = %v", sc)
			}
		})
	}
}

func TestStartAppliesNoClassLabel(t *testing.T) {
	yes := true
	for name, tc := range map[string]struct {
		profile string
		want    []string
	}{
		"the factory's choice": {"", nil},
		"a pinned workflow":    {"careful", []string{"workflow:careful"}},
	} {
		t.Run(name, func(t *testing.T) {
			g := governed{planShape: planShape{Maintainer: &yes}}
			raw, err := json.Marshal(map[string]any{"issues": []map[string]any{{"class": "docs", "profile": tc.profile}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &g.view); err != nil {
				t.Fatal(err)
			}
			if got := g.planLabels(); !slices.Equal(got, tc.want) {
				t.Fatalf("labels = %v, want %v", got, tc.want)
			}
		})
	}
}
