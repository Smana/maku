// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
)

// maxStages is the CRD's bound on status.stages.
const maxStages = 16

// stageOf is the measured workflow stage for one run record (WR5).
func stageOf(run v1alpha1.RunRecord) string {
	if run.Trigger == TriggerEscalation {
		return "step_up"
	}
	if run.Role == "triager" && run.Persona == "scoper" {
		return "plan"
	}
	switch run.Role {
	case "tester":
		return "tests"
	case "implementer":
		return "implement"
	case "reviewer":
		if run.Persona != "" && run.Persona != "reviewer" {
			return run.Persona
		}
		return "review"
	}
	return ""
}

// stageOfTaskRun resolves a run's stage using the task's context when Persona is absent on older
// records, and skips overlap evaluation runs.
func stageOfTaskRun(t *v1alpha1.Task, run v1alpha1.RunRecord) string {
	if evaluationRun(t, run.ID) {
		return ""
	}
	if s := stageOf(run); s != "" {
		return s
	}
	if run.Role == "triager" && personaOf(t, run) == "scoper" {
		return "plan"
	}
	return ""
}

// noteStage sets or replaces the effect of stage on t.Status.Stages.
func (r *Reconciler) noteStage(t *v1alpha1.Task, stage, effect string) {
	if stage == "" {
		return
	}
	for i := range t.Status.Stages {
		if t.Status.Stages[i].Stage == stage {
			if effect != "" {
				t.Status.Stages[i].Effect = effect
			}
			return
		}
	}
	if len(t.Status.Stages) >= maxStages {
		return
	}
	t.Status.Stages = append(t.Status.Stages, v1alpha1.StageRecord{Stage: stage, Effect: effect})
}

// syncStageCosts updates runs and microUSD on t.Status.Stages from t.Status.Runs.
func syncStageCosts(t *v1alpha1.Task) {
	if len(t.Status.Runs) == 0 {
		return
	}
	type agg struct {
		runs  int32
		micro int64
	}
	var order []string
	by := map[string]*agg{}
	for _, rec := range t.Status.Runs {
		s := stageOfTaskRun(t, rec)
		if s == "" {
			continue
		}
		a, ok := by[s]
		if !ok {
			a = &agg{}
			by[s] = a
			order = append(order, s)
		}
		a.runs++
		a.micro += rec.CostMicroUSD
	}
	for _, s := range order {
		a := by[s]
		idx := slices.IndexFunc(t.Status.Stages, func(sr v1alpha1.StageRecord) bool { return sr.Stage == s })
		if idx >= 0 {
			t.Status.Stages[idx].Runs = a.runs
			t.Status.Stages[idx].MicroUSD = a.micro
			continue
		}
		if len(t.Status.Stages) >= maxStages {
			continue
		}
		t.Status.Stages = append(t.Status.Stages, v1alpha1.StageRecord{Stage: s, Runs: a.runs, MicroUSD: a.micro})
	}
}

// reviewEffect classifies what the reviewer stage changed over the life of a task (WR5).
func reviewEffect(verdicts []string, commitAfterChanges, maintainerCommit, merged bool) string {
	hasChanges, hasApprove := false, false
	for _, v := range verdicts {
		switch v {
		case "changes":
			hasChanges = true
		case "approve":
			hasApprove = true
		}
	}
	if hasChanges {
		if commitAfterChanges {
			return "changes_committed"
		}
		return "changes_ignored"
	}
	if hasApprove {
		if merged && !maintainerCommit {
			return "approve_agreed"
		}
		return "approve_overruled"
	}
	return "no_verdict"
}

// judgeEffect pairs the completion judge's last verdict with whether the task merged (WR5).
func judgeEffect(verdict string, merged bool) string {
	switch verdict {
	case "met", "not_yet", "impossible":
	default:
		return ""
	}
	if merged {
		return verdict + "_merged"
	}
	return verdict + "_not_merged"
}

// planEffect classifies what the scoper/plan stage changed (WR5).
func planEffect(before, after *v1alpha1.TaskScope, asked, handoff bool) string {
	if !handoff {
		return "failed_open"
	}
	if asked {
		if before != nil && after != nil && !equality.Semantic.DeepEqual(before, after) {
			return "answer_changed_scope"
		}
		return "asked"
	}
	return "ready"
}

// testsEffect classifies what the tester stage changed (WR5).
func testsEffect(endCategory string, disputed, mergedGreen bool) string {
	if disputed {
		return "contract_disputed"
	}
	switch endCategory {
	case "not_testable":
		return "not_testable"
	case "contract_invalid", "contract_too_large", "contract_not_red", "contract_outside_tests":
		return "contract_refused"
	}
	if mergedGreen {
		return "contract_held"
	}
	return ""
}

// stepUpEffect classifies a workflow step-up or offer against its task outcome (WR5).
func stepUpEffect(mode, outcome string) string {
	switch mode {
	case "offered":
		if outcome == "taken" {
			return "offered_taken"
		}
		return "offered_left"
	case "auto", "manual":
		switch outcome {
		case "accepted", "merged_human", "done":
			return "finished"
		case "":
			return ""
		default:
			return "escalated"
		}
	}
	return ""
}

