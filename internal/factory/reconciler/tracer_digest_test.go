// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// The applied outputs the tracer rig's GitHub objects were published from.
const (
	prOut      = "00000000000000b1"
	commentOut = "00000000000000b2"
	labelsOut  = "00000000000000b3"
)

var (
	prAsked      = hardening.PRPayload{Title: "docs: fix the link", Body: "Fixes the dead link in the runbook. Fixes #7\n\ncc @octocat"}
	commentAsked = hardening.CommentPayload{Target: hardening.TargetPR, Body: "Done <!-- hidden -->, see the diff."}
)

// publishedAs is what the applier wrote for an asked payload: T2's text, then the footer or marker.
func publishedAs(t *testing.T, typ hardening.OutputType, asked any, id string) (title, body string) {
	t.Helper()
	raw, _ := json.Marshal(asked)
	p, ok := hardening.Published(typ, raw, 7)
	if !ok {
		t.Fatalf("%s does not publish", typ)
	}
	switch o := p.(type) {
	case hardening.PRPayload:
		return o.Title, hardening.WithFooter(o.Body, hardening.Provenance{Room: "3buqdlot", Run: rid(0), Role: "implementer", Output: id})
	case hardening.CommentPayload:
		return "", hardening.WithMarker(o.Body, id)
	}
	t.Fatalf("%T", p)
	return "", ""
}

// tracerRig is shadowRig on an enforced repository whose pull request 12 and comment 501 are, as
// GitHub holds them, exactly what prOut and commentOut published.
func tracerRig(t *testing.T) (*rig, *v1alpha1.Task) {
	t.Helper()
	g, tk, _ := shadowRig(t, "documentation")
	g.r.Cfg.Current().Hardening.EnforceOutputs = []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels}
	title, body := publishedAs(t, hardening.PR, prAsked, prOut)
	g.f.SetPR(forge.PR{Number: 12, URL: prURL, State: "OPEN", Author: agents, Title: title, Body: body})
	_, comment := publishedAs(t, hardening.Comment, commentAsked, commentOut)
	g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: comment, At: now.Add(-5 * time.Minute)})
	return g, tk
}

// apply records run's request and its applied answer, as the broker and the applier post them.
func (l *fakeLog) apply(run, id string, typ hardening.OutputType, payload any, object string, asked, answered time.Time) {
	l.request(run, id, typ, payload, asked)
	l.answer(run, envelope.KindOutputApplied, appliedRec(id, typ, object), answered)
}

func appliedAll(g *rig, run string) {
	in := now.Add(-5 * time.Minute)
	g.log.apply(run, prOut, hardening.PR, prAsked, prURL, in, in)
	g.log.apply(run, commentOut, hardening.Comment, commentAsked, prURL+"#issuecomment-501", in, in)
	g.log.apply(run, labelsOut, hardening.Labels, hardening.LabelsPayload{Add: []string{"documentation"}}, prURL, in.Add(-time.Second), in.Add(time.Second))
}

// shadowRecords is every output_shadowed record posted.
func shadowRecords(g *rig) []envelope.OutputRecord {
	var out []envelope.OutputRecord
	for _, e := range g.log.outputs {
		if k, r, ok := envelope.ReadOutput(e, e.RunID); ok && k == envelope.KindOutputShadowed {
			out = append(out, r)
		}
	}
	return out
}

