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
	// A run's own end steps only for the implementer; review_rounds_exhausted judges the implementer's work after a verifier's run.
	if reason != "review_rounds_exhausted" && current(t).Role != "implementer" {
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
	// The spec is written before any status change, so a refused write replays the whole step.
	if err := r.applyProfile(ctx, t, target); err != nil {
		return false, err
	}
	from := p.Name
	p.Name, p.Source, p.Reason, p.Checks = target, "escalation", g, p.Checks || v.Checks
	// TestFirst stays: the tester branches from main, which an implementer run has already left.
	t.Status.ReviewRounds = 0
	t.Status.NextTrigger = TriggerEscalation
	r.to(t, v1alpha1.PhaseQueued, "")
	narrateLater(t, narrate.ProfileEscalated(from, target, g))
	return true, nil
}

// lastJudge is the completion judge's newest verdict across t's last implementer run and the runs
// it resumed; "" when there is none, or when the room's log could not be read to its end.
func (r *Reconciler) lastJudge(ctx context.Context, t *v1alpha1.Task) (string, error) {
	if len(t.Status.Runs) == 0 {
		return "", nil
	}
	i := len(t.Status.Runs) - 1
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