func stageEffectOf(t *v1alpha1.Task, stage string) string {
	for _, st := range t.Status.Stages {
		if st.Stage == stage {
			return st.Effect
		}
	}
	return ""
}

func scopeForEffect(sc *envelope.Scope) *v1alpha1.TaskScope {
	if sc == nil {
		return nil
	}
	return &v1alpha1.TaskScope{
		State:       "ready",
		Task:        sc.Task,
		Touches:     slices.Clone(sc.Touches),
		Leave:       slices.Clone(sc.Leave),
		Plan:        slices.Clone(sc.Plan),
		Assumptions: slices.Clone(sc.Assumptions),
	}
}

// noteReviewStage recomputes the reviewer stage's effect from the task's recorded runs and PR state.
func (r *Reconciler) noteReviewStage(t *v1alpha1.Task, merged bool) {
	var verdicts []string
	hasReviewer := false
	commitAfterChanges := false
	lastApproveIdx := -1
	lastApproveHead := ""
	for i, rec := range t.Status.Runs {
		if !isBuiltInReviewer(rec) {
			continue
		}
		hasReviewer = true
		if rec.Verdict != "" {
			verdicts = append(verdicts, rec.Verdict)
		}
		if rec.Verdict == "approve" {
			lastApproveIdx = i
			lastApproveHead = rec.HeadSHA
		}
		if rec.Verdict == "changes" && !commitAfterChanges {
			for j := i + 1; j < len(t.Status.Runs); j++ {
				later := t.Status.Runs[j]
				if isBuiltInReviewer(later) && later.HeadSHA != "" && rec.HeadSHA != "" && later.HeadSHA != rec.HeadSHA {
					commitAfterChanges = true
					break
				}
			}
			if !commitAfterChanges && t.Status.PullRequest != nil && t.Status.PullRequest.HeadSHA != "" &&
				rec.HeadSHA != "" && t.Status.PullRequest.HeadSHA != rec.HeadSHA {
				commitAfterChanges = true
			}
		}
	}
	if !hasReviewer {
		return
	}
	maintainerCommit := t.Status.PullRequest != nil && t.Status.PullRequest.HumanCommits
	if !maintainerCommit && lastApproveIdx >= 0 {
		for j := lastApproveIdx + 1; j < len(t.Status.Runs); j++ {
			if t.Status.Runs[j].Trigger == "human" {
				maintainerCommit = true
				break
			}
		}
		if !maintainerCommit && t.Status.PullRequest != nil && lastApproveHead != "" &&
			t.Status.PullRequest.HeadSHA != "" && t.Status.PullRequest.HeadSHA != lastApproveHead {
			maintainerCommit = true
		}
	}
	r.noteStage(t, "review", reviewEffect(verdicts, commitAfterChanges, maintainerCommit, merged))
}

// finalizeStages updates end-of-task stage costs and effects on t.Status.Stages.
func (r *Reconciler) finalizeStages(ctx context.Context, _ *config.Config, t *v1alpha1.Task, outcome string) {
	syncStageCosts(t)
	merged := outcome == "accepted" || outcome == "merged_human" ||
		(t.Status.Phase == v1alpha1.PhaseDone && (t.Status.Reason == "merged" || (t.Status.PullRequest != nil && t.Status.PullRequest.MergedAt != nil)))
	r.noteReviewStage(t, merged)

	if prev := stageEffectOf(t, "judge"); prev != "" {
		verdict := strings.TrimSuffix(strings.TrimSuffix(prev, "_not_merged"), "_merged")
		if eff := judgeEffect(verdict, merged); eff != "" {
			r.noteStage(t, "judge", eff)
		}
	} else if t.Spec.Loop != nil && t.Spec.Loop.JudgeModel != "" && t.Status.RoomRef != "" && r.Rooms != nil {
		if v, err := r.lastJudgeVerdict(ctx, t); err == nil && v != "" {
			if eff := judgeEffect(v, merged); eff != "" {
				r.noteStage(t, "judge", eff)
			}
		}
	}

	if t.Status.Contract != nil && stageEffectOf(t, "tests") == "" && merged {
		r.noteStage(t, "tests", testsEffect("ready", false, true))
	}

	if p := t.Status.Profile; p != nil && p.StepUp != nil {
		if eff := stepUpEffect(p.StepUp.Mode, outcome); eff != "" {
			r.noteStage(t, "step_up", eff)
		}
	}
}

// countStages records each uncounted stage's cost and effect into fmetrics once at task end (WR5).
func (r *Reconciler) countStages(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, outcome string) {
	if outcome == "" {
		outcome = taskOutcome(t)
	}
	r.finalizeStages(ctx, cfg, t, outcome)
	class, repo := classOf(t), t.Spec.Repository
	for i := range t.Status.Stages {
		st := &t.Status.Stages[i]
		if st.Counted {
			continue
		}
		st.Counted = true
		stage, effect, usd := st.Stage, st.Effect, float64(st.MicroUSD)/1e6
		record(ctx, func(ctx context.Context) {
			r.Metrics.StageCost(ctx, repo, class, stage, usd)
			if effect != "" {
				r.Metrics.StageEffect(ctx, repo, class, stage, effect, outcome)
			}
		})
	}
}
