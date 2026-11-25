// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
)

// TriggerEscalation is the trigger of the run a step queues.
const TriggerEscalation = "escalation"

// group is the escalation group of an escalated reason; "" goes to the human.
func group(reason string) string {
	switch reason {
	case "budget-run", "stalled", "budget-task":
		return "out_of_budget"
	case "not_met", "review_rounds_exhausted":
		return "not_converged"
	case "ci_red":
		return "ci_red"
	}
	return ""
}

func stepUpRoleMatches(reason, role string) bool {
	switch reason {
	case "budget-task":
		// The task cap stops the next run before it starts, whichever role it is: the role of the
		// run before it (a reviewer's, after a rescue retry) says nothing about the step.
		return true
	case "review_rounds_exhausted":
		return role == "reviewer"
	case "ci_red":
		return role == "implementer" || role == "reviewer"
	default:
		return role == "implementer"
	}
}

func alreadyStepped(p *v1alpha1.ProfileStatus) bool {
	if p == nil {
		return false
	}
	if p.Source == "escalation" {
		return true
	}
	return p.StepUp != nil && (p.StepUp.Mode == "auto" || p.StepUp.Mode == "manual")
}

// targetCeilingMicroUSD is the task ceiling of stepping t up to workflow v, ignoring any per-task
// MaxMicroUSD cap so stepUp can compare the full target ceiling against the task's cap.
func targetCeilingMicroUSD(cfg *config.Config, t *v1alpha1.Task, v config.HarnessProfile) int64 {
	tier := t.Spec.Budget.Tier
	if tier == "" {
		tier = "standard"
	}
	if vt := v.EffectiveTier(); slices.Index(tiers, vt) > slices.Index(tiers, tier) {
		tier = vt // a list-form workflow's budget: names its tier, not tier:
	}
	if usd := cfg.Tiers[tier].TaskUSD; usd > 0 {
		return int64(usd * 1e6)
	}
	return t.Spec.Budget.TaskMicroUSD
}

// stepUp runs t once more under the workflow its own workflow's stepUp (or legacy escalation) names
// for reason, instead of escalating it (WR4). When auto step-up is held back (a run of a role the
// reason does not step, a workflow-evaluation arm, pinned workflow, capped below target ceiling,
// repository stepUp: ask, or a confirmed plan that did not show the stepped-up figure), it records
// StepUp{Mode: "offered"} and lets the task escalate with the offer and its reason. Only a
// repository's stepUp: off escalates without one.
func (r *Reconciler) stepUp(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, reason string) (bool, error) {
	p := t.Status.Profile
	if p == nil || !p.Resolved || len(t.Status.Runs) == 0 {
		return false, nil
	}
	if d, ok := decided(t); ok && d.Source == "escalation" && p.Source != "escalation" {
		// The step's spec write landed and its status write did not: the persisted step binds, not today's config.
		r.stepped(t, d)
		return true, nil
	}
	g := group(reason)
	if g == "" {
		return false, nil
	}
	target := cfg.StepUpFor(p.Name, reason)
	if target == "" || target == p.Name || alreadyStepped(p) {
		return false, nil // the same workflow again rarely converges: a maintainer decides (P10)
	}
	mode := r.repo(cfg, t).StepUpMode()
	if mode == "off" {
		return false, nil
	}
	v, ok := cfg.Workflow(target)
	if !ok {
		return false, nil
	}
	ceiling := targetCeilingMicroUSD(cfg, t, v)
	trial := t.DeepCopy()
	r.profileSpec(cfg, trial, target, v)

	offer := func(why string) (bool, error) {
		p.StepUp = &v1alpha1.StepUpStatus{
			From:            p.Name,
			To:              target,
			Trigger:         reason,
			Mode:            "offered",
			CeilingMicroUSD: ceiling,
		}
		r.noteStage(t, "step_up", stepUpEffect("offered", "left"))
		narrateLater(t, narrate.StepUpOffered(t, p.Name, target, reason, ceiling, why))
		return false, nil
	}
	// A step exists from here on: every path that does not take it offers it, saying why.
	if !stepUpRoleMatches(reason, current(t).Role) {
		return offer("role")
	}
	if p.Source == "treatment" || p.Cohort == "control" {
		return offer("measured") // either arm of the workflow evaluation keeps what triage drew (WF8)
	}
	if !runFits(trial) || (t.Spec.MaxMicroUSD > 0 && ceiling > t.Spec.MaxMicroUSD) {
		return offer("capped")
	}
	if p.Source == "label" {
		return offer("pinned")
	}
	if mode == "ask" {
		return offer("ask")
	}
	if t.Spec.Intake != nil && !t.Spec.Intake.StepUpShown {
		return offer("not_shown")
	}
	// TestFirst stays: the tester branches from main, which an implementer run has already left.
	su := &v1alpha1.StepUpStatus{
		From:            p.Name,
		To:              target,
		Trigger:         reason,
		Mode:            "auto",
		CeilingMicroUSD: ceiling,
	}
	d := profileDecision{Name: target, Source: "escalation", Reason: g, Checks: p.Checks || v.Checks, TestFirst: p.TestFirst,
		Recommended: p.Recommended, Why: p.Why, Offered: len(r.profileChoices(cfg, t)) > 0, StepUp: su}
	// The spec is written before any status change, so a refused write replays the whole step.
	if err := r.applyProfile(ctx, cfg, t, d); err != nil {
		return false, err
	}
	r.stepped(t, d)
	return true, nil
}

