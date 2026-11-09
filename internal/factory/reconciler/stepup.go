// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/narrate"
)

// TriggerEscalation is the trigger of the run a step queues.
const TriggerEscalation = "escalation"

// group is the escalation group of an escalated reason; "" goes to the human.
func group(reason string) string {
	switch reason {
	case "budget-run", "stalled":
		return "out_of_budget"
	case "not_met", "review_rounds_exhausted":
		return "not_converged"
	}
	return ""
}

// stepUp runs t once more under a richer profile instead of escalating it for reason.
func (r *Reconciler) stepUp(ctx context.Context, t *v1alpha1.Task, reason string) (bool, error) {
	p := t.Status.Profile
	if p == nil || !p.Resolved || len(t.Status.Runs) == 0 {
		return false, nil
	}
	if d, ok := decided(t); ok && d.Source == "escalation" && p.Source != "escalation" {
		// The step's spec write landed and its status write did not: the persisted step binds, not today's config.
		r.stepped(t, d)
		return true, nil
	}
	// A run's own end steps only for the implementer; review_rounds_exhausted is the reviewer's verdict on the implementer's work.
	if want := map[bool]string{true: "reviewer", false: "implementer"}[reason == "review_rounds_exhausted"]; current(t).Role != want {
		return false, nil
	}
	g := group(reason)
	if g == "" {
		return false, nil
	}
	cl := r.repo(t).Classes[t.Spec.PredictedClass]
	if cl.Escalation == nil {
		return false, nil
	}
	target := map[string]string{"out_of_budget": cl.Escalation.OutOfBudget, "not_converged": cl.Escalation.NotConverged}[g]
	if target == "" || p.Source == "escalation" || p.Cohort == "control" {
		return false, nil
	}
	v, ok := r.Cfg.ProfileFor(cl, target)
	if !ok {
		return false, nil
	}
	trial := t.DeepCopy()
	r.profileSpec(trial, trial.Spec.PredictedClass, v)
	if !runFits(trial) {
		return false, nil
	}
	// TestFirst stays: the tester branches from main, which an implementer run has already left.
	d := profileDecision{Name: target, Source: "escalation", Reason: g, Checks: p.Checks || v.Checks, TestFirst: p.TestFirst,
		Recommended: p.Recommended, Why: p.Why, Offered: len(r.profileChoices(t)) > 0}
	// The spec is written before any status change, so a refused write replays the whole step.
	if err := r.applyProfile(ctx, t, d); err != nil {
		return false, err
	}
	r.stepped(t, d)
	return true, nil
}

// stepped is the status half of a step whose spec is written: the profile, a fresh review loop and the queued run.
func (r *Reconciler) stepped(t *v1alpha1.Task, d profileDecision) {
	p := t.Status.Profile
	from := p.Name
	d.restore(p)
	t.Status.ReviewRounds = 0
	t.Status.NextTrigger = TriggerEscalation
	r.to(t, v1alpha1.PhaseQueued, "")
	narrateLater(t, narrate.ProfileEscalated(from, d.Name, d.Reason))
}

// lastJudge is the completion judge's newest verdict across t's newest implementer run and the runs
// it resumed; "" when there is none, or when the room's log could not be read to its end.
func (r *Reconciler) lastJudge(ctx context.Context, t *v1alpha1.Task) (string, error) {
	i := len(t.Status.Runs) - 1
	for i >= 0 && t.Status.Runs[i].Role != "implementer" {
		i--
	}
	if i < 0 {
		return "", nil
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
		return "", err
	}
	for j := len(evs) - 1; j >= 0; j-- {
		var p envelope.MessagePayload
		if json.Unmarshal(evs[j].Payload, &p) == nil && p.Kind == envelope.KindJudgeVerdict {
			return p.Text, nil
		}
	}
	return "", nil
}
