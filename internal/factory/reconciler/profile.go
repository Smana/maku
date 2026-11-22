// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/triage"
)

// resolveProfile settles t's profile from scope s (nil: the scoper left none); the count is deferred so a replay counts once.
func (r *Reconciler) resolveProfile(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, s *envelope.Scope) error {
	if !unresolved(t) {
		return nil
	}
	offered := len(r.profileChoices(cfg, t)) > 0
	q := *t.Status.Profile
	q.Resolved = true
	r.recommend(cfg, t, &q, s)
	switch d, persisted := decided(t); {
	case persisted && d.Source == "scoper": // the spec write landed and the status write did not: the decision binds, not today's config
		d.restore(&q)
		offered = offered || d.Offered
	case q.Source != triage.SourceRepository && q.Source != "floor" && q.Source != "control" && q.Source != "treatment":
		// a maintainer's label, or the task's source (WF3): the scoper's pick is only recorded
	case s == nil:
		q.Reason = "scoper_failed"
	case q.Source == "control", q.Source == "treatment": // either arm of the measurement keeps what triage drew (WF8)
	default:
		name, reason, err := r.variantFor(ctx, cfg, t, q.Name, s.Profile)
		if err != nil {
			return err
		}
		if reason != "" {
			q.Reason = reason
			break
		}
		v, _ := cfg.Workflow(name)
		d := profileDecision{Name: name, Source: "scoper", Checks: q.Checks || v.Checks, TestFirst: q.TestFirst || r.repo(cfg, t).TestFirstIn(name),
			Recommended: q.Recommended, Why: q.Why, Offered: offered}
		if err := r.applyProfile(ctx, cfg, t, d); err != nil {
			return err
		}
		d.restore(&q)
	}
	*t.Status.Profile = q
	narrateLater(t, narrate.ProfileChosen(t, q.Name, q.Source, q.Reason, q.Why, len(t.Status.Runs)))
	if offered {
		class, cohort, rec, applied := t.Spec.PredictedClass, q.Cohort, r.recommendedLabel(cfg, t), q.Source == "scoper"
		record(ctx, func(ctx context.Context) { r.Metrics.ProfileRecommendation(ctx, class, cohort, rec, applied) })
		agreed := agreement(t, q.Recommended)
		record(ctx, func(ctx context.Context) { r.Metrics.ProfileAgreement(ctx, class, agreed) })
	}
	return nil
}

// agreement compares the planner's suggestion (spec.intake.suggested, read here and by nothing
// that briefs an agent, P13) with the scoper's recommendation: no_suggestion when the plan named
// none (the issue path among them), no_recommendation when the scoper named none, else agree or
// differ.
func agreement(t *v1alpha1.Task, recommended string) string {
	suggested := ""
	if in := t.Spec.Intake; in != nil {
		suggested = in.Suggested
	}
	switch {
	case suggested == "":
		return "no_suggestion"
	case recommended == "":
		return "no_recommendation"
	case suggested == recommended:
		return "agree"
	}
	return "differ"
}

// recommend notes scope s's recommendation on p, t's profile or a copy of it, when the class offers
// a choice; nil, or a class with none, notes nothing.
func (r *Reconciler) recommend(cfg *config.Config, t *v1alpha1.Task, p *v1alpha1.ProfileStatus, s *envelope.Scope) {
	if s == nil || p == nil || len(r.profileChoices(cfg, t)) == 0 {
		return
	}
	p.Recommended, p.Why = s.Profile, s.ProfileWhy
}

// recommendedLabel is the scoper's recommendation on t as a bounded metric label: a profile the
// class offers or the one the task runs under (offered when it was applied), none without one,
// other for a name no longer offered.
func (r *Reconciler) recommendedLabel(cfg *config.Config, t *v1alpha1.Task) string {
	rec := t.Status.Profile.Recommended
	switch {
	case rec == "":
		return "none"
	case rec == t.Status.Profile.Name, slices.ContainsFunc(r.profileChoices(cfg, t), func(c ProfileChoice) bool { return c.Name == rec }):
		return rec
	}
	return "other"
}

