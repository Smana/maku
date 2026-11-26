// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/triage"
)

func TestTreatmentHarnessDoesNotChangeWorkflow(t *testing.T) {
	base := profileCfg(true)
	base.Evaluation.ControlPercent = 0
	base.Evaluation.TreatmentHarness = config.HarnessOpenCode

	ctrlCfg := profileCfg(true)
	ctrlCfg.Evaluation.ControlPercent = 100
	ctrlCfg.Evaluation.TreatmentHarness = config.HarnessOpenCode

	gTreat := profileRig(t, base, issueTask("3buqdlot", 7, "fix it"), "")
	tkTreat := gTreat.reconcile(t, "3buqdlot", 1)

	gCtrl := profileRig(t, ctrlCfg, issueTask("3buqdlot", 7, "fix it"), "")
	tkCtrl := gCtrl.reconcile(t, "3buqdlot", 1)

	if tkTreat.Status.Profile.Harness != config.HarnessOpenCode || tkTreat.Status.Profile.HarnessSource != triage.HarnessSourceTreatment {
		t.Fatalf("treatment profile: %+v", tkTreat.Status.Profile)
	}
	if tkCtrl.Status.Profile.Harness != config.HarnessOpenHands || tkCtrl.Status.Profile.HarnessSource != triage.HarnessSourceDefault {
		t.Fatalf("control profile: %+v", tkCtrl.Status.Profile)
	}
	if tkTreat.Spec.Template != tkCtrl.Spec.Template || tkTreat.Spec.Search != tkCtrl.Spec.Search ||
		tkTreat.Spec.ScopeMode != tkCtrl.Spec.ScopeMode || tkTreat.Spec.Budget != tkCtrl.Spec.Budget ||
		tkTreat.Status.Profile.Name != tkCtrl.Status.Profile.Name || tkTreat.Status.Profile.Checks != tkCtrl.Status.Profile.Checks {
		t.Fatalf("treatment harness changed workflow:\ntreat=%+v %+v\nctrl =%+v %+v",
			tkTreat.Spec, tkTreat.Status.Profile, tkCtrl.Spec, tkCtrl.Status.Profile)
	}
}

func TestHarnessLabelNeedsAMaintainer(t *testing.T) {
	cf := profileCfg(true)
	cf.Evaluation.ControlPercent = 100
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it\n\nAcceptance: tests pass"), "")
	g.f.SetIssue(forge.Issue{Number: 7, Labels: []string{"harness:opencode"}})
	g.f.SetEvents(7, forge.LabelEvent{Actor: "outsider", Label: "harness:opencode", At: now})

	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Profile.Harness != config.HarnessOpenHands || tk.Status.Profile.HarnessSource != triage.HarnessSourceDefault {
		t.Fatalf("outsider harness label should fall back to default: %+v", tk.Status.Profile)
	}
	comments := g.f.Comments(7)
	var narrated bool
	for _, c := range comments {
		if strings.Contains(c, "harness `opencode`") && strings.Contains(c, "Only a maintainer") {
			narrated = true
		}
	}
	if !narrated {
		t.Fatalf("expected non-maintainer harness refusal narration, got comments: %v", comments)
	}
}

func TestEveryRoleGetsTheFrozenHarnessAndRecordExistingReadsClaim(t *testing.T) {
	cf := profileCfg(true)
	cf.Evaluation.ControlPercent = 0
	cf.Evaluation.TreatmentHarness = config.HarnessOpenCode
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it\n\nAcceptance: tests pass"), "")
	// Reconcile through triage -> room -> queued -> first run (scoper triager).
	tk := g.reconcile(t, "3buqdlot", 4)
	if len(tk.Status.Runs) == 0 {
		t.Fatalf("expected a started run, phase=%s", tk.Status.Phase)
	}
	if got := tk.Status.Runs[0].Provenance.Harness; got != config.HarnessOpenCode {
		t.Fatalf("run provenance harness = %q, want opencode", got)
	}
	if tk.Status.Runs[0].Provenance.IsZero() {
		t.Fatal("RunProvenance with Harness must not be zero")
	}
	// Verify claim spec.harness matches.
	if spec := g.runs.specs[tk.Status.Runs[0].ID]; spec.Harness != config.HarnessOpenCode {
		t.Fatalf("created run spec harness = %q, want opencode", spec.Harness)
	}
	// Verify recordExisting reads harness from the claim rather than re-resolving config.
	if err := g.r.recordExisting(t.Context(), cf, tk, runs.Run{
		ID: "bbbbbbbb", TaskID: tk.Name, Role: "reviewer", Harness: config.HarnessOpenHands, Phase: "Running",
	}); err != nil {
		t.Fatal(err)
	}
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	if last.Provenance.Harness != config.HarnessOpenHands {
		t.Fatalf("recordExisting harness = %q, want openhands from claim", last.Provenance.Harness)
	}
	// Verify integration run is forced to openhands regardless of Profile.Harness.
	integ := &v1alpha1.Task{
		Spec:   v1alpha1.TaskSpec{Kind: "integration"},
		Status: v1alpha1.TaskStatus{Profile: &v1alpha1.ProfileStatus{Harness: config.HarnessOpenCode}},
	}
	if got := harnessFor(integ, runs.Spec{CoordinationManifest: "{}"}); got != config.HarnessOpenHands {
		t.Fatalf("integration harness = %q, want openhands", got)
	}
}

