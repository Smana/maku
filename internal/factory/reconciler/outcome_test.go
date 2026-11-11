// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/runs"
)

func TestStuckRunEscalatesAndMentions(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.reconcile(t, "3buqdlot", 3)
	g.runs.set(rid(0), "Running")
	g.log.evs = append(g.log.evs, envelope.Event{Seq: 1, RunID: rid(0), Type: envelope.ToolCall, TS: now})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatal("fresh activity")
	}
	g.r.Now = func() time.Time { return now.Add(11 * time.Minute) }
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != "stuck" || g.runs.patches[rid(0)][runs.AnnRevoked] != "manual" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if c := g.f.Comments(7); !strings.Contains(c[len(c)-1], "@Smana") {
		t.Fatalf("an escalation mentions the maintainers (§6.3): %q", c[len(c)-1])
	}
}

func TestControlIssueEngagesTheStop(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	if err := killswitch.PublishIssue(t.Context(), g.c, "agent-system", true); err != nil {
		t.Fatal(err)
	}
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseStopped || tk.Status.Reason != "kill_switch" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

func TestFitAndScores(t *testing.T) {
	done := func(tokens, budget int64) *v1alpha1.Task {
		return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Budget: v1alpha1.Budget{Tier: "standard", TaskTokens: budget}},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Usage: v1alpha1.Usage{Tokens: tokens}}}
	}
	if Fit(done(100_000, 3_000_000)) != "over" || Fit(done(2_000_000, 3_000_000)) != "fit" {
		t.Fatal("over below 20 % of the task budget, else fit (§7)")
	}
	under := &v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseEscalated, Reason: "review_rounds_exhausted"}}
	if Fit(under) != "under" || Fit(&v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseStopped}}) != "" {
		t.Fatal("under after a full attempt; a stop scores nothing")
	}
	if Fit(&v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseEscalated, Reason: "agent_error"}}) != "" {
		t.Fatal("agent_error may be the gateway's: unscored (R53)")
	}
	cl := v1alpha1.Classification{Classifier: "semantic-router", Tier: "standard",
		Shadow: []v1alpha1.ShadowVerdict{{Classifier: "jev", Tier: "frontier"}, {Classifier: "other", Tier: "light"}}}
	got := map[string]string{}
	for _, s := range Score(cl, "standard", "under") { // the right tier was frontier
		got[s.Classifier] = s.Fit
	}
	if got["semantic-router"] != "under" || got["jev"] != "fit" || got["other"] != "under" {
		t.Fatalf("%v", got)
	}
}

func TestTaskFinalLinksTheAuditChain(t *testing.T) {
	var buf bytes.Buffer
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.r.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	g.reconcile(t, "3buqdlot", 3)
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN"})
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	g.reconcile(t, "3buqdlot", 1)
	g.f.SetPR(forge.PR{Number: 12, State: "MERGED", MergedBy: "Smana"})
	g.reconcile(t, "3buqdlot", 1)
	var final map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "task.final" {
			final = m
		}
	}
	if final == nil {
		t.Fatal("no task.final line: " + buf.String())
	}
	for _, k := range []string{"task.id", "key", "contentSHA256", "classifier", "runIds", "room", "pr", "mergedBy", "phase"} {
		if final[k] == nil || final[k] == "" {
			t.Errorf("task.final lacks %s: %v", k, final)
		}
	}
}

// Loop policy part 2: a stall or spent continuations is a capability failure after a full attempt;
// a task the judge finds impossible says nothing of the tier.
func TestLoopEndsScoreTheTier(t *testing.T) {
	for reason, want := range map[string]string{"stalled": "under", "not_met": "under", "judge_impossible": ""} {
		if got := Fit(&v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseEscalated, Reason: reason}}); got != want {
			t.Errorf("Fit(%s) = %q, want %q", reason, got, want)
		}
	}
}