// profileDecision is what applyProfile persists beside the spec it applies, under
// AnnotationProfileDecision, so a replay can restore the status half of the decision without
// reading a config that may have changed since: the frozen booleans, the scoper's recommendation
// and whether the class offered a choice, which the recommendation count hangs on.
type profileDecision struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
	Checks      bool   `json:"checks,omitempty"`
	TestFirst   bool   `json:"testFirst,omitempty"`
	Recommended string `json:"recommended,omitempty"`
	Why         string `json:"why,omitempty"`
	Offered     bool   `json:"offered,omitempty"`
}

func (d profileDecision) restore(p *v1alpha1.ProfileStatus) {
	p.Name, p.Source, p.Reason, p.Checks, p.TestFirst = d.Name, d.Source, d.Reason, d.Checks, d.TestFirst
	p.Recommended, p.Why = d.Recommended, d.Why
}

// decided is the decision t's spec carries, if any.
func decided(t *v1alpha1.Task) (profileDecision, bool) {
	var d profileDecision
	raw, ok := t.Annotations[v1alpha1.AnnotationProfileDecision]
	if !ok || json.Unmarshal([]byte(raw), &d) != nil {
		return profileDecision{}, false
	}
	return d, true
}

func unresolved(t *v1alpha1.Task) bool {
	return t.Status.Profile != nil && !t.Status.Profile.Resolved && t.Spec.Parent == nil
}

// assumed is sc as an assumed end runs it: a question's scope carries no recommendation.
func assumed(sc envelope.Scope) *envelope.Scope {
	sc.Profile, sc.ProfileWhy = "", ""
	return &sc
}

// variantFor is the workflow the scoper's recommendation name moves t to from current, or why it
// does not: none (no recommendation, or the one it runs), unknown, observe (recorded only), or
// no_tests_paths for a public task the move would start with a tester the repository cannot run.
// Any workflow may be recommended since the workflow left the class (WF4).
func (r *Reconciler) variantFor(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, current, name string) (string, string, error) {
	if name == "" || name == current {
		return "", "none", nil
	}
	if !cfg.IsWorkflow(name) {
		return "", "unknown", nil
	}
	if !cfg.Applies() {
		return "", "observe", nil
	}
	if r.repo(cfg, t).TestFirstIn(name) && t.Spec.DataClass == "public" {
		paths, err := r.testPaths(ctx, t)
		if err != nil {
			return "", "", err
		}
		if len(paths) == 0 {
			return "", "no_tests_paths", nil
		}
	}
	return name, "", nil
}

// applyProfile persists decision d's spec and d itself in one update; a replay restores the status
// from the persisted decision, never by deciding again.
func (r *Reconciler) applyProfile(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, d profileDecision) error {
	p, ok := cfg.Workflow(d.Name)
	if !ok {
		return fmt.Errorf("workflow %q is not one of the factory's", d.Name)
	}
	before := t.Spec.DeepCopy()
	r.profileSpec(cfg, t, d.Name, p)
	// A changed mode would read the finished scoper's run as a triager proposal on replay.
	t.Spec.ScopeMode = before.ScopeMode
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(*before, t.Spec) && t.Annotations[v1alpha1.AnnotationProfileDecision] == string(raw) {
		return nil
	}
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[v1alpha1.AnnotationProfileDecision] = string(raw)
	status := t.Status // Update returns the server's status into t; keep ours
	if err := r.Client.Update(ctx, t); err != nil {
		return err
	}
	t.Status = status
	return nil
}

// resolveRetried runs before the retry changes any state, so a broker error replays the retry.
func (r *Reconciler) resolveRetried(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if !unresolved(t) || !scoperPlanned(t) || t.Status.Scope == nil || splitApproved(t) {
		return nil
	}
	sc, err := r.taskScope(ctx, t)
	switch {
	case err != nil:
		return err
	case sc == nil:
		return r.resolveProfile(ctx, cfg, t, nil)
	case t.Status.Reason == ReasonNeedsAuthor || sc.Assumed:
		return r.resolveProfile(ctx, cfg, t, assumed(sc.Scope))
	}
	return r.resolveProfile(ctx, cfg, t, &sc.Scope)
}

func resolveUnscoped(t *v1alpha1.Task) {
	if !unresolved(t) {
		return
	}
	p := t.Status.Profile
	p.Resolved = true
	narrateLater(t, narrate.ProfileChosen(t, p.Name, p.Source, p.Reason, "", len(t.Status.Runs)))
}
