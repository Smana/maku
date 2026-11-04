// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	requested []requestedRun // first-seen order, at most envelope.MaxRuns: runs asked for, not yet joined
}

type requestedRun struct{ run, by string }

type flowRun struct {
	id, role, model, startedBy string
	effort                     string // the harness's last reported reasoning effort, an envelope.Efforts value
	reason                     string // run_phase's terminal reason, as the log holds it
	handoff                    string // its handoff summary's first line, at most 120 runes
	from, to, last             int64
	at, leftAt                 time.Time
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
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(ev.Payload, &k) // a malformed payload still counts as the run's last event
		switch k.Kind {
		case "task":
			if st.Facts != nil && milestonePhases[st.Facts.Phase] && !f.hasPhase(st.Facts.Phase) {
				f.phases = append(f.phases, Milestone{Phase: st.Facts.Phase, At: ev.TS, Seq: ev.Seq})
			}
			return
		case "run_requested":
			if ev.Actor.Kind == envelope.ActorHuman && k.RunID != "" {
				f.requested = append(f.requested, requestedRun{k.RunID, ev.Actor.ID})
				if len(f.requested) > envelope.MaxRuns {
					f.requested = f.requested[len(f.requested)-envelope.MaxRuns:]
				}
			}
			return
		}
		if r := f.run(ev.RunID); r != nil && k.Kind == envelope.HarnessStatus {
			// As summary.go reads it: the harness's own report, an enum; the last one wins.
			if ev.Origin == envelope.OriginHarness && envelope.ValidEffort(k.Effort) {
				r.effort = k.Effort
			}
		} else if r != nil && k.Kind == "run_phase" {
			if k.Phase == "Running" && k.Model != "" {
				r.model = k.Model
			}
			if k.Phase != "Running" && k.Reason != "" {
				r.reason = k.Reason
			}
		}
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
			f.runs = append(f.runs, flowRun{id: id, role: p.Role, startedBy: f.takeRequested(id), from: ev.Seq, last: ev.Seq, at: ev.TS})
			if len(f.runs) > envelope.MaxRuns {
				f.runs = f.runs[len(f.runs)-envelope.MaxRuns:]
			}
		case "left":
			if r := f.run(id); r != nil {
				r.to, r.leftAt = ev.Seq, ev.TS
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

// takeRequested returns who asked for run id, and forgets the request.
func (f *flowState) takeRequested(id string) string {
	for i, q := range f.requested {
		if q.run == id {
			f.requested = append(f.requested[:i], f.requested[i+1:]...)
			return q.by
		}
	}
	return ""
}

func (f *flowState) hasPhase(p string) bool {
	for _, m := range f.phases {
		if m.Phase == p {
			return true
		}
	}
	return false
}

// Flow is the task's path as the animated run view draws it: its runs in start order, the plan's
// steps no run has matched yet, and the phases after the runs.
type Flow struct {
	Plan       []string             `json:"plan,omitempty"`
	Nodes      []FlowNode           `json:"nodes"`
	Ghosts     []string             `json:"ghosts,omitempty"`
	Milestones []Milestone          `json:"milestones,omitempty"`
	Split      *envelope.SplitFact  `json:"split,omitempty"`
	Parent     *envelope.ParentFact `json:"parent,omitempty"`
}

// FlowNode is one run on the path. ToSeq and EndedAt are zero while the run is live; Edge names
// the edge into it, from structured fields only. Models are every model that served the run, from
// its facts; Effort is the reasoning effort its harness last reported.
type FlowNode struct {
	Run       string     `json:"run"`
	Persona   string     `json:"persona"`
	Role      string     `json:"role"`
	Round     int        `json:"round"`
	StartedAt time.Time  `json:"startedAt,omitzero"`
	EndedAt   time.Time  `json:"endedAt,omitzero"`
	FromSeq   int64      `json:"fromSeq"`
	ToSeq     int64      `json:"toSeq"`
	Outcome   string     `json:"outcome,omitempty"`
	Verdict   string     `json:"verdict,omitempty"`
	MicroUSD  int64      `json:"microUSD,omitempty"`
	Model     string     `json:"model,omitempty"`
	Models    []string   `json:"models,omitempty"`
	Effort    string     `json:"effort,omitempty"`
	Judge     *JudgeMark `json:"judge,omitempty"`
	Tools     ToolCount  `json:"tools"`
	Edge      string     `json:"edge,omitempty"`
	StartedBy string     `json:"startedBy,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	// Handoff is the agent's own words, redacted at append: the page labels it as their claim.
	Handoff string `json:"handoff,omitempty"`
}

// maxFlowModels bounds a node's served models: the Task CRD's own bound.
const maxFlowModels = 8

// maxFlowReason bounds a node's end reason, in bytes: the log's text, rendered as is.
const maxFlowReason = 64

// terminalPhase reports whether a task phase closes the path (animated run view, decision 5).
func terminalPhase(p string) bool {
	switch p {
	case "Merged", "Done", "Reverted", "Escalated", "Closed", "Stopped", "NoOp", "Rejected":
		return true
	}
	return false
}

// triggerEdges are the edges a run's trigger names.
func triggerEdges() map[string]string {
	return map[string]string{"ci": "CI red", "human": "changes requested", "retry": "retry", "resume": "resumed"}
}

// newRound reports whether an edge starts a round: a reviewer's changes, a maintainer's or red CI.
func newRound(edge string) bool {
	return edge == "CI red" || edge == "changes requested" || edge == "changes"
}

type flowJoin struct {
	node    FlowNode
	trigger string
	scoped  bool
	split   bool
}

// flowView is the flow section of st, nil when the rail is hidden: a room without task facts
// and fewer than two runs.
func flowView(st State) *Flow {
	var joins []flowJoin
	inFacts := map[string]bool{}
	if st.Facts != nil {
		for _, rf := range st.Facts.Runs {
			inFacts[rf.ID] = true
			joins = append(joins, joinRun(&rf, st.flow.run(rf.ID)))
		}
	}
	for i := range st.flow.runs {
		if r := &st.flow.runs[i]; !inFacts[r.id] {
			joins = append(joins, joinRun(nil, r))
		}
	}
	slices.SortStableFunc(joins, func(a, b flowJoin) int {
		if c := a.node.StartedAt.Compare(b.node.StartedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.node.FromSeq, b.node.FromSeq)
	})
	if len(joins) > envelope.MaxRuns {
		joins = joins[len(joins)-envelope.MaxRuns:]
	}
	if st.Facts == nil && len(joins) < 2 {
		return nil
	}

	fl := &Flow{Nodes: make([]FlowNode, 0, len(joins)), Milestones: slices.Clone(st.flow.phases)}
	var pr *envelope.PRFact
	if f := st.Facts; f != nil {
		fl.Plan, pr = slices.Clone(f.Plan), f.PR
		if f.Split != nil {
			s := *f.Split
			s.Parts = slices.Clone(s.Parts)
			fl.Split = &s
		}
		if f.Parent != nil {
			p := *f.Parent
			fl.Parent = &p
		}
	}
	round, cursor := 1, 0
	for i, j := range joins {
		n := j.node
		if i > 0 {
			n.Edge = edgeInto(j, joins[i-1], pr)
		}
		if newRound(n.Edge) {
			round++
		}
		n.Round = round
		if k := slices.Index(fl.Plan[min(cursor, len(fl.Plan)):], n.Persona); k >= 0 {
			cursor += k + 1
		}
		fl.Nodes = append(fl.Nodes, n)
	}
	if st.Facts != nil && !terminalPhase(st.Facts.Phase) && cursor < len(fl.Plan) {
		fl.Ghosts = slices.Clone(fl.Plan[cursor:])
	}
	return fl
}

// joinRun is one node from a run's facts, its log, or both. Everything taken from the log is
// bounded here: it is rendered as text.
func joinRun(rf *envelope.RunFact, r *flowRun) flowJoin {
	var j flowJoin
	n := &j.node
	if rf != nil {
		n.Run, n.Role, n.Persona, j.trigger = rf.ID, rf.Role, rf.Persona, rf.Trigger
		n.StartedAt, n.EndedAt, n.Outcome, n.Verdict, n.MicroUSD = rf.StartedAt, rf.EndedAt, rf.Outcome, rf.Verdict, rf.MicroUSD
		if p := rf.Provenance; p != nil {
			for _, m := range p.ServedModels {
				if envelope.ValidModel(m) && len(n.Models) < maxFlowModels {
					n.Models = append(n.Models, m)
				}
			}
			if len(p.ServedModels) > 0 && envelope.ValidModel(p.ServedModels[0]) {
				n.Model = p.ServedModels[0]
			}
		}
	}
	if r != nil {
		n.Run = r.id
		if n.Role == "" {
			n.Role = r.role
		}
		if n.StartedAt.IsZero() {
			n.StartedAt = r.at
		}
		if n.EndedAt.IsZero() {
			n.EndedAt = r.leftAt // a manual room's facts, or facts that lag: a run that left is over
		}
		n.FromSeq, n.ToSeq = r.from, r.to
		if n.ToSeq == 0 && rf != nil && !rf.EndedAt.IsZero() {
			n.ToSeq = r.last // a lost run never left
		}
		if envelope.ValidModel(r.model) {
			n.Model = r.model
		}
		if r.judge.Count > 0 {
			jm := r.judge
			if jm.Last != "met" && jm.Last != "not_yet" && jm.Last != "impossible" {
				jm.Last = ""
			}
			n.Judge = &jm
		}
		n.Effort = r.effort
		n.Tools, n.StartedBy, n.Reason, n.Handoff = r.tools, r.startedBy, plainReason(r.reason), r.handoff
		j.scoped, j.split = r.scoped, r.split
	}
	if n.Persona == "" {
		n.Persona = n.Role
		// Remove once no room from before persona remains within retention (animated run view, decision 3).
		if n.Role == "triager" && j.scoped {
			n.Persona = "scoper"
		}
	}
	if !envelope.ValidPersona(n.Persona) {
		n.Persona = "unknown" // the fold reads facts unvalidated, and the role has no persona's bound
	}
	return j
}

// edgeInto is the edge from prev into j, from structured fields only (animated run view, decision 13).
func edgeInto(j, prev flowJoin, pr *envelope.PRFact) string {
	switch {
	case prev.split: // before scoped: a split handoff always carries its scope too
		return "split"
	case prev.scoped:
		return "plan"
	case triggerEdges()[j.trigger] != "":
		return triggerEdges()[j.trigger]
	case j.node.Role == "implementer" && prev.node.Role == "reviewer" && prev.node.Verdict == "changes":
		return "changes"
	case prev.node.Role == "tester":
		return "failing test"
	case j.node.Role == "reviewer" && pr != nil:
		return fmt.Sprintf("PR #%d", pr.Number)
	}
	return ""
}

// plainReason is s on one line, without control or format characters, cut to maxFlowReason bytes
// on a rune boundary.
func plainReason(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case envelope.Control(r) || r == utf8.RuneError:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	for len(s) > maxFlowReason {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
