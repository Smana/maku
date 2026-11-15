// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// WF6, MR9: a schedule sets workflow, and names class only when that policy has an oracle.probe.
func TestScheduleClassNeedsAProbe(t *testing.T) {
	_, err := Parse([]byte(repoWith(`schedules: [{name: s, cron: "0 7 * * *", class: docs-links, text: x}]`)))
	if err == nil || !strings.Contains(err.Error(), "oracle.probe") {
		t.Fatalf("schedule with non-probe class: got %v, want oracle.probe refusal", err)
	}
	if _, err := Parse([]byte(repoWith(`schedules: [{name: s, cron: "0 7 * * *", workflow: quick, text: x}]`))); err != nil {
		t.Fatalf("schedule with workflow only: %v", err)
	}
	withOracle := strings.Replace(clean, "revert: {shadow: true}",
		`revert: {shadow: true}, dead-links: {shadow: true, allowedFiles: ["^docs/.+$"], holdoutChecks: ["Pre-commit checks 🛃"], diffShape: links-only, modifyOnly: true, maxModifiedLines: 20, oracle: {probe: dead-links, workflow: dead-links.yaml, maxFindings: 10}}`, 1)
	withOracle = strings.Replace(withOracle, "    triggerLabel: factory/ready\n",
		"    triggerLabel: factory/ready\n    schedules: [{name: dl, cron: \"0 7 * * *\", class: dead-links, probe: dead-links, probeAuthor: \"renovate[bot]\", text: x}]\n", 1)
	if _, err := Parse([]byte(withOracle)); err != nil {
		t.Fatalf("schedule with oracle.probe policy: %v", err)
	}
}

// MR9, MR13: every merge-policy field release N deprecated is refused in config files with its new home.
func TestRemovedMergePolicySettingsRefused(t *testing.T) {
	base := strings.Replace(clean, "autoMergesPerDay: 10", "maxPerDay: 10", 1)
	for y, want := range map[string]string{
		"mergeDefaults: {autoMergesPerDay: 2}\n":          "mergeDefaults.autoMergesPerDay: renamed mergeDefaults.maxPerDay",
		"mergeGuardrails: {minPromotionEvidence: 10}\n":   "mergeGuardrails.minPromotionEvidence: removed",
		"policyManagement: {read: true}\n":                "policyManagement.read: removed",
		"policyManagement: {simulation: true}\n":          "policyManagement.simulation: removed",
		"policyManagement: {dualEvaluation: true}\n":      "policyManagement.dualEvaluation: removed",
		"policyManagement: {repositoryAuthority: true}\n": "policyManagement.repositoryAuthority: removed",
		"policyManagement: {approvalMemory: true}\n":      "policyManagement.approvalMemory: removed",
		"policyManagement: {promotion: true}\n":           "policyManagement.promotion: removed",
	} {
		if _, err := Parse([]byte(base + y)); !removedIn(err, want) {
			t.Errorf("%s: got %v, want %q", y, err, want)
		}
	}
	raw := strings.Replace(base, "maxPerDay: 10", "autoMergesPerDay: 10", 1)
	if _, err := Parse([]byte(raw)); !removedIn(err, "merge.autoMergesPerDay: renamed merge.maxPerDay") {
		t.Errorf("autoMergesPerDay: got %v", err)
	}
	for field, want := range map[string]string{
		"harness: standard":                   "harness: removed",
		"harnessAllowed: [careful]":           "harnessAllowed: removed",
		"escalation: {notConverged: careful}": "escalation: removed",
		"testFirst: true":                     "testFirst: removed",
		"checks: true":                        "checks: removed",
	} {
		raw := strings.Replace(base, "revert: {shadow: true}", "revert: {shadow: true, "+field+"}", 1)
		if _, err := Parse([]byte(raw)); !removedIn(err, want) {
			t.Errorf("class %s: got %v, want %q", field, err, want)
		}
	}
}
