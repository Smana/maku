// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"context"
	"errors"
	"fmt"
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
		{"docs", "issue", "light", "pair"}, {"review", "issue", "standard", "pair"}, {"tests", "schedule", "frontier", "trio"},
		{"review", "runlore", "light", "investigate"}, {"review", "runlore", "frontier", "investigate"},
	} {
		if got := Matrix(c.class, c.source, c.tier); got != c.want {
			t.Errorf("%s/%s/%s = %s, want %s", c.class, c.source, c.tier, got, c.want)
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
	return &config.Config{Maintainers: []string{"Smana"},
		Tiers: map[string]config.Tier{
			"light":    {Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20},
			"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			"frontier": {Model: "agent-default", RunTokens: 4_000_000, TaskTokens: 8_000_000, RunMinutes: 90}},
		Triage:  config.Triage{ClassifierURL: "http://c7.test/v1/classify", ControlPercent: 0},
		Classes: map[string]config.Class{"docs-links": {Live: true}, "revert": {Live: true}, "docs": {}, "tests": {}, "dashboards": {}}}
}

func issueTask() *v1alpha1.Task {
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
		Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "issue"}, Issue: 7, Text: "fix a link", DataClass: "public"}}
}

func TestClassifyUsesAMaintainersClassLabel(t *testing.T) {
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs-links"}})
	f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs-links", At: time.Now()})
	c7 := &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.91, Classifier: "semantic-router", Fallback: "none",
		Shadow: []C7Shadow{{Classifier: "jev", Tier: "standard", Confidence: 0.6}}}}
	d, err := Classify{C: c7, Forge: f}.Triage(context.Background(), testCfg(), issueTask())
	if err != nil {
		t.Fatal(err)
	}
	if d.PredictedClass != "docs-links" || d.Template != "solo" || d.Budget.Tier != "light" || d.Budget.RunTokens != 300_000 ||
		d.Classification.Classifier != "semantic-router" || d.Classification.Confidence != "0.91" || len(d.Classification.Shadow) != 1 {
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

// A class label is read against the task's repository: its classes and its maintainers (v2 D1).
func TestClassifyReadsTheTasksRepository(t *testing.T) {
	cfg := testCfg()
	a := config.Repository{Name: "Smana/a", Maintainers: []string{"Smana"}, Classes: cfg.Classes}
	b := config.Repository{Name: "Smana/b", Maintainers: []string{"Other"}, Classes: map[string]config.Class{"revert": {}}}
	cfg.Maintainers, cfg.Classes, cfg.Repositories = nil, nil, []config.Repository{a, b}
	for name, c := range map[string]struct{ repo, actor, want string }{
		"A's class by A's maintainer": {"Smana/a", "Smana", "docs-links"},
		"A's maintainer on B":         {"Smana/b", "Smana", "review"},
		"a class B does not declare":  {"Smana/b", "Other", "review"},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs-links"}})
			f.SetEvents(7, forge.LabelEvent{Actor: c.actor, Label: "class:docs-links", At: time.Now()})
			tk := issueTask()
			tk.Spec.Repository = c.repo
			d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cfg, tk)
			if err != nil {
				t.Fatal(err)
			}
			if d.PredictedClass != c.want {
				t.Fatalf("class %q, want %q", d.PredictedClass, c.want)
			}
		})
	}
}

// v2 D8: the class label is read on the task's repository: B's issue #7 is not A's.
func TestClassifyReadsTheIssueOnItsRepository(t *testing.T) {
	cfg := testCfg()
	a := config.Repository{Name: "Smana/a", Maintainers: []string{"Smana"}, Classes: cfg.Classes}
	b := config.Repository{Name: "Smana/b", Maintainers: []string{"Smana"}, Classes: cfg.Classes}
	cfg.Maintainers, cfg.Classes, cfg.Repositories = nil, nil, []config.Repository{a, b}
	onA, onB := forgetest.NewFake(), forgetest.NewFake()
	onA.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs-links"}})
	onA.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs-links", At: time.Now()})
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
	tk := issueTask()
	tk.Spec.Repository = "Smana/b"
	d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Clients: clients}.Triage(t.Context(), cfg, tk)
	if err != nil || d.PredictedClass != "review" {
		t.Fatalf("B's issue carries no class: %q %v", d.PredictedClass, err)
	}
	tk.Spec.Repository = "Smana/elsewhere"
	if d, err := (Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Clients: clients}).Triage(t.Context(), cfg, tk); err != nil ||
		d.PredictedClass != "review" {
		t.Fatalf("an unknown repository reads no label: %q %v", d.PredictedClass, err)
	}
}

