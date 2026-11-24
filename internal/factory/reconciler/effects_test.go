// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
)

func TestAStageEffectCountsOnce(t *testing.T) {
	g, tk := stepRig(t, ladderCfg(), nil)
	g.r.noteStage(tk, "review", "changes_committed")
	g.r.countStages(t.Context(), g.r.Cfg.Current(), tk, "accepted")
	g.r.countStages(t.Context(), g.r.Cfg.Current(), tk, "accepted") // a replayed end

	want := "stage_effect Smana/cloud-native-ref review review changes_committed accepted"
	n := 0
	for _, s := range g.metrics.stages {
		if s == want {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("counted %d times; stages %v", n, g.metrics.stages)
	}
}

func TestStageCostSumsTheStagesRuns(t *testing.T) {
	tk := &v1alpha1.Task{
		Status: v1alpha1.TaskStatus{
			Runs: []v1alpha1.RunRecord{
				{ID: rid(0), Role: "implementer", Trigger: "initial", CostMicroUSD: 400_000},
				{ID: rid(1), Role: "implementer", Trigger: "review", CostMicroUSD: 100_000},
				{ID: rid(2), Role: "reviewer", Trigger: "review", CostMicroUSD: 90_000},
			},
		},
	}
	syncStageCosts(tk)

	stage := func(name string) (v1alpha1.StageRecord, bool) {
		i := slices.IndexFunc(tk.Status.Stages, func(sr v1alpha1.StageRecord) bool { return sr.Stage == name })
		if i < 0 {
			return v1alpha1.StageRecord{}, false
		}
		return tk.Status.Stages[i], true
	}
	impl, ok := stage("implement")
	if !ok || impl.Runs != 2 || impl.MicroUSD != 500_000 {
		t.Fatalf("implement stage = %+v (ok=%v), want runs=2 microUSD=500000", impl, ok)
	}
	rev, ok := stage("review")
	if !ok || rev.Runs != 1 || rev.MicroUSD != 90_000 {
		t.Fatalf("review stage = %+v (ok=%v), want runs=1 microUSD=90000", rev, ok)
	}
}

func TestStageOfReadsThePersona(t *testing.T) {
	if got := stageOf(v1alpha1.RunRecord{Role: "triager", Persona: "scoper"}); got != "plan" {
		t.Fatalf("scoper stage = %q, want plan", got)
	}
	if got := stageOf(v1alpha1.RunRecord{Role: "tester"}); got != "tests" {
		t.Fatalf("tester stage = %q, want tests", got)
	}
	if got := stageOf(v1alpha1.RunRecord{Role: "implementer"}); got != "implement" {
		t.Fatalf("implementer stage = %q, want implement", got)
	}
	if got := stageOf(v1alpha1.RunRecord{Role: "reviewer"}); got != "review" {
		t.Fatalf("reviewer stage = %q, want review", got)
	}
	if got := stageOf(v1alpha1.RunRecord{Role: "implementer", Trigger: TriggerEscalation}); got != "step_up" {
		t.Fatalf("escalation stage = %q, want step_up", got)
	}
}

func TestTheReviewersEffect(t *testing.T) {
	for name, tc := range map[string]struct {
		verdicts                                     []string
		commitAfterChanges, maintainerCommit, merged bool
		want                                         string
	}{
		"changes led to a commit": {[]string{"changes", "approve"}, true, false, true, "changes_committed"},
		"changes ignored":         {[]string{"changes", "changes"}, false, false, false, "changes_ignored"},
		"approve agreed":          {[]string{"approve"}, false, false, true, "approve_agreed"},
		"approve overruled":       {[]string{"approve"}, false, true, true, "approve_overruled"},
		"approved, then closed":   {[]string{"approve"}, false, false, false, "approve_overruled"},
		"no verdict":              {[]string{"none"}, false, false, false, "no_verdict"},
	} {
		t.Run(name, func(t *testing.T) {
			got := reviewEffect(tc.verdicts, tc.commitAfterChanges, tc.maintainerCommit, tc.merged)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheJudgesEffectIsItsVerdictAgainstTheEnd(t *testing.T) {
	for _, tc := range []struct {
		verdict string
		merged  bool
		want    string
	}{
		{"met", true, "met_merged"},
		{"met", false, "met_not_merged"},
		{"not_yet", true, "not_yet_merged"},
		{"impossible", false, "impossible_not_merged"},
		{"", true, ""},
	} {
		if got := judgeEffect(tc.verdict, tc.merged); got != tc.want {
			t.Errorf("judgeEffect(%q, %v) = %q, want %q", tc.verdict, tc.merged, got, tc.want)
		}
	}
}

func TestAMergedPairTaskRecordsTheReviewStage(t *testing.T) {
	g, tk := stepRig(t, ladderCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Profile.Name = "standard"
		tk.Status.Runs[0].StartSeq = 0
		tk.Status.PullRequest = &v1alpha1.PullRequestRef{
			Number:  12,
			URL:     "https://github.com/Smana/cloud-native-ref/pull/12",
			HeadSHA: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
		}
	})
	head1 := "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	head2 := "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"

	g.f.SetPR(forge.PR{Number: 12, URL: tk.Status.PullRequest.URL, State: "OPEN", HeadRef: "agent/3buqdlot", HeadSHA: head1})
	g.runs.runs[rid(0)] = runs.Run{ID: rid(0), TaskID: "3buqdlot", Role: "implementer", Phase: "Succeeded"}
	g.log.end(rid(0), "Succeeded", "agent_finished")
	// Implementer ends -> Queued for reviewer -> Reviewing (rid(1))
	tk = g.reconcile(t, "3buqdlot", 2)
	if tk.Status.Phase != v1alpha1.PhaseReviewing || current(tk).Role != "reviewer" {
		t.Fatalf("phase=%s role=%s", tk.Status.Phase, current(tk).Role)
	}

	// Reviewer requests changes on head1
	g.runs.set(rid(1), "Succeeded")
	g.log.verdict(rid(1), "changes", head1, "fix edge case")
	g.log.end(rid(1), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2) // Reviewing -> Queued -> Implementing (rid(2))
	if tk.Status.Phase != v1alpha1.PhaseImplementing || current(tk).Role != "implementer" {
		t.Fatalf("after changes: phase=%s role=%s", tk.Status.Phase, current(tk).Role)
	}

	// Revision commit lands on head2
	g.f.SetPR(forge.PR{Number: 12, URL: tk.Status.PullRequest.URL, State: "OPEN", HeadRef: "agent/3buqdlot", HeadSHA: head2})
	g.runs.set(rid(2), "Succeeded")
	g.log.end(rid(2), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2) // Implementing -> Queued -> Reviewing (rid(3))
	if tk.Status.Phase != v1alpha1.PhaseReviewing || current(tk).Role != "reviewer" {
		t.Fatalf("second review: phase=%s role=%s", tk.Status.Phase, current(tk).Role)
	}

	// Reviewer approves head2
	g.runs.set(rid(3), "Succeeded")
	g.log.verdict(rid(3), "approve", head2, "looks good")
	g.log.end(rid(3), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 1) // Reviewing -> AwaitingCI
	if tk.Status.Phase != v1alpha1.PhaseAwaitingCI {
		t.Fatalf("after approve: phase=%s", tk.Status.Phase)
	}

	// PR merges
	g.f.SetPR(forge.PR{Number: 12, URL: tk.Status.PullRequest.URL, State: "MERGED", MergedBy: "Smana", HeadRef: "agent/3buqdlot", HeadSHA: head2, MergedAt: now})
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseDone {
		t.Fatalf("after merge: phase=%s", tk.Status.Phase)
	}

	got := stageEffectOf(tk, "review")
	if got != "changes_committed" {
		t.Fatalf("review stage effect = %q, want changes_committed; stages = %+v", got, tk.Status.Stages)
	}
}

func TestThePlanEffect(t *testing.T) {
	a := &v1alpha1.TaskScope{Task: "fix the checker"}
	b := &v1alpha1.TaskScope{Task: "fix the checker and its fixture"}
	for name, tc := range map[string]struct {
		before, after  *v1alpha1.TaskScope
		asked, handoff bool
		want           string
	}{
		"ready":        {nil, a, false, true, "ready"},
		"asked":        {nil, nil, true, true, "asked"},
		"answer moved": {a, b, true, true, "answer_changed_scope"},
		"failed open":  {nil, nil, false, false, "failed_open"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := planEffect(tc.before, tc.after, tc.asked, tc.handoff); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheTestsEffect(t *testing.T) {
	for name, tc := range map[string]struct {
		endCategory           string
		disputed, mergedGreen bool
		want                  string
	}{
		"contract held":      {"ready", false, true, "contract_held"},
		"contract disputed":  {"ready", true, false, "contract_disputed"},
		"not testable":       {"not_testable", false, false, "not_testable"},
		"contract invalid":   {"contract_invalid", false, false, "contract_refused"},
		"contract too large": {"contract_too_large", false, false, "contract_refused"},
		"contract not red":   {"contract_not_red", false, false, "contract_refused"},
		"contract outside":   {"contract_outside_tests", false, false, "contract_refused"},
		"ready but unmerged": {"ready", false, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := testsEffect(tc.endCategory, tc.disputed, tc.mergedGreen); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheStepUpEffect(t *testing.T) {
	for name, tc := range map[string]struct {
		mode, outcome string
		want          string
	}{
		"auto finished":   {"auto", "accepted", "finished"},
		"manual finished": {"manual", "merged_human", "finished"},
		"auto escalated":  {"auto", "failed", "escalated"},
		"manual stopped":  {"manual", "stopped", "escalated"},
		"offered taken":   {"offered", "taken", "offered_taken"},
		"offered left":    {"offered", "stopped", "offered_left"},
		"empty mode":      {"", "accepted", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stepUpEffect(tc.mode, tc.outcome); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAStepThatFinishesIsCountedAsRescued(t *testing.T) {
	g, tk := stepRig(t, ladderCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Profile.Name = "standard"
		tk.Status.Profile.Source = "repository"
	})
	// Standard task stalls -> auto steps up to careful.
	tk = g.fail(t, tk, "stalled")
	if tk.Status.Profile.Name != "careful" || tk.Status.Profile.StepUp == nil || tk.Status.Profile.StepUp.Mode != "auto" {
		t.Fatalf("expected auto step-up to careful, got %+v", tk.Status.Profile)
	}
	// Escalation run opens a PR and merges.
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 21, URL: "https://github.com/Smana/cloud-native-ref/pull/21", HeadSHA: "cccc3333cccc3333cccc3333cccc3333cccc3333"}
	tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{
		ID: rid(1), Role: "implementer", Trigger: TriggerEscalation, CostMicroUSD: 1_250_000,
		Started: &metav1.Time{Time: now}, Finished: &metav1.Time{Time: now.Add(time.Minute)},
	})
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = g.get(t)
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseDone, "merged"); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = g.get(t)
	if got := stageEffectOf(tk, "step_up"); got != "finished" {
		t.Fatalf("step_up effect = %q, want finished; stages = %+v", got, tk.Status.Stages)
	}
	// Settle counts the step_up stage cost and effect once.
	g.settled(t, "3buqdlot")
	wantEffect := "stage_effect Smana/cloud-native-ref review step_up finished accepted"
	wantCost := "stage_cost Smana/cloud-native-ref review step_up 1.25"
	hasEffect, hasCost := false, false
	for _, s := range g.metrics.stages {
		if s == wantEffect {
			hasEffect = true
		}
		if strings.HasPrefix(s, wantCost) {
			hasCost = true
		}
	}
	if !hasEffect || !hasCost {
		t.Fatalf("missing stage metrics: want %q and %q; got %v", wantEffect, wantCost, g.metrics.stages)
	}
}
