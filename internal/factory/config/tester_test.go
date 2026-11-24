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
	if !c.TestFirstIn(r, "careful") || c.TestFirstIn(r, "standard") || c.TestFirstIn(r, "quick") || r.Merge.ContractCheck != "Test contract 🧪" {
		t.Fatalf("careful %v, standard %v, contractCheck %q", c.TestFirstIn(r, "careful"), c.TestFirstIn(r, "standard"), r.Merge.ContractCheck)
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

func TestReviewClassIsRefused(t *testing.T) {
	bad := strings.Replace(withTestFirst(t), "dashboards: {}", "dashboards: {}, review: {}", 1)
	if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "review is not a policy") {
		t.Fatalf("review class parsed: %v", err)
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

func TestTheTesterFollowsTheRowKey(t *testing.T) {
	c := mustParse(t, good+`workflows:
  thorough: {template: pair, loop: thorough}
loops:
  thorough: {tests: true}
`)
	r := &Repository{Workflow: RepositoryWorkflow{TestFirst: true}}
	if !c.TestFirstIn(r, "thorough") {
		t.Fatal("a custom workflow whose row sets tests runs no tester")
	}
	if c.TestFirstIn(r, "standard") {
		t.Fatal("standard runs the tester")
	}
	if c.TestFirstIn(&Repository{}, "thorough") {
		t.Fatal("the repository did not opt in")
	}
}

func TestTheShippedCarefulRowSetsTests(t *testing.T) {
	c := mustParse(t, noTemplates)
	if !c.TestFirstIn(&Repository{Workflow: RepositoryWorkflow{TestFirst: true}}, "careful") {
		t.Fatal("careful lost its tester")
	}
}

func TestACarefulRowWithoutTheKeyKeepsTheTesterAndWarns(t *testing.T) {
	c := mustParse(t, good+`loops:
  careful: {scope: scoper}
`)
	if !c.TestFirstIn(&Repository{Workflow: RepositoryWorkflow{TestFirst: true}}, "careful") {
		t.Fatal("a platform's careful row lost the tester in the release that adds the key")
	}
	want := "loops.careful: set tests: true to keep the tester; read by key from the next minor"
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, want) }) {
		t.Fatalf("no warning; got %v", c.Warnings)
	}
}
