// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"strings"
	"testing"
)

func validPolicy() PolicyFact {
	return PolicyFact{Class: "docs-links", Mode: PolicyLive, ApproveToMerge: true,
		Conditions: &PolicyConditions{AllowedFiles: []string{`^docs/`}, DiffShape: "links-only", MaxPatchFiles: 5,
			AdoptLogins: []string{"renovate[bot]"}},
		Classes:   []PolicyClass{{Name: "docs-links", Mode: PolicyLive}, {Name: "deps-patch", Mode: PolicyShadow, Adopted: true}},
		MergedHow: "approved"}
}

func TestPolicyFactValidate(t *testing.T) {
	many := make([]PolicyClass, MaxPolicyClasses+1)
	for i := range many {
		many[i] = PolicyClass{Name: "c", Mode: PolicyLive}
	}
	nine := make([]string, MaxPolicyList+1)
	for i := range nine {
		nine[i] = "^a"
	}
	for _, tc := range []struct {
		name string
		edit func(*PolicyFact)
		ok   bool
	}{
		{"a live policy is valid", func(*PolicyFact) {}, true},
		{"a policy with no class and gate off is valid", func(p *PolicyFact) { *p = PolicyFact{Mode: PolicyUnarmed, GateOff: true} }, true},
		{"a mode outside the enum is refused", func(p *PolicyFact) { p.Mode = "armed" }, false},
		{"a class name outside the charset is refused", func(p *PolicyFact) { p.Class = "Bad_Name" }, false},
		{"more classes than the bound are refused", func(p *PolicyFact) { p.Classes = many }, false},
		{"a class row with a bad mode is refused", func(p *PolicyFact) { p.Classes[0].Mode = "x" }, false},
		{"a class row with a bad name is refused", func(p *PolicyFact) { p.Classes[0].Name = "-x" }, false},
		{"a list over the bound is refused", func(p *PolicyFact) { p.Conditions.AllowedFiles = nine }, false},
		{"an entry over the rune bound is refused", func(p *PolicyFact) { p.Conditions.ProtectedFiles = []string{strings.Repeat("é", MaxPolicyEntry+1)} }, false},
		{"a mergedHow outside the enum is refused", func(p *PolicyFact) { p.MergedHow = "auto" }, false},
		{"an adopted login that is not a login is refused", func(p *PolicyFact) { p.Conditions.AdoptLogins = []string{"not a login"} }, false},
		{"a negative cap is refused", func(p *PolicyFact) { p.Conditions.MaxPerDay = -1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := validPolicy()
			tc.edit(&p)
			if err := p.validate(); (err == nil) != tc.ok {
				t.Fatalf("validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestTaskFactsValidateNamesThePolicy(t *testing.T) {
	err := TaskFacts{Phase: "Implementing", Policy: &PolicyFact{Mode: "armed"}}.Validate()
	if err == nil || !strings.HasPrefix(err.Error(), "task facts: policy: ") {
		t.Fatalf("Validate() = %v, want a task facts: policy: error", err)
	}
	if err := (TaskFacts{Phase: "Implementing", Policy: new(validPolicy())}).Validate(); err != nil {
		t.Fatalf("Validate() of a valid policy = %v", err)
	}
}

func validProfileFact() ProfileFact {
	return ProfileFact{Name: "review-frontier", Source: "scoper", Cohort: "treatment",
		Recommended: "review-frontier", Reason: "observe", Why: "The change spans several files."}
}

func TestProfileFactValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProfileFact)
		ok   bool
	}{
		{"every field is valid", func(*ProfileFact) {}, true},
		{"only the required fields are valid", func(p *ProfileFact) { p.Recommended, p.Reason, p.Why = "", "", "" }, true},
		{"a name is required", func(p *ProfileFact) { p.Name = "" }, false},
		{"the name is bounded", func(p *ProfileFact) { p.Name = strings.Repeat("n", 64) }, false},
		{"the source is an enum", func(p *ProfileFact) { p.Source = "model" }, false},
		{"the cohort is an enum", func(p *ProfileFact) { p.Cohort = "experiment" }, false},
		{"the recommendation is bounded", func(p *ProfileFact) { p.Recommended = strings.Repeat("r", 64) }, false},
		{"the reason is bounded", func(p *ProfileFact) { p.Reason = strings.Repeat("r", 33) }, false},
		{"the explanation is bounded", func(p *ProfileFact) { p.Why = strings.Repeat("é", 201) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := validProfileFact()
			tc.edit(&p)
			err := (TaskFacts{Phase: "Implementing", Profile: &p}).Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && !strings.HasPrefix(err.Error(), "task facts: profile: ") {
				t.Fatalf("Validate() = %v, want a task facts: profile: error", err)
			}
		})
	}
}

func TestClip(t *testing.T) {
	nine := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if got, cut := Clip(nine); len(got) != MaxPolicyList || !cut {
		t.Fatalf("Clip(9) = %d entries, cut %v; want %d, true", len(got), cut, MaxPolicyList)
	}
	long := strings.Repeat("é", MaxPolicyEntry+10)
	if got, cut := Clip([]string{long}); len([]rune(got[0])) != MaxPolicyEntry || !cut {
		t.Fatalf("Clip(long) = %d runes, cut %v", len([]rune(got[0])), cut)
	}
	if got, cut := Clip(nil); got != nil || cut {
		t.Fatalf("Clip(nil) = %v, %v", got, cut)
	}
	in := []string{"a"}
	if got, _ := Clip(in); &got[0] == &in[0] {
		t.Fatal("Clip returns a copy, not the caller's slice")
	}
}
