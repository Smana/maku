// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// reasonNoPolicy: no policy of the repository holds on the diff (MR1). It replaces class_unarmed
// and, on an agents pull request, class_mismatch: no label is read to say what was expected.
const reasonNoPolicy = "no_policy_matched"

// held is one candidate policy judged on the pull request: Reason is "" when every condition it
// declares holds, else the reason it refused with. D is the decision it alone would make.
type held struct {
	Name   string
	Class  config.Class
	Rows   decision.Rows
	Reason string
	D      ArmDecision
}

// selectsOne is whether the task is decided on its one class rather than on the diff (MR1): a
// task with findings, which only a schedule's probe sets (its oracle reads them), or an adopted
// pull request, which the poller bound to its class on author, branch and files.
func selectsOne(classes map[string]config.Class, t *v1alpha1.Task) bool {
	return len(t.Spec.Findings) > 0 || adoptedTask(t) || classes[t.Spec.PredictedClass].Adopted()
}

// candidates are the policies a diff is judged against (MR1): every class live or in shadow, not
// adopted and without an oracle, in name order. A task decided on its one class has only that one.
func candidates(classes map[string]config.Class, t *v1alpha1.Task) []string {
	if selectsOne(classes, t) {
		return []string{t.Spec.PredictedClass}
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(classes)) {
		if name == "revert" {
			continue // rollback is merge.autoRevert, not a diff-chosen merge policy
		}
		if cl := classes[name]; (cl.Live || cl.Shadow) && !cl.Adopted() && cl.Oracle == nil {
			out = append(out, name)
		}
	}
	return out
}

// stricter is the selection order (MR1): a diff shape first, then the lower maxModifiedLines,
// then the lower maxPatchFiles (0, no cap, sorts last in both), then the name.
func stricter(a, b held) bool {
	return compareHeld(a, b) < 0
}

func compareHeld(a, b held) int {
	shaped := func(h held) int {
		if h.Class.DiffShape != "" {
			return 0
		}
		return 1
	}
	capOf := func(n int) int {
		if n <= 0 {
			return int(^uint(0) >> 1)
		}
		return n
	}
	return cmp.Or(cmp.Compare(shaped(a), shaped(b)),
		cmp.Compare(capOf(a.Class.MaxModifiedLines), capOf(b.Class.MaxModifiedLines)),
		cmp.Compare(capOf(a.Class.MaxPatchFiles), capOf(b.Class.MaxPatchFiles)),
		strings.Compare(a.Name, b.Name))
}

// pausedOf is the policy's breaker: PausedByClass's, else Paused for the task's own class.
func (in ArmInputs) pausedOf(name string) bool {
	if v, ok := in.PausedByClass[name]; ok {
		return v
	}
	return in.Paused && name == in.Task.Spec.PredictedClass
}

// armedOf is the policy's merges today: ArmedByClass's, else ClassArmedToday for the task's class.
func (in ArmInputs) armedOf(name string) int {
	if v, ok := in.ArmedByClass[name]; ok {
		return v
	}
	if name == in.Task.Spec.PredictedClass {
		return in.ClassArmedToday
	}
	return 0
}

// decideAmong is the diff-chosen gate (MR1): the rows no policy changes once, then each candidate
// of classes judged on the diff. A pending check waits where it could decide. Otherwise the strictest live
// policy that holds decides, its breaker and its day included, and no looser one takes over; with
// none, the strictest shadow one that holds; with none, no_policy_matched. Every other shadow
// policy that holds is in Shadows, and every candidate that did not hold is in Rows.Considered.
func decideAmong(in ArmInputs, cfg *config.Config, rc *config.Repository, classes map[string]config.Class,
	approvals map[string][]string) ArmDecision {
	rows := recordOf(in, config.Class{})
	if d, done := front(in, cfg, rc, config.Class{}, ""); done {
		d.Rows = rows
		return withIntegrationHold(in.Integration, d)
	}
	human := func(reason string) ArmDecision {
		return withIntegrationHold(in.Integration, ArmDecision{Verdict: "human", Reason: reason, Rows: rows})
	}
	if rows.Evidence = runEvidence(in, cfg); rows.Evidence != "" {
		return human(rows.Evidence)
	}
	if in.Task != nil && in.Task.Status.Profile != nil && in.Task.Status.Profile.Requires != nil && len(in.Task.Status.Profile.Requires.Unmet) > 0 {
		rows.Evidence = reasonRequiredStageMissing
		return human(reasonRequiredStageMissing)
	}
	rows.Evidence = "ok"
	if in.DiffRead && cfg.SteeringOn() && touchesSteering(in.Stats) {
		return human("steering_path") // no agent pull request changes a note (scoper SC3)
	}
	if in.DiffRead && touchesUpkeepPath(rc, in.Stats) {
		return human("upkeep_path") // no agent pull request changes a routine prompt or bound skill
	}
	var live, shadow, liveWait, shadowWait, liveGuards []held
	var considered []string
	for _, name := range candidates(classes, in.Task) {
		cl := withShape(classes[name], name)
		r := recordOf(in, cl)
		r.Evidence = "ok"
		d, holds := judge(in, rc, name, cl, r)
		if e := missingRequiredStage(in, cfg, cl); holds && e != "" { // WR6/E7: a policy that holds needs its required stages
			r.Evidence = e
			d, holds = ArmDecision{Verdict: "human", Reason: e, Matched: name, Rows: r}, false
		}
		if e := verdictEvidence(in, cfg, cl); holds && e != "" { // MR12: a policy that holds needs a reviewer's verdict
			r.Evidence = e
			d, holds = ArmDecision{Verdict: "human", Reason: e, Matched: name, Rows: r}, false
		}
		d = withApproval(d, in, approvals[name])
		h := held{Name: name, Class: cl, Rows: d.Rows, Reason: d.Reason, D: d}
		guard := cl.Live && d.Verdict == "human" && slices.Contains([]string{"file_protected", reasonApprovalRequired, "holdout_red"}, d.Reason)
		switch {
		case guard:
			liveGuards = append(liveGuards, h)
			considered = append(considered, name+":"+d.Reason)
		case d.Verdict == "wait" && cl.Live:
			liveWait = append(liveWait, h)
		case d.Verdict == "wait":
			shadowWait = append(shadowWait, h)
		case !holds:
			considered = append(considered, name+":"+d.Reason)
		case cl.Live:
			live = append(live, h)
		default:
			shadow = append(shadow, h)
		}
	}
	for _, l := range [][]held{live, shadow, liveWait, shadowWait, liveGuards} {
		slices.SortFunc(l, compareHeld)
	}
	if len(liveGuards) > 0 {
		d := liveGuards[0].D
		d.Rows.Considered = strings.Join(considered, ",")
		return withIntegrationHold(in.Integration, d)
	}
	// A pending check waits only where it could decide: the strictest pending policy of the tier
	// that decides, stricter than every policy of that tier that holds. A looser pending policy, or
	// a shadow one beside a live policy that holds, is recorded as considered and waits for nothing.
	pending := func(wait, hold []held) bool { return len(wait) > 0 && (len(hold) == 0 || stricter(wait[0], hold[0])) }
	switch {
	case pending(liveWait, live):
		return withIntegrationHold(in.Integration, liveWait[0].D)
	case len(live) == 0 && pending(shadowWait, shadow):
		return withIntegrationHold(in.Integration, shadowWait[0].D)
	}
	for _, h := range append(liveWait, shadowWait...) {
		considered = append(considered, h.Name+":"+h.Reason)
	}
	var d ArmDecision
	switch {
	case len(live) > 0:
		d = live[0].D
	case len(shadow) > 0:
		d, shadow = shadow[0].D, shadow[1:]
	default:
		d = ArmDecision{Verdict: "human", Reason: reasonNoPolicy, Rows: rows}
	}
	d.Rows.Considered = strings.Join(considered, ",")
	for _, h := range shadow {
		d.Shadows = append(d.Shadows, ShadowHold{Name: h.Name, Rows: h.Rows})
	}
	return withIntegrationHold(in.Integration, d)
}

