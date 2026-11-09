// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"reflect"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// profileCfg is the rig's config with a review loop row and a review-floor profile; harness says
// whether the review class names it.
func profileCfg(harness bool) *config.Config {
	c := cfg()
	steps := int32(40)
	c.Loops = map[string]config.Loop{"review": {StallSteps: &steps, Search: true, Scope: config.ScopeScoper}}
	c.HarnessProfiles = map[string]config.HarnessProfile{"review-floor": {Template: "pair", Checks: true}}
	review := config.Class{}
	if harness {
		review.Harness = "review-floor"
	}
	c.Templates["investigate"] = config.Template{Roles: []string{"triager"}}
	c.Classes = map[string]config.Class{"docs-links": {Shadow: true}, "review": review, "tests": {}}
	for name, usd := range map[string][2]float64{"standard": {2.55, 5.10}, "frontier": {6.80, 13.60}} {
		tr := c.Tiers[name]
		tr.RunUSD, tr.TaskUSD = usd[0], usd[1]
		c.Tiers[name] = tr
	}
	return c
}

// apiTasks are an internal principal's and a sponsored principal's api tasks.
func apiTasks(t *testing.T, c *config.Config) (internal, sponsored *v1alpha1.Task) {
	t.Helper()
	lc := sponsoredConfig(t)
	c.API, c.Budgets = lc.API, lc.Budgets
	c.API.InternalPrincipals = []config.InternalPrincipal{{Sponsored: config.Sponsored{Name: "runlore", Sponsor: "smana"}, DailyTasks: 5}}
	sponsored = issueTask("3buqdlot", 0, "Fix the dead link in docs/a.md.")
	sponsor(sponsored, 2_000_000)
	internal = sponsored.DeepCopy()
	internal.Spec.Source.Ref, internal.Spec.Source.RequestedBy = "api:runlore", c.API.InternalPrincipals[0].Principal()
	internal.Spec.DataClass = "internal"
	return internal, sponsored
}

// R9: a task routed to investigate by its source never takes part in the evaluation.
func TestInvestigateTasksSkipTheEvaluation(t *testing.T) {
	cf := profileCfg(true)
	internal, _ := apiTasks(t, cf)
	runlore := issueTask("3buqdlot", 7, "a finding")
	runlore.Spec.Source.Kind, runlore.Spec.DataClass = "runlore", "internal"
	for name, tk := range map[string]*v1alpha1.Task{"runlore": runlore, "internal api": internal} {
		t.Run(name, func(t *testing.T) {
			g := profileRig(t, cf, tk.DeepCopy(), "")
			want := mainSpec(t, g, tk)
			got := g.reconcile(t, "3buqdlot", 1)
			if got.Status.Phase != v1alpha1.PhaseTriaged || got.Status.Profile != nil || got.Spec.Template != "investigate" ||
				got.Spec.ScopeMode != string(config.ScopeOff) || !reflect.DeepEqual(got.Spec, want) {
				t.Fatalf("%s %+v %+v", got.Status.Phase, got.Status.Profile, got.Spec)
			}
		})
	}
}

func profileRig(t *testing.T, c *config.Config, tk *v1alpha1.Task, label string) *rig {
	t.Helper()
	g := newRig(t, tk)
	g.r.Cfg = c
	g.r.Triage = triage.Classify{Cfg: c, Forge: g.f}
	if tk.Spec.Issue != 0 {
		var labels []string
		if label != "" {
			labels = []string{label}
			g.f.SetEvents(tk.Spec.Issue, forge.LabelEvent{Actor: "Smana", Label: label, At: now})
		}
		g.f.SetIssue(forge.Issue{Number: tk.Spec.Issue, Labels: labels})
	}
	return g
}

