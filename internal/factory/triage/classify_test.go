// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

func TestMatrix(t *testing.T) { // §2
	for _, c := range []struct{ class, source, tier, want string }{
		{"docs-links", "issue", "light", "solo"}, {"docs-links", "issue", "standard", "solo"}, {"docs-links", "issue", "frontier", "pair"},
		{"docs", "issue", "light", "pair"}, {"review", "issue", "standard", "pair"}, {"tests", "schedule", "frontier", "pair"},
		{"review", "runlore", "light", "investigate"}, {"review", "runlore", "frontier", "investigate"},
	} {
		if got := Matrix(c.class, c.source, c.tier); got != c.want {
			t.Errorf("%s/%s/%s = %s, want %s", c.class, c.source, c.tier, got, c.want)
		}
	}
}

// trio left the shipped templates (workflows WF2): a frontier task runs pair, whatever its class or
// source, so it never names a template a factory may no longer define.
func TestMatrixNeverReturnsTrio(t *testing.T) {
	for _, class := range []string{"docs-links", "docs", "review", "tests", "revert"} {
		for _, source := range []string{"issue", "schedule", "api", "runlore"} {
			if got := Matrix(class, source, "frontier"); got == "trio" {
				t.Errorf("%s/%s/frontier = trio", class, source)
			}
		}
	}
}

func TestControlIsDeterministicAndAboutTenPercent(t *testing.T) {
	n := 0
	for i := 0; i < 2000; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + "234567"
		// SA4000 bars the literal Control(id,10) != Control(id,10); the two bindings are the
		// check: one task id must always land the same way.
		if a, b := Control(id, 10), Control(id, 10); a != b {
			t.Fatal("deterministic")
		}
		if Control(id, 10) {
			n++
		}
	}
	if n < 120 || n > 280 {
		t.Fatalf("%d of 2000 in the control group", n)
	}
	if Control("3buqdlot", 0) {
		t.Fatal("0 % means none")
	}
}

type fakeC7 struct {
	resp C7Response
	err  error
	got  C7Request
}

func (f *fakeC7) Classify(_ context.Context, r C7Request) (C7Response, error) {
	f.got = r
	return f.resp, f.err
}

func testCfg() *config.Config {
	profiles, _, _ := config.ShippedWorkflows()
	return &config.Config{HarnessProfiles: profiles,
		Repositories: []config.Repository{{Maintainers: []string{"Smana"},
			Classes: map[string]config.Class{"docs-links": {Live: true}, "revert": {Live: true}, "docs": {}, "tests": {}, "dashboards": {}}}},
		Tiers: map[string]config.Tier{
			"light":    {Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20},
			"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			"frontier": {Model: "agent-default", RunTokens: 4_000_000, TaskTokens: 8_000_000, RunMinutes: 90}},
		Triage: config.Triage{ControlPercent: 0}}
}

func issueTask() *v1alpha1.Task {
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
		Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "issue"}, Issue: 7, Text: "fix a link", DataClass: "public"}}
}

// WF4, WF13: a class: and a profile: label, a maintainer's included, are read by nothing, and
// triage narrates it once; the classifier still drives the tier.
func TestClassAndProfileLabelsAreIgnoredAndNarrated(t *testing.T) {
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs", "profile:careful"}})
	f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs", At: time.Now()},
		forge.LabelEvent{Actor: "Smana", Label: "profile:careful", At: time.Now()})
	c7 := &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.91, Classifier: "semantic-router", Fallback: "none",
		Shadow: []C7Shadow{{Classifier: "jev", Tier: "standard", Confidence: 0.6}}}}
	d, err := Classify{C: c7, Forge: f}.Triage(context.Background(), testCfg(), issueTask())
	if err != nil {
		t.Fatal(err)
	}
	if d.Pin.Name != "" || d.PredictedClass != "review" || d.Declared {
		t.Fatalf("a removed label was read: %+v", d)
	}
	if !slices.Contains(d.Notes, LegacyLabelNote) {
		t.Fatalf("not narrated: %v", d.Notes)
	}
	if d.Budget.Tier != "light" || d.Classification.Classifier != "semantic-router" || d.Classification.Confidence != "0.91" || len(d.Classification.Shadow) != 1 {
		t.Fatalf("%+v", d)
	}
	if c7.got.Ref != "3buqdlot" || c7.got.DataClass != "public" || c7.got.Text != "fix a link" {
		t.Fatalf("C7 request %+v", c7.got)
	}
}

