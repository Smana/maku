// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// The agreements of a dual evaluation (fmetrics.PolicyAgreements).
const (
	dualAgree       = "agree"
	dualExplained   = "explained"
	dualUnread      = "unread"
	dualUnexplained = "unexplained"
	dualNoPolicy    = "no_policy"
)

// dualOf decides in again under the repository policy snap (merge policy stage 3) and says how it
// compares with central, the decision the gate acts on: agree; explained when the two differ on
// a class the stage-1 comparison already lists as divergent; unread when the repository's
// decision would need the diff the central one did not read; unexplained otherwise; no_policy
// without a valid policy that has classes. Pure: the caller records it.
func dualOf(central ArmDecision, in ArmInputs, snap mergepolicy.Snapshot, ok bool) (ArmDecision, string) {
	if !ok || snap.Err != nil || !snap.HasClasses {
		return ArmDecision{}, dualNoPolicy
	}
	class := in.Task.Spec.PredictedClass
	r, inPolicy := snap.Classes[class]
	var repo ArmDecision
	if inPolicy {
		repo = DecideArmUnder(in, r.Variants(), r.Approval)
	} else {
		repo = DecideArmUnder(in, nil, nil)
	}
	switch {
	case repo.Verdict == central.Verdict && repo.Reason == central.Reason && repo.Matched == central.Matched:
		return repo, dualAgree
	case slices.ContainsFunc(snap.Divergences, func(d string) bool { return strings.HasPrefix(d, class+": ") }):
		return repo, dualExplained
	case !in.DiffRead && (repo.Verdict == "arm" || repo.Verdict == "shadow") && readsDiff(r, class):
		return repo, dualUnread
	}
	return repo, dualUnexplained
}

// readsDiff is whether deciding the class reads the diff, any variant or the approval subset.
func readsDiff(r mergepolicy.Resolved, class string) bool {
	return len(r.Approval) > 0 || slices.ContainsFunc(r.Variants(), func(v config.Class) bool { return v.ReadsDiff(class) })
}

// dual records a decision's dual evaluation, with policyManagement.dualEvaluation on: the counter,
// a log line with both decisions, and the record's policy and dual fields. It changes nothing the
// gate acts on: central's verdict is still the decision.
func (r *Reconciler) dual(ctx context.Context, t *v1alpha1.Task, in ArmInputs, central ArmDecision, d *ArmDecision) {
	if !r.Cfg.PolicyManagement.DualEvaluation || r.Policy == nil {
		return
	}
	snap, ok := r.Policy(t.Spec.Repository)
	repo, agreement := dualOf(central, in, snap, ok)
	d.Rows.Policy, d.Rows.Dual = shortHash(snap.Hash), agreement
	class := t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) { r.Metrics.PolicyDualDecision(ctx, t.Spec.Repository, class, agreement) })
	if agreement != dualAgree {
		r.log().Info("merge decision under the repository policy", "task.id", t.Name, "head", in.PR.HeadSHA, "class", class,
			"agreement", agreement, "policy", d.Rows.Policy, "central", central.Verdict+":"+central.Reason,
			"repository", repo.Verdict+":"+repo.Reason)
	}
}

func shortHash(h string) string { return h[:min(len(h), 12)] }
