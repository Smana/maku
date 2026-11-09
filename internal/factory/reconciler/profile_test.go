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
	c.Classes = map[string]config.Class{"docs-links": {Shadow: true}, "review": review, "tests": {}}
	return c
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
	for name, c := range map[string]struct {
		tk    *v1alpha1.Task
		label string
	}{
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