func TestClassifyFallsBackAndIgnoresStrangersLabels(t *testing.T) {
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs-links"}})
	f.SetEvents(7, forge.LabelEvent{Actor: "someone", Label: "class:docs-links", At: time.Now()})
	d, err := Classify{C: &fakeC7{err: errors.New("timeout")}, Forge: f}.Triage(context.Background(), testCfg(), issueTask())
	if err != nil {
		t.Fatal(err)
	}
	if d.PredictedClass != "review" || d.Template != "pair" || d.Budget.Tier != "standard" ||
		d.Classification.Fallback != "static" || d.Classification.Classifier != "static" {
		t.Fatalf("the classifier never blocks (C7) and labels are not trust anchors (§8): %+v", d)
	}
}

func TestControlGroupRunsAtFrontier(t *testing.T) {
	cfg := testCfg()
	cfg.Triage.ControlPercent = 100
	d, _ := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Classifier: "semantic-router", Fallback: "none"}},
		Forge: forgetest.NewFake()}.Triage(context.Background(), cfg, &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "schedule"}, DataClass: "public"}})
	if !d.Classification.Control || d.Classification.Tier != "light" || d.Budget.Tier != "frontier" {
		t.Fatalf("the classifier's answer is recorded as-is; the budget acts at frontier (OD-14): %+v", d)
	}
}

// A workflow label is judged against the task's repository's maintainers (v2 D1), read on the
// task's own repository (v2 D8): B's issue #7 is not A's.
func TestClassifyReadsTheTasksRepository(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories = []config.Repository{{Name: "Smana/a", Maintainers: []string{"Smana"}}, {Name: "Smana/b", Maintainers: []string{"Other"}}}
	for name, c := range map[string]struct{ repo, actor, refused string }{
		"A's maintainer on A": {"Smana/a", "Smana", ""},
		"A's maintainer on B": {"Smana/b", "Smana", "not_maintainer"},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			f.SetIssue(forge.Issue{Number: 7, Labels: []string{"workflow:careful"}})
			f.SetEvents(7, forge.LabelEvent{Actor: c.actor, Label: "workflow:careful", At: time.Now()})
			tk := issueTask()
			tk.Spec.Repository = c.repo
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cfg, tk)
			if err != nil || d.Pin != (Pin{Name: "careful", Refused: c.refused}) {
				t.Fatalf("pin %+v (%v), refused %q", d.Pin, err, c.refused)
			}
		})
	}
	onA, onB := forgetest.NewFake(), forgetest.NewFake()
	onA.SetIssue(forge.Issue{Number: 7, Labels: []string{"workflow:careful"}})
	onA.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "workflow:careful", At: time.Now()})
	onB.SetIssue(forge.Issue{Number: 7})
	clients := func(repo string) (ClassForge, bool) {
		switch repo {
		case "Smana/a":
			return onA, true
		case "Smana/b":
			return onB, true
		}
		return nil, false
	}
	for _, repo := range []string{"Smana/b", "Smana/elsewhere"} {
		tk := issueTask()
		tk.Spec.Repository = repo
		if d, err := (Classify{Clients: clients}).Triage(t.Context(), cfg, tk); err != nil || d.Pin != (Pin{}) {
			t.Fatalf("%s reads A's label: %+v %v", repo, d.Pin, err)
		}
	}
}

