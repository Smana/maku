// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
)

// ConditionDeprecated names, on a Factory, Repository or MergePolicy, the fields it sets that the
// next release refuses (MR13). True with ReasonRemovedNextRelease and the paths as its message;
// absent when there are none. It never affects Ready.
const (
	ConditionDeprecated      = "Deprecated"
	ReasonRemovedNextRelease = "RemovedNextRelease"
)

// PolicyDeprecations are the deprecated fields a policy sets (MR9), sorted: the workflow fields,
// whose new home is the workflows design, and requires.
func PolicyDeprecations(p PolicySpec) []string {
	var out []string
	for _, f := range []struct {
		path string
		set  bool
	}{
		{"checks", p.Checks},
		{"escalation", p.Escalation != nil},
		{"harness", p.Harness != ""},
		{"harnessAllowed", len(p.HarnessAllowed) > 0},
		{"requires", len(p.Requires) > 0},
		{"testFirst", p.TestFirst},
	} {
		if f.set {
			out = append(out, f.path)
		}
	}
	return out
}

// RepositoryDeprecations are the deprecated fields a repository sets itself (MR9), sorted: its own
// merge.autoMergesPerDay (one taken from the Factory's mergeDefaults is the Factory's) and each
// binding in mode off.
func RepositoryDeprecations(r *Repository) []string {
	var out []string
	if r.Merge.AutoMergesPerDay != 0 && !slices.Contains(r.MergeDefaulted, "autoMergesPerDay") {
		out = append(out, "merge.autoMergesPerDay")
	}
	for _, b := range r.Bindings {
		if b.Mode == "off" {
			out = append(out, fmt.Sprintf("mergePolicies[%s].mode: off", b.Name))
		}
	}
	slices.Sort(out)
	return out
}

// FactoryDeprecations are the deprecated fields of the factory's config and Factory object (MR9),
// sorted: mergeDefaults.autoMergesPerDay, mergeGuardrails.minPromotionEvidence and each of the six
// policyManagement switches stage replaces.
func FactoryDeprecations(c *Config) []string {
	if c == nil {
		return nil
	}
	pm := c.PolicyManagement
	var out []string
	for _, f := range []struct {
		path string
		set  bool
	}{
		{"mergeDefaults.autoMergesPerDay", c.MergeDefaults != nil && c.MergeDefaults.AutoMergesPerDay != 0},
		{"mergeGuardrails.minPromotionEvidence", c.MergeGuardrails.MinPromotionEvidence != 0},
		{"policyManagement.read", pm.Read},
		{"policyManagement.simulation", pm.Simulation},
		{"policyManagement.dualEvaluation", pm.DualEvaluation},
		{"policyManagement.repositoryAuthority", pm.RepositoryAuthority},
		{"policyManagement.approvalMemory", pm.ApprovalMemory},
		{"policyManagement.promotion", pm.Promotion},
	} {
		if f.set {
			out = append(out, f.path)
		}
	}
	slices.Sort(out)
	return out
}