func TestTaskOutcomeTable(t *testing.T) {
	for name, c := range map[string]struct {
		phase, reason string
		human         bool
		want          string
	}{
		"merged by the factory":  {v1alpha1.PhaseDone, "merged", false, "accepted"},
		"merged, a human commit": {v1alpha1.PhaseDone, "merged", true, "merged_human"},
		"integration failed":     {v1alpha1.PhaseDone, "integration_failed", false, "failed"},
		"escalation expired":     {v1alpha1.PhaseStopped, "escalation_expired", false, "stopped"},
		"stopped":                {v1alpha1.PhaseStopped, "kill_switch", false, "stopped"},
		"no-op":                  {v1alpha1.PhaseNoOp, "no_change", false, "no_op"},
		"closed":                 {v1alpha1.PhaseClosed, "pr_closed", false, "closed"},
		"rejected":               {v1alpha1.PhaseRejected, "unauthorized", false, "rejected"},
		"reverted":               {v1alpha1.PhaseReverted, "reverted", true, "reverted"},
	} {
		tk := &v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: c.phase, Reason: c.reason,
			PullRequest: &v1alpha1.PullRequestRef{HumanCommits: c.human}}}
		if got := taskOutcome(tk); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

func TestTaskOutcomeCountedOnce(t *testing.T) {
	stop := func(t *testing.T, g *rig, c client.Client) {
		tk := g.get(t)
		tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
		if err := c.Update(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
	}
	stuck := func(t *testing.T, g *rig, _ client.Client) {
		g.runs.set(rid(0), "Running")
		g.log.evs = append(g.log.evs, envelope.Event{Seq: 1, RunID: rid(0), Type: envelope.ToolCall, TS: now})
		g.reconcile(t, "3buqdlot", 1)
		g.r.Now = func() time.Time { return now.Add(11 * time.Minute) }
	}
	for name, c := range map[string]struct {
		profile *v1alpha1.ProfileStatus
		end     func(*testing.T, *rig, client.Client)
		phase   string
		want    []string
	}{
		"stopped with a profile": {&v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment"}, stop,
			v1alpha1.PhaseStopped, []string{"task_outcome review treatment review-search scoper none stopped 1.5"}},
		"stopped without a profile": {nil, stop, v1alpha1.PhaseStopped, nil},
		"escalated":                 {&v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment"}, stuck, v1alpha1.PhaseEscalated, nil},
	} {
		t.Run(name, func(t *testing.T) {
			refuse := false
			cl := fake.NewClientBuilder().WithScheme(scheme()).WithStatusSubresource(&v1alpha1.Task{}, &roomv1.Room{}).
				WithObjects(issueTask("3buqdlot", 7, "x")).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, k client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					if refuse {
						refuse = false
						return apierrors.NewConflict(schema.GroupResource{Resource: "tasks"}, o.GetName(), errors.New("stale"))
					}
					return k.SubResource(sub).Update(ctx, o, opts...)
				}}).Build()
			g := newRig(t)
			g.c, g.r.Client = cl, cl
			tk := g.reconcile(t, "3buqdlot", 3)
			tk.Status.Profile, tk.Status.Usage.CostMicroUSD = c.profile, 1_500_000
			if err := cl.Status().Update(t.Context(), tk); err != nil {
				t.Fatal(err)
			}
			c.end(t, g, cl)
			outcomes := func() []string {
				var out []string
				for _, s := range g.metrics.recorded {
					if strings.HasPrefix(s, "task_outcome ") {
						out = append(out, s)
					}
				}
				return out
			}
			refuse = true
			if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter == 0 {
				t.Fatal("the refused write is returned")
			}
			if p := g.get(t).Status.Profile; len(outcomes()) != 0 || (p != nil && p.Counted) {
				t.Fatalf("a refused write neither counts nor marks: %v %+v", outcomes(), p)
			}
			tk = g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase {
				t.Fatalf("phase %s", tk.Status.Phase)
			}
			if got := outcomes(); len(got) != 0 || (tk.Status.Profile != nil && tk.Status.Profile.Counted) {
				t.Fatalf("counted before the usage settled (R34): %v %+v", got, tk.Status.Profile)
			}
			tk = g.settled(t, "3buqdlot")
			g.reconcile(t, "3buqdlot", 1)
			if got := outcomes(); !slices.Equal(got, c.want) || (c.want != nil) != (tk.Status.Profile != nil && tk.Status.Profile.Counted) {
				t.Fatalf("%v, counted %+v", got, tk.Status.Profile)
			}
		})
	}
}

