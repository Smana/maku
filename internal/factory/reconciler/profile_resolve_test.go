// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/triage"
)

const testsDecl = "tests:\n  paths: [tests/]\n"

// resolveCfg is profileCfg with two variants the review class allows, one it does not, and mode.
func resolveCfg(mode string, percent int) *config.Config {
	c := profileCfg(true)
	steps := int32(7)
	c.Loops["review-search"] = config.Loop{StallSteps: &steps, Search: true}
	c.HarnessProfiles["review-search"] = config.HarnessProfile{Loop: "review-search", Description: "search first"}
	c.HarnessProfiles["review-tested"] = config.HarnessProfile{TestFirst: true, Description: "tests first"}
	c.HarnessProfiles["review-other"] = config.HarnessProfile{Description: "not allowed"}
	c.Classes["review"] = config.Class{Harness: "review-floor", HarnessAllowed: []string{"review-search", "review-tested"}}
	c.Evaluation = config.Evaluation{Mode: mode, ControlPercent: percent}
	return c
}

// triagedRig is a review task past triage under cf, its checks declaration decl.
func triagedRig(t *testing.T, cf *config.Config, label, decl string) (*rig, *v1alpha1.Task) {
	t.Helper()
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it"), label)
	if decl != "" {
		g.f.SetMainFiles(map[string]string{".github/agent-checks.yaml": decl})
	}
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Profile == nil {
		t.Fatal("no profile at triage")
	}
	return g, tk
}

func TestResolutionTable(t *testing.T) {
	type want struct {
		name, source, reason, recommended string
		steps                             int32
		testFirst                         bool
	}
	floor := func(source, reason, rec string) want { return want{"review-floor", source, reason, rec, 40, false} }
	for name, c := range map[string]struct {
		mode, label, profile, decl string
		percent                    int
		internal, failed           bool
		want                       want
	}{
		"applied":         {mode: "apply", profile: "review-search", want: want{"review-search", "scoper", "", "review-search", 7, false}},
		"applied tests":   {mode: "apply", profile: "review-tested", decl: testsDecl, want: want{"review-tested", "scoper", "", "review-tested", 40, true}},
		"absent":          {mode: "apply", want: floor("floor", "none", "")},
		"the floor":       {mode: "apply", profile: "review-floor", want: floor("floor", "none", "review-floor")},
		"unknown":         {mode: "apply", profile: "zzz", want: floor("floor", "unknown", "zzz")},
		"not allowed":     {mode: "apply", profile: "review-other", want: floor("floor", "not_allowed", "review-other")},
		"internal":        {mode: "apply", profile: "review-tested", decl: testsDecl, internal: true, want: floor("floor", "internal", "review-tested")},
		"no tests paths":  {mode: "apply", profile: "review-tested", want: floor("floor", "no_tests_paths", "review-tested")},
		"observe":         {mode: "observe", profile: "review-search", want: floor("floor", "observe", "review-search")},
		"control":         {mode: "apply", percent: 100, profile: "review-search", want: floor("control", "", "review-search")},
		"labelled":        {mode: "apply", label: "class:review", profile: "review-search", want: floor("label", "", "")},
		"scoper failed":   {mode: "apply", failed: true, want: floor("floor", "scoper_failed", "")},
		"control, failed": {mode: "apply", percent: 100, failed: true, want: floor("control", "scoper_failed", "")},
	} {
		t.Run(name, func(t *testing.T) {
			g, tk := triagedRig(t, resolveCfg(c.mode, c.percent), c.label, c.decl)
			if c.internal {
				tk.Spec.DataClass = "internal"
			}
			tier := tk.Spec.Budget.Tier
			var sc *envelope.Scope
			if !c.failed {
				s := readyScope()
				s.Profile = c.profile
				if c.profile != "" {
					s.ProfileWhy = "because"
				}
				sc = &s
			}
			if err := g.r.resolveProfile(t.Context(), tk, sc); err != nil {
				t.Fatal(err)
			}
			p := tk.Status.Profile
			if p == nil || !p.Resolved || p.Name != c.want.name || p.Source != c.want.source || p.Reason != c.want.reason ||
				p.Recommended != c.want.recommended || p.TestFirst != c.want.testFirst || !p.Checks {
				t.Fatalf("profile %+v, want %+v", p, c.want)
			}
			if c.want.recommended != "" && p.Why != "because" {
				t.Fatalf("why %q", p.Why)
			}
			if tk.Spec.Template != "pair" || tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != c.want.steps || tk.Spec.Budget.Tier != tier {
				t.Fatalf("spec %s %+v %s", tk.Spec.Template, tk.Spec.Loop, tk.Spec.Budget.Tier)
			}
			if !slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool {
				return o.Key == "profile-chosen-0" && strings.Contains(o.Body, c.want.name)
			}) {
				t.Fatalf("outbox %+v", tk.Status.Outbox)
			}
			counted := slices.ContainsFunc(g.metrics.recorded, func(m string) bool { return strings.HasPrefix(m, "profile ") })
			if counted == (c.label != "") {
				t.Fatalf("metrics %v", g.metrics.recorded)
			}
		})
	}
}

