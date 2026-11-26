// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

func TestHarnessConfigValidation(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("base config: %v", err)
	}
	if c.DefaultHarness() != HarnessOpenHands {
		t.Fatalf("default harness = %q, want openhands", c.DefaultHarness())
	}

	okDef := strings.Replace(good, "defaults: {dataClass: public, predictedClass: review}", "defaults: {dataClass: public, predictedClass: review, harness: opencode}", 1)
	if c2, err := Parse([]byte(okDef)); err != nil || c2.DefaultHarness() != HarnessOpenCode {
		t.Fatalf("defaults.harness=opencode: %v %q", err, c2.DefaultHarness())
	}

	badDef := strings.Replace(good, "defaults: {dataClass: public, predictedClass: review}", "defaults: {dataClass: public, predictedClass: review, harness: bogus}", 1)
	if _, err := Parse([]byte(badDef)); err == nil || !strings.Contains(err.Error(), "defaults.harness") {
		t.Fatalf("invalid defaults.harness must fail: %v", err)
	}

	okEval := good + "\nevaluation:\n  treatmentHarness: opencode\n"
	if c3, err := Parse([]byte(okEval)); err != nil || c3.Evaluation.TreatmentHarness != HarnessOpenCode {
		t.Fatalf("evaluation.treatmentHarness=opencode: %v %+v", err, c3.Evaluation)
	}

	badEval := good + "\nevaluation:\n  treatmentHarness: invalid\n"
	if _, err := Parse([]byte(badEval)); err == nil || !strings.Contains(err.Error(), "evaluation.treatmentHarness") {
		t.Fatalf("invalid evaluation.treatmentHarness must fail: %v", err)
	}
}

// evaluation.treatmentModel (Gemini plan A8): an alias the factory knows, on roles that exist, and
// the measurement's one variable: never beside treatmentHarness or a treatment workflow.
func TestTreatmentModelValidation(t *testing.T) {
	ok := good + "\nevaluation:\n  treatmentModel: gemini-standard\n  treatmentRoles: [implementer, reviewer]\n"
	c, err := Parse([]byte(ok))
	if err != nil || c.Evaluation.TreatmentModel != "gemini-standard" || !slices.Equal(c.Evaluation.ModelRoles(), []string{"implementer", "reviewer"}) {
		t.Fatalf("treatmentModel: %v %+v", err, c.Evaluation)
	}
	if c, _ := Parse([]byte(good + "\nevaluation:\n  treatmentModel: gemini-standard\n")); !slices.Equal(c.Evaluation.ModelRoles(), []string{"implementer"}) {
		t.Fatalf("treatmentRoles defaults to the implementer: %+v", c.Evaluation)
	}
	for name, c := range map[string]struct{ eval, want string }{
		"an unknown alias":    {"treatmentModel: gpt-9", "evaluation.treatmentModel"},
		"an unknown role":     {"treatmentModel: gemini-standard\n  treatmentRoles: [merger]", "evaluation.treatmentRoles"},
		"beside a harness":    {"treatmentModel: gemini-standard\n  treatmentHarness: opencode", "one variable"},
		"beside a workflow":   {"treatmentModel: gemini-standard\n  treatment: standard", "one variable"},
		"roles with no model": {"treatmentRoles: [reviewer]", "evaluation.treatmentRoles"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(good + "\nevaluation:\n  " + c.eval + "\n")); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}
