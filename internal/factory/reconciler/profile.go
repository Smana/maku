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

// resolveProfile settles t's profile once its scoper ends (TE), before the task is queued: the
// scoper's recommendation when the treatment cohort applies it and it fits, else the floor with
// the reason. s nil is a failed scoper. A labelled task keeps its profile; a split child is left as
// it is. The count waits for the status write, so a replay counts once.
func (r *Reconciler) resolveProfile(ctx context.Context, t *v1alpha1.Task, s *envelope.Scope) error {
	p := t.Status.Profile
	if p == nil || p.Resolved || t.Spec.Parent != nil {
		return nil
	}
	choices := r.profileChoices(t)
	p.Resolved = true
	if s != nil && len(choices) > 0 {
		p.Recommended, p.Why = s.Profile, s.ProfileWhy
	}
	switch {
	case p.Source == "label":
	case s == nil:
		p.Reason = "scoper_failed"
	case p.Source == "control":
	default:
		name, reason, err := r.variantFor(ctx, t, s.Profile)
		if err != nil {
			return err
		}
		if reason != "" {
			p.Reason = reason
			break
		}
		v, _ := r.Cfg.ProfileFor(r.repo(t).Classes[t.Spec.PredictedClass], name)
		if err := r.applyProfile(ctx, t, name); err != nil {
			return err
		}
		p.Name, p.Source, p.Reason = name, "scoper", ""
		p.Checks, p.TestFirst = p.Checks || v.Checks, p.TestFirst || v.TestFirst
	}
	narrateLater(t, narrate.ProfileChosen(p.Name, p.Source, p.Reason, p.Why, len(t.Status.Runs)))
	if len(choices) > 0 {
		class, cohort, applied := t.Spec.PredictedClass, p.Cohort, p.Source == "scoper"
		rec := p.Recommended
		if rec != "" && !slices.ContainsFunc(choices, func(c ProfileChoice) bool { return c.Name == rec }) {
			rec = "other" // a name the scoper made up never becomes a label value
		}
		record(ctx, func(ctx context.Context) { r.Metrics.ProfileRecommendation(ctx, class, cohort, rec, applied) })
	}
	return nil
}

// variantFor is the variant a treatment task takes on the scoper's recommendation name, or the
// reason it keeps the floor.
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
		if t.Spec.DataClass != "public" { // the tester runs on public tasks only (testerDue)
			return "", "internal", nil
		}
		if paths, err := r.testPaths(ctx, t); err != nil || len(paths) == 0 {
			return "", "no_tests_paths", nil // an unreadable declaration declares no paths
		}
	}
	return name, "", nil
}

// applyProfile writes profile name's spec onto t as received does, keeping t's local status and
// its scope mode; an unchanged spec is not written, so a replayed step writes nothing.
func (r *Reconciler) applyProfile(ctx context.Context, t *v1alpha1.Task, name string) error {
	class := t.Spec.PredictedClass
	p, ok := r.Cfg.ProfileFor(r.repo(t).Classes[class], name)
	if !ok {
		return fmt.Errorf("profile %q is not one of class %q's", name, class)
	}
	before := t.Spec.DeepCopy()
	r.profileSpec(t, class, p)
	// The scoper has run under triage's mode: a variant's row changing it would read the scoper's
	// run as a triager's proposal on replay and drop the scope from the implementer's brief.
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
