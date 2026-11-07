// SPDX-License-Identifier: Apache-2.0

// Package summary folds a room's log into the room's top layer (spec 2026-10-08): status,
// needs-you, actions and the agents' progress notes. It is pure: the web page and
// maku status render the same object, so every view agrees.
package summary

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/policy"
)

// APIVersion is the contract maku and the web page render against.
const APIVersion = "summary/v1"

// maxNotes bounds the notes a summary returns.
const maxNotes = 20

// maxWhat bounds an approval's one-line description.
const maxWhat = 200

// stopLabel is the factory's stop label (internal/factory/intake LabelStop), repeated here so
// this package stays free of the factory's imports.
const stopLabel = "factory/stop"

// holds are the factory's Queued reasons another holder of the room causes (reconciler slotFree),
// repeated here like stopLabel, each with what releases it.
func holds() map[string]string {
	return map[string]string{
		"waiting_human_driver": "the factory's task waits while a person drives this room: hand the driver back to start its next run",
		"waiting_room_busy":    "the factory's task waits for another run in this room to end",
	}
}

// IsHold reports whether reason is one of the Queued reasons another holder of the room causes.
func IsHold(reason string) bool {
	_, ok := holds()[reason]
	return ok
}

// Hold is N3's need: a factory task a person holds waits on whoever can release the room — its
// driver, or its owner, who can take the driver back. The factory's next facts end it. The room
// list's needsMe asks the same (#78).
func Hold(f *envelope.TaskFacts, sub policy.Subject, url string) (Need, bool) {
	if f == nil || f.Phase != "Queued" || holds()[f.Reason] == "" || !policy.Allowed(sub, policy.StartRun) {
		return Need{}, false
	}
	return Need{Kind: NeedHold, ID: f.Reason, What: holds()[f.Reason], URL: url}, true
}

// State is what Fold keeps of a log.
type State struct {
	Facts *envelope.TaskFacts
	// FactsAt is the ts of the event that carried Facts: when the room last heard what its task waits on,
	// which a needs/v1 item's At carries (a member's row and the room summary date an item alike).
	FactsAt   time.Time
	Verdict   *Verdict
	Judge     *JudgeVerdict
	Pending   map[string]Approval // approval id -> still open
	Notes     []Note              // oldest first, the last maxNotes
	Sealed    string              // "" while open; "Closed" (CloseRoom) or "Sealed" (MaxEvents)
	LastSeq   int64
	RoomPhase string
	Agents    map[string]bool // agent principals that joined and have not left: the runs present now
	// LLM is the model and effort of the latest run the log told them of; PrevLLM, the run's before
	// it. The model is the broker's run_phase{Running, model}, the effort the run's harness_status.
	LLM, PrevLLM *RunModel
	flow         flowState
}

// Clone is a copy of st that folds on apart from it. Add writes Pending, Agents, Notes and the
// run models in place, so a fold kept for later readers hands each a clone. Facts, Verdict and
// Judge are replaced by Add, never written through, so the clone shares them.
func (st State) Clone() State {
	c := st
	c.Pending = maps.Clone(st.Pending)
	c.Agents = maps.Clone(st.Agents)
	c.Notes = slices.Clone(st.Notes)
	if st.LLM != nil {
		m := *st.LLM
		c.LLM = &m
	}
	if st.PrevLLM != nil {
		m := *st.PrevLLM
		c.PrevLLM = &m
	}
	c.flow.runs = slices.Clone(st.flow.runs)
	c.flow.phases = slices.Clone(st.flow.phases)
	c.flow.requested = slices.Clone(st.flow.requested)
	return c
}