// A maintainer's model:<alias> label (Gemini plan A7) freezes the alias on the profile and keeps the
// task out of the evaluation cohorts; an outsider's is narrated and runs the task unpinned.
func TestModelLabelPin(t *testing.T) {
	for name, c := range map[string]struct {
		actor, model, cohort, said string
	}{
		"a maintainer's": {"smana", "gemini-standard", triage.CohortLabeled, ""},
		"an outsider's":  {"outsider", "", triage.CohortControl, "Only a maintainer of this repository can choose the model"},
	} {
		t.Run(name, func(t *testing.T) {
			cf := profileCfg(true)
			cf.Evaluation.ControlPercent = 100
			g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it\n\nAcceptance: tests pass"), "")
			g.f.SetIssue(forge.Issue{Number: 7, Labels: []string{"model:gemini-standard"}})
			g.f.SetEvents(7, forge.LabelEvent{Actor: c.actor, Label: "model:gemini-standard", At: now})
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Profile.Model != c.model || tk.Status.Profile.Cohort != c.cohort {
				t.Fatalf("profile %+v, want model %q cohort %q", tk.Status.Profile, c.model, c.cohort)
			}
			said := c.said == ""
			for _, m := range g.f.Comments(7) {
				said = said || strings.Contains(m, "model `gemini-standard`") && strings.Contains(m, c.said)
			}
			if !said {
				t.Fatalf("want the refusal narrated: %v", g.f.Comments(7))
			}
		})
	}
}

// evaluation.treatmentModel (Gemini plan A8) moves only the treatment cohort's treatmentRoles onto
// the alias, frozen on the profile; workflow, template, harness and caps stay the control's, and a
// provider alias leaves an internal task on its tier's model.
func TestTreatmentModelChangesOnlyTheModel(t *testing.T) {
	run := func(percent int, dataClass string) *v1alpha1.Task {
		cf := profileCfg(true)
		cf.Evaluation.ControlPercent = percent
		cf.Evaluation.TreatmentModel = "gemini-standard"
		tk := issueTask("3buqdlot", 7, "fix it")
		tk.Spec.DataClass = dataClass
		g := profileRig(t, cf, tk, "")
		return g.reconcile(t, "3buqdlot", 1)
	}
	treat, ctrl, internal := run(0, "public"), run(100, "public"), run(0, "internal")
	p := treat.Status.Profile
	if p.Model != "gemini-standard" || !slices.Equal(p.ModelRoles, []string{"implementer"}) || p.ModelSource != v1alpha1.ModelSourceTreatment || p.Cohort != triage.CohortTreatment {
		t.Fatalf("treatment profile: %+v", p)
	}
	if ctrl.Status.Profile.Model != "" || internal.Status.Profile.Model != "" {
		t.Fatalf("control %+v / internal %+v: want no model", ctrl.Status.Profile, internal.Status.Profile)
	}
	if treat.Spec.Template != ctrl.Spec.Template || treat.Spec.Budget != ctrl.Spec.Budget ||
		p.Name != ctrl.Status.Profile.Name || p.Harness != ctrl.Status.Profile.Harness {
		t.Fatalf("treatment changed more than the model:\ntreat=%+v %+v\nctrl =%+v %+v", treat.Spec, p, ctrl.Spec, ctrl.Status.Profile)
	}
}
