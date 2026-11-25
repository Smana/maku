// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// profileCfg is the rig's config with a review-floor workflow and its loops row; harness says
// whether the repository runs it by default (WF3). Without, the repository's default (standard) is
// no workflow the config defines, so a task carries no status.profile.
func profileCfg(harness bool) *config.Config {
	c := cfg()
	steps := int32(40)
	c.Loops = map[string]config.Loop{"review-floor": {StallSteps: &steps, Search: true, Scope: config.ScopeScoper}}
	c.HarnessProfiles = map[string]config.HarnessProfile{"review-floor": {Template: "pair", Checks: true}}
	if harness {
		c.Repositories[0].Workflow.Default = "review-floor"
	}
	c.Templates["investigate"] = config.Template{Roles: []string{"triager"}}
	c.Repositories[0].Classes = map[string]config.Class{"docs-links": {Shadow: true}, "review": {}, "tests": {}}
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

// A task routed to investigate by its source never takes part in the evaluation.
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
	g.r.Triage = triage.Classify{Forge: g.f}
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
		"unlabelled":              {"", 0, v1alpha1.ProfileStatus{Name: "review-floor", Source: "repository", Cohort: "treatment", Harness: "openhands", HarnessSource: "default", Checks: true}},
		"labelled":                {"class:review", 0, v1alpha1.ProfileStatus{Name: "review-floor", Source: "repository", Cohort: "treatment", Harness: "openhands", HarnessSource: "default", Checks: true}},
		"labelled, in the cohort": {"class:review", 100, v1alpha1.ProfileStatus{Name: "review-floor", Source: "control", Cohort: "control", Harness: "openhands", HarnessSource: "default", Checks: true}},
		"in the cohort":           {"", 100, v1alpha1.ProfileStatus{Name: "review-floor", Source: "control", Cohort: "control", Harness: "openhands", HarnessSource: "default", Checks: true}},
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

// P8, WF4: a class: label is no pin, so the task is offered every workflow as a choice.
func TestClassLabelledTaskIsOfferedProfileChoices(t *testing.T) {
	cf := profileCfg(true)
	cf.HarnessProfiles["review-careful"] = config.HarnessProfile{Template: "pair", Description: "Two reviews."}
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it"), "class:review")
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Profile == nil || tk.Status.Profile.Source != "repository" || tk.Status.Profile.Cohort != triage.CohortTreatment {
		t.Fatalf("%+v", tk.Status.Profile)
	}
	if got := g.r.profileChoices(cf, tk); len(got) != 2 || got[0].Name != "review-floor" || !got[0].Floor || got[1].Name != "review-careful" {
		t.Fatalf("choices %+v", got)
	}
}

// mainSpec is what received writes for a task with no workflow: the triage decision and default's row.
func mainSpec(t *testing.T, g *rig, tk *v1alpha1.Task) v1alpha1.TaskSpec {
	t.Helper()
	tk = tk.DeepCopy()
	d, err := g.r.Triage.Triage(t.Context(), g.r.Cfg.Current(), tk)
	if err != nil {
		t.Fatal(err)
	}
	s := tk.Spec
	s.Template, s.PredictedClass, s.DataClass = d.Template, d.PredictedClass, d.DataClass
	s.Budget = triage.Ceiling(d.Budget, s.MaxMicroUSD)
	const row = "" // no workflow: default's row (WF10)
	if p := g.r.Cfg.Current().LoopFor(row); p != nil {
		judge := p.JudgeModel
		if d.DataClass != "public" && config.PublicOnly(judge) {
			judge = config.DefaultJudge
		}
		s.Loop = &v1alpha1.Loop{StallSteps: p.StallSteps, Continuations: p.Continuations, JudgeModel: judge}
	}
	s.Search = g.r.Cfg.Current().SearchFor(row)
	mode := g.r.Cfg.Current().ScopeFor(row)
	if g.r.Cfg.Current().Investigates(s.Template) {
		mode = config.ScopeOff
	}
	s.ScopeMode, s.Steering = string(mode), nil
	tk.Spec = s
	g.r.splitAtTriage(g.r.Cfg.Current(), tk, row, d.Classification)
	return tk.Spec
}

// Off is today: a class without a harness gets the Task main gave it, Spec and Status, and no
// status.profile, whatever the intake. The expectation is testdata/no-harness.json: each case's whole
// Task after one reconcile at 88e4c11 (origin/main before the evaluation), normalized as
// normalizedTask does, written by hack/no-harness-golden.sh from a worktree of that commit with the
// base's own helpers; it is not derived from the code under test.
func TestNoHarnessIsToday(t *testing.T) {
	var golden map[string]any
	raw, err := os.ReadFile("testdata/no-harness.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
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
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != v1alpha1.PhaseTriaged || tk.Status.Profile != nil {
				t.Fatalf("triaged, no harness, no profile: %s %+v", tk.Status.Phase, tk.Status.Profile)
			}
			want, ok := golden[name]
			if !ok {
				t.Fatalf("no golden task for %q", name)
			}
			got := normalizedTask(t, tk)
			// Since the loops rows are keyed by workflow (WF10, stage B), a task with no workflow reads
			// default's row, not its class's: the loop keys are left out of the comparison.
			for _, m := range []any{want, got} {
				if spec, ok := m.(map[string]any)["spec"].(map[string]any); ok {
					delete(spec, "loop")
					delete(spec, "scopeMode")
					delete(spec, "search")
				}
			}
			if name == "labelled issue" { // the class: label is narrated once since it is read by nothing (WF13)
				if st, ok := got.(map[string]any)["status"].(map[string]any); ok {
					delete(st, "narrated")
					delete(st, "statusComment")
				}
			}
			if !reflect.DeepEqual(want, got) {
				w, _ := json.MarshalIndent(want, "", "  ")
				g, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("the task differs from main's\n got %s\nwant %s", g, w)
			}
		})
	}
}