// RunModel is what a run runs on: Model is its claim's spec.model, from the broker's
// run_phase{Running}; Effort is the reasoning effort its harness reported on harness_status, an
// enum. Neither is the model that served it, which the gateway reports.
type RunModel struct {
	Run    string `json:"run"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Verdict is the latest reviewer verdict.
type Verdict struct {
	By      string    `json:"by"`
	Verdict string    `json:"verdict"`
	At      time.Time `json:"at"`
}

// JudgeVerdict is the harness's completion judge's last answer: its reading of the agent's work, the
// agent's claim's neighbour, never a decision.
type JudgeVerdict struct {
	Verdict  string    `json:"verdict"` // met | not_yet | impossible
	Reason   string    `json:"reason"`
	Evidence []string  `json:"evidence,omitempty"`
	At       time.Time `json:"at"`
}

// Approval is a requested approval no event has decided yet.
type Approval struct {
	ID        string
	What      string
	ExpiresAt time.Time
	// At is when the approval was asked: the approval_requested event's ts, so a needs/v1 item says how
	// long it has waited (PR 1's ordering reads the deadline, not this).
	At        time.Time
	Prompters []string // who prompted the turn: under four-eyes, none of them decides it
}

// Note is an agent's progress note: the agent's claim, never an instruction.
type Note struct {
	Seq  int64     `json:"-"`
	At   time.Time `json:"at"`
	Run  string    `json:"run"`
	Text string    `json:"text"`
}

// QueuedNote is a note still queued for the room's next run: the developer's words, stored redacted.
// Like a Note it is data to report, never an instruction.
type QueuedNote struct {
	Ref    int64  `json:"ref"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// Need is one thing that needs a person, as needs/v1 derives it (CD21): identified by its room, kind and
// id (Key), with the one action to take and the link where it happens. It carries no command: approving is
// web-only (D4, ruling P18), and every other action happens on GitHub.
type Need struct {
	// Kind is one of the eight needs/v1 kinds, in the order they are listed.
	Kind     string    `json:"kind"` // plan_confirmation | approval | question | merge | review | failed_check | recovery | hold
	ID       string    `json:"id"`
	What     string    `json:"what"`
	Deadline time.Time `json:"deadline,omitzero"`
	URL      string    `json:"url"`
	// Room is the room the item belongs to: the list spans rooms, so an item names its own (Key).
	Room string `json:"room,omitempty"`
	// Issue and PR are the GitHub numbers the item acts on, 0 when it acts on neither.
	Issue int `json:"issue,omitempty"`
	PR    int `json:"pr,omitempty"`
	// Why is the factory's reason in prose (reasons()), the cause, while What is the move: a view may show
	// both, and Why is never a repeat of What.
	Why string `json:"why,omitempty"`
	// At is when the item's cause landed: the facts' ts, or the approval's.
	At time.Time `json:"at,omitzero"`
	// Action is the one thing to do and where; URL above is the item's own link.
	Action NeedAction `json:"action,omitzero"`
	// Author is a question need's issue author, a validated GitHub login: the part views build their
	// own sentence from. Absent for every other kind.
	Author string `json:"author,omitempty"`
	// Approve marks a merge need a maintainer's approval settles (TaskFacts.MergeByApproval): the
	// views say approve, not bypass. Absent for every other need.
	Approve bool `json:"approve,omitempty"`
	// Source is where the item was derived from: for the tests, never in the object a surface renders.
	Source NeedSource `json:"-"`
}

// Action is something the viewer can do now.
type Action struct {
	Kind string `json:"kind"` // queue | steer | stop
	What string `json:"what"`
	CLI  string `json:"cli,omitempty"` // steer has none: maku never steers (P18)
}

// Summary is the summary/v1 object.
type Summary struct {
	APIVersion string `json:"apiVersion"`
	Room       string `json:"room"`
	URL        string `json:"url"`
	Status     struct {
		Phase string `json:"phase"`
		// Task is the Task this room works on, while the log carries its facts: the factory names a
		// task's room as the task, so it is the room's id. Empty in a room without a task.
		Task string `json:"task,omitempty"`
		// State is the coarse state (Coarse): set by the caller, which holds the Room's projected
		// phase and its runs; the fold alone has neither.
		State  string               `json:"state,omitempty"`
		Run    *envelope.RunFact    `json:"run"`
		Budget *envelope.BudgetFact `json:"budget"`
		Spend  *envelope.SpendFact  `json:"spend,omitempty"` // the task's dollars, beside Budget's tokens
		// SpendFinal is CD23: Spend is the task's last figure, priced again after the settle window
		// and never bucketed by the factory's facts key. Absent while the dollars may still move, and
		// for a task whose runs the meter could not price — a surface that shows the figure reads this
		// first, and says "settling" or "unpriced" instead of a number it would have to change.
		SpendFinal  bool                `json:"spendFinal,omitempty"`
		PR          *envelope.PRFact    `json:"pr"`
		Issue       *envelope.IssueFact `json:"issue"`
		LastVerdict *Verdict            `json:"lastVerdict"`
		LastJudge   *JudgeVerdict       `json:"lastJudge,omitempty"` // the completion judge's last answer (untrusted text)
		Reason      string              `json:"reason,omitempty"`    // why the task waits, when it does (#59)
		// ReasonCode is the factory's raw reason code from the last task facts, bounded: the key the
		// status line reads on every surface, beside Reason's prose. Kept in a sealed room.
		ReasonCode string `json:"reasonCode,omitempty"`
		Next       string `json:"next,omitempty"` // what happens or who acts next
		// Approvals is the live approvals the caller may decide and those only others may, as the
		// status line counts them: set by the caller, which holds the room's four-eyes rule.
		Approvals *ApprovalCounts `json:"approvals,omitempty"`
		// RunModel is the present run's model and effort; LastRunModel, the run's before it, or the
		// last run's when none is present. Each absent until a run tells them.
		RunModel     *RunModel `json:"runModel,omitempty"`
		LastRunModel *RunModel `json:"lastRunModel,omitempty"`
	} `json:"status"`
	// Runs is the task's run history, oldest first: the factory's facts, so a sealed room keeps it.
	Runs     []envelope.RunFact `json:"runs,omitempty"`
	NeedsYou []Need             `json:"needsYou"`
	// Step is the one thing a human does for this room and where (approve here, or merge or close
	// the PR on GitHub): absent when nothing waits for them.
	Step    *Step    `json:"step,omitempty"`
	Actions []Action `json:"actions"`
	Notes   struct {
		Untrusted bool   `json:"untrusted"`
		Items     []Note `json:"items"`
	} `json:"notes"`
	// Queued is the notes waiting for the next run, oldest first. The caller of View sets it: the
	// fold does not hold the queue.
	Queued struct {
		Untrusted bool         `json:"untrusted"`
		Items     []QueuedNote `json:"items"`
	} `json:"queued,omitzero"`
	// Flow is the task's path: absent in a room without a task until its second run.
	Flow *Flow `json:"flow,omitempty"`
	// Coordination is a coordination parent's view as this viewer may read it, as JSON: the caller
	// attaches it (coordinationview.Attach), the fold of a log never holds one, and summary stays
	// free of the factory's types.
	Coordination json.RawMessage `json:"coordination,omitempty"`
	Cursor       string          `json:"cursor"`
}

// Fold is the state of a whole log; Add folds the same log a page at a time.
func Fold(evs []envelope.Event) State {
	var st State
	st.Add(evs)
	return st
}

// Add folds the next events, in seq order, into st. A reader that adds each page of the log and
// drops it holds one page and the state, however long the log. Unknown kinds are ignored, so an
// older broker's summary of a newer log stays well formed.
func (st *State) Add(evs []envelope.Event) {
	if st.Pending == nil {
		st.Pending = map[string]Approval{}
	}
	if st.Agents == nil {
		st.Agents = map[string]bool{}
	}
	for _, ev := range evs {
		st.LastSeq = max(st.LastSeq, ev.Seq)
		switch ev.Type {
		case envelope.StateChanged:
			foldState(st, ev)
		case envelope.ApprovalRequested:
			var a envelope.ApprovalRequestedPayload
			if json.Unmarshal(ev.Payload, &a) == nil {
				st.Pending[a.ApprovalID] = Approval{ID: a.ApprovalID, What: describe(a), ExpiresAt: a.ExpiresAt, At: ev.TS, Prompters: a.Prompters}
			}
		case envelope.ApprovalDecided:
			var a envelope.ApprovalDecidedPayload
			if json.Unmarshal(ev.Payload, &a) == nil {
				delete(st.Pending, a.ApprovalID) // approved, denied, expired or superseded
			}
		case envelope.Participant:
			var p envelope.ParticipantPayload
			if json.Unmarshal(ev.Payload, &p) == nil && strings.HasPrefix(p.Principal, "agent:") {
				switch p.Change { // role_changed neither starts nor ends a run
				case "joined":
					st.Agents[p.Principal] = true
				case "left":
					delete(st.Agents, p.Principal)
				}
			}
		case envelope.Message:
			var m envelope.MessagePayload
			if json.Unmarshal(ev.Payload, &m) != nil {
				break
			}
			switch m.Kind {
			case envelope.KindProgress:
				st.Notes = append(st.Notes, Note{Seq: ev.Seq, At: ev.TS, Run: ev.RunID, Text: m.Text})
				if len(st.Notes) > maxNotes {
					st.Notes = st.Notes[len(st.Notes)-maxNotes:]
				}
			case envelope.KindJudgeVerdict:
				st.Judge = &JudgeVerdict{Verdict: m.Verdict, Reason: m.Text, Evidence: m.Evidence, At: ev.TS}
			case envelope.KindReviewVerdict:
				st.Verdict = &Verdict{By: ev.Actor.Role, Verdict: m.Verdict, At: ev.TS}
			}
		}
		foldFlow(st, ev) // after the switch: a forked_from has reset st, and st.Facts is the new task facts
	}
}

func foldState(st *State, ev envelope.Event) {
	var k struct {
		Kind   string `json:"kind"`
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
		Events *int64 `json:"events"`
	}
	if json.Unmarshal(ev.Payload, &k) != nil {
		return
	}
	switch k.Kind {
	case "task":
		var f envelope.TaskFacts
		if json.Unmarshal(ev.Payload, &f) == nil {
			st.Facts, st.FactsAt = &f, ev.TS
		}
	case "run_phase":
		// The broker's run_phase{Running} names the claim's spec.model, which no sandbox writes.
		var p struct {
			Model string `json:"model"`
		}
		if ev.Origin == envelope.OriginBroker && k.Phase == "Running" && json.Unmarshal(ev.Payload, &p) == nil && envelope.ValidModel(p.Model) {
			st.tell(ev.RunID, RunModel{Model: p.Model})
		}
	case "room_phase":
		st.RoomPhase = k.Phase
		// store.CloseRoom seals with room_phase{Closed}.
		if k.Phase == "Closed" {
			st.Sealed = "Closed"
		}
	case "limit":
		// The MaxEvents seal appends limit{events, bytes}; a concurrent-run refusal is also kind
		// "limit" (reason, running) but seals nothing.
		if k.Events != nil && st.Sealed == "" {
			st.Sealed = "Sealed"
		}
	case envelope.HarnessStatus:
		// The effort is the harness's: an enum, so bounded even if the sandbox chose it. A model
		// here is never read: the agent can rewrite what its harness reports.
		var p struct {
			Effort string `json:"effort"`
		}
		if ev.Origin == envelope.OriginHarness && json.Unmarshal(ev.Payload, &p) == nil && envelope.ValidEffort(p.Effort) {
			st.tell(ev.RunID, RunModel{Effort: p.Effort})
		}
	case "forked_from":
		// What came before is the source's log, copied (store.Fork): its task, approvals, verdict
		// and notes are not the fork's, and its seal, if any, did not close the new room.
		*st = State{Pending: map[string]Approval{}, Agents: map[string]bool{}, LastSeq: st.LastSeq, RoomPhase: "Open"}
	}
}

// tell folds what the log says a run runs on into the latest run's entry, or the run's before it:
// a new run moves the latest to the one before. A field already known stays unless told again.
func (st *State) tell(run string, m RunModel) {
	if run == "" {
		return
	}
	var cur *RunModel
	switch {
	case st.LLM != nil && st.LLM.Run == run:
		cur = st.LLM
	case st.PrevLLM != nil && st.PrevLLM.Run == run:
		cur = st.PrevLLM // a late word of the run before
	default:
		st.PrevLLM, st.LLM = st.LLM, &RunModel{Run: run}
		cur = st.LLM
	}
	if m.Model != "" {
		cur.Model = m.Model
	}
	if m.Effort != "" {
		cur.Effort = m.Effort
	}
}

// runModels is the present run's model and the last one before it; with no run present, the last
// run's alone. Copies, not the fold state's.
func runModels(st State) (current, last *RunModel) {
	cp := func(m *RunModel) *RunModel {
		if m == nil {
			return nil
		}
		c := *m
		return &c
	}
	if st.LLM != nil && st.Agents["agent:"+st.LLM.Run] {
		return cp(st.LLM), cp(st.PrevLLM)
	}
	return nil, cp(st.LLM)
}

// describe is an approval's one line: the redacted call's command, else its class.
func describe(a envelope.ApprovalRequestedPayload) string { return DescribeApproval(a.Class, a.Action) }

// DescribeApproval is the one line an approval of this class and redacted action shows, so that a
// room's summary and a room list row say the same.
func DescribeApproval(class string, action json.RawMessage) string {
	var call struct {
		Command string `json:"command"`
	}
	what := class
	if json.Unmarshal(action, &call) == nil && call.Command != "" {
		what = call.Command
	}
	if r := []rune(what); len(r) > maxWhat {
		what = string(r[:maxWhat]) + "…"
	}
	return what
}

// View is st as sub sees it (CD21): needs-you from the one derivation, which takes the caller's GitHub
// standing because a merge, a review and an answer are GitHub's to decide (CD26). The standing is the
// caller's where the action happens: the server resolves it once per room and passes the same one to the
// rows, the summary and the tools. Notes are those after `after`.
func View(st State, room, url string, sub policy.Subject, standing GitHubStanding, after int64, now time.Time) Summary {
	var s Summary
	s.APIVersion, s.Room, s.URL = APIVersion, room, url
	s.Cursor = fmt.Sprintf("seq:%d", st.LastSeq)
	s.Status.Phase = st.RoomPhase
	if f := st.Facts; f != nil {
		s.Status.Task = room
		s.Status.Phase, s.Status.Run, s.Status.Budget, s.Status.PR, s.Status.Issue = f.Phase, f.Run, f.Budget, f.PR, f.Issue
		s.Runs = slices.Clone(f.Runs) // a copy, not the fold state's slice
		if f.Spend != nil {
			sp := *f.Spend
			s.Status.Spend = &sp
		}
		s.Status.SpendFinal = f.SpendFinal // CD23: the factory's claim that the figure above is the last
	}
	s.Status.LastVerdict = st.Verdict
	s.Status.LastJudge = st.Judge
	s.Status.ReasonCode = ReasonCode(st.Facts)
	s.Status.RunModel, s.Status.LastRunModel = runModels(st)
	s.Flow = flowView(st)
	s.NeedsYou, s.Actions = []Need{}, []Action{}
	s.Notes.Untrusted, s.Notes.Items = true, []Note{}
	for _, n := range st.Notes {
		if n.Seq > after {
			s.Notes.Items = append(s.Notes.Items, n)
		}
	}
	if st.Sealed != "" {
		s.Status.Phase = st.Sealed // the sealed room refuses new facts, so the last task phase would be stale
		return s                   // read-only: no wait, no needs, no actions
	}
	s.Status.Reason, s.Status.Next = Waiting(st.Facts)
	// Deciding happens in the web UI: judge "could decide" as the web UI would, so a CLI caller
	// still learns an approval is needed, with a link and no command (D4).
	web := sub
	web.WebUI = true
	s.NeedsYou = Ordered(Items(NeedsIn{Room: room, URL: url, Repository: repoOf(st.Facts),
		FactsRead: true, Facts: st.Facts, FactsAt: st.FactsAt, Approvals: live(st.Pending, now),
		GitHub: standing,
		Caller: Caller{WebUI: web.WebUI, CanDecide: policy.Allowed(web, policy.Decide),
			CanClose: policy.Allowed(sub, policy.Close), CanRelease: policy.Allowed(sub, policy.StartRun)},
		Now: now}))
	if s.NeedsYou == nil {
		s.NeedsYou = []Need{} // the object's shape, for a room with nothing due: needsYou is a list
	}
	s.Step = viewStep(s, factsReason(st.Facts), url, policy.Allowed(web, policy.Decide))
	// Review requests are not needs here: GitHub already notifies them (spec D6).
	if policy.Allowed(sub, policy.Queue) {
		s.Actions = append(s.Actions, Action{Kind: "queue", What: "queue a note for the next run",
			CLI: fmt.Sprintf("maku post %s --queue '<text>'", room)})
	}
	// Web-only in the matrix (P18), and only while an agent is present: the facts' run outlives it.
	if policy.Allowed(sub, policy.Steer) && len(st.Agents) > 0 {
		s.Actions = append(s.Actions, Action{Kind: "steer", What: "steer the running agent"})
	}
	// No dedicated stop right exists: stopping closes the task, which is the owner's (Close).
	if st.Facts != nil && st.Facts.Issue != nil && policy.Allowed(sub, policy.Close) {
		// Never without --repo: run from another checkout, it would label that repository's issue.
		if repo := repoOf(st.Facts); repo != "" {
			s.Actions = append(s.Actions, Action{Kind: "stop", What: "stop this task",
				CLI: fmt.Sprintf("gh issue edit %d --repo %s --add-label %s", st.Facts.Issue.Number, repo, stopLabel)})
		}
	}
	return s
}

// viewStep is the summary's step: an approval it lists, else a merge or a question need it lists.
// Those needs carry the standing that decided them, so the step is theirs and never a second opinion.
func viewStep(s Summary, reason, roomURL string, canDecide bool) *Step {
	in := StepIn{RoomURL: roomURL, Phase: s.Status.Phase, Reason: reason}
	if p := s.Status.PR; p != nil {
		in.PR, in.PRURL = p.Number, p.URL
	}
	if i := s.Status.Issue; i != nil {
		in.Issue, in.IssueURL = i.Number, i.URL
	}
	for _, n := range s.NeedsYou {
		switch n.Kind {
		case NeedApproval:
			in.Approvals = canDecide
		case NeedMerge, NeedQuestion:
			in.CanClose = true
			in.MergeByApproval = in.MergeByApproval || n.Approve
		}
	}
	return StepFor(in)
}

// maxReasonCode bounds a reason code, in runes: the factory writes a short code.
const maxReasonCode = 64

// ReasonCode is the raw reason code of f, bounded; "" without facts.
func ReasonCode(f *envelope.TaskFacts) string {
	if f == nil {
		return ""
	}
	if r := []rune(f.Reason); len(r) > maxReasonCode {
		return string(r[:maxReasonCode])
	}
	return f.Reason
}

func factsReason(f *envelope.TaskFacts) string {
	if f == nil {
		return ""
	}
	return f.Reason
}
