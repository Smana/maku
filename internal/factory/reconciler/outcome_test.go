// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/runs"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
			v1alpha1.PhaseStopped, []string{"task_outcome review treatment review-search scoper stopped 1.5"}},
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
			if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil {
				t.Fatal("the refused write is returned")
			}
			if p := g.get(t).Status.Profile; len(outcomes()) != 0 || (p != nil && p.Counted) {
				t.Fatalf("a refused write neither counts nor marks: %v %+v", outcomes(), p)
			}
			tk = g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase {
				t.Fatalf("phase %s", tk.Status.Phase)
			}
			g.reconcile(t, "3buqdlot", 1)
			if got := outcomes(); !slices.Equal(got, c.want) || (c.want != nil) != (tk.Status.Profile != nil && tk.Status.Profile.Counted) {
				t.Fatalf("%v, counted %+v", got, tk.Status.Profile)
			}
		})
	}
}

func taskOutcomes(ms []string) []string {
	return slices.DeleteFunc(slices.Clone(ms), func(m string) bool { return !strings.HasPrefix(m, "task_outcome ") })
}

// A parent whose split was approved did none of the work: its parts count, it does not. An
// unresolved task that fails without a pull request still counts.
func TestASplitParentIsNotCounted(t *testing.T) {
	g, _ := harnessedSplit(t, 2)
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(80, "Smana"))
	g.reconcile(t, "3buqdlot", 2) // Escalated → Split, then part 1
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	g.reconcile(t, "3buqdlot", 1)
	setPhase(t, g, child(2), v1alpha1.PhaseDone, "merged")
	p := g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseDone || p.Status.Reason != "children_merged" {
		t.Fatalf("%s %s", p.Status.Phase, p.Status.Reason)
	}
	if got := taskOutcomes(g.metrics.recorded); len(got) != 0 || p.Status.Profile == nil || p.Status.Profile.Counted {
		t.Fatalf("%v %+v", got, p.Status.Profile)
	}
}

func TestAnUnresolvedTaskThatFailsIsCounted(t *testing.T) {
	g := harnessedScoper(t, nil)
	scopeHandoff(g, 0, questionScope())
	if tk := endScoper(t, g, "Succeeded", "agent_finished"); tk.Status.Reason != ReasonNeedsAuthor || tk.Status.Profile.Resolved {
		t.Fatalf("%s %q %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.Profile)
	}
	g.r.Cfg.Caps.EscalatedHours = new(168)
	g.r.Now = func() time.Time { return now.Add(200 * time.Hour) }
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseStopped || tk.Status.Reason != "escalation_expired" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if got := taskOutcomes(g.metrics.recorded); len(got) != 1 || !strings.HasPrefix(got[0], "task_outcome review treatment review-floor floor stopped") || !tk.Status.Profile.Counted {
		t.Fatalf("%v %+v", got, tk.Status.Profile)
	}
}
