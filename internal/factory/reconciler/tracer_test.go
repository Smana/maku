// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// request appends an output_requested to the room, stamped as the broker stamps the run's own.
func (l *fakeLog) request(runID, id string, typ hardening.OutputType, payload any, at time.Time) {
	p, _ := json.Marshal(payload)
	rec := envelope.OutputRecord{ID: id, Type: string(typ), Payload: p, Digest: hardening.Digest(p), Bytes: len(p)}
	l.evs = append(l.evs, envelope.Event{Seq: int64(len(l.evs) + 1), RunID: runID, Type: envelope.Message, TS: at,
		Origin: envelope.OriginClient, Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID, Role: "implementer"},
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindOutputRequested, Delivery: envelope.DeliveryNone, Output: &rec})})
}

// answer appends the factory's answer to the room, as the applier posts it.
func (l *fakeLog) answer(runID string, kind envelope.MessageKind, rec envelope.OutputRecord, at time.Time) {
	l.evs = append(l.evs, envelope.Event{Seq: int64(len(l.evs) + 1), RunID: runID, Type: envelope.Message, TS: at,
		Origin: envelope.OriginClient, Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: envelope.FactoryActor},
		Payload: envelope.Must(envelope.MessagePayload{Kind: kind, Delivery: envelope.DeliveryNone, Output: &rec})})
}

const prURL = "https://github.com/Smana/cloud-native-ref/pull/12"

func appliedRec(id string, typ hardening.OutputType, object string) envelope.OutputRecord {
	return envelope.OutputRecord{ID: id, Type: string(typ), Digest: strings.Repeat("a", 64), Object: object}
}

// HR19: an ended implementer run with an unanswered output_pr waits for the applier, up to
// answerWait; the applied pull request is then detected, and past the wait the run's end goes on.
func TestRunEndWaitsForAnswers(t *testing.T) {
	start := func(t *testing.T) (*rig, *bytes.Buffer) {
		g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
		g.f.SetIssue(forge.Issue{Number: 7, Author: "smana", Association: "OWNER"})
		g.enforcing(hardening.PR)
		g.r.Cfg.Current().AgentsLogin = agents
		var buf bytes.Buffer
		g.r.Log = slog.New(slog.NewTextHandler(&buf, nil))
		if tk := g.reconcile(t, "3buqdlot", 3); tk.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatal(tk.Status.Phase)
		}
		g.runs.set(rid(0), "Running")
		g.reconcile(t, "3buqdlot", 1)
		g.log.request(rid(0), "00000000000000a1", hardening.PR, hardening.PRPayload{Title: "docs: fix the link", Body: "Fixes the dead link. Fixes #7"}, now)
		g.runs.set(rid(0), "Succeeded")
		g.log.end(rid(0), "Succeeded", "agent_finished")
		return g, &buf
	}

	t.Run("answered", func(t *testing.T) {
		g, _ := start(t)
		tk := g.reconcile(t, "3buqdlot", 2)
		if tk.Status.Phase != v1alpha1.PhaseImplementing || tk.Status.Runs[0].Shadowed {
			t.Fatalf("did not wait: %s, shadowed %v", tk.Status.Phase, tk.Status.Runs[0].Shadowed)
		}
		g.f.SetBranch("agent/3buqdlot", 12)
		g.f.SetPR(forge.PR{Number: 12, URL: prURL, State: "OPEN", NodeID: "PR_1", HeadSHA: "abc", Author: agents,
			Title: "docs: fix the link", Body: "Fixes the dead link. Fixes #7\n\n---\nAgent-Output: 00000000000000a1"})
		g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000a1", hardening.PR, prURL), now)
		tk = g.reconcile(t, "3buqdlot", 1)
		if tk.Status.PullRequest == nil || tk.Status.PullRequest.Number != 12 || tk.Status.Phase != v1alpha1.PhaseAwaitingCI {
			t.Fatalf("phase %s, pull request %+v", tk.Status.Phase, tk.Status.PullRequest)
		}
		if got := shadowed(g); len(got) != 0 {
			t.Fatalf("the applied pull request was shadowed: %v", got)
		}
	})

	t.Run("past the answer wait", func(t *testing.T) {
		g, buf := start(t)
		g.reconcile(t, "3buqdlot", 1)
		later := now.Add(6 * time.Minute)
		g.r.Now = func() time.Time { return later }
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase == v1alpha1.PhaseImplementing || !strings.Contains(buf.String(), "outputs_unanswered") {
			t.Fatalf("phase %s, log:\n%s", tk.Status.Phase, buf)
		}
	})
}

