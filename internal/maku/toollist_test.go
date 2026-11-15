// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// brokerTools is a release-N broker's full list: the three new broker tools first, then every
// name they replace and the rare ones, as humanapi orders them.
func brokerTools() []any {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"room": map[string]any{"type": "string", "pattern": "^[a-z2-7]{8}$"}}}
	taskSchema := map[string]any{"type": "object", "required": []any{"task"}, "properties": map[string]any{
		"task": map[string]any{"type": "string", "pattern": "^[a-z2-7]{8}$"}}}
	var tools []any
	for _, n := range []string{"handoff", "inbox", "task", "rooms_list", "room_summary", "room_brief", "room_changes",
		"room_queue", "room_tail", "room_fork", "needs_list", "intake_plan", "plan_check", "issue_draft", "classes_list",
		"coordination_plan"} {
		s := schema
		if n == "task" {
			s = taskSchema
		}
		tools = append(tools, map[string]any{"name": n, "description": "d", "inputSchema": s})
	}
	return tools
}

func listNames(t *testing.T, s session) []string {
	t.Helper()
	var names []string
	for _, raw := range s.request(t, 5, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any) {
		names = append(names, fmt.Sprint(raw.(map[string]any)["name"]))
	}
	return names
}

func TestDefaultListIsTheSix(t *testing.T) {
	b := newMCPBroker(t)
	b.tools = brokerTools()
	s := startSession(t, b, &filing{}, &labeling{}, time.Second)
	if got := strings.Join(listNames(t, s), ","); got != "handoff,start,inbox,task,wait,answer" {
		t.Fatalf("the default list is the six, in order: %s", got)
	}
}

func TestAdvancedListAddsRareTools(t *testing.T) {
	b := newMCPBroker(t)
	b.tools = brokerTools()
	s := relayTo(t, b, tok, func(r *Relay) { r.Advanced = true })
	got := strings.Join(listNames(t, s), ",")
	want := "handoff,start,inbox,task,wait,answer," +
		"room_tail,room_fork,room_checkout,coordination_checkout,coordination_open,coordination_plan"
	if got != want {
		t.Fatalf("advanced list:\n got %s\nwant %s", got, want)
	}
}

func TestReplacedToolsAreGone(t *testing.T) {
	b := newMCPBroker(t)
	b.tools = []any{
		map[string]any{"name": "handoff"},
		map[string]any{"name": "inbox"},
		map[string]any{"name": "task"},
		map[string]any{"name": "rooms_list"},
		map[string]any{"name": "room_tail"},
		map[string]any{"name": "room_fork"},
	}
	s := startSession(t, b, &filing{}, &labeling{}, time.Second)
	for _, name := range []string{"issue_file", "need_answer", "room_wait", "handoff_check", "task_note"} {
		res := s.request(t, 5, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if !strings.Contains(fmt.Sprint(res["error"]), "unknown tool") && !strings.Contains(fmt.Sprint(res["result"]), "unknown tool") {
			t.Errorf("relay %s: got %v, want unknown tool", name, res)
		}
	}
}

func TestTaskArgumentTakesReferences(t *testing.T) {
	b := newMCPBroker(t)
	b.tools = brokerTools()
	b.rooms = []map[string]any{{"id": "abcdefgh", "repository": "Smana/a", "issue": 412, "title": "t", "state": "running", "phase": "Active"}}
	s := startSession(t, b, &filing{}, &labeling{}, time.Second)
	for _, raw := range s.request(t, 4, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] != "task" {
			continue
		}
		if strings.Contains(fmt.Sprint(tool["inputSchema"]), "[a-z2-7]") || !strings.Contains(fmt.Sprint(tool["description"]), "task is the room's id, #<issue>") {
			t.Fatalf("task does not say it takes a reference: %v", tool)
		}
	}
	res := s.request(t, 5, "tools/call", map[string]any{"name": "task", "arguments": map[string]any{"task": "#412"}})
	if r, _ := res["result"].(map[string]any); r == nil || r["isError"] == true {
		t.Fatalf("%v", res)
	}
	if got := lastCall(t, b); got.Name != "task" || got.Args["task"] != "abcdefgh" {
		t.Fatalf("task was not resolved to the room id: %+v", got)
	}
}
