// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

func fev(seq int64, run string, typ envelope.Type, actor envelope.Actor, payload any) envelope.Event {
	return envelope.Event{RoomID: "r", Seq: seq, Type: typ, Actor: actor, RunID: run,
		TS: time.Unix(1_760_000_000+seq, 0).UTC(), Payload: envelope.Must(payload)}
}

var (
	fAgent = envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:aaaa1111", Role: "implementer"}
	fSys   = envelope.Actor{Kind: envelope.ActorSystem, ID: "system:runwatch"}
)

func fjoin(seq int64, run, role string) envelope.Event {
	return fev(seq, run, envelope.Participant, fSys, envelope.ParticipantPayload{Principal: "agent:" + run, Change: "joined", Role: role})
}

func fleft(seq int64, run string) envelope.Event {
	return fev(seq, run, envelope.Participant, fSys, envelope.ParticipantPayload{Principal: "agent:" + run, Change: "left"})
}

func ftool(seq int64, run, status string) envelope.Event {
	return fev(seq, run, envelope.ToolResult, fAgent, envelope.ToolResultPayload{CallID: "c", Status: status})
}

func fjudge(seq int64, run, verdict string) envelope.Event {
	return fev(seq, run, envelope.Message, fAgent, envelope.MessagePayload{Kind: envelope.KindJudgeVerdict, Verdict: verdict, Text: "why"})
}

func fphase(seq int64, run string, fields map[string]any) envelope.Event {
	return fev(seq, run, envelope.StateChanged, fSys, envelope.StatePayload("run_phase", fields))
}

func fstatus(seq int64, run, effort string, origin envelope.Origin) envelope.Event {
	ev := fev(seq, run, envelope.StateChanged, fSys, envelope.StatePayload(envelope.HarnessStatus, map[string]any{"status": "running", "effort": effort}))
	ev.Origin = origin
	return ev
}

func ftask(seq int64, phase string) envelope.Event {
	return fev(seq, "", envelope.StateChanged, fSys, envelope.TaskStatePayload(envelope.TaskFacts{Phase: phase}))
}

func fhandoff(seq int64, run string, h envelope.HandoffPayload) envelope.Event {
	return fev(seq, run, envelope.Handoff, fAgent, h)
}

