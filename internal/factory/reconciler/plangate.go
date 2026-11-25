// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
)

// Plan gate constants (phase F3).
const (
	ReasonPlanReview        = "plan_review"
	ReasonPlanRevisionLimit = "plan_revision_limit"
	LabelPlanProposed       = "factory/plan-proposed"

	PlanProposed  = "proposed"
	PlanApproved  = "approved"
	PlanRevised   = "revised"
	PlanSplit     = "split"
	PlanEscalated = "escalated"

	cmdPlanApprove = "/factory plan approve"
	cmdPlanRevise  = "/factory plan revise"
	cmdPlanSplit   = "/factory plan split"

	maxPlanRevisions = 1
)

// PlanDecisionAnnotation is the JSON shape stored on AnnotationPlanDecision by POST /v1/tasks/{task}/plans.
type PlanDecisionAnnotation struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
	By       string `json:"by"`
	Round    int32  `json:"round"`
}

func planGateApplies(t *v1alpha1.Task) bool {
	if t.Spec.Outcome != config.OutcomePlanFirst {
		return false
	}
	return t.Status.Plan == nil || t.Status.Plan.State != PlanApproved
}

func (r *Reconciler) autoApprovePlanGate(ctx context.Context, cfg *config.Config, t *v1alpha1.Task,
	sc *envelope.Scope, sp *envelope.Split, seq int64) error {
	if t.Status.Plan == nil {
		t.Status.Plan = &v1alpha1.PlanGateStatus{}
	}
	t.Status.Plan.State = PlanApproved
	t.Status.Plan.Parked = false
	t.Status.Plan.RevisionNote = ""
	if err := narrate.Room(ctx, r.Rooms, t, fmt.Sprintf("plan-gate-auto-%d", len(t.Status.Runs)),
		"Agent factory: `workflow.planGate` is `auto`; the plan is approved automatically and implementation starts on the scoped plan.",
		func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) }); err != nil {
		r.log().Warn("planGate auto note not posted", "task.id", t.Name, "err", err)
	}
	if sp != nil {
		return r.splitHandoff(ctx, cfg, t, sc, sp, seq)
	}
	return r.scopeEnd(ctx, cfg, t, sc, "ready", seq)
}

func (r *Reconciler) enterPlanGate(ctx context.Context, cfg *config.Config, t *v1alpha1.Task,
	sc *envelope.Scope, sp *envelope.Split, seq int64) error {
	if err := r.resolveProfile(ctx, cfg, t, sc); err != nil {
		return err
	}
	if sc != nil && len(sc.Touches) > 0 {
		if err := r.applyLatePlanRequires(ctx, cfg, t, sc.Touches); err != nil {
			return err
		}
	}
	t.Status.Scope = &v1alpha1.ScopeStatus{State: "ready", Seq: seq}
	r.noteStage(t, "plan", planEffect(nil, scopeForEffect(sc), false, sc != nil))
	var validSplit *envelope.Split
	if sp != nil && t.Spec.Split != nil && len(sp.Children) <= int(t.Spec.Split.MaxChildren) && sp.Validate() == nil {
		validSplit = sp
		t.Status.Split = &v1alpha1.SplitStatus{State: SplitProposed, Seq: seq, Of: int32(len(sp.Children))} //nolint:gosec // Validate bounds it to 2..5
	}
	var revisions int32
	if t.Status.Plan != nil {
		revisions = t.Status.Plan.Revisions
	}
	t.Status.Plan = &v1alpha1.PlanGateStatus{
		State:     PlanProposed,
		Revisions: revisions,
	}
	class, n, name := t.Spec.PredictedClass, t.Spec.Issue, t.Name
	record(ctx, func(ctx context.Context) { r.Metrics.ScoperRun(ctx, class, "ready") })
	if n > 0 {
		record(ctx, func(ctx context.Context) {
			if err := r.gh(ctx).AddLabels(ctx, n, LabelPlanProposed); err != nil {
				r.log().Warn("label not added", "task.id", name, "label", LabelPlanProposed, "err", err)
			}
		})
	}
	r.releaseUnheld(ctx, t)
	r.to(t, v1alpha1.PhaseAwaitingHuman, ReasonPlanReview)
	if n > 0 {
		narrateLater(t, narrate.PlanProposed(t, sc, validSplit, cfg.RoomsURL))
	}
	return nil
}

