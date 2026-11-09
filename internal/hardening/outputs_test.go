// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const issueBody = "## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz\n"

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// UH3, T1: the vocabulary is closed, each payload has a schema, and everything else is refused
// with one bounded reason.
func TestDecodeOutput(t *testing.T) {
	allow := []string{"documentation", "needs-human"}
	bundle := base64.StdEncoding.EncodeToString([]byte("# v2 git bundle\n"))
	for name, tc := range map[string]struct {
		typ  OutputType
		in   json.RawMessage
		want Refusal
	}{
		"a push":                  {Push, raw(map[string]string{"bundle": bundle}), ""},
		"a pr":                    {PR, raw(map[string]any{"title": "fix: a thing", "body": strings.Repeat("b", 20)}), ""},
		"a draft pr":              {PR, raw(map[string]any{"title": "fix", "body": strings.Repeat("b", 20), "draft": true}), ""},
		"a comment on the issue":  {Comment, raw(map[string]string{"target": "issue", "body": "done"}), ""},
		"labels":                  {Labels, raw(map[string][]string{"add": {"documentation"}}), ""},
		"an issue":                {Issue, raw(map[string]string{"title": "docs(x): a typo", "body": issueBody}), ""},
		"an unknown type":         {"merge", raw(map[string]string{}), RefuseType},
		"an unknown field":        {PR, raw(map[string]any{"title": "t", "body": strings.Repeat("b", 20), "base": "main"}), RefuseSchema},
		"not an object":           {PR, json.RawMessage(`"x"`), RefuseSchema},
		"trailing data":           {Comment, json.RawMessage(`{"target":"pr","body":"x"}{}`), RefuseSchema},
		"an empty bundle":         {Push, raw(map[string]string{"bundle": ""}), RefuseSchema},
		"a bundle not base64":     {Push, raw(map[string]string{"bundle": "!!"}), RefuseSchema},
		"a pr title too long":     {PR, raw(map[string]any{"title": strings.Repeat("t", 257), "body": strings.Repeat("b", 20)}), RefuseSchema},
		"a pr title on two lines": {PR, raw(map[string]any{"title": "a\nb", "body": strings.Repeat("b", 20)}), RefuseSchema},
		"a pr body too short":     {PR, raw(map[string]any{"title": "t", "body": "short"}), RefuseSchema},
		"a pr body too long":      {PR, raw(map[string]any{"title": "t", "body": strings.Repeat("b", 65001)}), RefuseSchema},
		"another target":          {Comment, raw(map[string]string{"target": "discussion", "body": "x"}), RefuseSchema},
		"a blank comment":         {Comment, raw(map[string]string{"target": "pr", "body": "  "}), RefuseSchema},
		"a comment over 16 KiB":   {Comment, raw(map[string]string{"target": "pr", "body": strings.Repeat("c", 16<<10+1)}), RefuseSchema},
		"no label":                {Labels, raw(map[string][]string{"add": {}}), RefuseSchema},
		"four labels":             {Labels, raw(map[string][]string{"add": {"documentation", "needs-human", "a", "b"}}), RefuseSchema},
		"a label off the list":    {Labels, raw(map[string][]string{"add": {"factory/live"}}), RefuseSchema},
		"a label twice":           {Labels, raw(map[string][]string{"add": {"documentation", "documentation"}}), RefuseSchema},
		"an issue off template":   {Issue, raw(map[string]string{"title": "docs(x): a typo", "body": "please"}), RefuseSchema},
	} {
		t.Run(name, func(t *testing.T) {
			_, got := DecodeOutput(tc.typ, tc.in, allow)
			if got != tc.want {
				t.Fatalf("refusal %q, want %q", got, tc.want)
			}
		})
	}
}

// A safe-output issue is filed externally, so unlike an intermediate plan it may not carry a marker.
func TestFinalDraftDoors(t *testing.T) {
	body := strings.Replace(issueBody, "\nx\n", "\n[NEEDS CLARIFICATION: g1 which check?]\n", 1)
	_, refusal := DecodeOutput(Issue, raw(map[string]string{"title": "docs(x): a typo", "body": body}), nil)
	if refusal != RefuseSchema {
		t.Fatalf("hardening accepted an unresolved marker: %q", refusal)
	}
}

func TestDecodeOutputBundleBound(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, MaxBundle+1))
	if _, r := DecodeOutput(Push, raw(map[string]string{"bundle": big}), nil); r != RefuseSchema {
		t.Fatalf("an oversize bundle: %q", r)
	}
	out, r := DecodeOutput(Push, raw(map[string]string{"bundle": base64.StdEncoding.EncodeToString([]byte("abc"))}), nil)
	if r != "" || string(out.(PushPayload).Bundle) != "abc" {
		t.Fatalf("%+v %q", out, r)
	}
}

// Per-run caps (safe outputs table): a pr is one open and five edits, labels one call.
func TestCaps(t *testing.T) {
	var c Counts
	for typ, n := range map[OutputType]int{Push: 20, PR: 6, Comment: 3, Labels: 1, Issue: 1} {
		for i := range n {
			if !c.Take(typ) {
				t.Fatalf("%s refused at %d of %d", typ, i+1, n)
			}
		}
		if c.Take(typ) {
			t.Fatalf("%s past its cap of %d", typ, n)
		}
	}
	if c.Take("merge") {
		t.Fatal("an unknown type has no cap")
	}
}

// The reviewer and the triager have no output but the room tools.
func TestOutputRoles(t *testing.T) {
	for _, typ := range Types() {
		r := Roles(typ)
		if strings.Join(r, ",") != "implementer,tester" {
			t.Fatalf("%s: %v", typ, r)
		}
	}
	if len(Types()) != 5 {
		t.Fatal("the vocabulary is push, pr, comment, labels and issue")
	}
}