func TestFoldFlow(t *testing.T) {
	const r = "aaaa1111"
	human := envelope.Actor{Kind: envelope.ActorHuman, ID: "github:ana"}
	long := strings.Repeat("é", 200)
	many := make([]envelope.Event, 0, 21)
	for i := range 21 {
		many = append(many, fjoin(int64(i+1), fmt.Sprintf("run%05d", i), "implementer"))
	}
	tests := []struct {
		name  string
		evs   []envelope.Event
		check func(t *testing.T, f flowState)
	}{
		{"a run's seq range", []envelope.Event{fjoin(5, r, "implementer"), ftool(6, r, "ok"), ftool(9, r, "ok"), fleft(10, r)},
			func(t *testing.T, f flowState) {
				if g := f.runs[0]; g.from != 5 || g.to != 10 || g.last != 10 || g.role != "implementer" {
					t.Fatalf("run = %+v", g)
				}
			}},
		{"a run's effort is its harness's last valid report", []envelope.Event{fjoin(1, r, "implementer"),
			fstatus(2, r, "low", envelope.OriginHarness), fstatus(3, r, "high", envelope.OriginHarness),
			fstatus(4, r, "<b>", envelope.OriginHarness), fstatus(5, r, "max", envelope.OriginClient),
			fjoin(6, "bbbb2222", "reviewer"), fstatus(7, "bbbb2222", "medium", envelope.OriginHarness)},
			func(t *testing.T, f flowState) {
				if f.runs[0].effort != "high" || f.runs[1].effort != "medium" {
					t.Fatalf("efforts = %q, %q", f.runs[0].effort, f.runs[1].effort)
				}
			}},
		{"a live run", []envelope.Event{fjoin(5, r, "implementer"), ftool(9, r, "ok")},
			func(t *testing.T, f flowState) {
				if g := f.runs[0]; g.to != 0 || g.last != 9 {
					t.Fatalf("run = %+v", g)
				}
			}},
		{"model", []envelope.Event{fjoin(5, r, "x"), fphase(6, r, map[string]any{"phase": "Running", "model": "glm-5.3"})},
			func(t *testing.T, f flowState) {
				if f.runs[0].model != "glm-5.3" {
					t.Fatalf("model = %q", f.runs[0].model)
				}
			}},
		{"tool results", []envelope.Event{fjoin(1, r, "x"), ftool(2, r, "ok"), ftool(3, r, "ok"), ftool(4, r, "error"), ftool(5, r, "rejected")},
			func(t *testing.T, f flowState) {
				if got := f.runs[0].tools; got != (ToolCount{OK: 2, Failed: 2}) {
					t.Fatalf("tools = %+v", got)
				}
			}},
		{"judge verdicts", []envelope.Event{fjoin(1, r, "x"), fjudge(2, r, "not_yet"), fjudge(3, r, "not_yet"), fjudge(4, r, "met")},
			func(t *testing.T, f flowState) {
				if got := f.runs[0].judge; got != (JudgeMark{Count: 3, Last: "met"}) {
					t.Fatalf("judge = %+v", got)
				}
			}},
		{"scoper's handoff", []envelope.Event{fjoin(1, r, "triager"), fhandoff(2, r, envelope.HandoffPayload{Summary: "s", Scope: &envelope.Scope{}})},
			func(t *testing.T, f flowState) {
				if g := f.runs[0]; !g.scoped || g.split {
					t.Fatalf("run = %+v", g)
				}
			}},
		{"split handoff", []envelope.Event{fjoin(1, r, "triager"), fhandoff(2, r, envelope.HandoffPayload{Summary: "s", Scope: &envelope.Scope{}, Split: &envelope.Split{}})},
			func(t *testing.T, f flowState) {
				if g := f.runs[0]; !g.scoped || !g.split {
					t.Fatalf("run = %+v", g)
				}
			}},
		{"handoff line", []envelope.Event{fjoin(1, r, "x"), fhandoff(2, r, envelope.HandoffPayload{Summary: "First line\nsecond"})},
			func(t *testing.T, f flowState) {
				if f.runs[0].handoff != "First line" {
					t.Fatalf("handoff = %q", f.runs[0].handoff)
				}
			}},
		{"handoff line cut to 120 runes", []envelope.Event{fjoin(1, r, "x"), fhandoff(2, r, envelope.HandoffPayload{Summary: long})},
			func(t *testing.T, f flowState) {
				if got := f.runs[0].handoff; got != strings.Repeat("é", 120) {
					t.Fatalf("handoff = %d runes", len([]rune(got)))
				}
			}},
		{"end reason", []envelope.Event{fjoin(1, r, "x"), fphase(2, r, map[string]any{"phase": "Running"}), fphase(3, r, map[string]any{"phase": "Failed", "reason": "stalled"})},
			func(t *testing.T, f flowState) {
				if f.runs[0].reason != "stalled" {
					t.Fatalf("reason = %q", f.runs[0].reason)
				}
			}},
		{"started by a person", []envelope.Event{
			fev(1, "", envelope.StateChanged, human, envelope.StatePayload("run_requested", map[string]any{"role": "implementer", "runId": r})),
			fjoin(2, r, "implementer")},
			func(t *testing.T, f flowState) {
				if f.runs[0].startedBy != "github:ana" {
					t.Fatalf("startedBy = %q", f.runs[0].startedBy)
				}
			}},
		{"milestones once", []envelope.Event{ftask(1, "Implementing"), ftask(2, "AwaitingCI"), ftask(3, "AwaitingHuman"), ftask(4, "AwaitingCI"), ftask(5, "Merged")},
			func(t *testing.T, f flowState) {
				want := []struct {
					p string
					s int64
				}{{"AwaitingCI", 2}, {"AwaitingHuman", 3}, {"Merged", 5}}
				if len(f.phases) != len(want) {
					t.Fatalf("phases = %+v", f.phases)
				}
				for i, w := range want {
					if f.phases[i].Phase != w.p || f.phases[i].Seq != w.s || f.phases[i].At.IsZero() {
						t.Fatalf("phase %d = %+v", i, f.phases[i])
					}
				}
			}},
		{"21 runs", many, func(t *testing.T, f flowState) {
			if len(f.runs) != 20 || f.runs[0].id != "run00001" || f.runs[19].id != "run00020" {
				t.Fatalf("runs = %d, first %q", len(f.runs), f.runs[0].id)
			}
		}},
		{"last is any event of the run", []envelope.Event{fjoin(5, r, "x"),
			fev(7, r, envelope.StateChanged, fSys, envelope.StatePayload(envelope.HarnessStatus, map[string]any{"effort": "high"}))},
			func(t *testing.T, f flowState) {
				if f.runs[0].last != 7 {
					t.Fatalf("last = %d", f.runs[0].last)
				}
			}},
		{"requests that never join stay bounded", func() []envelope.Event {
			var evs []envelope.Event
			for i := range envelope.MaxRuns + 5 {
				evs = append(evs, fev(int64(i+1), "", envelope.StateChanged, human,
					envelope.StatePayload("run_requested", map[string]any{"runId": fmt.Sprintf("req%05d", i)})))
			}
			return evs
		}(), func(t *testing.T, f flowState) {
			if len(f.requested) != envelope.MaxRuns || f.requested[0].run != "req00005" {
				t.Fatalf("requested = %d, first %+v", len(f.requested), f.requested[0])
			}
		}},
		{"a joined run's request is forgotten", []envelope.Event{
			fev(1, "", envelope.StateChanged, human, envelope.StatePayload("run_requested", map[string]any{"runId": r})),
			fjoin(2, r, "x")},
			func(t *testing.T, f flowState) {
				if len(f.requested) != 0 || f.runs[0].startedBy != "github:ana" {
					t.Fatalf("requested = %v", f.requested)
				}
			}},
		{"an event of no run", []envelope.Event{fjoin(1, r, "x"),
			fev(2, "", envelope.Message, envelope.Actor{Kind: envelope.ActorHuman, ID: "human:a"}, envelope.MessagePayload{Kind: envelope.KindChat, Text: "hi"})},
			func(t *testing.T, f flowState) {
				if g := f.runs[0]; g.last != 1 {
					t.Fatalf("run = %+v", g)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := Fold(tt.evs)
			tt.check(t, st.flow)
		})
	}
}

func TestFoldFlowHumanMessages(t *testing.T) {
	const a, b = "aaaa1111", "bbbb2222"
	human := envelope.Actor{Kind: envelope.ActorHuman, ID: "human:ana"}
	say := func(seq int64, d envelope.Delivery) envelope.Event {
		return fev(seq, "", envelope.Message, human, envelope.MessagePayload{Kind: envelope.KindChat, Text: "hi", Delivery: d})
	}
	own := fev(7, a, envelope.Message, fAgent, envelope.MessagePayload{Kind: envelope.KindChat, Text: "x", Delivery: envelope.DeliveryNone})
	sysMsg := fev(3, "", envelope.Message, fSys, envelope.MessagePayload{Kind: envelope.KindChat, Text: "x", Delivery: envelope.DeliverySteering})
	ended := &envelope.RunFact{ID: a, Role: "implementer", Outcome: "lost"}
	tests := []struct {
		name        string
		evs         []envelope.Event
		facts       *envelope.RunFact // a's facts, when the run is ended per facts
		wantSteered [2]int
		wantChat    [2]int
	}{
		{"a steer inside a run", []envelope.Event{fjoin(1, a, "implementer"), say(5, envelope.DeliverySteering), fleft(9, a)}, nil, [2]int{1}, [2]int{}},
		{"a steer at the join seq counts", []envelope.Event{fjoin(5, a, "implementer"), say(5, envelope.DeliverySteering), fleft(9, a)}, nil, [2]int{1}, [2]int{}},
		{"a steer at the leave seq counts", []envelope.Event{fjoin(1, a, "implementer"), say(9, envelope.DeliverySteering), fleft(9, a)}, nil, [2]int{1}, [2]int{}},
		{"a steer before the run counts nowhere", []envelope.Event{say(2, envelope.DeliverySteering), fjoin(5, a, "implementer")}, nil, [2]int{}, [2]int{}},
		{"a steer after the run left counts nowhere", []envelope.Event{fjoin(1, a, "implementer"), fleft(4, a), say(5, envelope.DeliverySteering)}, nil, [2]int{}, [2]int{}},
		{"chat during no run counts nowhere", []envelope.Event{say(2, envelope.DeliveryNone), fjoin(5, a, "implementer")}, nil, [2]int{}, [2]int{}},
		{"chat during a run counts as chat", []envelope.Event{fjoin(1, a, "implementer"), say(3, envelope.DeliveryNone)}, nil, [2]int{}, [2]int{1}},
		{"a steer during a live run counts", []envelope.Event{fjoin(1, a, "implementer"), say(3, envelope.DeliverySteering)}, nil, [2]int{1}, [2]int{}},
		{"overlapping runs each count the same steer", []envelope.Event{fjoin(1, a, "implementer"), fjoin(2, b, "reviewer"), say(3, envelope.DeliverySteering)}, nil, [2]int{1, 1}, [2]int{}},
		{"a run's own message counts nothing", []envelope.Event{fjoin(1, a, "implementer"), own, fjudge(8, a, "met")}, nil, [2]int{}, [2]int{}},
		{"a system actor's message counts nothing", []envelope.Event{fjoin(1, a, "implementer"), sysMsg}, nil, [2]int{}, [2]int{}},
		{"a queued message counts on no node", []envelope.Event{fjoin(1, a, "implementer"), say(3, envelope.DeliveryQueued)}, nil, [2]int{}, [2]int{}},
		{"a lost run stops counting at its last seq", []envelope.Event{fjoin(1, a, "implementer"), ftool(4, a, "ok"), say(3, envelope.DeliverySteering), say(6, envelope.DeliverySteering)}, ended, [2]int{1}, [2]int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var st State
			for _, ev := range tc.evs {
				foldFlow(&st, ev)
			}
			var gotS, gotC [2]int
			for i := range st.flow.runs {
				var rf *envelope.RunFact
				if tc.facts != nil && st.flow.runs[i].id == tc.facts.ID {
					rf = tc.facts
				}
				n := joinRun(&st.flow, rf, &st.flow.runs[i]).node
				gotS[i], gotC[i] = n.Steered, n.Chat
			}
			if gotS != tc.wantSteered || gotC != tc.wantChat {
				t.Fatalf("steered %v chat %v, want %v %v", gotS, gotC, tc.wantSteered, tc.wantChat)
			}
		})
	}
}