func (r *Reconciler) leavePlanGate(ctx context.Context, t *v1alpha1.Task, state string) {
	if t.Status.Plan == nil {
		t.Status.Plan = &v1alpha1.PlanGateStatus{}
	}
	t.Status.Plan.State = state
	t.Status.Plan.Parked = false
	n, name := t.Spec.Issue, t.Name
	if n > 0 {
		record(ctx, func(ctx context.Context) {
			if err := r.gh(ctx).RemoveLabel(ctx, n, LabelPlanProposed); err != nil {
				r.log().Warn("label not removed", "task.id", name, "label", LabelPlanProposed, "err", err)
			}
		})
	}
}

type planAnswer struct {
	id       int64
	decision string // approve | revise | split
	note     string
	author   string
}

func (r *Reconciler) awaitingPlanReview(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	ans, ok, err := r.readPlanAnswer(ctx, cfg, t)
	if err != nil {
		return err
	}
	if ok {
		switch ans.decision {
		case "approve":
			return r.applyPlanApprove(ctx, cfg, t, ans)
		case "revise":
			return r.applyPlanRevise(ctx, cfg, t, ans)
		case "split":
			return r.applyPlanSplit(ctx, cfg, t, ans)
		}
	}
	return r.parkPlanGateIfDue(ctx, cfg, t)
}

func (r *Reconciler) applyPlanApprove(ctx context.Context, _ *config.Config, t *v1alpha1.Task, ans planAnswer) error {
	markHandled(t, ans.id)
	r.leavePlanGate(ctx, t, PlanApproved)
	t.Status.Plan.RevisionNote = ""
	if t.Spec.Outcome != config.OutcomeChange {
		status := t.Status
		t.Spec.Outcome = config.OutcomeChange
		if err := r.Client.Update(ctx, t); err != nil {
			return err
		}
		t.Status = status
	}
	if t.Status.Split != nil && t.Status.Split.State == SplitProposed {
		t.Status.Split.State = SplitDeclined
	}
	t.Status.NextTrigger = "initial"
	r.to(t, v1alpha1.PhaseQueued, "")
	return nil
}

func (r *Reconciler) applyPlanRevise(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, ans planAnswer) error {
	markHandled(t, ans.id)
	r.leavePlanGate(ctx, t, PlanRevised)
	if t.Status.Plan.Revisions >= maxPlanRevisions {
		t.Status.Plan.State = PlanEscalated
		t.Status.Plan.RevisionNote = ""
		r.to(t, v1alpha1.PhaseEscalated, ReasonPlanRevisionLimit)
		if t.Spec.Issue > 0 {
			narrateLater(t, narrate.PlanRevisionLimit(t, ans.author, r.repo(cfg, t).Maintainers, cfg.RoomsURL))
		}
		return nil
	}
	t.Status.Plan.Revisions++
	t.Status.Plan.RevisionNote = ans.note
	t.Status.Scope = nil
	t.Status.Split = nil
	t.Status.NextTrigger = "plan_revise"
	r.to(t, v1alpha1.PhaseQueued, "")
	return nil
}

func (r *Reconciler) applyPlanSplit(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, ans planAnswer) error {
	if t.Status.Split == nil || t.Status.Split.State != SplitProposed {
		markHandled(t, ans.id)
		r.leavePlanGate(ctx, t, PlanEscalated)
		r.to(t, v1alpha1.PhaseEscalated, ReasonSplitProposed)
		return nil
	}
	person := v1alpha1.Person(ans.author)
	spent, err := r.daySpent(ctx, cfg, t, person)
	if err != nil {
		return err
	}
	markHandled(t, ans.id)
	if spent {
		narrateLater(t, narrate.ApproverOverBudget(t.Spec.Issue, person, cfg.Budgets.DailyMicroUSD(person), r.Now()))
		return nil
	}
	r.leavePlanGate(ctx, t, PlanSplit)
	t.Status.Split.State = SplitApproved
	t.Status.Split.ApprovedBy = person
	r.noteStage(t, "split", "proposed_accepted")
	class := t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) { r.Metrics.Split(ctx, class, SplitApproved) })
	r.intervention(ctx, cfg, t, "split")
	r.to(t, v1alpha1.PhaseSplit, "")
	return nil
}