func TestTriageFreezesTheFloor(t *testing.T) {
	for name, c := range map[string]struct {
		label   string
		percent int
		want    v1alpha1.ProfileStatus
	}{
		"unlabelled":    {"", 0, v1alpha1.ProfileStatus{Name: "review-floor", Source: "floor", Cohort: "treatment", Checks: true}},
		"labelled":      {"class:review", 100, v1alpha1.ProfileStatus{Name: "review-floor", Source: "label", Cohort: "labeled", Checks: true}},
		"in the cohort": {"", 100, v1alpha1.ProfileStatus{Name: "review-floor", Source: "control", Cohort: "control", Checks: true}},
	} {
		t.Run(name, func(t *testing.T) {
			cf := profileCfg(true)
			cf.Evaluation.ControlPercent = c.percent
			g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it"), c.label)
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Profile == nil || *tk.Status.Profile != c.want {
				t.Fatalf("%+v", tk.Status.Profile)
			}
			if tk.Spec.Template != "pair" || tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != 40 || !tk.Spec.Search ||
				tk.Spec.ScopeMode != string(config.ScopeScoper) {
				t.Fatalf("the floor and the review row: %+v", tk.Spec)
			}
		})
	}
}

// mainSpec is what received wrote before profiles: the triage decision and the class's own row.
func mainSpec(t *testing.T, g *rig, tk *v1alpha1.Task) v1alpha1.TaskSpec {
	t.Helper()
	tk = tk.DeepCopy()
	d, err := g.r.Triage.Triage(t.Context(), tk)
	if err != nil {
		t.Fatal(err)
	}
	s := tk.Spec
	s.Template, s.PredictedClass, s.DataClass = d.Template, d.PredictedClass, d.DataClass
	s.Budget = triage.Ceiling(d.Budget, s.MaxMicroUSD)
	if p := g.r.Cfg.LoopFor(d.PredictedClass); p != nil {
		judge := p.JudgeModel
		if d.DataClass != "public" && config.PublicOnly(judge) {
			judge = config.DefaultJudge
		}
		s.Loop = &v1alpha1.Loop{StallSteps: p.StallSteps, Continuations: p.Continuations, JudgeModel: judge}
	}
	s.Search = g.r.Cfg.SearchFor(d.PredictedClass)
	mode := g.r.Cfg.ScopeFor(d.PredictedClass)
	if g.r.Cfg.Investigates(s.Template) {
		mode = config.ScopeOff
	}
	s.ScopeMode, s.Steering = string(mode), nil
	tk.Spec = s
	g.r.splitAtTriage(tk, d.Classification)
	return tk.Spec
}

// Off is today: a class without a harness gets main's spec and no status.profile, whatever the intake.
func TestNoHarnessIsToday(t *testing.T) {
	schedule := issueTask("3buqdlot", 0, "add tests")
	schedule.Spec.Source.Kind, schedule.Spec.PredictedClass = "schedule", "tests"
	runlore := issueTask("3buqdlot", 7, "a finding")
	runlore.Spec.Source.Kind, runlore.Spec.DataClass = "runlore", "internal"
	origin := issueTask("3buqdlot", 7, "follow-up")
	origin.Spec.Origin, origin.Spec.PredictedClass = "parent1", "tests"
	sponsored := issueTask("3buqdlot", 7, "fix it")
	sponsored.Spec.MaxMicroUSD = 2_000_000
	internal, sponsoredAPI := apiTasks(t, &config.Config{})
	for name, c := range map[string]struct {
		tk    *v1alpha1.Task
		label string
	}{
		"internal api":    {internal, ""},
		"sponsored api":   {sponsoredAPI, ""},
		"issue":           {issueTask("3buqdlot", 7, "fix it"), ""},
		"labelled issue":  {issueTask("3buqdlot", 7, "fix it"), "class:review"},
		"schedule":        {schedule, ""},
		"runlore":         {runlore, ""},
		"origin":          {origin, ""},
		"sponsored issue": {sponsored, ""},
	} {
		t.Run(name, func(t *testing.T) {
			cf := profileCfg(false)
			cf.Evaluation.ControlPercent = 100
			apiTasks(t, cf)
			g := profileRig(t, cf, c.tk.DeepCopy(), c.label)
			want := mainSpec(t, g, c.tk)
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != v1alpha1.PhaseTriaged || tk.Status.Profile != nil {
				t.Fatalf("triaged, no harness, no profile: %s %+v", tk.Status.Phase, tk.Status.Profile)
			}
			if !reflect.DeepEqual(tk.Spec, want) {
				t.Fatalf("spec\n got %+v\nwant %+v", tk.Spec, want)
			}
		})
	}
}

