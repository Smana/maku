// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/intake"
)

// maxText bounds the text a line quotes, in runes.
const maxText = 160

// printable keeps only printable runes: room text is untrusted, and an escape or
// bidi control would drive the developer's terminal (T10's terminal twin).
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}

// Line is one terminal row per event: seq, who, what, then its text on one line.
func Line(ev envelope.Event) string {
	who := ev.Actor.ID
	if ev.Actor.Role != "" {
		who = ev.Actor.Role + " (" + ev.Actor.ID + ")"
	}
	var p map[string]any
	_ = json.Unmarshal(ev.Payload, &p)
	field := func(k string) string {
		if v, ok := p[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	what, text := []string{string(ev.Type)}, ""
	switch ev.Type {
	case envelope.Message:
		if field("kind") == string(envelope.KindReviewVerdict) {
			what = []string{"review_verdict", field("verdict")}
		} else if d := field("delivery"); d != "" && d != string(envelope.DeliveryNone) {
			what = append(what, d)
		}
		text = field("text")
	case envelope.ToolCall:
		what = append(what, field("tool"))
		if args, ok := p["args"].(map[string]any); ok {
			text, _ = args["command"].(string)
		}
	case envelope.ToolResult:
		what = append(what, field("status"))
	case envelope.StateChanged:
		what = []string{field("kind"), field("phase"), field("reason")}
		if field("unsandboxed") == "true" {
			what = append(what, "unsandboxed")
		}
	case envelope.Handoff:
		what = []string{"handoff →", field("toRole")}
		if c := field("commit"); c != "" {
			what = append(what, "@", c)
		}
		text = field("summary")
	}
	line := fmt.Sprintf("#%d %s %s", ev.Seq, who, strings.Join(strings.Fields(strings.Join(what, " ")), " "))
	if text = strings.Join(strings.Fields(text), " "); text != "" {
		if r := []rune(text); len(r) > maxText {
			text = string(r[:maxText]) + "…"
		}
		line += ": " + text
	}
	return printable(line)
}

// SafeText is printable for multi-line text the broker sent, such as a claim:
// its newlines stay.
func SafeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}

// reasons explains the rejections maku's acts can meet (docs/reference/api.md).
var reasons = map[string]string{
	"not_permitted":          "not allowed: your role in this room or this client does not permit it, or the factory refused you a run",
	"rate_limited":           "too many actions at once, or over 3 forks then one a minute; wait and retry",
	"room_busy":              "a run is already running there, or one was just requested and has not joined yet",
	"reviewer_needs_pr":      "a reviewer needs --pr, since no agent handoff or verdict names a pull request",
	"over_budget":            "the factory refused the run: you are over your budget",
	"factory_unavailable":    "the run could not be requested right now; retry",
	"bad_action":             "the broker refused the request as malformed: check the seq, role, PR and egress allow-lists (an internal room takes none)",
	"sealed":                 "the room is sealed: it takes no more messages, and only its owner or an agents-admin may fork it",
	"conflict":               "the room changed at the same moment; retry",
	"log_unavailable":        "the room's log is unavailable right now; retry",
	"too_large":              "the fork point is past 5,000 events or 32 MiB of the room's log: fork at an earlier seq",
	"not_maintainer":         "only a maintainer of its repository, with write on it, may do this",
	"not_allowed":            "only the issue's author or a maintainer of its repository may do this",
	"not_linked":             "your account is not linked to GitHub: link it on the rooms page, then retry",
	"not_running":            "the task has already ended",
	"not_escalated":          "the task is not waiting for a retry",
	"no_question":            "the task has no open question to answer",
	"no_split":               "the task has no proposed split waiting for a decision",
	"no_plan":                "the task has no plan waiting for review",
	"act_not_enabled":        "this action is not enabled on this broker (human.roomActs)",
	"invalid_draft":          "the text was refused by the content filter or contains a secret",
	"invalid_decisions":      "the issue's Decisions section could not be parsed",
	"kill_switch":            "the factory is paused by its stop switch",
	"repository_not_allowed": "the factory does not serve this repository",
	"access_unverified":      "GitHub access could not be verified right now; retry",
	"link_unverified":        "your GitHub link could not be verified right now; retry",
	"tasks_unavailable":      "the factory could not update the task right now; retry",
	"forge_unavailable":      "GitHub could not be reached right now; retry",
	"no_deeper_workflow":     "this task has no deeper workflow to step up to (or has already stepped up once)",
	"deeper_over_cap":        "stepping up costs more than the cost confirmed for this task: to run the deeper workflow, start the task again with it (New task, or maku start --workflow <name>), which confirms its higher cost",
	"unknown_task":           "the factory has no task of that id",
	"drill_not_merged":       "a rollback drill reverts a merged pull request: this one is not merged",
	"drill_no_connection":    "the factory has no GitHub connection for this repository right now; retry",
	"drill_no_merger":        "the factory has no merger connection for this repository, and the merger opens a drill's revert: check the merger App's key and installation",
	"drill_revert_failed":    "GitHub refused the revert (the factory's log names why): check the merger App's contents: write on this repository",
}

// Explain is a rejection in words, or the reason itself when it has none.
func Explain(reason string) string {
	shown := intake.Shown(reason)
	if why, ok := reasons[reason]; ok {
		return shown + ": " + why
	}
	return printable(shown)
}
