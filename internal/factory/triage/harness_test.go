// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

func TestHarnessSelectionPrecedence(t *testing.T) {
	cfg := &config.Config{
		Defaults:   config.Defaults{Harness: config.HarnessOpenHands},
		Evaluation: config.Evaluation{TreatmentHarness: config.HarnessOpenCode},
	}
	tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: "o/r"}}

	// 1. Maintainer pin beats treatmentHarness and defaults.harness.
	h, src := HarnessFor(cfg, tk, Pin{Name: config.HarnessOpenHands}, CohortTreatment)
	if h != config.HarnessOpenHands || src != HarnessSourceLabeled {
		t.Fatalf("pin: got (%q, %q), want (openhands, labeled)", h, src)
	}
	// 2. Treatment cohort uses treatmentHarness when unpinned.
	h, src = HarnessFor(cfg, tk, Pin{}, CohortTreatment)
	if h != config.HarnessOpenCode || src != HarnessSourceTreatment {
		t.Fatalf("treatment: got (%q, %q), want (opencode, treatment)", h, src)
	}
	// 3. Control cohort ignores treatmentHarness and uses defaults.harness.
	h, src = HarnessFor(cfg, tk, Pin{}, CohortControl)
	if h != config.HarnessOpenHands || src != HarnessSourceDefault {
		t.Fatalf("control: got (%q, %q), want (openhands, default)", h, src)
	}
	// 4. defaults.harness=opencode applies when treatmentHarness is unset.
	cfgDef := &config.Config{Defaults: config.Defaults{Harness: config.HarnessOpenCode}}
	h, src = HarnessFor(cfgDef, tk, Pin{}, CohortControl)
	if h != config.HarnessOpenCode || src != HarnessSourceDefault {
		t.Fatalf("default opencode: got (%q, %q), want (opencode, default)", h, src)
	}
	// 5. Unset defaults fall back to openhands.
	h, src = HarnessFor(&config.Config{}, tk, Pin{}, CohortTreatment)
	if h != config.HarnessOpenHands || src != HarnessSourceDefault {
		t.Fatalf("fallback: got (%q, %q), want (openhands, default)", h, src)
	}
	// 6. Integration-only (no-model) tasks are forced to OpenHands even if pinned/defaulted to opencode.
	integ := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Kind: "integration", Repository: "o/r"}}
	h, src = HarnessFor(cfgDef, integ, Pin{Name: config.HarnessOpenCode}, CohortTreatment)
	if h != config.HarnessOpenHands || src != HarnessSourceDefault {
		t.Fatalf("integration: got (%q, %q), want (openhands, default)", h, src)
	}
}

func TestResolveHarnessPin(t *testing.T) {
	cfg := &config.Config{Repositories: []config.Repository{{Name: "o/r", Maintainers: []string{"smana"}}}}

	if p := ResolveHarnessPin(cfg, "o/r", []LabelPin{{Name: "opencode", Actor: "smana"}}, "", ""); !p.Honoured() || p.Name != "opencode" {
		t.Fatalf("maintainer label: %+v", p)
	}
	if p := ResolveHarnessPin(cfg, "o/r", []LabelPin{{Name: "opencode", Actor: "stranger"}}, "", ""); p.Refused != intake.NotMaintainer {
		t.Fatalf("non-maintainer label: %+v", p)
	}
	if p := ResolveHarnessPin(cfg, "o/r", []LabelPin{{Name: "bogus", Actor: "smana"}}, "", ""); p.Refused != intake.ProfileUnknown {
		t.Fatalf("unknown harness: %+v", p)
	}
	if p := ResolveHarnessPin(cfg, "o/r", []LabelPin{{Name: "openhands", Actor: "smana"}, {Name: "opencode", Actor: "smana"}}, "", ""); p.Refused != intake.ProfileConflict {
		t.Fatalf("conflicting labels: %+v", p)
	}
	if p := ResolveHarnessPin(cfg, "o/r", nil, "opencode", "smana"); !p.Honoured() || p.Name != "opencode" {
		t.Fatalf("intake pin: %+v", p)
	}
}

// A maintainer's model:<alias> label pins the implementer and the reviewer to a gateway alias (Gemini
// plan A7): one alias, a maintainer's, one the factory knows, and a provider alias on a public task only.
func TestResolveModelPin(t *testing.T) {
	cfg := &config.Config{Repositories: []config.Repository{{Name: "o/r", Maintainers: []string{"smana"}}}}
	for name, c := range map[string]struct {
		labels    []LabelPin
		dataClass string
		want      Pin
	}{
		"a maintainer's provider alias on a public task": {[]LabelPin{{Name: "gemini-standard", Actor: "smana"}}, "public", Pin{Name: "gemini-standard"}},
		"a maintainer's tier name on an internal task":   {[]LabelPin{{Name: "tier-light", Actor: "smana"}}, "internal", Pin{Name: "tier-light"}},
		"an outsider's label":                            {[]LabelPin{{Name: "gemini-standard", Actor: "stranger"}}, "public", Pin{Name: "gemini-standard", Refused: intake.NotMaintainer}},
		"an alias the factory does not know":             {[]LabelPin{{Name: "gpt-9", Actor: "smana"}}, "public", Pin{Name: "gpt-9", Refused: intake.ModelUnknown}},
		"a provider alias on an internal task":           {[]LabelPin{{Name: "gemini-light", Actor: "smana"}}, "internal", Pin{Name: "gemini-light", Refused: intake.ModelPublicOnly}},
		"two aliases":                                    {[]LabelPin{{Name: "gemini-light", Actor: "smana"}, {Name: "mistral-light", Actor: "smana"}}, "public", Pin{Name: "gemini-light", Refused: intake.ProfileConflict}},
		"no label":                                       {nil, "public", Pin{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ResolveModelPin(cfg, "o/r", c.labels, c.dataClass); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
