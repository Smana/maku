// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/runs"
)

// StuckAfter is how long a Running run may show no room activity before the factory ends it (§6.3).
const StuckAfter = 10 * time.Minute

// stuck: a Running run with no room event for 10 minutes is deleted (§6.3). Activity starts
// counting when the factory first sees the run Running, so a cold node's 3 minutes are free.
func (r *Reconciler) stuck(ctx context.Context, t *v1alpha1.Task, run runs.Run) (bool, error) {
	if run.Phase != "Running" {
		return false, nil
	}
	cur := current(t)
	last := r.Now()
	// !Before, not After: the first observation stores Started itself when the run is seen at
	// once, and that value must count as this run's activity.
	if a := t.Status.LastActivity; a != nil && cur.Started != nil && !a.Before(cur.Started) {
		last = a.Time
	}
	// roomTail, not one EventsSince: a long room's newest activity is past a single 10,000-event
	// page, and a run busy in the room is exactly the run this must not end.
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, cur.StartSeq, func(e envelope.Event) bool { return e.RunID == run.ID })
	if err != nil {
		return false, err
	}
	for _, e := range evs {
		if e.TS.After(last) {
			last = e.TS
		}
	}
	lt := metav1.NewTime(last)
	t.Status.LastActivity = &lt
	if r.Now().Sub(last) < StuckAfter {
		return false, nil
	}
	if err := r.Runs.Annotate(ctx, run.ID, map[string]string{runs.AnnRevoked: "manual"}); err != nil {
		return false, err
	}
	return true, r.deleteRun(ctx, run.ID)
}

// agent_error is not here: it includes gateway and provider failures, which score nothing (R53).
// Nor is judge_impossible: a task the judge finds impossible says nothing of the tier.
var underReasons = []string{"stuck", "agent_stuck", "review_rounds_exhausted", "ci_red", "budget-run", "budget-task",
	"stalled", "not_met"}

// Fit is §7's after-the-fact score of the tier that ran: under when the task escalated for
// capability after a full attempt, over when it succeeded on < 20 % of the tier's task budget.
func Fit(t *v1alpha1.Task) string {
	switch {
	case t.Spec.Parent != nil || splitApproved(t):
		return "" // SR17: C7 chose neither a part's tier nor the work its parent did
	case t.Status.Phase == v1alpha1.PhaseEscalated && slices.Contains(underReasons, t.Status.Reason):
		return "under"
	case t.Status.Phase == v1alpha1.PhaseDone && t.Spec.Budget.TaskTokens > 0 && t.Status.Usage.Tokens*5 < t.Spec.Budget.TaskTokens:
		return "over"
	case t.Status.Phase == v1alpha1.PhaseDone:
		return "fit"
	}
	return ""
}

// TierScore is one classifier's tier rated against the tier that would have fit.
type TierScore struct{ Classifier, Tier, Fit string }

var tiers = []string{"light", "standard", "frontier"}

// Score rates every classifier, acting and shadow, against the tier that would have fit: a
// budget-fit heuristic (§7); the control group is not a counterfactual for lower tiers (R53).
func Score(cl v1alpha1.Classification, acting, fit string) []TierScore {
	right := slices.Index(tiers, acting)
	switch fit {
	case "under":
		right = min(right+1, 2)
	case "over":
		right = max(right-1, 0)
	}
	rate := func(tier string) string {
		switch i := slices.Index(tiers, tier); {
		case i == right:
			return "fit"
		case i < right:
			return "under"
		}
		return "over"
	}
	out := []TierScore{{Classifier: cl.Classifier, Tier: cl.Tier, Fit: rate(cl.Tier)}}
	for _, s := range cl.Shadow {
		out = append(out, TierScore{Classifier: s.Classifier, Tier: s.Tier, Fit: rate(s.Tier)})
	}
	return out
}