// profileSpec takes the profile's row, raises the tier only above the current one, and keeps a
// sponsored ceiling through a raise.
func TestProfileSpec(t *testing.T) {
	cf := profileCfg(true)
	steps := int32(7)
	cf.Loops["review-search"] = config.Loop{StallSteps: &steps, Search: true}
	for name, c := range map[string]struct {
		p           config.HarnessProfile
		tier        string
		maxMicroUSD int64
		wantTier    string
		wantTask    int64
	}{
		"own row":        {config.HarnessProfile{Loop: "review-search"}, "standard", 0, "standard", 5_100_000},
		"tier above":     {config.HarnessProfile{Tier: "frontier"}, "standard", 0, "frontier", 13_600_000},
		"tier equal":     {config.HarnessProfile{Tier: "standard"}, "standard", 0, "standard", 5_100_000},
		"tier below":     {config.HarnessProfile{Tier: "standard"}, "frontier", 0, "frontier", 13_600_000},
		"ceiling raised": {config.HarnessProfile{Tier: "frontier"}, "standard", 2_000_000, "frontier", 2_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.r.Cfg = cf
			tk := issueTask("3buqdlot", 7, "fix it")
			tk.Spec.MaxMicroUSD = c.maxMicroUSD
			tk.Spec.Budget = triage.Ceiling(triage.BudgetFor(cf, c.tier), c.maxMicroUSD)
			g.r.profileSpec(tk, "review", c.p)
			if b := tk.Spec.Budget; b.Tier != c.wantTier || b.TaskMicroUSD != c.wantTask {
				t.Fatalf("budget %+v", b)
			}
			wantSteps, wantScope := int32(40), string(config.ScopeScoper)
			if c.p.Loop != "" {
				wantSteps, wantScope = 7, string(config.ScopeOff)
			}
			if tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != wantSteps || tk.Spec.ScopeMode != wantScope || !tk.Spec.Search {
				t.Fatalf("row: %+v %s", tk.Spec.Loop, tk.Spec.ScopeMode)
			}
		})
	}
}

// D5: a run reads checks and testFirst frozen on status.profile, not the class's live config; a
// task without a profile reads the config as today.
func TestRunsReadTheFrozenProfile(t *testing.T) {
	g := newRig(t)
	repo := g.r.Cfg.Repos()[0]
	repo.Classes = map[string]config.Class{"docs-links": {Shadow: true}, "docs": {}}
	repo.Checks = config.Checks{Egress: []string{"golang"}}
	g.r.Cfg.Repositories = []config.Repository{repo}
	g.r.Cfg.Templates["solo"] = config.Template{Roles: []string{"implementer"}}
	tk := issueTask("3buqdlot", 7, "fix")
	tk.Spec.PredictedClass, tk.Spec.DataClass, tk.Spec.Template = "docs", "public", "solo"
	if s := g.r.implementerSpec(tk, "brief"); s.Checks || g.r.testFirst(tk) {
		t.Fatalf("no profile: checks %v testFirst %v, want today's off", s.Checks, g.r.testFirst(tk))
	}
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "p", Checks: true, TestFirst: true}
	s := g.r.implementerSpec(tk, "brief")
	if !s.Checks || !reflect.DeepEqual(s.EgressProfiles, []string{"golang"}) || !g.r.testFirst(tk) {
		t.Fatalf("frozen on: checks %v egress %v testFirst %v", s.Checks, s.EgressProfiles, g.r.testFirst(tk))
	}
	repo.Classes["docs"] = config.Class{Checks: true, TestFirst: true}
	g.r.Cfg.Repositories = []config.Repository{repo}
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "p"}
	if s := g.r.implementerSpec(tk, "brief"); s.Checks || g.r.testFirst(tk) {
		t.Fatalf("frozen off: checks %v testFirst %v, want the frozen off", s.Checks, g.r.testFirst(tk))
	}
}
