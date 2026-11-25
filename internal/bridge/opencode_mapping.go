// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validStuckPatterns are the bounded patterns Maku's loop_guard writes into
// /workspace/.agent-opencode-terminal.json (OpenCode design §7.3).
var validStuckPatterns = map[string]bool{
	"repeated_action": true,
	"repeated_error":  true,
	"monologue":       true,
	"alternating":     true,
	"context_window":  true,
}

type openCodeMessage struct {
	ID      json.RawMessage   `json:"id"`
	Type    json.RawMessage   `json:"type"`
	Text    string            `json:"text"`
	Content []json.RawMessage `json:"content"`
	Outcome string            `json:"outcome"`
	Error   *openCodeError    `json:"error"`
	Command string            `json:"command"`
	ShellID string            `json:"shellID"`
	Status  string            `json:"status"`
	Output  *struct {
		Output string `json:"output"`
	} `json:"output"`
}

type openCodeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type openCodePart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	State json.RawMessage `json:"state"`
}

type openCodeToolState struct {
	Status  string          `json:"status"`
	Input   json.RawMessage `json:"input"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *openCodeError `json:"error"`
}

func makeRaw(id, kind, source string, fields map[string]any) RawEvent {
	m := make(map[string]any, len(fields)+3)
	m["id"] = id
	m["kind"] = kind
	m["source"] = source
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return RawEvent{ID: id, Kind: kind, Source: source, Raw: b}
}

// normalizeOpenCode translates one OpenCode 2.3.0 Session.Message.Info JSON
// object into zero or more normalized RawEvents consumable by Map and
// Confirmer. Malformed objects and missing IDs produce a single Malformed
// RawEvent so cursor accounting never silently skips or stalls. Unknown types
// produce a bounded harness_event without copying payload text.
func normalizeOpenCode(raw json.RawMessage) []RawEvent {
	var msg openCodeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return []RawEvent{{Raw: append(json.RawMessage{}, raw...), Malformed: true}}
	}
	var id, mtype string
	idOK := optString(msg.ID, &id) && id != ""
	typeOK := optString(msg.Type, &mtype) && mtype != ""
	if !idOK || !typeOK {
		return []RawEvent{{Raw: append(json.RawMessage{}, raw...), Malformed: true}}
	}

	switch mtype {
	case "user":
		return []RawEvent{makeRaw(id+":user", "MessageEvent", "user", map[string]any{
			"llm_message": map[string]any{
				"role":    "user",
				"content": []map[string]string{{"type": "text", "text": msg.Text}},
			},
		})}

	case "assistant":
		return normalizeOpenCodeAssistant(id, msg.Content)

	case "shell":
		callID := msg.ShellID
		if callID == "" {
			callID = id
		}
		out := []RawEvent{makeRaw(id+":shell:call", "ActionEvent", "agent", map[string]any{
			"tool_name":    "shell",
			"tool_call_id": callID,
			"action":       map[string]string{"command": msg.Command},
		})}
		if msg.Status != "" && msg.Status != "running" {
			text := ""
			if msg.Output != nil {
				text = msg.Output.Output
			}
			out = append(out, makeRaw(id+":shell:obs", "ObservationEvent", "environment", map[string]any{
				"tool_name":    "shell",
				"tool_call_id": callID,
				"observation": map[string]any{
					"content":  []map[string]string{{"type": "text", "text": text}},
					"is_error": msg.Status == "killed" || msg.Status == "timeout",
				},
			}))
		}
		return out

	case "idle":
		switch msg.Outcome {
		case "failed":
			code, detail := "error", ""
			if msg.Error != nil {
				if msg.Error.Type != "" {
					code = msg.Error.Type
				}
				detail = msg.Error.Message
			}
			return []RawEvent{makeRaw(id+":idle:err", "ConversationErrorEvent", "environment", map[string]any{
				"code":   code,
				"detail": detail,
			})}
		case "interrupted":
			return []RawEvent{makeRaw(id+":idle:int", "InterruptEvent", "environment", nil)}
		default:
			return nil
		}

	case "compaction", "system", "agent_selected", "model_selected", "location_switched", "synthetic", "skill":
		return nil

	default:
		// Unknown event kind: record only the kind, never raw payload text.
		return []RawEvent{makeRaw(id, mtype, "environment", nil)}
	}
}

func normalizeOpenCodeAssistant(mid string, parts []json.RawMessage) []RawEvent {
	var out []RawEvent
	var textParts []map[string]string
	var pendingThought []map[string]string
	for idx, rawPart := range parts {
		var p openCodePart
		if json.Unmarshal(rawPart, &p) != nil {
			out = append(out, RawEvent{Raw: append(json.RawMessage{}, rawPart...), Malformed: true})
			continue
		}
		switch p.Type {
		case "text":
			if p.Text != "" {
				textParts = append(textParts, map[string]string{"type": "text", "text": p.Text})
			}
		case "reasoning":
			if p.Text != "" {
				pendingThought = append(pendingThought, map[string]string{"type": "text", "text": p.Text})
			}
		case "tool":
			callID := p.ID
			if callID == "" {
				callID = fmt.Sprintf("%s-tool-%d", mid, idx)
			}
			var st openCodeToolState
			if len(p.State) == 0 || json.Unmarshal(p.State, &st) != nil {
				continue
			}
			if st.Status == "streaming" || st.Status == "" {
				continue
			}
			input := st.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			actionFields := map[string]any{
				"tool_name":    p.Name,
				"tool_call_id": callID,
				"action":       input,
			}
			if len(pendingThought) > 0 {
				actionFields["thought"] = pendingThought
				pendingThought = nil
			}
			out = append(out, makeRaw(mid+":tool:"+callID+":call", "ActionEvent", "agent", actionFields))
			switch st.Status {
			case "completed":
				var content []map[string]string
				for _, c := range st.Content {
					if c.Type == "text" {
						content = append(content, map[string]string{"type": "text", "text": c.Text})
					}
				}
				out = append(out, makeRaw(mid+":tool:"+callID+":obs", "ObservationEvent", "environment", map[string]any{
					"tool_name":    p.Name,
					"tool_call_id": callID,
					"observation":  map[string]any{"content": content, "is_error": false},
				}))
			case "error":
				errMsg := "tool error"
				if st.Error != nil {
					if st.Error.Message != "" {
						errMsg = st.Error.Message
					} else if st.Error.Type != "" {
						errMsg = st.Error.Type
					}
				}
				out = append(out, makeRaw(mid+":tool:"+callID+":err", "AgentErrorEvent", "environment", map[string]any{
					"tool_name":    p.Name,
					"tool_call_id": callID,
					"error":        errMsg,
				}))
			}
		default:
			if p.Type != "" {
				out = append(out, makeRaw(fmt.Sprintf("%s:part:%d", mid, idx), "opencode_"+p.Type, "environment", nil))
			}
		}
	}
	if len(textParts) > 0 {
		out = append(out, makeRaw(mid+":assistant", "MessageEvent", "agent", map[string]any{
			"llm_message": map[string]any{
				"role":    "assistant",
				"content": textParts,
			},
		}))
	}
	return out
}

func isValidSessionID(s string) bool {
	if !strings.HasPrefix(s, "ses_") || len(s) <= 4 || len(s) > 128 {
		return false
	}
	for i := 4; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
