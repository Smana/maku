// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"strings"

	"github.com/Smana/maku/internal/mcpwire"
)

// The handoff loop is written where every model reads it: the tool's description, its argument
// descriptions and a `next` in each result that names the one call to make. The skill and the /maku
// command only say "do what next says", so an agent that loaded neither still follows the loop
// (aws-0, 2026-11-19: Kimi read files and ran git for minutes before its first call; Opus offered an
// answer option that could hold no answer; a planner refusal ended in three suggestions).

// handoffGuide is handoff's description as maku mcp lists it, over the broker's own.
const handoffGuide = "Use when the developer wants Maku to do a piece of work. Call it at once with their words as text and no other argument: " +
	"read no file, run no git and ask nothing first. Then do what the result's next says."

// handoffArgHelp describes handoff's arguments; only the ones the broker's schema has are described.
var handoffArgHelp = map[string]string{
	"text":       "The developer's request, word for word: never reworded, shortened or translated. First call only.",
	"id":         "The handoff id a result gave. With it, pass answer or acceptRest and nothing else.",
	"answer":     "The developer's answer to the question, exactly as they typed it: the same words, the same case.",
	"acceptRest": "true when the developer accepts the recommendation (\"accept the rest\").",
	"repository": "owner/name, only when the developer named a repository. Otherwise leave it out: maku reads it from the workspace.",
	"context":    "Optional: paths already open in this conversation. Never read a file or run a command to fill it.",
}

// guideHandoffEntry rewrites the broker's handoff entry for the model: the description that says
// what to do first, and what each argument takes.
func guideHandoffEntry(tool map[string]any) {
	tool["description"] = handoffGuide
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for name, help := range handoffArgHelp {
		if p, ok := props[name].(map[string]any); ok {
			p["description"] = help
		}
	}
}

// continuationOnly are the arguments the broker refuses beside an id: a model that sends the
// request again with its answer is answering, so they are dropped.
var continuationOnly = []string{"text", "repository", "context", "candidates", "evidence"}

// tidyHandoffArgs makes a continuation the broker takes out of what a model sends for one, and says
// whether it changed args: beside an id the first call's arguments are dropped, an answer that only
// says "accept the rest" is acceptRest, and a real answer wins over acceptRest sent with it. A first
// call is left as it came.
func tidyHandoffArgs(args map[string]any) bool {
	if id, _ := args["id"].(string); strings.TrimSpace(id) == "" {
		return false
	}
	changed := false
	for _, k := range continuationOnly {
		if _, ok := args[k]; ok {
			delete(args, k)
			changed = true
		}
	}
	answer, _ := args["answer"].(string)
	switch accept, _ := args["acceptRest"].(bool); {
	case isAcceptRest(answer):
		delete(args, "answer")
		args["acceptRest"] = true
		changed = true
	case strings.TrimSpace(answer) != "" && accept:
		delete(args, "acceptRest")
		changed = true
	}
	return changed
}

// isAcceptRest is whether an answer is only the words that accept the recommendation.
func isAcceptRest(answer string) bool {
	s := strings.ToLower(strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(answer), "\"'“”‘’`.!")), " "))
	return s == "accept the rest" || s == "accept rest"
}

// The handoff refusals a second try can pass: the planner writes another draft each time.
var handoffRetryable = map[string]bool{
	"planner_invalid": true, "invalid_draft": true, "invalid_decisions": true, "planner_timeout": true, "plan_too_large": true,
}

const (
	nextStop     = "show the developer this line as it is and stop: nothing was filed. Do not retry, do not reword their request and do not file anything yourself"
	nextNewPlan  = "this id is gone: call handoff again with {\"text\": the developer's original words}, a new handoff"
	nextArgs     = "call handoff with {\"text\": the developer's words} to begin, or with {\"id\", \"answer\"} or {\"id\", \"acceptRest\": true} to continue, and nothing else"
	nextRetry    = "call handoff again now with exactly the same arguments, without asking the developer. Do not reword their request"
	nextGaveUp   = "stop: tell the developer in one line that Maku's planner failed twice (%s), that nothing was filed, and that they can run the same handoff again in a minute. Do not reword their request and do not file anything yourself"
	nextDoctor   = "tell the developer Maku could not be reached, that nothing was filed, and to run `maku doctor` in a terminal: it names the fix. Do not retry on your own"
	nextQuestion = "ask the developer this one question now, with its recommendation, and show nothing else of this draft: it is not final. Add no question of your own and do not answer it yourself. " +
		"In a question tool, offer two options, the recommendation and \"Accept the rest\": they can always type their own answer. " +
		"Then call handoff with {\"id\":\"%s\",\"answer\": their words exactly as typed}, or with {\"id\":\"%s\",\"acceptRest\":true} when they accept the rest"
	nextStart = "show the plan above as it is, then call start with {\"id\":\"%s\"} in this same turn. Do not ask the developer whether to start: " +
		"start opens their form, and nothing is filed until they answer it"
)

