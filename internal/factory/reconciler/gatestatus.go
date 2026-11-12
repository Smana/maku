// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// GateStatusContext is the commit status the gate posts its own verdict as, when
// merge.gateStatus is on.
const GateStatusContext = "maku/merge-gate"

// statusDescriptionMax is GitHub's limit on a commit status's description, in characters.
const statusDescriptionMax = 140

// statusFor is the state and description the gate's decision posts as maku/merge-gate. Only arm,
// posted before the merge in the same pass, is success.
func statusFor(d ArmDecision, class string) (state, description string) {
	if class == "" {
		class = "no class"
	}
	switch d.Verdict {
	case "arm":
		state, description = "success", class+": merging"
	case "shadow":
		// Not success: with branch protection requiring this status, a success here would let any
		// writer, an agent token included, merge what the gate handed to a human (D11).
		state, description = "pending", class+", shadow: would merge; a maintainer merges"
	case "wait":
		state, description = "pending", fmt.Sprintf("%s: waiting, %s", class, d.Reason)
	case "human":
		state, description = "pending", fmt.Sprintf("%s: a human decides, %s", class, d.Reason)
	case "escalate":
		state, description = "failure", fmt.Sprintf("%s: escalated, %s", class, d.Reason)
	default: // ci_red, ci_cancelled
		state, description = "failure", fmt.Sprintf("%s: %s", class, d.Reason)
	}
	if r := []rune(description); len(r) > statusDescriptionMax {
		description = string(r[:statusDescriptionMax])
	}
	return state, description
}

// postGateStatus posts the decision as maku/merge-gate on the head, unless the head already
// carries that state and description (read from the checks this pass read: no field on the Task).
// A failed post is logged and never changes the decision.
func (r *Reconciler) postGateStatus(ctx context.Context, t *v1alpha1.Task, head string, checks forge.Checks, d ArmDecision) {
	state, description := statusFor(d, t.Spec.PredictedClass)
	if have, _ := checks.StatusOf(GateStatusContext); strings.EqualFold(have, state) && checks.DescriptionOf(GateStatusContext) == description {
		return
	}
	if err := r.merger(ctx).SetStatus(ctx, head, state, GateStatusContext, description); err != nil {
		r.log().Warn("could not post the gate's status", "task.id", t.Name, "head", head, "state", state, "error", err)
	}
}
