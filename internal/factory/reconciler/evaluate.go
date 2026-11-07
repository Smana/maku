// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/filefence"
)

// DecideArmUnder is DecideArm with the task's class read from a repository's policy rather than
// the central config (merge policy MP16): one evaluator, so a simulation or a dual evaluation
// cannot disagree with the gate on the same inputs. variants are the class's resolved branches of
// any (one without any; none when the policy lacks the class, which arms nothing), each decided
// as DecideArm decides a class. The class holds when one variant does: an arm outranks a shadow,
// a shadow a wait, a wait any refusal, and with none holding the first variant's refusal stands.
// approval is the class's requiresApproval subset: a class decision touching it asks a human in a
// live class and is recorded as would-require-approval in a shadow one (the owner's Q3). Through
// stage 3 nothing merges on this decision.
func DecideArmUnder(in ArmInputs, variants []config.Class, approval []string) ArmDecision {
	rc := in.Cfg.RepositoryFor(in.Task.Spec.Repository)
	class := in.Task.Spec.PredictedClass
	if len(variants) == 0 {
		return decide(in, rc, config.Class{})
	}
	var best ArmDecision
	for i, v := range variants {
		d := decide(in, rc, withShape(v, class))
		if i == 0 || rank(d) > rank(best) {
			best = d
		}
	}
	return withApproval(best, in, approval)
}

// rank orders a variant's decisions for any: what holds first, a wait before a refusal.
func rank(d ArmDecision) int {
	switch d.Verdict {
	case "arm":
		return 3
	case "shadow":
		return 2
	case "wait":
		return 1
	}
	return 0
}

// withApproval applies the approval subset to a class decision: an arm or a shadow of the class,
// not a maintainer's approval, which is a human's judgement already (AM1). The subset reads the
// diff and holds until it is read.
func withApproval(d ArmDecision, in ArmInputs, approval []string) ArmDecision {
	classArm := d.Verdict == "arm" && d.Reason == ""
	if len(approval) == 0 || !in.DiffRead || (!classArm && d.Verdict != "shadow") {
		return d
	}
	if filefence.ProtectedFiles(config.Class{ProtectedFiles: approval}, in.Stats) == "" {
		return d
	}
	if d.Verdict == "shadow" {
		d.Reason = reasonShadowApproval
		return d
	}
	return ArmDecision{Verdict: "human", Reason: reasonApprovalRequired, Matched: d.Matched, Rows: d.Rows}
}