func (r *Reconciler) parkPlanGateIfDue(_ context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if t.Status.PhaseSince == nil {
		return nil
	}
	rc := r.repo(cfg, t)
	wait := rc.PlanGateWait()
	if r.Now().Sub(t.Status.PhaseSince.Time) < wait {
		return nil
	}
	if t.Status.Plan == nil {
		t.Status.Plan = &v1alpha1.PlanGateStatus{State: PlanProposed}
	}
	if t.Status.Plan.Parked {
		return nil
	}
	t.Status.Plan.Parked = true
	hours := rc.PlanGateHoursOr()
	if t.Spec.Issue > 0 {
		narrateLater(t, narrate.PlanParked(t, hours, rc.Maintainers, cfg.RoomsURL))
	}
	return nil
}

func (r *Reconciler) readPlanAnswer(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (planAnswer, bool, error) {
	if ans, ok, err := r.planDecisionAnnotation(ctx, cfg, t); err != nil || ok {
		return ans, ok, err
	}
	if t.Spec.Issue <= 0 {
		return planAnswer{}, false, nil
	}
	cs, err := r.gh(ctx).RecentComments(ctx, t.Spec.Issue)
	if err != nil {
		return planAnswer{}, false, err
	}
	var best forge.Comment
	var bestDec, bestNote string
	found := false
	for _, c := range cs {
		if c.Edited || slices.Contains(t.Status.Handled, c.ID) ||
			(t.Status.PhaseSince != nil && !c.At.After(t.Status.PhaseSince.Add(-retrySkew))) {
			continue
		}
		dec, note, ok := parsePlanCommand(c.Body)
		if !ok {
			continue
		}
		allowed, err := r.canAnswerPlan(ctx, cfg, t, c.Author)
		if err != nil {
			return planAnswer{}, false, err
		}
		if !allowed {
			continue
		}
		if !found || c.At.After(best.At) {
			best, bestDec, bestNote, found = c, dec, note, true
		}
	}
	if !found {
		return planAnswer{}, false, nil
	}
	return planAnswer{id: best.ID, decision: bestDec, note: bestNote, author: best.Author}, true, nil
}

func (r *Reconciler) planDecisionAnnotation(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (planAnswer, bool, error) {
	raw := t.Annotations[v1alpha1.AnnotationPlanDecision]
	if raw == "" {
		return planAnswer{}, false, nil
	}
	var dec PlanDecisionAnnotation
	if err := json.Unmarshal([]byte(raw), &dec); err != nil {
		return planAnswer{}, false, nil
	}
	var revisions int32
	if t.Status.Plan != nil {
		revisions = t.Status.Plan.Revisions
	}
	if dec.Round != revisions || dec.By == "" {
		return planAnswer{}, false, nil
	}
	switch dec.Decision {
	case "approve", "split":
	case "revise":
		if strings.TrimSpace(dec.Note) == "" {
			return planAnswer{}, false, nil
		}
	default:
		return planAnswer{}, false, nil
	}
	id := retryAnnotationID("plan:" + raw)
	if slices.Contains(t.Status.Handled, id) {
		return planAnswer{}, false, nil
	}
	allowed, err := r.canAnswerPlan(ctx, cfg, t, dec.By)
	if err != nil || !allowed {
		return planAnswer{}, false, err
	}
	return planAnswer{id: id, decision: dec.Decision, note: strings.TrimSpace(dec.Note), author: dec.By}, true, nil
}

func (r *Reconciler) canAnswerPlan(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, login string) (bool, error) {
	if login == "" {
		return false, nil
	}
	if t.Spec.IssueAuthor != "" && forge.SameLogin(t.Spec.IssueAuthor, login) {
		return true, nil
	}
	if !r.repo(cfg, t).IsMaintainer(login) {
		return false, nil
	}
	if r.CanWrite == nil {
		return false, nil
	}
	w, err := r.CanWrite(ctx, t.Spec.Repository, login)
	if err != nil {
		return false, fmt.Errorf("write check of %s: %w", login, err)
	}
	return w, nil
}

func parsePlanCommand(body string) (decision, note string, ok bool) {
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		trimmed := strings.TrimRight(line, " \t\r")
		switch {
		case trimmed == cmdPlanApprove:
			return "approve", "", true
		case trimmed == cmdPlanSplit:
			return "split", "", true
		case strings.HasPrefix(trimmed, cmdPlanRevise+" "):
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, cmdPlanRevise)); rest != "" {
				return "revise", rest, true
			}
		}
	}
	return "", "", false
}