// settled moves the clock past the settle window from the task's end and reconciles once: the
// pass that records the task's tokens, outcome and USD (R34).
func (g *rig) settled(t *testing.T, name string) *v1alpha1.Task {
	t.Helper()
	at := g.r.Now().Add(g.r.settleWindow(g.r.Cfg.Current()))
	g.r.Now = func() time.Time { return at }
	return g.reconcile(t, name, 1)
}

func taskOutcomes(ms []string) []string {
	return slices.DeleteFunc(slices.Clone(ms), func(m string) bool { return !strings.HasPrefix(m, "task_outcome ") })
}

// interventions is the intervention lines among the metrics recorded; a leading *Task is ignored,
// so a reconcile's result can be chained in.
func interventions(args ...any) []string {
	ms := args[len(args)-1].([]string)
	return slices.DeleteFunc(slices.Clone(ms), func(m string) bool { return !strings.HasPrefix(m, "intervention ") })
}

// An entry into Escalated counts once, by the task's cohort and profile, through a refused status
// write and the reconciles that follow; a bounce counts as needs_author instead.
func TestAnEscalationCountsOnce(t *testing.T) {
	for name, c := range map[string]struct {
		profile *v1alpha1.ProfileStatus
		want    string
	}{
		"with a profile": {&v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment"},
			"intervention escalated treatment review-search none"},
		"without a profile": {nil, "intervention escalated none none none"},
	} {
		t.Run(name, func(t *testing.T) {
			refuse := false
			cl := fake.NewClientBuilder().WithScheme(scheme()).WithStatusSubresource(&v1alpha1.Task{}, &roomv1.Room{}).
				WithObjects(issueTask("3buqdlot", 7, "x")).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, k client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					if refuse {
						refuse = false
						return apierrors.NewConflict(schema.GroupResource{Resource: "tasks"}, o.GetName(), errors.New("stale"))
					}
					return k.SubResource(sub).Update(ctx, o, opts...)
				}}).Build()
			g := newRig(t)
			g.c, g.r.Client = cl, cl
			tk := g.reconcile(t, "3buqdlot", 3)
			tk.Status.Profile = c.profile
			if err := cl.Status().Update(t.Context(), tk); err != nil {
				t.Fatal(err)
			}
			g.runs.set(rid(0), "Running")
			g.log.evs = append(g.log.evs, envelope.Event{Seq: 1, RunID: rid(0), Type: envelope.ToolCall, TS: now})
			g.reconcile(t, "3buqdlot", 1)
			g.r.Now = func() time.Time { return now.Add(11 * time.Minute) }
			refuse = true
			if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter == 0 {
				t.Fatal("the refused write is returned")
			}
			if got := interventions(g.metrics.recorded); len(got) != 0 {
				t.Fatalf("a refused write counts nothing: %v", got)
			}
			if tk = g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseEscalated {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			g.reconcile(t, "3buqdlot", 1)
			if got := interventions(g.metrics.recorded); !slices.Equal(got, []string{c.want}) {
				t.Fatalf("%v, want %q once", got, c.want)
			}
		})
	}
}