// MC6: an oracle class's task runs solo on light, without a classifier call or the control group.
func TestAnOracleClassRunsSoloOnLight(t *testing.T) {
	cfg := testCfg()
	cfg.Triage.ControlPercent = 100
	cfg.Repositories[0].Classes["dead-links"] = config.Class{Shadow: true, Oracle: &config.Oracle{Probe: "dead-links", Workflow: "dead-links.yaml", MaxFindings: 10}}
	c7 := &fakeC7{resp: C7Response{Tier: "frontier", Classifier: "semantic-router"}}
	d, err := Classify{C: c7, Forge: forgetest.NewFake()}.Triage(context.Background(), cfg,
		&v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "schedule"}, PredictedClass: "dead-links", Text: "x", DataClass: "public"}})
	if err != nil || d.Template != "solo" || d.Budget.Tier != "light" || d.PredictedClass != "dead-links" || d.Classification.Control ||
		c7.got.Text != "" {
		t.Fatalf("%+v %v (classifier asked: %+v)", d, err, c7.got)
	}
	// The Task CRD's status.classification.fallback is a required enum: "" is refused by the API server.
	if f := d.Classification.Fallback; f != "none" && f != "default" && f != "static" {
		t.Fatalf("fallback %q is outside the CRD enum none|default|static", f)
	}
}

// No C7 service (triage.classifierURL was removed, WF9): Triage takes the static fallback.
func TestClassifyWithoutAClassifierMakesNoCall(t *testing.T) {
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 7})
	d, err := Classify{Forge: f}.Triage(t.Context(), testCfg(), issueTask())
	if err != nil {
		t.Fatal(err)
	}
	if d.Classification.Tier != "standard" || d.Classification.Classifier != "static" || d.Classification.Fallback != "static" {
		t.Fatalf("%+v", d.Classification)
	}
}

// RA4, R3: an internal principal's task takes investigate on internal data whatever its issue says;
// a sponsored principal's task, and an api task of a principal the config does not declare, do not.
func TestInternalPrincipalTasksInvestigate(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories = []config.Repository{{Name: "Smana/cloud-native-ref", Maintainers: []string{"Smana"}, Classes: cfg.Repositories[0].Classes}}
	cfg.API.Sponsored = []config.Sponsored{{Name: "slack-app"}}
	cfg.API.InternalPrincipals = []config.InternalPrincipal{{Sponsored: config.Sponsored{Name: "runlore"}}}
	for name, c := range map[string]struct {
		by                  string
		template, class, dc string
	}{
		"an internal principal":   {"system:runlore", "investigate", "review", "internal"},
		"a sponsored principal":   {"system:slack-app", "pair", "review", "public"},
		"an undeclared principal": {"system:ghost", "pair", "review", "public"},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs"}})
			f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs", At: time.Now()})
			tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
				Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "api", RequestedBy: c.by}, Repository: "Smana/cloud-native-ref",
					Issue: 7, Text: "x", DataClass: "public"}}
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "standard", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cfg, tk)
			if err != nil || d.Template != c.template || d.PredictedClass != c.class || d.DataClass != c.dc {
				t.Fatalf("%+v %v", d, err)
			}
		})
	}
}

// K5: a task with an origin keeps the class the factory inferred at publish; no class: label, a
// maintainer's or the factory's own, names another (WF4).
func TestAnOriginTaskKeepsItsInferredClass(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories[0].Classes["alert-rules"] = config.Class{}
	for name, c := range map[string]struct {
		predicted, origin, label, actor, want string
	}{
		"the factory's own label":    {"alert-rules", "3buqdlot", "class:alert-rules", "ogenki-agent-factory[bot]", "alert-rules"},
		"a maintainer's other label": {"alert-rules", "3buqdlot", "class:docs", "Smana", "alert-rules"},
		"revert is never inferred":   {"revert", "3buqdlot", "", "", "review"},
		"an undeclared class":        {"gone", "3buqdlot", "", "", "review"},
		"no origin":                  {"alert-rules", "", "", "", "review"},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			iss := forge.Issue{Number: 7}
			if c.label != "" {
				iss.Labels = []string{c.label}
				f.SetEvents(7, forge.LabelEvent{Actor: c.actor, Label: c.label, At: time.Now()})
			}
			f.SetIssue(iss)
			tk := issueTask()
			tk.Spec.PredictedClass, tk.Spec.Origin = c.predicted, c.origin
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cfg, tk)
			if err != nil || d.PredictedClass != c.want {
				t.Fatalf("PredictedClass %q, want %q (%v)", d.PredictedClass, c.want, err)
			}
		})
	}
}

