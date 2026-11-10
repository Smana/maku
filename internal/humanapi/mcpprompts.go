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

// factoryIntakeText is the canonical intake loop (D6). The broker's planner remains the only
// planner: the harness asks and edits, while intake_plan and plan_check validate every revision.
// The guarded planner-off route is the pre-existing handoff exception, not another planning flow.
func factoryIntakeText(what string) string {
	return "Call `intake_plan` with the developer's own words, " + orElse(what, "the side task we just discussed") +
		", and the context you have: the repository you are in and the paths you read. Show its text as it is. " +
		"While gaps remain, ask only the first gap with its recommendation and why, and offer \"accept the rest\" at every question. " +
		"For an answer, resolve only its clarification markers and append their matching D/A records (Decision/Assumption) in a candidate. " +
		"Call `intake_plan` with the original request and the revision envelope `plan` (the base plan), `hash` (the base plan's hash), " +
		"and `candidate`; show each revised plan as it is and repeat with its first gap. " + acceptRestInstructions +
		" When no gaps remain, finish with `plan_check`, show the final draft and every assumption, then call `issue_file` with the returned " +
		"plan and its new hash. It files the plan's first issue, so with a longer plan say the others are not filed. " +
		"Only if `intake_plan` is not listed, the planner is off: write the issue in the template's three sections " +
		"(`## What is wrong`, `## What it should be`, `## Acceptance check`), have `issue_draft` check it, then call " +
		"`issue_file`, and say the broker planned nothing rather than passing a plan you wrote as the planner's. " +
		"Never write the plan yourself and never apply a label."
}

// mcpPrompts are the prompts a human's maku is offered; a sponsored principal has none.
var mcpPrompts = []mcpPrompt{
	{
		name: "factory_intake", description: "Plan a side task through clarification, then hand it to the agent factory.",
		args: []promptArg{{"request", "the side task; default: the one just discussed"}},
		text: func(a map[string]string) string { return factoryIntakeText(a["request"]) },
	},
	{
		name: "handoff", description: "Hand a side task to the agent factory as one issue (alias for factory_intake).",
		args: []promptArg{{"what", "the side task; default: the one just discussed"}},
		text: func(a map[string]string) string { return factoryIntakeText(a["what"]) },
	},
	{
		name: "plan", description: "Plan a side task for the agent factory, without filing it.",
		args: []promptArg{{"what", "the side task; default: the one just discussed"}},
		text: func(a map[string]string) string {
			return "Call `intake_plan` with the developer's own words, " + orElse(a["what"], "the side task we just discussed") +
				", and the context you have: the repository you are in and the paths you read. " +
				"Show its text and the hash it answered with, and stop there: filing is the developer's. " +
				"Never write the plan yourself."
		},
	},
	{
		name: "status", description: "Show one room's card.",
		args: []promptArg{{"room", "a room id, #<issue>, PR #<n> or an issue number; default: your most recently updated room"}},
		text: func(a map[string]string) string {
			if r := refText(a["room"]); r != "" {
				return "Call `room_summary` with room `" + r + "` (as written: maku resolves #<issue>, PR #<n> and numbers, and says when several rooms match; ask the developer then, never guess), and show its text as it is."
			}
			return "Find the caller's most recently updated room (from `rooms_list`), call `room_summary` for it, and show its text as it is."
		},
	},
	{
		name: "resume", description: "Come back to a task: what it needs, the plan, what changed, and the one next step.",
		args: []promptArg{{"room", "a room id, #<issue> or PR #<n>; default: the first room in your needs list"}},
		text: func(a map[string]string) string { return resumeText(refText(a["room"])) },
	},
	{
		name: "inbox", description: "Show what waits on you, from one needs_list call; when nothing does, point to rooms_list.",
		text: func(map[string]string) string {
			return "Call `needs_list`. Show its text as it is. If it is empty, say so in one line and name " +
				"`rooms_list` with `mine` for the rest."
		},
	},
}

// resumeText is the resume prompt (CD11): one call to the needs list, then the brief of the room that needs
// the developer, so a session starts from the brief rather than from a search of the logs. The brief decides
// nothing — its one next step is a statement and a link — and a room is never claimed to exist before the
// brief has shown it, which keeps a resume from inventing a room id the broker would refuse.
func resumeText(room string) string {
	if room != "" {
		return "Call `room_brief` for room `" + room + "` (as written: maku resolves #<issue>, PR #<n> and numbers, " +
			"and says when several rooms match; ask the developer then, never guess), and `needs_list` with `room` `" +
			room + "` for what it waits on: show each tool's text as it is, name the brief's `Next:` line as the one " +
			"next step, and never say a room exists before room_brief shows it."
	}
	return "Call `needs_list`, take its first item's room, and call `room_brief` for that room: show each tool's " +
		"text as it is, name the brief's `Next:` line as the one next step and the item's action link with it, and " +
		"never say a room exists before room_brief shows it. If the list is empty, say so in one line and name " +
		"`rooms_list` with `mine`."
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
