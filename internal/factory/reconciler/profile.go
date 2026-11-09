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
	"github.com/Smana/maku/internal/factory/narrate"
)

// resolveProfile settles t's profile from scope s (nil: the scoper left none); the count is deferred so a replay counts once.
func (r *Reconciler) resolveProfile(ctx context.Context, t *v1alpha1.Task, s *envelope.Scope) error {
	if !unresolved(t) {
		return nil
	}
	choices := r.profileChoices(t)
	q := *t.Status.Profile
	q.Resolved = true
	if s != nil && len(choices) > 0 {
		q.Recommended, q.Why = s.Profile, s.ProfileWhy
	}
	switch d, persisted := decided(t); {
	case persisted && d.Source == "scoper": // the spec write landed and the status write did not: the decision binds, not today's config
		d.restore(&q)
	case q.Source == "label":
	case s == nil:
		q.Reason = "scoper_failed"
	case q.Source == "control":
	default:
		name, reason, err := r.variantFor(ctx, t, s.Profile)
		if err != nil {
			return err
		}
		if reason != "" {
			q.Reason = reason
			break
		}
		v, _ := r.Cfg.ProfileFor(r.repo(t).Classes[t.Spec.PredictedClass], name)
		d := profileDecision{Name: name, Source: "scoper", Checks: q.Checks || v.Checks, TestFirst: q.TestFirst || v.TestFirst}
		if err := r.applyProfile(ctx, t, d); err != nil {
			return err
		}
		d.restore(&q)
	}
	*t.Status.Profile = q
	narrateLater(t, narrate.ProfileChosen(q.Name, q.Source, q.Reason, q.Why, len(t.Status.Runs)))
	if len(choices) > 0 {
		class, cohort, applied := t.Spec.PredictedClass, q.Cohort, q.Source == "scoper"
		rec := q.Recommended
		if rec != "" && !slices.ContainsFunc(choices, func(c ProfileChoice) bool { return c.Name == rec }) {
			rec = "other" // keeps the label bounded
		}
		record(ctx, func(ctx context.Context) { r.Metrics.ProfileRecommendation(ctx, class, cohort, rec, applied) })
	}
	return nil
}

// profileDecision is what applyProfile persists beside the spec it applies, under
// AnnotationProfileDecision, so a replay can restore the status half of the decision.
type profileDecision struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Reason    string `json:"reason,omitempty"`
	Checks    bool   `json:"checks,omitempty"`
	TestFirst bool   `json:"testFirst,omitempty"`
}

func (d profileDecision) restore(p *v1alpha1.ProfileStatus) {
	p.Name, p.Source, p.Reason, p.Checks, p.TestFirst = d.Name, d.Source, d.Reason, d.Checks, d.TestFirst
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

func (r *Reconciler) variantFor(ctx context.Context, t *v1alpha1.Task, name string) (string, string, error) {
	cl := r.repo(t).Classes[t.Spec.PredictedClass]
	if name == "" || name == cl.Harness {
		return "", "none", nil
	}
	if _, ok := r.Cfg.HarnessProfiles[name]; !ok {
		return "", "unknown", nil
	}
	v, ok := r.Cfg.ProfileFor(cl, name)
	if !ok {
		return "", "not_allowed", nil
	}
	if !r.Cfg.Applies() {
		return "", "observe", nil
	}
	if v.TestFirst {
		if t.Spec.DataClass != "public" {
			return "", "internal", nil
		}
		if paths, err := r.testPaths(ctx, t); err != nil || len(paths) == 0 {
			return "", "no_tests_paths", nil // an unreadable declaration declares no paths
		}
	}
	return name, "", nil
}

// applyProfile persists decision d's spec and d itself in one update; a replay restores the status
// from the persisted decision, never by deciding again.
func (r *Reconciler) applyProfile(ctx context.Context, t *v1alpha1.Task, d profileDecision) error {
	class := t.Spec.PredictedClass
	p, ok := r.Cfg.ProfileFor(r.repo(t).Classes[class], d.Name)
	if !ok {
		return fmt.Errorf("profile %q is not one of class %q's", d.Name, class)
	}
	before := t.Spec.DeepCopy()
	r.profileSpec(t, class, p)
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
func (r *Reconciler) resolveRetried(ctx context.Context, t *v1alpha1.Task) error {
	if !unresolved(t) || !scoperPlanned(t) || t.Status.Scope == nil || splitApproved(t) {
		return nil
	}
	sc, err := r.taskScope(ctx, t)
	switch {
	case err != nil:
		return err
	case sc == nil:
		return r.resolveProfile(ctx, t, nil)
	case t.Status.Reason == ReasonNeedsAuthor || sc.Assumed:
		return r.resolveProfile(ctx, t, assumed(sc.Scope))
	}
	return r.resolveProfile(ctx, t, &sc.Scope)
}

func resolveUnscoped(t *v1alpha1.Task) {
	if !unresolved(t) {
		return
	}
	p := t.Status.Profile
	p.Resolved = true
	narrateLater(t, narrate.ProfileChosen(p.Name, p.Source, p.Reason, "", len(t.Status.Runs)))
}