// TE: a class a maintainer's intake or a schedule names is declared; an inferred one is not, and a
// class: label names none (WF4).
func TestPredictClassReportsDeclared(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories[0].Classes["dead-links"] = config.Class{Oracle: &config.Oracle{Probe: "dead-links"}}
	cfg.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"doc-drift":  {Name: "doc-drift"},
		"dead-links": {Name: "dead-links", OracleClass: "dead-links"},
	}
	sched := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "schedule"}, PredictedClass: "tests"}}
	routinePlain := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "doc-drift"}}}
	routineOracleSpec := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-links"}, PredictedClass: "dead-links"}}
	routineOracleCfg := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-links"}}}
	planned := issueTask()
	planned.Spec.Intake = &v1alpha1.TaskIntake{Class: "docs", Login: "Smana"}
	for name, c := range map[string]struct {
		task     *v1alpha1.Task
		want     string
		declared bool
	}{
		"an issue":                     {issueTask(), "review", false},
		"a maintainer's plan":          {planned, "docs", true},
		"a schedule":                   {sched, "tests", true},
		"a routine without oracle":     {routinePlain, "review", false},
		"a routine with spec oracle":   {routineOracleSpec, "dead-links", true},
		"a routine with config oracle": {routineOracleCfg, "dead-links", true},
	} {
		t.Run(name, func(t *testing.T) {
			class, declared := Classify{}.predictClass(cfg, c.task)
			if class != c.want || declared != c.declared {
				t.Fatalf("(%q, %v), want (%q, %v)", class, declared, c.want, c.declared)
			}
		})
	}
}

// TE: the evaluation cohort is hashed apart from OD-14's control group.
func TestCohortIsApartFromControl(t *testing.T) {
	cohort, control, both := 0, 0, 0
	for i := range 1000 {
		id := fmt.Sprintf("t-%d", i)
		a, b := Cohort(id, 10), Control(id, 10)
		if a {
			cohort++
		}
		if b {
			control++
		}
		if a && b {
			both++
		}
	}
	if cohort < 80 || cohort > 120 || control < 80 || control > 120 || both >= 30 {
		t.Fatalf("cohort %d, control %d, both %d", cohort, control, both)
	}
	if Cohort("t-1", 0) {
		t.Fatal("0 % means none")
	}
}

// P8: a class: label, read by nothing (WF4), never sets the cohort: the task is hashed into control
// or treatment like any other.
func TestClassLabelJoinsCohorts(t *testing.T) {
	for percent, want := range map[int]string{100: CohortControl, 0: CohortTreatment} {
		f := forgetest.NewFake()
		f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs"}})
		f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs", At: time.Now()})
		cf := testCfg()
		cf.Evaluation.ControlPercent = percent
		d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cf, issueTask())
		if err != nil || d.PredictedClass != "review" || d.Declared || d.Pinned || d.Cohort != want {
			t.Fatalf("%d %%: %+v (%v), want cohort %s", percent, d, err, want)
		}
	}
}

// G8: a schedule's class is declared, and the task still joins the cohorts.
func TestScheduledClassJoinsCohorts(t *testing.T) {
	for percent, want := range map[int]string{100: CohortControl, 0: CohortTreatment} {
		tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
			Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "schedule"}, PredictedClass: "tests", DataClass: "public"}}
		cf := testCfg()
		cf.Evaluation.ControlPercent = percent
		d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}}.Triage(t.Context(), cf, tk)
		if err != nil || d.PredictedClass != "tests" || !d.Declared || d.Pinned || d.Cohort != want {
			t.Fatalf("%d %%: %+v (%v), want cohort %s", percent, d, err, want)
		}
	}
}

