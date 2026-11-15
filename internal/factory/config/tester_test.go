// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// withTestFirst is withChecks plus the repository's tester opt-in and its contract check (WF7).
func withTestFirst(t *testing.T) string {
	t.Helper()
	raw := strings.Replace(withChecks(t, "[pypi]"), "    triggerLabel: factory/ready\n", "    triggerLabel: factory/ready\n    workflow: {testFirst: true}\n", 1)
	return strings.Replace(raw, "    merge:\n", "    merge:\n      contractCheck: \"Test contract 🧪\"\n", 1)
}

// WF7: the tester is the repository's opt-in, and runs in careful only; it needs the contract check.
func TestTestFirstIsTheRepositorysAndNeedsTheContractCheck(t *testing.T) {
	c, err := Parse([]byte(withTestFirst(t)))
	if err != nil {
		t.Fatal(err)
	}
	r := c.RepositoryFor("Smana/cloud-native-ref")
	if !r.TestFirstIn("careful") || r.TestFirstIn("standard") || r.TestFirstIn("quick") || r.Merge.ContractCheck != "Test contract 🧪" {
		t.Fatalf("careful %v, standard %v, contractCheck %q", r.TestFirstIn("careful"), r.TestFirstIn("standard"), r.Merge.ContractCheck)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"no contract check", "      contractCheck: \"Test contract 🧪\"\n", "", "workflow.testFirst needs merge.contractCheck"},
		{"required", "      contractCheck: \"Test contract 🧪\"\n", "      contractCheck: \"Security scanning 🔒\"\n", "must not be one of merge.requiredChecks"},
	} {
		raw := strings.Replace(withTestFirst(t), tc.from, tc.to, 1)
		if raw == withTestFirst(t) {
			t.Fatalf("%s: the edit did not apply", tc.name)
		}
		if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestReviewMayBeDeclaredForChecksAndTestFirstOnly(t *testing.T) {
	ok := strings.Replace(withTestFirst(t), "dashboards: {}", "dashboards: {}, review: {checks: true, testFirst: true}", 1)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.Replace(ok, "review: {checks: true, testFirst: true}", "review: {live: true}", 1))); err == nil ||
		!strings.Contains(err.Error(), "review takes checks and testFirst only") {
		t.Fatalf("a live review parsed: %v", err)
	}
}

func TestATemplateListingTheTesterLoadsWithoutItAndWarns(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(good, "  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}", "  pair: {roles: [implementer, tester, reviewer], maxReviewRounds: 2}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if roles := c.Templates["pair"].Roles; slices.Contains(roles, "tester") {
		t.Fatalf("pair %v still lists the tester", roles)
	}
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "template pair: tester is the test writer") }) {
		t.Fatalf("warnings %v", c.Warnings)
	}
}

func TestALiveClassWithoutHoldoutsWarns(t *testing.T) {
	// policy-bot on: without it a live class needs allowedFiles, which this one lacks.
	withBot := strings.Replace(good, "      policyBotLogin:", "      policyBot: true\n      policyBotLogin:", 1)
	c, err := Parse([]byte(strings.Replace(withBot, "docs-links: {shadow: true}", "docs-links: {live: true}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(c.Warnings, func(w string) bool {
		return strings.Contains(w, "class docs-links of Smana/cloud-native-ref is live without holdoutChecks")
	}) {
		t.Fatalf("warnings %v", c.Warnings)
	}
}
