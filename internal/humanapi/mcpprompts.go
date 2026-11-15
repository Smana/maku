// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/mcpwire"
)

// promptArg is one optional prompt argument.
type promptArg struct{ name, description string }

// mcpPrompt is one /mcp prompt: a canned user message the client offers as a slash command.
type mcpPrompt struct {
	name, description string
	args              []promptArg
	text              func(args map[string]string) string
}

// orElse is v, or def when v is empty.
func orElse(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// refText is the developer's room reference made safe to quote in a code span: no backtick or
// control character, at most 80 runes.
func refText(s string) string {
	out := []rune{}
	for _, r := range s {
		if r == '`' || unicode.IsControl(r) {
			continue
		}
		if out = append(out, r); len(out) == 80 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

// handoffText is the handoff loop (UX1, UX2): the broker's planner writes and holds the plan, the
// model passes the developer's words and answers, and only the developer's answer to start's form
// files the issue and starts the work.
func handoffText(what string) string {
	return "Call `" + mcpwire.Handoff + "` with the developer's own words, " + orElse(what, "the side task we just discussed") +
		", and the context you have: the repository you are in and the paths you read. Show its text as it is. " +
		"When it asks one question, ask the developer that question with its recommendation and why, and offer \"accept the rest\"; " +
		"call `" + mcpwire.Handoff + "` with its id and their answer, or with acceptRest: true, and show each plan as it is. " +
		"When the plan is ready, call `" + mcpwire.Start + "` with the id: its form is the developer's, and only their answer files " +
		"the plan's first issue and starts the work, so with a longer plan say the others are not filed. " +
		"Once the work started, run `" + mcpwire.Wait + "` on the task in a background task where the client can. " +
		"Never write the plan yourself and never apply a label."
}

// taskText is the task prompt: one call for where a task stands, or, with none named, the first one
// that needs the developer. A task is never claimed to exist before a tool has shown it.
func taskText(ref string) string {
	if ref != "" {
		return "Call `" + mcpwire.Task + "` with task `" + ref + "` (as written: maku resolves #<issue>, PR #<n>, numbers and " +
			"title fragments, and says when several tasks match; ask the developer then, never guess), and show its text as it is."
	}
	return "Call `" + mcpwire.Inbox + "`, take its first task, and call `" + mcpwire.Task + "` for it: show each text as it is, " +
		"and never say a task exists before a tool has shown it. If nothing needs the developer, say so in one line."
}

// mcpPrompts are the prompts a human's maku is offered; a sponsored principal has none.
var mcpPrompts = []mcpPrompt{
	{
		name: mcpwire.Handoff, description: "Hand a piece of work to Maku: a plan, its questions, then the start form.",
		args: []promptArg{{"what", "the work; default: the one just discussed"}},
		text: func(a map[string]string) string { return handoffText(a["what"]) },
	},
	{
		name: mcpwire.Task, description: "Where one task stands, what it needs and what changed since you last asked.",
		args: []promptArg{{"task", "a task id, #<issue>, PR #<n> or an issue number; default: the first task that needs you"}},
		text: func(a map[string]string) string { return taskText(refText(a["task"])) },
	},
	{
		name: mcpwire.Inbox, description: "What needs you across your tasks.",
		text: func(map[string]string) string {
			return "Call `" + mcpwire.Inbox + "`. Show its text as it is. If nothing needs you, say so in one line."
		},
	},
}

// mcpPromptsList is prompts/list: a sponsored principal has no prompts, so its list is empty.
func mcpPromptsList(w http.ResponseWriter, req mcpwire.Request, p authn.Principal) {
	list := []map[string]any{}
	if !p.Sponsored {
		for _, pr := range mcpPrompts {
			args := []map[string]any{}
			for _, a := range pr.args {
				args = append(args, map[string]any{"name": a.name, "description": a.description, "required": false})
			}
			list = append(list, map[string]any{"name": pr.name, "description": pr.description, "arguments": args})
		}
	}
	mcpwire.Write(w, req.ID, map[string]any{"prompts": list}, nil)
}

// mcpPromptsGet is prompts/get: the named prompt's message, its arguments filled in. An unknown
// prompt or argument is invalid params.
func mcpPromptsGet(w http.ResponseWriter, req mcpwire.Request, p authn.Principal) {
	var g struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	bad := func(msg string) {
		mcpwire.Write(w, req.ID, nil, &mcpwire.Error{Code: mcpwire.InvalidParams, Message: msg})
	}
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &g) != nil || g.Name == "" {
		bad("prompts/get takes {name, arguments}")
		return
	}
	i := slices.IndexFunc(mcpPrompts, func(pr mcpPrompt) bool { return pr.name == g.Name })
	if i < 0 || p.Sponsored {
		bad("unknown prompt: " + g.Name)
		return
	}
	pr := mcpPrompts[i]
	for k := range g.Arguments {
		if !slices.ContainsFunc(pr.args, func(a promptArg) bool { return a.name == k }) {
			bad("unknown argument: " + k)
			return
		}
	}
	mcpwire.Write(w, req.ID, map[string]any{"description": pr.description, "messages": []map[string]any{
		{"role": "user", "content": map[string]string{"type": "text", "text": pr.text(g.Arguments)}}}}, nil)
}