// P7: an intake class a maintainer's plan carried is declared, a class: label never beats it (WF4),
// and one from a login that no longer maintains the repository is ignored. None sets the cohort (P8).
func TestIntakeClass(t *testing.T) {
	for name, c := range map[string]struct {
		login, label, class, want string
		declared                  bool
	}{
		"from a maintainer":           {"Smana", "", "docs", "docs", true},
		"from a non-maintainer":       {"someone", "", "docs", "review", false},
		"a label never beats it":      {"Smana", "class:tests", "docs", "docs", true},
		"a class the config lacks":    {"Smana", "", "nope", "review", false},
		"revert is the factory's own": {"Smana", "", "revert", "review", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			var labels []string
			if c.label != "" {
				labels = []string{c.label}
				f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: c.label, At: time.Now()})
			}
			f.SetIssue(forge.Issue{Number: 7, Labels: labels})
			tk := issueTask()
			tk.Spec.Source.Kind = "api"
			tk.Spec.Intake = &v1alpha1.TaskIntake{Class: c.class, Login: c.login}
			cf := testCfg()
			cf.Evaluation.ControlPercent = 100
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cf, tk)
			if err != nil || d.PredictedClass != c.want || d.Declared != c.declared || d.Cohort != CohortControl {
				t.Fatalf("%+v (%v), want %s declared %v", d, err, c.want, c.declared)
			}
		})
	}
}

// P5, P6, P9: a workflow pin is read from a workflow: label (its last applier) or the intake field
// (its resolved login), checked by ResolvePin, and only an honoured one is labeled; a refused one
// leaves the task in the cohorts by hash, the refusal recorded. A profile: label is no pin (WF13).
func TestTriageResolvesThePin(t *testing.T) {
	for name, c := range map[string]struct {
		labels []forge.LabelEvent
		intake *v1alpha1.TaskIntake
		want   Pin
		cohort string
	}{
		"no pin": {cohort: CohortControl},
		"a maintainer's label": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:careful"}},
			want: Pin{Name: "careful"}, cohort: CohortLabeled},
		"a stranger's label": {labels: []forge.LabelEvent{{Actor: "someone", Label: "workflow:careful"}},
			want: Pin{Name: "careful", Refused: "not_maintainer"}, cohort: CohortControl},
		"the last applier counts": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:careful"},
			{Actor: "someone", Label: "workflow:careful"}}, want: Pin{Name: "careful", Refused: "not_maintainer"}, cohort: CohortControl},
		"two workflows": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:careful"}, {Actor: "Smana", Label: "workflow:quick"}},
			want: Pin{Name: "careful", Refused: "profile_conflict"}, cohort: CohortControl},
		"a maintainer's intake": {intake: &v1alpha1.TaskIntake{Class: "docs", Profile: "careful", Login: "Smana"},
			want: Pin{Name: "careful"}, cohort: CohortLabeled},
		"a label and an intake that disagree": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:quick"}},
			intake: &v1alpha1.TaskIntake{Class: "docs", Profile: "careful", Login: "Smana"},
			want:   Pin{Name: "quick", Refused: "profile_conflict"}, cohort: CohortControl},
		"a live class": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:quick"}},
			intake: &v1alpha1.TaskIntake{Class: "docs-links", Login: "Smana"}, want: Pin{Name: "quick"}, cohort: CohortLabeled},
		"an unknown workflow": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "workflow:nope"}},
			want: Pin{Name: "nope", Refused: "profile_unknown"}, cohort: CohortControl},
		"a profile: label is no pin": {labels: []forge.LabelEvent{{Actor: "Smana", Label: "profile:careful"}}, cohort: CohortControl},
		// Broker-confirmed handoff: the App's pin is the confirmed maintainer's, never the App's own.
		"the App's label on a confirmed handoff": {labels: []forge.LabelEvent{{Actor: "ogenki-agent-factory[bot]", Label: "workflow:careful"}},
			intake: &v1alpha1.TaskIntake{Profile: "careful", Login: "Smana"}, want: Pin{Name: "careful"}, cohort: CohortLabeled},
		"the App's label alone": {labels: []forge.LabelEvent{{Actor: "ogenki-agent-factory[bot]", Label: "workflow:careful"}},
			want: Pin{Name: "careful", Refused: "not_maintainer"}, cohort: CohortControl},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			var labels []string
			var events []forge.LabelEvent
			for _, e := range c.labels {
				if !slices.Contains(labels, e.Label) {
					labels = append(labels, e.Label)
				}
				e.At = time.Now()
				events = append(events, e)
			}
			f.SetEvents(7, events...)
			f.SetIssue(forge.Issue{Number: 7, Labels: labels})
			tk := issueTask()
			tk.Spec.Intake = c.intake
			cf := testCfg()
			cf.Evaluation.ControlPercent = 100
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cf, tk)
			if err != nil || d.Pin != c.want || d.Pinned != c.want.Honoured() || d.Cohort != c.cohort {
				t.Fatalf("%+v (%v), want pin %+v cohort %s", d, err, c.want, c.cohort)
			}
			legacy := slices.ContainsFunc(c.labels, func(e forge.LabelEvent) bool { return strings.HasPrefix(e.Label, "profile:") })
			if got := slices.Contains(d.Notes, LegacyLabelNote); got != legacy {
				t.Fatalf("notes %q, want the legacy note %v", d.Notes, legacy)
			}
		})
	}
}