// R32: an approved split's family is counted once, on the parent, under the parent's triage
// profile: accepted when every part merged with no human commit, merged_human when any had one, the
// USD the family's whole (the parent's and every part's, every generation). The parts carry no
// profile and count nothing of their own.
func TestASplitFamilyIsCountedOnceOnTheParent(t *testing.T) {
	for name, c := range map[string]struct {
		of      int
		human   bool
		dropped bool
		stopped bool
		phase   string
		reason  string
		want    string
	}{
		"every part by the bot":  {phase: v1alpha1.PhaseDone, reason: "children_merged", want: "accepted"},
		"a part a human amended": {human: true, phase: v1alpha1.PhaseDone, reason: "children_merged", want: "merged_human"},
		"a part dropped":         {dropped: true, phase: v1alpha1.PhaseClosed, reason: "child_dropped", want: "stopped"},
		// The parent stopped with its third part never opened: every opened part landed, and the
		// family is still not accepted (R32).
		"a part never opened": {of: 3, stopped: true, phase: v1alpha1.PhaseStopped, reason: "stopped_by_annotation", want: "stopped"},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := harnessedSplit(t, max(c.of, 2))
			g.r.Now = func() time.Time { return now.Add(time.Hour) }
			g.f.SetComments(7, splitBy(80, "Smana"))
			g.reconcile(t, "3buqdlot", 2) // Escalated → Split, then part 1
			ended := func(k int, phase string, usd int64, human bool) {
				setPhase(t, g, child(k), phase, "")
				ch := childTask(t, g, k)
				ch.Status.Usage.CostMicroUSD, ch.Status.PullRequest.HumanCommits = usd, human
				if ch.Status.Profile != nil {
					t.Fatalf("part %d carries a profile: %+v", k, ch.Status.Profile)
				}
				if err := g.c.Status().Update(t.Context(), ch); err != nil {
					t.Fatal(err)
				}
			}
			ended(1, v1alpha1.PhaseDone, 300_000, false)
			g.reconcile(t, "3buqdlot", 1)
			phase := v1alpha1.PhaseDone
			if c.dropped { // stopped, its issue closed by a maintainer: dropped (SL12)
				phase = v1alpha1.PhaseStopped
				_ = g.f.CloseIssue(t.Context(), 502)
			}
			ended(2, phase, 450_000, c.human)
			if c.stopped {
				p := g.get(t)
				p.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
				if err := g.c.Update(t.Context(), p); err != nil {
					t.Fatal(err)
				}
			}
			p := g.reconcile(t, "3buqdlot", 1)
			if p.Status.Phase != c.phase || p.Status.Reason != c.reason {
				t.Fatalf("%s %s", p.Status.Phase, p.Status.Reason)
			}
			p.Status.Usage.CostMicroUSD = 250_000 // the scoper's own spend
			if err := g.c.Status().Update(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			p = g.settled(t, "3buqdlot")
			g.reconcile(t, "3buqdlot", 2)
			want := []string{"task_outcome review treatment review-floor floor review-wide " + c.want + " 1"}
			if got := taskOutcomes(g.metrics.recorded); !slices.Equal(got, want) || p.Status.Profile == nil || !p.Status.Profile.Counted {
				t.Fatalf("%v, want %v; %+v", got, want, p.Status.Profile)
			}
		})
	}
}

