// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

// maxHandoffLine bounds a run's handoff line, in runes.
const maxHandoffLine = 120

// milestonePhases are the task phases the flow marks, as strings: summary does not import the
// factory API.
var milestonePhases = map[string]bool{
	"AwaitingCI": true, "AutoMerging": true, "AwaitingHuman": true, "Merged": true,
	"Verifying": true, "Done": true, "Reverted": true, "Escalated": true,
	"Closed": true, "Stopped": true, "NoOp": true, "Rejected": true,
}

// ToolCount is a run's tool results: Failed counts error and rejected.
type ToolCount struct {
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

// JudgeMark is a run's completion-judge verdicts: how many, and the last one.
type JudgeMark struct {
	Count int    `json:"count"`
	Last  string `json:"last"`
}

// Milestone is the first appearance of a task phase in the log.
type Milestone struct {
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
	Seq   int64     `json:"seq"`
}

type flowState struct {
	runs      []flowRun // first-seen order, the last envelope.MaxRuns
	phases    []Milestone
	requested map[string]string // run id -> who asked for it, before the run joins
}

type flowRun struct {
	id, role, model, startedBy string
	reason                     string // run_phase's terminal reason, as the log holds it
	handoff                    string // its handoff summary's first line, at most 120 runes
	from, to, last             int64
	at                         time.Time
	tools                      ToolCount
	judge                      JudgeMark
	scoped, split              bool
}

func (f *flowState) run(id string) *flowRun {
	for i := range f.runs {
		if f.runs[i].id == id {
			return &f.runs[i]
		}
	}
	return nil
}

// foldFlow keeps, per run, what the animated view draws. Every event is offered to it, so it
// reads st.Facts after the task facts have replaced it.
func foldFlow(st *State, ev envelope.Event) {
	f := &st.flow
	if ev.Type == envelope.StateChanged {
		var k struct {
			Kind   string `json:"kind"`
			Phase  string `json:"phase"`
			Reason string `json:"reason"`
			Model  string `json:"model"`
			RunID  string `json:"runId"`
		}
		if json.Unmarshal(ev.Payload, &k) != nil {
			return
		}
		switch k.Kind {
		case "task":
			if st.Facts != nil && milestonePhases[st.Facts.Phase] && !f.hasPhase(st.Facts.Phase) {
				f.phases = append(f.phases, Milestone{Phase: st.Facts.Phase, At: ev.TS, Seq: ev.Seq})
			}
			return
		case "run_requested":
			if ev.Actor.Kind == envelope.ActorHuman && k.RunID != "" {
				if f.requested == nil {
					f.requested = map[string]string{}
				}
				f.requested[k.RunID] = ev.Actor.ID
			}
			return
		}
		if r := f.run(ev.RunID); r != nil && k.Kind == "run_phase" {
			r.last = max(r.last, ev.Seq)
			if k.Phase == "Running" && k.Model != "" {
				r.model = k.Model
			}
			if k.Phase != "Running" && k.Reason != "" {
				r.reason = k.Reason
			}
		}
		return
	}

	if ev.Type == envelope.Participant {
		var p envelope.ParticipantPayload
		if json.Unmarshal(ev.Payload, &p) != nil || !strings.HasPrefix(p.Principal, "agent:") {
			return
		}
		id := ev.RunID
		if id == "" {
			id = strings.TrimPrefix(p.Principal, "agent:")
		}
		switch p.Change {
		case "joined":
			if f.run(id) != nil {
				return
			}
			f.runs = append(f.runs, flowRun{id: id, role: p.Role, startedBy: f.requested[id], from: ev.Seq, last: ev.Seq, at: ev.TS})
			if len(f.runs) > envelope.MaxRuns {
				f.runs = f.runs[len(f.runs)-envelope.MaxRuns:]
			}
		case "left":
			if r := f.run(id); r != nil {
				r.to = ev.Seq
				r.last = max(r.last, ev.Seq)
			}
		}
		return
	}

	r := f.run(ev.RunID)
	if r == nil {
		return
	}
	r.last = max(r.last, ev.Seq)
	switch ev.Type {
	case envelope.ToolResult:
		var t envelope.ToolResultPayload
		if json.Unmarshal(ev.Payload, &t) != nil {
			return
		}
		if t.Status == "ok" {
			r.tools.OK++
		} else {
			r.tools.Failed++
		}
	case envelope.Message:
		var m envelope.MessagePayload
		if json.Unmarshal(ev.Payload, &m) == nil && m.Kind == envelope.KindJudgeVerdict {
			r.judge.Count++
			r.judge.Last = m.Verdict
		}
	case envelope.Handoff:
		var h envelope.HandoffPayload
		if json.Unmarshal(ev.Payload, &h) != nil {
			return
		}
		line, _, _ := strings.Cut(h.Summary, "\n")
		if rs := []rune(line); len(rs) > maxHandoffLine {
			line = string(rs[:maxHandoffLine])
		}
		r.handoff, r.scoped, r.split = line, h.Scope != nil, h.Split != nil
	}
}

func (f *flowState) hasPhase(p string) bool {
	for _, m := range f.phases {
		if m.Phase == p {
			return true
		}
	}
	return false
}
