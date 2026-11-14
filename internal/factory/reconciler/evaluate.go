// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/filefence"
	"github.com/Smana/maku/internal/factory/grant"
)

// DecideArmUnder is DecideArm with the task's class read from a repository's policy rather than
// the central config (merge policy MP16): one evaluator, so a simulation or a dual evaluation
// cannot disagree with the gate on the same inputs. cl is the class as the policy resolves it; nil
// when the policy lacks the class, which arms nothing. approval is the class's requiresApproval
// subset: a class decision touching it asks a human in a live class and is recorded as
// would-require-approval in a shadow one (the owner's Q3), and no approval arms over a gate path.
func DecideArmUnder(in ArmInputs, cl *config.Class, approval []string) ArmDecision {
	cfg := in.Cfg
	rc := cfg.RepositoryFor(in.Task.Spec.Repository)
	if rc == nil {
		return notServedDecision(in)
	}
	if cl == nil {
		return withGatePath(decide(in, cfg, rc, config.Class{}), in, rc, nil)
	}
	d := decide(in, cfg, rc, withShape(*cl, in.Task.Spec.PredictedClass))
	return withGatePath(withApproval(d, in, approval), in, rc, cl.ProtectedFiles)
}

// withApproval applies the approval subset to a class decision: an arm or a shadow of the class,
// not a maintainer's approval, which is a human's judgement already (AM1). The subset reads the
// diff and holds until it is read. In a live class, a saved grant that covers what the subset
// caught keeps the arm, and the record names it (merge policy stage 5, MP9); a shadow class never
// consults one.
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
	if g, ok := grantFor(in, approval); ok {
		d.Rows.Grant = g.Ref()
		return d
	}
	return ArmDecision{Verdict: "human", Reason: reasonApprovalRequired, Matched: d.Matched, Rows: d.Rows}
}

// grantFor is the grant that covers what the approval subset caught (MP10): active, of the task's
// repository and class, bound to the policy decided under, unexpired at the lookup, every caught
// path matching one of its patterns and its diff shape, when it names one, holding. A lookup that
// failed is no grant: it never authorizes (MP13). Of several, the lowest id, so a decision replays.
func grantFor(in ArmInputs, approval []string) (grant.Grant, bool) {
	if !in.Grants.Read {
		return grant.Grant{}, false
	}
	res := filefence.Compile(approval, false)
	var caught []string
	for _, p := range filefence.Paths(in.Stats) {
		if filefence.MatchesAny(res, p) {
			caught = append(caught, p)
		}
	}
	gs := slices.SortedFunc(slices.Values(in.Grants.List), func(a, b grant.Grant) int { return strings.Compare(a.ID, b.ID) })
	for _, g := range gs {
		if g.Status != grant.StatusActive || !strings.EqualFold(g.Repository, in.Task.Spec.Repository) ||
			g.Class != in.Task.Spec.PredictedClass || !in.Grants.At.Before(g.ExpiresAt) ||
			!g.BoundTo(in.Grants.PolicyHash) || !g.Covers(caught) {
			continue
		}
		if reason, _ := diffShape(config.Class{DiffShape: g.DiffShape}, in); g.DiffShape != "" && reason != "" {
			continue
		}
		return g, true
	}
	return grant.Grant{}, false
}