// R32's order over every part of the approved split, opened or not: merged_human wins over every
// other part; else the first part by index that is not accepted; a part the family ended before its
// own end, or never opened, reads as the parent's outcome; a part the parent recorded reverted is
// reverted whatever its task's first end. The family is built as approval builds it: of parts, the
// opened ones appended in order, each with its newest generation's task.
func TestFamilyOutcomeOrder(t *testing.T) {
	part := func(phase string, human bool) *v1alpha1.Task {
		return &v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: phase, PullRequest: &v1alpha1.PullRequestRef{HumanCommits: human}}}
	}
	for name, c := range map[string]struct {
		of       int32
		opened   []*v1alpha1.Task
		reverted int32 // the part the parent recorded reverted, 0 for none
		want     string
	}{
		"all accepted":               {2, []*v1alpha1.Task{part(v1alpha1.PhaseDone, false), part(v1alpha1.PhaseDone, false)}, 0, "accepted"},
		"a human after a stop":       {2, []*v1alpha1.Task{part(v1alpha1.PhaseStopped, false), part(v1alpha1.PhaseDone, true)}, 0, "merged_human"},
		"first by index":             {2, []*v1alpha1.Task{part(v1alpha1.PhaseStopped, false), part(v1alpha1.PhaseClosed, false)}, 0, "stopped"},
		"an open part, the parent's": {2, []*v1alpha1.Task{part(v1alpha1.PhaseDone, false), part(v1alpha1.PhaseImplementing, false)}, 0, "closed"},
		"never opened, the parent's": {2, []*v1alpha1.Task{part(v1alpha1.PhaseDone, false)}, 0, "closed"},
		"three of, one unopened":     {3, []*v1alpha1.Task{part(v1alpha1.PhaseDone, false), part(v1alpha1.PhaseDone, false)}, 0, "closed"},
		"reverted on the parent":     {2, []*v1alpha1.Task{part(v1alpha1.PhaseDone, false), part(v1alpha1.PhaseDone, false)}, 1, "reverted"},
		"no part":                    {2, nil, 0, "closed"},
	} {
		t.Run(name, func(t *testing.T) {
			p := &v1alpha1.Task{Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, Split: &v1alpha1.SplitStatus{State: SplitApproved, Of: c.of}}}
			newest := map[int32]*v1alpha1.Task{}
			for i, ch := range c.opened {
				k := int32(i + 1)
				sc := v1alpha1.SplitChild{Index: k, Issue: 500 + int(k), Task: child(int(k))}
				if k == c.reverted {
					sc.Outcome = ChildReverted
				}
				p.Status.Split.Children = append(p.Status.Split.Children, sc)
				newest[k] = ch
			}
			if got := familyOutcome(p, newest); got != c.want {
				t.Fatalf("%s, want %s", got, c.want)
			}
		})
	}
}

// R34: a read the count needs that fails (here the family's) leaves the status as it was: neither
// the settle mark nor the count lands, and the next pass counts once.
func TestAFailedFamilyReadRetriesTheCount(t *testing.T) {
	g, _ := harnessedSplit(t, 2)
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(80, "Smana"))
	g.reconcile(t, "3buqdlot", 2)
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "")
	g.reconcile(t, "3buqdlot", 1)
	setPhase(t, g, child(2), v1alpha1.PhaseDone, "")
	if p := g.reconcile(t, "3buqdlot", 1); p.Status.Phase != v1alpha1.PhaseDone {
		t.Fatal(p.Status.Phase)
	}
	refuse := false
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
			if _, ok := l.(*v1alpha1.TaskList); ok && refuse {
				refuse = false
				return errors.New("the family read refused")
			}
			return cl.List(ctx, l, opts...)
		}})
	refuse = true
	g.r.Now = func() time.Time { return now.Add(time.Hour + g.r.settleWindow(g.r.Cfg.Current())) }
	if _, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: taskKey("3buqdlot")}); err == nil {
		t.Fatal("the refusal should surface")
	}
	p := g.get(t)
	if p.Status.UsageSettled || p.Status.Profile.Counted || len(taskOutcomes(g.metrics.recorded)) != 0 {
		t.Fatalf("a failed read must leave the status as it was: settled %v counted %v %v", p.Status.UsageSettled, p.Status.Profile.Counted, g.metrics.recorded)
	}
	p = g.reconcile(t, "3buqdlot", 2)
	if got := taskOutcomes(g.metrics.recorded); len(got) != 1 || !strings.Contains(got[0], " accepted ") || !p.Status.UsageSettled || !p.Status.Profile.Counted {
		t.Fatalf("%v %+v", got, p.Status)
	}
}

