// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// #182: a wait verdict past merge.awaitingCIFor hands the task to a human, naming what never
// reported; inside the bound, or with no bound (written 0s), it keeps waiting.
func TestAwaitingCIIsBounded(t *testing.T) {
	for name, c := range map[string]struct {
		bound, since  time.Duration
		phase, reason string
	}{
		"no bound, a week waiting": {0, 7 * 24 * time.Hour, v1alpha1.PhaseAwaitingCI, "holdout_pending"},
		"inside the bound":         {24 * time.Hour, 23 * time.Hour, v1alpha1.PhaseAwaitingCI, "holdout_pending"},
		"exactly at the bound":     {24 * time.Hour, 24 * time.Hour, v1alpha1.PhaseAwaitingHuman, "ci_wait_expired"},
		"past the bound":           {24 * time.Hour, 25 * time.Hour, v1alpha1.PhaseAwaitingHuman, "ci_wait_expired"},
	} {
		t.Run(name, func(t *testing.T) {
			g := declaredRig(t)
			g.r.Cfg.Current().Merge.AwaitingCIFor = &config.Duration{Duration: c.bound}
			g.f.SetChecks(12, green("SUCCESS")) // the holdout never reports
			g.setPhaseSince(t, "3buqdlot", now.Add(-c.since))
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase || tk.Status.Reason != c.reason {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			if c.reason == "ci_wait_expired" && !contains(g.f.Comments(12), "External links 🔗") {
				t.Fatalf("the hand-off names the check that never reported: %q", g.f.Comments(12))
			}
		})
	}
}

// ci_pending is a wait like any other: a required check that never reports is bounded too.
func TestAPendingRequiredCheckIsBounded(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Merge.AwaitingCIFor = &config.Duration{Duration: time.Hour}
	checks := green("SUCCESS")
	checks.Runs = checks.Runs[1:] // one required check missing
	g.f.SetChecks(12, checks)
	g.setPhaseSince(t, "3buqdlot", now.Add(-2*time.Hour))
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "ci_wait_expired" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// The hand-off is once: a later pass finds the task in AwaitingHuman and posts nothing more, and
// the comment says how long the wait was, on the reconciler's clock.
func TestTheExpiredWaitIsHandedOffOnce(t *testing.T) {
	g := declaredRig(t)
	g.r.Cfg.Current().Merge.AwaitingCIFor = &config.Duration{Duration: time.Hour}
	g.f.SetChecks(12, green("SUCCESS"))
	g.setPhaseSince(t, "3buqdlot", now.Add(-90*time.Minute))
	g.reconcile(t, "3buqdlot", 1)
	first := g.f.Comments(12)
	if !contains(first, "waited 1h30m0s for checks that had not reported as completed") {
		t.Fatalf("the comment says how long it waited: %q", first)
	}
	tk := g.reconcile(t, "3buqdlot", 2)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "ci_wait_expired" || len(g.f.Comments(12)) != len(first) {
		t.Fatalf("%s %s, %d comments then %d", tk.Status.Phase, tk.Status.Reason, len(first), len(g.f.Comments(12)))
	}
}

// A task with no PhaseSince (a legacy object) has no clock to bound: it keeps waiting.
func TestNoPhaseSinceIsNotBounded(t *testing.T) {
	g := declaredRig(t)
	g.r.Cfg.Current().Merge.AwaitingCIFor = &config.Duration{Duration: time.Hour}
	g.f.SetChecks(12, green("SUCCESS"))
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), keyFor("3buqdlot"), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Status.PhaseSince = nil
	if err := g.c.Status().Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "holdout_pending" {
		t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
	}
}

// A release-age status the adopted login posted as PENDING is working as designed (Renovate's
// stability days run for days): the awaitingCIFor bound does not hand the PR off as a check that
// never reported. It has its own, longer bound; a status never posted is still a check that never
// reported and expires at awaitingCIFor.
func TestAPendingReleaseAgeOutlastsTheCIBound(t *testing.T) {
	for name, c := range map[string]struct {
		edit          func(g *rig)
		since         time.Duration
		phase, reason string
	}{
		"pending, past the CI bound": {func(g *rig) { g.setStatus(30, ageCheck, "PENDING", bot) }, 25 * time.Hour,
			v1alpha1.PhaseAwaitingCI, "release_age_pending"},
		"pending, past its own bound": {func(g *rig) { g.setStatus(30, ageCheck, "PENDING", bot) }, releaseAgeWaitMax,
			v1alpha1.PhaseAwaitingHuman, "ci_wait_expired"},
		"never posted, past the CI bound": {func(g *rig) {
			c, _ := g.f.PullRequestChecks(t.Context(), 30)
			c.Statuses = c.Statuses[:1]
			g.f.SetChecks(30, c)
		}, 25 * time.Hour, v1alpha1.PhaseAwaitingHuman, "ci_wait_expired"},
	} {
		t.Run(name, func(t *testing.T) {
			g := adoptRig(t)
			g.r.Cfg.Current().Merge.AwaitingCIFor = &config.Duration{Duration: 24 * time.Hour}
			c.edit(g)
			g.setPhaseSince(t, adoptTask, now.Add(-c.since))
			tk := g.reconcile(t, adoptTask, 1)
			if tk.Status.Phase != c.phase || tk.Status.Reason != c.reason {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
		})
	}
}
