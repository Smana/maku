// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

func (m *fakeMetrics) ProfilePin(_ context.Context, class, profile, outcome, reason string) {
	m.add(fmt.Sprintf("pin %s %s %s %s", class, profile, outcome, reason))
}

func (m *fakeMetrics) ProfileAgreement(_ context.Context, class, outcome string) {
	m.add(fmt.Sprintf("agreement %s %s", class, outcome))
}

// pinRig is a review task at triage under cf, its issue carrying labels, each applied by actor.
func pinRig(t *testing.T, cf *config.Config, tk *v1alpha1.Task, actor string, labels ...string) *rig {
	t.Helper()
	g := newRig(t, tk)
	g.r.Cfg = cf
	g.r.Triage = triage.Classify{Forge: g.f}
	var evs []forge.LabelEvent
	for _, l := range labels {
		evs = append(evs, forge.LabelEvent{Actor: actor, Label: l, At: now})
	}
	g.f.SetEvents(tk.Spec.Issue, evs...)
	g.f.SetIssue(forge.Issue{Number: tk.Spec.Issue, Labels: labels})
	return g
}

func pinMetrics(g *rig) []string {
	return slices.DeleteFunc(slices.Clone(g.metrics.recorded), func(m string) bool { return !strings.HasPrefix(m, "pin ") })
}

// P9: an honoured pin runs its profile in observe and in apply alike: labeled, source label, the
// request recorded, its loop row applied, and no scoper choice, so the observe refusal never applies.
func TestPinHonouredInObserveAndApply(t *testing.T) {
	for _, mode := range []string{"observe", "apply"} {
		t.Run(mode, func(t *testing.T) {
			g := pinRig(t, resolveCfg(mode, 0), issueTask("3buqdlot", 7, "fix it"), "Smana", "profile:review-search")
			tk := g.reconcile(t, "3buqdlot", 1)
			want := v1alpha1.ProfileStatus{Name: "review-search", Source: "label", Cohort: triage.CohortLabeled, Checks: false,
				Requested: "review-search"}
			if p := tk.Status.Profile; p == nil || *p != want {
				t.Fatalf("%+v, want %+v", p, want)
			}
			if tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != 7 {
				t.Fatalf("the pinned profile's loop row: %+v", tk.Spec.Loop)
			}
			if got := g.r.profileChoices(g.r.Cfg.Current(), tk); got != nil {
				t.Fatalf("a pinned task was offered %+v", got)
			}
			sc := readyScope()
			sc.Profile, sc.ProfileWhy = "review-tested", "because"
			if err := g.r.resolveProfile(t.Context(), g.r.Cfg.Current(), tk, &sc); err != nil {
				t.Fatal(err)
			}
			if p := tk.Status.Profile; p.Name != "review-search" || p.Source != "label" || p.Reason != "" || p.Recommended != "" {
				t.Fatalf("resolved %+v", p)
			}
			if got := pinMetrics(g); !slices.Equal(got, []string{"pin review review-search honoured "}) {
				t.Fatalf("metrics %q", got)
			}
		})
	}
}

// P6: a pin triage refuses runs the task unpinned: the floor, its cohort by hash, the request and
// the code recorded, one narration line, and the count under its code.
func TestPinRefusalsAtTriage(t *testing.T) {
	for name, c := range map[string]struct {
		labels   []string
		actor    string
		internal bool
		decl     string
		class    string
		want     string
		metric   string
	}{
		"unknown":        {labels: []string{"profile:zzz"}, want: "profile_unknown", metric: "pin review other refused profile_unknown"},
		"not allowed":    {labels: []string{"profile:review-other"}, want: "profile_not_allowed", metric: "pin review review-other refused profile_not_allowed"},
		"internal":       {labels: []string{"profile:review-tested"}, internal: true, decl: testsDecl, want: "profile_internal", metric: "pin review review-tested refused profile_internal"},
		"no tests paths": {labels: []string{"profile:review-tested"}, want: "profile_no_tests_paths", metric: "pin review review-tested refused profile_no_tests_paths"},
		"not maintainer": {labels: []string{"profile:review-search"}, actor: "someone", want: "not_maintainer", metric: "pin review review-search refused not_maintainer"},
		"conflict":       {labels: []string{"profile:review-search", "profile:review-tested"}, want: "profile_conflict", metric: "pin review review-search refused profile_conflict"},
		"fixed":          {labels: []string{"class:docs-links", "profile:review-search"}, class: "docs-links", want: "class_profile_fixed", metric: "pin docs-links review-search refused class_profile_fixed"},
	} {
		t.Run(name, func(t *testing.T) {
			cf := resolveCfg("apply", 0)
			cf.Classes["docs-links"] = config.Class{Shadow: true, Harness: "review-floor"}
			actor := c.actor
			if actor == "" {
				actor = "Smana"
			}
			tk := issueTask("3buqdlot", 7, "fix it")
			if c.internal {
				tk.Spec.DataClass = "internal"
			}
			g := pinRig(t, cf, tk, actor, c.labels...)
			if c.decl != "" {
				g.f.SetMainFiles(map[string]string{".github/agent-checks.yaml": c.decl})
			}
			tk = g.reconcile(t, "3buqdlot", 1)
			p := tk.Status.Profile
			requested := strings.TrimPrefix(c.labels[len(c.labels)-1], "profile:")
			if name == "conflict" {
				requested = "review-search"
			}
			if p == nil || p.Name != "review-floor" || p.Source != "floor" || p.Cohort != triage.CohortTreatment ||
				p.Requested != requested || p.Refused != c.want {
				t.Fatalf("%+v, want refused %s", p, c.want)
			}
			if !contains(g.f.Comments(7), "Agent factory: `profile:"+requested+"` was not applied: ") {
				t.Fatalf("comments %q", g.f.Comments(7))
			}
			if got := pinMetrics(g); !slices.Equal(got, []string{c.metric}) {
				t.Fatalf("metrics %q, want %q", got, c.metric)
			}
		})
	}
}

// P5: the field's pin is the sponsor login's the factory resolved, honoured only for a maintainer.
func TestPinFieldFromSponsorLogin(t *testing.T) {
	for login, want := range map[string]string{"Smana": "", "someone": "not_maintainer"} {
		tk := issueTask("3buqdlot", 7, "fix it")
		tk.Spec.Intake = &v1alpha1.TaskIntake{Profile: "review-search", Login: login}
		g := pinRig(t, resolveCfg("observe", 0), tk, "Smana")
		got := g.reconcile(t, "3buqdlot", 1).Status.Profile
		if got == nil || got.Requested != "review-search" || got.Refused != want || (want == "") != (got.Source == "label") {
			t.Fatalf("%s: %+v", login, got)
		}
	}
}

// #455: a failed scoper leaves a pinned task under its pin: the label case comes first.
func TestFailedScoperKeepsPin(t *testing.T) {
	g := pinRig(t, resolveCfg("apply", 0), issueTask("3buqdlot", 7, "fix it"), "Smana", "profile:review-search")
	tk := g.reconcile(t, "3buqdlot", 1)
	if err := g.r.resolveProfile(t.Context(), g.r.Cfg.Current(), tk, nil); err != nil {
		t.Fatal(err)
	}
	if p := tk.Status.Profile; p.Name != "review-search" || p.Source != "label" || p.Reason != "" || !p.Resolved {
		t.Fatalf("%+v", p)
	}
}