// MC6: an oracle class's task runs solo on light, without a classifier call or the control group.
func TestAnOracleClassRunsSoloOnLight(t *testing.T) {
	cfg := testCfg()
	cfg.Triage.ControlPercent = 100
	cfg.Classes["dead-links"] = config.Class{Shadow: true, Oracle: &config.Oracle{Probe: "dead-links", Workflow: "dead-links.yaml", MaxFindings: 10}}
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

// An unset triage.classifierURL means no C7 service: Triage skips the call and takes the static fallback.
func TestClassifyWithoutAClassifierURLMakesNoCall(t *testing.T) {
	cfg := testCfg()
	cfg.Triage.ClassifierURL = ""
	c7 := &fakeC7{resp: C7Response{Tier: "light", Classifier: "semantic-router"}}
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 7})
	d, err := Classify{C: c7, Forge: f}.Triage(t.Context(), cfg, issueTask())
	if err != nil {
		t.Fatal(err)
	}
	if c7.got != (C7Request{}) {
		t.Fatalf("C7 was called: %+v", c7.got)
	}
	if d.Classification.Tier != "standard" || d.Classification.Classifier != "static" || d.Classification.Fallback != "static" {
		t.Fatalf("%+v", d.Classification)
	}
}

// RA4, R3: an internal principal's task takes investigate on internal data whatever its issue says;
// a sponsored principal's task, and an api task of a principal the config does not declare, do not.
func TestInternalPrincipalTasksInvestigate(t *testing.T) {
	cfg := testCfg()
	cfg.Repositories = []config.Repository{{Name: "Smana/cloud-native-ref", Maintainers: []string{"Smana"}, Classes: cfg.Classes}}
	cfg.API.Sponsored = []config.Sponsored{{Name: "slack-app"}}
	cfg.API.InternalPrincipals = []config.InternalPrincipal{{Sponsored: config.Sponsored{Name: "runlore"}}}
	for name, c := range map[string]struct {
		by                  string
		template, class, dc string
	}{
		"an internal principal":   {"system:runlore", "investigate", "review", "internal"},
		"a sponsored principal":   {"system:slack-app", "pair", "docs", "public"},
		"an undeclared principal": {"system:ghost", "pair", "docs", "public"},
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

// K5: a task with an origin keeps the class the factory inferred at publish, unless a maintainer's
// class: label names another. The factory's own label never counts.
func TestAnOriginTaskKeepsItsInferredClass(t *testing.T) {
	cfg := testCfg()
	cfg.Classes["alert-rules"] = config.Class{}
	for name, c := range map[string]struct {
		predicted, origin, label, actor, want string
	}{
		"the factory's own label":    {"alert-rules", "3buqdlot", "class:alert-rules", "ogenki-agent-factory[bot]", "alert-rules"},
		"a maintainer's other label": {"alert-rules", "3buqdlot", "class:docs", "Smana", "docs"},
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

// TE: a class a maintainer or a schedule names is declared; an inferred one is not.
func TestPredictClassReportsDeclared(t *testing.T) {
	sched := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "schedule"}, PredictedClass: "tests"}}
	for name, c := range map[string]struct {
		actor    string
		task     *v1alpha1.Task
		want     string
		declared bool
	}{
		"a maintainer's label":     {"Smana", issueTask(), "docs", true},
		"a non-maintainer's label": {"someone", issueTask(), "review", false},
		"a schedule":               {"", sched, "tests", true},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs"}})
			f.SetEvents(7, forge.LabelEvent{Actor: c.actor, Label: "class:docs", At: time.Now()})
			class, declared, err := Classify{Forge: f}.predictClass(t.Context(), testCfg(), c.task)
			if err != nil || class != c.want || declared != c.declared {
				t.Fatalf("(%q, %v, %v), want (%q, %v)", class, declared, err, c.want, c.declared)
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

// P8: a maintainer's class: label names the class, never the cohort: the task is hashed into
// control or treatment like any other.
func TestClassLabelJoinsCohorts(t *testing.T) {
	for percent, want := range map[int]string{100: CohortControl, 0: CohortTreatment} {
		f := forgetest.NewFake()
		f.SetIssue(forge.Issue{Number: 7, Labels: []string{"class:docs"}})
		f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "class:docs", At: time.Now()})
		cf := testCfg()
		cf.Evaluation.ControlPercent = percent
		d, err := Classify{C: &fakeC7{resp: C7Response{Tier: "light", Confidence: 0.9}}, Forge: f}.Triage(t.Context(), cf, issueTask())
		if err != nil || d.PredictedClass != "docs" || !d.Declared || d.Pinned || d.Cohort != want {
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