// outcome runs once per task, when it first ends: tier fit (SC-10) and the task.final line (SC-7).
func (r *Reconciler) outcome(ctx context.Context, t *v1alpha1.Task) {
	if cl := t.Status.Classification; cl != nil && cl.Fit == "" {
		if fit := Fit(t); fit != "" {
			cl.Fit = fit
			for _, s := range Score(*cl, t.Spec.Budget.Tier, fit) {
				record(ctx, func(ctx context.Context) { r.Metrics.TierFit(ctx, s.Classifier, s.Tier, s.Fit, cl.Control) })
			}
		}
	}
	if !v1alpha1.TerminalPhase(t.Status.Phase) {
		return
	}
	if t.Status.EndedAt == nil { // the first end is the one counted (R34): a later Reverted revises nothing
		at := metav1.NewTime(r.Now())
		if t.Status.PhaseSince != nil {
			at = *t.Status.PhaseSince
		}
		t.Status.EndedAt, t.Status.Outcome = &at, taskOutcome(t)
	}
	if !r.settling(t) { // never ran: nothing settles, and nothing waits (R34)
		outcome, micro, _, err := r.tally(ctx, t)
		if err != nil { // a family read: a task that never ran has none
			r.log().Warn("task outcome not counted", "task.id", t.Name, "err", err)
		} else {
			r.count(ctx, t, outcome, micro)
		}
	}
	ids := make([]string, 0, len(t.Status.Runs))
	for _, x := range t.Status.Runs {
		ids = append(ids, x.ID)
	}
	var pr, mergedBy, classifier, tier string
	control := false
	if ref := t.Status.PullRequest; ref != nil {
		pr, mergedBy = ref.URL, ref.MergedBy
	}
	if cl := t.Status.Classification; cl != nil {
		classifier, tier, control = cl.Classifier, cl.Tier, cl.Control
	}
	r.log().Info("task.final", "task.id", t.Name, "source", t.Spec.Source.Kind, "key", t.Spec.Source.Key,
		"contentSHA256", t.Spec.Source.ContentSHA256, "classifier", classifier, "tier", tier, "control", control,
		"runIds", strings.Join(ids, ","), "room", t.Status.RoomRef, "pr", pr, "mergedBy", mergedBy,
		"phase", t.Status.Phase, "reason", t.Status.Reason, "tokens", t.Status.Usage.Tokens)
}

// counting: t has a profile its outcome and USD are owed to, and has not been counted yet.
func counting(t *v1alpha1.Task) bool {
	return t.Status.Profile != nil && !t.Status.Profile.Counted
}

// tally is what a terminal task's count says (R34): the outcome it first ended with and its USD;
// for an approved split's parent, the family's (R32), every part's generation summed. Every read
// is here and none is a write, so an error leaves the status as it was and the next pass retries.
// ready is false while a part's usage still settles, within familyBound of the parent's end; past
// it the parent counts what it has. A task that counts nothing reads nothing.
func (r *Reconciler) tally(ctx context.Context, t *v1alpha1.Task) (outcome string, micro int64, ready bool, err error) {
	if !counting(t) {
		return "", 0, true, nil
	}
	outcome, micro = t.Status.Outcome, t.Status.Usage.CostMicroUSD
	if outcome == "" {
		outcome = taskOutcome(t)
	}
	if !splitApproved(t) {
		return outcome, micro, true, nil
	}
	newest, all, err := r.family(ctx, t)
	if err != nil {
		return "", 0, false, err
	}
	for i := range all {
		c := &all[i]
		if !settledTask(c) && !r.familyBoundPassed(t) {
			return "", 0, false, nil
		}
		micro += r.costOf(c)
	}
	return familyOutcome(t, newest), micro, true, nil
}

// settledTask: t has ended and its usage is final, nothing to settle for a task that never ran.
func settledTask(t *v1alpha1.Task) bool {
	return v1alpha1.TerminalPhase(t.Status.Phase) && (len(t.Status.Runs) == 0 || t.Status.UsageSettled)
}

// count records a tally once, by the task's profile. The mark rides the status write the metric
// waits for: once, or neither.
func (r *Reconciler) count(ctx context.Context, t *v1alpha1.Task, outcome string, micro int64) {
	if !counting(t) {
		return
	}
	p := t.Status.Profile
	p.Counted = true
	class, rec, usd := t.Spec.PredictedClass, r.recommendedLabel(t), float64(micro)/1e6
	record(ctx, func(ctx context.Context) {
		r.Metrics.TaskOutcome(ctx, class, p.Cohort, p.Name, p.Source, rec, outcome, usd)
	})
}

// familyBound is how long past its own settle window an approved split's parent waits for its
// parts' usage to settle: one more window (twice the window from its end). A part the family ended
// is stopped on the parent's pass and settles one window later; a part that never settles — deleted,
// or ended by nobody — holds the count no longer than this.
func (r *Reconciler) familyBound() time.Duration { return 2 * r.settleWindow() }

func (r *Reconciler) familyBoundPassed(t *v1alpha1.Task) bool {
	at := endedAt(t)
	return at != nil && r.Now().Sub(at.Time) >= r.familyBound()
}

// endedAt is a terminal task's first end, PhaseSince for a task ended before the field was written.
func endedAt(t *v1alpha1.Task) *metav1.Time {
	if t.Status.EndedAt != nil {
		return t.Status.EndedAt
	}
	return t.Status.PhaseSince
}

