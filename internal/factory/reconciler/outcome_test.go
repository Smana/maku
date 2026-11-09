// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
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

// TE: a task's outcome, the column team evaluation compares cohorts on.
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

// TE: an ended task with a profile is counted once, with its USD; the guard rides the status.
func TestTaskOutcomeCountedOnce(t *testing.T) {
	g := newRig(t)
	ended := func(phase string, p *v1alpha1.ProfileStatus) *v1alpha1.Task {
		return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{PredictedClass: "review"},
			Status: v1alpha1.TaskStatus{Phase: phase, Reason: "merged", Profile: p, Usage: v1alpha1.Usage{CostMicroUSD: 1_500_000}}}
	}
	count := func() int {
		n := 0
		for _, s := range g.metrics.recorded {
			if strings.HasPrefix(s, "task_outcome ") {
				n++
			}
		}
		return n
	}
	tk := ended(v1alpha1.PhaseDone, &v1alpha1.ProfileStatus{Name: "review-search", Source: "scoper", Cohort: "treatment"})
	g.r.outcome(t.Context(), tk)
	g.r.outcome(t.Context(), tk)
	if count() != 1 || !tk.Status.Profile.Counted || !slices.Contains(g.metrics.recorded, "task_outcome review treatment review-search scoper accepted 1.5") {
		t.Fatalf("one count: %v %v", tk.Status.Profile.Counted, g.metrics.recorded)
	}
	g.r.outcome(t.Context(), ended(v1alpha1.PhaseDone, nil))
	esc := &v1alpha1.ProfileStatus{Name: "review-search", Cohort: "treatment"}
	g.r.outcome(t.Context(), ended(v1alpha1.PhaseEscalated, esc))
	if count() != 1 || esc.Counted {
		t.Fatalf("no profile, or an escalation, counts nothing: %v", g.metrics.recorded)
	}
}
