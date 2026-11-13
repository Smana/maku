// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"slices"
	"strings"
	"testing"
)

// D11: the rules a branch's rulesets apply keep only their required status checks, each with the
// App that must post it; a branch no ruleset covers is no requirement, never an error.
func TestBranchRules(t *testing.T) {
	r := ghFake(t, map[string]string{
		"/repos/Smana/demo/rules/branches/main": `[
		  {"type":"pull_request","ruleset_source_type":"Repository","ruleset_source":"Smana/demo","ruleset_id":1,
		   "parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false}},
		  {"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"Smana/demo","ruleset_id":1,
		   "parameters":{"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"ci","integration_id":15368},{"context":"maku/merge-gate","integration_id":424242},{"context":"policy-bot: main"}]}},
		  {"type":"deletion","ruleset_source_type":"Repository","ruleset_source":"Smana/demo","ruleset_id":1}
		]`,
	})
	got, err := r.g.BranchRules(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []RequiredStatus{{Context: "ci", IntegrationID: 15368}, {Context: "maku/merge-gate", IntegrationID: 424242}, {Context: "policy-bot: main"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if q := strings.Join(r.gh.queries(), " "); !strings.Contains(q, "per_page=100") {
		t.Fatalf("per_page: %q", q)
	}
	// No ruleset on the branch: GitHub's 404 (the fake's for any path it does not serve).
	if got, err := r.g.BranchRules(t.Context(), "release"); err != nil || len(got) != 0 {
		t.Fatalf("a 404 is no rule: %+v %v", got, err)
	}
}

func TestBranchRulesFailsOnAnError(t *testing.T) {
	r := ghFake(t, map[string]string{"/repos/Smana/demo/rules/branches/main": `{not json`})
	if _, err := r.g.BranchRules(t.Context(), "main"); err == nil {
		t.Fatal("a reply that does not parse is an error")
	}
}