// familyOutcome is an approved split's outcome, read on the parent over every part of the split,
// opened or not (R32): accepted when every part merged with no human commit; merged_human when any
// did with one; otherwise the first other outcome by part index. A part reads as the parent's own
// outcome when it was never opened or the family ended before its own end; as reverted when the
// parent recorded it so, whatever its task first ended as; else as its task's first end.
func familyOutcome(p *v1alpha1.Task, newest map[int32]*v1alpha1.Task) string {
	own := taskOutcome(p)
	sp := p.Status.Split
	first, human := "", false
	of := sp.Of
	for _, c := range sp.Children {
		of = max(of, c.Index)
	}
	for k := int32(1); k <= of; k++ {
		o := own
		i := slices.IndexFunc(sp.Children, func(c v1alpha1.SplitChild) bool { return c.Index == k })
		switch n := newest[k]; {
		case i < 0:
		case sp.Children[i].Outcome == ChildReverted:
			o = "reverted"
		case n != nil && n.Status.Outcome != "":
			o = n.Status.Outcome
		case n != nil && v1alpha1.TerminalPhase(n.Status.Phase):
			o = taskOutcome(n)
		}
		switch {
		case o == "merged_human":
			human = true
		case o != "accepted" && first == "":
			first = o
		}
	}
	switch {
	case human:
		return "merged_human"
	case first != "":
		return first
	}
	return "accepted"
}

// costOf is what t's runs cost, each at the larger of its record and the meter's current figure:
// a part's status may trail the meter by a tick when its parent settles.
func (r *Reconciler) costOf(t *v1alpha1.Task) int64 {
	var sum int64
	for _, rec := range t.Status.Runs {
		c := rec.CostMicroUSD
		if r.Costs != nil {
			if m, ok := r.Costs.Get(rec.ID); ok {
				c = max(c, m.MicroUSD)
			}
		}
		sum += c
	}
	return max(sum, t.Status.Usage.CostMicroUSD)
}

// splitApproved: t is the parent of an approved split, whose parts carry the work.
func splitApproved(t *v1alpha1.Task) bool {
	return t.Status.Split != nil && t.Status.Split.State == SplitApproved
}

// taskOutcome is an ended task's outcome: accepted is merged with no human commit.
func taskOutcome(t *v1alpha1.Task) string {
	switch t.Status.Phase {
	case v1alpha1.PhaseDone:
		switch {
		case t.Status.Reason == reasonIntegrationFailed:
			return "failed"
		case t.Status.PullRequest != nil && t.Status.PullRequest.HumanCommits:
			return "merged_human"
		}
		return "accepted"
	case v1alpha1.PhaseNoOp:
		return "no_op"
	case v1alpha1.PhaseClosed:
		return "closed"
	case v1alpha1.PhaseStopped:
		return "stopped"
	case v1alpha1.PhaseRejected:
		return "rejected"
	case v1alpha1.PhaseReverted:
		return "reverted"
	}
	return "failed"
}

// cohortOf labels a task's metrics with its team-evaluation cohort, "none" without a profile.
func cohortOf(t *v1alpha1.Task) string {
	if t.Status.Profile == nil {
		return "none"
	}
	return t.Status.Profile.Cohort
}

// profileOf is t's harness profile as a metric label, none without one.
func profileOf(t *v1alpha1.Task) string {
	if t.Status.Profile == nil {
		return "none"
	}
	return t.Status.Profile.Name
}

// recommendedOf is the scoper's recommendation on t as a metric label, none without a profile.
func (r *Reconciler) recommendedOf(t *v1alpha1.Task) string {
	if t.Status.Profile == nil {
		return "none"
	}
	return r.recommendedLabel(t)
}

// intervention counts one intervention on t, by its cohort, profile and recommendation, once the
// status write that carries its cause succeeds.
func (r *Reconciler) intervention(ctx context.Context, t *v1alpha1.Task, kind string) {
	cohort, profile, rec := cohortOf(t), profileOf(t), r.recommendedOf(t)
	record(ctx, func(ctx context.Context) { r.Metrics.Intervention(ctx, kind, cohort, profile, rec) })
}

// interventions counts, once per run, how humans steered it from the room (§7).
func (r *Reconciler) interventions(ctx context.Context, t *v1alpha1.Task) {
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, current(t).StartSeq, func(e envelope.Event) bool {
		return e.Actor.Kind == envelope.ActorHuman || e.Type == envelope.Driver
	})
	if err != nil {
		return
	}
	for _, e := range evs {
		switch e.Type {
		case envelope.Message:
			if e.Actor.Kind != envelope.ActorHuman {
				continue
			}
			var p envelope.MessagePayload
			if json.Unmarshal(e.Payload, &p) == nil && p.Delivery == envelope.DeliverySteering {
				r.intervention(ctx, t, "steer")
			}
		case envelope.Driver:
			var p envelope.DriverPayload
			if json.Unmarshal(e.Payload, &p) == nil && strings.HasPrefix(p.To, "human:") {
				r.intervention(ctx, t, "takeover")
			}
		}
	}
}
