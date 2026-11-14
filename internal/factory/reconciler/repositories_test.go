// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

const (
	repoA = "Smana/cloud-native-ref"
	repoB = "Smana/ai-native-ref"
)

// listRepos lists c's legacy settings once per name, as a config serving each of them would: the
// rig's repository first. A task is served only on a listed repository (factory resources D12).
func listRepos(c *config.Config, names ...string) {
	one := config.Repository{Maintainers: c.Repositories[0].Maintainers, TriggerLabel: c.Repositories[0].TriggerLabel, Classes: c.Repositories[0].Classes, Merge: c.Repositories[0].Merge, Schedules: c.Repositories[0].Schedules}
	rs := []config.Repository{}
	for _, n := range append([]string{c.Repositories[0].Name}, names...) {
		r := one
		r.Name = n
		rs = append(rs, r)
	}
	c.Repositories = rs
}

// twoRepos lists A and B with the rig's settings, and gives each its own GitHub (v2 D8).
func twoRepos(g *rig) (a, b *forgetest.Fake) {
	c := g.r.Cfg.Current()
	one := config.Repository{Maintainers: c.Repositories[0].Maintainers, Classes: c.Repositories[0].Classes, Merge: c.Repositories[0].Merge}
	ra, rb := one, one
	ra.Name, rb.Name = repoA, repoB
	c.Repositories = []config.Repository{ra, rb}
	a, b = forgetest.NewFake(), forgetest.NewFake()
	a.Now, b.Now = g.f.Now, g.f.Now
	g.r.Forge, g.r.Merger = nil, nil
	g.r.Clients = func(repository string) (TaskForge, forge.Merger, bool) {
		switch repository {
		case repoA:
			return a, a, true
		case repoB:
			return b, b, true
		}
		return nil, nil, false
	}
	return a, b
}

func taskOn(name, repo string) *v1alpha1.Task {
	t := issueTask(name, 7, "x")
	t.Spec.Repository = repo
	return t
}

// A task of B is narrated on B, never on A's issue of the same number.
func TestATaskSpeaksOnItsRepositoryOnly(t *testing.T) {
	g := newRig(t, taskOn("3buqdlot", repoB))
	a, b := twoRepos(g)
	tk := g.reconcile(t, "3buqdlot", 3)
	tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
	if err := g.c.Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, "3buqdlot", 2)
	if len(b.Comments(7)) == 0 || len(a.Comments(7)) != 0 {
		t.Fatalf("B's issue %d comments, A's %d", len(b.Comments(7)), len(a.Comments(7)))
	}
}

// A task on a repository the config does not list is not served: it requeues later without an
// error, and no client acts for it.
func TestATaskOnAnUnlistedRepositoryActsNowhere(t *testing.T) {
	g := newRig(t, taskOn("3buqdlot", "Smana/elsewhere"))
	a, b := twoRepos(g)
	if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != notServedRequeue {
		t.Fatalf("a task not served: %+v %v", res, err)
	}
	if len(a.Comments(7))+len(b.Comments(7)) != 0 {
		t.Fatal("a client acted for an unlisted repository")
	}
}

// The daily auto-merge cap and the breaker count the task's repository only: B's merges spend
// none of A's day and revert none of A's classes.
func TestTheMergeCapAndBreakerArePerRepository(t *testing.T) {
	armed := func(name, repo string, n int, reverted bool) *v1alpha1.Task {
		tk := taskOn(name, repo)
		tk.Spec.PredictedClass = "docs-links"
		at := metav1.NewTime(now)
		tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: n, ArmedAt: &at, MergedAt: &at}
		if reverted {
			tk.Status.Phase = v1alpha1.PhaseReverted
		}
		return tk
	}
	g := newRig(t, armed("aaaaaaaa", repoA, 1, true), armed("bbbbbbbb", repoA, 2, false), armed("cccccccc", repoB, 3, false))
	twoRepos(g)
	ctx := context.Background()
	for repo, want := range map[string]int{repoA: 2, repoB: 1} {
		bctx, err := g.r.bind(ctx, taskOn("x", repo))
		if err != nil {
			t.Fatal(err)
		}
		n, err := g.r.armedToday(bctx, g.r.Cfg.Current(), g.r.Cfg.Current().RepositoryFor(repo))
		if err != nil || n != want {
			t.Errorf("%s armed today: %d %v, want %d", repo, n, err, want)
		}
		paused, err := g.r.paused(bctx, g.r.Cfg.Current(), g.r.Cfg.Current().RepositoryFor(repo), "docs-links")
		if err != nil || paused != (repo == repoA) {
			t.Errorf("%s paused %v %v: A's revert must not demote B's class", repo, paused, err)
		}
	}
}

// A publish that drops B (2→1, factory resources D12) leaves B's tasks standing down: B is never
// decided under A's settings, even while B's connection is still wired. The task requeues later
// with no error, and nothing arms, merges or speaks on either repository.
func TestARemovedRepositoryIsNotDecidedUnderTheRemainingOne(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.f.SetChecks(12, green("SUCCESS"))
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), keyFor("3buqdlot"), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Spec.Repository = repoB
	if err := g.c.Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	two := g.r.Cfg.Current()
	one := config.Repository{Maintainers: two.Repositories[0].Maintainers, Classes: two.Repositories[0].Classes, Merge: two.Repositories[0].Merge}
	ra, rb := one, one
	ra.Name, rb.Name = repoA, repoB
	rb.Classes = map[string]config.Class{} // B never armed docs-links: only A's settings would
	two.Repositories = []config.Repository{ra, rb}
	two.Hash = "two"
	live := config.NewLive(two)
	g.r.Cfg = live
	g.r.Forge, g.r.Merger = nil, nil
	g.r.Clients = func(string) (TaskForge, forge.Merger, bool) { return g.f, g.f, true } // B's connection not yet dropped
	next := *two
	next.Repositories, next.Hash = []config.Repository{ra}, "one"
	live.Publish(&next)

	before := g.f.Comments(7)
	res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	if err != nil || res.RequeueAfter != notServedRequeue {
		t.Fatalf("a task of a repository not served requeues later without error: %+v %v", res, err)
	}
	got := g.reconcile(t, "3buqdlot", 0)
	if len(g.f.Armed()) != 0 || got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.PullRequest.ArmedAt != nil ||
		len(g.f.Comments(7)) != len(before) {
		t.Fatalf("B was decided under A's classes: %s %s armed %v", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}
	// The pure decision stands down too: no class of A's is read for B.
	in := ArmInputs{Cfg: live.Current(), Task: got}
	if d := DecideArm(in); d.Verdict != "wait" || d.Reason != reasonRepositoryNotServed {
		t.Fatalf("decided %s %s for a repository not served", d.Verdict, d.Reason)
	}
}

// A task deleted while its repository is unserved is forgotten, so the set does not grow.
func TestADeletedUnservedTaskIsForgotten(t *testing.T) {
	g := newRig(t, taskOn("3buqdlot", "Smana/elsewhere"))
	twoRepos(g)
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatal(err)
	}
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), reqFor("3buqdlot").NamespacedName, &tk); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Delete(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatal(err)
	}
	if !g.r.notServed.once("3buqdlot") {
		t.Fatal("a deleted task is still remembered as not served")
	}
}
