// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/policy"
)

func ffacts(seq int64, f envelope.TaskFacts) envelope.Event {
	return fev(seq, "", envelope.StateChanged, fSys, envelope.TaskStatePayload(f))
}

func fat(seq int64) time.Time { return time.Unix(1_760_000_000+seq, 0).UTC() }

func frun(id, role, persona string, seq int64) envelope.RunFact {
	return envelope.RunFact{ID: id, Role: role, Persona: persona, StartedAt: fat(seq)}
}

// fruns joins one run per role, in order, at seq 1, 11, 21…, and the facts listing them last.
func fruns(f envelope.TaskFacts, runs ...envelope.RunFact) []envelope.Event {
	var evs []envelope.Event
	for i, r := range runs {
		seq := int64(i*10 + 1)
		runs[i].StartedAt = fat(seq)
		evs = append(evs, fjoin(seq, r.ID, r.Role))
	}
	f.Runs = runs
	return append(evs, ffacts(int64(len(runs)*10+1), f))
}

func personas(fl *Flow) []string {
	var out []string
	for _, n := range fl.Nodes {
		out = append(out, n.Persona)
	}
	return out
}

func TestFlowView(t *testing.T) {
	plan := []string{"scoper", "tester", "implementer", "reviewer"}
	many := make([]envelope.Event, 0, 22)
	manyFacts := make([]envelope.RunFact, 0, 20)
	for i := range 21 {
		id := fmt.Sprintf("run%05d", i)
		many = append(many, fjoin(int64(i+1), id, "implementer"))
		if i > 0 {
			manyFacts = append(manyFacts, envelope.RunFact{ID: id, Role: "implementer", Persona: "fixer", StartedAt: fat(int64(i + 1))})
		}
	}
	many = append(many, ffacts(30, envelope.TaskFacts{Phase: "Implementing", Runs: manyFacts}))

	tests := []struct {
		name  string
		evs   []envelope.Event
		check func(t *testing.T, fl *Flow)
	}{
		{"no task, one run", []envelope.Event{fjoin(1, "a", "implementer")},
			func(t *testing.T, fl *Flow) {
				if fl != nil {
					t.Fatalf("flow = %+v", fl)
				}
			}},
		{"no task, two runs", []envelope.Event{fjoin(1, "a", "implementer"), fleft(2, "a"), fjoin(3, "b", "reviewer")},
			func(t *testing.T, fl *Flow) {
				if fl == nil || !slices.Equal(personas(fl), []string{"implementer", "reviewer"}) || fl.Plan != nil || fl.Ghosts != nil {
					t.Fatalf("flow = %+v", fl)
				}
			}},
		{"a task with no run yet", []envelope.Event{ffacts(1, envelope.TaskFacts{Phase: "Triaged", Plan: plan})},
			func(t *testing.T, fl *Flow) {
				if fl == nil || len(fl.Nodes) != 0 || fl.Nodes == nil || !slices.Equal(fl.Ghosts, plan) {
					t.Fatalf("flow = %+v", fl)
				}
			}},
		{"persona from the facts", fruns(envelope.TaskFacts{Phase: "Reviewing"}, frun("a", "reviewer", "security-reviewer", 0)),
			func(t *testing.T, fl *Flow) {
				if !slices.Equal(personas(fl), []string{"security-reviewer"}) || fl.Nodes[0].Role != "reviewer" {
					t.Fatalf("nodes = %+v", fl.Nodes)
				}
			}},
		{"an old scoper", append(fruns(envelope.TaskFacts{Phase: "Implementing"}, frun("a", "triager", "", 0)),
			fhandoff(50, "a", envelope.HandoffPayload{Summary: "s", Scope: &envelope.Scope{}})),
			func(t *testing.T, fl *Flow) {
				if !slices.Equal(personas(fl), []string{"scoper"}) {
					t.Fatalf("personas = %v", personas(fl))
				}
			}},
		{"ghosts", fruns(envelope.TaskFacts{Phase: "Implementing", Plan: plan}, frun("a", "triager", "scoper", 0), frun("b", "tester", "tester", 0)),
			func(t *testing.T, fl *Flow) {
				if !slices.Equal(fl.Ghosts, []string{"implementer", "reviewer"}) || !slices.Equal(fl.Plan, plan) {
					t.Fatalf("ghosts = %v", fl.Ghosts)
				}
			}},
		{"a skipped step", fruns(envelope.TaskFacts{Phase: "Implementing", Plan: plan}, frun("a", "triager", "scoper", 0), frun("b", "implementer", "implementer", 0)),
			func(t *testing.T, fl *Flow) {
				if !slices.Equal(fl.Ghosts, []string{"reviewer"}) {
					t.Fatalf("ghosts = %v", fl.Ghosts)
				}
			}},
		{"no ghosts once terminal", fruns(envelope.TaskFacts{Phase: "Done", Plan: plan}, frun("a", "triager", "scoper", 0)),
			func(t *testing.T, fl *Flow) {
				if len(fl.Ghosts) != 0 {
					t.Fatalf("ghosts = %v", fl.Ghosts)
				}
			}},
		{"rounds", func() []envelope.Event {
			rev := frun("b", "reviewer", "reviewer", 0)
			rev.Verdict = "changes"
			return fruns(envelope.TaskFacts{Phase: "Reviewing"}, frun("a", "implementer", "implementer", 0), rev,
				frun("c", "implementer", "implementer", 0), frun("d", "reviewer", "reviewer", 0))
		}(), func(t *testing.T, fl *Flow) {
			var rounds []int
			for _, n := range fl.Nodes {
				rounds = append(rounds, n.Round)
			}
			if !slices.Equal(rounds, []int{1, 1, 2, 2}) {
				t.Fatalf("rounds = %v", rounds)
			}
		}},
		{"edges", func() []envelope.Event {
			ci := frun("e", "implementer", "implementer", 0)
			ci.Trigger = "ci"
			evs := fruns(envelope.TaskFacts{Phase: "Implementing", PR: &envelope.PRFact{Number: 41}},
				frun("a", "triager", "scoper", 0), frun("b", "tester", "tester", 0), frun("c", "implementer", "implementer", 0),
				frun("d", "reviewer", "reviewer", 0), ci)
			return append(evs, fhandoff(60, "a", envelope.HandoffPayload{Summary: "s", Scope: &envelope.Scope{}}))
		}(), func(t *testing.T, fl *Flow) {
			var edges []string
			for _, n := range fl.Nodes {
				edges = append(edges, n.Edge)
			}
			if want := []string{"", "plan", "failing test", "PR #41", "CI red"}; !slices.Equal(edges, want) {
				t.Fatalf("edges = %q, want %q", edges, want)
			}
		}},
		{"edges by trigger", func() []envelope.Event {
			human := frun("b", "implementer", "implementer", 0)
			human.Trigger = "human"
			rev := frun("c", "reviewer", "reviewer", 0)
			rev.Verdict = "changes"
			retry := frun("e", "implementer", "implementer", 0)
			retry.Trigger = "retry"
			resume := frun("f", "implementer", "implementer", 0)
			resume.Trigger = "resume"
			return fruns(envelope.TaskFacts{Phase: "Implementing"}, frun("a", "implementer", "implementer", 0), human, rev,
				frun("d", "implementer", "implementer", 0), retry, resume)
		}(), func(t *testing.T, fl *Flow) {
			var edges []string
			for _, n := range fl.Nodes {
				edges = append(edges, n.Edge)
			}
			if want := []string{"", "changes requested", "", "changes", "retry", "resumed"}; !slices.Equal(edges, want) {
				t.Fatalf("edges = %q, want %q", edges, want)
			}
		}},
		{"a lost run", func() []envelope.Event {
			lost := frun("a", "implementer", "implementer", 0)
			lost.EndedAt, lost.Outcome = fat(40), envelope.OutcomeLost
			return append([]envelope.Event{fjoin(1, "a", "implementer"), ftool(7, "a", "ok")},
				ffacts(9, envelope.TaskFacts{Phase: "Implementing", Runs: []envelope.RunFact{lost}}))
		}(), func(t *testing.T, fl *Flow) {
			if n := fl.Nodes[0]; n.FromSeq != 1 || n.ToSeq != 7 {
				t.Fatalf("node = %+v", n)
			}
		}},
		{"a run rotated out", many, func(t *testing.T, fl *Flow) {
			if len(fl.Nodes) != 20 || fl.Nodes[0].Run != "run00001" {
				t.Fatalf("nodes = %d, first %+v", len(fl.Nodes), fl.Nodes[0])
			}
			for _, n := range fl.Nodes {
				if n.Persona != "fixer" {
					t.Fatalf("node %s persona = %q", n.Run, n.Persona)
				}
			}
		}},
		{"split and parent", []envelope.Event{ffacts(1, envelope.TaskFacts{Phase: "Split",
			Split:  &envelope.SplitFact{Of: 3, Parts: []envelope.PartFact{{Index: 1, Issue: 52, Room: "k2mqx7ba", Outcome: "landed"}}},
			Parent: &envelope.ParentFact{Room: "p4zt6wce", Issue: 40, Index: 2, Of: 3}})},
			func(t *testing.T, fl *Flow) {
				if !reflect.DeepEqual(fl.Split, &envelope.SplitFact{Of: 3, Parts: []envelope.PartFact{{Index: 1, Issue: 52, Room: "k2mqx7ba", Outcome: "landed"}}}) ||
					!reflect.DeepEqual(fl.Parent, &envelope.ParentFact{Room: "p4zt6wce", Issue: 40, Index: 2, Of: 3}) {
					t.Fatalf("split = %+v, parent = %+v", fl.Split, fl.Parent)
				}
			}},
		{"cost and outcome", func() []envelope.Event {
			a := frun("a", "implementer", "implementer", 0)
			a.EndedAt, a.Outcome, a.MicroUSD = fat(5), envelope.OutcomeSucceeded, 820000
			a.Provenance = &envelope.ProvenanceFact{ServedModels: []string{"glm-5.3", "other"}}
			b := frun("b", "reviewer", "reviewer", 0)
			b.Verdict = "approve"
			evs := fruns(envelope.TaskFacts{Phase: "Reviewing"}, a, b)
			return append(evs, fphase(50, "b", map[string]any{"phase": "Running", "model": "kimi-k3"}))
		}(), func(t *testing.T, fl *Flow) {
			a, b := fl.Nodes[0], fl.Nodes[1]
			if a.Outcome != "succeeded" || a.MicroUSD != 820000 || !a.EndedAt.Equal(fat(5)) || a.Model != "glm-5.3" {
				t.Fatalf("a = %+v", a)
			}
			if b.Verdict != "approve" || b.Model != "kimi-k3" {
				t.Fatalf("b = %+v", b)
			}
		}},
		{"a model the log holds is a model name, else the served one", func() []envelope.Event {
			a := frun("a", "implementer", "implementer", 0)
			a.Provenance = &envelope.ProvenanceFact{ServedModels: []string{"glm-5.3"}}
			b := frun("b", "reviewer", "reviewer", 0)
			b.Provenance = &envelope.ProvenanceFact{ServedModels: []string{"<b>served</b>"}}
			evs := fruns(envelope.TaskFacts{Phase: "Implementing"}, a, b)
			return append(evs,
				fphase(50, "a", map[string]any{"phase": "Running", "model": "<img src=x>"}),
				fphase(51, "b", map[string]any{"phase": "Running", "model": "bad model\n"}))
		}(), func(t *testing.T, fl *Flow) {
			if fl.Nodes[0].Model != "glm-5.3" || fl.Nodes[1].Model != "" {
				t.Fatalf("models = %q, %q", fl.Nodes[0].Model, fl.Nodes[1].Model)
			}
		}},
		{"a node names every served model that is a model name, and its effort", func() []envelope.Event {
			a := frun("a", "implementer", "implementer", 0)
			a.Provenance = &envelope.ProvenanceFact{ServedModels: []string{"glm-5.3", "<b>x</b>", "kimi-k3", "m4", "m5", "m6", "m7", "m8", "m9", "m10"}}
			b := frun("b", "reviewer", "reviewer", 0)
			evs := fruns(envelope.TaskFacts{Phase: "Implementing"}, a, b)
			return append(evs, fstatus(50, "a", "xhigh", envelope.OriginHarness))
		}(), func(t *testing.T, fl *Flow) {
			if got := fl.Nodes[0].Models; !slices.Equal(got, []string{"glm-5.3", "kimi-k3", "m4", "m5", "m6", "m7", "m8", "m9"}) {
				t.Fatalf("models = %q", got)
			}
			if fl.Nodes[0].Effort != "xhigh" {
				t.Fatalf("effort = %q", fl.Nodes[0].Effort)
			}
			if b := fl.Nodes[1]; b.Models != nil || b.Effort != "" {
				t.Fatalf("a run with no provenance or status: %+v", b)
			}
		}},
		{"a role that is no persona name draws as unknown",
			fruns(envelope.TaskFacts{Phase: "Implementing"}, frun("a", "impl`[x](https://evil.example) <b>", "", 0)),
			func(t *testing.T, fl *Flow) {
				if fl.Nodes[0].Persona != "unknown" {
					t.Fatalf("persona = %q", fl.Nodes[0].Persona)
				}
			}},
		{"a judge's last verdict is one of three", []envelope.Event{fjoin(1, "a", "implementer"), fjudge(2, "a", "met"),
			fjoin(3, "b", "implementer"), fjudge(4, "b", "<script>")},
			func(t *testing.T, fl *Flow) {
				if j := fl.Nodes[0].Judge; j == nil || *j != (JudgeMark{Count: 1, Last: "met"}) {
					t.Fatalf("a judge = %+v", j)
				}
				if j := fl.Nodes[1].Judge; j == nil || *j != (JudgeMark{Count: 1, Last: ""}) {
					t.Fatalf("b judge = %+v", j)
				}
			}},
		{"no judge, no mark", []envelope.Event{fjoin(1, "a", "implementer"), fjoin(2, "b", "implementer")},
			func(t *testing.T, fl *Flow) {
				if fl.Nodes[0].Judge != nil {
					t.Fatalf("judge = %+v", fl.Nodes[0].Judge)
				}
			}},
		{"reason and handoff", func() []envelope.Event {
			return append(fruns(envelope.TaskFacts{Phase: "Reviewing"}, frun("a", "implementer", "implementer", 0)),
				fhandoff(50, "a", envelope.HandoffPayload{Summary: "Fixed it\nmore"}),
				fphase(51, "a", map[string]any{"phase": "Failed", "reason": "stalled"}))
		}(), func(t *testing.T, fl *Flow) {
			if n := fl.Nodes[0]; n.Reason != "stalled" || n.Handoff != "Fixed it" {
				t.Fatalf("node = %+v", n)
			}
		}},
		{"a reason is 64 bytes of plain text", []envelope.Event{fjoin(1, "a", "implementer"), fjoin(2, "b", "implementer"),
			fphase(3, "a", map[string]any{"phase": "Failed", "reason": "a\x1b[31m\u202ered\nline" + strings.Repeat("é", 40)})},
			func(t *testing.T, fl *Flow) {
				got := fl.Nodes[0].Reason
				if len(got) > 64 || strings.ContainsFunc(got, func(r rune) bool { return envelope.Control(r) || r == '\n' || r == '\t' }) ||
					!strings.HasPrefix(got, "a[31mred line") || !strings.HasSuffix(got, "é") {
					t.Fatalf("reason = %q (%d bytes)", got, len(got))
				}
			}},
		{"a person's run", []envelope.Event{
			fev(1, "", envelope.StateChanged, envelope.Actor{Kind: envelope.ActorHuman, ID: "github:ana"},
				envelope.StatePayload("run_requested", map[string]any{"runId": "b"})),
			fjoin(2, "a", "implementer"), fjoin(3, "b", "implementer")},
			func(t *testing.T, fl *Flow) {
				if fl.Nodes[1].StartedBy != "github:ana" || fl.Nodes[0].StartedBy != "" {
					t.Fatalf("nodes = %+v", fl.Nodes)
				}
			}},
		{"ordered by start, the log's when the facts have none", func() []envelope.Event {
			late := frun("a", "implementer", "implementer", 0)
			return []envelope.Event{fjoin(1, "a", "implementer"), fjoin(2, "b", "reviewer"),
				ffacts(3, envelope.TaskFacts{Phase: "Implementing", Runs: []envelope.RunFact{{ID: late.ID, Role: late.Role, StartedAt: fat(9)}}})}
		}(), func(t *testing.T, fl *Flow) {
			if fl.Nodes[0].Run != "b" || fl.Nodes[1].Run != "a" || !fl.Nodes[0].StartedAt.Equal(fat(2)) {
				t.Fatalf("nodes = %+v", fl.Nodes)
			}
		}},
		{"a split scoper's edge is split, though its handoff also scopes", append(
			fruns(envelope.TaskFacts{Phase: "Split"}, frun("a", "triager", "scoper", 0), frun("b", "implementer", "implementer", 0)),
			fhandoff(60, "a", envelope.HandoffPayload{Summary: "s", Scope: &envelope.Scope{}, Split: &envelope.Split{}})),
			func(t *testing.T, fl *Flow) {
				if fl.Nodes[1].Edge != "split" {
					t.Fatalf("edge = %q", fl.Nodes[1].Edge)
				}
			}},
		{"milestones", fruns(envelope.TaskFacts{Phase: "AwaitingCI"}, frun("a", "implementer", "implementer", 0)),
			func(t *testing.T, fl *Flow) {
				if len(fl.Milestones) != 1 || fl.Milestones[0].Phase != "AwaitingCI" {
					t.Fatalf("milestones = %+v", fl.Milestones)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := View(Fold(tt.evs), "r", "https://rooms.example/r/r", policy.Subject{Kind: envelope.ActorHuman}, 0, fat(100))
			tt.check(t, s.Flow)
		})
	}
}

func TestASealedRoomKeepsItsFlow(t *testing.T) {
	evs := append(fruns(envelope.TaskFacts{Phase: "Done"}, frun("a", "implementer", "implementer", 0)),
		fev(90, "", envelope.StateChanged, fSys, envelope.StatePayload("room_phase", map[string]any{"phase": "Closed"})))
	s := View(Fold(evs), "r", "u", policy.Subject{Kind: envelope.ActorHuman}, 0, fat(100))
	if s.Flow == nil || len(s.Flow.Nodes) != 1 {
		t.Fatalf("flow = %+v", s.Flow)
	}
}
