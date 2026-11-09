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

// group is the escalation group an escalated reason falls in (TE7); "" goes to the human.
func group(reason string) string {
	switch reason {
	case "budget-run", "stalled":
		return "out_of_budget"
	case "not_met", "review_rounds_exhausted":
		return "not_converged"
	}
	return ""
}

// stepUp tries t's one escalation step (TE7) instead of escalating for reason. Status changes only
// once the stepped spec is written, so a refused write replays the whole step.
func (r *Reconciler) stepUp(ctx context.Context, t *v1alpha1.Task, reason string) (bool, error) {
	p := t.Status.Profile
	if p == nil || !p.Resolved {
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

// lastJudge is the completion judge's last verdict text on t's last run, as that run recorded it.
func (r *Reconciler) lastJudge(ctx context.Context, t *v1alpha1.Task) (string, error) {
	if len(t.Status.Runs) == 0 {
		return "", nil
	}
	run := t.Status.Runs[len(t.Status.Runs)-1].ID
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, briefSince(t), func(e envelope.Event) bool {
		return e.RunID == run && e.Origin == envelope.OriginClient && e.Actor.Kind == envelope.ActorAgent &&
			e.Actor.ID == "agent:"+run && e.Type == envelope.Message
	})
	if err != nil {
		return "", err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		var p envelope.MessagePayload
		if json.Unmarshal(evs[i].Payload, &p) == nil && p.Kind == envelope.KindJudgeVerdict {
			return p.Text, nil
		}
	}
	return "", nil
}