// classOf is the policy the gate last decided t's pull request under (MR1), which its merge, its
// breaker, its day and its monitor count against; the task's predicted class before any did.
func classOf(t *v1alpha1.Task) string {
	if ref := t.Status.PullRequest; ref != nil && ref.Policy != "" {
		return ref.Policy
	}
	return t.Spec.PredictedClass
}

// decidedPolicy is the policy a decision names for its task's record (MR1): the policy matched on
// the diff, "" when none was; a task decided on its one class keeps that class.
func decidedPolicy(t *v1alpha1.Task, d ArmDecision) string {
	if d.Matched == "" || d.Matched == "review" || d.Matched == "gate" || d.Matched == t.Spec.PredictedClass {
		return ""
	}
	return d.Matched
}

// candidateCounts are each diff candidate's breaker and merges today (MR1), the central classes'
// and a cut-over repository's policy's alike.
func (r *Reconciler) candidateCounts(ctx context.Context, cfg *config.Config, rc *config.Repository, p gatePolicy,
	t *v1alpha1.Task) (map[string]bool, map[string]int, error) {
	names := candidates(rc.Classes, t)
	if p.on && p.err == nil {
		names = append(names, candidates(snapClasses(p.snap.Classes), t)...)
	}
	paused, armed := map[string]bool{}, map[string]int{}
	for _, name := range names {
		if _, ok := paused[name]; ok {
			continue
		}
		var err error
		if paused[name], err = r.paused(ctx, cfg, rc, name); err != nil {
			return nil, nil, err
		}
		if armed[name], err = r.armedTodayOf(ctx, cfg, rc, name); err != nil {
			return nil, nil, err
		}
	}
	return paused, armed, nil
}

// snapClasses is a repository policy's classes as the gate judges them.
func snapClasses(classes map[string]mergepolicy.Resolved) map[string]config.Class {
	out := make(map[string]config.Class, len(classes))
	for name, r := range classes {
		out[name] = r.Class
	}
	return out
}

// decidePolicy is DecideArm under a repository policy's classes (merge policy MP16): the task's
// one class through DecideArmUnder when it is decided on one, else the diff-chosen gate over every
// class of the policy, each with its requiresApproval subset.
func decidePolicy(in ArmInputs, classes map[string]mergepolicy.Resolved) ArmDecision {
	cs := snapClasses(classes)
	if selectsOne(cs, in.Task) {
		if r, ok := classes[in.Task.Spec.PredictedClass]; ok {
			return DecideArmUnder(in, &r.Class, r.Approval)
		}
		return DecideArmUnder(in, nil, nil)
	}
	rc := in.Cfg.RepositoryFor(in.Task.Spec.Repository)
	if rc == nil {
		return notServedDecision(in)
	}
	approvals := make(map[string][]string, len(classes))
	for name, r := range classes {
		approvals[name] = r.Approval
	}
	return withGatePath(decideAmong(in, in.Cfg, rc, cs, approvals), in, rc, nil)
}

// recordClass is the class= of a decision's record: the task's class when it is decided on its one
// class, else the policy the diff matched, "" when none was (MR1).
func recordClass(rc *config.Repository, t *v1alpha1.Task, d ArmDecision) string {
	if rc == nil || selectsOne(rc.Classes, t) {
		return t.Spec.PredictedClass
	}
	if d.Matched == "review" || d.Matched == "gate" {
		return ""
	}
	return d.Matched
}