// untracedCounter is the metrics port with the tracer's counter.
type untracedCounter struct {
	fakeMetrics
	types []string
}

func (u *untracedCounter) HardeningUntraced(_ context.Context, typ string) {
	u.types = append(u.types, typ)
}

func typesOf(records []string) []string {
	var out []string
	for _, r := range records {
		out = append(out, strings.SplitN(r, " ", 2)[0])
	}
	return out
}

func sameSorted(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// HR32 (AC2): on a repository that enforces push, an agents-App push is traced only when an
// output_applied push's object names its head; any other is posted untraced_write, counted, and
// then checked as in H1. A repository that does not enforce push shadows its pushes as in H1.
func TestTracerPush(t *testing.T) {
	commit := func(tk *v1alpha1.Task, c string) string {
		return "https://github.com/" + tk.Spec.Repository + "/commit/" + strings.Repeat(c, 40)
	}
	for name, tc := range map[string]struct {
		enforce  []hardening.OutputType
		applied  []string // the heads applied pushes name
		untraced []string // the untraced pushes' heads
		checked  []string // the pushes checked as in H1
	}{
		"both pushes applied":   {[]hardening.OutputType{hardening.Push}, []string{"1", "2"}, nil, nil},
		"one push applied":      {[]hardening.OutputType{hardening.Push}, []string{"2"}, []string{"1"}, []string{"1"}},
		"nothing applied":       {[]hardening.OutputType{hardening.Push}, nil, []string{"1", "2"}, []string{"1", "2"}},
		"push not enforced":     {[]hardening.OutputType{hardening.PR}, []string{"2"}, nil, []string{"1", "2"}},
		"another commit traced": {[]hardening.OutputType{hardening.Push}, []string{"9"}, []string{"1", "2"}, []string{"1", "2"}},
	} {
		t.Run(name, func(t *testing.T) {
			g, tk, _ := shadowRig(t, "documentation")
			g.r.Cfg.Current().Hardening.EnforceOutputs = tc.enforce
			m := &untracedCounter{}
			g.r.Metrics = m
			in := now.Add(-5 * time.Minute)
			for i, c := range tc.applied {
				id := fmt.Sprintf("00000000000000c%d", i)
				g.log.request(rid(0), id, hardening.Push, nil, in)
				g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec(id, hardening.Push, commit(tk, c)), in)
			}
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			var untraced, checked, wantU, wantC []string
			for _, r := range shadowRecords(g) {
				switch {
				case r.Type != string(hardening.Push):
				case r.Reason == reasonUntraced:
					untraced = append(untraced, r.Object)
				default:
					checked = append(checked, r.Object)
				}
			}
			for _, c := range tc.untraced {
				wantU = append(wantU, commit(tk, c))
			}
			for _, c := range tc.checked {
				wantC = append(wantC, commit(tk, c))
			}
			pushesCounted := slices.DeleteFunc(slices.Clone(m.types), func(s string) bool { return s != string(hardening.Push) })
			if !sameSorted(untraced, wantU) || len(pushesCounted) != len(wantU) {
				t.Fatalf("untraced %q (counted %q), want %q", untraced, m.types, wantU)
			}
			if !sameSorted(checked, wantC) {
				t.Fatalf("checked as in H1 %q, want %q", checked, wantC)
			}
		})
	}
}