// The step that resolves fails its status write: the replay writes the spec once, queues one line
// and counts once.
func TestResolutionReplaysOnce(t *testing.T) {
	cf := resolveCfg("apply", 0)
	cf.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\nthe link resolves"), "")
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	if tk := g.reconcile(t, "3buqdlot", 3); len(tk.Status.Runs) != 1 {
		t.Fatalf("%s %s %+v %q", tk.Status.Phase, tk.Spec.ScopeMode, tk.Status.Runs, tk.Status.Reason)
	}
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-search", "because"
	scopeHandoff(g, 0, sc)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	updates, failed := 0, false
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			updates++
			return cl.Update(ctx, o, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if tk, ok := o.(*v1alpha1.Task); ok && tk.Status.Profile != nil && tk.Status.Profile.Resolved && !failed {
				failed = true
				return errors.New("status write refused")
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}})
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil || !failed {
		t.Fatalf("the status write should fail: %v", err)
	}
	if n := countProfile(g.metrics.recorded); n != 0 {
		t.Fatalf("counted before the write: %v", g.metrics.recorded)
	}
	tk := g.reconcile(t, "3buqdlot", 1)
	if p := tk.Status.Profile; p == nil || p.Name != "review-search" || !p.Resolved || tk.Status.Phase != v1alpha1.PhaseQueued ||
		tk.Status.Scope == nil || tk.Status.Scope.State != "ready" || tk.Spec.ScopeMode != string(config.ScopeScoper) {
		t.Fatalf("%s %q %+v %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.Runs, p)
	}
	keys := 0
	for _, o := range tk.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-chosen-") {
			keys++
		}
	}
	if updates != 1 || keys+len(slices.DeleteFunc(slices.Clone(tk.Status.Narrated), func(k string) bool { return !strings.HasPrefix(k, "profile-chosen-") })) != 1 ||
		countProfile(g.metrics.recorded) != 1 {
		t.Fatalf("updates %d, outbox %+v narrated %v, metrics %v", updates, tk.Status.Outbox, tk.Status.Narrated, g.metrics.recorded)
	}
}

func countProfile(ms []string) int {
	n := 0
	for _, m := range ms {
		if strings.HasPrefix(m, "profile ") {
			n++
		}
	}
	return n
}

// A profile frozen at resolution holds when config drops it: checks and the tester as frozen.
func TestADeletedProfileKeepsTheFrozenOne(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", testsDecl)
	sc := readyScope()
	sc.Profile = "review-tested"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	delete(cf.HarnessProfiles, "review-tested")
	cf.Classes["review"] = config.Class{Harness: "review-floor"}
	if !g.r.testFirst(tk) || !g.r.implementerSpec(tk, "brief").Checks || !g.r.testerDue(tk) {
		t.Fatalf("testFirst %v checks %v", g.r.testFirst(tk), g.r.implementerSpec(tk, "brief").Checks)
	}
}

func TestResolutionNeverLowersTheTier(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", "")
	tk.Spec.Budget = triage.BudgetFor(cf, "frontier")
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	if tk.Status.Profile.Name != "review-search" || tk.Spec.Budget.Tier != "frontier" {
		t.Fatalf("%+v %s", tk.Status.Profile, tk.Spec.Budget.Tier)
	}
}

func TestASplitChildKeepsToday(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", "")
	tk.Spec.Parent = &v1alpha1.Parent{Task: "parent1"}
	before := *tk.Status.Profile
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	if *tk.Status.Profile != before || len(tk.Status.Outbox) != 0 || countProfile(g.metrics.recorded) != 0 {
		t.Fatalf("%+v %+v", tk.Status.Profile, tk.Status.Outbox)
	}
}