func TestAReportOnAPublicRepositoryKeepsItsDataClass(t *testing.T) {
	cfg := testCfg()
	cfg.Outcomes = []string{config.OutcomeChange, config.OutcomeReport}
	tk := issueTask()
	tk.Spec.DataClass = "public"
	tk.Spec.Outcome = config.OutcomeReport
	d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "standard", Classifier: "static", Fallback: "none"}},
		Forge: forgetest.NewFake()}.Triage(t.Context(), cfg, tk)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "investigate" {
		t.Fatalf("expected template=investigate, got %q", d.Template)
	}
	if d.Outcome != config.OutcomeReport {
		t.Fatalf("expected outcome=report, got %q", d.Outcome)
	}
	if d.DataClass != "public" {
		t.Fatalf("public report must keep dataClass=public, got %q", d.DataClass)
	}
}

func TestARunLoreFindingStaysInternal(t *testing.T) {
	cfg := testCfg()
	cfg.Outcomes = []string{config.OutcomeChange, config.OutcomeReport}
	tk := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
		Spec: v1alpha1.TaskSpec{
			Source:    v1alpha1.Source{Kind: "runlore", Ref: "runlore:alert/pod", RequestedBy: "system:runlore"},
			DataClass: "public", // even if passed as public, RunLore forces internal
			Text:      "Alert firing",
		},
	}
	d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "standard", Classifier: "static", Fallback: "none"}},
		Forge: forgetest.NewFake()}.Triage(t.Context(), cfg, tk)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template != "investigate" || d.DataClass != "internal" {
		t.Fatalf("RunLore finding must run investigate at internal, got template=%q dataClass=%q", d.Template, d.DataClass)
	}
}