// R34: a parent counts its family once every part's usage has settled, so a part's late cost is in
// the family's USD; it waits on the poll past its own window, and no longer than the family bound
// (twice the settle window from its end), after which it counts what it has.
func TestAParentWaitsForItsPartsToSettle(t *testing.T) {
	for _, late := range []bool{true, false} {
		t.Run(map[bool]string{true: "the part settles", false: "the part never settles"}[late], func(t *testing.T) {
			g, _ := harnessedSplit(t, 2)
			g.r.Now = func() time.Time { return now.Add(time.Hour) }
			g.f.SetComments(7, splitBy(80, "Smana"))
			g.reconcile(t, "3buqdlot", 2)
			setPhase(t, g, child(1), v1alpha1.PhaseDone, "")
			g.reconcile(t, "3buqdlot", 1)
			setPhase(t, g, child(2), v1alpha1.PhaseDone, "")
			ch := childTask(t, g, 2) // a part whose run the meter still prices: ended, not settled
			ch.Status.Runs = []v1alpha1.RunRecord{{ID: runID(child(2), 0), CostMicroUSD: 100_000}}
			ch.Status.Usage.CostMicroUSD = 100_000
			if err := g.c.Status().Update(t.Context(), ch); err != nil {
				t.Fatal(err)
			}
			if p := g.reconcile(t, "3buqdlot", 1); p.Status.Phase != v1alpha1.PhaseDone {
				t.Fatal(p.Status.Phase)
			}
			ended := g.r.Now()
			g.r.Now = func() time.Time { return ended.Add(g.r.settleWindow(g.r.Cfg.Current())) }
			res, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: taskKey("3buqdlot")})
			if err != nil {
				t.Fatal(err)
			}
			p := g.get(t)
			if p.Status.UsageSettled || len(taskOutcomes(g.metrics.recorded)) != 0 || res.RequeueAfter != g.r.Cfg.Current().Poll.Tasks.Duration {
				t.Fatalf("the parent waits for the part on the poll: settled %v %v requeue %s", p.Status.UsageSettled, g.metrics.recorded, res.RequeueAfter)
			}
			want := "0.1"
			if late {
				ch = childTask(t, g, 2)
				ch.Status.Usage.CostMicroUSD, ch.Status.Runs[0].CostMicroUSD, ch.Status.UsageSettled = 450_000, 450_000, true
				if err := g.c.Status().Update(t.Context(), ch); err != nil {
					t.Fatal(err)
				}
				g.r.Now = func() time.Time { return ended.Add(g.r.settleWindow(g.r.Cfg.Current()) + time.Minute) }
				want = "0.45"
			} else {
				g.r.Now = func() time.Time { return ended.Add(2 * g.r.settleWindow(g.r.Cfg.Current())) }
			}
			p = g.reconcile(t, "3buqdlot", 2)
			if got := taskOutcomes(g.metrics.recorded); len(got) != 1 || !strings.HasSuffix(got[0], " accepted "+want) || !p.Status.UsageSettled {
				t.Fatalf("%v, want usd %s; settled %v", got, want, p.Status.UsageSettled)
			}
		})
	}
}

// R34: the outcome and the settle window are the first end's; a revert before the settle revises
// neither: the task counts accepted, at the window its first end opened.
func TestARevertBeforeTheSettleRevisesNothing(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	tk := g.reconcile(t, "3buqdlot", 3)
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment", Resolved: true}
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12"}
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseDone, "merged"); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = g.get(t)
	if tk.Status.EndedAt == nil || !tk.Status.EndedAt.Equal(tk.Status.PhaseSince) || tk.Status.Outcome != "accepted" {
		t.Fatalf("the first end is frozen: %+v %q", tk.Status.EndedAt, tk.Status.Outcome)
	}
	half := g.r.settleWindow(g.r.Cfg.Current()) / 2
	g.r.Now = func() time.Time { return now.Add(half) }
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseReverted, "main_red"); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = g.get(t)
	if tk.Status.Phase != v1alpha1.PhaseReverted || !tk.Status.EndedAt.Time.Equal(now) || tk.Status.Outcome != "accepted" {
		t.Fatalf("%s %v %q", tk.Status.Phase, tk.Status.EndedAt, tk.Status.Outcome)
	}
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow(g.r.Cfg.Current())) } // the first end's window, not the revert's
	tk = g.reconcile(t, "3buqdlot", 2)
	want := []string{"task_outcome review treatment review-search scoper none accepted 0"}
	if got := taskOutcomes(g.metrics.recorded); !slices.Equal(got, want) || !tk.Status.UsageSettled {
		t.Fatalf("%v, want %v; settled %v", got, want, tk.Status.UsageSettled)
	}
}

