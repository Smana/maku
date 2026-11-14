// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"maps"
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
		if b.Mode == ModeOff { // disabled is its replacement (MR9)
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
	for _, d := range c.workflowDeprecations() {
		out = append(out, d.path)
	}
	slices.Sort(out)
	return out
}

// readWorkflows moves workflows into HarnessProfiles, its old name, which every reader keeps
// reading (WF9). A file that sets both is refused: neither may silently win.
func (c *Config) readWorkflows() error {
	c.harnessProfilesKey = len(c.HarnessProfiles) > 0
	if len(c.Workflows) == 0 {
		return nil
	}
	if c.harnessProfilesKey {
		return errors.New("workflows and harnessProfiles: set one; harnessProfiles is workflows' old name (WF9)")
	}
	c.HarnessProfiles, c.Workflows = c.Workflows, nil
	return nil
}

// deprecation is one setting release N+1 refuses (WF9, WF13): its path and what to do instead.
type deprecation struct{ path, instead string }

// workflowDeprecations are the settings of the workflows design that the next minor refuses, in
// the paths its refusals name: templates.trio, triage.classifierURL, defaults.template,
// defaults.tier, harnessProfiles, and each loops row named neither default nor a workflow. Read
// after the shipped workflows are filled, so a row named by one is no row to rename.
func (c *Config) workflowDeprecations() []deprecation {
	var out []deprecation
	if _, ok := c.Templates["trio"]; ok {
		out = append(out, deprecation{"templates.trio", "trio has pair's roles since the tester left it: name pair"})
	}
	if c.Triage.ClassifierURL != "" {
		out = append(out, deprecation{"triage.classifierURL", "the task classifier was never deployed and the scoper replaced it: remove the key"})
	}
	if c.Defaults.Template != "" {
		out = append(out, deprecation{"defaults.template", "a workflow names its template: remove the key"})
	}
	if c.Defaults.Tier != "" {
		out = append(out, deprecation{"defaults.tier", "a workflow names its tier: remove the key"})
	}
	if c.harnessProfilesKey {
		out = append(out, deprecation{"harnessProfiles", "renamed workflows: move the map under workflows"})
	}
	for _, name := range slices.Sorted(maps.Keys(c.Loops)) {
		if _, ok := c.HarnessProfiles[name]; !ok && name != "default" {
			out = append(out, deprecation{"loops." + name, "a row is named default or a workflow: rename it after a workflow and set that workflow's loop"})
		}
	}
	return out
}