// stepUpBy runs t once more under the next workflow on its ladder on a maintainer's explicit
// request (`/factory retry deeper`, API `deeper: true`, or `maku retry --deeper`). It uses the
// task's one step, skips the automatic "ask" guards (pinned, ask mode, unshown plan), and still
// enforces the target ceiling and remaining budget (WR4).
func (r *Reconciler) stepUpBy(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, by string, handledID int64) error {
	p := t.Status.Profile
	if p == nil || !p.Resolved || alreadyStepped(p) {
		if handledID != 0 {
			markHandled(t, handledID)
		}
		return nil
	}
	target := cfg.StepUpFor(p.Name, "stalled")
	if target == "" && p.StepUp != nil && p.StepUp.Mode == "offered" {
		target = p.StepUp.To
	}
	if target == "" || target == p.Name {
		if handledID != 0 {
			markHandled(t, handledID)
		}
		return nil
	}
	v, ok := cfg.Workflow(target)
	if !ok {
		if handledID != 0 {
			markHandled(t, handledID)
		}
		return nil
	}
	ceiling := targetCeilingMicroUSD(cfg, t, v)
	trial := t.DeepCopy()
	r.profileSpec(cfg, trial, target, v)
	if !runFits(trial) || (t.Spec.MaxMicroUSD > 0 && ceiling > t.Spec.MaxMicroUSD) {
		if handledID != 0 {
			markHandled(t, handledID)
			narrateLater(t, narrate.TaskCapHolds(t, handledID))
		}
		return nil
	}
	wasOffered := p.StepUp != nil && p.StepUp.Mode == "offered"
	su := &v1alpha1.StepUpStatus{
		From:            p.Name,
		To:              target,
		Trigger:         "manual",
		Mode:            "manual",
		By:              by,
		CeilingMicroUSD: ceiling,
	}
	d := profileDecision{Name: target, Source: "escalation", Reason: "manual", Checks: p.Checks || v.Checks, TestFirst: p.TestFirst,
		Recommended: p.Recommended, Why: p.Why, Offered: len(r.profileChoices(cfg, t)) > 0, StepUp: su}
	if err := r.applyProfile(ctx, cfg, t, d); err != nil {
		return err
	}
	if wasOffered {
		r.noteStage(t, "step_up", stepUpEffect("offered", "taken"))
	}
	if handledID != 0 {
		markHandled(t, handledID)
	}
	r.leaveSplit(ctx, t, SplitDeclined)
	if t.Status.Reason == ReasonNeedsAuthor {
		r.leaveBounce(ctx, t, "retry")
		if t.Status.Scope != nil {
			t.Status.Scope.State = "assumed"
		}
	}
	t.Status.Retries++
	t.Status.RetriedBy = v1alpha1.Person(by)
	r.intervention(ctx, cfg, t, "retry")
	r.stepped(t, d)
	return nil
}

