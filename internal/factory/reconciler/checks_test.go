// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/runwatch"
)

// v3 feature 3 (RC8, offline proof 7), WF7: a task whose workflow switches checks on has its public
// implementer, tester and reviewer claims carry spec.loop.checks and the repository's egress; every
// other claim is today's. A task without a workflow reads its repository's default's.
func TestASwitchedOnClassChecksItsPublicImplementerAndReviewer(t *testing.T) {
	g := newRig(t)
	repo := g.r.Cfg.Current().Repos()[0]
	repo.Checks = config.Checks{Egress: []string{"golang", "pypi"}}
	g.r.Cfg.Current().Repositories = []config.Repository{repo}
	g.r.Cfg.Current().HarnessProfiles = map[string]config.HarnessProfile{"checked": {Checks: true}, "standard": {}}
	task := func(workflow, dataClass string) *v1alpha1.Task {
		tk := issueTask("3buqdlot", 7, "fix")
		tk.Spec.PredictedClass, tk.Spec.DataClass = "review", dataClass
		if workflow != "" {
			tk.Status.Profile = &v1alpha1.ProfileStatus{Name: workflow, Checks: g.r.Cfg.Current().HarnessProfiles[workflow].Checks}
		}
		tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12"}
		return tk
	}
	on := []runs.Spec{g.r.implementerSpec(g.r.Cfg.Current(), task("checked", "public"), "brief"), g.r.verifierSpec(g.r.Cfg.Current(), task("checked", "public"), "reviewer"),
		g.r.testerSpec(g.r.Cfg.Current(), task("checked", "public"), "brief")}
	for _, s := range on {
		if !s.Checks || !reflect.DeepEqual(s.EgressProfiles, []string{"golang", "pypi"}) {
			t.Fatalf("%s: checks %v egress %v, want on with [golang pypi]", s.Role, s.Checks, s.EgressProfiles)
		}
	}
	off := []runs.Spec{g.r.testerSpec(g.r.Cfg.Current(), task("checked", "internal"), "brief"), g.r.implementerSpec(g.r.Cfg.Current(), task("checked", "internal"), "brief"),
		g.r.implementerSpec(g.r.Cfg.Current(), task("standard", "public"), "brief"), g.r.implementerSpec(g.r.Cfg.Current(), task("", "public"), "brief")}
	for _, s := range off {
		u := runs.Build(runwatch.DefaultKind, s)
		for _, key := range []string{"loop", "egress"} {
			if _, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", key); found {
				t.Fatalf("%s %s: spec.%s written on a run without checks", s.Role, s.DataClass, key)
			}
		}
	}
}

// Checks ride beside the task's loop policy: one spec.loop holds both, and a checks-only claim
// (no loop policy, or a reviewer) writes only checks.
func TestChecksJoinTheLoopPolicyInOneSpecLoop(t *testing.T) {
	s := runs.Spec{Checks: true, Loop: &runs.Loop{StallSteps: 80, Continuations: 2}}
	loop, _, _ := unstructured.NestedMap(runs.Build(runwatch.DefaultKind, s).Object, "spec", "loop")
	if loop["checks"] != true || loop["stallSteps"] != int64(80) || loop["continuations"] != int64(2) {
		t.Fatalf("loop %v", loop)
	}
	loop, _, _ = unstructured.NestedMap(runs.Build(runwatch.DefaultKind, runs.Spec{Checks: true}).Object, "spec", "loop")
	if !reflect.DeepEqual(loop, map[string]any{"checks": true}) {
		t.Fatalf("checks-only loop %v", loop)
	}
}

// Developer review gap 2: the implementer, tester and reviewer take the repository's runtime; a
// triager stays the harness image, small.
func TestTheRepositorysRuntimeReachesItsRuns(t *testing.T) {
	g := newRig(t)
	image := "ghcr.io/acme/maku-harness-jvm@sha256:" + strings.Repeat("d", 64)
	g.r.Cfg.Current().Repositories[0].Runtime = config.Runtime{Image: image, Size: "large", Services: []config.Service{"postgres"}}
	tk := issueTask("3buqdlot", 7, "fix")
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12"}
	cfg := g.r.Cfg.Current()
	for _, s := range []runs.Spec{g.r.implementerSpec(cfg, tk, "brief"), g.r.testerSpec(cfg, tk, "brief"), g.r.verifierSpec(cfg, tk, "reviewer")} {
		if s.Image != image || s.Size != "large" || len(s.Services) != 1 || s.Services[0] != "postgres" {
			t.Errorf("%s: %q %q", s.Role, s.Image, s.Size)
		}
	}
}
