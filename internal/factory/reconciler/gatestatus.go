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
// A failed post is logged and returned: the arm's caller aborts the pass on it, so nothing merges
// that a required maku/merge-gate would not show (D11); every other caller goes on.
func (r *Reconciler) postGateStatus(ctx context.Context, t *v1alpha1.Task, head string, checks forge.Checks, d ArmDecision) error {
	state, description := statusFor(d, t.Spec.PredictedClass)
	if have, _ := checks.StatusOf(GateStatusContext); strings.EqualFold(have, state) && checks.DescriptionOf(GateStatusContext) == description {
		return nil
	}
	if err := r.merger(ctx).SetStatus(ctx, head, state, GateStatusContext, description); err != nil {
		r.log().Warn("could not post the gate's status", "task.id", t.Name, "head", head, "state", state, "error", err)
		return fmt.Errorf("gate status: %w", err)
	}
	return nil
}

// withdrawGateStatus replaces a success this pass posted on head with d's state, once the factory
// did not merge it after all: no path leaves success on a head the factory did not merge, which a
// ruleset requiring maku/merge-gate would read as a standing permit. It posts whatever the head
// carried before the pass, since the success came after that read. A failed post is logged.
func (r *Reconciler) withdrawGateStatus(ctx context.Context, t *v1alpha1.Task, head string, d ArmDecision) {
	_ = r.postGateStatus(ctx, t, head, forge.Checks{}, d)
}

// postStatusAs posts d as maku/merge-gate on head under class's name, unconditionally: the
// merger's own merges that the gate does not decide, such as a revert pull request's.
func (r *Reconciler) postStatusAs(ctx context.Context, t *v1alpha1.Task, head, class string, d ArmDecision) error {
	state, description := statusFor(d, class)
	if err := r.merger(ctx).SetStatus(ctx, head, state, GateStatusContext, description); err != nil {
		r.log().Warn("could not post the gate's status", "task.id", t.Name, "head", head, "state", state, "error", err)
		return fmt.Errorf("gate status: %w", err)
	}
	return nil
}