// stepped is the status half of a step whose spec is written: the profile, a fresh review loop, a
// fresh CI-repair allowance (R33) and the queued run.
func (r *Reconciler) stepped(t *v1alpha1.Task, d profileDecision) {
	p := t.Status.Profile
	from := p.Name
	d.restore(p)
	t.Status.ReviewRounds, t.Status.FixRunsBeforeStep = 0, t.Status.FixRuns
	t.Status.NextTrigger = TriggerEscalation
	r.to(t, v1alpha1.PhaseQueued, "")
	trigger := d.Reason
	ceiling := t.Spec.Budget.TaskMicroUSD
	if d.StepUp != nil {
		if d.StepUp.Trigger != "" {
			trigger = d.StepUp.Trigger
		}
		if d.StepUp.CeilingMicroUSD > 0 {
			ceiling = d.StepUp.CeilingMicroUSD
		}
	}
	narrateLater(t, narrate.ProfileEscalated(from, d.Name, trigger, ceiling, len(t.Status.Runs), t.Status.Usage.CostMicroUSD))
}

// lastJudge is the completion judge's newest verdict text across t's newest implementer run and
// the runs it resumed; "" when there is none, or when the room's log could not be read to its end.
func (r *Reconciler) lastJudge(ctx context.Context, t *v1alpha1.Task) (string, error) {
	p, ok, err := r.lastJudgeMessage(ctx, t)
	if err != nil || !ok {
		return "", err
	}
	return p.Text, nil
}

// lastJudgeVerdict is the completion judge's newest verdict word (met, not_yet, impossible) across
// t's newest implementer run and the runs it resumed.
func (r *Reconciler) lastJudgeVerdict(ctx context.Context, t *v1alpha1.Task) (string, error) {
	p, ok, err := r.lastJudgeMessage(ctx, t)
	if err != nil || !ok {
		return "", err
	}
	return p.Verdict, nil
}

func (r *Reconciler) lastJudgeMessage(ctx context.Context, t *v1alpha1.Task) (envelope.MessagePayload, bool, error) {
	i := len(t.Status.Runs) - 1
	for i >= 0 && t.Status.Runs[i].Role != "implementer" {
		i--
	}
	if i < 0 {
		return envelope.MessagePayload{}, false, nil
	}
	chain := map[string]bool{t.Status.Runs[i].ID: true}
	for i > 0 && t.Status.Runs[i].Trigger == "resume" {
		i--
		chain[t.Status.Runs[i].ID] = true
	}
	evs, complete, err := r.roomTail(ctx, t.Status.RoomRef, t.Status.Runs[i].StartSeq, func(e envelope.Event) bool {
		return chain[e.RunID] && e.Origin == envelope.OriginClient && e.Actor.Kind == envelope.ActorAgent &&
			e.Actor.ID == "agent:"+e.RunID && e.Type == envelope.Message
	})
	if err != nil || !complete {
		return envelope.MessagePayload{}, false, err
	}
	for j := len(evs) - 1; j >= 0; j-- {
		var p envelope.MessagePayload
		if json.Unmarshal(evs[j].Payload, &p) == nil && p.Kind == envelope.KindJudgeVerdict {
			return p, true, nil
		}
	}
	return envelope.MessagePayload{}, false, nil
}