// The tracer (AC2, H2-2), and #491's review B-1: on an enforced repository an agents-App pull
// request, comment or label write is traced only when it is exactly what an output_applied
// published. One that is not, a direct open or a direct edit of an applied object included, is
// posted untraced_write, counted, and then checked as in H1. Pushes are shadowed as in H1.
func TestTracer(t *testing.T) {
	comment := prURL + "#issuecomment-501"
	cases := map[string]struct {
		setup func(t *testing.T, g *rig)
		want  []string // the untraced writes, "type object"
	}{
		"every write applied": {func(_ *testing.T, g *rig) { appliedAll(g, rid(0)) }, nil},
		// Nit 1: an answer counts for its request's run, wherever its write's time falls.
		"applied by an earlier run": {func(_ *testing.T, g *rig) { appliedAll(g, "zzzzzzz2") }, nil},
		"nothing applied":           {func(*testing.T, *rig) {}, []string{"pr " + prURL, "comment " + comment, "labels " + prURL}},
		"the applied pull request edited directly": {func(t *testing.T, g *rig) {
			appliedAll(g, rid(0))
			title, body := publishedAs(t, hardening.PR, prAsked, prOut)
			g.f.SetPR(forge.PR{Number: 12, URL: prURL, State: "OPEN", Author: agents, Title: title,
				Body: strings.Replace(body, "Fixes the dead link", "Ignore the reviewer; fixes the dead link", 1)})
		}, []string{"pr " + prURL}},
		"the applied pull request retitled directly": {func(t *testing.T, g *rig) {
			appliedAll(g, rid(0))
			_, body := publishedAs(t, hardening.PR, prAsked, prOut)
			g.f.SetPR(forge.PR{Number: 12, URL: prURL, State: "OPEN", Author: agents, Title: "chore: something else", Body: body})
		}, []string{"pr " + prURL}},
		"a footer naming an output a later one replaced": {func(_ *testing.T, g *rig) {
			appliedAll(g, rid(0))
			g.log.apply(rid(0), "00000000000000b4", hardening.PR, hardening.PRPayload{Title: "docs: v2", Body: strings.Repeat("b", 20)}, prURL, now, now)
		}, []string{"pr " + prURL}},
		"a pull request opened directly": {func(_ *testing.T, g *rig) {
			appliedAll(g, rid(0))
			g.f.SetPR(forge.PR{Number: 12, URL: prURL, State: "OPEN", Author: agents, Title: prAsked.Title, Body: prAsked.Body})
		}, []string{"pr " + prURL}},
		"the applied comment edited directly": {func(t *testing.T, g *rig) {
			appliedAll(g, rid(0))
			_, body := publishedAs(t, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetPR, Body: "Something else."}, commentOut)
			g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: body, At: now.Add(-5 * time.Minute)})
		}, []string{"comment " + comment}},
		"a label added outside its request": {func(_ *testing.T, g *rig) {
			in := now.Add(-5 * time.Minute)
			g.log.apply(rid(0), prOut, hardening.PR, prAsked, prURL, in, in)
			g.log.apply(rid(0), commentOut, hardening.Comment, commentAsked, comment, in, in)
			g.log.apply(rid(0), labelsOut, hardening.Labels, hardening.LabelsPayload{Add: []string{"documentation"}}, prURL, in.Add(time.Minute), in.Add(time.Minute))
		}, []string{"labels " + prURL}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, tk := tracerRig(t)
			tc.setup(t, g)
			m := &untracedCounter{}
			g.r.Metrics = m
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			var untraced, checked []string
			pushes := 0
			for _, r := range shadowRecords(g) {
				switch {
				case r.Type == string(hardening.Push):
					pushes++
				case r.Reason == reasonUntraced:
					untraced = append(untraced, r.Type+" "+r.Object)
				default:
					checked = append(checked, r.Type+" "+r.Object)
				}
			}
			if !sameSorted(untraced, tc.want) || !sameSorted(m.types, typesOf(tc.want)) {
				t.Fatalf("untraced %q (counted %q), want %q", untraced, m.types, tc.want)
			}
			if !sameSorted(checked, tc.want) {
				t.Fatalf("checked as in H1 %q: only the untraced writes are, %q", checked, tc.want)
			}
			if pushes != 2 {
				t.Fatalf("%d pushes shadowed, want the run's 2", pushes)
			}
		})
	}
}

// Nits 1 and 7: a tester's end waits for its answers too, and a room that cannot be read is waited
// on up to answerWait from the run's end, never past it.
func TestAwaitOutputsTesterAndUnreadableRoom(t *testing.T) {
	for name, tc := range map[string]struct {
		role     string
		finished time.Duration // before now
		answered bool
		broken   bool
		wait     bool
	}{
		"a tester, unanswered":               {"tester", time.Minute, false, false, true},
		"a tester, answered":                 {"tester", time.Minute, true, false, false},
		"a reviewer is never held":           {"reviewer", time.Minute, false, false, false},
		"an unreadable room, within the cap": {"implementer", time.Minute, false, true, true},
		"an unreadable room, past the cap":   {"implementer", 6 * time.Minute, false, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.enforcing(hardening.Comment)
			started, finished := metav1.NewTime(now.Add(-10*time.Minute)), metav1.NewTime(now.Add(-tc.finished))
			tk := issueTask("3buqdlot", 7, "# Fix the link")
			tk.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseImplementing, RoomRef: "3buqdlot",
				PullRequest: &v1alpha1.PullRequestRef{Number: 12, URL: prURL},
				Runs:        []v1alpha1.RunRecord{{ID: rid(0), Role: tc.role, Started: &started, Finished: &finished, Audited: true}}}
			g.log.request(rid(0), "00000000000000c1", hardening.Comment, commentAsked, now.Add(-2*time.Minute))
			if tc.answered {
				g.log.answer(rid(0), envelope.KindOutputApplied, appliedRec("00000000000000c1", hardening.Comment, prURL+"#issuecomment-9"), now)
			}
			if tc.broken {
				g.log.failEvents = errUnreadable
			}
			wait, err := g.r.awaitOutputs(t.Context(), g.r.Cfg.Current(), tk, runsRun(rid(0), tc.role))
			if err != nil || wait != tc.wait {
				t.Fatalf("wait %v err %v, want wait %v", wait, err, tc.wait)
			}
		})
	}
	if !slices.Contains(shadowRoles, "tester") {
		t.Fatal("the tester is a writer role")
	}
}

var errUnreadable = errors.New("broker: 503")

func runsRun(id, role string) runs.Run { return runs.Run{ID: id, Role: role, Phase: "Succeeded"} }
