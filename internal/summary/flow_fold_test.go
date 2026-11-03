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
