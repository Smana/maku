// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNeedAnswerLocalRegistration(t *testing.T) {
	if _, ok := buildHandlers()["need_answer"]; !ok {
		t.Fatal("need_answer is not registered as a local tool")
	}
	var entry map[string]any
	for _, raw := range localEntries {
		tool, ok := raw.(map[string]any)
		if ok && tool["name"] == "need_answer" {
			entry = tool
			break
		}
	}
	if entry == nil {
		t.Fatal("need_answer is absent from tools/list")
	}
	raw, ok := entry["inputSchema"].(json.RawMessage)
	if !ok {
		t.Fatalf("need_answer inputSchema = %T", entry["inputSchema"])
	}
	var schema struct {
		Type                 string         `json:"type"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
		Properties           map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || strings.Join(schema.Required, ",") != "repo,issue" {
		t.Fatalf("need_answer schema = %s", raw)
	}
	for _, name := range []string{"repo", "issue", "room"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("need_answer schema lacks %s: %s", name, raw)
		}
	}
}

func TestNeedAnswerArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "unknown field", args: map[string]any{"repo": "Smana/a", "issue": 7, "send": true}},
		{name: "bad repository", args: map[string]any{"repo": "Smana/a;label", "issue": 7}},
		{name: "missing repository", args: map[string]any{"issue": 7}},
		{name: "missing issue", args: map[string]any{"repo": "Smana/a"}},
		{name: "zero issue", args: map[string]any{"repo": "Smana/a", "issue": 0}},
		{name: "bad room", args: map[string]any{"repo": "Smana/a", "issue": 7, "room": "../room"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Relay{Issues: &fakeIssueEditor{}}
			got := r.needAnswer(t.Context(), mustArgs(t, tc.args))
			if !strings.Contains(toolText(got), "invalid_arguments") {
				t.Fatalf("need_answer() = %v; want invalid_arguments", got)
			}
		})
	}
}

func toolText(got any) string {
	m, _ := got.(map[string]any)
	content, _ := m["content"].([]map[string]string)
	if len(content) == 0 {
		return ""
	}
	return content[0]["text"]
}