func TestRoutineTriage(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories[0].Classes["dead-links"] = config.Class{Shadow: true, Oracle: &config.Oracle{Probe: "dead-links"}}
	cfg.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"dead-links": {Name: "dead-links", OracleClass: "dead-links"},
		"doc-drift":  {Name: "doc-drift"},
	}

	t.Run("oracle routine skips pin and C7, runs solo light static", func(t *testing.T) {
		f := forgetest.NewFake()
		// Even if an issue #7 existed with workflow:careful, routine pin must skip Forge entirely.
		f.SetIssue(forge.Issue{Number: 7, Labels: []string{"workflow:careful"}})
		c7 := &fakeC7{resp: C7Response{Tier: "frontier", Classifier: "semantic-router"}}
		tk := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
			Spec: v1alpha1.TaskSpec{
				Source:      v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-links"},
				Issue:       7,
				DataClass:   "public",
				MaxMicroUSD: 600_000,
				Text:        "fix dead link",
			},
		}
		d, err := Classify{C: c7, Forge: f}.Triage(t.Context(), cfg, tk)
		if err != nil {
			t.Fatal(err)
		}
		if d.Template != "solo" || d.PredictedClass != "dead-links" || !d.Declared || d.Pinned || d.Pin.Name != "" ||
			d.Budget.Tier != "light" || d.Classification.Classifier != "static" || c7.got.Text != "" {
			t.Fatalf("unexpected oracle routine triage: %+v (c7=%+v)", d, c7.got)
		}
		capped := Ceiling(d.Budget, tk.Spec.MaxMicroUSD)
		if capped.TaskMicroUSD != 600_000 || capped.RunMicroUSD != 200_000 {
			t.Fatalf("unexpected ceiling: %+v", capped)
		}
	})

	t.Run("non-oracle routine skips pin, uses C7 and Ceiling", func(t *testing.T) {
		c7 := &fakeC7{resp: C7Response{Tier: "standard", Classifier: "semantic-router", Fallback: "none"}}
		tk := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
			Spec: v1alpha1.TaskSpec{
				Source:      v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "doc-drift"},
				DataClass:   "public",
				MaxMicroUSD: 900_000,
				Text:        "sync docs",
				Intake:      &v1alpha1.TaskIntake{Profile: "careful", Login: "Smana"},
			},
		}
		d, err := Classify{C: c7}.Triage(t.Context(), cfg, tk)
		if err != nil {
			t.Fatal(err)
		}
		if d.Template != "pair" || d.PredictedClass != "review" || d.Declared || d.Pinned || d.Pin.Name != "" ||
			d.Budget.Tier != "standard" || c7.got.Ref != "3buqdlot" {
			t.Fatalf("unexpected non-oracle routine triage: %+v", d)
		}
		capped := Ceiling(d.Budget, tk.Spec.MaxMicroUSD)
		if capped.TaskMicroUSD != 900_000 || capped.RunMicroUSD != 300_000 {
			t.Fatalf("unexpected ceiling: %+v", capped)
		}
	})

	t.Run("propose routine investigates while preserving DataClass", func(t *testing.T) {
		c7 := &fakeC7{resp: C7Response{Tier: "light", Classifier: "semantic-router", Fallback: "none"}}
		// Public propose routine with OracleClass must still take investigate and keep DataClass=public.
		pubTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "proposep1"},
			Spec: v1alpha1.TaskSpec{
				Source:      v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-links"},
				DataClass:   "public",
				MaxMicroUSD: 600_000,
				Text:        "propose link fixes",
				Routine:     &v1alpha1.TaskRoutine{Name: "dead-links", Mode: config.RoutineModePropose},
			},
		}
		dPub, err := Classify{C: c7}.Triage(t.Context(), cfg, pubTask)
		if err != nil {
			t.Fatal(err)
		}
		if dPub.Template != "investigate" || dPub.PredictedClass != "review" || dPub.DataClass != "public" || !Investigates(cfg, pubTask) {
			t.Fatalf("unexpected public propose triage: %+v", dPub)
		}

		// Internal propose routine preserves DataClass=internal.
		privTask := pubTask.DeepCopy()
		privTask.Spec.DataClass = "internal"
		dPriv, err := Classify{C: c7}.Triage(t.Context(), cfg, privTask)
		if err != nil {
			t.Fatal(err)
		}
		if dPriv.Template != "investigate" || dPriv.DataClass != "internal" {
			t.Fatalf("unexpected internal propose triage: %+v", dPriv)
		}

		// RunLore source still forces DataClass=internal even if Spec.DataClass was public.
		rlTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "rlp1"},
			Spec: v1alpha1.TaskSpec{
				Source:    v1alpha1.Source{Kind: "runlore", Ref: "alert-1"},
				DataClass: "public",
				Text:      "alert text",
			},
		}
		dRL, err := Classify{C: c7}.Triage(t.Context(), cfg, rlTask)
		if err != nil {
			t.Fatal(err)
		}
		if dRL.Template != "investigate" || dRL.DataClass != "internal" {
			t.Fatalf("runlore did not force internal: %+v", dRL)
		}
	})
}