// normalizedTask is the Task as JSON with its volatile fields fixed: the server-set metadata dropped,
// every RFC3339 time "<time>". hack/no-harness-golden.sh carries the same function; change both.
func normalizedTask(t *testing.T, tk *v1alpha1.Task) any {
	t.Helper()
	raw, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if meta, ok := v.(map[string]any)["metadata"].(map[string]any); ok {
		for _, k := range []string{"resourceVersion", "generation", "uid", "creationTimestamp", "managedFields"} {
			delete(meta, k)
		}
	}
	return scrubTimes(v)
}

func scrubTimes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = scrubTimes(e)
		}
	case []any:
		for i, e := range x {
			x[i] = scrubTimes(e)
		}
	case string:
		if _, err := time.Parse(time.RFC3339, x); err == nil {
			return "<time>"
		}
	}
	return v
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
			g.r.profileSpec(g.r.Cfg.Current(), tk, "review-floor", c.p) // a workflow without a loop reads the row named after it
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

// A run reads checks and testFirst frozen on status.profile, not the workflow's live config; a task
// without a profile reads the repository's default workflow as configured live.
func TestRunsReadTheFrozenProfile(t *testing.T) {
	for name, tc := range map[string]struct {
		live    bool
		profile *v1alpha1.ProfileStatus
		on      bool
		egress  []string
	}{
		"no profile":              {live: false},
		"no profile, live on":     {live: true, on: true, egress: []string{"golang"}},
		"frozen on, workflow off": {live: false, profile: &v1alpha1.ProfileStatus{Name: "p", Checks: true, TestFirst: true}, on: true, egress: []string{"golang"}},
		"frozen off, workflow on": {live: true, profile: &v1alpha1.ProfileStatus{Name: "p"}},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			cfg := g.r.Cfg.Current()
			repo := cfg.Repos()[0]
			repo.Classes = map[string]config.Class{"docs-links": {}, "docs": {}}
			repo.Checks = config.Checks{Egress: []string{"golang"}}
			repo.Workflow = config.RepositoryWorkflow{Default: config.WorkflowCareful, TestFirst: tc.live}
			cfg.Repositories = []config.Repository{repo}
			cfg.HarnessProfiles = map[string]config.HarnessProfile{config.WorkflowCareful: {Checks: tc.live}}
			cfg.Loops = map[string]config.Loop{config.WorkflowCareful: {Tests: true}}
			cfg.Templates["solo"] = config.Template{Roles: []string{"implementer"}}
			tk := issueTask("3buqdlot", 7, "fix")
			tk.Spec.PredictedClass, tk.Spec.DataClass, tk.Spec.Template = "docs", "public", "solo"
			tk.Status.Profile = tc.profile
			s := g.r.implementerSpec(cfg, tk, "brief")
			if s.Checks != tc.on || !reflect.DeepEqual(s.EgressProfiles, tc.egress) || g.r.testFirst(cfg, tk) != tc.on {
				t.Fatalf("checks %v egress %v testFirst %v, want %v %v %v", s.Checks, s.EgressProfiles, g.r.testFirst(cfg, tk), tc.on, tc.egress, tc.on)
			}
		})
	}
}

