// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
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
	switch {
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
		if err := r.applyProfile(ctx, t, name); err != nil {
			return err
		}
		q.Name, q.Source, q.Reason = name, "scoper", ""
		q.Checks, q.TestFirst = q.Checks || v.Checks, q.TestFirst || v.TestFirst
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

// applyProfile persists profile name's spec, skipping an unchanged one so a replay writes nothing.
func (r *Reconciler) applyProfile(ctx context.Context, t *v1alpha1.Task, name string) error {
	class := t.Spec.PredictedClass
	p, ok := r.Cfg.ProfileFor(r.repo(t).Classes[class], name)
	if !ok {
		return fmt.Errorf("profile %q is not one of class %q's", name, class)
	}
	before := t.Spec.DeepCopy()
	r.profileSpec(t, class, p)
	// A changed mode would read the finished scoper's run as a triager proposal on replay.
	t.Spec.ScopeMode = before.ScopeMode
	if equality.Semantic.DeepEqual(*before, t.Spec) {
		return nil
	}
	status := t.Status // Update returns the server's status into t; keep ours
	if err := r.Client.Update(ctx, t); err != nil {
		return err
	}
	t.Status = status
	return nil
}

// resolveRetried runs before the retry changes any state, so a broker error replays the retry.
func (r *Reconciler) resolveRetried(ctx context.Context, t *v1alpha1.Task) error {
	if !unresolved(t) || !scoperPlanned(t) || t.Status.Scope == nil || (t.Status.Split != nil && t.Status.Split.State == SplitApproved) {
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
