// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
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

// The tracer (AC2, H2-2): on an enforced repository every agents-App pull request, comment and
// label write is matched to an output_applied; one that is not is posted untraced_write and counted.
// Pushes are shadowed as in H1.
func TestTracer(t *testing.T) {
	cases := map[string]struct {
		applied func(g *rig)
		want    []string // the untraced records, "type object"
	}{
		"every write applied": {func(g *rig) {
			in := now.Add(-5 * time.Minute)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b1", hardening.PR, prURL), in)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b2", hardening.Comment, prURL+"#issuecomment-501"), in)
			g.log.request(rid(0), "00000000000000b3", hardening.Labels, hardening.LabelsPayload{Add: []string{"documentation"}}, in.Add(-time.Second))
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b3", hardening.Labels, prURL), in.Add(time.Second))
		}, nil},
		"nothing applied": {func(*rig) {}, []string{"pr " + prURL, "comment " + prURL + "#issuecomment-501", "labels " + prURL}},
		"a marker the comment does not end with": {func(g *rig) {
			in := now.Add(-5 * time.Minute)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b1", hardening.PR, prURL), in)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000ff", hardening.Comment, prURL+"#issuecomment-501"), in)
		}, []string{"comment " + prURL + "#issuecomment-501", "labels " + prURL}},
		"a label added outside its request": {func(g *rig) {
			in := now.Add(-5 * time.Minute)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b1", hardening.PR, prURL), in)
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b2", hardening.Comment, prURL+"#issuecomment-501"), in)
			g.log.request(rid(0), "00000000000000b3", hardening.Labels, hardening.LabelsPayload{Add: []string{"documentation"}}, in.Add(time.Minute))
			g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000b3", hardening.Labels, prURL), in.Add(time.Minute))
		}, []string{"labels " + prURL}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, tk, _ := shadowRig(t, "documentation")
			g.r.Cfg.Current().Hardening.EnforceOutputs = []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels}
			g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: hardening.WithMarker("done", "00000000000000b2"), At: now.Add(-5 * time.Minute)})
			tc.applied(g)
			m := &untracedCounter{}
			g.r.Metrics = m
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			got := shadowed(g)
			var untraced []string
			for k, r := range got {
				if r.Reason == reasonUntraced {
					untraced = append(untraced, k)
				}
			}
			if !sameSorted(untraced, tc.want) || !sameSorted(m.types, typesOf(tc.want)) {
				t.Fatalf("untraced %q (counted %q), want %q", untraced, m.types, tc.want)
			}
			commit := "https://github.com/Smana/cloud-native-ref/commit/"
			for _, push := range []string{"push " + commit + strings.Repeat("1", 40), "push " + commit + strings.Repeat("2", 40)} {
				if r, ok := got[push]; !ok || r.Reason == reasonUntraced {
					t.Fatalf("pushes are shadowed as in H1: %v", got)
				}
			}
			for k, r := range got {
				if !strings.HasPrefix(k, "push ") && r.Reason != reasonUntraced {
					t.Errorf("%s: a write of an enforced type was shadowed as H1's: %+v", k, r)
				}
			}
		})
	}
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