// treatmentCfg is profileCfg with a careful workflow named as evaluation.treatment (WF8): a
// frontier tier and its own judged loop row, so its spec is told apart from the default's.
func treatmentCfg(percent int) *config.Config {
	c := profileCfg(true)
	c.HarnessProfiles["careful"] = config.HarnessProfile{Template: "pair", Tier: "frontier", Loop: "careful"}
	c.Loops["careful"] = config.Loop{Scope: config.ScopeScoper, Judge: "tier-light", Search: true, Tests: true}
	c.Evaluation.ControlPercent, c.Evaluation.Treatment = percent, "careful"
	return c
}

func TestTreatmentArmRunsTheNamedWorkflow(t *testing.T) {
	g := profileRig(t, treatmentCfg(0), issueTask("3buqdlot", 7, "fix it"), "")
	tk := g.reconcile(t, "3buqdlot", 1)
	if p := tk.Status.Profile; p == nil || p.Name != "careful" || p.Source != "treatment" || p.Cohort != triage.CohortTreatment {
		t.Fatalf("%+v", tk.Status.Profile)
	}
	if tk.Spec.Budget.Tier != "frontier" || tk.Spec.Loop == nil || tk.Spec.Loop.JudgeModel != "tier-light" ||
		tk.Spec.ScopeMode != string(config.ScopeScoper) {
		t.Fatalf("the careful workflow's spec was not applied: %+v", tk.Spec)
	}
}

func TestControlArmRunsTheFloor(t *testing.T) {
	g := profileRig(t, treatmentCfg(100), issueTask("3buqdlot", 7, "fix it"), "")
	tk := g.reconcile(t, "3buqdlot", 1)
	if p := tk.Status.Profile; p == nil || p.Name != "review-floor" || p.Source != "control" || p.Cohort != triage.CohortControl {
		t.Fatalf("%+v", tk.Status.Profile)
	}
	if tk.Spec.Budget.Tier != "standard" || tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != 40 {
		t.Fatalf("the control task runs the floor: %+v", tk.Spec)
	}
}

// Review Focus 3, stage B: a treatment-cohort task whose workflow its source chose (a schedule, a
// revert) keeps it with that source, and one with no treatment set keeps the repository's default
// with source repository, so the measurement's source=~"control|treatment" leaves both out. A live
// or shadow class no longer excludes a task: the class names no workflow (WF4).
func TestTreatmentArmFallsBackAndIsLabelled(t *testing.T) {
	schedule := issueTask("3buqdlot", 0, "fix it")
	schedule.Spec.Source.Kind = "schedule"
	revert := issueTask("3buqdlot", 7, "fix it")
	revert.Spec.PredictedClass = "revert"
	for name, c := range map[string]struct {
		task         *v1alpha1.Task
		name, source string
	}{
		"a schedule":         {schedule, "quick", "schedule"},
		"a revert":           {revert, "quick", "revert"},
		"an unset treatment": {issueTask("3buqdlot", 7, "fix it"), "review-floor", "repository"},
		"a shadow class":     {issueTask("3buqdlot", 7, "fix it"), "careful", "treatment"},
	} {
		t.Run(name, func(t *testing.T) {
			cf := treatmentCfg(0)
			cf.HarnessProfiles["quick"] = config.HarnessProfile{Template: "solo"}
			if name == "an unset treatment" {
				cf.Evaluation.Treatment = ""
			}
			p := (&Reconciler{}).floorProfile(cf, c.task, triage.Decision{PredictedClass: c.task.Spec.PredictedClass, Cohort: triage.CohortTreatment})
			if p == nil || p.Name != c.name || p.Source != c.source || p.Cohort != triage.CohortTreatment {
				t.Fatalf("got %+v, want %s from %s", p, c.name, c.source)
			}
		})
	}
}

