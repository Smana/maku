// SPDX-License-Identifier: Apache-2.0

package config

import (
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
