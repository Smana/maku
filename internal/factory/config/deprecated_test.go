// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"testing"
)

// MR13: release N reads every field the next release refuses and names it, in a fixed order.
func TestDeprecations(t *testing.T) {
	if got := PolicyDeprecations(PolicySpec{Class: Class{Harness: "plain", Checks: true}, Requires: []string{"allowedFiles"}}); !slices.Equal(got,
		[]string{"checks", "harness", "requires"}) {
		t.Errorf("policy: %q", got)
	}
	all := PolicySpec{Class: Class{Harness: "p", HarnessAllowed: []string{"q"}, Escalation: &Escalation{}, TestFirst: true, Checks: true},
		Requires: []string{"allowedFiles"}}
	if got := PolicyDeprecations(all); !slices.Equal(got, []string{"checks", "escalation", "harness", "harnessAllowed", "requires", "testFirst"}) {
		t.Errorf("every policy field: %q", got)
	}
	r := &Repository{Merge: Merge{AutoMergesPerDay: 2},
		Bindings: []Binding{{Name: "docs", Mode: "off"}, {Name: "revert", Mode: ModeLive}, {Name: "tests", Mode: ModeDisabled}}}
	if got := RepositoryDeprecations(r); !slices.Equal(got, []string{"merge.autoMergesPerDay", "mergePolicies[docs].mode: off"}) {
		t.Errorf("repository: %q", got)
	}
	// A cap taken from the Factory's mergeDefaults is the Factory's to rename, not the repository's.
	if got := RepositoryDeprecations(&Repository{Merge: Merge{AutoMergesPerDay: 2}, MergeDefaulted: []string{"autoMergesPerDay"}}); got != nil {
		t.Errorf("a defaulted cap: %q", got)
	}
	c := &Config{PolicyManagement: PolicyManagement{Read: true, Promotion: true},
		MergeGuardrails: MergeGuardrails{MinPromotionEvidence: 20}, MergeDefaults: &Merge{AutoMergesPerDay: 5}}
	if got := FactoryDeprecations(c); !slices.Equal(got, []string{"mergeDefaults.autoMergesPerDay", "mergeGuardrails.minPromotionEvidence",
		"policyManagement.promotion", "policyManagement.read"}) {
		t.Errorf("factory: %q", got)
	}
	if PolicyDeprecations(PolicySpec{Class: Class{MaxPerDay: 2}}) != nil || RepositoryDeprecations(&Repository{Merge: Merge{MaxPerDay: 2}}) != nil ||
		FactoryDeprecations(&Config{PolicyManagement: PolicyManagement{Stage: StageRead}}) != nil || FactoryDeprecations(nil) != nil {
		t.Error("clean objects name nothing")
	}
}