// R34: the task's USD is counted once its usage has settled — terminal, then twice the meter's
// poll and 30 s — so a cost the meter prices after the end is in it; and once only, through the
// reconciles that follow.
func TestTaskUSDIsCountedOnceTheUsageSettled(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	tk := g.reconcile(t, "3buqdlot", 3)
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment", Resolved: true}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
	if err := g.c.Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if tk = g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseStopped {
		t.Fatal(tk.Status.Phase)
	}
	g.r.Costs = fakeCosts{rid(0): {MicroUSD: 420_000}} // priced after the end: a late reading
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow(g.r.Cfg.Current()) - time.Second) }
	if tk = g.reconcile(t, "3buqdlot", 1); tk.Status.UsageSettled || len(taskOutcomes(g.metrics.recorded)) != 0 {
		t.Fatalf("counted inside the window: %v", g.metrics.recorded)
	}
	tk = g.settled(t, "3buqdlot")
	g.reconcile(t, "3buqdlot", 2)
	want := []string{"task_outcome review treatment review-search scoper none stopped 0.42"}
	if got := taskOutcomes(g.metrics.recorded); !slices.Equal(got, want) || !tk.Status.UsageSettled || !tk.Status.Profile.Counted {
		t.Fatalf("%v, want %v once; %+v", got, want, tk.Status.Profile)
	}
}

func TestAnUnresolvedTaskThatFailsIsCounted(t *testing.T) {
	g := harnessedScoper(t, nil)
	scopeHandoff(g, 0, questionScope())
	if tk := endScoper(t, g, "Succeeded", "agent_finished"); tk.Status.Reason != ReasonNeedsAuthor || tk.Status.Profile.Resolved {
		t.Fatalf("%s %q %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.Profile)
	}
	g.r.Cfg.Current().Caps.EscalatedHours = new(168)
	g.r.Now = func() time.Time { return now.Add(200 * time.Hour) }
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseStopped || tk.Status.Reason != "escalation_expired" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	tk = g.settled(t, "3buqdlot")
	if got := taskOutcomes(g.metrics.recorded); len(got) != 1 || !strings.HasPrefix(got[0], "task_outcome review treatment review-floor floor none stopped") || !tk.Status.Profile.Counted {
		t.Fatalf("%v %+v", got, tk.Status.Profile)
	}
}

// The outcome carries the scoper's recommendation as ProfileRecommendation does: an offered name,
// none without one, other for a name the class no longer offers.
func TestTaskOutcomeCarriesTheRecommendation(t *testing.T) {
	for name, c := range map[string]struct {
		profile v1alpha1.ProfileStatus
		want    string
	}{
		"applied":     {v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment", Recommended: "review-search", Resolved: true}, "review-search"},
		"control":     {v1alpha1.ProfileStatus{Name: "review-floor", Source: "control", Cohort: "control", Recommended: "review-search", Resolved: true}, "review-search"},
		"stepped":     {v1alpha1.ProfileStatus{Name: "review-frontier", Source: "escalation", Cohort: "treatment", Recommended: "review-search", Resolved: true}, "review-search"},
		"none":        {v1alpha1.ProfileStatus{Name: "review-floor", Source: "floor", Cohort: "treatment", Resolved: true}, "none"},
		"not offered": {v1alpha1.ProfileStatus{Name: "review-floor", Source: "floor", Cohort: "treatment", Recommended: "review-gone", Resolved: true}, "other"},
	} {
		t.Run(name, func(t *testing.T) {
			g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) { tk.Status.Profile = &c.profile })
			tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
			if err := g.c.Update(t.Context(), tk); err != nil {
				t.Fatal(err)
			}
			if tk = g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseStopped {
				t.Fatal(tk.Status.Phase)
			}
			g.settled(t, "3buqdlot")
			want := fmt.Sprintf("task_outcome review %s %s %s %s stopped 0", c.profile.Cohort, c.profile.Name, c.profile.Source, c.want)
			if got := taskOutcomes(g.metrics.recorded); !slices.Equal(got, []string{want}) {
				t.Fatalf("%v, want %q", got, want)
			}
		})
	}
}
