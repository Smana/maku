// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// mergePoliciesUpgradeNotes is where every refusal of a removed merge-policy setting points (MR9, MR13).
const mergePoliciesUpgradeNotes = "docs/upgrading.md#merge-policies-removals"

// removedPolicySpecFields maps each field removed from MergePolicy.spec in release N+1 to what
// replaces it (MR9, MR13).
var removedPolicySpecFields = map[string]string{
	"harness":        "removed, set Repository.spec.workflow.default",
	"harnessAllowed": "removed, every workflow the Factory defines may be pinned",
	"escalation":     "removed, set Factory.spec.workflows.<workflow>.escalation",
	"testFirst":      "removed, set Repository.spec.workflow.testFirst",
	"checks":         "removed, set Factory.spec.workflows.<workflow>.checks",
	"live":           "removed, set mode: live on the Repository's mergePolicies binding",
	"shadow":         "removed, set mode: shadow on the Repository's mergePolicies binding",
}

// removedClassFields maps each workflow field removed from a class in release N+1 to its new home.
var removedClassFields = map[string]string{
	"harness":        "removed, set workflow.default on the repository",
	"harnessAllowed": "removed, every workflow the Factory defines may be pinned",
	"escalation":     "removed, set workflows.<workflow>.escalation",
	"testFirst":      "removed, set workflow.testFirst on the repository",
	"checks":         "removed, set workflows.<workflow>.checks",
}

// RefusePolicySpec reports any removed field present on a raw MergePolicy spec map (MR9, MR13).
func RefusePolicySpec(spec map[string]any) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(spec)) {
		if msg, ok := removedPolicySpecFields[k]; ok {
			out = append(out, fmt.Sprintf("%s: %s (%s)", k, msg, mergePoliciesUpgradeNotes))
		}
	}
	return out
}

// refuseRemovedMergePolicies names each merge-policy setting release N deprecated and release N+1
// refuses (MR9, MR13), with what to write instead.
func refuseRemovedMergePolicies(top map[string]json.RawMessage) []error {
	var errs []error
	removed := func(path, instead string) {
		errs = append(errs, fmt.Errorf("%s: %s (%s)", path, instead, mergePoliciesUpgradeNotes))
	}
	var block map[string]json.RawMessage
	if json.Unmarshal(top["mergeDefaults"], &block) == nil {
		if _, ok := block["autoMergesPerDay"]; ok {
			removed("mergeDefaults.autoMergesPerDay", "renamed mergeDefaults.maxPerDay")
		}
	}
	block = nil
	if json.Unmarshal(top["mergeGuardrails"], &block) == nil {
		if _, ok := block["minPromotionEvidence"]; ok {
			removed("mergeGuardrails.minPromotionEvidence", "removed, set evidence (low or standard) on each MergePolicy")
		}
	}
	block = nil
	if json.Unmarshal(top["policyManagement"], &block) == nil {
		for _, k := range []string{"approvalMemory", "dualEvaluation", "promotion", "read", "repositoryAuthority", "simulation"} {
			if _, ok := block[k]; ok {
				removed("policyManagement."+k, "removed, set policyManagement.stage (read, simulate or manage)")
			}
		}
	}
	var repos []struct {
		Name    string                                `json:"name"`
		Merge   map[string]json.RawMessage            `json:"merge"`
		Classes map[string]map[string]json.RawMessage `json:"classes"`
	}
	if json.Unmarshal(top["repositories"], &repos) == nil {
		for i, r := range repos {
			prefix := fmt.Sprintf("repositories[%d]", i)
			if r.Name != "" {
				prefix = fmt.Sprintf("repositories[%s]", r.Name)
			}
			if _, ok := r.Merge["autoMergesPerDay"]; ok {
				removed(prefix+".merge.autoMergesPerDay", "renamed merge.maxPerDay")
			}
			for _, cname := range slices.Sorted(maps.Keys(r.Classes)) {
				cl := r.Classes[cname]
				for _, f := range []string{"checks", "escalation", "harness", "harnessAllowed", "testFirst"} {
					if _, ok := cl[f]; ok {
						removed(fmt.Sprintf("%s.classes.%s.%s", prefix, cname, f), removedClassFields[f])
					}
				}
			}
		}
	}
	return errs
}

// readWorkflows moves workflows into HarnessProfiles, the name every reader reads (WF9).
func (c *Config) readWorkflows() {
	c.HarnessProfiles, c.Workflows = c.Workflows, nil
}
