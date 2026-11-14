// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"maps"
	"slices"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// The reasons a cut-over repository's gate pauses on its policy (stage 4, MP13).
const (
	// reasonPolicyUnavailable: no read, or one older than the fence's window. The gate waits for
	// the next read, bounded by merge.awaitingCIFor like any wait.
	reasonPolicyUnavailable = "policy_unavailable"
	// reasonPolicyInvalid: the default branch's file is invalid or declares no class. A
	// maintainer fixes the file, or merges by hand.
	reasonPolicyInvalid = "policy_invalid"
)

// reasonRepositoryNotServed: the task's repository is not in the published config (a publish
// removed it). Nothing is decided under another repository's settings (factory resources D12).
const reasonRepositoryNotServed = "repository_not_served"

// gatePolicy is a cut-over repository's policy for one pass of the gate (stage 4), read once so
// both of the pass's decisions bind to the same snapshot (MP12). on is false for every other
// repository, and for every repository with policyManagement.repositoryAuthority off.
type gatePolicy struct {
	on   bool
	snap mergepolicy.Snapshot
	err  error
}

// gatePolicy reads the task's repository's policy when the repository is cut over. Without an
// Authority source a cut-over repository has no readable policy, and pauses.
func (r *Reconciler) gatePolicy(cfg *config.Config, rc *config.Repository, repository string) gatePolicy {
	if !cfg.RepositoryAuthoritative(rc) {
		return gatePolicy{}
	}
	if r.Authority == nil {
		return gatePolicy{on: true, err: mergepolicy.ErrUnreadable}
	}
	snap, err := r.Authority(repository)
	return gatePolicy{on: true, snap: snap, err: err}
}

// loadError is the policy load error the gate counts for p: unreadable or stale, which only the
// gate sees; an invalid file is counted on each read (mergepolicy.Watch).
func (p gatePolicy) loadError() string {
	switch {
	case !p.on:
		return ""
	case errors.Is(p.err, mergepolicy.ErrUnreadable):
		return mergepolicy.ReasonUnreadable
	case errors.Is(p.err, mergepolicy.ErrStale):
		return mergepolicy.ReasonStale
	}
	return ""
}

// decideUnder is the gate's decision: DecideArm on the central config, or, for a cut-over
// repository, DecideArmUnder on the class its policy binds and its requiresApproval subset (one evaluator, MP16). The record binds the decision to the policy's
// hash. A policy that gives no authority pauses: the fixed base is still decided first.
func decideUnder(in ArmInputs, p gatePolicy) ArmDecision {
	if !p.on {
		return DecideArm(in)
	}
	if p.err != nil {
		return noAuthority(in, p)
	}
	var d ArmDecision
	if r, ok := p.snap.Classes[in.Task.Spec.PredictedClass]; ok {
		d = DecideArmUnder(in, &r.Class, r.Approval)
	} else {
		d = DecideArmUnder(in, nil, nil)
	}
	if rc := in.Cfg.RepositoryFor(in.Task.Spec.Repository); rc != nil {
		d = withGatePath(d, in, rc, policyGatePaths(p.snap.Classes)) // the policy's gate paths, every class
	}
	d.Rows.Policy = shortHash(p.snap.Hash)
	return d
}

// policyGatePaths is every protectedFiles pattern of a policy's classes: without policy-bot, the
// gate paths of a repository whose policy is authoritative.
func policyGatePaths(classes map[string]mergepolicy.Resolved) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(classes)) {
		out = append(out, classes[name].Class.ProtectedFiles...)
	}
	return out
}

// noAuthority is the decision on a policy that gives no authority (MP13): the fixed base's, when
// it refuses or waits; otherwise a wait for the next read, or a maintainer for an invalid file.
// Nothing arms or shadows: human merges continue.
func noAuthority(in ArmInputs, p gatePolicy) ArmDecision {
	rc := in.Cfg.RepositoryFor(in.Task.Spec.Repository)
	if rc == nil {
		return notServedDecision(in)
	}
	rows := recordOf(in, config.Class{})
	rows.Policy = shortHash(p.snap.Hash)
	d, done := baseChecks(in, rc)
	if !done {
		d = ArmDecision{Verdict: "wait", Reason: reasonPolicyUnavailable, Matched: in.Task.Spec.PredictedClass}
		if errors.Is(p.err, mergepolicy.ErrInvalid) {
			d.Verdict, d.Reason = "human", reasonPolicyInvalid
		}
	}
	d.Rows = rows
	return withIntegrationHold(in.Integration, d)
}

// readsDiff is whether deciding the task's class reads the diff: the central class's, or every
// variant and the approval subset of the repository's.
func (p gatePolicy) readsDiff(rc *config.Repository, class string) bool {
	if !p.on {
		return rc.Classes[class].ReadsDiff(class) || len(rc.Classes[class].RequiresApproval) > 0
	}
	r, ok := p.snap.Classes[class]
	return ok && readsDiff(r, class)
}