// refusalCode is the code a refusal's text opens with (`code: sentence`), "" when it has none.
func refusalCode(text string) string {
	code, _, ok := strings.Cut(strings.TrimSpace(text), ":")
	if !ok || code == "" || strings.ContainsAny(code, " \n") {
		return ""
	}
	return code
}

// handoffRefusalNext is the one next step after handoff refused with text; failed counts the
// refusals a second try can pass, this one included.
func handoffRefusalNext(text string, failed int) string {
	switch code := refusalCode(text); {
	case handoffRetryable[code] && failed < 2:
		return nextRetry
	case handoffRetryable[code]:
		return strings.Replace(nextGaveUp, "%s", code, 1)
	case code == "handoff_expired":
		return nextNewPlan
	case code == "invalid_arguments":
		return nextArgs
	case code == "unavailable":
		return nextDoctor
	}
	return nextStop
}

// guideHandoff adds to a handoff result the one call to make next: in structuredContent's `next`
// and as a last text block, so a model that reads either follows it. A refusal gets its next step
// the same way. A result it cannot read passes through as it came.
func (r *Relay) guideHandoff(res json.RawMessage) json.RawMessage {
	var out map[string]json.RawMessage
	if json.Unmarshal(res, &out) != nil {
		return res
	}
	var content []json.RawMessage
	_ = json.Unmarshal(out["content"], &content)
	next := ""
	if string(out["isError"]) == "true" {
		var first struct {
			Text string `json:"text"`
		}
		if len(content) > 0 {
			_ = json.Unmarshal(content[0], &first)
		}
		if refusalCode(first.Text) == "" {
			return res // the planner is off: one line to show, with its own next step
		}
		failed := 0
		if handoffRetryable[refusalCode(first.Text)] {
			failed = r.handoffFailed(false)
		}
		next = handoffRefusalNext(first.Text, failed)
	} else {
		var sc map[string]json.RawMessage
		if json.Unmarshal(out["structuredContent"], &sc) != nil {
			return res
		}
		var id string
		if json.Unmarshal(sc["id"], &id) != nil || id == "" {
			return res
		}
		r.handoffFailed(true)
		switch {
		case present(sc["question"]):
			next = strings.ReplaceAll(nextQuestion, "%s", id)
		case string(sc["ready"]) == "true":
			next = strings.Replace(nextStart, "%s", id, 1)
		default:
			return res
		}
		sc["next"], _ = json.Marshal(next)
		if out["structuredContent"], _ = json.Marshal(sc); out["structuredContent"] == nil {
			return res
		}
	}
	return withNext(out, content, next, res)
}

// withNext is the result out with next as its last text block; res when it cannot be written.
func withNext(out map[string]json.RawMessage, content []json.RawMessage, next string, res json.RawMessage) json.RawMessage {
	block, err := json.Marshal(map[string]string{"type": "text", "text": "next: " + next})
	if err != nil {
		return res
	}
	if out["content"], err = json.Marshal(append(content, block)); err != nil {
		return res
	}
	b, err := json.Marshal(out)
	if err != nil {
		return res
	}
	return b
}

// handoffFailed counts the handoff refusals in a row that a second try can pass, and returns the
// count; a plan that came back (reset) ends the row.
func (r *Relay) handoffFailed(reset bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reset {
		r.handoffFails = 0
		return 0
	}
	r.handoffFails++
	return r.handoffFails
}

// startRefusalNext is the one next step after the broker refused to validate the plan start was
// asked to file.
func startRefusalNext(text, id string) string {
	switch refusalCode(text) {
	case "unresolved_marker":
		return "the plan still has its question: ask the developer, call " + mcpwire.Handoff + " with {\"id\":\"" + id +
			"\",\"answer\": their words} or {\"id\":\"" + id + "\",\"acceptRest\":true}, then call start again"
	case "handoff_expired":
		return nextNewPlan
	case "unavailable":
		return nextDoctor
	}
	return nextStop
}