// WF8, comparison 2: the treatment may run fewer roles than the default (quick drops the reviewer):
// the class no longer holds a floor (WF4), and the gate holds an unreviewed change for a human.
func TestTreatmentMayDropTheReviewer(t *testing.T) {
	cf := treatmentCfg(0)
	cf.HarnessProfiles["quick"] = config.HarnessProfile{Template: "solo"}
	cf.Evaluation.Treatment = "quick"
	p := (&Reconciler{}).floorProfile(cf, issueTask("3buqdlot", 7, "fix it"), triage.Decision{PredictedClass: "review", Cohort: triage.CohortTreatment})
	if p == nil || p.Name != "quick" || p.Source != "treatment" {
		t.Fatalf("got %+v", p)
	}
}

// WF7: the tester runs where the repository opts in and the task runs careful, whatever its class.
func TestTestFirstFollowsTheRepository(t *testing.T) {
	cf := treatmentCfg(0)
	cf.Repositories[0].Workflow.TestFirst = true
	for name, want := range map[string]bool{"careful": true, "review-floor": false} {
		d := triage.Decision{PredictedClass: "review", Cohort: triage.CohortLabeled, Pinned: true, Pin: triage.Pin{Name: name}}
		if p := (&Reconciler{}).floorProfile(cf, issueTask("3buqdlot", 7, "fix it"), d); p == nil || p.TestFirst != want {
			t.Fatalf("%s: got %+v, want testFirst %v", name, p, want)
		}
	}
}

// A pinned task is labeled: the treatment never replaces the pin.
func TestPinnedTaskIgnoresTreatment(t *testing.T) {
	d := triage.Decision{PredictedClass: "review", Cohort: triage.CohortLabeled, Pinned: true, Pin: triage.Pin{Name: "review-floor"}}
	p := (&Reconciler{}).floorProfile(treatmentCfg(0), issueTask("3buqdlot", 7, "fix it"), d)
	if p == nil || p.Name != "review-floor" || p.Source != "label" || p.Cohort != triage.CohortLabeled {
		t.Fatalf("got %+v", p)
	}
}

// WR3: a default task gets task-card at triage and no duplicate profile-chosen comment unless the
// workflow actually changes after the card.
func TestADefaultTaskGetsOneWorkflowComment(t *testing.T) {
	cf := resolveCfg("observe", 0)
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\nthe link resolves"), "")
	tk := g.reconcile(t, "3buqdlot", 1)
	if !slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool { return o.Key == "task-card" }) &&
		!slices.Contains(tk.Status.Narrated, "task-card") {
		t.Fatalf("expected task-card in outbox/narrated: outbox=%+v narrated=%v", tk.Status.Outbox, tk.Status.Narrated)
	}
	for _, o := range tk.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-chosen-") {
			t.Fatalf("unexpected profile-chosen at triage: %+v", tk.Status.Outbox)
		}
	}
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), cf, tk, &sc); err != nil {
		t.Fatal(err)
	}
	for _, o := range tk.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-chosen-") {
			t.Fatalf("observe mode should not post profile-chosen: %+v", tk.Status.Outbox)
		}
	}
}

func TestProfileSpecResolvesTemplateFromReviewRowKey(t *testing.T) {
	cf := profileCfg(true)
	reviewOn, reviewOff, planOn := true, false, true
	cf.Loops["judged-pair"] = config.Loop{Judge: config.DefaultJudge, Review: &reviewOn}
	cf.Loops["judged-solo"] = config.Loop{Judge: config.DefaultJudge, Review: &reviewOff, Plan: &planOn}

	g := newRig(t)
	g.r.Cfg = cf
	tk := issueTask("3buqdlot", 7, "fix it")
	tk.Spec.Template = "solo"
	g.r.profileSpec(cf, tk, "judged-pair", config.HarnessProfile{})
	if tk.Spec.Template != "pair" || tk.Spec.Loop == nil || tk.Spec.Loop.JudgeModel != config.DefaultJudge || tk.Spec.ScopeMode != string(config.ScopeOff) {
		t.Fatalf("judged-pair: template=%q loop=%+v scope=%q", tk.Spec.Template, tk.Spec.Loop, tk.Spec.ScopeMode)
	}

	g.r.profileSpec(cf, tk, "judged-solo", config.HarnessProfile{})
	if tk.Spec.Template != "solo" || tk.Spec.ScopeMode != string(config.ScopeScoper) {
		t.Fatalf("judged-solo: template=%q scope=%q", tk.Spec.Template, tk.Spec.ScopeMode)
	}
}
